-- =====================================================================
-- Syntra — migrasi tertunda 23..30, digabung untuk sekali jalan.
--
-- Cara pakai: Supabase Dashboard > SQL Editor > tempel SELURUH isi berkas
-- ini > Run. Migrasi terakhir yang sudah dijalankan sebelumnya: 22_sfu_webhook.
--
-- Aman diulang: semua memakai CREATE OR REPLACE / IF NOT EXISTS / DROP IF
-- EXISTS. Tiap bagian transaksinya sendiri; kalau satu gagal, yang sebelumnya
-- tetap tersimpan dan bisa diulang.
--
-- Setelah ini aktif: daftar follower, edit pesan, starred, privasi presence,
-- hapus media, realtime hapus/reaksi, audiens story.new, DAN pencarian/
-- penemuan pengguna (search_users) — kunci agar aplikasi tidak terasa
-- "dunia sendiri".
-- =====================================================================




-- =====================================================================
-- BAGIAN: 20260724000023_list_followers.sql
-- =====================================================================

-- Daftar follower — kebalikan dari list_following.
--
-- list_following menjawab "siapa yang aku ikuti"; ini menjawab "siapa yang
-- mengikutiku" (atau mengikuti orang lain). Layar profil butuh keduanya, dan
-- selama ini hanya satu arah yang tersedia.
--
-- p_username NULL berarti follower milik pemanggil sendiri; kalau diisi,
-- follower milik pengguna itu. Hanya follower berstatus 'accepted' yang
-- ditampilkan — permintaan yang masih pending bukan follower.

BEGIN;

CREATE OR REPLACE FUNCTION public.list_followers(p_username text DEFAULT NULL)
RETURNS TABLE (
    id              uuid,
    username        text,
    display_name    text,
    avatar_media_id uuid,
    status          text,
    created_at      timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user   uuid := public.require_auth();
    v_target uuid;
BEGIN
    IF p_username IS NULL THEN
        v_target := v_user;
    ELSE
        SELECT u.id INTO v_target
        FROM users u
        WHERE u.username = p_username::citext
          AND u.deleted_at IS NULL
          AND u.account_status = 'active';

        IF NOT FOUND THEN
            RAISE EXCEPTION 'pengguna tidak ditemukan' USING ERRCODE = 'P0002';
        END IF;
    END IF;

    RETURN QUERY
    SELECT u.id,
           u.username::text,
           COALESCE(p.display_name, ''),
           p.avatar_media_id,
           f.status,
           f.created_at
    FROM follows f
    JOIN users u ON u.id = f.follower_id AND u.deleted_at IS NULL
    LEFT JOIN user_profiles p ON p.user_id = u.id
    WHERE f.followee_id = v_target
      AND f.status = 'accepted'
    ORDER BY COALESCE(p.display_name, u.username::text);
END;
$$;

DO $$
BEGIN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.list_followers(text) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.list_followers(text) TO authenticated';
END;
$$;

COMMIT;


-- =====================================================================
-- BAGIAN: 20260724000024_edit_message.sql
-- =====================================================================

    -- Ubah pesan — melengkapi hapus pesan yang sudah ada.
    --
    -- Hanya pengirim yang boleh mengubah, hanya pesan teks (mengubah lampiran atau
    -- pesan sistem tidak masuk akal), dan hanya yang belum dihapus. edited_at diisi
    -- supaya klien bisa menandai "diedit".
    --
    -- Tidak perlu menyentuh ringkasan percakapan: list_conversations membaca body
    -- pesan terakhir secara langsung lewat join, jadi preview ikut berubah sendiri.
    --
    -- Nama kolom keluaran diawali out_ agar tidak pernah bentrok dengan kolom tabel
    -- (kebiasaan sejak start_call sempat gagal karena ambiguitas — migrasi 19/22).

    BEGIN;

    CREATE OR REPLACE FUNCTION public.edit_message(p_message uuid, p_body text)
    RETURNS TABLE (out_conversation_id uuid, out_edited_at timestamptz)
    LANGUAGE plpgsql
    SECURITY DEFINER
    SET search_path = public
    AS $$
    DECLARE
        v_user   uuid := public.require_auth();
        v_sender uuid;
        v_type   text;
        v_conv   uuid;
        v_body   text := btrim(coalesce(p_body, ''));
        v_now    timestamptz := now();
    BEGIN
        IF v_body = '' THEN
            RAISE EXCEPTION 'isi pesan kosong' USING ERRCODE = '22023';
        END IF;
        IF char_length(v_body) > 4000 THEN
            RAISE EXCEPTION 'isi pesan melebihi batas' USING ERRCODE = '22023';
        END IF;

        SELECT m.sender_id, m.type, m.conversation_id
        INTO v_sender, v_type, v_conv
        FROM messages m
        WHERE m.id = p_message AND m.deleted_at IS NULL;

        IF NOT FOUND THEN
            RAISE EXCEPTION 'pesan tidak ditemukan' USING ERRCODE = 'P0002';
        END IF;

        IF v_sender <> v_user THEN
            RAISE EXCEPTION 'pesan bukan milik pengguna ini' USING ERRCODE = '42501';
        END IF;

        IF v_type <> 'text' THEN
            RAISE EXCEPTION 'hanya pesan teks yang bisa diubah' USING ERRCODE = '22023';
        END IF;

        UPDATE messages m
        SET body = v_body, edited_at = v_now
        WHERE m.id = p_message;

        RETURN QUERY SELECT v_conv, v_now;
    END;
    $$;

    DO $$
    BEGIN
        EXECUTE 'REVOKE EXECUTE ON FUNCTION public.edit_message(uuid, text) FROM PUBLIC';
        EXECUTE 'GRANT  EXECUTE ON FUNCTION public.edit_message(uuid, text) TO authenticated';
    END;
    $$;

    COMMIT;


-- =====================================================================
-- BAGIAN: 20260724000025_starred_messages.sql
-- =====================================================================

-- Starred messages — menandai pesan penting untuk dibuka lagi nanti.
--
-- Menu titik-tiga aplikasi sudah punya slot "Starred messages" yang selama ini
-- tak berujung ke mana-mana. Tabel di bawah menyimpannya per pengguna: bintang
-- adalah penanda pribadi, tidak terlihat peserta lain (berbeda dari reaksi).
--
-- Menyimpan hanya (user, message); isi pesan diambil lewat join saat ditampilkan
-- supaya edit/hapus pesan otomatis tercermin di daftar bintang.

BEGIN;

CREATE TABLE IF NOT EXISTS message_stars (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    message_id uuid NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, message_id)
);

CREATE INDEX IF NOT EXISTS message_stars_user_idx
    ON message_stars (user_id, created_at DESC);

ALTER TABLE message_stars ENABLE ROW LEVEL SECURITY;

-- ============================================================
-- TANDAI / LEPAS
-- ============================================================
-- Hanya boleh menandai pesan yang benar-benar bisa dilihat pemanggil: ia harus
-- anggota percakapannya, dan pesannya belum dihapus.
CREATE OR REPLACE FUNCTION public.star_message(p_message uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_conv uuid;
BEGIN
    SELECT m.conversation_id INTO v_conv
    FROM messages m
    WHERE m.id = p_message AND m.deleted_at IS NULL;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'pesan tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM conversation_members cm
        WHERE cm.conversation_id = v_conv AND cm.user_id = v_user AND cm.left_at IS NULL
    ) THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;

    INSERT INTO message_stars (user_id, message_id)
    VALUES (v_user, p_message)
    ON CONFLICT (user_id, message_id) DO NOTHING;
END;
$$;

CREATE OR REPLACE FUNCTION public.unstar_message(p_message uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    DELETE FROM message_stars
    WHERE user_id = v_user AND message_id = p_message;
END;
$$;

-- ============================================================
-- DAFTAR BERBINTANG
-- ============================================================
-- Lintas-percakapan, terbaru dulu, cursor berdasarkan waktu ditandai. Hanya
-- pesan di percakapan yang masih diikuti dan belum dihapus yang ditampilkan —
-- bintang pada pesan yang kemudian hilang tidak bocor.
CREATE OR REPLACE FUNCTION public.list_starred_messages(
    p_before timestamptz,
    p_limit  integer
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
    is_deleted          boolean,
    attachments         text,
    starred_at          timestamptz
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
    SELECT m.id, m.conversation_id, m.sender_id, m.type,
           COALESCE(m.body, ''),
           m.reply_to_message_id, m.created_at, m.edited_at,
           false,
           (SELECT string_agg(ma.storage_key, ',' ORDER BY a.position)
            FROM message_attachments a JOIN media_assets ma ON ma.id = a.media_id
            WHERE a.message_id = m.id),
           s.created_at
    FROM message_stars s
    JOIN messages m ON m.id = s.message_id AND m.deleted_at IS NULL
    JOIN conversation_members cm
      ON cm.conversation_id = m.conversation_id
     AND cm.user_id = v_user
     AND cm.left_at IS NULL
    WHERE s.user_id = v_user
      AND s.created_at < p_before
    ORDER BY s.created_at DESC
    LIMIT LEAST(GREATEST(p_limit, 1), 100);
END;
$$;

DO $$
DECLARE
    fn text;
BEGIN
    FOREACH fn IN ARRAY ARRAY[
        'public.star_message(uuid)',
        'public.unstar_message(uuid)',
        'public.list_starred_messages(timestamptz, integer)'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO authenticated', fn);
    END LOOP;
END;
$$;

COMMIT;


-- =====================================================================
-- BAGIAN: 20260724000026_presence_privacy.sql
-- =====================================================================

-- Privasi presence — sembunyikan status online dari lawan bicara.
--
-- Selama ini status online seseorang terlihat oleh SEMUA lawan bicaranya tanpa
-- kecuali. Kolom baru presence_visible memberi pilihan menyembunyikannya.
--
-- Penegakannya ada di lapisan WebSocket: saat pengguna yang menyembunyikan
-- presence terhubung, backend tidak mencatatnya online dan tidak menyiarkan
-- perubahan statusnya — jadi ia tak pernah tampak online, dan "last seen" pun
-- tidak terekam. Berlaku sejak koneksi berikutnya setelah setelan diubah.
--
-- Setelan ini ikut aliran profil yang sudah ada (dm_privacy, story_privacy):
-- GET /users/me mengembalikannya, PATCH /users/me mengubahnya.

BEGIN;

ALTER TABLE user_settings
    ADD COLUMN IF NOT EXISTS presence_visible boolean NOT NULL DEFAULT true;

-- ============================================================
-- get_my_profile — tambah presence_visible
-- ============================================================
DROP FUNCTION IF EXISTS public.get_my_profile();
CREATE OR REPLACE FUNCTION public.get_my_profile()
RETURNS TABLE (
    id               uuid,
    username         text,
    email            text,
    display_name     text,
    bio              text,
    avatar_key       text,
    cover_key        text,
    follower_count   integer,
    following_count  integer,
    is_private       boolean,
    date_of_birth    date,
    dm_privacy       text,
    story_privacy    text,
    presence_visible boolean,
    locale           text,
    created_at       timestamptz
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
    SELECT u.id,
           u.username::text,
           COALESCE(u.email::text, ''),
           COALESCE(p.display_name, ''),
           COALESCE(p.bio, ''),
           COALESCE(ma.storage_key, ''),
           COALESCE(mc.storage_key, ''),
           COALESCE(p.follower_count, 0),
           COALESCE(p.following_count, 0),
           u.is_private,
           u.date_of_birth,
           COALESCE(s.dm_privacy, 'everyone'),
           COALESCE(s.story_privacy, 'followers'),
           COALESCE(s.presence_visible, true),
           COALESCE(s.locale, 'id'),
           u.created_at
    FROM users u
    LEFT JOIN user_profiles p  ON p.user_id = u.id
    LEFT JOIN user_settings s  ON s.user_id = u.id
    LEFT JOIN media_assets  ma ON ma.id = p.avatar_media_id
    LEFT JOIN media_assets  mc ON mc.id = p.cover_media_id
    WHERE u.id = v_user;
END;
$$;

-- ============================================================
-- update_my_profile — tambah p_presence_visible
-- ============================================================
DROP FUNCTION IF EXISTS public.update_my_profile(text, text, uuid, boolean, text, text, text, uuid);
CREATE OR REPLACE FUNCTION public.update_my_profile(
    p_display_name     text    DEFAULT NULL,
    p_bio              text    DEFAULT NULL,
    p_avatar_media     uuid    DEFAULT NULL,
    p_is_private       boolean DEFAULT NULL,
    p_dm_privacy       text    DEFAULT NULL,
    p_story_privacy    text    DEFAULT NULL,
    p_username         text    DEFAULT NULL,
    p_cover_media      uuid    DEFAULT NULL,
    p_presence_visible boolean DEFAULT NULL
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user     uuid := public.require_auth();
    v_username text := NULLIF(btrim(p_username), '');
BEGIN
    IF p_avatar_media IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM media_assets ma
                       WHERE ma.id = p_avatar_media AND ma.owner_id = v_user AND ma.kind = 'image') THEN
        RAISE EXCEPTION 'avatar harus gambar milik pengguna ini' USING ERRCODE = '42501';
    END IF;

    IF p_cover_media IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM media_assets ma
                       WHERE ma.id = p_cover_media AND ma.owner_id = v_user AND ma.kind = 'image') THEN
        RAISE EXCEPTION 'sampul harus gambar milik pengguna ini' USING ERRCODE = '42501';
    END IF;

    IF v_username IS NOT NULL THEN
        UPDATE users u
        SET username = v_username, updated_at = now()
        WHERE u.id = v_user AND u.username <> v_username;
    END IF;

    UPDATE user_profiles p
    SET display_name    = COALESCE(NULLIF(btrim(p_display_name), ''), p.display_name),
        bio             = COALESCE(p_bio, p.bio),
        avatar_media_id = COALESCE(p_avatar_media, p.avatar_media_id),
        cover_media_id  = COALESCE(p_cover_media, p.cover_media_id),
        updated_at      = now()
    WHERE p.user_id = v_user;

    IF p_is_private IS NOT NULL THEN
        UPDATE users u SET is_private = p_is_private, updated_at = now()
        WHERE u.id = v_user;
    END IF;

    IF p_dm_privacy IS NOT NULL OR p_story_privacy IS NOT NULL OR p_presence_visible IS NOT NULL THEN
        UPDATE user_settings s
        SET dm_privacy       = COALESCE(p_dm_privacy, s.dm_privacy),
            story_privacy    = COALESCE(p_story_privacy, s.story_privacy),
            presence_visible = COALESCE(p_presence_visible, s.presence_visible)
        WHERE s.user_id = v_user;
    END IF;
END;
$$;

DO $$
DECLARE
    fn text;
BEGIN
    FOREACH fn IN ARRAY ARRAY[
        'public.get_my_profile()',
        'public.update_my_profile(text, text, uuid, boolean, text, text, text, uuid, boolean)'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO authenticated', fn);
    END LOOP;
END;
$$;

COMMIT;


-- =====================================================================
-- BAGIAN: 20260724000027_delete_media.sql
-- =====================================================================

-- Menghapus satu media milik pengguna beserta berkasnya — atas permintaan.
--
-- Konteksnya dari pesan-untuk-backend.md poin 5: saat pengguna mengganti foto
-- profil, foto lama tertinggal di object storage selamanya. Aplikasi sudah
-- menyimpan media_id lama dan memanggil deleteMedia(oldId) tiap ganti avatar —
-- sampai sekarang itu no-op karena endpoint-nya belum ada.
--
-- Ini BERBEDA dari purge_orphan_media(): purge bekerja di latar setelah masa
-- tenggang untuk media yatim; fungsi ini penghapusan langsung yang diminta
-- pemiliknya. Karena langsung, ia butuh dua penjaga yang purge tidak perlu:
--
--   * hanya pemilik yang boleh menghapus medianya (bukan sembarang orang);
--   * media yang MASIH ditunjuk sesuatu tidak boleh dihapus. Ini bukan sekadar
--     kerapian: sebagian foreign key ke media_assets memakai ON DELETE CASCADE
--     (stories, message_attachments), sehingga menghapus barisnya diam-diam
--     ikut menghapus story atau lampiran pesan yang masih memakainya; yang lain
--     ON DELETE SET NULL (avatar/cover profil, avatar percakapan), yang akan
--     mengosongkan avatar seseorang tanpa ia sadari; dan reels memakai NO
--     ACTION, yang menolak dengan galat foreign key mentah. Semua kasus itu
--     ditolak lebih dulu di sini dengan pesan yang jelas.
--
-- Aplikasi memang baru memanggil ini SETELAH profil menunjuk avatar baru, jadi
-- media lama sudah yatim saat sampai ke sini. Kalau ternyata belum, menolak
-- jauh lebih baik daripada merusak tautan di tempat lain.
--
-- storage_key dikembalikan supaya backend tahu berkas mana yang harus dibuang
-- dari object storage setelah barisnya hilang.

BEGIN;

CREATE OR REPLACE FUNCTION public.delete_media(p_id uuid)
RETURNS TABLE (storage_key text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_owner uuid;
    v_key   text;
BEGIN
    SELECT ma.owner_id, ma.storage_key INTO v_owner, v_key
    FROM media_assets ma
    WHERE ma.id = p_id;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'media tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    IF v_owner <> v_user THEN
        RAISE EXCEPTION 'media bukan milik pengguna ini' USING ERRCODE = '42501';
    END IF;

    -- Referensi diperiksa apa adanya, tanpa menyaring baris yang sudah
    -- di-soft-delete: foreign key ditegakkan pada barisnya, bukan pada status
    -- tampilnya. Kalau story sudah disembunyikan tetapi barisnya masih
    -- menunjuk media ini, penghapusan tetap akan menabrak FK — jadi lebih baik
    -- menolaknya di sini dengan pesan yang bisa dibaca.
    IF EXISTS (SELECT 1 FROM stories s              WHERE s.media_id = p_id)
    OR EXISTS (SELECT 1 FROM message_attachments a  WHERE a.media_id = p_id)
    OR EXISTS (SELECT 1 FROM reels r                WHERE r.media_id = p_id)
    OR EXISTS (SELECT 1 FROM user_profiles p        WHERE p.avatar_media_id = p_id OR p.cover_media_id = p_id)
    OR EXISTS (SELECT 1 FROM conversations c        WHERE c.avatar_media_id = p_id)
    OR EXISTS (SELECT 1 FROM rooms rm               WHERE rm.recording_media_id = p_id)
    THEN
        RAISE EXCEPTION 'media masih dipakai' USING ERRCODE = '55006';
    END IF;

    DELETE FROM media_assets ma WHERE ma.id = p_id;

    RETURN QUERY SELECT v_key;
END;
$$;

REVOKE EXECUTE ON FUNCTION public.delete_media(uuid) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.delete_media(uuid) TO authenticated;

COMMIT;


-- =====================================================================
-- BAGIAN: 20260724000028_realtime_delete_reaction.sql
-- =====================================================================

-- Siaran realtime untuk hapus pesan & reaksi (pesan-untuk-backend.md poin 11.1 & 11.2).
--
-- Keduanya sudah bekerja, tetapi hanya di sisi database: perangkat lawan bicara
-- baru tahu setelah membuka ulang chat / memuat ulang reaksi. Agar backend bisa
-- MENYIARKAN perubahannya lewat WebSocket, ia butuh id percakapan untuk
-- menentukan topik "conversation:<id>". Sampai kini kedua fungsi RETURNS void,
-- jadi id itu tidak pernah kembali ke Go.
--
-- Migrasi ini mengubah keduanya agar mengembalikan conversation_id. Karena
-- mengubah tipe kembalian, fungsinya harus DROP dulu — CREATE OR REPLACE tidak
-- bisa mengganti return type. Kolom keluaran diberi awalan out_ mengikuti
-- konvensi edit_message, supaya tidak bentrok dengan nama kolom di body fungsi.

BEGIN;

-- ============================================================
-- 1. HAPUS PESAN  -> kembalikan conversation_id
-- ============================================================

DROP FUNCTION IF EXISTS public.delete_message(uuid);

CREATE FUNCTION public.delete_message(p_message uuid)
RETURNS TABLE (out_conversation_id uuid)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user   uuid := public.require_auth();
    v_sender uuid;
    v_conv   uuid;
BEGIN
    SELECT m.sender_id, m.conversation_id
      INTO v_sender, v_conv
    FROM messages m
    WHERE m.id = p_message AND m.deleted_at IS NULL;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'pesan tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    IF v_sender <> v_user THEN
        RAISE EXCEPTION 'pesan bukan milik pengguna ini' USING ERRCODE = '42501';
    END IF;

    UPDATE messages m SET deleted_at = now() WHERE m.id = p_message;

    RETURN QUERY SELECT v_conv;
END;
$$;

-- ============================================================
-- 2. REAKSI  -> kembalikan conversation_id
-- ============================================================

DROP FUNCTION IF EXISTS public.react_to_message(uuid, text);

CREATE FUNCTION public.react_to_message(
    p_message uuid,
    p_emoji   text
)
RETURNS TABLE (out_conversation_id uuid)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_conv uuid;
BEGIN
    SELECT m.conversation_id INTO v_conv
    FROM messages m
    WHERE m.id = p_message AND m.deleted_at IS NULL;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'pesan tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM conversation_members cm
        WHERE cm.conversation_id = v_conv AND cm.user_id = v_user AND cm.left_at IS NULL
    ) THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;

    IF p_emoji IS NULL OR btrim(p_emoji) = '' THEN
        DELETE FROM message_reactions r WHERE r.message_id = p_message AND r.user_id = v_user;
    ELSE
        INSERT INTO message_reactions (message_id, user_id, emoji)
        VALUES (p_message, v_user, p_emoji)
        ON CONFLICT (message_id, user_id) DO UPDATE SET emoji = EXCLUDED.emoji, created_at = now();
    END IF;

    RETURN QUERY SELECT v_conv;
END;
$$;

-- ============================================================
-- 3. HAK EKSEKUSI (fungsi dibuat ulang, grant-nya ikut hilang)
-- ============================================================

DO $$
BEGIN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.delete_message(uuid) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.delete_message(uuid) TO authenticated';
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.react_to_message(uuid, text) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.react_to_message(uuid, text) TO authenticated';
END;
$$;

COMMIT;


-- =====================================================================
-- BAGIAN: 20260724000029_story_audience.sql
-- =====================================================================

-- Audiens sebuah story — untuk siaran realtime story.new (pesan-untuk-backend 11.5).
--
-- Saat seseorang memposting story, backend perlu tahu KE SIAPA menyiarkan
-- event-nya. Himpunannya harus sama persis dengan siapa yang nanti melihat
-- story itu di GET /stories (fungsi list_stories), kalau tidak ada perangkat
-- yang dapat notifikasi untuk story yang tak akan pernah ia lihat, atau
-- sebaliknya.
--
-- list_stories menampilkan story dari: penulisnya sendiri, orang yang di-follow
-- dengan status 'accepted', DAN lawan bicara di percakapan yang sama — dikurangi
-- blokir dua arah. Fungsi ini membalik sudut pandangnya: diberi seorang penulis,
-- siapa saja yang berhak melihat story-nya. (Visibility per-story sengaja tidak
-- disaring di sini, mengikuti perilaku list_stories yang sekarang.)
--
-- Dipakai hanya oleh penulisnya sendiri: p_author harus sama dengan pemanggil,
-- supaya audiens orang lain tidak bisa dienumerasi.

BEGIN;

CREATE OR REPLACE FUNCTION public.story_audience(p_author uuid)
RETURNS TABLE (user_id uuid)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF p_author <> v_user THEN
        RAISE EXCEPTION 'hanya boleh untuk diri sendiri' USING ERRCODE = '42501';
    END IF;

    RETURN QUERY
    SELECT DISTINCT aud.uid
    FROM (
        -- pengikut yang sudah diterima
        SELECT f.follower_id AS uid
        FROM follows f
        WHERE f.followee_id = p_author AND f.status = 'accepted'

        UNION

        -- lawan bicara di percakapan yang sama
        SELECT them.user_id AS uid
        FROM conversation_members me
        JOIN conversation_members them
          ON them.conversation_id = me.conversation_id
        WHERE me.user_id = p_author AND me.left_at IS NULL
          AND them.user_id <> p_author AND them.left_at IS NULL
    ) aud
    WHERE aud.uid <> p_author
      AND NOT EXISTS (
            SELECT 1 FROM blocks b
            WHERE (b.blocker_id = aud.uid AND b.blocked_id = p_author)
               OR (b.blocker_id = p_author AND b.blocked_id = aud.uid)
      );
END;
$$;

DO $$
BEGIN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.story_audience(uuid) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.story_audience(uuid) TO authenticated';
END;
$$;

COMMIT;


-- =====================================================================
-- BAGIAN: 20260724000030_search_users.sql
-- =====================================================================

-- Pencarian & penemuan pengguna — supaya aplikasi tidak terasa "dunia sendiri".
--
-- Masalahnya: satu-satunya cara menemukan orang lain adalah tahu username
-- persisnya (find_user / scan QR). Akun baru yang belum mengikuti siapa pun
-- karena itu melihat layar kosong — tak ada percakapan, tak ada story, dan tak
-- ada cara membangun daftar following. Terasa seolah tiap pengguna terisolasi.
--
-- search_users menutup itu: cari berdasarkan username ATAU nama tampilan
-- (substring, case-insensitive), dengan follow_status dari sudut pandang
-- pemanggil supaya tombol Follow/Requested/Following langsung benar. Query
-- kosong sengaja mengembalikan "saran" (pengguna teraktif/terpopuler) supaya
-- layar temukan-orang tidak pernah kosong.
--
-- Menyembunyikan: diri sendiri, akun terhapus/nonaktif, dan blokir dua arah.

BEGIN;

CREATE OR REPLACE FUNCTION public.search_users(p_query text DEFAULT '', p_limit int DEFAULT 30)
RETURNS TABLE (
    id              uuid,
    username        text,
    display_name    text,
    avatar_media_id uuid,
    follower_count  integer,
    following_count integer,
    follow_status   text,
    is_self         boolean
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_q    text := btrim(coalesce(p_query, ''));
    v_lim  int  := least(greatest(coalesce(p_limit, 30), 1), 50);
BEGIN
    RETURN QUERY
    SELECT u.id,
           u.username::text,
           COALESCE(p.display_name, ''),
           p.avatar_media_id,
           COALESCE(p.follower_count, 0),
           COALESCE(p.following_count, 0),
           COALESCE(f.status, ''),   -- '' = belum diikuti
           false                     -- diri sendiri sudah disaring di bawah
    FROM users u
    LEFT JOIN user_profiles p ON p.user_id = u.id
    LEFT JOIN follows f ON f.follower_id = v_user AND f.followee_id = u.id
    WHERE u.deleted_at IS NULL
      AND u.account_status = 'active'
      AND u.id <> v_user
      AND NOT EXISTS (
            SELECT 1 FROM blocks b
            WHERE (b.blocker_id = u.id     AND b.blocked_id = v_user)
               OR (b.blocker_id = v_user   AND b.blocked_id = u.id)
      )
      AND (
            v_q = ''
         OR u.username::text ILIKE '%' || v_q || '%'
         OR COALESCE(p.display_name, '') ILIKE '%' || v_q || '%'
      )
    ORDER BY
      -- saat mencari, awalan username yang cocok naik ke atas; selebihnya
      -- (dan seluruh daftar saran saat query kosong) urut dari terpopuler.
      (v_q <> '' AND u.username::text ILIKE v_q || '%') DESC,
      COALESCE(p.follower_count, 0) DESC,
      u.username
    LIMIT v_lim;
END;
$$;

DO $$
BEGIN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.search_users(text, int) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.search_users(text, int) TO authenticated';
END;
$$;

COMMIT;
