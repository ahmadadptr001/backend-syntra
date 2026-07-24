-- Pencarian & penemuan pengguna — supaya aplikasi tidak terasa "dunia sendiri".
--
-- Masalahnya: satu-satunya cara menemukan orang lain adalah tahu username
-- persisnya (find_user / scan QR). Akun baru yang belum mengikuti siapa pun
-- karena itu melihat layar kosong — tak ada percakapan, tak ada story, dan tak
-- ada cara membangun daftar following. Terasa seolah tiap pengguna terisolasi.
--
-- search_users menutup itu: cari berdasarkan username ATAU nama tampilan
-- (substring, case-insensitive), dengan follow_status dari sudut pandang
-- pemanggil supaya tombol Follow/Requested/Following langsung benar. Query
-- kosong sengaja mengembalikan "saran" (pengguna teraktif/terpopuler) supaya
-- layar temukan-orang tidak pernah kosong.
--
-- Menyembunyikan: diri sendiri, akun terhapus/nonaktif, dan blokir dua arah.

BEGIN;

CREATE OR REPLACE FUNCTION public.search_users(p_query text DEFAULT '', p_limit int DEFAULT 30)
RETURNS TABLE (
    id              uuid,
    username        text,
    display_name    text,
    avatar_media_id uuid,
    follower_count  integer,
    following_count integer,
    follow_status   text,
    is_self         boolean
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_q    text := btrim(coalesce(p_query, ''));
    v_lim  int  := least(greatest(coalesce(p_limit, 30), 1), 50);
BEGIN
    RETURN QUERY
    SELECT u.id,
           u.username::text,
           COALESCE(p.display_name, ''),
           p.avatar_media_id,
           COALESCE(p.follower_count, 0),
           COALESCE(p.following_count, 0),
           COALESCE(f.status, ''),   -- '' = belum diikuti
           false                     -- diri sendiri sudah disaring di bawah
    FROM users u
    LEFT JOIN user_profiles p ON p.user_id = u.id
    LEFT JOIN follows f ON f.follower_id = v_user AND f.followee_id = u.id
    WHERE u.deleted_at IS NULL
      AND u.account_status = 'active'
      AND u.id <> v_user
      AND NOT EXISTS (
            SELECT 1 FROM blocks b
            WHERE (b.blocker_id = u.id     AND b.blocked_id = v_user)
               OR (b.blocker_id = v_user   AND b.blocked_id = u.id)
      )
      AND (
            v_q = ''
         OR u.username::text ILIKE '%' || v_q || '%'
         OR COALESCE(p.display_name, '') ILIKE '%' || v_q || '%'
      )
    ORDER BY
      -- saat mencari, awalan username yang cocok naik ke atas; selebihnya
      -- (dan seluruh daftar saran saat query kosong) urut dari terpopuler.
      (v_q <> '' AND u.username::text ILIKE v_q || '%') DESC,
      COALESCE(p.follower_count, 0) DESC,
      u.username
    LIMIT v_lim;
END;
$$;

DO $$
BEGIN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.search_users(text, int) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.search_users(text, int) TO authenticated';
END;
$$;

COMMIT;
