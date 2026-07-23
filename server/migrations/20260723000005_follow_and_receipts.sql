-- Menjawab dua permintaan teratas di server/catatan-untuk-backend.md §3.
--
--   1. Follow/unfollow — pemblokir story row. list_stories hanya menampilkan
--      story dari orang yang diikuti, jadi tanpa cara membangun daftar
--      following, story row selamanya hanya berisi milik sendiri.
--
--   2. Bahan untuk indikator ✓✓ — posisi baca terakhir lawan bicara,
--      ditambahkan ke daftar percakapan.

BEGIN;

-- ============================================================
-- 1. FOLLOW / UNFOLLOW
-- ============================================================

CREATE OR REPLACE FUNCTION public.follow_user(p_target uuid)
RETURNS text
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user    uuid := public.require_auth();
    v_private boolean;
    v_status  text;
BEGIN
    IF p_target IS NULL OR p_target = v_user THEN
        RAISE EXCEPTION 'tidak bisa mengikuti diri sendiri' USING ERRCODE = '22023';
    END IF;

    SELECT is_private INTO v_private
    FROM users WHERE id = p_target AND deleted_at IS NULL AND account_status = 'active';

    IF NOT FOUND THEN
        RAISE EXCEPTION 'pengguna tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    IF EXISTS (
        SELECT 1 FROM blocks
        WHERE (blocker_id = v_user AND blocked_id = p_target)
           OR (blocker_id = p_target AND blocked_id = v_user)
    ) THEN
        RAISE EXCEPTION 'tidak diizinkan' USING ERRCODE = '42501';
    END IF;

    -- Akun privat harus menyetujui dulu; akun publik langsung diterima.
    v_status := CASE WHEN v_private THEN 'pending' ELSE 'accepted' END;

    INSERT INTO follows (follower_id, followee_id, status)
    VALUES (v_user, p_target, v_status)
    ON CONFLICT (follower_id, followee_id) DO NOTHING;

    -- Counter hanya bergerak kalau barisnya benar-benar baru DAN sudah
    -- diterima. Tanpa syarat pertama, menekan tombol follow dua kali akan
    -- menggelembungkan angkanya.
    IF FOUND AND v_status = 'accepted' THEN
        UPDATE user_profiles SET follower_count  = follower_count  + 1 WHERE user_id = p_target;
        UPDATE user_profiles SET following_count = following_count + 1 WHERE user_id = v_user;
    ELSIF NOT FOUND THEN
        SELECT status INTO v_status FROM follows
        WHERE follower_id = v_user AND followee_id = p_target;
    END IF;

    RETURN v_status;
END;
$$;

CREATE OR REPLACE FUNCTION public.unfollow_user(p_target uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user   uuid := public.require_auth();
    v_status text;
BEGIN
    DELETE FROM follows
    WHERE follower_id = v_user AND followee_id = p_target
    RETURNING status INTO v_status;

    IF v_status = 'accepted' THEN
        UPDATE user_profiles
        SET follower_count = GREATEST(follower_count - 1, 0)
        WHERE user_id = p_target;

        UPDATE user_profiles
        SET following_count = GREATEST(following_count - 1, 0)
        WHERE user_id = v_user;
    END IF;
END;
$$;

-- Daftar orang yang diikuti — sumber data layar kontak, dan cara memeriksa
-- kenapa sebuah story tidak muncul.
CREATE OR REPLACE FUNCTION public.list_following()
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
    v_user uuid := public.require_auth();
BEGIN
    RETURN QUERY
    SELECT u.id,
           u.username::text,
           COALESCE(p.display_name, ''),
           p.avatar_media_id,
           f.status,
           f.created_at
    FROM follows f
    JOIN users u ON u.id = f.followee_id AND u.deleted_at IS NULL
    LEFT JOIN user_profiles p ON p.user_id = u.id
    WHERE f.follower_id = v_user
    ORDER BY COALESCE(p.display_name, u.username::text);
END;
$$;

-- ============================================================
-- 2. PROFIL + STATUS FOLLOW
-- ============================================================
-- Layar profil butuh tahu apakah tombolnya "Follow" atau "Following", dan itu
-- tidak bisa dijawab tanpa satu query tambahan kalau tidak disertakan di sini.

DROP FUNCTION IF EXISTS public.find_user(text);

CREATE FUNCTION public.find_user(p_username text)
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
BEGIN
    RETURN QUERY
    SELECT u.id,
           u.username::text,
           COALESCE(p.display_name, ''),
           p.avatar_media_id,
           COALESCE(p.follower_count, 0),
           COALESCE(p.following_count, 0),
           COALESCE(f.status, ''),   -- '' = belum diikuti
           (u.id = v_user)
    FROM users u
    LEFT JOIN user_profiles p ON p.user_id = u.id
    LEFT JOIN follows f ON f.follower_id = v_user AND f.followee_id = u.id
    WHERE u.username = p_username::citext
      AND u.deleted_at IS NULL
      AND u.account_status = 'active'
    LIMIT 1;
END;
$$;

-- ============================================================
-- 3. BAHAN INDIKATOR ✓✓
-- ============================================================
-- Untuk chat 1:1, posisi baca lawan bicara sudah cukup untuk menggambar ✓✓:
-- id pesan adalah UUIDv7 yang terurut waktu, jadi klien cukup membandingkan
-- id pesannya dengan nilai ini — tidak butuh tabel receipt per pesan.
--
-- MESSAGE_RECEIPTS tetap hanya untuk grup, sesuai catatan di docs/erd.md:
-- receipt per-pesan per-anggota tumbuh sebesar (jumlah pesan × jumlah anggota).

DROP FUNCTION IF EXISTS public.list_conversations(timestamptz, integer);

CREATE FUNCTION public.list_conversations(
    p_before timestamptz,
    p_limit  integer
)
RETURNS TABLE (
    id                        uuid,
    type                      text,
    title                     text,
    avatar_media_id           uuid,
    counterpart_id            uuid,
    counterpart_last_read     uuid,
    unread_count              integer,
    last_message_preview      text,
    last_message_type         text,
    last_message_sender       uuid,
    last_message_at           timestamptz,
    created_at                timestamptz
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
        c.id,
        c.type,

        CASE WHEN c.type = 'group'
             THEN COALESCE(c.title, '')
             ELSE COALESCE(NULLIF(cp.display_name, ''), cu.username, '')
        END AS title,

        CASE WHEN c.type = 'group'
             THEN c.avatar_media_id
             ELSE cp.avatar_media_id
        END AS avatar_media_id,

        cu.id            AS counterpart_id,
        cm_other.last_read_message_id AS counterpart_last_read,
        cm.unread_count,

        CASE WHEN lm.deleted_at IS NOT NULL THEN ''
             ELSE COALESCE(left(lm.body, 120), '')
        END AS last_message_preview,

        COALESCE(lm.type, '')                     AS last_message_type,
        lm.sender_id                              AS last_message_sender,
        COALESCE(c.last_message_at, c.created_at) AS last_message_at,
        c.created_at

    FROM conversations c
    JOIN conversation_members cm
      ON cm.conversation_id = c.id
     AND cm.user_id = v_user
     AND cm.left_at IS NULL

    LEFT JOIN LATERAL (
        SELECT m2.user_id, u.username, m2.last_read_message_id
        FROM conversation_members m2
        JOIN users u ON u.id = m2.user_id
        WHERE m2.conversation_id = c.id
          AND m2.user_id <> v_user
          AND m2.left_at IS NULL
        ORDER BY m2.joined_at
        LIMIT 1
    ) cm_other ON c.type = 'direct'

    LEFT JOIN users         cu ON cu.id = cm_other.user_id
    LEFT JOIN user_profiles cp ON cp.user_id = cm_other.user_id
    LEFT JOIN messages      lm ON lm.id = c.last_message_id

    WHERE COALESCE(c.last_message_at, c.created_at) < p_before
    ORDER BY COALESCE(c.last_message_at, c.created_at) DESC
    LIMIT LEAST(GREATEST(p_limit, 1), 100);
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
        'public.follow_user(uuid)',
        'public.unfollow_user(uuid)',
        'public.list_following()',
        'public.find_user(text)',
        'public.list_conversations(timestamptz, integer)'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO authenticated', fn);
    END LOOP;
END;
$$;

COMMIT;
