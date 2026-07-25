-- Story hanya dari orang yang diikuti (plus milik sendiri).
--
-- Sebelumnya list_stories menampilkan story dari: diri sendiri, orang yang
-- diikuti, ATAU lawan bicara di percakapan mana pun. Cabang "lawan bicara"
-- membuat story muncul dari orang yang belum tentu diikuti. Permintaan: feed
-- story hanya berisi orang yang benar-benar diikuti (accepted) dan diri sendiri.
--
-- Perbaikan: buang cabang conversation_members. Sisanya (blokir, kedaluwarsa,
-- urutan) tetap. CREATE OR REPLACE -- bentuk RETURNS TABLE identik migrasi 10.

BEGIN;

CREATE OR REPLACE FUNCTION public.list_stories()
RETURNS TABLE (
    id                uuid,
    author_id         uuid,
    author_username   text,
    author_name       text,
    author_avatar_key text,
    media_id          uuid,
    media_kind        text,
    storage_key       text,
    duration_ms       integer,
    created_at        timestamptz,
    expires_at        timestamptz,
    viewed            boolean
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
    WHERE s.deleted_at IS NULL
      AND s.expires_at > now()
      AND (
            s.author_id = v_user
         OR EXISTS (
                SELECT 1 FROM follows f
                WHERE f.follower_id = v_user
                  AND f.followee_id = s.author_id
                  AND f.status = 'accepted'
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

COMMIT;
