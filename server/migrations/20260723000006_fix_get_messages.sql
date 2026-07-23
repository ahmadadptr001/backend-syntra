-- Perbaikan bug: get_messages selalu gagal dengan
--   42702  column reference "conversation_id" is ambiguous
--
-- Penyebabnya: `conversation_id` adalah salah satu nama kolom di RETURNS TABLE,
-- sekaligus nama kolom di tabel conversation_members. Di dalam subquery EXISTS,
-- nama itu dipakai tanpa kualifikasi, sehingga PL/pgSQL tidak bisa memutuskan
-- yang mana yang dimaksud.
--
-- Ini kelas kesalahan yang mudah terulang: setiap fungsi dengan RETURNS TABLE
-- perlu memenuhi-syarati SETIAP acuan kolom di dalamnya. Fungsi lain sudah
-- memakai alias di semua tempat, jadi hanya yang ini yang terdampak.

BEGIN;

CREATE OR REPLACE FUNCTION public.get_messages(
    p_conversation uuid,
    p_before       uuid,
    p_limit        integer
)
RETURNS TABLE (
    id                  uuid,
    conversation_id     uuid,
    sender_id           uuid,
    type                text,
    body                text,
    reply_to_message_id uuid,
    created_at          timestamptz,
    edited_at           timestamptz,
    is_deleted          boolean
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
        SELECT 1
        FROM conversation_members cm
        WHERE cm.conversation_id = p_conversation
          AND cm.user_id = v_user
          AND cm.left_at IS NULL
    ) THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;

    RETURN QUERY
    SELECT
        m.id,
        m.conversation_id,
        m.sender_id,
        m.type,
        -- Isi pesan yang dihapus tidak pernah dikirim, tetapi barisnya tetap
        -- ada supaya klien bisa menampilkan "pesan ini dihapus" di posisi yang
        -- benar, bukan meninggalkan lubang di riwayat.
        CASE WHEN m.deleted_at IS NOT NULL THEN '' ELSE COALESCE(m.body, '') END,
        m.reply_to_message_id,
        m.created_at,
        m.edited_at,
        (m.deleted_at IS NOT NULL)
    FROM messages m
    WHERE m.conversation_id = p_conversation
      AND (p_before IS NULL OR m.id < p_before)
    ORDER BY m.id DESC
    LIMIT LEAST(GREATEST(p_limit, 1), 100);
END;
$$;

REVOKE EXECUTE ON FUNCTION public.get_messages(uuid, uuid, integer) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.get_messages(uuid, uuid, integer) TO authenticated;

COMMIT;
