-- Suka / batal suka pada KOMENTAR reel (bukan reel-nya).
--
-- Kolom reel_comments.like_count sudah ada sejak migrasi 16, tapi tak pernah ada
-- tabel penampung "siapa menyukai komentar mana", jadi counter tak pernah bisa
-- naik dan klien tak tahu apakah pemakai sudah menyukai. Berkas ini menutup itu:
--
--   1. reel_comment_likes — satu baris per (komentar, pemakai), dedup lewat PK,
--      persis seperti reel_likes. Counter didenormalisasi di reel_comments.
--   2. like_reel_comment / unlike_reel_comment — idempoten, menjaga like_count
--      tetap akurat (naik hanya saat baris benar-benar baru, turun hanya saat
--      benar-benar terhapus, dijaga >= 0).
--   3. list_reel_comments di-recreate untuk menambah kolom `liked` (status suka
--      pemanggil), supaya UI bisa menampilkan hati terisi tanpa panggilan kedua.
--
-- Visibilitas dijaga: menyukai komentar pada reel yang tak boleh dilihat pemakai
-- ditolak lewat reel_visible_to, sama seperti menyukai reel.

BEGIN;

CREATE TABLE IF NOT EXISTS reel_comment_likes (
    comment_id uuid NOT NULL REFERENCES reel_comments(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (comment_id, user_id)
);

CREATE INDEX IF NOT EXISTS reel_comment_likes_comment_idx
    ON reel_comment_likes (comment_id);

ALTER TABLE reel_comment_likes ENABLE ROW LEVEL SECURITY;

-- ============================================================
-- SUKA / BATAL SUKA  (idempoten, jaga counter akurat)
-- ============================================================
CREATE OR REPLACE FUNCTION public.like_reel_comment(p_comment uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_reel uuid;
BEGIN
    SELECT c.reel_id INTO v_reel
    FROM reel_comments c
    WHERE c.id = p_comment AND c.deleted_at IS NULL;

    IF v_reel IS NULL THEN
        RAISE EXCEPTION 'komentar tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    IF NOT public.reel_visible_to(v_reel, v_user) THEN
        RAISE EXCEPTION 'reel tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    INSERT INTO reel_comment_likes (comment_id, user_id)
    VALUES (p_comment, v_user)
    ON CONFLICT DO NOTHING;

    IF FOUND THEN
        UPDATE reel_comments SET like_count = like_count + 1 WHERE id = p_comment;
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION public.unlike_reel_comment(p_comment uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    DELETE FROM reel_comment_likes
    WHERE comment_id = p_comment AND user_id = v_user;

    IF FOUND THEN
        UPDATE reel_comments SET like_count = GREATEST(like_count - 1, 0) WHERE id = p_comment;
    END IF;
END;
$$;

-- ============================================================
-- list_reel_comments — tambah kolom `liked` (status suka pemanggil)
-- ============================================================
-- RETURNS TABLE berubah, jadi DROP+CREATE. Dekode JSON PostgREST mengabaikan
-- kolom tak dikenal, jadi biner lama tetap aman menerima kolom baru ini.
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
        c.created_at
    FROM reel_comments c
    JOIN users u ON u.id = c.author_id AND u.deleted_at IS NULL
    LEFT JOIN user_profiles p ON p.user_id = c.author_id
    LEFT JOIN reel_comments rt ON rt.id = c.reply_to_comment_id
    LEFT JOIN users ru ON ru.id = rt.author_id AND ru.deleted_at IS NULL
    LEFT JOIN reel_comment_likes cl ON cl.comment_id = c.id AND cl.user_id = v_user
    WHERE c.reel_id = p_reel AND c.deleted_at IS NULL
      AND (p_before_at IS NULL OR (c.created_at, c.id) < (p_before_at, p_before_id))
    ORDER BY c.created_at DESC, c.id DESC
    LIMIT v_limit;
END;
$$;

-- ============================================================
-- GRANT
-- ============================================================
DO $$
BEGIN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.like_reel_comment(uuid) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.like_reel_comment(uuid) TO authenticated';
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.unlike_reel_comment(uuid) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.unlike_reel_comment(uuid) TO authenticated';
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.list_reel_comments(uuid,integer,timestamptz,uuid) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.list_reel_comments(uuid,integer,timestamptz,uuid) TO authenticated';
END;
$$;

COMMIT;
