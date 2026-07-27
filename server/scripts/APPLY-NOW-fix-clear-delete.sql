-- Perbaiki clear_conversation & delete_conversation: SEMUA pemanggilan gagal.
--
-- MASALAH. Keduanya menghitung batas "sudah dibersihkan" dengan
--     SELECT max(m.id) INTO v_last FROM messages m WHERE m.conversation_id = ...
-- dan messages.id bertipe uuid. PostgreSQL TIDAK punya agregat max(uuid), jadi
-- setiap pemanggilan gagal dengan SQLSTATE 42883 ("function max(uuid) does not
-- exist"). PostgREST memetakan itu ke HTTP 404, lapisan Go menerjemahkan 404
-- menjadi ErrNotFound, dan aplikasi menerima "sumber daya tidak ditemukan".
--
-- Akibatnya "bersihkan obrolan" dan "hapus percakapan" TIDAK PERNAH berhasil,
-- untuk siapa pun, sejak fungsi ini dibuat. Yang membuatnya sulit dilacak: 404
-- terlihat persis seperti "percakapan tidak ada", padahal percakapannya ada dan
-- GET /messages pada id yang sama menjawab 200 -- baris tetap kembali setiap
-- kali daftar disegarkan.
--
-- Ini bug yang SAMA PERSIS dengan yang diperbaiki migrasi 43 untuk
-- delete_reel_comment (max(reel_id)). Pola perbaikannya juga sama.
--
-- PERBAIKAN. Tidak perlu agregat sama sekali: ambil satu baris terbaru dengan
-- ORDER BY ... LIMIT 1. Urutan uuid di PostgreSQL adalah urutan byte, dan id
-- pesan adalah UUIDv7 yang byte awalnya timestamp -- jadi "id terbesar" dan
-- "pesan terbaru" adalah hal yang sama, persis seperti yang diasumsikan max()
-- semula. Perilaku tidak berubah; hanya caranya yang sah secara SQL.
--
-- Sisa badan fungsi disalin apa adanya dari migrasi 13 dan 21. Signature, hak
-- akses, dan SQLSTATE lain tetap. ASCII murni.

BEGIN;

-- 1. clear_conversation (dari migrasi 13)
CREATE OR REPLACE FUNCTION public.clear_conversation(p_conversation uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_last uuid;
BEGIN
    SELECT m.id INTO v_last
    FROM messages m
    WHERE m.conversation_id = p_conversation
    ORDER BY m.id DESC
    LIMIT 1;

    UPDATE conversation_members cm
    SET cleared_before_id = COALESCE(v_last, cm.cleared_before_id),
        unread_count      = 0
    WHERE cm.conversation_id = p_conversation AND cm.user_id = v_user AND cm.left_at IS NULL;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;
END;
$$;

-- 2. delete_conversation (dari migrasi 21)
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
    SELECT m.id INTO v_last
    FROM messages m
    WHERE m.conversation_id = p_conversation
    ORDER BY m.id DESC
    LIMIT 1;

    UPDATE conversation_members cm
    SET left_at           = now(),
        cleared_before_id = COALESCE(v_last, cm.cleared_before_id),
        unread_count      = 0
    WHERE cm.conversation_id = p_conversation AND cm.user_id = v_user;
END;
$$;

COMMIT;
