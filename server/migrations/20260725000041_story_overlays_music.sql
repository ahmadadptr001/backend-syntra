-- Story overlays: simpan & kembalikan JSONB overlays (dipakai untuk MUSIK).
--
-- Tabel stories sudah punya kolom overlays jsonb (default '{}') sejak awal,
-- tetapi create_story tak pernah menyimpannya dan list_stories tak pernah
-- mengembalikannya. Migrasi ini menyambungkan keduanya, sehingga klien bisa
-- melampirkan lagu ke story (foto/gambar polos) sebagai:
--   overlays = { "music": { "title":.., "artist":.., "url":.., "artwork":.. } }
-- dan memutarnya di penonton story. Tanpa kolom baru.
--
-- Kedua fungsi di-DROP dulu karena signature (create_story) dan return type
-- (list_stories) berubah. Ditulis ASCII murni.

BEGIN;

-- create_story: tambah p_overlays jsonb.
DROP FUNCTION IF EXISTS public.create_story(uuid, uuid, text, timestamptz);

CREATE FUNCTION public.create_story(
    p_id         uuid,
    p_media      uuid,
    p_visibility text,
    p_created_at timestamptz,
    p_overlays   jsonb
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF NOT EXISTS (SELECT 1 FROM media_assets WHERE id = p_media AND owner_id = v_user) THEN
        RAISE EXCEPTION 'media bukan milik pengguna ini' USING ERRCODE = '42501';
    END IF;

    INSERT INTO stories (id, author_id, media_id, visibility, overlays, created_at, expires_at)
    VALUES (p_id, v_user, p_media, COALESCE(p_visibility, 'followers'),
            COALESCE(p_overlays, '{}'::jsonb),
            p_created_at, p_created_at + interval '24 hours');
END;
$$;

-- list_stories: tambah kolom overlays di RETURNS TABLE (bentuk lain identik mig 38).
DROP FUNCTION IF EXISTS public.list_stories();

CREATE FUNCTION public.list_stories()
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
    viewed            boolean,
    overlays          jsonb
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
        (sv.viewer_id IS NOT NULL),
        COALESCE(s.overlays, '{}'::jsonb)
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

DO $$
BEGIN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.create_story(uuid, uuid, text, timestamptz, jsonb) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.create_story(uuid, uuid, text, timestamptz, jsonb) TO authenticated';
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.list_stories() FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.list_stories() TO authenticated';
END;
$$;

COMMIT;
