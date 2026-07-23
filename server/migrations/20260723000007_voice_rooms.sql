-- Voice rooms — metadata sesi, bukan audio.
--
-- PENTING: tabel dan fungsi di berkas ini TIDAK mengalirkan suara. Audio
-- realtime tidak bisa dan tidak boleh melewati PostgreSQL maupun proses Go —
-- ia butuh UDP, jitter buffer, echo cancellation, dan koreksi paket hilang.
-- Itu pekerjaan SFU (LiveKit/mediasoup/Janus).
--
-- Yang disimpan di sini: siapa membuat room, siapa boleh masuk, siapa boleh
-- bicara. Backend Go menjadi otoritas peran dan penerbit token; SFU yang
-- membawa suaranya. Lihat docs/voice-rooms.md.

BEGIN;

CREATE TABLE IF NOT EXISTS rooms (
    id          uuid PRIMARY KEY,
    host_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title       text NOT NULL,
    topic       text NOT NULL DEFAULT '',
    visibility  text NOT NULL DEFAULT 'public'
        CHECK (visibility IN ('public', 'followers', 'invite_only')),
    status      text NOT NULL DEFAULT 'live'
        CHECK (status IN ('scheduled', 'live', 'ended')),
    is_recorded boolean NOT NULL DEFAULT false,

    max_participants      integer NOT NULL DEFAULT 50,
    peak_participant_count integer NOT NULL DEFAULT 0,

    -- Jembatan ke media server. Backend tidak pernah menyentuh audio; ia hanya
    -- menerbitkan token yang mengizinkan klien bergabung ke room ini di SFU.
    sfu_room_id text,

    scheduled_at timestamptz,
    started_at   timestamptz NOT NULL DEFAULT now(),
    ended_at     timestamptz
);

CREATE INDEX IF NOT EXISTS rooms_live_idx
    ON rooms (started_at DESC) WHERE status = 'live';

CREATE TABLE IF NOT EXISTS room_participants (
    -- PK sendiri, bukan composite: satu orang bisa keluar-masuk berkali-kali
    -- dalam satu sesi, dan tiap kunjungan perlu tercatat terpisah.
    id       uuid PRIMARY KEY,
    room_id  uuid NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    user_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role     text NOT NULL DEFAULT 'listener'
        CHECK (role IN ('host', 'moderator', 'speaker', 'listener')),
    is_muted boolean NOT NULL DEFAULT true,

    joined_at timestamptz NOT NULL DEFAULT now(),
    left_at   timestamptz
);

-- Satu orang hanya boleh punya SATU kehadiran aktif per room. Tanpa ini,
-- reconnect yang cepat akan menghasilkan peserta hantu yang tidak pernah
-- keluar dan ikut terhitung di daftar.
CREATE UNIQUE INDEX IF NOT EXISTS room_participants_active_idx
    ON room_participants (room_id, user_id) WHERE left_at IS NULL;

CREATE TABLE IF NOT EXISTS room_speak_requests (
    room_id      uuid NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status       text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'denied')),
    requested_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (room_id, user_id)
);

ALTER TABLE rooms               ENABLE ROW LEVEL SECURITY;
ALTER TABLE room_participants   ENABLE ROW LEVEL SECURITY;
ALTER TABLE room_speak_requests ENABLE ROW LEVEL SECURITY;

-- ============================================================
-- FUNGSI
-- ============================================================

CREATE OR REPLACE FUNCTION public.create_room(
    p_id         uuid,
    p_title      text,
    p_topic      text,
    p_visibility text,
    p_sfu_room   text
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF p_title IS NULL OR btrim(p_title) = '' THEN
        RAISE EXCEPTION 'judul room tidak boleh kosong' USING ERRCODE = '22023';
    END IF;

    INSERT INTO rooms (id, host_id, title, topic, visibility, status, sfu_room_id)
    VALUES (p_id, v_user, btrim(p_title), COALESCE(p_topic, ''),
            COALESCE(p_visibility, 'public'), 'live', p_sfu_room);

    -- Pembuat langsung menjadi host dan boleh bicara.
    INSERT INTO room_participants (id, room_id, user_id, role, is_muted)
    VALUES (gen_random_uuid(), p_id, v_user, 'host', false);
END;
$$;

CREATE OR REPLACE FUNCTION public.list_rooms()
RETURNS TABLE (
    id                uuid,
    host_id           uuid,
    host_username     text,
    host_name         text,
    host_avatar       uuid,
    title             text,
    topic             text,
    visibility        text,
    participant_count integer,
    speaker_count     integer,
    max_participants  integer,
    started_at        timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    RETURN QUERY
    SELECT
        r.id,
        r.host_id,
        u.username::text,
        COALESCE(p.display_name, ''),
        p.avatar_media_id,
        r.title,
        r.topic,
        r.visibility,
        COALESCE(cnt.total, 0)::integer,
        COALESCE(cnt.speakers, 0)::integer,
        r.max_participants,
        r.started_at
    FROM rooms r
    JOIN users u ON u.id = r.host_id AND u.deleted_at IS NULL
    LEFT JOIN user_profiles p ON p.user_id = r.host_id
    LEFT JOIN LATERAL (
        SELECT count(*) AS total,
               count(*) FILTER (WHERE rp.role IN ('host', 'moderator', 'speaker')) AS speakers
        FROM room_participants rp
        WHERE rp.room_id = r.id AND rp.left_at IS NULL
    ) cnt ON true
    WHERE r.status = 'live'
      AND (
            r.visibility = 'public'
         OR r.host_id = v_user
         OR (r.visibility = 'followers' AND EXISTS (
                SELECT 1 FROM follows f
                WHERE f.follower_id = v_user AND f.followee_id = r.host_id
                  AND f.status = 'accepted'))
          )
      AND NOT EXISTS (
            SELECT 1 FROM blocks b
            WHERE (b.blocker_id = r.host_id AND b.blocked_id = v_user)
               OR (b.blocker_id = v_user AND b.blocked_id = r.host_id)
          )
    ORDER BY r.started_at DESC;
END;
$$;

-- Bergabung ke room. Mengembalikan peran yang diberikan, yang menentukan
-- apakah klien boleh menerbitkan audio di SFU atau hanya mendengarkan.
CREATE OR REPLACE FUNCTION public.join_room(p_room uuid)
RETURNS TABLE (role text, sfu_room_id text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user    uuid := public.require_auth();
    v_room    rooms%ROWTYPE;
    v_count   integer;
    v_role    text;
BEGIN
    SELECT * INTO v_room FROM rooms WHERE rooms.id = p_room AND status = 'live';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'room tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    IF EXISTS (
        SELECT 1 FROM blocks b
        WHERE (b.blocker_id = v_room.host_id AND b.blocked_id = v_user)
           OR (b.blocker_id = v_user AND b.blocked_id = v_room.host_id)
    ) THEN
        RAISE EXCEPTION 'tidak diizinkan' USING ERRCODE = '42501';
    END IF;

    IF v_room.visibility = 'followers' AND v_room.host_id <> v_user
       AND NOT EXISTS (
            SELECT 1 FROM follows f
            WHERE f.follower_id = v_user AND f.followee_id = v_room.host_id
              AND f.status = 'accepted') THEN
        RAISE EXCEPTION 'room hanya untuk pengikut' USING ERRCODE = '42501';
    END IF;

    SELECT count(*) INTO v_count
    FROM room_participants rp WHERE rp.room_id = p_room AND rp.left_at IS NULL;

    IF v_count >= v_room.max_participants THEN
        RAISE EXCEPTION 'room penuh' USING ERRCODE = '53400';
    END IF;

    v_role := CASE WHEN v_room.host_id = v_user THEN 'host' ELSE 'listener' END;

    INSERT INTO room_participants (id, room_id, user_id, role, is_muted)
    VALUES (gen_random_uuid(), p_room, v_user, v_role, v_role <> 'host')
    ON CONFLICT (room_id, user_id) WHERE left_at IS NULL
    DO UPDATE SET joined_at = now()
    RETURNING room_participants.role INTO v_role;

    UPDATE rooms
    SET peak_participant_count = GREATEST(peak_participant_count, v_count + 1)
    WHERE rooms.id = p_room;

    RETURN QUERY SELECT v_role, v_room.sfu_room_id;
END;
$$;

CREATE OR REPLACE FUNCTION public.leave_room(p_room uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    UPDATE room_participants rp
    SET left_at = now()
    WHERE rp.room_id = p_room AND rp.user_id = v_user AND rp.left_at IS NULL;

    -- Room berakhir bersama host-nya. Tanpa aturan ini, room yang ditinggalkan
    -- akan menumpuk di daftar sebagai "live" selamanya.
    UPDATE rooms r
    SET status = 'ended', ended_at = now()
    WHERE r.id = p_room AND r.host_id = v_user AND r.status = 'live';
END;
$$;

CREATE OR REPLACE FUNCTION public.list_room_participants(p_room uuid)
RETURNS TABLE (
    user_id      uuid,
    username     text,
    display_name text,
    avatar       uuid,
    role         text,
    is_muted     boolean,
    joined_at    timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
BEGIN
    PERFORM public.require_auth();

    RETURN QUERY
    SELECT rp.user_id,
           u.username::text,
           COALESCE(p.display_name, ''),
           p.avatar_media_id,
           rp.role,
           rp.is_muted,
           rp.joined_at
    FROM room_participants rp
    JOIN users u ON u.id = rp.user_id
    LEFT JOIN user_profiles p ON p.user_id = rp.user_id
    WHERE rp.room_id = p_room AND rp.left_at IS NULL
    ORDER BY
        CASE rp.role WHEN 'host' THEN 0 WHEN 'moderator' THEN 1
                     WHEN 'speaker' THEN 2 ELSE 3 END,
        rp.joined_at;
END;
$$;

-- Mengubah peran peserta. Hanya host dan moderator yang boleh.
--
-- Ini otoritas yang menentukan siapa boleh bersuara — SFU akan menolak
-- penerbitan audio dari peserta yang perannya bukan speaker ke atas.
CREATE OR REPLACE FUNCTION public.set_room_role(
    p_room   uuid,
    p_target uuid,
    p_role   text
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF p_role NOT IN ('moderator', 'speaker', 'listener') THEN
        RAISE EXCEPTION 'peran tidak valid' USING ERRCODE = '22023';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM room_participants rp
        WHERE rp.room_id = p_room AND rp.user_id = v_user
          AND rp.left_at IS NULL AND rp.role IN ('host', 'moderator')
    ) THEN
        RAISE EXCEPTION 'hanya host atau moderator' USING ERRCODE = '42501';
    END IF;

    UPDATE room_participants rp
    SET role = p_role, is_muted = (p_role = 'listener')
    WHERE rp.room_id = p_room AND rp.user_id = p_target AND rp.left_at IS NULL;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'peserta tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    UPDATE room_speak_requests rsr
    SET status = CASE WHEN p_role = 'listener' THEN 'denied' ELSE 'approved' END
    WHERE rsr.room_id = p_room AND rsr.user_id = p_target;
END;
$$;

CREATE OR REPLACE FUNCTION public.request_speak(p_room uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM room_participants rp
        WHERE rp.room_id = p_room AND rp.user_id = v_user AND rp.left_at IS NULL
    ) THEN
        RAISE EXCEPTION 'bukan peserta room' USING ERRCODE = '42501';
    END IF;

    INSERT INTO room_speak_requests (room_id, user_id, status)
    VALUES (p_room, v_user, 'pending')
    ON CONFLICT (room_id, user_id) DO UPDATE SET status = 'pending', requested_at = now();
END;
$$;

-- Mengubah status bisu diri sendiri. Peserta yang bukan speaker tidak bisa
-- membunyikan dirinya — itu jalan pintas paling jelas untuk menyela.
CREATE OR REPLACE FUNCTION public.set_room_muted(p_room uuid, p_muted boolean)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_role text;
BEGIN
    SELECT rp.role INTO v_role
    FROM room_participants rp
    WHERE rp.room_id = p_room AND rp.user_id = v_user AND rp.left_at IS NULL;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'bukan peserta room' USING ERRCODE = '42501';
    END IF;

    IF NOT p_muted AND v_role = 'listener' THEN
        RAISE EXCEPTION 'pendengar tidak bisa membunyikan mikrofon' USING ERRCODE = '42501';
    END IF;

    UPDATE room_participants rp
    SET is_muted = p_muted
    WHERE rp.room_id = p_room AND rp.user_id = v_user AND rp.left_at IS NULL;
END;
$$;

-- ============================================================
-- HAK EKSEKUSI
-- ============================================================

DO $$
DECLARE
    fn text;
BEGIN
    FOREACH fn IN ARRAY ARRAY[
        'public.create_room(uuid, text, text, text, text)',
        'public.list_rooms()',
        'public.join_room(uuid)',
        'public.leave_room(uuid)',
        'public.list_room_participants(uuid)',
        'public.set_room_role(uuid, uuid, text)',
        'public.request_speak(uuid)',
        'public.set_room_muted(uuid, boolean)'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO authenticated', fn);
    END LOOP;
END;
$$;

COMMIT;
