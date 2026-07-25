-- Panggilan tak muncul lagi setelah satu panggilan gagal: call 'ongoing' zombie.
--
-- Migrasi 36 sudah mengkadaluarsakan ring 'ringing' yang tak dijawab > 60 detik,
-- TAPI tidak menyentuh 'ongoing'. Kalau sebuah panggilan sempat dijawab lalu
-- gagal menyambung media (dan aplikasi ter-kill sebelum mengirim leave), baris
-- calls tersangkut status 'ongoing' selamanya. start_call lalu MENGGABUNG diam-
-- diam ke zombie itu (is_new=false) sehingga call.incoming tak pernah disiarkan
-- dan lawan tak pernah berdering. Persis gejala "muncul sekali lalu tak bisa
-- lagi".
--
-- Panggilan di Syntra selalu 1:1 (grup pakai voice room, bukan calls). Jadi kalau
-- seseorang memulai panggilan BARU ke sebuah percakapan, panggilan lama di situ
-- pasti sudah mati (mustahil memulai panggilan sambil sedang menelepon). Maka
-- aman mengkadaluarsakan 'ongoing' yang sudah cukup lama sebelum cek panggilan
-- aktif, memaksa panggilan baru menjadi is_new=true dan benar-benar berdering.
--
-- CREATE OR REPLACE (signature & tipe kembalian sama seperti migrasi 19/36).

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

    -- Kadaluarsakan panggilan yang jelas mati sebelum cek panggilan aktif:
    --   * 'ringing' tak dijawab > 45 detik
    --   * 'ongoing' yang sudah berjalan > 2 menit (untuk 1:1, memulai panggilan
    --     baru berarti yang lama sudah mati; 2 menit memberi ruang untuk retry
    --     sambungan yang wajar tanpa menahan zombie berjam-jam)
    UPDATE calls
    SET status = 'missed', ended_at = now()
    WHERE conversation_id = p_conversation
      AND ended_at IS NULL
      AND (
            (status = 'ringing' AND started_at < now() - interval '45 seconds')
         OR (status = 'ongoing' AND started_at < now() - interval '2 minutes')
      );

    SELECT c.id, c.sfu_room_id INTO v_existing, v_sfu
    FROM calls c
    WHERE c.conversation_id = p_conversation AND c.status IN ('ringing', 'ongoing')
    LIMIT 1;

    IF v_existing IS NOT NULL THEN
        -- Bergabung ke panggilan yang benar-benar masih berlangsung.
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
