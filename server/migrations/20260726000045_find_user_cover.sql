-- Ekspos cover/background profil ke profil PUBLIK (GET /users/{username}).
--
-- Cover sudah didukung untuk profil sendiri (get_my_profile/update_my_profile via
-- user_profiles.cover_media_id). Tapi find_user -- yang dipakai saat membuka profil
-- ORANG LAIN -- belum mengembalikannya, jadi background hanya terlihat oleh diri
-- sendiri. Migrasi ini menambah kolom cover_media_id (storage_key, seperti avatar)
-- ke hasil find_user. RETURNS TABLE berubah => DROP lalu CREATE. ASCII murni.

BEGIN;

DROP FUNCTION IF EXISTS public.find_user(text);

CREATE FUNCTION public.find_user(p_username text)
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
