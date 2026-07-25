-- Kunjungan profil: siapa yang melihat profil seseorang, terakhir kapan.
--
-- Satu baris per (profil, pengunjung); mengunjungi ulang memperbarui waktunya,
-- bukan menambah baris — daftar "siapa yang mampir" pendek dan tidak menggembung.
-- Kunjungan ke profil sendiri tidak dicatat, dan blokir dua arah menyembunyikannya.
--
-- Dipakai app: header profil sendiri menampilkan avatar pengunjung bertumpuk +
-- jumlah total. Ditulis ASCII murni.

BEGIN;

CREATE TABLE IF NOT EXISTS profile_visits (
    profile_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    visitor_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    visited_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (profile_id, visitor_id),
    CONSTRAINT profile_visits_no_self CHECK (profile_id <> visitor_id)
);

-- Untuk "pengunjung terbaru profil X", urut terbaru dulu.
CREATE INDEX IF NOT EXISTS profile_visits_recent_idx
    ON profile_visits (profile_id, visited_at DESC);

ALTER TABLE profile_visits ENABLE ROW LEVEL SECURITY;
-- Semua akses lewat fungsi SECURITY DEFINER di bawah; tabel sendiri tertutup.

-- ============================================================
-- CATAT KUNJUNGAN
-- ============================================================
-- Dipanggil saat seseorang membuka profil orang lain. Upsert: kunjungan ulang
-- memperbarui visited_at. Diam-diam mengabaikan kunjungan ke diri sendiri dan
-- ke/oleh pihak yang saling memblokir (tidak error, cukup tidak mencatat).
CREATE OR REPLACE FUNCTION public.record_profile_visit(p_profile uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF p_profile IS NULL OR p_profile = v_user THEN
        RETURN;
    END IF;

    IF EXISTS (
        SELECT 1 FROM blocks b
        WHERE (b.blocker_id = p_profile AND b.blocked_id = v_user)
           OR (b.blocker_id = v_user AND b.blocked_id = p_profile)
    ) THEN
        RETURN;
    END IF;

    INSERT INTO profile_visits (profile_id, visitor_id, visited_at)
    VALUES (p_profile, v_user, now())
    ON CONFLICT (profile_id, visitor_id) DO UPDATE SET visited_at = now();
END;
$$;

-- ============================================================
-- DAFTAR PENGUNJUNG (PROFIL SENDIRI)
-- ============================================================
-- Mengembalikan pengunjung terbaru profil PEMANGGIL, plus jumlah total lewat
-- kolom total_count yang sama di setiap baris (agar klien tak perlu query kedua).
-- Menyaring pengunjung yang kini diblokir dua arah dan akun terhapus.
CREATE OR REPLACE FUNCTION public.list_profile_visitors(p_limit integer)
RETURNS TABLE (
    visitor_id   uuid,
    username     text,
    display_name text,
    avatar_key   text,
    visited_at   timestamptz,
    total_count  bigint
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_total bigint;
BEGIN
    SELECT count(*) INTO v_total
    FROM profile_visits pv
    JOIN users u ON u.id = pv.visitor_id AND u.deleted_at IS NULL
    WHERE pv.profile_id = v_user
      AND NOT EXISTS (
            SELECT 1 FROM blocks b
            WHERE (b.blocker_id = pv.visitor_id AND b.blocked_id = v_user)
               OR (b.blocker_id = v_user AND b.blocked_id = pv.visitor_id)
        );

    RETURN QUERY
    SELECT
        pv.visitor_id,
        u.username::text,
        COALESCE(p.display_name, ''),
        COALESCE(av.storage_key, ''),
        pv.visited_at,
        v_total
    FROM profile_visits pv
    JOIN users        u  ON u.id = pv.visitor_id AND u.deleted_at IS NULL
    LEFT JOIN user_profiles p  ON p.user_id = pv.visitor_id
    LEFT JOIN media_assets  av ON av.id = p.avatar_media_id
    WHERE pv.profile_id = v_user
      AND NOT EXISTS (
            SELECT 1 FROM blocks b
            WHERE (b.blocker_id = pv.visitor_id AND b.blocked_id = v_user)
               OR (b.blocker_id = v_user AND b.blocked_id = pv.visitor_id)
        )
    ORDER BY pv.visited_at DESC
    LIMIT LEAST(GREATEST(p_limit, 1), 50);
END;
$$;

DO $$
BEGIN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.record_profile_visit(uuid) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.record_profile_visit(uuid) TO authenticated';
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.list_profile_visitors(integer) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.list_profile_visitors(integer) TO authenticated';
END;
$$;

COMMIT;
