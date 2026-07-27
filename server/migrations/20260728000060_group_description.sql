-- Deskripsi grup — teks bebas (≤500 char) yang dilihat & (untuk admin/owner) diubah
-- di layar Info grup. Setara title/avatar: tersimpan di conversations, diubah lewat
-- update_group, dan dikembalikan get_conversation.
--
-- p_description bersifat NULLABLE: NULL = jangan ubah (mis. saat hanya ganti judul),
-- '' = kosongkan deskripsi. Default NULL supaya pemanggil lama (3 argumen) tetap jalan.

BEGIN;

ALTER TABLE conversations ADD COLUMN IF NOT EXISTS description text NOT NULL DEFAULT '';

-- update_group mendapat argumen deskripsi. Argumen baru diberi DEFAULT NULL agar
-- pemanggilan lama tanpa p_description tetap ter-resolve.
CREATE OR REPLACE FUNCTION public.update_group(
    p_conversation uuid,
    p_title        text,
    p_avatar_media uuid,
    p_description  text DEFAULT NULL
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM conversation_members cm
        WHERE cm.conversation_id = p_conversation AND cm.user_id = v_user
          AND cm.left_at IS NULL AND cm.role IN ('owner', 'admin')
    ) THEN
        RAISE EXCEPTION 'hanya admin grup yang boleh mengubah grup' USING ERRCODE = '42501';
    END IF;

    UPDATE conversations c
    SET title           = COALESCE(NULLIF(btrim(p_title), ''), c.title),
        avatar_media_id = COALESCE(p_avatar_media, c.avatar_media_id),
        description     = COALESCE(LEFT(p_description, 500), c.description)
    WHERE c.id = p_conversation AND c.type = 'group';

    IF p_title IS NOT NULL AND btrim(p_title) <> '' THEN
        PERFORM public.post_system_message(p_conversation, 'Nama grup diubah menjadi "' || btrim(p_title) || '"');
    END IF;
    IF p_description IS NOT NULL THEN
        PERFORM public.post_system_message(p_conversation, 'Deskripsi grup diperbarui');
    END IF;
END;
$$;

REVOKE EXECUTE ON FUNCTION public.update_group(uuid, text, uuid, text) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.update_group(uuid, text, uuid, text) TO authenticated;

-- get_conversation kini juga mengembalikan description. Ganti signature (kolom baru),
-- jadi DROP dulu lalu buat ulang, dan atur GRANT.
DROP FUNCTION IF EXISTS public.get_conversation(uuid);
CREATE FUNCTION public.get_conversation(p_conversation uuid)
RETURNS TABLE (
    id              uuid,
    type            text,
    title           text,
    description     text,
    avatar_key      text,
    created_by      uuid,
    my_role         text,
    is_muted        boolean,
    member_count    integer,
    created_at      timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM conversation_members cm
        WHERE cm.conversation_id = p_conversation AND cm.user_id = v_user AND cm.left_at IS NULL
    ) THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;

    RETURN QUERY
    SELECT c.id,
           c.type,
           COALESCE(c.title, ''),
           COALESCE(c.description, ''),
           COALESCE(ma.storage_key, ''),
           c.created_by,
           me.role,
           (me.muted_until IS NOT NULL AND me.muted_until > now()),
           (SELECT count(*)::integer FROM conversation_members m
            WHERE m.conversation_id = c.id AND m.left_at IS NULL),
           c.created_at
    FROM conversations c
    JOIN conversation_members me ON me.conversation_id = c.id AND me.user_id = v_user
    LEFT JOIN media_assets ma ON ma.id = c.avatar_media_id
    WHERE c.id = p_conversation;
END;
$$;

REVOKE EXECUTE ON FUNCTION public.get_conversation(uuid) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.get_conversation(uuid) TO authenticated;

COMMIT;
