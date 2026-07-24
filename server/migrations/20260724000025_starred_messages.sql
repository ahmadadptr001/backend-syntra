-- Starred messages — menandai pesan penting untuk dibuka lagi nanti.
--
-- Menu titik-tiga aplikasi sudah punya slot "Starred messages" yang selama ini
-- tak berujung ke mana-mana. Tabel di bawah menyimpannya per pengguna: bintang
-- adalah penanda pribadi, tidak terlihat peserta lain (berbeda dari reaksi).
--
-- Menyimpan hanya (user, message); isi pesan diambil lewat join saat ditampilkan
-- supaya edit/hapus pesan otomatis tercermin di daftar bintang.

BEGIN;

CREATE TABLE IF NOT EXISTS message_stars (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    message_id uuid NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, message_id)
);

CREATE INDEX IF NOT EXISTS message_stars_user_idx
    ON message_stars (user_id, created_at DESC);

ALTER TABLE message_stars ENABLE ROW LEVEL SECURITY;

-- ============================================================
-- TANDAI / LEPAS
-- ============================================================
-- Hanya boleh menandai pesan yang benar-benar bisa dilihat pemanggil: ia harus
-- anggota percakapannya, dan pesannya belum dihapus.
CREATE OR REPLACE FUNCTION public.star_message(p_message uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_conv uuid;
BEGIN
    SELECT m.conversation_id INTO v_conv
    FROM messages m
    WHERE m.id = p_message AND m.deleted_at IS NULL;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'pesan tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM conversation_members cm
        WHERE cm.conversation_id = v_conv AND cm.user_id = v_user AND cm.left_at IS NULL
    ) THEN
        RAISE EXCEPTION 'bukan anggota percakapan' USING ERRCODE = '42501';
    END IF;

    INSERT INTO message_stars (user_id, message_id)
    VALUES (v_user, p_message)
    ON CONFLICT (user_id, message_id) DO NOTHING;
END;
$$;

CREATE OR REPLACE FUNCTION public.unstar_message(p_message uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    DELETE FROM message_stars
    WHERE user_id = v_user AND message_id = p_message;
END;
$$;

-- ============================================================
-- DAFTAR BERBINTANG
-- ============================================================
-- Lintas-percakapan, terbaru dulu, cursor berdasarkan waktu ditandai. Hanya
-- pesan di percakapan yang masih diikuti dan belum dihapus yang ditampilkan —
-- bintang pada pesan yang kemudian hilang tidak bocor.
CREATE OR REPLACE FUNCTION public.list_starred_messages(
    p_before timestamptz,
    p_limit  integer
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
    attachments         text,
    starred_at          timestamptz
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
    SELECT m.id, m.conversation_id, m.sender_id, m.type,
           COALESCE(m.body, ''),
           m.reply_to_message_id, m.created_at, m.edited_at,
           false,
           (SELECT string_agg(ma.storage_key, ',' ORDER BY a.position)
            FROM message_attachments a JOIN media_assets ma ON ma.id = a.media_id
            WHERE a.message_id = m.id),
           s.created_at
    FROM message_stars s
    JOIN messages m ON m.id = s.message_id AND m.deleted_at IS NULL
    JOIN conversation_members cm
      ON cm.conversation_id = m.conversation_id
     AND cm.user_id = v_user
     AND cm.left_at IS NULL
    WHERE s.user_id = v_user
      AND s.created_at < p_before
    ORDER BY s.created_at DESC
    LIMIT LEAST(GREATEST(p_limit, 1), 100);
END;
$$;

DO $$
DECLARE
    fn text;
BEGIN
    FOREACH fn IN ARRAY ARRAY[
        'public.star_message(uuid)',
        'public.unstar_message(uuid)',
        'public.list_starred_messages(timestamptz, integer)'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO authenticated', fn);
    END LOOP;
END;
$$;

COMMIT;
