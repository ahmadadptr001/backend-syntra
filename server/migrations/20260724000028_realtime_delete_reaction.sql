-- Siaran realtime untuk hapus pesan & reaksi (pesan-untuk-backend.md poin 11.1 & 11.2).
--
-- Keduanya sudah bekerja, tetapi hanya di sisi database: perangkat lawan bicara
-- baru tahu setelah membuka ulang chat / memuat ulang reaksi. Agar backend bisa
-- MENYIARKAN perubahannya lewat WebSocket, ia butuh id percakapan untuk
-- menentukan topik "conversation:<id>". Sampai kini kedua fungsi RETURNS void,
-- jadi id itu tidak pernah kembali ke Go.
--
-- Migrasi ini mengubah keduanya agar mengembalikan conversation_id. Karena
-- mengubah tipe kembalian, fungsinya harus DROP dulu — CREATE OR REPLACE tidak
-- bisa mengganti return type. Kolom keluaran diberi awalan out_ mengikuti
-- konvensi edit_message, supaya tidak bentrok dengan nama kolom di body fungsi.

BEGIN;

-- ============================================================
-- 1. HAPUS PESAN  -> kembalikan conversation_id
-- ============================================================

DROP FUNCTION IF EXISTS public.delete_message(uuid);

CREATE FUNCTION public.delete_message(p_message uuid)
RETURNS TABLE (out_conversation_id uuid)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user   uuid := public.require_auth();
    v_sender uuid;
    v_conv   uuid;
BEGIN
    SELECT m.sender_id, m.conversation_id
      INTO v_sender, v_conv
    FROM messages m
    WHERE m.id = p_message AND m.deleted_at IS NULL;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'pesan tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    IF v_sender <> v_user THEN
        RAISE EXCEPTION 'pesan bukan milik pengguna ini' USING ERRCODE = '42501';
    END IF;

    UPDATE messages m SET deleted_at = now() WHERE m.id = p_message;

    RETURN QUERY SELECT v_conv;
END;
$$;

-- ============================================================
-- 2. REAKSI  -> kembalikan conversation_id
-- ============================================================

DROP FUNCTION IF EXISTS public.react_to_message(uuid, text);

CREATE FUNCTION public.react_to_message(
    p_message uuid,
    p_emoji   text
)
RETURNS TABLE (out_conversation_id uuid)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_conv uuid;
BEGIN
    SELECT m.conversation_id INTO v_conv
    FROM messages m
    WHERE m.id = p_message AND m.deleted_at IS NULL;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'pesan tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM conversation_members cm
        WHERE cm.conversation_id = v_conv AND cm.user_id = v_user AND cm.left_at IS NULL
    ) THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;

    IF p_emoji IS NULL OR btrim(p_emoji) = '' THEN
        DELETE FROM message_reactions r WHERE r.message_id = p_message AND r.user_id = v_user;
    ELSE
        INSERT INTO message_reactions (message_id, user_id, emoji)
        VALUES (p_message, v_user, p_emoji)
        ON CONFLICT (message_id, user_id) DO UPDATE SET emoji = EXCLUDED.emoji, created_at = now();
    END IF;

    RETURN QUERY SELECT v_conv;
END;
$$;

-- ============================================================
-- 3. HAK EKSEKUSI (fungsi dibuat ulang, grant-nya ikut hilang)
-- ============================================================

DO $$
BEGIN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.delete_message(uuid) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.delete_message(uuid) TO authenticated';
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.react_to_message(uuid, text) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.react_to_message(uuid, text) TO authenticated';
END;
$$;

COMMIT;
