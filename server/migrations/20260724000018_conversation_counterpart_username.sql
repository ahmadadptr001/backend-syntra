-- Tambah counterpart_username pada GET /conversations.
--
-- Tim aplikasi memintanya (pesan-untuk-backend.md poin 6): layar panggilan
-- masuk mengambil nama lawan bicara dari daftar percakapan, dan fitur
-- laporkan/blokir dari daftar chat butuh username tanpa harus membuka chat-nya
-- dulu. counterpart_id sudah dikembalikan; username-nya sudah tersedia di join
-- tapi belum ikut di-SELECT.
--
-- Menambah kolom mengubah tipe baris kembalian, jadi fungsi lama di-DROP dulu
-- (CREATE OR REPLACE tidak bisa mengubah bentuk keluaran — Postgres 42P13).

BEGIN;

DROP FUNCTION IF EXISTS public.list_conversations(timestamptz, integer);
CREATE FUNCTION public.list_conversations(
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
    unread_count              integer,
    last_message_preview      text,
    last_message_type         text,
    last_message_sender       uuid,
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

        cu.id                         AS counterpart_id,
        cu.username::text             AS counterpart_username,
        cm_other.last_read_message_id AS counterpart_last_read,
        cm.unread_count,

        CASE WHEN lm.deleted_at IS NOT NULL THEN ''
             ELSE COALESCE(left(lm.body, 120), '')
        END AS last_message_preview,

        COALESCE(lm.type, '')                     AS last_message_type,
        lm.sender_id                              AS last_message_sender,
        COALESCE(c.last_message_at, c.created_at) AS last_message_at,
        c.created_at

    FROM conversations c
    JOIN conversation_members cm
      ON cm.conversation_id = c.id
     AND cm.user_id = v_user
     AND cm.left_at IS NULL

    LEFT JOIN LATERAL (
        SELECT m2.user_id, u.username, m2.last_read_message_id
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

    WHERE COALESCE(c.last_message_at, c.created_at) < p_before
    ORDER BY COALESCE(c.last_message_at, c.created_at) DESC
    LIMIT LEAST(GREATEST(p_limit, 1), 100);
END;
$$;

REVOKE EXECUTE ON FUNCTION public.list_conversations(timestamptz, integer) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.list_conversations(timestamptz, integer) TO authenticated;

COMMIT;
