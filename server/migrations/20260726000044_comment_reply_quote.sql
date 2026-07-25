-- Balasan komentar menyimpan "dibalas ke komentar mana" yang PERSIS, agar UI bisa
-- menampilkan kutipan komentar target di dalam balasan itu sendiri.
--
-- Threading tetap satu tingkat (parent_comment_id menunjuk komentar puncak). Kolom
-- baru reply_to_comment_id menunjuk komentar TEPAT yang dibalas -- termasuk sebuah
-- balasan di dalam thread -- dan hanya untuk keperluan tampilan (kutipan).
--
-- Kompatibilitas tanpa jeda: add_reel_comment versi 6-argumen dibuat sebagai
-- OVERLOAD (versi 5-argumen lama dibiarkan), jadi biner lama/baru sama-sama jalan.
-- list_reel_comments di-DROP+CREATE karena RETURNS TABLE berubah; dekode JSON
-- PostgREST mengabaikan kolom yang tak dikenal, jadi biner lama tetap aman.
-- Ditulis ASCII murni.

BEGIN;

ALTER TABLE reel_comments
    ADD COLUMN IF NOT EXISTS reply_to_comment_id uuid REFERENCES reel_comments(id) ON DELETE SET NULL;

-- --- add_reel_comment (overload 6-argumen) ---
CREATE OR REPLACE FUNCTION public.add_reel_comment(
    p_id         uuid,
    p_reel       uuid,
    p_body       text,
    p_parent     uuid,
    p_reply_to   uuid,
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
    IF p_body IS NULL OR char_length(btrim(p_body)) = 0 THEN
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
    -- Kalau tidak valid, abaikan (NULL) -- jangan menggagalkan komentar.
    IF p_reply_to IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM reel_comments c
        WHERE c.id = p_reply_to AND c.reel_id = p_reel AND c.deleted_at IS NULL
    ) THEN
        p_reply_to := NULL;
    END IF;

    INSERT INTO reel_comments (id, reel_id, author_id, parent_comment_id, reply_to_comment_id, body, created_at)
    VALUES (p_id, p_reel, v_user, p_parent, p_reply_to, btrim(p_body), COALESCE(p_created_at, now()));

    UPDATE reels SET comment_count = comment_count + 1 WHERE id = p_reel;
END;
$$;

-- --- list_reel_comments (tambah kolom target balasan) ---
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
        c.body, c.like_count, c.created_at
    FROM reel_comments c
    JOIN users u ON u.id = c.author_id AND u.deleted_at IS NULL
    LEFT JOIN user_profiles p ON p.user_id = c.author_id
    LEFT JOIN reel_comments rt ON rt.id = c.reply_to_comment_id
    LEFT JOIN users ru ON ru.id = rt.author_id AND ru.deleted_at IS NULL
    WHERE c.reel_id = p_reel AND c.deleted_at IS NULL
      AND (p_before_at IS NULL OR (c.created_at, c.id) < (p_before_at, p_before_id))
    ORDER BY c.created_at DESC, c.id DESC
    LIMIT v_limit;
END;
$$;

-- Re-GRANT (fungsi baru + list yang di-recreate).
DO $$
BEGIN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.add_reel_comment(uuid,uuid,text,uuid,uuid,timestamptz) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.add_reel_comment(uuid,uuid,text,uuid,uuid,timestamptz) TO authenticated';
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.list_reel_comments(uuid,integer,timestamptz,uuid) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.list_reel_comments(uuid,integer,timestamptz,uuid) TO authenticated';
END;
$$;

COMMIT;
