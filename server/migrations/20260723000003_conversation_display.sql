-- Melengkapi list_conversations agar daftar chat benar-benar bisa dirender.
--
-- Versi sebelumnya mengembalikan `title`, yang untuk percakapan bertipe
-- 'direct' selalu NULL — judul percakapan pribadi memang tidak disimpan.
-- Akibatnya klien tidak punya nama untuk ditampilkan sama sekali.
--
-- Selain itu klien menampilkan cuplikan pesan terakhir di tiap baris, dan itu
-- juga belum pernah dikirim.
--
-- Yang ditambahkan:
--   title                 nama tampil — judul grup, atau nama lawan bicara
--   avatar_media_id       avatar grup, atau avatar lawan bicara
--   counterpart_id        id lawan bicara (NULL untuk grup) — untuk buka profil
--   last_message_preview  cuplikan, dipotong 120 karakter
--   last_message_type     supaya klien bisa menulis "📷 Foto" alih-alih teks kosong
--   last_message_sender   untuk menandai "Kamu: ..." pada percakapan grup

BEGIN;

-- CREATE OR REPLACE tidak bisa mengubah tipe kembalian sebuah fungsi,
-- jadi versi lama harus dibuang lebih dulu.
DROP FUNCTION IF EXISTS public.list_conversations(timestamptz, integer);

CREATE FUNCTION public.list_conversations(
    p_before timestamptz,
    p_limit  integer
)
RETURNS TABLE (
    id                   uuid,
    type                 text,
    title                text,
    avatar_media_id      uuid,
    counterpart_id       uuid,
    unread_count         integer,
    last_message_preview text,
    last_message_type    text,
    last_message_sender  uuid,
    last_message_at      timestamptz,
    created_at           timestamptz
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

        -- Grup memakai judulnya sendiri; percakapan pribadi memakai nama
        -- lawan bicara, dengan username sebagai cadangan kalau profil belum diisi.
        CASE WHEN c.type = 'group'
             THEN COALESCE(c.title, '')
             ELSE COALESCE(NULLIF(cp.display_name, ''), cu.username, '')
        END AS title,

        CASE WHEN c.type = 'group'
             THEN c.avatar_media_id
             ELSE cp.avatar_media_id
        END AS avatar_media_id,

        cu.id AS counterpart_id,
        cm.unread_count,

        -- Pesan yang sudah dihapus tetap menempati baris terakhir, tetapi
        -- isinya tidak boleh ikut terbaca di daftar.
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

    -- LATERAL dipakai supaya subquery bisa mengacu ke c.id. Untuk grup,
    -- kondisi join-nya tidak pernah terpenuhi sehingga kolomnya NULL —
    -- persis yang diinginkan.
    LEFT JOIN LATERAL (
        SELECT u.id, u.username
        FROM conversation_members m2
        JOIN users u ON u.id = m2.user_id
        WHERE m2.conversation_id = c.id
          AND m2.user_id <> v_user
          AND m2.left_at IS NULL
        ORDER BY m2.joined_at
        LIMIT 1
    ) cu ON c.type = 'direct'

    LEFT JOIN user_profiles cp ON cp.user_id = cu.id
    LEFT JOIN messages      lm ON lm.id = c.last_message_id

    WHERE COALESCE(c.last_message_at, c.created_at) < p_before
    ORDER BY COALESCE(c.last_message_at, c.created_at) DESC
    LIMIT LEAST(GREATEST(p_limit, 1), 100);
END;
$$;

-- Hak eksekusi ikut hilang bersama fungsi lama, jadi harus diberikan lagi.
REVOKE EXECUTE ON FUNCTION public.list_conversations(timestamptz, integer) FROM PUBLIC;
GRANT  EXECUTE ON FUNCTION public.list_conversations(timestamptz, integer) TO authenticated;

COMMIT;
