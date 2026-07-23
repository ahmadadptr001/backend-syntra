-- Reels / Shorts — video pendek vertikal (Fase 2).
--
-- Prinsip yang dijaga ketat: DATABASE HANYA MENYIMPAN METADATA, tidak pernah
-- byte video. Kolom media_id menunjuk ke media_assets yang menunjuk ke object
-- storage — sama seperti story dan chat. Yang bisa "meledak" bukan ukuran byte
-- (itu di storage), melainkan JUMLAH BARIS. Dua sumber ledakan dijinakkan:
--
--   1. reel_views — kalau satu baris per tayangan, tabel ini tumbuh paling
--      cepat di seluruh sistem. Di sini di-dedup per (reel, penonton): satu
--      orang dihitung sekali, view_count hanya naik pada tayangan pertama.
--      Analitik per-play mentah (watch_ms, replay) sengaja TIDAK disimpan di
--      Postgres; itu untuk pipeline analitik terpisah (lihat docs/erd.md §3).
--   2. Hitungan like/komentar/simpan didenormalisasi sebagai counter di reels,
--      jadi feed tidak perlu agregasi mahal per permintaan.
--
-- Audio tracks, hashtags, dan ranking algoritmik (docs/erd.md §3) sengaja
-- ditunda — feed di sini kronologis. Semua itu bisa ditambah tanpa mengubah
-- tabel inti.

BEGIN;

-- ============================================================
-- TABEL
-- ============================================================

CREATE TABLE IF NOT EXISTS reels (
    id               uuid PRIMARY KEY,
    author_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    media_id         uuid NOT NULL REFERENCES media_assets(id),

    caption          text NOT NULL DEFAULT '' CHECK (char_length(caption) <= 2200),
    visibility       text NOT NULL DEFAULT 'public'
        CHECK (visibility IN ('public', 'followers', 'private')),
    status           text NOT NULL DEFAULT 'published'
        CHECK (status IN ('draft', 'published', 'removed')),
    comments_enabled boolean NOT NULL DEFAULT true,

    -- Counter denormalisasi — sumber kebenaran untuk feed.
    like_count       integer NOT NULL DEFAULT 0,
    comment_count    integer NOT NULL DEFAULT 0,
    view_count       integer NOT NULL DEFAULT 0,
    share_count      integer NOT NULL DEFAULT 0,

    published_at     timestamptz NOT NULL DEFAULT now(),
    created_at       timestamptz NOT NULL DEFAULT now(),
    deleted_at       timestamptz
);

-- Feed kronologis: cursor (published_at, id). Hanya yang tayang.
CREATE INDEX IF NOT EXISTS reels_feed_idx
    ON reels (published_at DESC, id DESC)
    WHERE status = 'published' AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS reels_author_idx
    ON reels (author_id, published_at DESC)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS reel_likes (
    reel_id    uuid NOT NULL REFERENCES reels(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (reel_id, user_id)
);

CREATE TABLE IF NOT EXISTS reel_comments (
    id                uuid PRIMARY KEY,
    reel_id           uuid NOT NULL REFERENCES reels(id) ON DELETE CASCADE,
    author_id         uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    parent_comment_id uuid REFERENCES reel_comments(id) ON DELETE CASCADE,
    body              text NOT NULL CHECK (char_length(body) BETWEEN 1 AND 1000),
    like_count        integer NOT NULL DEFAULT 0,
    created_at        timestamptz NOT NULL DEFAULT now(),
    deleted_at        timestamptz
);

CREATE INDEX IF NOT EXISTS reel_comments_reel_idx
    ON reel_comments (reel_id, created_at DESC)
    WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS reel_saves (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reel_id    uuid NOT NULL REFERENCES reels(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, reel_id)
);

CREATE INDEX IF NOT EXISTS reel_saves_user_idx ON reel_saves (user_id, created_at DESC);

-- Dedup per penonton — inilah rem utama terhadap ledakan baris.
CREATE TABLE IF NOT EXISTS reel_views (
    reel_id   uuid NOT NULL REFERENCES reels(id) ON DELETE CASCADE,
    viewer_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    viewed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (reel_id, viewer_id)
);

ALTER TABLE reels         ENABLE ROW LEVEL SECURITY;
ALTER TABLE reel_likes    ENABLE ROW LEVEL SECURITY;
ALTER TABLE reel_comments ENABLE ROW LEVEL SECURITY;
ALTER TABLE reel_saves    ENABLE ROW LEVEL SECURITY;
ALTER TABLE reel_views    ENABLE ROW LEVEL SECURITY;

-- ============================================================
-- HELPER: visibilitas satu reel bagi seorang pengguna
-- ============================================================
-- Sebuah reel terlihat kalau: milik sendiri, atau public (dan tak ada blokir),
-- atau followers-only dan pemanggil pengikut yang diterima (dan tak ada blokir).
-- private hanya untuk pemilik.
CREATE OR REPLACE FUNCTION public.reel_visible_to(p_reel uuid, p_user uuid)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
    SELECT EXISTS (
        SELECT 1
        FROM reels r
        WHERE r.id = p_reel
          AND r.deleted_at IS NULL
          AND r.status = 'published'
          AND (
                r.author_id = p_user
             OR (
                    NOT EXISTS (
                        SELECT 1 FROM blocks b
                        WHERE (b.blocker_id = r.author_id AND b.blocked_id = p_user)
                           OR (b.blocker_id = p_user AND b.blocked_id = r.author_id)
                    )
                    AND (
                          r.visibility = 'public'
                       OR (r.visibility = 'followers' AND EXISTS (
                              SELECT 1 FROM follows f
                              WHERE f.follower_id = p_user
                                AND f.followee_id = r.author_id
                                AND f.status = 'accepted'
                          ))
                    )
                )
          )
    );
$$;

-- ============================================================
-- MEMBUAT REEL
-- ============================================================
-- Media harus milik pemanggil, sudah 'ready', dan berupa video/gambar.
-- Menautkan media orang lain, media belum jadi, atau audio ditolak.
CREATE OR REPLACE FUNCTION public.create_reel(
    p_id               uuid,
    p_media            uuid,
    p_caption          text,
    p_visibility       text,
    p_comments_enabled boolean,
    p_published_at     timestamptz
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF COALESCE(p_visibility, 'public') NOT IN ('public', 'followers', 'private') THEN
        RAISE EXCEPTION 'visibilitas tidak valid' USING ERRCODE = '22023';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM media_assets ma
        WHERE ma.id = p_media
          AND ma.owner_id = v_user
          AND ma.kind IN ('video', 'image')
          AND ma.processing_status = 'ready'
    ) THEN
        -- Bisa berarti media bukan milik pemanggil, salah jenis, atau belum
        -- selesai diproses. Semua dipetakan ke 42501 (ditolak) di lapisan Go.
        RAISE EXCEPTION 'media tidak valid untuk reel' USING ERRCODE = '42501';
    END IF;

    INSERT INTO reels (id, author_id, media_id, caption, visibility, comments_enabled, published_at, created_at)
    VALUES (
        p_id, v_user, p_media,
        COALESCE(p_caption, ''),
        COALESCE(p_visibility, 'public'),
        COALESCE(p_comments_enabled, true),
        COALESCE(p_published_at, now()),
        now()
    );
END;
$$;

-- ============================================================
-- FEED & DETAIL
-- ============================================================
-- Baris reel lengkap dengan info penulis, media, dan status interaksi
-- pemanggil (liked/saved). Dipakai oleh feed, detail, dan daftar profil.
CREATE OR REPLACE FUNCTION public.list_reels_feed(
    p_limit     integer,
    p_before_at timestamptz,
    p_before_id uuid
)
RETURNS TABLE (
    id              uuid,
    author_id       uuid,
    author_username text,
    author_name     text,
    author_avatar   uuid,
    media_id        uuid,
    media_kind      text,
    storage_key     text,
    duration_ms     integer,
    caption         text,
    visibility      text,
    comments_enabled boolean,
    like_count      integer,
    comment_count   integer,
    view_count      integer,
    share_count     integer,
    liked           boolean,
    saved           boolean,
    published_at    timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_limit integer := LEAST(GREATEST(COALESCE(p_limit, 20), 1), 50);
BEGIN
    RETURN QUERY
    SELECT
        r.id, r.author_id, u.username::text, COALESCE(p.display_name, ''), p.avatar_media_id,
        r.media_id, ma.kind, ma.storage_key, ma.duration_ms,
        r.caption, r.visibility, r.comments_enabled,
        r.like_count, r.comment_count, r.view_count, r.share_count,
        (rl.user_id IS NOT NULL), (rs.user_id IS NOT NULL),
        r.published_at
    FROM reels r
    JOIN users        u  ON u.id  = r.author_id AND u.deleted_at IS NULL
    JOIN media_assets ma ON ma.id = r.media_id
    LEFT JOIN user_profiles p  ON p.user_id  = r.author_id
    LEFT JOIN reel_likes    rl ON rl.reel_id = r.id AND rl.user_id = v_user
    LEFT JOIN reel_saves    rs ON rs.reel_id = r.id AND rs.user_id = v_user
    WHERE r.status = 'published' AND r.deleted_at IS NULL
      AND (
            r.author_id = v_user
         OR (
                NOT EXISTS (
                    SELECT 1 FROM blocks b
                    WHERE (b.blocker_id = r.author_id AND b.blocked_id = v_user)
                       OR (b.blocker_id = v_user AND b.blocked_id = r.author_id)
                )
                AND (
                      r.visibility = 'public'
                   OR (r.visibility = 'followers' AND EXISTS (
                          SELECT 1 FROM follows f
                          WHERE f.follower_id = v_user AND f.followee_id = r.author_id AND f.status = 'accepted'
                      ))
                )
            )
      )
      AND (
            p_before_at IS NULL
         OR (r.published_at, r.id) < (p_before_at, p_before_id)
      )
    ORDER BY r.published_at DESC, r.id DESC
    LIMIT v_limit;
END;
$$;

-- Satu reel (untuk deep-link / halaman detail).
CREATE OR REPLACE FUNCTION public.get_reel(p_reel uuid)
RETURNS TABLE (
    id              uuid,
    author_id       uuid,
    author_username text,
    author_name     text,
    author_avatar   uuid,
    media_id        uuid,
    media_kind      text,
    storage_key     text,
    duration_ms     integer,
    caption         text,
    visibility      text,
    comments_enabled boolean,
    like_count      integer,
    comment_count   integer,
    view_count      integer,
    share_count     integer,
    liked           boolean,
    saved           boolean,
    published_at    timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF NOT public.reel_visible_to(p_reel, v_user) THEN
        RAISE EXCEPTION 'reel tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    RETURN QUERY
    SELECT
        r.id, r.author_id, u.username::text, COALESCE(p.display_name, ''), p.avatar_media_id,
        r.media_id, ma.kind, ma.storage_key, ma.duration_ms,
        r.caption, r.visibility, r.comments_enabled,
        r.like_count, r.comment_count, r.view_count, r.share_count,
        (rl.user_id IS NOT NULL), (rs.user_id IS NOT NULL),
        r.published_at
    FROM reels r
    JOIN users        u  ON u.id  = r.author_id
    JOIN media_assets ma ON ma.id = r.media_id
    LEFT JOIN user_profiles p  ON p.user_id  = r.author_id
    LEFT JOIN reel_likes    rl ON rl.reel_id = r.id AND rl.user_id = v_user
    LEFT JOIN reel_saves    rs ON rs.reel_id = r.id AND rs.user_id = v_user
    WHERE r.id = p_reel;
END;
$$;

-- Reel milik seorang pengguna (grid profil). Menghormati visibilitas &
-- blokir: yang bukan pemilik hanya melihat reel yang boleh ia lihat.
CREATE OR REPLACE FUNCTION public.list_user_reels(
    p_username  text,
    p_limit     integer,
    p_before_at timestamptz,
    p_before_id uuid
)
RETURNS TABLE (
    id              uuid,
    author_id       uuid,
    author_username text,
    author_name     text,
    author_avatar   uuid,
    media_id        uuid,
    media_kind      text,
    storage_key     text,
    duration_ms     integer,
    caption         text,
    visibility      text,
    comments_enabled boolean,
    like_count      integer,
    comment_count   integer,
    view_count      integer,
    share_count     integer,
    liked           boolean,
    saved           boolean,
    published_at    timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user   uuid := public.require_auth();
    v_author uuid;
    v_limit  integer := LEAST(GREATEST(COALESCE(p_limit, 20), 1), 50);
    v_self   boolean;
    v_follows boolean;
    v_blocked boolean;
BEGIN
    SELECT u.id INTO v_author FROM users u
    WHERE u.username = p_username AND u.deleted_at IS NULL;
    IF v_author IS NULL THEN
        RAISE EXCEPTION 'pengguna tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    v_self := (v_author = v_user);
    v_blocked := EXISTS (
        SELECT 1 FROM blocks b
        WHERE (b.blocker_id = v_author AND b.blocked_id = v_user)
           OR (b.blocker_id = v_user AND b.blocked_id = v_author)
    );
    v_follows := EXISTS (
        SELECT 1 FROM follows f
        WHERE f.follower_id = v_user AND f.followee_id = v_author AND f.status = 'accepted'
    );

    RETURN QUERY
    SELECT
        r.id, r.author_id, u.username::text, COALESCE(p.display_name, ''), p.avatar_media_id,
        r.media_id, ma.kind, ma.storage_key, ma.duration_ms,
        r.caption, r.visibility, r.comments_enabled,
        r.like_count, r.comment_count, r.view_count, r.share_count,
        (rl.user_id IS NOT NULL), (rs.user_id IS NOT NULL),
        r.published_at
    FROM reels r
    JOIN users        u  ON u.id  = r.author_id
    JOIN media_assets ma ON ma.id = r.media_id
    LEFT JOIN user_profiles p  ON p.user_id  = r.author_id
    LEFT JOIN reel_likes    rl ON rl.reel_id = r.id AND rl.user_id = v_user
    LEFT JOIN reel_saves    rs ON rs.reel_id = r.id AND rs.user_id = v_user
    WHERE r.author_id = v_author
      AND r.status = 'published' AND r.deleted_at IS NULL
      AND (
            v_self
         OR (NOT v_blocked AND (
                r.visibility = 'public'
             OR (r.visibility = 'followers' AND v_follows)
         ))
      )
      AND (p_before_at IS NULL OR (r.published_at, r.id) < (p_before_at, p_before_id))
    ORDER BY r.published_at DESC, r.id DESC
    LIMIT v_limit;
END;
$$;

-- Reel milik pemanggil sendiri (semua yang masih tayang, tanpa perlu username).
CREATE OR REPLACE FUNCTION public.list_my_reels(
    p_limit     integer,
    p_before_at timestamptz,
    p_before_id uuid
)
RETURNS TABLE (
    id              uuid,
    author_id       uuid,
    author_username text,
    author_name     text,
    author_avatar   uuid,
    media_id        uuid,
    media_kind      text,
    storage_key     text,
    duration_ms     integer,
    caption         text,
    visibility      text,
    comments_enabled boolean,
    like_count      integer,
    comment_count   integer,
    view_count      integer,
    share_count     integer,
    liked           boolean,
    saved           boolean,
    published_at    timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_limit integer := LEAST(GREATEST(COALESCE(p_limit, 20), 1), 50);
BEGIN
    RETURN QUERY
    SELECT
        r.id, r.author_id, u.username::text, COALESCE(p.display_name, ''), p.avatar_media_id,
        r.media_id, ma.kind, ma.storage_key, ma.duration_ms,
        r.caption, r.visibility, r.comments_enabled,
        r.like_count, r.comment_count, r.view_count, r.share_count,
        (rl.user_id IS NOT NULL), (rs.user_id IS NOT NULL),
        r.published_at
    FROM reels r
    JOIN users        u  ON u.id  = r.author_id
    JOIN media_assets ma ON ma.id = r.media_id
    LEFT JOIN user_profiles p  ON p.user_id  = r.author_id
    LEFT JOIN reel_likes    rl ON rl.reel_id = r.id AND rl.user_id = v_user
    LEFT JOIN reel_saves    rs ON rs.reel_id = r.id AND rs.user_id = v_user
    WHERE r.author_id = v_user
      AND r.status = 'published' AND r.deleted_at IS NULL
      AND (p_before_at IS NULL OR (r.published_at, r.id) < (p_before_at, p_before_id))
    ORDER BY r.published_at DESC, r.id DESC
    LIMIT v_limit;
END;
$$;

-- Reel yang disimpan pemanggil.
CREATE OR REPLACE FUNCTION public.list_saved_reels(
    p_limit     integer,
    p_before_at timestamptz,
    p_before_id uuid
)
RETURNS TABLE (
    id              uuid,
    author_id       uuid,
    author_username text,
    author_name     text,
    author_avatar   uuid,
    media_id        uuid,
    media_kind      text,
    storage_key     text,
    duration_ms     integer,
    caption         text,
    visibility      text,
    comments_enabled boolean,
    like_count      integer,
    comment_count   integer,
    view_count      integer,
    share_count     integer,
    liked           boolean,
    saved           boolean,
    published_at    timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_limit integer := LEAST(GREATEST(COALESCE(p_limit, 20), 1), 50);
BEGIN
    RETURN QUERY
    SELECT
        r.id, r.author_id, u.username::text, COALESCE(p.display_name, ''), p.avatar_media_id,
        r.media_id, ma.kind, ma.storage_key, ma.duration_ms,
        r.caption, r.visibility, r.comments_enabled,
        r.like_count, r.comment_count, r.view_count, r.share_count,
        true, true,
        r.published_at
    FROM reel_saves sv
    JOIN reels        r  ON r.id  = sv.reel_id AND r.status = 'published' AND r.deleted_at IS NULL
    JOIN users        u  ON u.id  = r.author_id
    JOIN media_assets ma ON ma.id = r.media_id
    LEFT JOIN user_profiles p ON p.user_id = r.author_id
    LEFT JOIN reel_likes   rl ON rl.reel_id = r.id AND rl.user_id = v_user
    WHERE sv.user_id = v_user
      AND public.reel_visible_to(r.id, v_user)
      AND (p_before_at IS NULL OR (sv.created_at, r.id) < (p_before_at, p_before_id))
    ORDER BY sv.created_at DESC, r.id DESC
    LIMIT v_limit;
END;
$$;

-- ============================================================
-- HAPUS
-- ============================================================
CREATE OR REPLACE FUNCTION public.delete_reel(p_reel uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    UPDATE reels SET deleted_at = now(), status = 'removed'
    WHERE id = p_reel AND author_id = v_user AND deleted_at IS NULL;

    IF NOT FOUND THEN
        -- Bukan milik pemanggil, atau tidak ada. Keduanya: tidak boleh.
        RAISE EXCEPTION 'reel tidak ditemukan atau bukan milikmu' USING ERRCODE = '42501';
    END IF;
END;
$$;

-- ============================================================
-- LIKE / UNLIKE  (idempoten, jaga counter tetap akurat)
-- ============================================================
CREATE OR REPLACE FUNCTION public.like_reel(p_reel uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF NOT public.reel_visible_to(p_reel, v_user) THEN
        RAISE EXCEPTION 'reel tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    INSERT INTO reel_likes (reel_id, user_id) VALUES (p_reel, v_user)
    ON CONFLICT DO NOTHING;

    IF FOUND THEN
        UPDATE reels SET like_count = like_count + 1 WHERE id = p_reel;
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION public.unlike_reel(p_reel uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    DELETE FROM reel_likes WHERE reel_id = p_reel AND user_id = v_user;
    IF FOUND THEN
        UPDATE reels SET like_count = GREATEST(like_count - 1, 0) WHERE id = p_reel;
    END IF;
END;
$$;

-- ============================================================
-- SIMPAN / BATAL SIMPAN
-- ============================================================
CREATE OR REPLACE FUNCTION public.save_reel(p_reel uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF NOT public.reel_visible_to(p_reel, v_user) THEN
        RAISE EXCEPTION 'reel tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;
    INSERT INTO reel_saves (user_id, reel_id) VALUES (v_user, p_reel)
    ON CONFLICT DO NOTHING;
END;
$$;

CREATE OR REPLACE FUNCTION public.unsave_reel(p_reel uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    DELETE FROM reel_saves WHERE user_id = v_user AND reel_id = p_reel;
END;
$$;

-- ============================================================
-- CATAT TAYANGAN  (di-dedup — rem ledakan baris)
-- ============================================================
-- Satu baris per (reel, penonton). view_count hanya naik pada tayangan
-- pertama; tayangan ulang tidak menambah baris maupun counter. Ini menjaga
-- reel_views tetap terikat pada jumlah penonton unik, bukan jumlah play.
CREATE OR REPLACE FUNCTION public.record_reel_view(p_reel uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF NOT public.reel_visible_to(p_reel, v_user) THEN
        RAISE EXCEPTION 'reel tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    INSERT INTO reel_views (reel_id, viewer_id) VALUES (p_reel, v_user)
    ON CONFLICT DO NOTHING;

    IF FOUND THEN
        UPDATE reels SET view_count = view_count + 1 WHERE id = p_reel;
    END IF;
END;
$$;

-- ============================================================
-- KOMENTAR
-- ============================================================
CREATE OR REPLACE FUNCTION public.add_reel_comment(
    p_id         uuid,
    p_reel       uuid,
    p_body       text,
    p_parent     uuid,
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

    INSERT INTO reel_comments (id, reel_id, author_id, parent_comment_id, body, created_at)
    VALUES (p_id, p_reel, v_user, p_parent, btrim(p_body), COALESCE(p_created_at, now()));

    UPDATE reels SET comment_count = comment_count + 1 WHERE id = p_reel;
END;
$$;

CREATE OR REPLACE FUNCTION public.list_reel_comments(
    p_reel      uuid,
    p_limit     integer,
    p_before_at timestamptz,
    p_before_id uuid
)
RETURNS TABLE (
    id                uuid,
    reel_id           uuid,
    author_id         uuid,
    author_username   text,
    author_name       text,
    author_avatar     uuid,
    parent_comment_id uuid,
    body              text,
    like_count        integer,
    created_at        timestamptz
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
        c.parent_comment_id, c.body, c.like_count, c.created_at
    FROM reel_comments c
    JOIN users u ON u.id = c.author_id AND u.deleted_at IS NULL
    LEFT JOIN user_profiles p ON p.user_id = c.author_id
    WHERE c.reel_id = p_reel AND c.deleted_at IS NULL
      AND (p_before_at IS NULL OR (c.created_at, c.id) < (p_before_at, p_before_id))
    ORDER BY c.created_at DESC, c.id DESC
    LIMIT v_limit;
END;
$$;

-- Hapus komentar: penulis komentar ATAU pemilik reel (moderasi kontennya
-- sendiri). Soft delete supaya balasan tak ikut hilang dari urutan.
CREATE OR REPLACE FUNCTION public.delete_reel_comment(p_comment uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_reel uuid;
BEGIN
    UPDATE reel_comments c
    SET deleted_at = now()
    WHERE c.id = p_comment AND c.deleted_at IS NULL
      AND (
            c.author_id = v_user
         OR EXISTS (SELECT 1 FROM reels r WHERE r.id = c.reel_id AND r.author_id = v_user)
      )
    RETURNING c.reel_id INTO v_reel;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'komentar tidak ditemukan atau bukan milikmu' USING ERRCODE = '42501';
    END IF;

    UPDATE reels SET comment_count = GREATEST(comment_count - 1, 0) WHERE id = v_reel;
END;
$$;

-- ============================================================
-- RLS & GRANT
-- ============================================================
-- Semua akses lewat fungsi SECURITY DEFINER di atas; tabel sendiri tidak
-- diberi policy longgar. Satu policy baca disediakan agar pemilik bisa
-- mengambil barisnya lewat PostgREST langsung bila perlu.
DROP POLICY IF EXISTS reels_owner_read ON reels;
CREATE POLICY reels_owner_read ON reels
    FOR SELECT USING (author_id = auth.uid());

DO $$
DECLARE
    fn text;
BEGIN
    FOREACH fn IN ARRAY ARRAY[
        'public.create_reel(uuid, uuid, text, text, boolean, timestamptz)',
        'public.list_reels_feed(integer, timestamptz, uuid)',
        'public.get_reel(uuid)',
        'public.list_user_reels(text, integer, timestamptz, uuid)',
        'public.list_my_reels(integer, timestamptz, uuid)',
        'public.list_saved_reels(integer, timestamptz, uuid)',
        'public.delete_reel(uuid)',
        'public.like_reel(uuid)',
        'public.unlike_reel(uuid)',
        'public.save_reel(uuid)',
        'public.unsave_reel(uuid)',
        'public.record_reel_view(uuid)',
        'public.add_reel_comment(uuid, uuid, text, uuid, timestamptz)',
        'public.list_reel_comments(uuid, integer, timestamptz, uuid)',
        'public.delete_reel_comment(uuid)'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO authenticated', fn);
    END LOOP;
END;
$$;

COMMIT;
