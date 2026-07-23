-- Telepon suara & video antar-anggota percakapan.
--
-- Sama seperti voice room, backend TIDAK mengalirkan audio/video — itu tugas
-- SFU (LiveKit). Backend hanya mencatat sesi, mengotorisasi peserta, dan
-- menerbitkan token. Bedanya dari room: panggilan terikat pada sebuah
-- percakapan (chat) dan punya siklus dering → jawab/tolak → selesai.
--
-- Tabel calls di docs/erd.md akhirnya diimplementasikan di sini.

BEGIN;

CREATE TABLE IF NOT EXISTS calls (
    id              uuid PRIMARY KEY,
    conversation_id uuid NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    initiator_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind            text NOT NULL CHECK (kind IN ('audio', 'video')),
    status          text NOT NULL DEFAULT 'ringing'
        CHECK (status IN ('ringing', 'ongoing', 'ended', 'missed', 'declined')),

    -- Jembatan ke SFU, sama seperti rooms.sfu_room_id.
    sfu_room_id     text,

    started_at      timestamptz NOT NULL DEFAULT now(),
    answered_at     timestamptz,
    ended_at        timestamptz
);

CREATE INDEX IF NOT EXISTS calls_conversation_idx ON calls (conversation_id, started_at DESC);
CREATE INDEX IF NOT EXISTS calls_active_idx ON calls (conversation_id)
    WHERE status IN ('ringing', 'ongoing');

CREATE TABLE IF NOT EXISTS call_participants (
    call_id   uuid NOT NULL REFERENCES calls(id) ON DELETE CASCADE,
    user_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    joined_at timestamptz,
    left_at   timestamptz,

    PRIMARY KEY (call_id, user_id)
);

ALTER TABLE calls             ENABLE ROW LEVEL SECURITY;
ALTER TABLE call_participants ENABLE ROW LEVEL SECURITY;

-- ============================================================
-- MEMULAI PANGGILAN
-- ============================================================
-- Hanya anggota percakapan yang boleh memulai. Untuk chat pribadi, panggilan
-- ke orang yang memblokir (atau diblokir) ditolak. Kalau sudah ada panggilan
-- aktif di percakapan itu, id-nya dikembalikan alih-alih membuat yang baru —
-- mencegah dua panggilan paralel di satu chat.
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

    SELECT c.id, c.sfu_room_id INTO v_existing, v_sfu
    FROM calls c
    WHERE c.conversation_id = p_conversation AND c.status IN ('ringing', 'ongoing')
    LIMIT 1;

    IF v_existing IS NOT NULL THEN
        -- Bergabung ke panggilan yang sudah berlangsung.
        INSERT INTO call_participants (call_id, user_id, joined_at)
        VALUES (v_existing, v_user, now())
        ON CONFLICT (call_id, user_id) DO UPDATE SET joined_at = now(), left_at = NULL;

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

-- ============================================================
-- MENJAWAB / MENOLAK / MENGAKHIRI
-- ============================================================

CREATE OR REPLACE FUNCTION public.answer_call(p_call uuid)
RETURNS text
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
    v_sfu  text;
BEGIN
    SELECT c.sfu_room_id INTO v_sfu
    FROM calls c
    JOIN conversation_members cm
      ON cm.conversation_id = c.conversation_id AND cm.user_id = v_user AND cm.left_at IS NULL
    WHERE c.id = p_call AND c.status IN ('ringing', 'ongoing');

    IF NOT FOUND THEN
        RAISE EXCEPTION 'panggilan tidak ditemukan' USING ERRCODE = 'P0002';
    END IF;

    UPDATE calls SET status = 'ongoing', answered_at = COALESCE(answered_at, now())
    WHERE id = p_call AND status = 'ringing';

    INSERT INTO call_participants (call_id, user_id, joined_at)
    VALUES (p_call, v_user, now())
    ON CONFLICT (call_id, user_id) DO UPDATE SET joined_at = now(), left_at = NULL;

    RETURN v_sfu;
END;
$$;

CREATE OR REPLACE FUNCTION public.decline_call(p_call uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user uuid := public.require_auth();
BEGIN
    -- Menolak hanya menutup panggilan kalau ini chat pribadi (dua orang).
    -- Di grup, satu orang menolak tidak boleh mengakhiri panggilan yang lain.
    UPDATE calls c
    SET status = 'declined', ended_at = now()
    WHERE c.id = p_call
      AND c.status = 'ringing'
      AND EXISTS (
          SELECT 1 FROM conversations conv WHERE conv.id = c.conversation_id AND conv.type = 'direct'
      )
      AND EXISTS (
          SELECT 1 FROM conversation_members cm
          WHERE cm.conversation_id = c.conversation_id AND cm.user_id = v_user AND cm.left_at IS NULL
      );
END;
$$;

-- Meninggalkan panggilan. Kalau tak ada peserta tersisa, panggilan berakhir.
CREATE OR REPLACE FUNCTION public.leave_call(p_call uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = public
AS $$
DECLARE
    v_user      uuid := public.require_auth();
    v_remaining integer;
BEGIN
    UPDATE call_participants SET left_at = now()
    WHERE call_id = p_call AND user_id = v_user AND left_at IS NULL;

    SELECT count(*) INTO v_remaining
    FROM call_participants WHERE call_id = p_call AND left_at IS NULL;

    IF v_remaining = 0 THEN
        UPDATE calls
        SET status   = CASE WHEN status = 'ringing' THEN 'missed' ELSE 'ended' END,
            ended_at = now()
        WHERE id = p_call AND status IN ('ringing', 'ongoing');
    END IF;
END;
$$;

-- Panggilan aktif pada sebuah percakapan (untuk gabung / tampilkan "sedang
-- menelepon").
CREATE OR REPLACE FUNCTION public.get_active_call(p_conversation uuid)
RETURNS TABLE (
    id           uuid,
    kind         text,
    status       text,
    initiator_id uuid,
    sfu_room_id  text,
    started_at   timestamptz
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
    SELECT c.id, c.kind, c.status, c.initiator_id, c.sfu_room_id, c.started_at
    FROM calls c
    WHERE c.conversation_id = p_conversation AND c.status IN ('ringing', 'ongoing')
    ORDER BY c.started_at DESC
    LIMIT 1;
END;
$$;

DO $$
DECLARE
    fn text;
BEGIN
    FOREACH fn IN ARRAY ARRAY[
        'public.start_call(uuid, uuid, text, text)',
        'public.answer_call(uuid)',
        'public.decline_call(uuid)',
        'public.leave_call(uuid)',
        'public.get_active_call(uuid)'
    ]
    LOOP
        EXECUTE format('REVOKE EXECUTE ON FUNCTION %s FROM PUBLIC', fn);
        EXECUTE format('GRANT  EXECUTE ON FUNCTION %s TO authenticated', fn);
    END LOOP;
END;
$$;

COMMIT;
