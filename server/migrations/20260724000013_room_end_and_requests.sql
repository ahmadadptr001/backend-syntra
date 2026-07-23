-- Menjawab pesan tim aplikasi (pesan-untuk-backend.md).
--
-- Tiga dari tujuh keluhan ternyata sudah tidak berlaku — mereka menguji sebelum
-- migrasi 9 dan sebelum AUTH_DEV_BYPASS dimatikan:
--   * host_id berupa JWT       → kini UUID (akar masalahnya bypass auth)
--   * max_participants = 0     → kini 50
--   * has_raised_hand mentok   → kini turun sendiri saat peran jadi speaker
--
-- Yang di bawah ini benar-benar belum ada.

BEGIN;

-- ============================================================
-- 1. MENGAKHIRI ROOM SECARA EKSPLISIT
-- ============================================================
-- leave_room sudah mengakhiri room saat host keluar, tetapi aplikasi butuh
-- tombol "akhiri room" yang berdiri sendiri — host mungkin ingin menutup room
-- tanpa harus keluar lebih dulu.

CREATE OR REPLACE FUNCTION public.end_room_by_host(p_room uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_host uuid;
BEGIN
    SELECT r.host_id INTO v_host FROM rooms r WHERE r.id = p_room AND r.status = 'live';

    IF NOT FOUND THEN
        RAISE EXCEPTION 'room tidak ditemukan atau sudah berakhir' USING ERRCODE = 'P0002';
    END IF;

    IF v_host <> v_user THEN
        RAISE EXCEPTION 'hanya host yang boleh mengakhiri room' USING ERRCODE = '42501';
    END IF;

    PERFORM public.end_room(p_room);
END;
$$;

-- Peserta yang masih membuka layar room perlu tahu room-nya sudah tutup.
-- Sebelumnya fungsi ini membalas daftar kosong dengan status 200, yang tidak
-- bisa dibedakan dari "room sepi" — aplikasi meminta 404 supaya bisa langsung
-- mengeluarkan penggunanya.
DROP FUNCTION IF EXISTS public.list_room_participants(uuid);

CREATE FUNCTION public.list_room_participants(p_room uuid)
RETURNS TABLE (
    user_id         uuid,
    username        text,
    display_name    text,
    avatar_key      text,
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
           rp.role,
           rp.is_muted,
           (rsr.status = 'pending'),
           rp.joined_at
    FROM room_participants rp
    JOIN users u ON u.id = rp.user_id
    LEFT JOIN user_profiles p  ON p.user_id = rp.user_id
    LEFT JOIN media_assets  ma ON ma.id = p.avatar_media_id
    LEFT JOIN room_speak_requests rsr
           ON rsr.room_id = rp.room_id AND rsr.user_id = rp.user_id
    WHERE rp.room_id = p_room AND rp.left_at IS NULL
    ORDER BY
        CASE rp.role WHEN 'host' THEN 0 WHEN 'moderator' THEN 1
                     WHEN 'speaker' THEN 2 ELSE 3 END,
        rp.joined_at;
END;
$$;

-- ============================================================
-- 2. MEMBATALKAN ANGKAT TANGAN
-- ============================================================
-- Bendera turun sendiri saat peran naik jadi speaker, tetapi peminta juga harus
-- bisa menariknya kembali.

CREATE OR REPLACE FUNCTION public.cancel_speak_request(p_room uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    DELETE FROM room_speak_requests rsr
    WHERE rsr.room_id = p_room AND rsr.user_id = v_user;
END;
$$;

-- ============================================================
-- 3. PERSETUJUAN MASUK ROOM
-- ============================================================
-- Aplikasi meminta alur persetujuan, bukan undangan yang dikirim lebih dulu:
-- peserta menekan Join, host memutuskan. Sebelumnya invite_only mensyaratkan
-- undangan sudah ada sebelum orangnya mencoba masuk.
--
-- Ditegakkan di sisi server dengan sengaja: kalau aplikasi yang "menahan"
-- peserta, siapa pun yang memanggil endpoint langsung tetap bisa masuk dan
-- bicara.

CREATE TABLE IF NOT EXISTS room_join_requests (
    room_id      uuid NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status       text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'rejected')),
    requested_at timestamptz NOT NULL DEFAULT now(),
    decided_at   timestamptz,

    PRIMARY KEY (room_id, user_id)
);

ALTER TABLE room_join_requests ENABLE ROW LEVEL SECURITY;

-- join_room versi baru. Mengembalikan status supaya backend tahu harus
-- menerbitkan token SFU atau menahan pemanggil di ruang tunggu.
DROP FUNCTION IF EXISTS public.join_room(uuid);

CREATE FUNCTION public.join_room(p_room uuid)
RETURNS TABLE (status text, role text, sfu_room_id text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_room  rooms%ROWTYPE;
    v_count integer;
    v_role  text;
    v_req   text;
BEGIN
    SELECT * INTO v_room FROM rooms WHERE rooms.id = p_room AND rooms.status = 'live';
    IF NOT FOUND THEN
        RAISE EXCEPTION 'room tidak ditemukan atau sudah berakhir' USING ERRCODE = 'P0002';
    END IF;

    IF EXISTS (
        SELECT 1 FROM blocks b
        WHERE (b.blocker_id = v_room.host_id AND b.blocked_id = v_user)
           OR (b.blocker_id = v_user AND b.blocked_id = v_room.host_id)
    ) THEN
        RAISE EXCEPTION 'tidak diizinkan' USING ERRCODE = '42501';
    END IF;

    IF v_room.host_id <> v_user THEN
        IF v_room.visibility = 'followers'
           AND NOT EXISTS (
                SELECT 1 FROM follows f
                WHERE f.follower_id = v_user AND f.followee_id = v_room.host_id
                  AND f.status = 'accepted') THEN
            RAISE EXCEPTION 'room hanya untuk pengikut' USING ERRCODE = '42501';
        END IF;

        IF v_room.visibility = 'invite_only' THEN
            -- Undangan yang sudah ada tetap dihormati; kalau belum ada,
            -- permintaan masuk antre menunggu keputusan host.
            IF NOT EXISTS (SELECT 1 FROM room_invites ri
                           WHERE ri.room_id = p_room AND ri.user_id = v_user) THEN

                SELECT rjr.status INTO v_req FROM room_join_requests rjr
                WHERE rjr.room_id = p_room AND rjr.user_id = v_user;

                IF v_req = 'rejected' THEN
                    RAISE EXCEPTION 'permintaan masuk ditolak' USING ERRCODE = '42501';
                END IF;

                IF v_req IS DISTINCT FROM 'approved' THEN
                    INSERT INTO room_join_requests (room_id, user_id, status)
                    VALUES (p_room, v_user, 'pending')
                    ON CONFLICT (room_id, user_id) DO NOTHING;

                    RETURN QUERY SELECT 'pending'::text, ''::text, ''::text;
                    RETURN;
                END IF;
            END IF;
        END IF;
    END IF;

    SELECT count(*) INTO v_count
    FROM room_participants rp WHERE rp.room_id = p_room AND rp.left_at IS NULL;

    IF v_count >= v_room.max_participants THEN
        RAISE EXCEPTION 'room penuh' USING ERRCODE = '53400';
    END IF;

    v_role := CASE WHEN v_room.host_id = v_user THEN 'host' ELSE 'listener' END;

    INSERT INTO room_participants (id, room_id, user_id, role, is_muted)
    VALUES (gen_random_uuid(), p_room, v_user, v_role, v_role <> 'host')
    ON CONFLICT (room_id, user_id) WHERE left_at IS NULL
    DO UPDATE SET joined_at = now()
    RETURNING room_participants.role INTO v_role;

    UPDATE rooms SET peak_participant_count = GREATEST(peak_participant_count, v_count + 1)
    WHERE rooms.id = p_room;

    RETURN QUERY SELECT 'joined'::text, v_role, v_room.sfu_room_id;
END;
$$;

CREATE OR REPLACE FUNCTION public.list_join_requests(p_room uuid)
RETURNS TABLE (
    user_id      uuid,
    username     text,
    display_name text,
    avatar_key   text,
    requested_at timestamptz
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
        SELECT 1 FROM room_participants rp
        WHERE rp.room_id = p_room AND rp.user_id = v_user
          AND rp.left_at IS NULL AND rp.role IN ('host', 'moderator')
    ) THEN
        RAISE EXCEPTION 'hanya host atau moderator' USING ERRCODE = '42501';
    END IF;

    RETURN QUERY
    SELECT rjr.user_id,
           u.username::text,
           COALESCE(p.display_name, ''),
           COALESCE(ma.storage_key, ''),
           rjr.requested_at
    FROM room_join_requests rjr
    JOIN users u ON u.id = rjr.user_id
    LEFT JOIN user_profiles p  ON p.user_id = rjr.user_id
    LEFT JOIN media_assets  ma ON ma.id = p.avatar_media_id
    WHERE rjr.room_id = p_room AND rjr.status = 'pending'
    ORDER BY rjr.requested_at;
END;
$$;

CREATE OR REPLACE FUNCTION public.decide_join_request(
    p_room    uuid,
    p_user    uuid,
    p_approve boolean
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM room_participants rp
        WHERE rp.room_id = p_room AND rp.user_id = v_user
          AND rp.left_at IS NULL AND rp.role IN ('host', 'moderator')
    ) THEN
        RAISE EXCEPTION 'hanya host atau moderator' USING ERRCODE = '42501';
    END IF;

    UPDATE room_join_requests rjr
    SET status     = CASE WHEN p_approve THEN 'approved' ELSE 'rejected' END,
        decided_at = now()
    WHERE rjr.room_id = p_room AND rjr.user_id = p_user;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'permintaan tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;
END;
$$;

-- ============================================================
-- 4. HAPUS PESAN
-- ============================================================
-- Soft delete, alasannya sama seperti pada story: permintaan moderasi bisa
-- datang setelah pesan hilang dari layar. Barisnya tetap dikirim ke klien
-- dengan is_deleted=true supaya urutan riwayat tidak berlubang.

CREATE OR REPLACE FUNCTION public.delete_message(p_message uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user   uuid := public.require_auth();
    v_sender uuid;
BEGIN
    SELECT m.sender_id INTO v_sender
    FROM messages m WHERE m.id = p_message AND m.deleted_at IS NULL;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'pesan tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    IF v_sender <> v_user THEN
        RAISE EXCEPTION 'pesan bukan milik pengguna ini' USING ERRCODE = '42501';
    END IF;

    UPDATE messages m SET deleted_at = now() WHERE m.id = p_message;
END;
$$;

-- Mengosongkan percakapan HANYA untuk pemanggil.
--
-- Menghapus pesan orang lain dari layar mereka bukan wewenang siapa pun di
-- percakapan, jadi yang dicatat adalah batas baca: pesan lama disembunyikan
-- dari pemanggil, sementara peserta lain tetap melihat riwayatnya utuh.
ALTER TABLE conversation_members
    ADD COLUMN IF NOT EXISTS cleared_before_id uuid;

CREATE OR REPLACE FUNCTION public.clear_conversation(p_conversation uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_last uuid;
BEGIN
    SELECT max(m.id) INTO v_last FROM messages m WHERE m.conversation_id = p_conversation;

    UPDATE conversation_members cm
    SET cleared_before_id = COALESCE(v_last, cm.cleared_before_id),
        unread_count      = 0
    WHERE cm.conversation_id = p_conversation AND cm.user_id = v_user AND cm.left_at IS NULL;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;
END;
$$;

-- get_messages menghormati batas itu.
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
    v_user    uuid := public.require_auth();
    v_cleared uuid;
BEGIN
    SELECT cm.cleared_before_id INTO v_cleared
    FROM conversation_members cm
    WHERE cm.conversation_id = p_conversation AND cm.user_id = v_user AND cm.left_at IS NULL;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;

    RETURN QUERY
    SELECT m.id, m.conversation_id, m.sender_id, m.type,
           CASE WHEN m.deleted_at IS NOT NULL THEN '' ELSE COALESCE(m.body, '') END,
           m.reply_to_message_id, m.created_at, m.edited_at,
           (m.deleted_at IS NOT NULL)
    FROM messages m
    WHERE m.conversation_id = p_conversation
      AND (p_before IS NULL OR m.id < p_before)
      AND (v_cleared IS NULL OR m.id > v_cleared)
    ORDER BY m.id DESC
    LIMIT LEAST(GREATEST(p_limit, 1), 100);
END;
$$;

-- ============================================================
-- 5. MENYETUJUI PERMINTAAN FOLLOW
-- ============================================================
-- Akun privat menghasilkan status 'pending' yang selama ini menggantung
-- selamanya karena tidak ada cara menyetujuinya.

CREATE OR REPLACE FUNCTION public.list_follow_requests()
RETURNS TABLE (
    user_id      uuid,
    username     text,
    display_name text,
    avatar_key   text,
    requested_at timestamptz
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
    SELECT f.follower_id,
           u.username::text,
           COALESCE(p.display_name, ''),
           COALESCE(ma.storage_key, ''),
           f.created_at
    FROM follows f
    JOIN users u ON u.id = f.follower_id AND u.deleted_at IS NULL
    LEFT JOIN user_profiles p  ON p.user_id = f.follower_id
    LEFT JOIN media_assets  ma ON ma.id = p.avatar_media_id
    WHERE f.followee_id = v_user AND f.status = 'pending'
    ORDER BY f.created_at;
END;
$$;

CREATE OR REPLACE FUNCTION public.decide_follow_request(
    p_follower uuid,
    p_approve  boolean
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF p_approve THEN
        UPDATE follows f SET status = 'accepted'
        WHERE f.follower_id = p_follower AND f.followee_id = v_user AND f.status = 'pending';

        IF NOT FOUND THEN
            RAISE EXCEPTION 'permintaan tidak ditemukan' USING ERRCODE = 'P0002';
        END IF;

        UPDATE user_profiles p SET follower_count  = follower_count  + 1 WHERE p.user_id = v_user;
        UPDATE user_profiles p SET following_count = following_count + 1 WHERE p.user_id = p_follower;
    ELSE
        DELETE FROM follows f
        WHERE f.follower_id = p_follower AND f.followee_id = v_user AND f.status = 'pending';
    END IF;
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
        'public.end_room_by_host(uuid)',
        'public.list_room_participants(uuid)',
        'public.cancel_speak_request(uuid)',
        'public.join_room(uuid)',
        'public.list_join_requests(uuid)',
        'public.decide_join_request(uuid, uuid, boolean)',
        'public.delete_message(uuid)',
        'public.clear_conversation(uuid)',
        'public.get_messages(uuid, uuid, integer)',
        'public.list_follow_requests()',
        'public.decide_follow_request(uuid, boolean)'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO authenticated', fn);
    END LOOP;
END;
$$;

-- Bersihkan room lama yang diminta tim aplikasi.
UPDATE rooms r
SET status = 'ended', ended_at = COALESCE(r.ended_at, now())
WHERE r.status = 'live'
  AND r.started_at < now() - interval '2 hours';

UPDATE room_participants rp
SET left_at = now()
WHERE rp.left_at IS NULL
  AND EXISTS (SELECT 1 FROM rooms r WHERE r.id = rp.room_id AND r.status = 'ended');

COMMIT;
