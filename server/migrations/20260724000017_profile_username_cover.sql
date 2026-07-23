-- Edit profil lanjutan: ganti username & foto sampul (cover).
--
-- Sebelumnya PATCH /users/me hanya menyentuh display_name, bio, avatar, dan
-- preferensi privasi. Layar Edit Profil aplikasi juga perlu mengganti username
-- dan cover — keduanya ditambahkan di sini.
--
-- Username unik ditegakkan oleh constraint UNIQUE citext pada users.username;
-- fungsi ini tidak mengecek keunikan secara manual (itu rawan balapan), cukup
-- membiarkan pelanggaran keunikan (23505) naik ke aplikasi.

BEGIN;

-- ============================================================
-- VALIDASI USERNAME
-- ============================================================
-- 3–30 karakter, huruf kecil/angka/titik/garis bawah, harus diawali huruf.
-- Dipisah jadi fungsi tersendiri supaya aturannya satu tempat.
CREATE OR REPLACE FUNCTION public.is_valid_username(p_username text)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
AS $$
    SELECT p_username ~ '^[a-z][a-z0-9._]{2,29}$';
$$;

-- ============================================================
-- get_my_profile — tambah cover_key
-- ============================================================
-- Menambah kolom mengubah tipe baris kembalian, jadi fungsi lama harus di-DROP
-- dulu (CREATE OR REPLACE tidak bisa mengubah bentuk keluaran — Postgres 42P13).
DROP FUNCTION IF EXISTS public.get_my_profile();
CREATE OR REPLACE FUNCTION public.get_my_profile()
RETURNS TABLE (
    id              uuid,
    username        text,
    email           text,
    display_name    text,
    bio             text,
    avatar_key      text,
    cover_key       text,
    follower_count  integer,
    following_count integer,
    is_private      boolean,
    date_of_birth   date,
    dm_privacy      text,
    story_privacy   text,
    locale          text,
    created_at      timestamptz
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
-- update_my_profile — tambah p_username & p_cover_media
-- ============================================================
-- Fungsi lama bertanda 6 argumen di-DROP agar tidak ada overload menggantung
-- yang bisa membingungkan PostgREST. Aplikasi selalu memanggil versi baru.
DROP FUNCTION IF EXISTS public.update_my_profile(text, text, uuid, boolean, text, text);
CREATE OR REPLACE FUNCTION public.update_my_profile(
    p_display_name  text    DEFAULT NULL,
    p_bio           text    DEFAULT NULL,
    p_avatar_media  uuid    DEFAULT NULL,
    p_is_private    boolean DEFAULT NULL,
    p_dm_privacy    text    DEFAULT NULL,
    p_story_privacy text    DEFAULT NULL,
    p_username      text    DEFAULT NULL,
    p_cover_media   uuid    DEFAULT NULL
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user     uuid := public.require_auth();
    v_username text := NULLIF(btrim(lower(p_username)), '');
BEGIN
    -- Avatar & cover harus milik pemanggil sendiri.
    IF p_avatar_media IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM media_assets ma
                       WHERE ma.id = p_avatar_media AND ma.owner_id = v_user) THEN
        RAISE EXCEPTION 'media bukan milik pengguna ini' USING ERRCODE = '42501';
    END IF;

    IF p_cover_media IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM media_assets ma
                       WHERE ma.id = p_cover_media AND ma.owner_id = v_user) THEN
        RAISE EXCEPTION 'media sampul bukan milik pengguna ini' USING ERRCODE = '42501';
    END IF;

    -- Ganti username hanya bila benar-benar berubah.
    IF v_username IS NOT NULL THEN
        IF NOT public.is_valid_username(v_username) THEN
            RAISE EXCEPTION 'username tidak valid' USING ERRCODE = '22023';
        END IF;
        -- Tidak apa-apa kalau sama dengan milik sendiri; UPDATE di bawah
        -- menyaringnya sehingga tidak memicu pelanggaran keunikan pada diri
        -- sendiri. Keunikan lintas-pengguna ditegakkan constraint (23505).
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

    IF p_dm_privacy IS NOT NULL OR p_story_privacy IS NOT NULL THEN
        UPDATE user_settings s
        SET dm_privacy    = COALESCE(p_dm_privacy, s.dm_privacy),
            story_privacy = COALESCE(p_story_privacy, s.story_privacy)
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
        'public.update_my_profile(text, text, uuid, boolean, text, text, text, uuid)'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO authenticated', fn);
    END LOOP;
END;
$$;

COMMIT;
