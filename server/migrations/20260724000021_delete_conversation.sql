-- Hapus seluruh obrolan (chat & grup) dari sisi pemanggil.
--
-- Berbeda dari:
--   - DELETE /conversations/{id}/messages (clear_conversation): hanya
--     mengosongkan tampilan pesan, percakapan tetap di daftar.
--   - POST /conversations/{id}/leave: keluar grup, tapi tak menyembunyikan.
--
-- delete_conversation MENGHAPUS percakapan dari daftar pemanggil sepenuhnya:
--   - grup: keluar (umumkan + wariskan kepemilikan bila owner) lalu sembunyikan
--   - direct: sembunyikan dari daftar & bersihkan tampilan
--
-- Anggota lain tidak terpengaruh — ini aksi personal, bukan menghapus percakapan
-- untuk semua orang.

BEGIN;

CREATE OR REPLACE FUNCTION public.delete_conversation(p_conversation uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_type text;
    v_role text;
    v_name text;
    v_last uuid;
BEGIN
    SELECT c.type INTO v_type FROM conversations c WHERE c.id = p_conversation;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'percakapan tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    SELECT cm.role INTO v_role FROM conversation_members cm
    WHERE cm.conversation_id = p_conversation AND cm.user_id = v_user AND cm.left_at IS NULL;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;

    -- Grup: mengumumkan keluar dan mewariskan kepemilikan bila pemanggil owner,
    -- supaya grup tidak menjadi yatim tanpa pengelola.
    IF v_type = 'group' THEN
        SELECT COALESCE(p.display_name, u.username::text) INTO v_name
        FROM users u LEFT JOIN user_profiles p ON p.user_id = u.id WHERE u.id = v_user;
        PERFORM public.post_system_message(p_conversation, v_name || ' keluar dari grup');

        IF v_role = 'owner' THEN
            UPDATE conversation_members cm SET role = 'owner'
            WHERE cm.conversation_id = p_conversation AND cm.left_at IS NULL
              AND cm.user_id = (
                  SELECT m.user_id FROM conversation_members m
                  WHERE m.conversation_id = p_conversation AND m.left_at IS NULL AND m.user_id <> v_user
                  ORDER BY CASE m.role WHEN 'admin' THEN 0 ELSE 1 END, m.joined_at
                  LIMIT 1
              );
        END IF;
    END IF;

    -- Sembunyikan dari daftar (left_at) dan bersihkan tampilan (cleared_before_id)
    -- untuk pemanggil. list_conversations menyaring left_at IS NULL, jadi
    -- percakapan hilang dari daftar pemanggil.
    SELECT max(m.id) INTO v_last FROM messages m WHERE m.conversation_id = p_conversation;
    UPDATE conversation_members cm
    SET left_at           = now(),
        cleared_before_id = COALESCE(v_last, cm.cleared_before_id),
        unread_count      = 0
    WHERE cm.conversation_id = p_conversation AND cm.user_id = v_user;
END;
$$;

-- create_direct_conversation harus MENGHIDUPKAN kembali membership yang sudah
-- di-"hapus" (left_at terisi), bukan membuat percakapan direct baru yang
-- duplikat. Versi lama mencari percakapan yang KEDUA anggotanya left_at IS NULL,
-- sehingga setelah delete_conversation ia gagal menemukannya dan membuat yang
-- baru — meninggalkan dua percakapan direct antara pasangan yang sama.
CREATE OR REPLACE FUNCTION public.create_direct_conversation(p_other uuid)
RETURNS uuid
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_conv uuid;
BEGIN
    IF p_other IS NULL OR p_other = v_user THEN
        RAISE EXCEPTION 'lawan bicara tidak valid' USING ERRCODE = '22023';
    END IF;

    IF NOT EXISTS (SELECT 1 FROM users WHERE id = p_other AND deleted_at IS NULL) THEN
        RAISE EXCEPTION 'pengguna tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    IF EXISTS (
        SELECT 1 FROM blocks
        WHERE (blocker_id = v_user AND blocked_id = p_other)
           OR (blocker_id = p_other AND blocked_id = v_user)
    ) THEN
        RAISE EXCEPTION 'percakapan tidak diizinkan' USING ERRCODE = '42501';
    END IF;

    -- Cari percakapan direct yang sudah ada TANPA menyaring left_at, supaya
    -- percakapan yang pernah dihapus pemanggil ditemukan kembali.
    SELECT c.id INTO v_conv
    FROM conversations c
    JOIN conversation_members a ON a.conversation_id = c.id AND a.user_id = v_user
    JOIN conversation_members b ON b.conversation_id = c.id AND b.user_id = p_other
    WHERE c.type = 'direct'
    LIMIT 1;

    IF v_conv IS NOT NULL THEN
        -- Hidupkan kembali membership pemanggil bila sebelumnya dihapus.
        UPDATE conversation_members
        SET left_at = NULL
        WHERE conversation_id = v_conv AND user_id = v_user AND left_at IS NOT NULL;
        RETURN v_conv;
    END IF;

    v_conv := gen_random_uuid();
    INSERT INTO conversations (id, type, created_by) VALUES (v_conv, 'direct', v_user);
    INSERT INTO conversation_members (conversation_id, user_id, role) VALUES
        (v_conv, v_user,  'member'),
        (v_conv, p_other, 'member');

    RETURN v_conv;
END;
$$;

REVOKE EXECUTE ON FUNCTION public.delete_conversation(uuid) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.delete_conversation(uuid) TO authenticated;

COMMIT;
