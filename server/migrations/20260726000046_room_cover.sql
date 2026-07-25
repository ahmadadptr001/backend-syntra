-- Ekspos background/cover profil ke daftar room dan peserta room.
--
-- App: kartu di daftar room dan ubin peserta di dalam room memakai BACKGROUND
-- profil (cover) sebagai latar, bukan foto profil. Saat video dimatikan, ubin
-- menampilkan cover profil. Karena itu list_rooms perlu cover host, dan
-- list_room_participants perlu cover tiap peserta.
--
-- list_rooms: host_avatar diubah dari uuid (id media) menjadi storage_key (text)
-- supaya bisa langsung di-resolve jadi URL seperti avatar peserta, plus kolom
-- baru host_cover. Kedua RETURNS TABLE berubah => DROP lalu CREATE. Go graceful:
-- kolom absen => string kosong => klien pakai gradient bawaan. ASCII murni.

BEGIN;

-- ============================================================
-- list_rooms: host_avatar (storage_key) + host_cover (storage_key)
-- ============================================================
DROP FUNCTION IF EXISTS public.list_rooms();

CREATE FUNCTION public.list_rooms()
RETURNS TABLE (
    id                uuid,
    host_id           uuid,
    host_username     text,
    host_name         text,
    host_avatar       text,   -- storage_key (dulu uuid)
    host_cover        text,   -- storage_key background/cover profil host
    title             text,
    topic             text,
    visibility        text,
    participant_count integer,
    speaker_count     integer,
    max_participants  integer,
    started_at        timestamptz
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
        r.id,
        r.host_id,
        u.username::text,
        COALESCE(p.display_name, ''),
        COALESCE(av.storage_key, ''),
        COALESCE(cv.storage_key, ''),
        r.title,
        r.topic,
        r.visibility,
        COALESCE(cnt.total, 0)::integer,
        COALESCE(cnt.speakers, 0)::integer,
        r.max_participants,
        r.started_at
    FROM rooms r
    JOIN users u ON u.id = r.host_id AND u.deleted_at IS NULL
    LEFT JOIN user_profiles p  ON p.user_id = r.host_id
    LEFT JOIN media_assets  av ON av.id = p.avatar_media_id
    LEFT JOIN media_assets  cv ON cv.id = p.cover_media_id
    LEFT JOIN LATERAL (
        SELECT count(*) AS total,
               count(*) FILTER (WHERE rp.role IN ('host', 'moderator', 'speaker')) AS speakers
        FROM room_participants rp
        WHERE rp.room_id = r.id AND rp.left_at IS NULL
    ) cnt ON true
    WHERE r.status = 'live'
      AND (
            r.visibility = 'public'
         OR r.host_id = v_user
         OR (r.visibility = 'followers' AND EXISTS (
                SELECT 1 FROM follows f
                WHERE f.follower_id = v_user AND f.followee_id = r.host_id
                  AND f.status = 'accepted'))
          )
      AND NOT EXISTS (
            SELECT 1 FROM blocks b
            WHERE (b.blocker_id = r.host_id AND b.blocked_id = v_user)
               OR (b.blocker_id = v_user AND b.blocked_id = r.host_id)
          )
    ORDER BY r.started_at DESC;
END;
$$;

-- ============================================================
-- list_room_participants: + cover_key (storage_key)
-- ============================================================
DROP FUNCTION IF EXISTS public.list_room_participants(uuid);

CREATE FUNCTION public.list_room_participants(p_room uuid)
RETURNS TABLE (
    user_id         uuid,
    username        text,
    display_name    text,
    avatar_key      text,
    cover_key       text,
    role            text,
    is_muted        boolean,
    has_raised_hand boolean,
    joined_at       timestamptz
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
BEGIN
    PERFORM public.require_auth();

    IF NOT EXISTS (SELECT 1 FROM rooms r WHERE r.id = p_room AND r.status = 'live') THEN
        RAISE EXCEPTION 'room tidak ditemukan atau sudah berakhir' USING ERRCODE = 'P0002';
    END IF;

    RETURN QUERY
    SELECT rp.user_id,
           u.username::text,
           COALESCE(p.display_name, ''),
           COALESCE(ma.storage_key, ''),
           COALESCE(cv.storage_key, ''),
           rp.role,
           rp.is_muted,
           (rsr.status = 'pending'),
           rp.joined_at
    FROM room_participants rp
    JOIN users u ON u.id = rp.user_id
    LEFT JOIN user_profiles p  ON p.user_id = rp.user_id
    LEFT JOIN media_assets  ma ON ma.id = p.avatar_media_id
    LEFT JOIN media_assets  cv ON cv.id = p.cover_media_id
    LEFT JOIN room_speak_requests rsr
           ON rsr.room_id = rp.room_id AND rsr.user_id = rp.user_id
    WHERE rp.room_id = p_room AND rp.left_at IS NULL
    ORDER BY
        CASE rp.role WHEN 'host' THEN 0 WHEN 'moderator' THEN 1
                     WHEN 'speaker' THEN 2 ELSE 3 END,
        rp.joined_at;
END;
$$;

DO $$
BEGIN
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.list_rooms() FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.list_rooms() TO authenticated';
    EXECUTE 'REVOKE EXECUTE ON FUNCTION public.list_room_participants(uuid) FROM PUBLIC';
    EXECUTE 'GRANT  EXECUTE ON FUNCTION public.list_room_participants(uuid) TO authenticated';
END;
$$;

COMMIT;
