-- Menutup lubang pendaftaran.
--
-- Supabase Auth hanya membuat baris di auth.users. Tabel aplikasi
-- (public.users, user_profiles, user_settings) tidak ikut terisi, sehingga
-- pengguna yang mendaftar lewat SDK Supabase langsung akan punya akun yang
-- bisa login tetapi tidak punya profil — setiap query mengembalikan kosong,
-- dan tidak ada pesan error yang menjelaskan kenapa.
--
-- Terbukti terjadi: 4 baris di auth.users, 3 di public.users.
--
-- ensure_profile() dipanggil backend tepat setelah signup berhasil, dan juga
-- saat login sebagai jaring pengaman untuk akun yang terlanjur yatim.

BEGIN;

CREATE OR REPLACE FUNCTION public.ensure_profile(
    p_username     text,
    p_display_name text,
    p_dob          date
)
RETURNS TABLE (
    id           uuid,
    username     text,
    display_name text,
    created      boolean
)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user     uuid := public.require_auth();
    v_email    text;
    v_username citext;
    v_created  boolean := false;
BEGIN
    -- Sudah punya profil: tidak melakukan apa-apa, cukup kembalikan yang ada.
    -- Ini yang membuat fungsi aman dipanggil di setiap login.
    IF EXISTS (SELECT 1 FROM users u WHERE u.id = v_user) THEN
        RETURN QUERY
        SELECT u.id, u.username::text, COALESCE(p.display_name, ''), false
        FROM users u
        LEFT JOIN user_profiles p ON p.user_id = u.id
        WHERE u.id = v_user;
        RETURN;
    END IF;

    SELECT au.email INTO v_email FROM auth.users au WHERE au.id = v_user;

    -- Username: dari permintaan, atau diturunkan dari email. Karakter selain
    -- huruf/angka/underscore dibuang supaya tetap layak dipakai di URL dan QR.
    v_username := NULLIF(btrim(COALESCE(p_username, '')), '')::citext;
    IF v_username IS NULL THEN
        v_username := regexp_replace(lower(split_part(COALESCE(v_email, 'user'), '@', 1)),
                                     '[^a-z0-9_]', '', 'g')::citext;
    END IF;
    IF length(v_username) < 3 THEN
        v_username := (v_username || substr(replace(v_user::text, '-', ''), 1, 6))::citext;
    END IF;

    -- Bentrok username diselesaikan dengan menambah sufiks, bukan menolak
    -- pendaftaran. Menolak di titik ini berarti pengguna sudah punya akun di
    -- auth.users tetapi gagal menyelesaikan profil — keadaan setengah jadi
    -- yang persis ingin dihindari fungsi ini.
    WHILE EXISTS (SELECT 1 FROM users u WHERE u.username = v_username) LOOP
        v_username := (v_username || floor(random() * 1000)::text)::citext;
    END LOOP;

    INSERT INTO users (id, username, email, date_of_birth)
    VALUES (v_user, v_username, v_email,
            COALESCE(p_dob, DATE '2000-01-01'));

    INSERT INTO user_profiles (user_id, display_name)
    VALUES (v_user, COALESCE(NULLIF(btrim(p_display_name), ''), v_username::text));

    INSERT INTO user_settings (user_id) VALUES (v_user)
    ON CONFLICT DO NOTHING;

    v_created := true;

    RETURN QUERY
    SELECT u.id, u.username::text, COALESCE(p.display_name, ''), v_created
    FROM users u
    LEFT JOIN user_profiles p ON p.user_id = u.id
    WHERE u.id = v_user;
END;
$$;

REVOKE EXECUTE ON FUNCTION public.ensure_profile(text, text, date) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.ensure_profile(text, text, date) TO authenticated;

-- Perbaiki akun yang sudah terlanjur yatim sebelum fungsi ini ada.
INSERT INTO users (id, username, email, date_of_birth)
SELECT au.id,
       (regexp_replace(lower(split_part(au.email, '@', 1)), '[^a-z0-9_]', '', 'g')
        || substr(replace(au.id::text, '-', ''), 1, 4))::citext,
       au.email,
       DATE '2000-01-01'
FROM auth.users au
WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.id = au.id)
ON CONFLICT DO NOTHING;

INSERT INTO user_profiles (user_id, display_name)
SELECT u.id, u.username::text
FROM users u
WHERE NOT EXISTS (SELECT 1 FROM user_profiles p WHERE p.user_id = u.id)
ON CONFLICT DO NOTHING;

INSERT INTO user_settings (user_id)
SELECT u.id FROM users u
WHERE NOT EXISTS (SELECT 1 FROM user_settings s WHERE s.user_id = u.id)
ON CONFLICT DO NOTHING;

COMMIT;
