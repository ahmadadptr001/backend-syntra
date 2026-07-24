-- Privasi presence — sembunyikan status online dari lawan bicara.
--
-- Selama ini status online seseorang terlihat oleh SEMUA lawan bicaranya tanpa
-- kecuali. Kolom baru presence_visible memberi pilihan menyembunyikannya.
--
-- Penegakannya ada di lapisan WebSocket: saat pengguna yang menyembunyikan
-- presence terhubung, backend tidak mencatatnya online dan tidak menyiarkan
-- perubahan statusnya — jadi ia tak pernah tampak online, dan "last seen" pun
-- tidak terekam. Berlaku sejak koneksi berikutnya setelah setelan diubah.
--
-- Setelan ini ikut aliran profil yang sudah ada (dm_privacy, story_privacy):
-- GET /users/me mengembalikannya, PATCH /users/me mengubahnya.

BEGIN;

ALTER TABLE user_settings
    ADD COLUMN IF NOT EXISTS presence_visible boolean NOT NULL DEFAULT true;

-- ============================================================
-- get_my_profile — tambah presence_visible
-- ============================================================
DROP FUNCTION IF EXISTS public.get_my_profile();
CREATE OR REPLACE FUNCTION public.get_my_profile()
RETURNS TABLE (
    id               uuid,
    username         text,
    email            text,
    display_name     text,
    bio              text,
    avatar_key       text,
    cover_key        text,
    follower_count   integer,
    following_count  integer,
    is_private       boolean,
    date_of_birth    date,
    dm_privacy       text,
    story_privacy    text,
    presence_visible boolean,
    locale           text,
    created_at       timestamptz
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
           COALESCE(u.email::text, ''),
           COALESCE(p.display_name, ''),
           COALESCE(p.bio, ''),
           COALESCE(ma.storage_key, ''),
           COALESCE(mc.storage_key, ''),
           COALESCE(p.follower_count, 0),
           COALESCE(p.following_count, 0),
           u.is_private,
           u.date_of_birth,
           COALESCE(s.dm_privacy, 'everyone'),
           COALESCE(s.story_privacy, 'followers'),
           COALESCE(s.presence_visible, true),
           COALESCE(s.locale, 'id'),
           u.created_at
    FROM users u
    LEFT JOIN user_profiles p  ON p.user_id = u.id
    LEFT JOIN user_settings s  ON s.user_id = u.id
    LEFT JOIN media_assets  ma ON ma.id = p.avatar_media_id
    LEFT JOIN media_assets  mc ON mc.id = p.cover_media_id
    WHERE u.id = v_user;
END;
$$;

-- ============================================================
-- update_my_profile — tambah p_presence_visible
-- ============================================================
DROP FUNCTION IF EXISTS public.update_my_profile(text, text, uuid, boolean, text, text, text, uuid);
CREATE OR REPLACE FUNCTION public.update_my_profile(
    p_display_name     text    DEFAULT NULL,
    p_bio              text    DEFAULT NULL,
    p_avatar_media     uuid    DEFAULT NULL,
    p_is_private       boolean DEFAULT NULL,
    p_dm_privacy       text    DEFAULT NULL,
    p_story_privacy    text    DEFAULT NULL,
    p_username         text    DEFAULT NULL,
    p_cover_media      uuid    DEFAULT NULL,
    p_presence_visible boolean DEFAULT NULL
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user     uuid := public.require_auth();
    v_username text := NULLIF(btrim(p_username), '');
BEGIN
    IF p_avatar_media IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM media_assets ma
                       WHERE ma.id = p_avatar_media AND ma.owner_id = v_user AND ma.kind = 'image') THEN
        RAISE EXCEPTION 'avatar harus gambar milik pengguna ini' USING ERRCODE = '42501';
    END IF;

    IF p_cover_media IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM media_assets ma
                       WHERE ma.id = p_cover_media AND ma.owner_id = v_user AND ma.kind = 'image') THEN
        RAISE EXCEPTION 'sampul harus gambar milik pengguna ini' USING ERRCODE = '42501';
    END IF;

    IF v_username IS NOT NULL THEN
        UPDATE users u
        SET username = v_username, updated_at = now()
        WHERE u.id = v_user AND u.username <> v_username;
    END IF;

    UPDATE user_profiles p
    SET display_name    = COALESCE(NULLIF(btrim(p_display_name), ''), p.display_name),
        bio             = COALESCE(p_bio, p.bio),
        avatar_media_id = COALESCE(p_avatar_media, p.avatar_media_id),
        cover_media_id  = COALESCE(p_cover_media, p.cover_media_id),
        updated_at      = now()
    WHERE p.user_id = v_user;

    IF p_is_private IS NOT NULL THEN
        UPDATE users u SET is_private = p_is_private, updated_at = now()
        WHERE u.id = v_user;
    END IF;

    IF p_dm_privacy IS NOT NULL OR p_story_privacy IS NOT NULL OR p_presence_visible IS NOT NULL THEN
        UPDATE user_settings s
        SET dm_privacy       = COALESCE(p_dm_privacy, s.dm_privacy),
            story_privacy    = COALESCE(p_story_privacy, s.story_privacy),
            presence_visible = COALESCE(p_presence_visible, s.presence_visible)
        WHERE s.user_id = v_user;
    END IF;
END;
$$;

DO $$
DECLARE
    fn text;
BEGIN
    FOREACH fn IN ARRAY ARRAY[
        'public.get_my_profile()',
        'public.update_my_profile(text, text, uuid, boolean, text, text, text, uuid, boolean)'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO authenticated', fn);
    END LOOP;
END;
$$;

COMMIT;
