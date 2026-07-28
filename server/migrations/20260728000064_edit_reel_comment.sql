-- Edit komentar reel — penulisnya saja, dan jejaknya terlihat.
--
-- Dua bagian yang saling melengkapi:
--   1. update_reel_comment(uuid, text) untuk mengubah badan komentar.
--   2. kolom reel_comments.edited_at, di-set oleh fungsi itu, lalu dibawa keluar
--      oleh list_reel_comments supaya app bisa menandai "diedit".
--
-- Poin kedua bukan hiasan. Komentar yang bisa berubah diam-diam setelah dibalas
-- adalah cara mengubah arti percakapan orang lain secara surut; penanda itu yang
-- menjadikan edit sebagai koreksi, bukan penulisan ulang sejarah.
--
-- CATATAN: list_reel_comments di-CREATE OR REPLACE dengan menambah satu kolom di
-- AKHIR, jadi klien lama yang membaca per-nama tidak terpengaruh.

BEGIN;

ALTER TABLE reel_comments
    ADD COLUMN IF NOT EXISTS edited_at timestamptz;

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

    IF char_length(v_body) > 2200 THEN
        RAISE EXCEPTION 'komentar maksimal 2200 karakter' USING ERRCODE = '22023';
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

-- --- list_reel_comments (tambah edited_at di akhir) ---
-- Nama & tipe kolom lain tidak berubah, hanya bertambah satu di ujung.
DROP FUNCTION IF EXISTS public.list_reel_comments(uuid, integer, timestamptz, uuid);

CREATE FUNCTION public.list_reel_comments(
    p_reel      uuid,
    p_limit     integer,
    p_before_at timestamptz,
    p_before_id uuid
)
RETURNS TABLE (
    id                  uuid,
    reel_id             uuid,
    author_id           uuid,
    author_username     text,
    author_name         text,
    author_avatar       uuid,
    parent_comment_id   uuid,
    reply_to_comment_id uuid,
    reply_to_username   text,
    reply_to_body       text,
    body                text,
    like_count          integer,
    liked               boolean,
    media_id            uuid,
    media_kind          text,
    created_at          timestamptz,
    edited_at           timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_limit integer := LEAST(GREATEST(COALESCE(p_limit, 30), 1), 100);
BEGIN
    IF NOT public.reel_visible_to(p_reel, v_user) THEN
        RAISE EXCEPTION 'reel tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    RETURN QUERY
    SELECT
        c.id, c.reel_id, c.author_id, u.username::text, COALESCE(p.display_name, ''), p.avatar_media_id,
        c.parent_comment_id,
        c.reply_to_comment_id,
        ru.username::text,
        CASE WHEN rt.deleted_at IS NULL THEN left(rt.body, 140) ELSE NULL END,
        c.body, c.like_count,
        (cl.user_id IS NOT NULL),
        c.media_id, mm.kind,
        c.created_at,
        c.edited_at
    FROM reel_comments c
    JOIN users u ON u.id = c.author_id AND u.deleted_at IS NULL
    LEFT JOIN user_profiles p ON p.user_id = c.author_id
    LEFT JOIN reel_comments rt ON rt.id = c.reply_to_comment_id
    LEFT JOIN users ru ON ru.id = rt.author_id AND ru.deleted_at IS NULL
    LEFT JOIN reel_comment_likes cl ON cl.comment_id = c.id AND cl.user_id = v_user
    LEFT JOIN media_assets mm ON mm.id = c.media_id
    WHERE c.reel_id = p_reel AND c.deleted_at IS NULL
      AND (p_before_at IS NULL OR (c.created_at, c.id) < (p_before_at, p_before_id))
    ORDER BY c.created_at DESC, c.id DESC
    LIMIT v_limit;
END;
$$;

REVOKE EXECUTE ON FUNCTION public.list_reel_comments(uuid, integer, timestamptz, uuid) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.list_reel_comments(uuid, integer, timestamptz, uuid) TO authenticated;

COMMIT;
