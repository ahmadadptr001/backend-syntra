-- Menghapus story, dan membersihkan yang sudah kedaluwarsa.
--
-- Dua hal yang belum ada sebelumnya:
--   * tidak ada cara apa pun menghapus story — endpoint maupun fungsi
--   * story kedaluwarsa hanya disaring dari hasil query, tidak pernah dibuang,
--     sehingga tabelnya tumbuh selamanya
--
-- Keputusan: story di-SOFT DELETE, medianya dibiarkan.
--
--   Soft delete karena permintaan moderasi dan permintaan hukum bisa datang
--   setelah story hilang dari layar; menghapus barisnya seketika membuat itu
--   mustahil dijawab.
--
--   Media dibiarkan karena satu media_asset boleh dipakai berkali-kali —
--   dikirim ulang sebagai pesan, misalnya. Menghapus byte-nya bersama story
--   akan merusak tautan di tempat lain. Media yatim dibersihkan terpisah oleh
--   purge_orphan_media(), yang menunggu masa tenggang.

BEGIN;

ALTER TABLE stories ADD COLUMN IF NOT EXISTS deleted_at timestamptz;

-- Index parsial: yang dibaca hampir selalu hanya story yang masih hidup.
CREATE INDEX IF NOT EXISTS stories_active_idx
    ON stories (author_id, created_at) WHERE deleted_at IS NULL;

-- ============================================================
-- 1. HAPUS STORY
-- ============================================================

CREATE OR REPLACE FUNCTION public.delete_story(p_story uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_owner uuid;
BEGIN
    SELECT s.author_id INTO v_owner
    FROM stories s
    WHERE s.id = p_story AND s.deleted_at IS NULL;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'story tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    IF v_owner <> v_user THEN
        RAISE EXCEPTION 'story bukan milik pengguna ini' USING ERRCODE = '42501';
    END IF;

    UPDATE stories s SET deleted_at = now() WHERE s.id = p_story;

    -- Catatan tontonan ikut dibuang: setelah story hilang, daftar penontonnya
    -- tidak berguna lagi dan hanya menyimpan siapa melihat apa tanpa alasan.
    DELETE FROM story_views sv WHERE sv.story_id = p_story;
END;
$$;

-- ============================================================
-- 2. LIST — menyaring yang sudah dihapus
-- ============================================================

DROP FUNCTION IF EXISTS public.list_stories();

CREATE FUNCTION public.list_stories()
RETURNS TABLE (
    id                uuid,
    author_id         uuid,
    author_username   text,
    author_name       text,
    author_avatar_key text,
    media_id          uuid,
    media_kind        text,
    storage_key       text,
    duration_ms       integer,
    created_at        timestamptz,
    expires_at        timestamptz,
    viewed            boolean
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
        COALESCE(av.storage_key, ''),
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
    LEFT JOIN user_profiles p  ON p.user_id = s.author_id
    LEFT JOIN media_assets  av ON av.id = p.avatar_media_id
    LEFT JOIN story_views   sv ON sv.story_id = s.id AND sv.viewer_id = v_user
    WHERE s.deleted_at IS NULL
      AND s.expires_at > now()
      AND (
            s.author_id = v_user
         OR EXISTS (
                SELECT 1 FROM follows f
                WHERE f.follower_id = v_user
                  AND f.followee_id = s.author_id
                  AND f.status = 'accepted'
            )
         OR EXISTS (
                SELECT 1
                FROM conversation_members me
                JOIN conversation_members them
                  ON them.conversation_id = me.conversation_id
                WHERE me.user_id = v_user
                  AND me.left_at IS NULL
                  AND them.user_id = s.author_id
                  AND them.left_at IS NULL
            )
          )
      AND NOT EXISTS (
            SELECT 1 FROM blocks b
            WHERE (b.blocker_id = s.author_id AND b.blocked_id = v_user)
               OR (b.blocker_id = v_user AND b.blocked_id = s.author_id)
          )
    ORDER BY (s.author_id = v_user) DESC, s.author_id, s.created_at;
END;
$$;

-- Story milik sendiri, termasuk yang sudah kedaluwarsa — untuk layar arsip
-- dan untuk memilih mana yang mau dihapus.
CREATE OR REPLACE FUNCTION public.list_my_stories(p_include_expired boolean DEFAULT false)
RETURNS TABLE (
    id          uuid,
    media_id    uuid,
    media_kind  text,
    storage_key text,
    duration_ms integer,
    view_count  integer,
    created_at  timestamptz,
    expires_at  timestamptz,
    is_expired  boolean
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
    SELECT s.id,
           s.media_id,
           ma.kind,
           ma.storage_key,
           ma.duration_ms,
           s.view_count,
           s.created_at,
           s.expires_at,
           (s.expires_at <= now())
    FROM stories s
    JOIN media_assets ma ON ma.id = s.media_id
    WHERE s.author_id = v_user
      AND s.deleted_at IS NULL
      AND (p_include_expired OR s.expires_at > now())
    ORDER BY s.created_at DESC;
END;
$$;

-- ============================================================
-- 3. PEMBERSIHAN
-- ============================================================

-- Membuang story yang sudah lama kedaluwarsa.
--
-- Masa tenggang ada dengan sengaja: story yang baru saja habis masih mungkin
-- jadi bahan laporan moderasi. Tujuh hari cukup untuk itu tanpa menahan data
-- selamanya.
CREATE OR REPLACE FUNCTION public.purge_expired_stories(p_grace_days integer DEFAULT 7)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_count integer;
BEGIN
    WITH gone AS (
        DELETE FROM stories s
        WHERE s.expires_at < now() - make_interval(days => GREATEST(p_grace_days, 1))
        RETURNING s.id
    )
    SELECT count(*) INTO v_count FROM gone;

    RETURN v_count;
END;
$$;

-- Membuang media yang tidak lagi ditunjuk apa pun.
--
-- Dipisahkan dari penghapusan story karena satu media boleh dipakai berkali-
-- kali. Yang dihapus di sini hanya barisnya; berkas di object storage dibuang
-- oleh backend setelah baris ini hilang, dan storage_key-nya dikembalikan
-- supaya backend tahu mana yang harus dihapus.
CREATE OR REPLACE FUNCTION public.purge_orphan_media(p_grace_days integer DEFAULT 7)
RETURNS TABLE (id uuid, storage_key text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
BEGIN
    RETURN QUERY
    WITH gone AS (
        DELETE FROM media_assets ma
        WHERE ma.created_at < now() - make_interval(days => GREATEST(p_grace_days, 1))
          AND NOT EXISTS (SELECT 1 FROM stories s             WHERE s.media_id = ma.id)
          AND NOT EXISTS (SELECT 1 FROM message_attachments a WHERE a.media_id = ma.id)
          AND NOT EXISTS (SELECT 1 FROM user_profiles p
                          WHERE p.avatar_media_id = ma.id OR p.cover_media_id = ma.id)
          AND NOT EXISTS (SELECT 1 FROM conversations c       WHERE c.avatar_media_id = ma.id)
          AND NOT EXISTS (SELECT 1 FROM rooms r               WHERE r.recording_media_id = ma.id)
        RETURNING ma.id, ma.storage_key
    )
    SELECT gone.id, gone.storage_key FROM gone;
END;
$$;

-- ============================================================
-- 4. HAK EKSEKUSI
-- ============================================================

DO $$
DECLARE
    fn text;
BEGIN
    FOREACH fn IN ARRAY ARRAY[
        'public.delete_story(uuid)',
        'public.list_stories()',
        'public.list_my_stories(boolean)'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO authenticated', fn);
    END LOOP;
END;
$$;

-- Fungsi pembersih hanya untuk pekerjaan latar; tidak diberikan ke pengguna.
REVOKE EXECUTE ON FUNCTION public.purge_expired_stories(integer) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.purge_orphan_media(integer)    FROM PUBLIC;

COMMIT;
