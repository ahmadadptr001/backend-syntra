-- Membersihkan obrolan memakai WAKTU, bukan urutan id.
--
-- MASALAH
-- clear_conversation memilih "pesan terakhir" dengan ORDER BY m.id DESC LIMIT 1, dengan
-- asumsi seluruh messages.id adalah UUIDv7 sehingga urutan byte = urutan waktu. Asumsi
-- itu benar untuk pesan pengguna: id dibuat di Go (internal/pkg/id) sebagai UUIDv7.
--
-- Tetapi post_system_message() menulis ke tabel messages memakai gen_random_uuid() —
-- UUID v4, acak penuh. UUIDv7 selalu diawali timestamp milidetik (saat ini ~0x0198…),
-- sedangkan v4 tersebar merata di seluruh ruang 128 bit, jadi hampir setiap id sistem
-- lebih besar daripada id pesan mana pun.
--
-- Akibatnya, di percakapan yang pernah punya pesan sistem, "pesan terakhir menurut id"
-- adalah pesan sistem acak itu. cleared_before_id jadi berisi nilai yang sangat besar,
-- dan filter get_messages (m.id > v_cleared) menolak SEMUA pesan sesudahnya — selamanya.
--
-- Gejalanya persis seperti yang dilaporkan: sesudah "bersihkan obrolan", balasan story
-- yang baru terkirim TIDAK muncul di dalam obrolan, padahal di beranda previewnya ada.
-- Beranda memakai jalur lain (list_conversations JOIN ON lm.id = c.last_message_id),
-- kolom yang diisi saat INSERT, jadi ia tetap benar.
--
-- PERBAIKAN
-- Simpan penanda berdasarkan created_at. Waktu adalah yang sebenarnya dimaksud, dan ia
-- tidak peduli id mana yang v4 dan mana yang v7. created_at tetap dipasangkan dengan id
-- sebagai tie-breaker supaya dua pesan pada milidetik yang sama tidak saling menutupi.

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
    IF NOT EXISTS (
        SELECT 1 FROM conversation_members
        WHERE conversation_id = p_conversation AND user_id = v_user AND left_at IS NULL
    ) THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;

    SELECT m.id INTO v_last
    FROM messages m
    WHERE m.conversation_id = p_conversation
    ORDER BY m.created_at DESC, m.id DESC
    LIMIT 1;

    UPDATE conversation_members
    SET cleared_before_id = v_last
    WHERE conversation_id = p_conversation AND user_id = v_user;
END;
$$;

-- get_messages harus menyaring dengan waktu penanda, bukan perbandingan id, karena
-- alasan yang sama: satu pesan sistem ber-id v4 sudah cukup untuk membuat perbandingan
-- id salah untuk seluruh percakapan.
-- Tanda tangan HARUS sama persis dengan yang sudah ada: nama kolom terakhir adalah
-- `attachments` (dipetakan oleh messageRow.Attachments di Go), dan parameternya tanpa
-- DEFAULT. Mengubah nama kolom mengubah row type — Postgres menolaknya (42P13), dan
-- seandainya lolos pun lampiran akan diam-diam berhenti terbaca.
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
    is_deleted          boolean,
    attachments         text
)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user       uuid := public.require_auth();
    v_cleared    uuid;
    v_cleared_at timestamptz;
    v_before_at  timestamptz;
BEGIN
    SELECT cm.cleared_before_id INTO v_cleared
    FROM conversation_members cm
    WHERE cm.conversation_id = p_conversation AND cm.user_id = v_user AND cm.left_at IS NULL;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;

    SELECT m.created_at INTO v_cleared_at FROM messages m WHERE m.id = v_cleared;
    SELECT m.created_at INTO v_before_at  FROM messages m WHERE m.id = p_before;

    RETURN QUERY
    SELECT m.id, m.conversation_id, m.sender_id, m.type,
           CASE WHEN m.deleted_at IS NOT NULL THEN '' ELSE COALESCE(m.body, '') END,
           m.reply_to_message_id, m.created_at, m.edited_at,
           (m.deleted_at IS NOT NULL),
           (SELECT string_agg(ma.storage_key, ',' ORDER BY a.position)
            FROM message_attachments a JOIN media_assets ma ON ma.id = a.media_id
            WHERE a.message_id = m.id)
    FROM messages m
    WHERE m.conversation_id = p_conversation
      AND (p_before   IS NULL OR v_before_at  IS NULL
           OR (m.created_at, m.id) < (v_before_at, p_before))
      AND (v_cleared  IS NULL OR v_cleared_at IS NULL
           OR (m.created_at, m.id) > (v_cleared_at, v_cleared))
    ORDER BY m.created_at DESC, m.id DESC
    LIMIT LEAST(GREATEST(p_limit, 1), 100);
END;
$$;

-- Sumber masalahnya sendiri: pesan sistem sebaiknya tidak menyuntikkan id acak ke tabel
-- yang seluruh urutannya bergantung pada waktu. Tidak bisa dibuat UUIDv7 penuh di SQL
-- tanpa ekstensi, tetapi 48 bit pertama bisa diisi timestamp milidetik seperti v7,
-- sehingga id sistem ikut terurut waktu dan tidak lagi melompat ke atas.
CREATE OR REPLACE FUNCTION public.post_system_message(
    p_conversation uuid,
    p_body         text
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_ms  bigint := (EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::bigint;
    v_id  uuid;
BEGIN
    v_id := (
        lpad(to_hex(v_ms), 12, '0') ||
        '7' || substr(replace(gen_random_uuid()::text, '-', ''), 13, 3) ||
        '8' || substr(replace(gen_random_uuid()::text, '-', ''), 17, 15)
    )::uuid;

    INSERT INTO messages (id, conversation_id, sender_id, type, body, created_at)
    VALUES (v_id, p_conversation, NULL, 'system', p_body, now());

    UPDATE conversations
    SET last_message_id = v_id, last_message_at = now()
    WHERE id = p_conversation;
END;
$$;

-- Tidak ada UPDATE pemulihan, dan itu disengaja.
--
-- Penanda yang terlanjur tersimpan tetap menunjuk pesan nyata dengan created_at nyata.
-- Begitu get_messages menyaring memakai waktu, penanda itu langsung bekerja benar —
-- percakapan yang tadinya kosong permanen akan menampilkan lagi pesan sesudah waktu
-- pesan tersebut. Menulis ulang penanda pengguna justru berisiko memunculkan kembali
-- riwayat yang memang sengaja mereka bersihkan.
