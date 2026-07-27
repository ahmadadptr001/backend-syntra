-- find_user harus menghormati blokir.
--
-- MASALAH. Blokir sudah ditegakkan hampir di mana-mana: search_users menyaring
-- dua arah, list_reels_feed dan list_user_reels menyembunyikan reel, follow_user
-- menolak dengan 42501. Tetapi find_user -- yang dipakai GET /users/{username},
-- yaitu jalur yang dipakai saat MEMBUKA halaman profil -- tidak memeriksa blocks
-- sama sekali.
--
-- Akibatnya blokir bisa dilewati sepenuhnya hanya dengan tahu username-nya:
-- orang yang diblokir tetap melihat nama, foto profil, background, dan jumlah
-- pengikut targetnya. Aplikasi memang menyembunyikan halaman itu di sisi klien,
-- tetapi itu bukan penegakan -- klien lain (atau curl) tetap mendapat datanya.
--
-- PERBAIKAN. Perlakukan profil yang terblokir seperti tidak ada: kembalikan nol
-- baris, yang sudah diterjemahkan lapisan Go menjadi ErrNotFound -> 404. Sengaja
-- 404 dan bukan 403, supaya balasannya tidak membocorkan "akun ini ada dan dia
-- memblokirmu".
--
-- Blokir DUA ARAH, seperti fungsi lain: yang memblokir juga tidak melihat profil
-- yang ia blokir (aplikasi sudah menampilkan dinding "diblokir" untuk itu).
-- Profil sendiri tidak pernah tersaring -- seseorang tidak bisa memblokir dirinya
-- (lihat ErrCannotBlockSelf), tetapi syarat u.id = v_user tetap ditulis eksplisit
-- supaya aman.
--
-- RETURNS TABLE tidak berubah, jadi CREATE OR REPLACE cukup. ASCII murni.

BEGIN;

CREATE OR REPLACE FUNCTION public.find_user(p_username text)
RETURNS TABLE (
    id              uuid,
    username        text,
    display_name    text,
    avatar_media_id text,   -- storage_key
    cover_media_id  text,   -- storage_key background/cover
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
BEGIN
    RETURN QUERY
    SELECT u.id,
           u.username::text,
           COALESCE(p.display_name, ''),
           COALESCE(av.storage_key, ''),
           COALESCE(cv.storage_key, ''),
           COALESCE(p.follower_count, 0),
           COALESCE(p.following_count, 0),
           COALESCE(f.status, ''),
           (u.id = v_user)
    FROM users u
    LEFT JOIN user_profiles p  ON p.user_id = u.id
    LEFT JOIN media_assets  av ON av.id = p.avatar_media_id
    LEFT JOIN media_assets  cv ON cv.id = p.cover_media_id
    LEFT JOIN follows f ON f.follower_id = v_user AND f.followee_id = u.id
    WHERE u.username = p_username::citext
      AND u.deleted_at IS NULL
      AND u.account_status = 'active'
      AND (
            u.id = v_user
         OR NOT EXISTS (
                SELECT 1 FROM blocks b
                WHERE (b.blocker_id = u.id   AND b.blocked_id = v_user)
                   OR (b.blocker_id = v_user AND b.blocked_id = u.id)
            )
      )
    LIMIT 1;
END;
$$;

DO $$
BEGIN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.find_user(text) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.find_user(text) TO authenticated';
END;
$$;

COMMIT;
