-- Perbaiki batas panjang saat MENGEDIT komentar reel.
--
-- Bug: update_reel_comment (migrasi 64) memakai batas 2200 karakter — angka itu
-- terbawa dari batas KETERANGAN reel di migrasi 63 (reels.caption memang <= 2200).
-- Tapi badan komentar dibatasi CHECK di tabelnya sendiri: reel_comments_body_check
-- => char_length(body) <= 1000 (migrasi 16, dilonggarkan soal kosong di migrasi 62).
--
-- Akibatnya: mengedit komentar menjadi 1001–2200 karakter LOLOS pengecekan fungsi,
-- lalu DITOLAK oleh CHECK tabel dengan galat mentah Postgres (23514) — bukan pesan
-- ramah "komentar maksimal ... karakter". Di app tampil "Gagal menyimpan" lalu
-- teks dikembalikan. Jalur MEMBUAT komentar (add_reel_comment) sudah benar <= 1000,
-- jadi hanya jalur EDIT yang tak konsisten.
--
-- Perbaikan: samakan batas edit dengan tabel & jalur buat => 1000 karakter, dan
-- pesannya ikut diperbaiki. CREATE OR REPLACE, jadi aman dijalankan baik migrasi 64
-- sudah diterapkan maupun belum.

BEGIN;

CREATE OR REPLACE FUNCTION public.update_reel_comment(p_comment uuid, p_body text)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_body text := COALESCE(trim(p_body), '');
    v_has_media boolean;
    v_rows integer;
BEGIN
    SELECT media_id IS NOT NULL INTO v_has_media
    FROM reel_comments
    WHERE id = p_comment AND author_id = v_user AND deleted_at IS NULL;

    IF v_has_media IS NULL THEN
        -- Bukan milik pemanggil, atau sudah tak ada. Sengaja tidak dibedakan.
        RAISE EXCEPTION 'komentar tidak ditemukan' USING ERRCODE = '42501';
    END IF;

    -- Sama seperti saat membuat: badan boleh kosong HANYA kalau ada lampiran.
    -- Tanpa cek ini, edit bisa dipakai untuk mengosongkan komentar yang tak
    -- punya media, menyisakan baris hantu yang tak bisa dihapus siapa pun.
    IF v_body = '' AND NOT v_has_media THEN
        RAISE EXCEPTION 'komentar tidak boleh kosong' USING ERRCODE = '22023';
    END IF;

    -- Samakan dengan CHECK tabel (reel_comments_body_check) & add_reel_comment.
    IF char_length(v_body) > 1000 THEN
        RAISE EXCEPTION 'komentar maksimal 1000 karakter' USING ERRCODE = '22023';
    END IF;

    UPDATE reel_comments
    SET body = v_body,
        edited_at = now()
    WHERE id = p_comment AND author_id = v_user AND deleted_at IS NULL;

    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows = 0 THEN
        RAISE EXCEPTION 'komentar tidak ditemukan' USING ERRCODE = '42501';
    END IF;
END;
$$;

REVOKE EXECUTE ON FUNCTION public.update_reel_comment(uuid, text) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.update_reel_comment(uuid, text) TO authenticated;

COMMIT;
