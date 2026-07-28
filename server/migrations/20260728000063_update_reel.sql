-- Edit sebuah short — hanya pemiliknya, hanya kolom yang benar-benar dikirim.
--
-- Sampai sekarang reel hanya bisa dibuat dan dihapus. Salah ketik di keterangan
-- berarti hapus lalu unggah ulang: tayangan, suka, dan komentarnya hilang untuk
-- memperbaiki satu huruf.
--
-- Bentuknya setara update_music_track_title: SECURITY DEFINER, kepemilikan
-- diperiksa lewat author_id = require_auth(). Bedanya tiga kolom sekaligus dan
-- semuanya OPSIONAL — NULL berarti "jangan sentuh", bukan "kosongkan". Itu yang
-- membuat PATCH yang hanya membawa caption tidak diam-diam menerbitkan ulang
-- reel privat sebagai publik.
--
-- Media dan counter tidak pernah tersentuh: mengganti videonya bukan edit, itu
-- postingan lain.

BEGIN;

CREATE OR REPLACE FUNCTION public.update_reel(
    p_id       uuid,
    p_caption  text    DEFAULT NULL,
    p_vis      text    DEFAULT NULL,
    p_comments boolean DEFAULT NULL
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user    uuid := public.require_auth();
    v_caption text;
    v_rows    integer;
BEGIN
    -- Keterangan boleh dikosongkan (string kosong), jadi yang dibedakan adalah
    -- NULL (tidak dikirim) versus '' (dikirim, minta dikosongkan).
    IF p_caption IS NOT NULL THEN
        v_caption := COALESCE(trim(p_caption), '');
        IF char_length(v_caption) > 2200 THEN
            RAISE EXCEPTION 'keterangan maksimal 2200 karakter' USING ERRCODE = '22023';
        END IF;
    END IF;

    IF p_vis IS NOT NULL AND p_vis NOT IN ('public', 'followers', 'private') THEN
        RAISE EXCEPTION 'visibility tidak dikenal' USING ERRCODE = '22023';
    END IF;

    UPDATE reels
    SET caption          = COALESCE(v_caption, caption),
        visibility       = COALESCE(p_vis, visibility),
        comments_enabled = COALESCE(p_comments, comments_enabled)
    WHERE id = p_id
      AND author_id = v_user
      AND deleted_at IS NULL
      AND status <> 'removed';

    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows = 0 THEN
        -- Bukan milik pemanggil, atau sudah tak ada. Sengaja tidak dibedakan:
        -- membedakannya memberi tahu orang asing bahwa id itu memang ada.
        RAISE EXCEPTION 'short tidak ditemukan' USING ERRCODE = '42501';
    END IF;
END;
$$;

REVOKE EXECUTE ON FUNCTION public.update_reel(uuid, text, text, boolean) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.update_reel(uuid, text, text, boolean) TO authenticated;

COMMIT;
