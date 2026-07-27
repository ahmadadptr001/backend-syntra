-- list_conversations harus menghormati "bersihkan obrolan".
--
-- MASALAH. clear_conversation menyetel cleared_before_id milik pemanggil, dan
-- get_messages sudah menghormatinya — jadi isi obrolan benar-benar kosong saat
-- dibuka. Tetapi daftar di beranda mengambil pratinjau lewat
--     LEFT JOIN messages lm ON lm.id = c.last_message_id
-- tanpa melihat cleared_before_id sama sekali.
--
-- Akibatnya baris di beranda tetap menampilkan pesan terakhir yang sudah dihapus
-- ("cek cek", "tes tes", dan seterusnya) padahal obrolannya sudah kosong. Dari
-- sudut pandang pengguna, pembersihan gagal separuh: hilang di dalam, masih ada
-- di luar — dan justru yang di luar itulah yang dilihat orang lain saat melirik
-- layar.
--
-- PERBAIKAN. Ikat join pratinjau ke batas bersih milik PEMANGGIL. Bila pesan
-- terakhir berada pada atau sebelum batas itu, lm menjadi NULL dan seluruh kolom
-- turunannya (preview, type, sender) otomatis kosong lewat COALESCE yang sudah
-- ada — tidak perlu mengubah bentuk baris keluaran.
--
-- Perbandingan memakai id, bukan waktu: id pesan adalah UUIDv7 sehingga urutan
-- byte-nya adalah urutan waktu, dan itu tepat sama dengan cara get_messages
-- menyaring. Dua tempat yang memutuskan "sudah dibersihkan atau belum" wajib
-- memakai ukuran yang sama, kalau tidak keduanya akan berbeda pendapat lagi.
--
-- SENGAJA TIDAK DIUBAH: barisnya tetap ada, lengkap dengan nama dan foto lawan
-- bicara, dan tetap pada urutan waktu yang sama. "Bersihkan obrolan" menghapus
-- ISI, bukan percakapannya — yang menghapus baris adalah "hapus percakapan"
-- (delete_conversation, yang menyetel left_at).
--
-- Bentuk kembalian tidak berubah => CREATE OR REPLACE. ASCII murni.

BEGIN;

CREATE OR REPLACE FUNCTION public.list_conversations(
    p_before timestamptz,
    p_limit  integer
)
RETURNS TABLE (
    id                        uuid,
    type                      text,
    title                     text,
    avatar_media_id           uuid,
    counterpart_id            uuid,
    counterpart_username      text,
    counterpart_last_read     uuid,
    counterpart_last_delivered uuid,
    unread_count              integer,
    last_message_preview      text,
    last_message_type         text,
    last_message_sender       uuid,
    last_message_id           uuid,
    last_message_at           timestamptz,
    created_at                timestamptz
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

        CASE WHEN c.type = 'group'
             THEN COALESCE(c.title, '')
             ELSE COALESCE(NULLIF(cp.display_name, ''), cu.username, '')
        END AS title,

        CASE WHEN c.type = 'group'
             THEN c.avatar_media_id
             ELSE cp.avatar_media_id
        END AS avatar_media_id,

        cu.id                            AS counterpart_id,
        cu.username::text                AS counterpart_username,
        cm_other.last_read_message_id    AS counterpart_last_read,
        cm_other.last_delivered_message_id AS counterpart_last_delivered,
        cm.unread_count,

        CASE WHEN lm.deleted_at IS NOT NULL THEN ''
             ELSE COALESCE(left(lm.body, 120), '')
        END AS last_message_preview,

        -- Untuk pesan media, laporkan jenis lampiran supaya beranda bisa menulis
        -- [Foto]/[GIF]/[Video]/[Pesan suara]; selain itu pakai tipe pesan apa adanya.
        CASE
            WHEN lm.deleted_at IS NOT NULL THEN 'text'
            WHEN lm.type IN ('media', 'voice_note') THEN COALESCE(lmk.kind, lm.type)
            ELSE COALESCE(lm.type, '')
        END AS last_message_type,

        lm.sender_id                              AS last_message_sender,
        c.last_message_id                         AS last_message_id,
        COALESCE(c.last_message_at, c.created_at) AS last_message_at,
        c.created_at

    FROM conversations c
    JOIN conversation_members cm
      ON cm.conversation_id = c.id
     AND cm.user_id = v_user
     AND cm.left_at IS NULL

    LEFT JOIN LATERAL (
        SELECT m2.user_id, u.username,
               m2.last_read_message_id,
               m2.last_delivered_message_id
        FROM conversation_members m2
        JOIN users u ON u.id = m2.user_id
        WHERE m2.conversation_id = c.id
          AND m2.user_id <> v_user
          AND m2.left_at IS NULL
        ORDER BY m2.joined_at
        LIMIT 1
    ) cm_other ON c.type = 'direct'

    LEFT JOIN users         cu ON cu.id = cm_other.user_id
    LEFT JOIN user_profiles cp ON cp.user_id = cm_other.user_id

    -- Pratinjau HANYA bila pesan terakhir belum ikut dibersihkan pemanggil.
    -- Inilah satu-satunya perubahan terhadap versi migrasi 48.
    LEFT JOIN messages      lm
      ON lm.id = c.last_message_id
     AND (cm.cleared_before_id IS NULL OR lm.id > cm.cleared_before_id)

    -- Kind lampiran pertama dari pesan terakhir; GIF dipisah dari image biasa.
    LEFT JOIN LATERAL (
        SELECT CASE
                 WHEN ma2.mime_type = 'image/gif' OR ma2.storage_key ILIKE '%.gif' THEN 'gif'
                 ELSE ma2.kind
               END AS kind
        FROM message_attachments matt
        JOIN media_assets ma2 ON ma2.id = matt.media_id
        WHERE matt.message_id = lm.id
        ORDER BY matt.position
        LIMIT 1
    ) lmk ON true

    WHERE COALESCE(c.last_message_at, c.created_at) < p_before
    ORDER BY COALESCE(c.last_message_at, c.created_at) DESC
    LIMIT LEAST(GREATEST(p_limit, 1), 100);
END;
$$;

COMMIT;
