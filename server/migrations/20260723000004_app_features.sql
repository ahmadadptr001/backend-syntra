-- Melengkapi backend agar seluruh layar di README-APP.md bisa berjalan dengan
-- data sungguhan. Lihat docs/app-backend-alignment.md untuk analisis gapnya.
--
-- Isi:
--   1. Tabel story (belum pernah dibuat — di ERD ada, di migrasi belum)
--   2. RPC riwayat pesan
--   3. RPC membuat percakapan (pribadi idempoten + grup)
--   4. RPC direktori pengguna untuk hasil scan QR
--   5. RPC story
--   6. RPC pendaftaran media
--   7. RLS policy + hak eksekusi

BEGIN;

-- ============================================================
-- 1. TABEL STORY
-- ============================================================

CREATE TABLE IF NOT EXISTS stories (
    id         uuid PRIMARY KEY,
    author_id  uuid NOT NULL REFERENCES users(id)        ON DELETE CASCADE,
    media_id   uuid NOT NULL REFERENCES media_assets(id) ON DELETE CASCADE,
    visibility text NOT NULL DEFAULT 'followers'
        CHECK (visibility IN ('public', 'followers', 'close_friends')),
    overlays   jsonb NOT NULL DEFAULT '{}'::jsonb,
    view_count integer NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now(),

    -- Story tidak dihapus saat kedaluwarsa, hanya disaring dari hasil query.
    -- Alasannya: permintaan moderasi dan permintaan hukum bisa datang setelah
    -- story hilang dari layar.
    expires_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS stories_author_idx  ON stories (author_id, created_at);
CREATE INDEX IF NOT EXISTS stories_expiry_idx  ON stories (expires_at);

CREATE TABLE IF NOT EXISTS story_views (
    story_id  uuid NOT NULL REFERENCES stories(id) ON DELETE CASCADE,
    viewer_id uuid NOT NULL REFERENCES users(id)   ON DELETE CASCADE,
    viewed_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (story_id, viewer_id)
);

ALTER TABLE stories     ENABLE ROW LEVEL SECURITY;
ALTER TABLE story_views ENABLE ROW LEVEL SECURITY;

-- ============================================================
-- 2. RIWAYAT PESAN
-- ============================================================

-- Cursor memakai id pesan, bukan waktu. Id adalah UUIDv7 yang terurut secara
-- waktu, jadi `id < p_before` sudah cukup dan tidak butuh index tambahan.
-- Memakai created_at sebagai cursor akan melewatkan pesan ketika dua pesan
-- punya timestamp identik — sesuatu yang pasti terjadi di percakapan ramai.
CREATE OR REPLACE FUNCTION public.get_messages(
    p_conversation uuid,
    p_before       uuid,
    p_limit        integer
)
RETURNS TABLE (
    id                  uuid,
    conversation_id     uuid,
    sender_id           uuid,
    type                text,
    body                text,
    reply_to_message_id uuid,
    created_at          timestamptz,
    edited_at           timestamptz,
    is_deleted          boolean
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM conversation_members
        WHERE conversation_id = p_conversation AND user_id = v_user AND left_at IS NULL
    ) THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;

    RETURN QUERY
    SELECT
        m.id,
        m.conversation_id,
        m.sender_id,
        m.type,
        -- Isi pesan yang dihapus tidak pernah dikirim, tetapi barisnya tetap
        -- ada supaya klien bisa menampilkan "pesan ini dihapus" di posisi yang
        -- benar, bukan meninggalkan lubang di riwayat.
        CASE WHEN m.deleted_at IS NOT NULL THEN '' ELSE COALESCE(m.body, '') END,
        m.reply_to_message_id,
        m.created_at,
        m.edited_at,
        (m.deleted_at IS NOT NULL)
    FROM messages m
    WHERE m.conversation_id = p_conversation
      AND (p_before IS NULL OR m.id < p_before)
    ORDER BY m.id DESC
    LIMIT LEAST(GREATEST(p_limit, 1), 100);
END;
$$;

-- ============================================================
-- 3. MEMBUAT PERCAKAPAN
-- ============================================================

-- Idempoten dengan sengaja: memanggilnya dua kali untuk orang yang sama
-- mengembalikan percakapan yang sudah ada. Tanpa itu, satu pasang pengguna
-- bisa berakhir punya beberapa percakapan pribadi paralel — kerusakan data
-- yang sangat sulit dirapikan setelah pesan mulai tersebar di antaranya.
CREATE OR REPLACE FUNCTION public.create_direct_conversation(p_other uuid)
RETURNS uuid
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user   uuid := public.require_auth();
    v_conv   uuid;
BEGIN
    IF p_other IS NULL OR p_other = v_user THEN
        RAISE EXCEPTION 'lawan bicara tidak valid' USING ERRCODE = '22023';
    END IF;

    IF NOT EXISTS (SELECT 1 FROM users WHERE id = p_other AND deleted_at IS NULL) THEN
        RAISE EXCEPTION 'pengguna tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    -- Blokir berlaku dua arah: yang memblokir maupun yang diblokir sama-sama
    -- tidak bisa memulai percakapan.
    IF EXISTS (
        SELECT 1 FROM blocks
        WHERE (blocker_id = v_user AND blocked_id = p_other)
           OR (blocker_id = p_other AND blocked_id = v_user)
    ) THEN
        RAISE EXCEPTION 'percakapan tidak diizinkan' USING ERRCODE = '42501';
    END IF;

    SELECT c.id INTO v_conv
    FROM conversations c
    JOIN conversation_members a ON a.conversation_id = c.id AND a.user_id = v_user  AND a.left_at IS NULL
    JOIN conversation_members b ON b.conversation_id = c.id AND b.user_id = p_other AND b.left_at IS NULL
    WHERE c.type = 'direct'
    LIMIT 1;

    IF v_conv IS NOT NULL THEN
        RETURN v_conv;
    END IF;

    v_conv := gen_random_uuid();

    INSERT INTO conversations (id, type, created_by) VALUES (v_conv, 'direct', v_user);
    INSERT INTO conversation_members (conversation_id, user_id, role) VALUES
        (v_conv, v_user,  'member'),
        (v_conv, p_other, 'member');

    RETURN v_conv;
END;
$$;

CREATE OR REPLACE FUNCTION public.create_group_conversation(
    p_title   text,
    p_members uuid[]
)
RETURNS uuid
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_conv uuid := gen_random_uuid();
BEGIN
    IF p_title IS NULL OR btrim(p_title) = '' THEN
        RAISE EXCEPTION 'judul grup tidak boleh kosong' USING ERRCODE = '22023';
    END IF;

    INSERT INTO conversations (id, type, title, created_by)
    VALUES (v_conv, 'group', btrim(p_title), v_user);

    INSERT INTO conversation_members (conversation_id, user_id, role)
    VALUES (v_conv, v_user, 'owner');

    -- DISTINCT dan penyaringan pembuat mencegah duplikat kalau klien
    -- mengirimkan dirinya sendiri di dalam daftar anggota.
    INSERT INTO conversation_members (conversation_id, user_id, role)
    SELECT v_conv, u.id, 'member'
    FROM unnest(COALESCE(p_members, ARRAY[]::uuid[])) AS m(id)
    JOIN users u ON u.id = m.id AND u.deleted_at IS NULL
    WHERE u.id <> v_user
    GROUP BY u.id
    ON CONFLICT DO NOTHING;

    RETURN v_conv;
END;
$$;

-- ============================================================
-- 4. DIREKTORI PENGGUNA — untuk hasil scan QR
-- ============================================================

CREATE OR REPLACE FUNCTION public.find_user(p_username text)
RETURNS TABLE (
    id              uuid,
    username        text,
    display_name    text,
    avatar_media_id uuid
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
BEGIN
    PERFORM public.require_auth();

    RETURN QUERY
    SELECT u.id,
           u.username::text,
           COALESCE(p.display_name, ''),
           p.avatar_media_id
    FROM users u
    LEFT JOIN user_profiles p ON p.user_id = u.id
    WHERE u.username = p_username::citext
      AND u.deleted_at IS NULL
      AND u.account_status = 'active'
    LIMIT 1;
END;
$$;

-- ============================================================
-- 5. MEDIA
-- ============================================================

-- Dipanggil SETELAH klien selesai mengunggah ke object storage. Backend hanya
-- mencatat metadatanya; byte medianya tidak pernah melewati server Go.
CREATE OR REPLACE FUNCTION public.register_media(
    p_id          uuid,
    p_kind        text,
    p_storage_key text,
    p_mime        text,
    p_size        bigint,
    p_duration_ms integer,
    p_width       integer,
    p_height      integer
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    INSERT INTO media_assets
        (id, owner_id, kind, storage_key, mime_type, size_bytes, duration_ms, width, height, processing_status)
    VALUES
        (p_id, v_user, p_kind, p_storage_key, p_mime, p_size, p_duration_ms, p_width, p_height, 'ready');
END;
$$;

-- ============================================================
-- 6. STORY
-- ============================================================

CREATE OR REPLACE FUNCTION public.create_story(
    p_id         uuid,
    p_media      uuid,
    p_visibility text,
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
    IF NOT EXISTS (SELECT 1 FROM media_assets WHERE id = p_media AND owner_id = v_user) THEN
        RAISE EXCEPTION 'media bukan milik pengguna ini' USING ERRCODE = '42501';
    END IF;

    INSERT INTO stories (id, author_id, media_id, visibility, created_at, expires_at)
    VALUES (p_id, v_user, p_media, COALESCE(p_visibility, 'followers'),
            p_created_at, p_created_at + interval '24 hours');
END;
$$;

-- Story aktif dari diri sendiri dan orang yang diikuti, urut per penulis lalu
-- per waktu — bentuk yang langsung bisa dikelompokkan klien menjadi story row.
CREATE OR REPLACE FUNCTION public.list_stories()
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
    created_at      timestamptz,
    expires_at      timestamptz,
    viewed          boolean
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    RETURN QUERY
    SELECT
        s.id,
        s.author_id,
        u.username::text,
        COALESCE(p.display_name, ''),
        p.avatar_media_id,
        s.media_id,
        ma.kind,
        ma.storage_key,
        ma.duration_ms,
        s.created_at,
        s.expires_at,
        (sv.viewer_id IS NOT NULL)
    FROM stories s
    JOIN users        u  ON u.id  = s.author_id AND u.deleted_at IS NULL
    JOIN media_assets ma ON ma.id = s.media_id
    LEFT JOIN user_profiles p ON p.user_id = s.author_id
    LEFT JOIN story_views  sv ON sv.story_id = s.id AND sv.viewer_id = v_user
    WHERE s.expires_at > now()
      AND (
            s.author_id = v_user
         OR EXISTS (
                SELECT 1 FROM follows f
                WHERE f.follower_id = v_user
                  AND f.followee_id = s.author_id
                  AND f.status = 'accepted'
            )
          )
      AND NOT EXISTS (
            SELECT 1 FROM blocks b
            WHERE (b.blocker_id = s.author_id AND b.blocked_id = v_user)
               OR (b.blocker_id = v_user AND b.blocked_id = s.author_id)
          )
    -- Story milik sendiri selalu di depan, persis seperti di layar aplikasi.
    ORDER BY (s.author_id = v_user) DESC, s.author_id, s.created_at;
END;
$$;

CREATE OR REPLACE FUNCTION public.mark_story_viewed(p_story uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    INSERT INTO story_views (story_id, viewer_id)
    VALUES (p_story, v_user)
    ON CONFLICT DO NOTHING;

    -- Counter hanya naik saat baris benar-benar baru, sehingga menonton ulang
    -- tidak menggelembungkan angkanya.
    IF FOUND THEN
        UPDATE stories SET view_count = view_count + 1 WHERE id = p_story;
    END IF;
END;
$$;

-- ============================================================
-- 7. HAK EKSEKUSI
-- ============================================================

DO $$
DECLARE
    fn text;
BEGIN
    FOREACH fn IN ARRAY ARRAY[
        'public.get_messages(uuid, uuid, integer)',
        'public.create_direct_conversation(uuid)',
        'public.create_group_conversation(text, uuid[])',
        'public.find_user(text)',
        'public.register_media(uuid, text, text, text, bigint, integer, integer, integer)',
        'public.create_story(uuid, uuid, text, timestamptz)',
        'public.list_stories()',
        'public.mark_story_viewed(uuid)'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO authenticated', fn);
    END LOOP;
END;
$$;

-- ============================================================
-- 8. RLS POLICY UNTUK STORY
-- ============================================================
-- Server Go berjalan lewat fungsi SECURITY DEFINER di atas dan tidak tunduk
-- pada policy ini. Policy dibutuhkan kalau klien Kotlin nanti menyentuh
-- Supabase langsung, misalnya untuk Realtime.

CREATE POLICY stories_select_visible ON stories
    FOR SELECT TO authenticated
    USING (
        expires_at > now()
        AND (
            author_id = auth.uid()
            OR EXISTS (
                SELECT 1 FROM follows f
                WHERE f.follower_id = auth.uid()
                  AND f.followee_id = stories.author_id
                  AND f.status = 'accepted'
            )
        )
    );

CREATE POLICY stories_delete_own ON stories
    FOR DELETE TO authenticated
    USING (author_id = auth.uid());

-- Pemilik story boleh melihat siapa saja yang menontonnya; penonton hanya
-- boleh melihat catatannya sendiri.
CREATE POLICY story_views_visible ON story_views
    FOR SELECT TO authenticated
    USING (
        viewer_id = auth.uid()
        OR EXISTS (
            SELECT 1 FROM stories s
            WHERE s.id = story_views.story_id AND s.author_id = auth.uid()
        )
    );

COMMIT;
