-- Syntra — RLS policies + fungsi RPC
--
-- Migrasi pertama mengaktifkan Row Level Security tanpa satu pun policy,
-- artinya semua tertutup rapat. Berkas ini yang membukanya secukupnya.
--
-- Kenapa lapisan ini dibutuhkan: server terhubung memakai ANON KEY, bukan
-- kredensial istimewa. Anon key tidak memberi hak apa pun — yang menentukan
-- baris mana yang boleh disentuh adalah policy di bawah ini, dievaluasi
-- terhadap auth.uid() dari JWT pengguna yang diteruskan server pada setiap
-- permintaan. Tanpa berkas ini, aplikasi akan berjalan tetapi setiap query
-- mengembalikan nol baris.

BEGIN;

-- ============================================================
-- 1. HELPER
-- ============================================================

-- Memeriksa keanggotaan percakapxan untuk pengguna yang sedang login.
--
-- SECURITY DEFINER di sini bukan sekadar kenyamanan, tapi keharusan:
-- fungsi ini dipakai di dalam policy tabel conversation_members sendiri.
-- Kalau ia tunduk pada RLS, mengevaluasi policy akan memanggil fungsi yang
-- kembali mengevaluasi policy — rekursi tak berujung. Ini jebakan Supabase
-- yang paling sering ditemui orang.
CREATE OR REPLACE FUNCTION public.is_conversation_member(p_conversation uuid)
RETURNS boolean
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
    SELECT EXISTS (
        SELECT 1
        FROM conversation_members
        WHERE conversation_id = p_conversation
          AND user_id = auth.uid()
          AND left_at IS NULL
    );
$$;

-- Menghentikan eksekusi kalau permintaan datang tanpa identitas.
CREATE OR REPLACE FUNCTION public.require_auth()
RETURNS uuid
LANGUAGE plpgsql
STABLE
AS $$
DECLARE
    v_user uuid := auth.uid();
BEGIN
    IF v_user IS NULL THEN
        -- Penyebab tersering: server meneruskan anon key, bukan JWT pengguna.
        RAISE EXCEPTION 'permintaan tidak membawa identitas pengguna'
            USING ERRCODE = '28000';
    END IF;
    RETURN v_user;
END;
$$;

-- ============================================================
-- 2. FUNGSI RPC — dipanggil internal/repository/supabase
-- ============================================================

-- Daftar percakapan milik pengguna, terbaru dulu.
CREATE OR REPLACE FUNCTION public.list_conversations(
    p_before timestamptz,
    p_limit  integer
)
RETURNS TABLE (
    id              uuid,
    type            text,
    title           text,
    unread_count    integer,
    last_message_at timestamptz,
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
    SELECT c.id,
           c.type,
           COALESCE(c.title, '')                        AS title,
           cm.unread_count,
           COALESCE(c.last_message_at, c.created_at)    AS last_message_at,
           c.created_at
    FROM conversations c
    JOIN conversation_members cm
      ON cm.conversation_id = c.id
     AND cm.user_id = v_user
     AND cm.left_at IS NULL
    WHERE COALESCE(c.last_message_at, c.created_at) < p_before
    ORDER BY COALESCE(c.last_message_at, c.created_at) DESC
    LIMIT LEAST(GREATEST(p_limit, 1), 100);
END;
$$;

-- Menyimpan pesan, memperbarui ringkasan percakapan, dan menaikkan unread
-- anggota lain — ketiganya dalam SATU transaksi.
--
-- Inilah alasan operasi ini menjadi fungsi database alih-alih tiga panggilan
-- HTTP: lewat PostgREST, tiga panggilan berarti tiga transaksi terpisah, dan
-- kegagalan di tengah meninggalkan pesan yang tersimpan tetapi tidak pernah
-- muncul di daftar chat.
CREATE OR REPLACE FUNCTION public.send_message(
    p_id           uuid,
    p_conversation uuid,
    p_type         text,
    p_body         text,
    p_reply_to     uuid,
    p_created_at   timestamptz
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
        SELECT 1 FROM conversation_members
        WHERE conversation_id = p_conversation
          AND user_id = v_user
          AND left_at IS NULL
    ) THEN
        -- Dipetakan menjadi chat.ErrNotMember oleh translate() di Go.
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;

    INSERT INTO messages (id, conversation_id, sender_id, type, body, reply_to_message_id, created_at)
    VALUES (p_id, p_conversation, v_user, COALESCE(p_type, 'text'), p_body, p_reply_to, p_created_at);

    UPDATE conversations
    SET last_message_id = p_id,
        last_message_at = p_created_at
    WHERE id = p_conversation;

    UPDATE conversation_members
    SET unread_count = unread_count + 1
    WHERE conversation_id = p_conversation
      AND user_id <> v_user
      AND left_at IS NULL;
END;
$$;

-- Menandai percakapan sudah dibaca sampai pesan tertentu.
CREATE OR REPLACE FUNCTION public.mark_conversation_read(
    p_conversation uuid,
    p_message      uuid
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_rows integer;
BEGIN
    UPDATE conversation_members
    SET last_read_message_id = p_message,
        unread_count = 0
    WHERE conversation_id = p_conversation
      AND user_id = v_user
      AND left_at IS NULL;

    GET DIAGNOSTICS v_rows = ROW_COUNT;
    IF v_rows = 0 THEN
        -- Dipetakan menjadi chat.ErrNotFound oleh translate() di Go.
        RAISE EXCEPTION 'percakapan tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;
END;
$$;

-- ============================================================
-- 3. HAK EKSEKUSI
-- ============================================================
-- PostgreSQL memberi EXECUTE ke PUBLIC secara bawaan, termasuk ke role anon.
-- Semua fungsi di atas dicabut dulu, lalu diberikan hanya kepada pengguna
-- yang sudah login. Tanpa langkah ini, siapa pun yang memegang anon key —
-- dan anon key memang publik — bisa memanggilnya.

REVOKE EXECUTE ON FUNCTION public.require_auth()                                          FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.is_conversation_member(uuid)                            FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.list_conversations(timestamptz, integer)                FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.send_message(uuid, uuid, text, text, uuid, timestamptz) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION public.mark_conversation_read(uuid, uuid)                      FROM PUBLIC;

GRANT EXECUTE ON FUNCTION public.require_auth()                                          TO authenticated;
GRANT EXECUTE ON FUNCTION public.is_conversation_member(uuid)                            TO authenticated;
GRANT EXECUTE ON FUNCTION public.list_conversations(timestamptz, integer)                TO authenticated;
GRANT EXECUTE ON FUNCTION public.send_message(uuid, uuid, text, text, uuid, timestamptz) TO authenticated;
GRANT EXECUTE ON FUNCTION public.mark_conversation_read(uuid, uuid)                      TO authenticated;

-- ============================================================
-- 4. RLS POLICIES
-- ============================================================
-- Policy di bawah menjadi penting ketika klien Kotlin nanti menyentuh
-- Supabase secara langsung — misalnya untuk unggah media atau Realtime.
-- Server Go sendiri berjalan lewat fungsi SECURITY DEFINER di atas, yang
-- memang tidak tunduk pada policy ini.
--
-- Peran `anon` sengaja tidak diberi policy apa pun: pengguna yang belum
-- login tidak boleh membaca apa-apa.

-- --- identitas ---

CREATE POLICY users_select_active ON users
    FOR SELECT TO authenticated
    USING (deleted_at IS NULL);

CREATE POLICY users_update_self ON users
    FOR UPDATE TO authenticated
    USING (id = auth.uid()) WITH CHECK (id = auth.uid());

CREATE POLICY user_profiles_select ON user_profiles
    FOR SELECT TO authenticated USING (true);

CREATE POLICY user_profiles_write_self ON user_profiles
    FOR ALL TO authenticated
    USING (user_id = auth.uid()) WITH CHECK (user_id = auth.uid());

CREATE POLICY user_settings_self ON user_settings
    FOR ALL TO authenticated
    USING (user_id = auth.uid()) WITH CHECK (user_id = auth.uid());

CREATE POLICY devices_self ON devices
    FOR ALL TO authenticated
    USING (user_id = auth.uid()) WITH CHECK (user_id = auth.uid());

-- --- social graph ---

CREATE POLICY follows_select ON follows
    FOR SELECT TO authenticated USING (true);

CREATE POLICY follows_write_self ON follows
    FOR ALL TO authenticated
    USING (follower_id = auth.uid()) WITH CHECK (follower_id = auth.uid());

CREATE POLICY blocks_self ON blocks
    FOR ALL TO authenticated
    USING (blocker_id = auth.uid()) WITH CHECK (blocker_id = auth.uid());

-- --- media ---

CREATE POLICY media_select ON media_assets
    FOR SELECT TO authenticated USING (true);

CREATE POLICY media_insert_own ON media_assets
    FOR INSERT TO authenticated WITH CHECK (owner_id = auth.uid());

-- --- chat ---

CREATE POLICY conversations_select_member ON conversations
    FOR SELECT TO authenticated
    USING (public.is_conversation_member(id));

CREATE POLICY conversation_members_select ON conversation_members
    FOR SELECT TO authenticated
    USING (user_id = auth.uid() OR public.is_conversation_member(conversation_id));

CREATE POLICY messages_select_member ON messages
    FOR SELECT TO authenticated
    USING (public.is_conversation_member(conversation_id));

-- Menulis pesan langsung dari klien sengaja TIDAK diizinkan. Pengiriman harus
-- lewat send_message() supaya ringkasan percakapan dan unread ikut terjaga.

CREATE POLICY message_attachments_select ON message_attachments
    FOR SELECT TO authenticated
    USING (EXISTS (
        SELECT 1 FROM messages m
        WHERE m.id = message_attachments.message_id
          AND public.is_conversation_member(m.conversation_id)
    ));

-- --- notifikasi, laporan, consent ---

CREATE POLICY notifications_own ON notifications
    FOR ALL TO authenticated
    USING (recipient_id = auth.uid()) WITH CHECK (recipient_id = auth.uid());

-- Laporan bisa dibuat, tetapi tidak bisa dibaca ulang oleh pelapor:
-- membocorkan status penanganan membuka pintu penyalahgunaan sistem laporan.
CREATE POLICY reports_insert_own ON reports
    FOR INSERT TO authenticated WITH CHECK (reporter_id = auth.uid());

CREATE POLICY consents_own ON consents
    FOR ALL TO authenticated
    USING (user_id = auth.uid()) WITH CHECK (user_id = auth.uid());

COMMIT;
