-- Edit judul lagu komunitas — hanya pemiliknya, hanya kolom title.
--
-- Setara delete_music_track: fungsi SECURITY DEFINER yang memeriksa kepemilikan
-- lewat author_id = require_auth(), lalu meng-UPDATE title. Tak menyentuh media,
-- cover, atau visibility. Judul dipangkas ke 200 char sama seperti create.

BEGIN;

CREATE OR REPLACE FUNCTION public.update_music_track_title(p_id uuid, p_title text)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_title text := LEFT(COALESCE(trim(p_title), ''), 200);
    v_rows  integer;
BEGIN
    IF v_title = '' THEN
        RAISE EXCEPTION 'judul tidak boleh kosong' USING ERRCODE = '22023';
    END IF;

    UPDATE music_tracks
    SET title = v_title
    WHERE id = p_id AND author_id = v_user AND deleted_at IS NULL;

    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows = 0 THEN
        -- Bukan milik pemanggil, atau sudah tak ada.
        RAISE EXCEPTION 'lagu tidak ditemukan' USING ERRCODE = '42501';
    END IF;
END;
$$;

REVOKE EXECUTE ON FUNCTION public.update_music_track_title(uuid, text) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.update_music_track_title(uuid, text) TO authenticated;

COMMIT;
