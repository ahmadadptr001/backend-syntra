-- Menutup empat celah yang ditemukan saat audit menyeluruh.
--
-- Semua tabel di bawah sudah ada sejak migrasi pertama, tetapi tidak satu pun
-- punya fungsi untuk membacanya — jadi fiturnya mustahil dibangun:
--
--   notifications  tabel ada, konstanta protokol `notification.new` ada,
--                  tetapi tidak ada yang menyiarkan maupun membacanya
--   user_profiles  tidak ada cara membaca atau mengubah profil sendiri
--   blocks         dicek di hampir setiap query, tetapi tidak ada cara memblokir
--   devices        dibutuhkan push notification, tidak pernah bisa diisi

BEGIN;

-- ============================================================
-- 1. PROFIL SENDIRI
-- ============================================================
-- GET /users/{username} hanya mengembalikan data publik. Layar pengaturan
-- butuh email dan preferensi, yang tidak boleh ikut terlihat orang lain.

CREATE OR REPLACE FUNCTION public.get_my_profile()
RETURNS TABLE (
    id              uuid,
    username        text,
    email           text,
    display_name    text,
    bio             text,
    avatar_key      text,
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
    WHERE u.id = v_user;
END;
$$;

-- Mengubah profil. Parameter NULL berarti "biarkan seperti semula", sehingga
-- klien bisa mengirim hanya field yang benar-benar diubah.
CREATE OR REPLACE FUNCTION public.update_my_profile(
    p_display_name  text    DEFAULT NULL,
    p_bio           text    DEFAULT NULL,
    p_avatar_media  uuid    DEFAULT NULL,
    p_is_private    boolean DEFAULT NULL,
    p_dm_privacy    text    DEFAULT NULL,
    p_story_privacy text    DEFAULT NULL
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF p_avatar_media IS NOT NULL
       AND NOT EXISTS (SELECT 1 FROM media_assets ma
                       WHERE ma.id = p_avatar_media AND ma.owner_id = v_user) THEN
        RAISE EXCEPTION 'media bukan milik pengguna ini' USING ERRCODE = '42501';
    END IF;

    UPDATE user_profiles p
    SET display_name    = COALESCE(NULLIF(btrim(p_display_name), ''), p.display_name),
        bio             = COALESCE(p_bio, p.bio),
        avatar_media_id = COALESCE(p_avatar_media, p.avatar_media_id),
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

-- ============================================================
-- 2. NOTIFIKASI
-- ============================================================

CREATE OR REPLACE FUNCTION public.list_notifications(
    p_before uuid    DEFAULT NULL,
    p_limit  integer DEFAULT 30
)
RETURNS TABLE (
    id            uuid,
    type          text,
    actor_id      uuid,
    actor_username text,
    actor_name    text,
    actor_avatar_key text,
    subject_type  text,
    subject_id    uuid,
    is_read       boolean,
    created_at    timestamptz
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
    SELECT n.id,
           n.type,
           n.actor_id,
           COALESCE(u.username::text, ''),
           COALESCE(p.display_name, ''),
           COALESCE(ma.storage_key, ''),
           COALESCE(n.subject_type, ''),
           n.subject_id,
           (n.read_at IS NOT NULL),
           n.created_at
    FROM notifications n
    LEFT JOIN users         u  ON u.id = n.actor_id AND u.deleted_at IS NULL
    LEFT JOIN user_profiles p  ON p.user_id = n.actor_id
    LEFT JOIN media_assets  ma ON ma.id = p.avatar_media_id
    WHERE n.recipient_id = v_user
      -- Cursor memakai id: UUIDv7 terurut waktu, jadi tidak butuh index
      -- tambahan dan tidak melewatkan baris saat waktunya kembar.
      AND (p_before IS NULL OR n.id < p_before)
    ORDER BY n.id DESC
    LIMIT LEAST(GREATEST(p_limit, 1), 100);
END;
$$;

CREATE OR REPLACE FUNCTION public.count_unread_notifications()
RETURNS integer
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_n    integer;
BEGIN
    SELECT count(*)::integer INTO v_n
    FROM notifications n
    WHERE n.recipient_id = v_user AND n.read_at IS NULL;
    RETURN v_n;
END;
$$;

-- p_notification NULL berarti "tandai semua".
CREATE OR REPLACE FUNCTION public.mark_notifications_read(p_notification uuid DEFAULT NULL)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_n    integer;
BEGIN
    UPDATE notifications n
    SET read_at = now()
    WHERE n.recipient_id = v_user
      AND n.read_at IS NULL
      AND (p_notification IS NULL OR n.id = p_notification);

    GET DIAGNOSTICS v_n = ROW_COUNT;
    RETURN v_n;
END;
$$;

-- Membuat notifikasi. Dipanggil backend setelah kejadian yang memicunya.
--
-- Notifikasi untuk diri sendiri dilewati diam-diam: menyukai postingan sendiri
-- atau membalas diri sendiri tidak perlu memberi tahu siapa pun.
CREATE OR REPLACE FUNCTION public.create_notification(
    p_id           uuid,
    p_recipient    uuid,
    p_type         text,
    p_subject_type text,
    p_subject      uuid
)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF p_recipient = v_user OR p_recipient IS NULL THEN
        RETURN false;
    END IF;

    -- Penerima yang memblokir pengirim tidak boleh dikirimi notifikasi.
    IF EXISTS (
        SELECT 1 FROM blocks b
        WHERE (b.blocker_id = p_recipient AND b.blocked_id = v_user)
           OR (b.blocker_id = v_user AND b.blocked_id = p_recipient)
    ) THEN
        RETURN false;
    END IF;

    INSERT INTO notifications (id, recipient_id, actor_id, type, subject_type, subject_id)
    VALUES (p_id, p_recipient, v_user, p_type, NULLIF(p_subject_type, ''), p_subject);

    RETURN true;
END;
$$;

-- ============================================================
-- 3. BLOKIR
-- ============================================================
-- Tabel blocks sudah dicek di hampir setiap query sejak awal, tetapi tidak ada
-- cara mengisinya — jadi pemeriksaan itu selama ini tidak pernah berarti apa-apa.

CREATE OR REPLACE FUNCTION public.block_user(p_target uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF p_target IS NULL OR p_target = v_user THEN
        RAISE EXCEPTION 'tidak bisa memblokir diri sendiri' USING ERRCODE = '22023';
    END IF;

    INSERT INTO blocks (blocker_id, blocked_id)
    VALUES (v_user, p_target)
    ON CONFLICT DO NOTHING;

    -- Memblokir memutus hubungan dua arah. Membiarkan follow tetap ada berarti
    -- yang diblokir masih menerima story dan pembaruan dari orang yang
    -- memblokirnya — persis yang ingin dihindari.
    DELETE FROM follows f
    WHERE (f.follower_id = v_user AND f.followee_id = p_target)
       OR (f.follower_id = p_target AND f.followee_id = v_user);

    UPDATE user_profiles p
    SET follower_count  = GREATEST(p.follower_count - 1, 0)
    WHERE p.user_id IN (v_user, p_target);
END;
$$;

CREATE OR REPLACE FUNCTION public.unblock_user(p_target uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    DELETE FROM blocks b
    WHERE b.blocker_id = v_user AND b.blocked_id = p_target;
END;
$$;

CREATE OR REPLACE FUNCTION public.list_blocked()
RETURNS TABLE (
    user_id      uuid,
    username     text,
    display_name text,
    avatar_key   text,
    created_at   timestamptz
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
    SELECT b.blocked_id,
           u.username::text,
           COALESCE(p.display_name, ''),
           COALESCE(ma.storage_key, ''),
           b.created_at
    FROM blocks b
    JOIN users u ON u.id = b.blocked_id
    LEFT JOIN user_profiles p  ON p.user_id = b.blocked_id
    LEFT JOIN media_assets  ma ON ma.id = p.avatar_media_id
    WHERE b.blocker_id = v_user
    ORDER BY b.created_at DESC;
END;
$$;

-- ============================================================
-- 4. PERANGKAT — untuk push notification
-- ============================================================

CREATE OR REPLACE FUNCTION public.register_device(
    p_id          uuid,
    p_platform    text,
    p_push_token  text,
    p_app_version text
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF p_platform NOT IN ('android', 'ios', 'web') THEN
        RAISE EXCEPTION 'platform tidak valid' USING ERRCODE = '22023';
    END IF;

    -- Satu push token hanya boleh dimiliki satu akun. Kalau ponsel berpindah
    -- pengguna, pemilik lama harus berhenti menerima notifikasinya.
    UPDATE devices d
    SET revoked_at = now()
    WHERE d.push_token = p_push_token AND d.user_id <> v_user AND d.revoked_at IS NULL;

    INSERT INTO devices (id, user_id, platform, push_token, app_version, last_seen_at)
    VALUES (p_id, v_user, p_platform, p_push_token, p_app_version, now())
    ON CONFLICT (id) DO UPDATE
    SET push_token   = EXCLUDED.push_token,
        app_version  = EXCLUDED.app_version,
        last_seen_at = now(),
        revoked_at   = NULL;
END;
$$;

CREATE OR REPLACE FUNCTION public.revoke_device(p_id uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    UPDATE devices d
    SET revoked_at = now()
    WHERE d.id = p_id AND d.user_id = v_user;
END;
$$;

-- ============================================================
-- 5. LAPORAN — kewajiban trust & safety
-- ============================================================

CREATE OR REPLACE FUNCTION public.create_report(
    p_id          uuid,
    p_target_type text,
    p_target      uuid,
    p_reason      text,
    p_detail      text
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_prio text;
BEGIN
    IF p_target_type NOT IN ('user','reel','story','message','room','comment') THEN
        RAISE EXCEPTION 'target_type tidak valid' USING ERRCODE = '22023';
    END IF;
    IF p_reason NOT IN ('spam','harassment','nudity','violence','csam','copyright','other') THEN
        RAISE EXCEPTION 'reason tidak valid' USING ERRCODE = '22023';
    END IF;

    -- csam punya kewajiban pelaporan hukum dan SLA yang berbeda total dari
    -- spam; ia harus bisa dirutekan khusus, bukan mengantre bersama sisanya.
    v_prio := CASE p_reason
                WHEN 'csam' THEN 'critical'
                WHEN 'violence' THEN 'high'
                WHEN 'harassment' THEN 'high'
                ELSE 'normal'
              END;

    INSERT INTO reports (id, reporter_id, target_type, target_id, reason, detail, priority)
    VALUES (p_id, v_user, p_target_type, p_target, p_reason, NULLIF(btrim(p_detail), ''), v_prio);
END;
$$;

-- ============================================================
-- 6. HAK EKSEKUSI
-- ============================================================

DO $$
DECLARE
    fn text;
BEGIN
    FOREACH fn IN ARRAY ARRAY[
        'public.get_my_profile()',
        'public.update_my_profile(text, text, uuid, boolean, text, text)',
        'public.list_notifications(uuid, integer)',
        'public.count_unread_notifications()',
        'public.mark_notifications_read(uuid)',
        'public.create_notification(uuid, uuid, text, text, uuid)',
        'public.block_user(uuid)',
        'public.unblock_user(uuid)',
        'public.list_blocked()',
        'public.register_device(uuid, text, text, text)',
        'public.revoke_device(uuid)',
        'public.create_report(uuid, text, uuid, text, text)'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO authenticated', fn);
    END LOOP;
END;
$$;

COMMIT;
