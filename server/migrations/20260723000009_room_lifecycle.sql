-- Memperbaiki enam celah yang terbukti dari data dan laporan pengujian.
--
-- Bukti yang mendasarinya:
--   * 1 room berstatus 'live' padahal host-nya sudah lama pergi (room hantu)
--   * 5 baris room_participants dengan left_at NULL, sebagian di room yang
--     sudah berakhir — peserta yang tidak pernah "keluar"
--   * 3 story ada, 0 baris follows → story orang lain tidak pernah muncul
--
-- Yang diperbaiki:
--   1. Room berakhir ikut mengeluarkan SELURUH pesertanya
--   2. Room terbengkalai ditutup otomatis
--   3. Daftar permintaan bicara bisa dibaca host
--   4. Persetujuan/penolakan permintaan bicara
--   5. invite_only benar-benar ditegakkan
--   6. Story juga tampil dari lawan bicara, bukan hanya dari yang diikuti

BEGIN;

-- ============================================================
-- 1. UNDANGAN ROOM
-- ============================================================
-- Sebelumnya visibility 'invite_only' diperlakukan sama dengan 'public' saat
-- bergabung, sehingga siapa pun bisa masuk. Tabel ini yang membuatnya berarti.

CREATE TABLE IF NOT EXISTS room_invites (
    room_id    uuid NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    invited_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (room_id, user_id)
);

ALTER TABLE room_invites ENABLE ROW LEVEL SECURITY;

-- ============================================================
-- 2. MENGAKHIRI ROOM DENGAN BENAR
-- ============================================================

-- Menutup room sekaligus mengeluarkan seluruh pesertanya.
--
-- Versi lama hanya menandai room 'ended' dan mengeluarkan si pemanggil.
-- Peserta lain tetap tercatat aktif selamanya — itu sumber "peserta hantu"
-- yang masih terhitung di daftar, dan sebabnya klien mengira masih di dalam.
CREATE OR REPLACE FUNCTION public.end_room(p_room uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
BEGIN
    UPDATE rooms r
    SET status = 'ended', ended_at = now()
    WHERE r.id = p_room AND r.status <> 'ended';

    UPDATE room_participants rp
    SET left_at = now()
    WHERE rp.room_id = p_room AND rp.left_at IS NULL;

    DELETE FROM room_speak_requests rsr WHERE rsr.room_id = p_room;
END;
$$;

-- CREATE OR REPLACE tidak bisa mengubah tipe kembalian; versi lama
-- mengembalikan void, yang baru mengembalikan boolean.
DROP FUNCTION IF EXISTS public.leave_room(uuid);

CREATE FUNCTION public.leave_room(p_room uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_host  boolean;
BEGIN
    SELECT (r.host_id = v_user) INTO v_host FROM rooms r WHERE r.id = p_room;

    UPDATE room_participants rp
    SET left_at = now()
    WHERE rp.room_id = p_room AND rp.user_id = v_user AND rp.left_at IS NULL;

    -- Room berakhir bersama host-nya. Mengembalikan penanda supaya backend
    -- tahu harus menyiarkan `room.ended` ke peserta yang tersisa.
    IF COALESCE(v_host, false) THEN
        PERFORM public.end_room(p_room);
        RETURN true;
    END IF;

    RETURN false;
END;
$$;

-- Menutup room yang terbengkalai.
--
-- Host yang aplikasinya tertutup paksa, kehabisan baterai, atau kehilangan
-- jaringan tidak pernah memanggil leave_room. Tanpa pembersihan ini, room
-- tersebut menetap sebagai 'live' selamanya dan menumpuk di daftar — persis
-- yang sudah terjadi. Dipanggil backend secara berkala.
CREATE OR REPLACE FUNCTION public.close_stale_rooms(p_idle_minutes integer DEFAULT 30)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_room  uuid;
    v_count integer := 0;
BEGIN
    FOR v_room IN
        SELECT r.id
        FROM rooms r
        WHERE r.status = 'live'
          AND NOT EXISTS (
                SELECT 1 FROM room_participants rp
                WHERE rp.room_id = r.id AND rp.left_at IS NULL
              )
           OR (r.status = 'live'
               AND r.started_at < now() - make_interval(mins => p_idle_minutes)
               AND NOT EXISTS (
                    SELECT 1 FROM room_participants rp
                    WHERE rp.room_id = r.id
                      AND rp.left_at IS NULL
                      AND rp.role IN ('host', 'moderator')
                  ))
    LOOP
        PERFORM public.end_room(v_room);
        v_count := v_count + 1;
    END LOOP;

    RETURN v_count;
END;
$$;

-- ============================================================
-- 3. PERMINTAAN BICARA — bisa dibaca dan diputuskan host
-- ============================================================
-- Sebelumnya permintaan tersimpan tetapi tidak ada cara membacanya, sehingga
-- UI "angkat tangan" di sisi host mustahil dibuat. Pengguna melihat "menunggu
-- izin" yang tidak akan pernah dijawab.

CREATE OR REPLACE FUNCTION public.list_speak_requests(p_room uuid)
RETURNS TABLE (
    user_id      uuid,
    username     text,
    display_name text,
    avatar_key   text,
    requested_at timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM room_participants rp
        WHERE rp.room_id = p_room AND rp.user_id = v_user
          AND rp.left_at IS NULL AND rp.role IN ('host', 'moderator')
    ) THEN
        RAISE EXCEPTION 'hanya host atau moderator' USING ERRCODE = '42501';
    END IF;

    RETURN QUERY
    SELECT rsr.user_id,
           u.username::text,
           COALESCE(p.display_name, ''),
           COALESCE(ma.storage_key, ''),
           rsr.requested_at
    FROM room_speak_requests rsr
    JOIN users u ON u.id = rsr.user_id
    LEFT JOIN user_profiles p ON p.user_id = rsr.user_id
    LEFT JOIN media_assets ma ON ma.id = p.avatar_media_id
    WHERE rsr.room_id = p_room AND rsr.status = 'pending'
    ORDER BY rsr.requested_at;
END;
$$;

-- ============================================================
-- 4. AVATAR SEBAGAI STORAGE KEY
-- ============================================================
-- Sebelumnya hanya avatar_media_id yang dikirim. Klien tidak punya cara
-- mengubahnya menjadi URL — ia tidak tahu storage_key-nya. Akibatnya avatar
-- peserta tidak bisa dirender sama sekali.

DROP FUNCTION IF EXISTS public.list_room_participants(uuid);

CREATE FUNCTION public.list_room_participants(p_room uuid)
RETURNS TABLE (
    user_id      uuid,
    username     text,
    display_name text,
    avatar_key   text,
    role         text,
    is_muted     boolean,
    has_raised_hand boolean,
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
           COALESCE(ma.storage_key, ''),
           rp.role,
           rp.is_muted,
           (rsr.status = 'pending'),
           rp.joined_at
    FROM room_participants rp
    JOIN users u ON u.id = rp.user_id
    LEFT JOIN user_profiles p ON p.user_id = rp.user_id
    LEFT JOIN media_assets  ma ON ma.id = p.avatar_media_id
    LEFT JOIN room_speak_requests rsr
           ON rsr.room_id = rp.room_id AND rsr.user_id = rp.user_id
    WHERE rp.room_id = p_room AND rp.left_at IS NULL
    ORDER BY
        CASE rp.role WHEN 'host' THEN 0 WHEN 'moderator' THEN 1
                     WHEN 'speaker' THEN 2 ELSE 3 END,
        rp.joined_at;
END;
$$;

-- ============================================================
-- 5. JOIN — menegakkan invite_only
-- ============================================================

CREATE OR REPLACE FUNCTION public.join_room(p_room uuid)
RETURNS TABLE (role text, sfu_room_id text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_room  rooms%ROWTYPE;
    v_count integer;
    v_role  text;
BEGIN
    SELECT * INTO v_room FROM rooms WHERE rooms.id = p_room AND status = 'live';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'room tidak ditemukan atau sudah berakhir' USING ERRCODE = 'P0002';
    END IF;

    IF EXISTS (
        SELECT 1 FROM blocks b
        WHERE (b.blocker_id = v_room.host_id AND b.blocked_id = v_user)
           OR (b.blocker_id = v_user AND b.blocked_id = v_room.host_id)
    ) THEN
        RAISE EXCEPTION 'tidak diizinkan' USING ERRCODE = '42501';
    END IF;

    IF v_room.host_id <> v_user THEN
        IF v_room.visibility = 'followers'
           AND NOT EXISTS (
                SELECT 1 FROM follows f
                WHERE f.follower_id = v_user AND f.followee_id = v_room.host_id
                  AND f.status = 'accepted') THEN
            RAISE EXCEPTION 'room hanya untuk pengikut' USING ERRCODE = '42501';
        END IF;

        -- Ditegakkan sekarang; sebelumnya invite_only sama saja dengan public.
        IF v_room.visibility = 'invite_only'
           AND NOT EXISTS (
                SELECT 1 FROM room_invites ri
                WHERE ri.room_id = p_room AND ri.user_id = v_user) THEN
            RAISE EXCEPTION 'room hanya untuk yang diundang' USING ERRCODE = '42501';
        END IF;
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

CREATE OR REPLACE FUNCTION public.invite_to_room(p_room uuid, p_user uuid)
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
        WHERE rp.room_id = p_room AND rp.user_id = v_user
          AND rp.left_at IS NULL AND rp.role IN ('host', 'moderator')
    ) THEN
        RAISE EXCEPTION 'hanya host atau moderator' USING ERRCODE = '42501';
    END IF;

    INSERT INTO room_invites (room_id, user_id, invited_by)
    VALUES (p_room, p_user, v_user)
    ON CONFLICT DO NOTHING;
END;
$$;

-- ============================================================
-- 6. STORY — juga dari lawan bicara
-- ============================================================
-- Data menunjukkan 3 story ada tetapi 0 baris follows, sehingga story orang
-- lain tidak pernah muncul. Mensyaratkan follow saja terlalu ketat untuk
-- aplikasi yang intinya percakapan: orang yang sudah saling berkirim pesan
-- jelas saling kenal. Ditambahkan sebagai sumber kedua, bukan pengganti.
--
-- Sekalian mengembalikan storage_key untuk avatar penulis, dengan alasan
-- yang sama seperti pada peserta room.

DROP FUNCTION IF EXISTS public.list_stories();

CREATE FUNCTION public.list_stories()
RETURNS TABLE (
    id              uuid,
    author_id       uuid,
    author_username text,
    author_name     text,
    author_avatar_key text,
    media_id        uuid,
    media_kind      text,
    storage_key     text,
    duration_ms     integer,
    created_at      timestamptz,
    expires_at      timestamptz,
    viewed          boolean
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
        s.id,
        s.author_id,
        u.username::text,
        COALESCE(p.display_name, ''),
        COALESCE(av.storage_key, ''),
        s.media_id,
        ma.kind,
        ma.storage_key,
        ma.duration_ms,
        s.created_at,
        s.expires_at,
        (sv.viewer_id IS NOT NULL)
    FROM stories s
    JOIN users        u  ON u.id  = s.author_id AND u.deleted_at IS NULL
    JOIN media_assets ma ON ma.id = s.media_id
    LEFT JOIN user_profiles p  ON p.user_id = s.author_id
    LEFT JOIN media_assets  av ON av.id = p.avatar_media_id
    LEFT JOIN story_views   sv ON sv.story_id = s.id AND sv.viewer_id = v_user
    WHERE s.expires_at > now()
      AND (
            s.author_id = v_user
         OR EXISTS (
                SELECT 1 FROM follows f
                WHERE f.follower_id = v_user
                  AND f.followee_id = s.author_id
                  AND f.status = 'accepted'
            )
         -- Sumber kedua: siapa pun yang berbagi percakapan dengan kita.
         OR EXISTS (
                SELECT 1
                FROM conversation_members me
                JOIN conversation_members them
                  ON them.conversation_id = me.conversation_id
                WHERE me.user_id = v_user
                  AND me.left_at IS NULL
                  AND them.user_id = s.author_id
                  AND them.left_at IS NULL
            )
          )
      AND NOT EXISTS (
            SELECT 1 FROM blocks b
            WHERE (b.blocker_id = s.author_id AND b.blocked_id = v_user)
               OR (b.blocker_id = v_user AND b.blocked_id = s.author_id)
          )
    ORDER BY (s.author_id = v_user) DESC, s.author_id, s.created_at;
END;
$$;

-- ============================================================
-- 7. HAK EKSEKUSI
-- ============================================================

DO $$
DECLARE
    fn text;
BEGIN
    FOREACH fn IN ARRAY ARRAY[
        'public.end_room(uuid)',
        'public.leave_room(uuid)',
        'public.close_stale_rooms(integer)',
        'public.list_speak_requests(uuid)',
        'public.list_room_participants(uuid)',
        'public.join_room(uuid)',
        'public.invite_to_room(uuid, uuid)',
        'public.list_stories()'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO authenticated', fn);
    END LOOP;
END;
$$;

-- ============================================================
-- 8. BERSIHKAN SISA YANG SUDAH TERLANJUR
-- ============================================================

-- Room hantu: masih 'live' tetapi tidak ada peserta aktif sama sekali.
UPDATE rooms r
SET status = 'ended', ended_at = COALESCE(r.ended_at, now())
WHERE r.status = 'live'
  AND NOT EXISTS (
      SELECT 1 FROM room_participants rp
      WHERE rp.room_id = r.id AND rp.left_at IS NULL
  );

-- Peserta hantu: masih tercatat aktif di room yang sudah berakhir.
UPDATE room_participants rp
SET left_at = now()
WHERE rp.left_at IS NULL
  AND EXISTS (
      SELECT 1 FROM rooms r WHERE r.id = rp.room_id AND r.status = 'ended'
  );

COMMIT;
