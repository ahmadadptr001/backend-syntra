-- Avatar orang lain tidak muncul (hanya huruf awal nama).
--
-- Penyebab: find_user & search_users mengembalikan avatar_media_id (UUID),
-- bukan storage_key, sehingga backend tak bisa menyusun URL avatar dan aplikasi
-- jatuh ke inisial. GET /users/me sudah benar (mengembalikan storage_key via
-- get_full_profile), tapi profil orang lain tidak.
--
-- Perbaikan: kedua fungsi kini mengembalikan storage_key media avatar (di kolom
-- yang sama, tipe text), sehingga handler bisa memanggil PublicURL() dan
-- mengirim avatar_url siap pakai — persis alur profil sendiri.

BEGIN;

-- ============================================================
-- find_user  → storage_key avatar
-- ============================================================
DROP FUNCTION IF EXISTS public.find_user(text);

CREATE FUNCTION public.find_user(p_username text)
RETURNS TABLE (
    id              uuid,
    username        text,
    display_name    text,
    avatar_media_id text,   -- kini storage_key, bukan uuid
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
           COALESCE(p.follower_count, 0),
           COALESCE(p.following_count, 0),
           COALESCE(f.status, ''),
           (u.id = v_user)
    FROM users u
    LEFT JOIN user_profiles p ON p.user_id = u.id
    LEFT JOIN media_assets  av ON av.id = p.avatar_media_id
    LEFT JOIN follows f ON f.follower_id = v_user AND f.followee_id = u.id
    WHERE u.username = p_username::citext
      AND u.deleted_at IS NULL
      AND u.account_status = 'active'
    LIMIT 1;
END;
$$;

-- ============================================================
-- search_users → storage_key avatar
-- ============================================================
DROP FUNCTION IF EXISTS public.search_users(text, int);

CREATE FUNCTION public.search_users(p_query text DEFAULT '', p_limit int DEFAULT 30)
RETURNS TABLE (
    id              uuid,
    username        text,
    display_name    text,
    avatar_media_id text,   -- kini storage_key
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
           COALESCE(av.storage_key, ''),
           COALESCE(p.follower_count, 0),
           COALESCE(p.following_count, 0),
           COALESCE(f.status, ''),
           false
    FROM users u
    LEFT JOIN user_profiles p ON p.user_id = u.id
    LEFT JOIN media_assets  av ON av.id = p.avatar_media_id
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
      (v_q <> '' AND u.username::text ILIKE v_q || '%') DESC,
      COALESCE(p.follower_count, 0) DESC,
      u.username
    LIMIT v_lim;
END;
$$;

DO $$
BEGIN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.find_user(text) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.find_user(text) TO authenticated';
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.search_users(text, int) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.search_users(text, int) TO authenticated';
END;
$$;

COMMIT;
