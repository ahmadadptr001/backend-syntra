-- Ubah pesan — melengkapi hapus pesan yang sudah ada.
--
-- Hanya pengirim yang boleh mengubah, hanya pesan teks (mengubah lampiran atau
-- pesan sistem tidak masuk akal), dan hanya yang belum dihapus. edited_at diisi
-- supaya klien bisa menandai "diedit".
--
-- Tidak perlu menyentuh ringkasan percakapan: list_conversations membaca body
-- pesan terakhir secara langsung lewat join, jadi preview ikut berubah sendiri.
--
-- Nama kolom keluaran diawali out_ agar tidak pernah bentrok dengan kolom tabel
-- (kebiasaan sejak start_call sempat gagal karena ambiguitas — migrasi 19/22).

BEGIN;

CREATE OR REPLACE FUNCTION public.edit_message(p_message uuid, p_body text)
RETURNS TABLE (out_conversation_id uuid, out_edited_at timestamptz)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user   uuid := public.require_auth();
    v_sender uuid;
    v_type   text;
    v_conv   uuid;
    v_body   text := btrim(coalesce(p_body, ''));
    v_now    timestamptz := now();
BEGIN
    IF v_body = '' THEN
        RAISE EXCEPTION 'isi pesan kosong' USING ERRCODE = '22023';
    END IF;
    IF char_length(v_body) > 4000 THEN
        RAISE EXCEPTION 'isi pesan melebihi batas' USING ERRCODE = '22023';
    END IF;

    SELECT m.sender_id, m.type, m.conversation_id
      INTO v_sender, v_type, v_conv
    FROM messages m
    WHERE m.id = p_message AND m.deleted_at IS NULL;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'pesan tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    IF v_sender <> v_user THEN
        RAISE EXCEPTION 'pesan bukan milik pengguna ini' USING ERRCODE = '42501';
    END IF;

    IF v_type <> 'text' THEN
        RAISE EXCEPTION 'hanya pesan teks yang bisa diubah' USING ERRCODE = '22023';
    END IF;

    UPDATE messages m
    SET body = v_body, edited_at = v_now
    WHERE m.id = p_message;

    RETURN QUERY SELECT v_conv, v_now;
END;
$$;

DO $$
BEGIN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.edit_message(uuid, text) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.edit_message(uuid, text) TO authenticated';
END;
$$;

COMMIT;
