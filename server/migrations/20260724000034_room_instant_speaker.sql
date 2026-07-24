-- Voice room tanpa antre bicara: sekali gabung, langsung boleh menyalakan mic.
--
-- Sebelumnya non-host masuk sebagai 'listener' dan harus mengangkat tangan lalu
-- disetujui host untuk bisa bicara (canPublish). Permintaan aplikasi: siapa pun
-- yang gabung langsung bisa pencet mic — tak perlu persetujuan.
--
-- Perubahan: join_room memberi peran 'speaker' ke semua non-host (host tetap
-- 'host'), mulai dalam keadaan mute (is_muted = true) supaya tidak tiba-tiba
-- menyiarkan suara sampai penggunanya sendiri menyalakan mic. Alur invite_only
-- yang menahan token tetap dipertahankan (itu soal SIAPA boleh masuk, bukan
-- soal boleh bicara).

BEGIN;

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

    -- Semua non-host masuk sebagai speaker (boleh bicara), tapi mulai mute.
    v_role := CASE WHEN v_room.host_id = v_user THEN 'host' ELSE 'speaker' END;

    INSERT INTO room_participants (id, room_id, user_id, role, is_muted)
    VALUES (gen_random_uuid(), p_room, v_user, v_role, v_role <> 'host')
    ON CONFLICT (room_id, user_id) WHERE left_at IS NULL
    DO UPDATE SET joined_at = now(), role = EXCLUDED.role
    RETURNING room_participants.role INTO v_role;

    UPDATE rooms SET peak_participant_count = GREATEST(peak_participant_count, v_count + 1)
    WHERE rooms.id = p_room;

    RETURN QUERY SELECT 'joined'::text, v_role, v_room.sfu_room_id;
END;
$$;

REVOKE EXECUTE ON FUNCTION public.join_room(uuid) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.join_room(uuid) TO authenticated;

COMMIT;
