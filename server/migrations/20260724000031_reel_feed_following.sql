-- Tambah is_following ke feed reels, supaya aplikasi tahu penulis mana yang
-- sudah diikuti dan bisa menyembunyikan tombol "+" (follow) di Shorts.
--
-- Tanpa ini, feed tak membawa status follow, jadi tombol "+" tetap muncul di
-- reel orang yang sudah diikuti. Menambah satu kolom boolean lebih murah
-- daripada memanggil endpoint follow-status per reel.
--
-- Mengubah tipe kembalian → DROP dulu (CREATE OR REPLACE tak bisa ganti tipe).

BEGIN;

DROP FUNCTION IF EXISTS public.list_reels_feed(integer, timestamptz, uuid);

CREATE FUNCTION public.list_reels_feed(
    p_limit     integer,
    p_before_at timestamptz,
    p_before_id uuid
)
RETURNS TABLE (
    id               uuid,
    author_id        uuid,
    author_username  text,
    author_name      text,
    author_avatar    uuid,
    media_id         uuid,
    media_kind       text,
    storage_key      text,
    duration_ms      integer,
    caption          text,
    visibility       text,
    comments_enabled boolean,
    like_count       integer,
    comment_count    integer,
    view_count       integer,
    share_count      integer,
    liked            boolean,
    saved            boolean,
    is_following     boolean,
    published_at     timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_limit integer := LEAST(GREATEST(COALESCE(p_limit, 20), 1), 50);
BEGIN
    RETURN QUERY
    SELECT
        r.id, r.author_id, u.username::text, COALESCE(p.display_name, ''), p.avatar_media_id,
        r.media_id, ma.kind, ma.storage_key, ma.duration_ms,
        r.caption, r.visibility, r.comments_enabled,
        r.like_count, r.comment_count, r.view_count, r.share_count,
        (rl.user_id IS NOT NULL),
        (rs.user_id IS NOT NULL),
        -- true kalau pemanggil sudah mengikuti penulis (status accepted). Reel
        -- milik sendiri dianggap "following" supaya tombol + juga tak muncul.
        (r.author_id = v_user OR f.follower_id IS NOT NULL),
        r.published_at
    FROM reels r
    JOIN users        u  ON u.id  = r.author_id AND u.deleted_at IS NULL
    JOIN media_assets ma ON ma.id = r.media_id
    LEFT JOIN user_profiles p  ON p.user_id  = r.author_id
    LEFT JOIN reel_likes    rl ON rl.reel_id = r.id AND rl.user_id = v_user
    LEFT JOIN reel_saves    rs ON rs.reel_id = r.id AND rs.user_id = v_user
    LEFT JOIN follows       f  ON f.follower_id = v_user AND f.followee_id = r.author_id AND f.status = 'accepted'
    WHERE r.status = 'published' AND r.deleted_at IS NULL
      AND (
            r.author_id = v_user
         OR (
                NOT EXISTS (
                    SELECT 1 FROM blocks b
                    WHERE (b.blocker_id = r.author_id AND b.blocked_id = v_user)
                       OR (b.blocker_id = v_user AND b.blocked_id = r.author_id)
                )
                AND (
                      r.visibility = 'public'
                   OR (r.visibility = 'followers' AND EXISTS (
                          SELECT 1 FROM follows f2
                          WHERE f2.follower_id = v_user AND f2.followee_id = r.author_id AND f2.status = 'accepted'
                      ))
                )
            )
      )
      AND (
            p_before_at IS NULL
         OR (r.published_at, r.id) < (p_before_at, p_before_id)
      )
    ORDER BY r.published_at DESC, r.id DESC
    LIMIT v_limit;
END;
$$;

DO $$
BEGIN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.list_reels_feed(integer, timestamptz, uuid) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.list_reels_feed(integer, timestamptz, uuid) TO authenticated';
END;
$$;

COMMIT;
