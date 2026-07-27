-- send_message harus menghormati blokir.
--
-- MASALAH. create_direct_conversation menolak membuat percakapan baru antara dua
-- orang yang saling memblokir (42501). Tetapi send_message hanya memeriksa satu
-- hal: apakah pengirim anggota percakapan itu. Jadi percakapan yang sudah ada
-- SEBELUM blokir tetap bisa dipakai -- dan itu justru kasus yang paling sering
-- terjadi: orang memblokir seseorang yang memang sudah pernah mengobrol dengannya.
--
-- Hasilnya: memblokir seseorang tidak menghentikan pesannya sama sekali. Ia tetap
-- masuk, tetap menambah unread_count, tetap memicu notifikasi push. Satu-satunya
-- yang menyembunyikannya adalah aplikasi, dan itu bukan penegakan.
--
-- PERBAIKAN. Untuk percakapan DIRECT, tolak pengiriman bila ada blokir dua arah
-- dengan lawan bicara. Dua arah karena keduanya masuk akal: yang diblokir jelas
-- tidak boleh mengirim, dan yang memblokir juga tidak seharusnya bisa -- aplikasi
-- sudah mengganti kolom ketik dengan "Kamu memblokir kontak ini", jadi kalau
-- server tetap menerima, satu-satunya yang menahan adalah UI.
--
-- Percakapan GRUP sengaja tidak disentuh: blokir bersifat satu-lawan-satu, dan
-- mengeluarkan seseorang dari grup karena satu anggota memblokirnya adalah
-- keputusan produk yang berbeda -- bukan sesuatu yang boleh diselundupkan lewat
-- perbaikan ini.
--
-- ERRCODE sengaja P0003, BUKAN 42501. Lapisan Go memetakan 42501 ke
-- chat.ErrNotMember, yang berbalas "kamu bukan anggota percakapan ini" -- keliru
-- dan membingungkan untuk kasus blokir, karena orangnya memang anggota. P0003
-- dipetakan ke chat.ErrNotAllowed sehingga balasannya berbunyi "percakapan tidak
-- diizinkan".
--
-- Tanda tangan dan tipe kembalian tidak berubah => CREATE OR REPLACE. ASCII murni.

BEGIN;

CREATE OR REPLACE FUNCTION public.send_message(
    p_id           uuid,
    p_conversation uuid,
    p_type         text,
    p_body         text,
    p_reply_to     uuid,
    p_created_at   timestamptz,
    p_media        uuid[] DEFAULT NULL
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_pos   smallint := 0;
    v_mid   uuid;
    v_other uuid;
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM conversation_members
        WHERE conversation_id = p_conversation AND user_id = v_user AND left_at IS NULL
    ) THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;

    -- Lawan bicara, hanya untuk percakapan direct. NULL untuk grup, sehingga
    -- pemeriksaan blokir di bawah otomatis dilewati.
    SELECT cm.user_id INTO v_other
    FROM conversation_members cm
    JOIN conversations c ON c.id = cm.conversation_id
    WHERE cm.conversation_id = p_conversation
      AND cm.user_id <> v_user
      AND c.type = 'direct'
    LIMIT 1;

    IF v_other IS NOT NULL AND EXISTS (
        SELECT 1 FROM blocks
        WHERE (blocker_id = v_user  AND blocked_id = v_other)
           OR (blocker_id = v_other AND blocked_id = v_user)
    ) THEN
        RAISE EXCEPTION 'percakapan tidak diizinkan' USING ERRCODE = 'P0003';
    END IF;

    INSERT INTO messages (id, conversation_id, sender_id, type, body, reply_to_message_id, created_at)
    VALUES (p_id, p_conversation, v_user, COALESCE(p_type, 'text'), p_body, p_reply_to, p_created_at);

    FOREACH v_mid IN ARRAY COALESCE(p_media, ARRAY[]::uuid[])
    LOOP
        -- Hanya media milik pengirim yang boleh dilampirkan; mencegah menautkan
        -- berkas orang lain ke pesan sendiri.
        CONTINUE WHEN NOT EXISTS (
            SELECT 1 FROM media_assets ma WHERE ma.id = v_mid AND ma.owner_id = v_user
        );
        INSERT INTO message_attachments (message_id, media_id, position)
        VALUES (p_id, v_mid, v_pos)
        ON CONFLICT DO NOTHING;
        v_pos := v_pos + 1;
    END LOOP;

    UPDATE conversations
    SET last_message_id = p_id, last_message_at = p_created_at
    WHERE id = p_conversation;

    UPDATE conversation_members
    SET unread_count = unread_count + 1
    WHERE conversation_id = p_conversation AND user_id <> v_user AND left_at IS NULL;
END;
$$;

COMMIT;
