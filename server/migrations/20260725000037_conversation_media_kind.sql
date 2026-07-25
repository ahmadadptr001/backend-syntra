-- Beranda chat: bedakan pratinjau media jadi [Foto] / [Video] / [Pesan suara].
--
-- list_conversations mengembalikan last_message_type = messages.type, yang untuk
-- kiriman media selalu bernilai generik 'media' (atau 'voice_note'), sehingga
-- daftar chat tak bisa menuliskan pesan terakhir yang berupa foto/video/suara --
-- previewnya jadi kosong. Perbaikan: untuk pesan media, kembalikan KIND lampiran
-- (image/video/audio/voice_note) dari media_assets, bukan sekadar 'media'.
--
-- Mengubah isi kolom (bukan bentuk baris), jadi CREATE OR REPLACE cukup -- bentuk
-- RETURNS TABLE identik dengan migrasi 35.

BEGIN;

CREATE OR REPLACE FUNCTION public.list_conversations(
    p_before timestamptz,
    p_limit  integer
)
RETURNS TABLE (
    id                        uuid,
    type                      text,
    title                     text,
    avatar_media_id           uuid,
    counterpart_id            uuid,
    counterpart_username      text,
    counterpart_last_read     uuid,
    counterpart_last_delivered uuid,
    unread_count              integer,
    last_message_preview      text,
    last_message_type         text,
    last_message_sender       uuid,
    last_message_id           uuid,
    last_message_at           timestamptz,
    created_at                timestamptz
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
        c.id,
        c.type,

        CASE WHEN c.type = 'group'
             THEN COALESCE(c.title, '')
             ELSE COALESCE(NULLIF(cp.display_name, ''), cu.username, '')
        END AS title,

        CASE WHEN c.type = 'group'
             THEN c.avatar_media_id
             ELSE cp.avatar_media_id
        END AS avatar_media_id,

        cu.id                            AS counterpart_id,
        cu.username::text                AS counterpart_username,
        cm_other.last_read_message_id    AS counterpart_last_read,
        cm_other.last_delivered_message_id AS counterpart_last_delivered,
        cm.unread_count,

        CASE WHEN lm.deleted_at IS NOT NULL THEN ''
             ELSE COALESCE(left(lm.body, 120), '')
        END AS last_message_preview,

        -- Untuk pesan media, laporkan jenis lampiran supaya beranda bisa menulis
        -- [Foto]/[Video]/[Pesan suara]; selain itu pakai tipe pesan apa adanya.
        CASE
            WHEN lm.deleted_at IS NOT NULL THEN 'text'
            WHEN lm.type IN ('media', 'voice_note') THEN COALESCE(lmk.kind, lm.type)
            ELSE COALESCE(lm.type, '')
        END AS last_message_type,

        lm.sender_id                              AS last_message_sender,
        c.last_message_id                         AS last_message_id,
        COALESCE(c.last_message_at, c.created_at) AS last_message_at,
        c.created_at

    FROM conversations c
    JOIN conversation_members cm
      ON cm.conversation_id = c.id
     AND cm.user_id = v_user
     AND cm.left_at IS NULL

    LEFT JOIN LATERAL (
        SELECT m2.user_id, u.username,
               m2.last_read_message_id,
               m2.last_delivered_message_id
        FROM conversation_members m2
        JOIN users u ON u.id = m2.user_id
        WHERE m2.conversation_id = c.id
          AND m2.user_id <> v_user
          AND m2.left_at IS NULL
        ORDER BY m2.joined_at
        LIMIT 1
    ) cm_other ON c.type = 'direct'

    LEFT JOIN users         cu ON cu.id = cm_other.user_id
    LEFT JOIN user_profiles cp ON cp.user_id = cm_other.user_id
    LEFT JOIN messages      lm ON lm.id = c.last_message_id

    -- Kind lampiran pertama dari pesan terakhir (kalau ada).
    LEFT JOIN LATERAL (
        SELECT ma2.kind
        FROM message_attachments matt
        JOIN media_assets ma2 ON ma2.id = matt.media_id
        WHERE matt.message_id = lm.id
        ORDER BY matt.position
        LIMIT 1
    ) lmk ON true

    WHERE COALESCE(c.last_message_at, c.created_at) < p_before
    ORDER BY COALESCE(c.last_message_at, c.created_at) DESC
    LIMIT LEAST(GREATEST(p_limit, 1), 100);
END;
$$;

REVOKE EXECUTE ON FUNCTION public.list_conversations(timestamptz, integer) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.list_conversations(timestamptz, integer) TO authenticated;

COMMIT;
