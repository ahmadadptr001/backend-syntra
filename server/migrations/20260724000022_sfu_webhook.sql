-- Webhook LiveKit → menutup panggilan yang ditinggalkan tanpa lapor.
--
-- Latar belakang: setelah backend menerbitkan token, ia tidak tahu apa yang
-- terjadi di dalam room SFU. Selama ini status panggilan bergantung pada
-- aplikasi memanggil leave_call/decline_call dengan jujur. Kalau aplikasi
-- crash, HP mati, atau jaringan putus di tengah panggilan, leave tidak pernah
-- terkirim dan baris calls tersangkut 'ongoing' selamanya — sama persisnya
-- dengan "room hantu" yang dulu menimpa voice room.
--
-- LiveKit Cloud bisa mengirim webhook setiap kali peserta keluar atau room
-- selesai. Dua fungsi di bawah adalah sisi database dari webhook itu: mereka
-- mencocokkan room SFU ke panggilan lalu menutupnya, meniru logika leave_call
-- tetapi tanpa auth.uid() — sebab webhook datang dari LiveKit, bukan pengguna.
--
-- Keamanan: kedua fungsi SECURITY DEFINER (menembus RLS) dan HANYA diberikan
-- ke service_role. Anon/authenticated TIDAK boleh memanggilnya — kalau bisa,
-- siapa pun yang memegang anon key dapat memaksa panggilan orang lain berakhir.
-- Backend memanggilnya dengan kunci service, di belakang verifikasi tanda
-- tangan webhook, jadi hanya event asli dari LiveKit yang sampai ke sini.
--
-- Nama kolom keluaran sengaja diberi awalan out_ supaya tidak pernah bentrok
-- dengan kolom call_id di call_participants — ambiguitas semacam itu pernah
-- membuat start_call gagal (lihat migrasi 19).

BEGIN;

-- ============================================================
-- PESERTA KELUAR (event participant_left)
-- ============================================================
-- Menandai satu peserta keluar. Kalau tidak ada peserta tersisa, panggilan
-- ikut berakhir (missed kalau belum sempat dijawab, selain itu ended).
CREATE OR REPLACE FUNCTION public.sfu_participant_left(p_sfu_room text, p_identity text)
RETURNS TABLE (out_call_id uuid, out_conversation_id uuid, out_ended boolean)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_call      uuid;
    v_conv      uuid;
    v_remaining integer;
BEGIN
    SELECT c.id, c.conversation_id INTO v_call, v_conv
    FROM calls c
    WHERE c.sfu_room_id = p_sfu_room AND c.status IN ('ringing', 'ongoing')
    ORDER BY c.started_at DESC
    LIMIT 1;

    IF v_call IS NULL THEN
        RETURN;  -- tidak ada panggilan aktif untuk room ini; abaikan diam-diam
    END IF;

    UPDATE call_participants cp
    SET left_at = now()
    WHERE cp.call_id = v_call AND cp.user_id::text = p_identity AND cp.left_at IS NULL;

    SELECT count(*) INTO v_remaining
    FROM call_participants cp
    WHERE cp.call_id = v_call AND cp.left_at IS NULL;

    IF v_remaining = 0 THEN
        UPDATE calls
        SET status   = CASE WHEN status = 'ringing' THEN 'missed' ELSE 'ended' END,
            ended_at = now()
        WHERE id = v_call AND status IN ('ringing', 'ongoing');

        RETURN QUERY SELECT v_call, v_conv, true;
    ELSE
        RETURN QUERY SELECT v_call, v_conv, false;
    END IF;
END;
$$;

-- ============================================================
-- ROOM SELESAI (event room_finished)
-- ============================================================
-- LiveKit menutup room saat peserta terakhir pergi. Ini jaring pengaman:
-- tutup panggilan dan keluarkan sisa peserta apa pun keadaannya.
CREATE OR REPLACE FUNCTION public.sfu_room_finished(p_sfu_room text)
RETURNS TABLE (out_call_id uuid, out_conversation_id uuid, out_ended boolean)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_call uuid;
    v_conv uuid;
BEGIN
    SELECT c.id, c.conversation_id INTO v_call, v_conv
    FROM calls c
    WHERE c.sfu_room_id = p_sfu_room AND c.status IN ('ringing', 'ongoing')
    ORDER BY c.started_at DESC
    LIMIT 1;

    IF v_call IS NULL THEN
        RETURN;
    END IF;

    UPDATE call_participants cp
    SET left_at = now()
    WHERE cp.call_id = v_call AND cp.left_at IS NULL;

    UPDATE calls
    SET status   = CASE WHEN status = 'ringing' THEN 'missed' ELSE 'ended' END,
        ended_at = now()
    WHERE id = v_call AND status IN ('ringing', 'ongoing');

    RETURN QUERY SELECT v_call, v_conv, true;
END;
$$;

-- Hanya service_role — bukan anon/authenticated. Backend memanggil dengan kunci
-- service di belakang verifikasi tanda tangan webhook.
DO $$
DECLARE
    fn text;
BEGIN
    FOREACH fn IN ARRAY ARRAY[
        'public.sfu_participant_left(text, text)',
        'public.sfu_room_finished(text)'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO service_role', fn);
    END LOOP;
END;
$$;

COMMIT;
