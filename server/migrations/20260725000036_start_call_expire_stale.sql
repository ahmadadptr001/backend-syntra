-- Panggilan tak sampai ke lawan: dering hantu memblokir dering baru.
--
-- start_call menganggap panggilan ber-status 'ringing'/'ongoing' di percakapan
-- itu sebagai "sedang berlangsung" lalu BERGABUNG diam-diam (is_new = false),
-- sehingga event call.incoming TIDAK disiarkan dan perangkat lawan tak pernah
-- berdering. Masalahnya: sebuah baris 'ringing' bisa tersangkut selamanya kalau
-- penelepon menutup aplikasi atau gagal menyambung sebelum ada yang menjawab
-- (tidak ada webhook participant_left karena media belum benar-benar terhubung).
-- Setelah itu SETIAP panggilan ke percakapan tersebut ikut bergabung ke hantu
-- itu: hening, tak pernah sampai.
--
-- Perbaikan: sebelum memeriksa panggilan aktif, kadaluarsakan ring yang sudah
-- lebih dari 60 detik tak terjawab (tandai 'missed'). Dengan begitu panggilan
-- baru membuat baris segar, is_new = true, call.incoming disiarkan, berdering.
--
-- CREATE OR REPLACE (signature dan tipe kembalian sama seperti migrasi 19).

BEGIN;

CREATE OR REPLACE FUNCTION public.start_call(
    p_id           uuid,
    p_conversation uuid,
    p_kind         text,
    p_sfu_room     text
)
RETURNS TABLE (call_id uuid, sfu_room_id text, is_new boolean)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user     uuid := public.require_auth();
    v_existing uuid;
    v_sfu      text;
BEGIN
    IF p_kind NOT IN ('audio', 'video') THEN
        RAISE EXCEPTION 'jenis panggilan tidak valid' USING ERRCODE = '22023';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM conversation_members cm
        WHERE cm.conversation_id = p_conversation AND cm.user_id = v_user AND cm.left_at IS NULL
    ) THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;

    -- Blokir dua arah pada chat pribadi.
    IF EXISTS (
        SELECT 1
        FROM conversation_members other
        JOIN blocks b
          ON (b.blocker_id = other.user_id AND b.blocked_id = v_user)
          OR (b.blocker_id = v_user AND b.blocked_id = other.user_id)
        JOIN conversations c ON c.id = p_conversation AND c.type = 'direct'
        WHERE other.conversation_id = p_conversation AND other.user_id <> v_user AND other.left_at IS NULL
    ) THEN
        RAISE EXCEPTION 'panggilan tidak diizinkan' USING ERRCODE = '42501';
    END IF;

    -- Kadaluarsakan dering hantu: 'ringing' yang tak dijawab lebih dari 60 detik
    -- sudah mati (penelepon pergi atau crash). Membersihkannya membuat panggilan
    -- baru berdering lagi alih-alih bergabung diam-diam ke panggilan yang tak ada.
    UPDATE calls
    SET status = 'missed', ended_at = now()
    WHERE conversation_id = p_conversation
      AND status = 'ringing'
      AND started_at < now() - interval '60 seconds';

    SELECT c.id, c.sfu_room_id INTO v_existing, v_sfu
    FROM calls c
    WHERE c.conversation_id = p_conversation AND c.status IN ('ringing', 'ongoing')
    LIMIT 1;

    IF v_existing IS NOT NULL THEN
        -- Bergabung ke panggilan yang sudah berlangsung.
        INSERT INTO call_participants (call_id, user_id, joined_at)
        VALUES (v_existing, v_user, now())
        ON CONFLICT ON CONSTRAINT call_participants_pkey
        DO UPDATE SET joined_at = now(), left_at = NULL;

        RETURN QUERY SELECT v_existing, v_sfu, false;
        RETURN;
    END IF;

    INSERT INTO calls (id, conversation_id, initiator_id, kind, status, sfu_room_id, started_at)
    VALUES (p_id, p_conversation, v_user, p_kind, 'ringing', p_sfu_room, now());

    INSERT INTO call_participants (call_id, user_id, joined_at)
    VALUES (p_id, v_user, now());

    RETURN QUERY SELECT p_id, p_sfu_room, true;
END;
$$;

REVOKE EXECUTE ON FUNCTION public.start_call(uuid, uuid, text, text) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.start_call(uuid, uuid, text, text) TO authenticated;

COMMIT;
