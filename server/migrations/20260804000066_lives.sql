-- Live streaming — metadata sesi, bukan video.
--
-- PENTING: seperti voice room, tabel dan fungsi di berkas ini TIDAK mengalirkan
-- video. Video realtime lewat SFU (LiveKit): host MENERBITKAN track kamera,
-- penonton BERLANGGANAN. Backend Go hanya jadi otoritas — menentukan siapa host
-- (boleh publish) dan siapa penonton (subscribe saja), lalu menerbitkan token
-- LiveKit yang membuktikannya.
--
--   host  ──video (UDP/WebRTC)──► SFU ──video──► penonton
--     │                            ▲
--     └──token & peran (HTTPS)──► backend ini
--
-- Beda dari voice room: satu live selalu punya SATU host publisher dan banyak
-- penonton listener — tidak ada speaker/moderator/raise-hand. Live juga selalu
-- publik (siapa pun yang tidak memblokir/diblokir boleh menonton).

BEGIN;

CREATE TABLE IF NOT EXISTS lives (
    id          uuid PRIMARY KEY,
    host_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title       text NOT NULL,
    category    text NOT NULL DEFAULT '',
    status      text NOT NULL DEFAULT 'live'
        CHECK (status IN ('live', 'ended')),

    -- Jembatan ke media server. Backend tidak pernah menyentuh video; ia hanya
    -- menerbitkan token yang mengizinkan klien bergabung ke room ini di SFU.
    sfu_room_id text,

    peak_viewer_count integer NOT NULL DEFAULT 0,

    started_at timestamptz NOT NULL DEFAULT now(),
    ended_at   timestamptz
);

CREATE INDEX IF NOT EXISTS lives_live_idx
    ON lives (started_at DESC) WHERE status = 'live';

-- Kehadiran di sebuah live. Host ikut tercatat (role 'host') supaya "tidak ada
-- peserta aktif" bisa dipakai membersihkan live hantu; jumlah penonton dihitung
-- dari baris role 'viewer' saja.
CREATE TABLE IF NOT EXISTS live_viewers (
    id       uuid PRIMARY KEY,
    live_id  uuid NOT NULL REFERENCES lives(id) ON DELETE CASCADE,
    user_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role     text NOT NULL DEFAULT 'viewer'
        CHECK (role IN ('host', 'viewer')),

    joined_at timestamptz NOT NULL DEFAULT now(),
    left_at   timestamptz
);

-- Satu orang hanya boleh punya SATU kehadiran aktif per live.
CREATE UNIQUE INDEX IF NOT EXISTS live_viewers_active_idx
    ON live_viewers (live_id, user_id) WHERE left_at IS NULL;

ALTER TABLE lives        ENABLE ROW LEVEL SECURITY;
ALTER TABLE live_viewers ENABLE ROW LEVEL SECURITY;

-- ============================================================
-- FUNGSI
-- ============================================================

-- Membuka live baru dengan pemanggil sebagai host.
CREATE OR REPLACE FUNCTION public.create_live(
    p_id       uuid,
    p_title    text,
    p_category text,
    p_sfu_room text
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
        RAISE EXCEPTION 'judul live tidak boleh kosong' USING ERRCODE = '22023';
    END IF;

    INSERT INTO lives (id, host_id, title, category, status, sfu_room_id)
    VALUES (p_id, v_user, btrim(p_title), COALESCE(p_category, ''), 'live', p_sfu_room);

    -- Host langsung tercatat sebagai peserta (bukan penonton).
    INSERT INTO live_viewers (id, live_id, user_id, role)
    VALUES (gen_random_uuid(), p_id, v_user, 'host');
END;
$$;

-- Daftar live yang sedang berlangsung dan boleh dilihat pemanggil (bukan yang
-- saling blokir dengan host).
CREATE OR REPLACE FUNCTION public.list_lives()
RETURNS TABLE (
    id            uuid,
    host_id       uuid,
    host_username text,
    host_name     text,
    host_avatar   text,
    title         text,
    category      text,
    viewer_count  integer,
    started_at    timestamptz
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
        l.id,
        l.host_id,
        u.username::text,
        COALESCE(p.display_name, ''),
        COALESCE(av.storage_key, ''),
        l.title,
        l.category,
        COALESCE(cnt.viewers, 0)::integer,
        l.started_at
    FROM lives l
    JOIN users u ON u.id = l.host_id AND u.deleted_at IS NULL
    LEFT JOIN user_profiles p  ON p.user_id = l.host_id
    LEFT JOIN media_assets  av ON av.id = p.avatar_media_id
    LEFT JOIN LATERAL (
        SELECT count(*) AS viewers
        FROM live_viewers lv
        WHERE lv.live_id = l.id AND lv.left_at IS NULL AND lv.role = 'viewer'
    ) cnt ON true
    WHERE l.status = 'live'
      AND NOT EXISTS (
            SELECT 1 FROM blocks b
            WHERE (b.blocker_id = l.host_id AND b.blocked_id = v_user)
               OR (b.blocker_id = v_user AND b.blocked_id = l.host_id)
          )
    ORDER BY l.started_at DESC;
END;
$$;

-- Satu live berdasarkan id — dipakai penonton untuk memeriksa apakah siaran
-- masih hidup (kosong = sudah berakhir, klien menutup layar).
CREATE OR REPLACE FUNCTION public.get_live(p_live uuid)
RETURNS TABLE (
    id            uuid,
    host_id       uuid,
    host_username text,
    host_name     text,
    host_avatar   text,
    title         text,
    category      text,
    viewer_count  integer,
    started_at    timestamptz
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
        l.id,
        l.host_id,
        u.username::text,
        COALESCE(p.display_name, ''),
        COALESCE(av.storage_key, ''),
        l.title,
        l.category,
        COALESCE(cnt.viewers, 0)::integer,
        l.started_at
    FROM lives l
    JOIN users u ON u.id = l.host_id AND u.deleted_at IS NULL
    LEFT JOIN user_profiles p  ON p.user_id = l.host_id
    LEFT JOIN media_assets  av ON av.id = p.avatar_media_id
    LEFT JOIN LATERAL (
        SELECT count(*) AS viewers
        FROM live_viewers lv
        WHERE lv.live_id = l.id AND lv.left_at IS NULL AND lv.role = 'viewer'
    ) cnt ON true
    WHERE l.id = p_live AND l.status = 'live'
      AND NOT EXISTS (
            SELECT 1 FROM blocks b
            WHERE (b.blocker_id = l.host_id AND b.blocked_id = v_user)
               OR (b.blocker_id = v_user AND b.blocked_id = l.host_id)
          );
END;
$$;

-- Bergabung ke live. Mengembalikan peran (host / viewer) yang menentukan apakah
-- klien boleh menerbitkan video di SFU atau hanya menonton, dan id room SFU.
CREATE OR REPLACE FUNCTION public.join_live(p_live uuid)
RETURNS TABLE (role text, sfu_room_id text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_live lives%ROWTYPE;
    v_role text;
BEGIN
    SELECT * INTO v_live FROM lives WHERE lives.id = p_live AND status = 'live';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'live tidak ditemukan atau sudah berakhir' USING ERRCODE = 'P0002';
    END IF;

    IF EXISTS (
        SELECT 1 FROM blocks b
        WHERE (b.blocker_id = v_live.host_id AND b.blocked_id = v_user)
           OR (b.blocker_id = v_user AND b.blocked_id = v_live.host_id)
    ) THEN
        RAISE EXCEPTION 'tidak diizinkan' USING ERRCODE = '42501';
    END IF;

    v_role := CASE WHEN v_live.host_id = v_user THEN 'host' ELSE 'viewer' END;

    INSERT INTO live_viewers (id, live_id, user_id, role)
    VALUES (gen_random_uuid(), p_live, v_user, v_role)
    ON CONFLICT (live_id, user_id) WHERE left_at IS NULL
    DO UPDATE SET joined_at = now();

    -- Perbarui puncak penonton (host tidak dihitung).
    UPDATE lives
    SET peak_viewer_count = GREATEST(peak_viewer_count, (
        SELECT count(*) FROM live_viewers lv
        WHERE lv.live_id = p_live AND lv.left_at IS NULL AND lv.role = 'viewer'
    ))
    WHERE lives.id = p_live;

    RETURN QUERY SELECT v_role, v_live.sfu_room_id;
END;
$$;

-- Menutup live sekaligus mengeluarkan seluruh penontonnya.
CREATE OR REPLACE FUNCTION public.end_live(p_live uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
BEGIN
    UPDATE lives l
    SET status = 'ended', ended_at = now()
    WHERE l.id = p_live AND l.status <> 'ended';

    UPDATE live_viewers lv
    SET left_at = now()
    WHERE lv.live_id = p_live AND lv.left_at IS NULL;
END;
$$;

-- Mengakhiri live atas permintaan host, tanpa harus keluar dulu. Hanya host.
CREATE OR REPLACE FUNCTION public.end_live_by_host(p_live uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_host boolean;
BEGIN
    SELECT (l.host_id = v_user) INTO v_host FROM lives l WHERE l.id = p_live;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'live tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;
    IF NOT COALESCE(v_host, false) THEN
        RAISE EXCEPTION 'hanya host yang boleh mengakhiri' USING ERRCODE = '42501';
    END IF;

    PERFORM public.end_live(p_live);
END;
$$;

-- Keluar dari live. Kalau yang keluar host, live ikut berakhir — mengembalikan
-- penanda supaya backend tahu harus memberi tahu penonton yang tersisa.
CREATE OR REPLACE FUNCTION public.leave_live(p_live uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_host boolean;
BEGIN
    SELECT (l.host_id = v_user) INTO v_host FROM lives l WHERE l.id = p_live;

    UPDATE live_viewers lv
    SET left_at = now()
    WHERE lv.live_id = p_live AND lv.user_id = v_user AND lv.left_at IS NULL;

    IF COALESCE(v_host, false) THEN
        PERFORM public.end_live(p_live);
        RETURN true;
    END IF;

    RETURN false;
END;
$$;

-- Menutup live yang terbengkalai — host yang aplikasinya tertutup paksa atau
-- kehilangan jaringan tidak pernah memanggil leave_live. Dipanggil backend
-- berkala. Sebuah live dianggap terbengkalai bila host-nya tidak lagi tercatat
-- aktif (baris role 'host' sudah left_at), atau sudah lama dibuka tanpa peserta
-- aktif sama sekali.
CREATE OR REPLACE FUNCTION public.close_stale_lives(p_idle_minutes integer DEFAULT 5)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_live  uuid;
    v_count integer := 0;
BEGIN
    FOR v_live IN
        SELECT l.id
        FROM lives l
        WHERE l.status = 'live'
          AND NOT EXISTS (
                SELECT 1 FROM live_viewers lv
                WHERE lv.live_id = l.id AND lv.left_at IS NULL AND lv.role = 'host'
              )
          AND l.started_at < now() - make_interval(mins => p_idle_minutes)
    LOOP
        PERFORM public.end_live(v_live);
        v_count := v_count + 1;
    END LOOP;

    RETURN v_count;
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
        'public.create_live(uuid, text, text, text)',
        'public.list_lives()',
        'public.get_live(uuid)',
        'public.join_live(uuid)',
        'public.end_live(uuid)',
        'public.end_live_by_host(uuid)',
        'public.leave_live(uuid)',
        'public.close_stale_lives(integer)'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO authenticated', fn);
    END LOOP;
END;
$$;

COMMIT;
