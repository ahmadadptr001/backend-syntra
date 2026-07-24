-- Daftar follower — kebalikan dari list_following.
--
-- list_following menjawab "siapa yang aku ikuti"; ini menjawab "siapa yang
-- mengikutiku" (atau mengikuti orang lain). Layar profil butuh keduanya, dan
-- selama ini hanya satu arah yang tersedia.
--
-- p_username NULL berarti follower milik pemanggil sendiri; kalau diisi,
-- follower milik pengguna itu. Hanya follower berstatus 'accepted' yang
-- ditampilkan — permintaan yang masih pending bukan follower.

BEGIN;

CREATE OR REPLACE FUNCTION public.list_followers(p_username text DEFAULT NULL)
RETURNS TABLE (
    id              uuid,
    username        text,
    display_name    text,
    avatar_media_id uuid,
    status          text,
    created_at      timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user   uuid := public.require_auth();
    v_target uuid;
BEGIN
    IF p_username IS NULL THEN
        v_target := v_user;
    ELSE
        SELECT u.id INTO v_target
        FROM users u
        WHERE u.username = p_username::citext
          AND u.deleted_at IS NULL
          AND u.account_status = 'active';

        IF NOT FOUND THEN
            RAISE EXCEPTION 'pengguna tidak ditemukan' USING ERRCODE = 'P0002';
        END IF;
    END IF;

    RETURN QUERY
    SELECT u.id,
           u.username::text,
           COALESCE(p.display_name, ''),
           p.avatar_media_id,
           f.status,
           f.created_at
    FROM follows f
    JOIN users u ON u.id = f.follower_id AND u.deleted_at IS NULL
    LEFT JOIN user_profiles p ON p.user_id = u.id
    WHERE f.followee_id = v_target
      AND f.status = 'accepted'
    ORDER BY COALESCE(p.display_name, u.username::text);
END;
$$;

DO $$
BEGIN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.list_followers(text) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.list_followers(text) TO authenticated';
END;
$$;

COMMIT;
