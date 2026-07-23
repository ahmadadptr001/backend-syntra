-- Melengkapi chat & grup agar setara fitur WhatsApp.
--
-- Klarifikasi soal "bug grup": grup memang hanya tampil ke anggotanya, dan itu
-- benar — grup bukan kanal publik. Yang selama ini kurang adalah cara MENGELOLA
-- anggota setelah grup dibuat, sehingga grup terasa "cuma untuk yang diundang".
--
-- Yang ditambahkan:
--   1. Info grup + daftar anggota
--   2. Tambah/keluarkan anggota, keluar sendiri, ganti judul/avatar
--   3. Pesan sistem otomatis ("X menambahkan Y", "Z keluar")
--   4. Reaksi emoji pada pesan
--   5. Bisukan percakapan
--   6. Lampiran media pada pesan

BEGIN;

-- ============================================================
-- 1. REAKSI PESAN
-- ============================================================

CREATE TABLE IF NOT EXISTS message_reactions (
    message_id uuid NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id)    ON DELETE CASCADE,
    emoji      text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),

    -- Satu orang satu reaksi per pesan. Bereaksi lagi mengganti emojinya,
    -- persis WhatsApp — bukan menumpuk.
    PRIMARY KEY (message_id, user_id)
);

ALTER TABLE message_reactions ENABLE ROW LEVEL SECURITY;

-- ============================================================
-- 2. HELPER: pesan sistem
-- ============================================================
-- Pesan bertipe 'system' muncul di aliran chat sebagai keterangan abu-abu
-- ("Budi menambahkan Citra"). body-nya sudah jadi teks siap tampil.

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
    v_id uuid := gen_random_uuid();
BEGIN
    INSERT INTO messages (id, conversation_id, sender_id, type, body, created_at)
    VALUES (v_id, p_conversation, NULL, 'system', p_body, now());

    UPDATE conversations
    SET last_message_id = v_id, last_message_at = now()
    WHERE id = p_conversation;
END;
$$;

-- ============================================================
-- 3. INFO GRUP + ANGGOTA
-- ============================================================

CREATE OR REPLACE FUNCTION public.get_conversation(p_conversation uuid)
RETURNS TABLE (
    id              uuid,
    type            text,
    title           text,
    avatar_key      text,
    created_by      uuid,
    my_role         text,
    is_muted        boolean,
    member_count    integer,
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
    IF NOT EXISTS (
        SELECT 1 FROM conversation_members cm
        WHERE cm.conversation_id = p_conversation AND cm.user_id = v_user AND cm.left_at IS NULL
    ) THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;

    RETURN QUERY
    SELECT c.id,
           c.type,
           COALESCE(c.title, ''),
           COALESCE(ma.storage_key, ''),
           c.created_by,
           me.role,
           (me.muted_until IS NOT NULL AND me.muted_until > now()),
           (SELECT count(*)::integer FROM conversation_members m
            WHERE m.conversation_id = c.id AND m.left_at IS NULL),
           c.created_at
    FROM conversations c
    JOIN conversation_members me ON me.conversation_id = c.id AND me.user_id = v_user
    LEFT JOIN media_assets ma ON ma.id = c.avatar_media_id
    WHERE c.id = p_conversation;
END;
$$;

CREATE OR REPLACE FUNCTION public.list_conversation_members(p_conversation uuid)
RETURNS TABLE (
    user_id      uuid,
    username     text,
    display_name text,
    avatar_key   text,
    role         text,
    joined_at    timestamptz
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
        SELECT 1 FROM conversation_members cm
        WHERE cm.conversation_id = p_conversation AND cm.user_id = v_user AND cm.left_at IS NULL
    ) THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;

    RETURN QUERY
    SELECT cm.user_id,
           u.username::text,
           COALESCE(p.display_name, ''),
           COALESCE(ma.storage_key, ''),
           cm.role,
           cm.joined_at
    FROM conversation_members cm
    JOIN users u ON u.id = cm.user_id
    LEFT JOIN user_profiles p  ON p.user_id = cm.user_id
    LEFT JOIN media_assets  ma ON ma.id = p.avatar_media_id
    WHERE cm.conversation_id = p_conversation AND cm.left_at IS NULL
    ORDER BY
        CASE cm.role WHEN 'owner' THEN 0 WHEN 'admin' THEN 1 ELSE 2 END,
        cm.joined_at;
END;
$$;

-- ============================================================
-- 4. KELOLA ANGGOTA GRUP
-- ============================================================

-- Hanya owner/admin yang boleh menambah. Yang sudah diblokir tidak bisa
-- ditambahkan. Setiap penambahan meninggalkan jejak pesan sistem.
CREATE OR REPLACE FUNCTION public.add_group_members(
    p_conversation uuid,
    p_members      uuid[]
)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user  uuid := public.require_auth();
    v_added integer := 0;
    v_uid   uuid;
    v_name  text;
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM conversations c WHERE c.id = p_conversation AND c.type = 'group'
    ) THEN
        RAISE EXCEPTION 'bukan percakapan grup' USING ERRCODE = '22023';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM conversation_members cm
        WHERE cm.conversation_id = p_conversation AND cm.user_id = v_user
          AND cm.left_at IS NULL AND cm.role IN ('owner', 'admin')
    ) THEN
        RAISE EXCEPTION 'hanya admin grup yang boleh menambah anggota' USING ERRCODE = '42501';
    END IF;

    FOREACH v_uid IN ARRAY COALESCE(p_members, ARRAY[]::uuid[])
    LOOP
        CONTINUE WHEN NOT EXISTS (SELECT 1 FROM users u WHERE u.id = v_uid AND u.deleted_at IS NULL);
        CONTINUE WHEN EXISTS (
            SELECT 1 FROM blocks b
            WHERE (b.blocker_id = v_user AND b.blocked_id = v_uid)
               OR (b.blocker_id = v_uid AND b.blocked_id = v_user)
        );

        -- Anggota yang pernah keluar bisa masuk lagi; baris lamanya
        -- dihidupkan kembali alih-alih diduplikasi.
        INSERT INTO conversation_members (conversation_id, user_id, role, joined_at)
        VALUES (p_conversation, v_uid, 'member', now())
        ON CONFLICT (conversation_id, user_id)
        DO UPDATE SET left_at = NULL, joined_at = now()
        WHERE conversation_members.left_at IS NOT NULL;

        IF FOUND THEN
            v_added := v_added + 1;
            SELECT COALESCE(p.display_name, u.username::text) INTO v_name
            FROM users u LEFT JOIN user_profiles p ON p.user_id = u.id WHERE u.id = v_uid;
            PERFORM public.post_system_message(p_conversation, v_name || ' ditambahkan ke grup');
        END IF;
    END LOOP;

    RETURN v_added;
END;
$$;

CREATE OR REPLACE FUNCTION public.remove_group_member(
    p_conversation uuid,
    p_member       uuid
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_name text;
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM conversation_members cm
        WHERE cm.conversation_id = p_conversation AND cm.user_id = v_user
          AND cm.left_at IS NULL AND cm.role IN ('owner', 'admin')
    ) THEN
        RAISE EXCEPTION 'hanya admin grup yang boleh mengeluarkan anggota' USING ERRCODE = '42501';
    END IF;

    -- Owner tidak bisa dikeluarkan; ia harus menyerahkan grup dulu (belum ada)
    -- atau membubarkannya.
    IF EXISTS (
        SELECT 1 FROM conversation_members cm
        WHERE cm.conversation_id = p_conversation AND cm.user_id = p_member AND cm.role = 'owner'
    ) THEN
        RAISE EXCEPTION 'pemilik grup tidak bisa dikeluarkan' USING ERRCODE = '42501';
    END IF;

    UPDATE conversation_members cm
    SET left_at = now()
    WHERE cm.conversation_id = p_conversation AND cm.user_id = p_member AND cm.left_at IS NULL;

    IF FOUND THEN
        SELECT COALESCE(p.display_name, u.username::text) INTO v_name
        FROM users u LEFT JOIN user_profiles p ON p.user_id = u.id WHERE u.id = p_member;
        PERFORM public.post_system_message(p_conversation, v_name || ' dikeluarkan dari grup');
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION public.leave_conversation(p_conversation uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_name text;
    v_role text;
BEGIN
    SELECT cm.role INTO v_role FROM conversation_members cm
    WHERE cm.conversation_id = p_conversation AND cm.user_id = v_user AND cm.left_at IS NULL;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;

    UPDATE conversation_members cm SET left_at = now()
    WHERE cm.conversation_id = p_conversation AND cm.user_id = v_user AND cm.left_at IS NULL;

    SELECT COALESCE(p.display_name, u.username::text) INTO v_name
    FROM users u LEFT JOIN user_profiles p ON p.user_id = u.id WHERE u.id = v_user;
    PERFORM public.post_system_message(p_conversation, v_name || ' keluar dari grup');

    -- Kalau owner keluar, kepemilikan diwariskan ke admin/anggota terlama
    -- supaya grup tidak menjadi yatim tanpa pengelola.
    IF v_role = 'owner' THEN
        UPDATE conversation_members cm
        SET role = 'owner'
        WHERE cm.conversation_id = p_conversation AND cm.left_at IS NULL
          AND cm.user_id = (
              SELECT m.user_id FROM conversation_members m
              WHERE m.conversation_id = p_conversation AND m.left_at IS NULL
              ORDER BY CASE m.role WHEN 'admin' THEN 0 ELSE 1 END, m.joined_at
              LIMIT 1
          );
    END IF;
END;
$$;

CREATE OR REPLACE FUNCTION public.update_group(
    p_conversation uuid,
    p_title        text,
    p_avatar_media uuid
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
        SELECT 1 FROM conversation_members cm
        WHERE cm.conversation_id = p_conversation AND cm.user_id = v_user
          AND cm.left_at IS NULL AND cm.role IN ('owner', 'admin')
    ) THEN
        RAISE EXCEPTION 'hanya admin grup yang boleh mengubah grup' USING ERRCODE = '42501';
    END IF;

    UPDATE conversations c
    SET title           = COALESCE(NULLIF(btrim(p_title), ''), c.title),
        avatar_media_id = COALESCE(p_avatar_media, c.avatar_media_id)
    WHERE c.id = p_conversation AND c.type = 'group';

    IF p_title IS NOT NULL AND btrim(p_title) <> '' THEN
        PERFORM public.post_system_message(p_conversation, 'Nama grup diubah menjadi "' || btrim(p_title) || '"');
    END IF;
END;
$$;

-- Mengubah peran anggota (jadikan admin / turunkan). Owner saja.
CREATE OR REPLACE FUNCTION public.set_member_role(
    p_conversation uuid,
    p_member       uuid,
    p_role         text
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    IF p_role NOT IN ('admin', 'member') THEN
        RAISE EXCEPTION 'peran tidak valid' USING ERRCODE = '22023';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM conversation_members cm
        WHERE cm.conversation_id = p_conversation AND cm.user_id = v_user
          AND cm.left_at IS NULL AND cm.role = 'owner'
    ) THEN
        RAISE EXCEPTION 'hanya pemilik grup yang boleh mengubah peran' USING ERRCODE = '42501';
    END IF;

    UPDATE conversation_members cm SET role = p_role
    WHERE cm.conversation_id = p_conversation AND cm.user_id = p_member
      AND cm.left_at IS NULL AND cm.role <> 'owner';
END;
$$;

-- ============================================================
-- 5. BISUKAN PERCAKAPAN
-- ============================================================

CREATE OR REPLACE FUNCTION public.mute_conversation(
    p_conversation uuid,
    p_until        timestamptz
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    UPDATE conversation_members cm
    SET muted_until = p_until       -- NULL = bunyikan lagi
    WHERE cm.conversation_id = p_conversation AND cm.user_id = v_user AND cm.left_at IS NULL;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;
END;
$$;

-- ============================================================
-- 6. REAKSI
-- ============================================================

CREATE OR REPLACE FUNCTION public.react_to_message(
    p_message uuid,
    p_emoji   text
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_conv uuid;
BEGIN
    SELECT m.conversation_id INTO v_conv FROM messages m WHERE m.id = p_message AND m.deleted_at IS NULL;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'pesan tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM conversation_members cm
        WHERE cm.conversation_id = v_conv AND cm.user_id = v_user AND cm.left_at IS NULL
    ) THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;

    IF p_emoji IS NULL OR btrim(p_emoji) = '' THEN
        DELETE FROM message_reactions r WHERE r.message_id = p_message AND r.user_id = v_user;
    ELSE
        INSERT INTO message_reactions (message_id, user_id, emoji)
        VALUES (p_message, v_user, p_emoji)
        ON CONFLICT (message_id, user_id) DO UPDATE SET emoji = EXCLUDED.emoji, created_at = now();
    END IF;
END;
$$;

-- Reaksi dikembalikan bersama pesan lewat get_messages, tapi karena banyak
-- pesan, dibaca sekaligus per percakapan lalu dikelompokkan klien.
CREATE OR REPLACE FUNCTION public.list_reactions(p_message_ids uuid[])
RETURNS TABLE (message_id uuid, user_id uuid, emoji text)
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    RETURN QUERY
    SELECT r.message_id, r.user_id, r.emoji
    FROM message_reactions r
    JOIN messages m ON m.id = r.message_id
    JOIN conversation_members cm
      ON cm.conversation_id = m.conversation_id AND cm.user_id = v_user AND cm.left_at IS NULL
    WHERE r.message_id = ANY(COALESCE(p_message_ids, ARRAY[]::uuid[]));
END;
$$;

-- ============================================================
-- 7. LAMPIRAN MEDIA PADA PESAN
-- ============================================================
-- send_message diperluas menerima daftar media. Foto/voice note kini bisa
-- dikirim; media harus sudah dikonfirmasi (milik pengirim).

CREATE OR REPLACE FUNCTION public.send_message(
    p_id           uuid,
    p_conversation uuid,
    p_type         text,
    p_body         text,
    p_reply_to     uuid,
    p_created_at   timestamptz,
    p_media        uuid[] DEFAULT NULL
)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_pos  smallint := 0;
    v_mid  uuid;
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM conversation_members
        WHERE conversation_id = p_conversation AND user_id = v_user AND left_at IS NULL
    ) THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;

    INSERT INTO messages (id, conversation_id, sender_id, type, body, reply_to_message_id, created_at)
    VALUES (p_id, p_conversation, v_user, COALESCE(p_type, 'text'), p_body, p_reply_to, p_created_at);

    FOREACH v_mid IN ARRAY COALESCE(p_media, ARRAY[]::uuid[])
    LOOP
        -- Hanya media milik pengirim yang boleh dilampirkan; mencegah menautkan
        -- berkas orang lain ke pesan sendiri.
        CONTINUE WHEN NOT EXISTS (
            SELECT 1 FROM media_assets ma WHERE ma.id = v_mid AND ma.owner_id = v_user
        );
        INSERT INTO message_attachments (message_id, media_id, position)
        VALUES (p_id, v_mid, v_pos)
        ON CONFLICT DO NOTHING;
        v_pos := v_pos + 1;
    END LOOP;

    UPDATE conversations
    SET last_message_id = p_id, last_message_at = p_created_at
    WHERE id = p_conversation;

    UPDATE conversation_members
    SET unread_count = unread_count + 1
    WHERE conversation_id = p_conversation AND user_id <> v_user AND left_at IS NULL;
END;
$$;

-- get_messages ikut mengembalikan lampiran (sebagai storage key CSV agar tetap
-- satu baris per pesan; klien memecahnya). Kolom baru mengubah tipe kembalian,
-- jadi fungsi lama harus di-DROP dulu — CREATE OR REPLACE tidak bisa mengubah
-- bentuk baris keluaran (Postgres 42P13).
DROP FUNCTION IF EXISTS public.get_messages(uuid, uuid, integer);
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
           (m.deleted_at IS NOT NULL),
           (SELECT string_agg(ma.storage_key, ',' ORDER BY a.position)
            FROM message_attachments a JOIN media_assets ma ON ma.id = a.media_id
            WHERE a.message_id = m.id)
    FROM messages m
    WHERE m.conversation_id = p_conversation
      AND (p_before IS NULL OR m.id < p_before)
      AND (v_cleared IS NULL OR m.id > v_cleared)
    ORDER BY m.id DESC
    LIMIT LEAST(GREATEST(p_limit, 1), 100);
END;
$$;

-- ============================================================
-- 8. HAK EKSEKUSI + RLS
-- ============================================================

DO $$
DECLARE
    fn text;
BEGIN
    FOREACH fn IN ARRAY ARRAY[
        'public.get_conversation(uuid)',
        'public.list_conversation_members(uuid)',
        'public.add_group_members(uuid, uuid[])',
        'public.remove_group_member(uuid, uuid)',
        'public.leave_conversation(uuid)',
        'public.update_group(uuid, text, uuid)',
        'public.set_member_role(uuid, uuid, text)',
        'public.mute_conversation(uuid, timestamptz)',
        'public.react_to_message(uuid, text)',
        'public.list_reactions(uuid[])',
        'public.send_message(uuid, uuid, text, text, uuid, timestamptz, uuid[])',
        'public.get_messages(uuid, uuid, integer)'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO authenticated', fn);
    END LOOP;
END;
$$;

-- post_system_message hanya dipanggil fungsi lain, tidak langsung oleh klien.
REVOKE EXECUTE ON FUNCTION public.post_system_message(uuid, text) FROM PUBLIC;

CREATE POLICY reactions_visible ON message_reactions
    FOR SELECT TO authenticated
    USING (EXISTS (
        SELECT 1 FROM messages m
        JOIN conversation_members cm ON cm.conversation_id = m.conversation_id
        WHERE m.id = message_reactions.message_id
          AND cm.user_id = auth.uid() AND cm.left_at IS NULL
    ));

COMMIT;
