-- Panggilan banyak orang (maksimal 5), lewat undangan.
--
-- Tabel call_participants sudah many-to-many sejak awal, jadi bentuk datanya memang
-- sudah mendukung. Yang belum ada: cara MENGUNDANG orang ke panggilan yang sedang
-- berjalan, dan izin bagi orang itu untuk masuk.
--
-- BATASNYA DI SERVER, BUKAN DI APLIKASI. Klien boleh menyembunyikan tombol undang saat
-- sudah penuh, tetapi yang menentukan tetap di sini — dua orang yang mengundang pada
-- saat bersamaan tidak boleh bisa menembus batas, dan aturan ini juga berlaku untuk
-- klien versi lama.
CREATE OR REPLACE FUNCTION public.invite_to_call(p_call uuid, p_target uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_count integer;
BEGIN
    -- Hanya peserta yang sedang berada DI DALAM panggilan boleh mengundang. Bukan
    -- sekadar anggota percakapan: orang yang sudah keluar tidak boleh menarik orang
    -- lain masuk ke panggilan yang tidak lagi ia ikuti.
    IF NOT EXISTS (
        SELECT 1 FROM call_participants cp
        WHERE cp.call_id = p_call AND cp.user_id = v_user AND cp.left_at IS NULL
    ) THEN
        RAISE EXCEPTION 'bukan peserta panggilan' USING ERRCODE = '42501';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM calls c WHERE c.id = p_call AND c.status IN ('ringing', 'ongoing')
    ) THEN
        RAISE EXCEPTION 'panggilan tidak aktif' USING ERRCODE = 'P0002';
    END IF;

    -- Tidak boleh mengundang orang yang memblokir kita, atau yang kita blokir.
    IF EXISTS (
        SELECT 1 FROM blocks b
        WHERE (b.blocker_id = p_target AND b.blocked_id = v_user)
           OR (b.blocker_id = v_user AND b.blocked_id = p_target)
    ) THEN
        RAISE EXCEPTION 'tidak diizinkan' USING ERRCODE = 'P0003';
    END IF;

    -- Dihitung dari yang masih di dalam DITAMBAH yang sedang berdering (joined_at
    -- masih NULL), supaya lima undangan beruntun tidak semuanya lolos sebelum ada
    -- yang sempat menjawab.
    SELECT count(*) INTO v_count
    FROM call_participants cp
    WHERE cp.call_id = p_call AND cp.left_at IS NULL;

    IF v_count >= 5 THEN
        RAISE EXCEPTION 'panggilan sudah penuh' USING ERRCODE = 'P0004';
    END IF;

    -- joined_at NULL = diundang, belum masuk. answer_call yang mengisinya.
    INSERT INTO call_participants (call_id, user_id, joined_at, left_at)
    VALUES (p_call, p_target, NULL, NULL)
    ON CONFLICT (call_id, user_id) DO UPDATE SET left_at = NULL;
END;
$$;

-- answer_call kini juga menerima orang yang DIUNDANG.
--
-- Versinya yang lama mensyaratkan keanggotaan percakapan, sehingga orang yang diundang
-- dari percakapan lain tidak akan pernah bisa masuk — ia bukan anggota percakapan asal
-- panggilan itu. Sekarang: anggota percakapan ATAU punya baris undangan.
CREATE OR REPLACE FUNCTION public.answer_call(p_call uuid)
RETURNS text
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_sfu   text;
    v_count integer;
BEGIN
    SELECT c.sfu_room_id INTO v_sfu
    FROM calls c
    WHERE c.id = p_call
      AND c.status IN ('ringing', 'ongoing')
      AND (
          EXISTS (
              SELECT 1 FROM conversation_members cm
              WHERE cm.conversation_id = c.conversation_id
                AND cm.user_id = v_user AND cm.left_at IS NULL
          )
          OR EXISTS (
              SELECT 1 FROM call_participants cp
              WHERE cp.call_id = c.id AND cp.user_id = v_user AND cp.left_at IS NULL
          )
      );

    IF NOT FOUND THEN
        RAISE EXCEPTION 'panggilan tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    -- Batas yang sama ditegakkan saat masuk, bukan hanya saat mengundang: undangan
    -- bisa saja dikirim sebelum orang lain masuk duluan.
    SELECT count(*) INTO v_count
    FROM call_participants cp
    WHERE cp.call_id = p_call AND cp.left_at IS NULL AND cp.joined_at IS NOT NULL;

    IF v_count >= 5 AND NOT EXISTS (
        SELECT 1 FROM call_participants cp
        WHERE cp.call_id = p_call AND cp.user_id = v_user AND cp.joined_at IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'panggilan sudah penuh' USING ERRCODE = 'P0004';
    END IF;

    UPDATE calls SET status = 'ongoing', answered_at = COALESCE(answered_at, now())
    WHERE id = p_call AND status = 'ringing';

    INSERT INTO call_participants (call_id, user_id, joined_at)
    VALUES (p_call, v_user, now())
    ON CONFLICT (call_id, user_id) DO UPDATE SET joined_at = now(), left_at = NULL;

    RETURN v_sfu;
END;
$$;

-- Siapa saja yang ada di sebuah panggilan — untuk daftar peserta dan hitungan "3/5".
CREATE OR REPLACE FUNCTION public.list_call_participants(p_call uuid)
RETURNS TABLE (
    user_id      uuid,
    username     text,
    display_name text,
    joined       boolean
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
    SELECT cp.user_id, u.username::text, COALESCE(p.display_name, ''),
           (cp.joined_at IS NOT NULL)
    FROM call_participants cp
    JOIN users u ON u.id = cp.user_id AND u.deleted_at IS NULL
    LEFT JOIN user_profiles p ON p.user_id = cp.user_id
    WHERE cp.call_id = p_call
      AND cp.left_at IS NULL
      AND EXISTS (
          SELECT 1 FROM call_participants me
          WHERE me.call_id = p_call AND me.user_id = public.require_auth() AND me.left_at IS NULL
      );
$$;

DO $$
DECLARE fn text;
BEGIN
    FOREACH fn IN ARRAY ARRAY[
        'public.invite_to_call(uuid, uuid)',
        'public.list_call_participants(uuid)'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO authenticated', fn);
    END LOOP;
END $$;
