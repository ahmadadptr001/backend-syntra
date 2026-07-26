-- Musik komunitas — lagu yang diunggah pengguna dari perangkatnya, publik &
-- bisa dicari (tab Musik → rail "Unggahan komunitas" + pencarian).
--
-- Sama prinsipnya dengan reels/story: DATABASE HANYA MENYIMPAN METADATA, tidak
-- pernah byte audio. Kolom media_id menunjuk ke media_assets (kind 'audio') yang
-- sudah diunggah & 'ready'; cover_media_id (opsional) menunjuk ke gambar sampul.
--
-- Sengaja minimal: tanpa like/komentar/simpan/tayangan — sebuah track hanya
-- perlu bisa diterbitkan, muncul di feed publik, dicari, dan dihapus pemiliknya.
-- Semua itu bisa ditambah kemudian tanpa mengubah tabel inti.

BEGIN;

-- ============================================================
-- TABEL
-- ============================================================
CREATE TABLE IF NOT EXISTS music_tracks (
    id             uuid PRIMARY KEY,
    author_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    media_id       uuid NOT NULL REFERENCES media_assets(id),
    cover_media_id uuid REFERENCES media_assets(id) ON DELETE SET NULL,

    title       text NOT NULL DEFAULT '' CHECK (char_length(title) <= 200),
    artist      text NOT NULL DEFAULT '' CHECK (char_length(artist) <= 200),
    duration_ms integer NOT NULL DEFAULT 0,
    visibility  text NOT NULL DEFAULT 'public'
        CHECK (visibility IN ('public', 'private')),

    created_at  timestamptz NOT NULL DEFAULT now(),
    deleted_at  timestamptz
);

-- Feed kronologis: cursor sederhana (created_at, id). Hanya yang publik & hidup.
CREATE INDEX IF NOT EXISTS music_tracks_feed_idx
    ON music_tracks (created_at DESC, id DESC)
    WHERE visibility = 'public' AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS music_tracks_author_idx
    ON music_tracks (author_id, created_at DESC)
    WHERE deleted_at IS NULL;

ALTER TABLE music_tracks ENABLE ROW LEVEL SECURITY;

-- ============================================================
-- MEMBUAT LAGU
-- ============================================================
-- Media audio harus milik pemanggil, sudah 'ready', dan berjenis 'audio'.
-- Cover (opsional) harus gambar milik pemanggil yang 'ready'; kalau tidak valid
-- ia diabaikan (jadi NULL) — sampul yang salah tak boleh menggagalkan unggahan.
CREATE OR REPLACE FUNCTION public.create_music_track(
    p_id          uuid,
    p_media       uuid,
    p_cover       uuid,
    p_title       text,
    p_artist      text,
    p_duration_ms integer
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_cover uuid := NULL;
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM media_assets ma
        WHERE ma.id = p_media
          AND ma.owner_id = v_user
          AND ma.kind = 'audio'
          AND ma.processing_status = 'ready'
    ) THEN
        -- Media bukan milik pemanggil, bukan audio, atau belum selesai diproses.
        RAISE EXCEPTION 'media tidak valid untuk musik' USING ERRCODE = '42501';
    END IF;

    IF p_cover IS NOT NULL AND EXISTS (
        SELECT 1 FROM media_assets ma
        WHERE ma.id = p_cover
          AND ma.owner_id = v_user
          AND ma.kind = 'image'
          AND ma.processing_status = 'ready'
    ) THEN
        v_cover := p_cover;
    END IF;

    INSERT INTO music_tracks (id, author_id, media_id, cover_media_id, title, artist, duration_ms, visibility, created_at)
    VALUES (
        p_id, v_user, p_media, v_cover,
        LEFT(COALESCE(p_title, ''), 200),
        LEFT(COALESCE(p_artist, ''), 200),
        GREATEST(COALESCE(p_duration_ms, 0), 0),
        'public',
        now()
    );
END;
$$;

-- ============================================================
-- FEED & PENCARIAN
-- ============================================================
-- Baris track lengkap dengan info penulis + storage_key audio & sampul, supaya
-- lapisan Go bisa menyusun URL publik. Hanya track publik & hidup, dan bukan dari
-- penulis yang saling blokir dengan pemanggil.
CREATE OR REPLACE FUNCTION public.list_music_feed(p_limit integer)
RETURNS TABLE (
    id                uuid,
    author_id         uuid,
    author_username   text,
    author_name       text,
    storage_key       text,
    cover_storage_key text,
    title             text,
    artist            text,
    duration_ms       integer,
    created_at        timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_limit integer := LEAST(GREATEST(COALESCE(p_limit, 40), 1), 100);
BEGIN
    RETURN QUERY
    SELECT
        m.id, m.author_id, u.username::text, COALESCE(p.display_name, ''),
        ma.storage_key, cov.storage_key,
        m.title, m.artist, m.duration_ms, m.created_at
    FROM music_tracks m
    JOIN users        u   ON u.id  = m.author_id AND u.deleted_at IS NULL
    JOIN media_assets ma  ON ma.id = m.media_id
    LEFT JOIN media_assets cov ON cov.id = m.cover_media_id
    LEFT JOIN user_profiles p  ON p.user_id = m.author_id
    WHERE m.visibility = 'public' AND m.deleted_at IS NULL
      AND NOT EXISTS (
            SELECT 1 FROM blocks b
            WHERE (b.blocker_id = m.author_id AND b.blocked_id = v_user)
               OR (b.blocker_id = v_user AND b.blocked_id = m.author_id)
      )
    ORDER BY m.created_at DESC, m.id DESC
    LIMIT v_limit;
END;
$$;

-- Pencarian berdasarkan judul ATAU artis (case-insensitive).
CREATE OR REPLACE FUNCTION public.search_music_tracks(p_q text, p_limit integer)
RETURNS TABLE (
    id                uuid,
    author_id         uuid,
    author_username   text,
    author_name       text,
    storage_key       text,
    cover_storage_key text,
    title             text,
    artist            text,
    duration_ms       integer,
    created_at        timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_limit integer := LEAST(GREATEST(COALESCE(p_limit, 40), 1), 100);
    v_pat   text   := '%' || COALESCE(trim(p_q), '') || '%';
BEGIN
    IF COALESCE(trim(p_q), '') = '' THEN
        RETURN;
    END IF;
    RETURN QUERY
    SELECT
        m.id, m.author_id, u.username::text, COALESCE(p.display_name, ''),
        ma.storage_key, cov.storage_key,
        m.title, m.artist, m.duration_ms, m.created_at
    FROM music_tracks m
    JOIN users        u   ON u.id  = m.author_id AND u.deleted_at IS NULL
    JOIN media_assets ma  ON ma.id = m.media_id
    LEFT JOIN media_assets cov ON cov.id = m.cover_media_id
    LEFT JOIN user_profiles p  ON p.user_id = m.author_id
    WHERE m.visibility = 'public' AND m.deleted_at IS NULL
      AND (m.title ILIKE v_pat OR m.artist ILIKE v_pat)
      AND NOT EXISTS (
            SELECT 1 FROM blocks b
            WHERE (b.blocker_id = m.author_id AND b.blocked_id = v_user)
               OR (b.blocker_id = v_user AND b.blocked_id = m.author_id)
      )
    ORDER BY m.created_at DESC, m.id DESC
    LIMIT v_limit;
END;
$$;

-- ============================================================
-- HAPUS (pemilik saja, soft delete)
-- ============================================================
CREATE OR REPLACE FUNCTION public.delete_music_track(p_id uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_rows integer;
BEGIN
    UPDATE music_tracks
    SET deleted_at = now()
    WHERE id = p_id AND author_id = v_user AND deleted_at IS NULL;
    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows = 0 THEN
        -- Bukan milik pemanggil, atau sudah tak ada.
        RAISE EXCEPTION 'lagu tidak ditemukan' USING ERRCODE = '42501';
    END IF;
END;
$$;

-- ============================================================
-- RLS & GRANT
-- ============================================================
-- Semua akses lewat fungsi SECURITY DEFINER di atas; satu policy baca disediakan
-- agar pemilik bisa mengambil barisnya lewat PostgREST langsung bila perlu.
DROP POLICY IF EXISTS music_tracks_owner_read ON music_tracks;
CREATE POLICY music_tracks_owner_read ON music_tracks
    FOR SELECT USING (author_id = auth.uid());

DO $$
DECLARE
    fn text;
BEGIN
    FOREACH fn IN ARRAY ARRAY[
        'public.create_music_track(uuid, uuid, uuid, text, text, integer)',
        'public.list_music_feed(integer)',
        'public.search_music_tracks(text, integer)',
        'public.delete_music_track(uuid)'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO authenticated', fn);
    END LOOP;
END;
$$;

COMMIT;
