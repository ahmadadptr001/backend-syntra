-- Pengunjung profil kedaluwarsa 24 jam.
--
-- list_profile_visitors (migrasi 42) menampilkan SEMUA kunjungan selamanya. Di
-- sini dibatasi hanya kunjungan dalam 24 jam terakhir -- baik untuk hitungan
-- total maupun daftarnya -- jadi "siapa yang lihat profilmu" hilang setelah 24
-- jam, seperti story. Baris lama dibiarkan di tabel (murah, tidak mengganggu);
-- yang berubah hanya penyaringan pada fungsi baca. Ditulis ASCII murni.

BEGIN;

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
    v_since timestamptz := now() - interval '24 hours';
BEGIN
    SELECT count(*) INTO v_total
    FROM profile_visits pv
    JOIN users u ON u.id = pv.visitor_id AND u.deleted_at IS NULL
    WHERE pv.profile_id = v_user
      AND pv.visited_at > v_since
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
      AND pv.visited_at > v_since
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
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.list_profile_visitors(integer) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.list_profile_visitors(integer) TO authenticated';
END;
$$;

COMMIT;
