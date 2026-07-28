-- Foto pada KOMENTAR reel — komentar boleh membawa satu gambar (opsional).
--
-- Sampai kini komentar hanya teks. Berkas ini menambah lampiran gambar tunggal,
-- mengikuti pola yang sama seperti reel: DATABASE HANYA MENYIMPAN METADATA
-- (media_id → media_assets → storage), tak pernah byte. Yang berubah:
--
--   1. reel_comments.media_id — nullable, menunjuk media_assets. ON DELETE SET
--      NULL supaya komentar tak ikut hilang bila medianya dibersihkan.
--   2. add_reel_comment overload 7-argumen (…, p_media, p_created_at): boleh
--      badan kosong ASAL ada media; memvalidasi media milik pemanggil, jenis
--      'image', dan sudah 'ready' — persis seperti create_reel.
--   3. list_reel_comments di-recreate lagi (di atas migrasi 61) untuk menambah
--      kolom media_id + media_kind, supaya repo bisa menyusun URL foto komentar.
--
-- Kompatibilitas: overload 6-argumen (migrasi 44) DIBIARKAN, jadi biner lama
-- tetap bisa mengirim komentar teks. Dekode JSON PostgREST mengabaikan kolom tak
-- dikenal, jadi biner lama aman menerima kolom media baru.

BEGIN;

ALTER TABLE reel_comments
    ADD COLUMN IF NOT EXISTS media_id uuid REFERENCES media_assets(id) ON DELETE SET NULL;

-- CHECK lama mewajibkan badan >= 1 char. Komentar hanya-foto berbadan kosong,
-- jadi longgarkan: badan boleh kosong ASAL ada media, tetap dibatasi <= 1000.
ALTER TABLE reel_comments DROP CONSTRAINT IF EXISTS reel_comments_body_check;
ALTER TABLE reel_comments ADD CONSTRAINT reel_comments_body_check
    CHECK (char_length(body) <= 1000 AND (char_length(btrim(body)) >= 1 OR media_id IS NOT NULL));

-- --- add_reel_comment (overload 7-argumen: + p_media) ---
CREATE OR REPLACE FUNCTION public.add_reel_comment(
    p_id         uuid,
    p_reel       uuid,
    p_body       text,
    p_parent     uuid,
    p_reply_to   uuid,
    p_media      uuid,
    p_created_at timestamptz
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    -- Kosong hanya boleh bila ada media. Teks kosong + tanpa media = ditolak.
    IF (p_body IS NULL OR char_length(btrim(p_body)) = 0) AND p_media IS NULL THEN
        RAISE EXCEPTION 'komentar kosong' USING ERRCODE = '22023';
    END IF;

    IF NOT public.reel_visible_to(p_reel, v_user) THEN
        RAISE EXCEPTION 'reel tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    IF NOT EXISTS (SELECT 1 FROM reels r WHERE r.id = p_reel AND r.comments_enabled) THEN
        RAISE EXCEPTION 'komentar dimatikan' USING ERRCODE = '42501';
    END IF;

    -- Balasan hanya satu tingkat: parent tidak boleh punya parent.
    IF p_parent IS NOT NULL AND EXISTS (
        SELECT 1 FROM reel_comments c
        WHERE c.id = p_parent AND (c.reel_id <> p_reel OR c.parent_comment_id IS NOT NULL OR c.deleted_at IS NOT NULL)
    ) THEN
        RAISE EXCEPTION 'balasan tidak valid' USING ERRCODE = '22023';
    END IF;

    -- Target kutipan hanya untuk tampilan: harus komentar hidup di reel yang sama.
    IF p_reply_to IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM reel_comments c
        WHERE c.id = p_reply_to AND c.reel_id = p_reel AND c.deleted_at IS NULL
    ) THEN
        p_reply_to := NULL;
    END IF;

    -- Media (kalau ada) harus milik pemanggil, gambar, dan sudah 'ready'.
    IF p_media IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM media_assets ma
        WHERE ma.id = p_media
          AND ma.owner_id = v_user
          AND ma.kind = 'image'
          AND ma.processing_status = 'ready'
    ) THEN
        RAISE EXCEPTION 'media tidak valid untuk komentar' USING ERRCODE = '42501';
    END IF;

    INSERT INTO reel_comments (id, reel_id, author_id, parent_comment_id, reply_to_comment_id, media_id, body, created_at)
    VALUES (p_id, p_reel, v_user, p_parent, p_reply_to, p_media, btrim(COALESCE(p_body, '')), COALESCE(p_created_at, now()));

    UPDATE reels SET comment_count = comment_count + 1 WHERE id = p_reel;
END;
$$;

-- --- list_reel_comments (tambah media_id + media_kind) ---
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
    created_at          timestamptz
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
        c.created_at
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

-- --- GRANT (overload baru + list yang di-recreate) ---
DO $$
BEGIN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.add_reel_comment(uuid,uuid,text,uuid,uuid,uuid,timestamptz) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.add_reel_comment(uuid,uuid,text,uuid,uuid,uuid,timestamptz) TO authenticated';
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.list_reel_comments(uuid,integer,timestamptz,uuid) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.list_reel_comments(uuid,integer,timestamptz,uuid) TO authenticated';
END;
$$;

COMMIT;
