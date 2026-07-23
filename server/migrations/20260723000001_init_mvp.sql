-- Syntra — skema MVP
-- Turunan dari docs/erd.md, terbatas pada entitas berlabel MVP di bagian 9.
-- Target: PostgreSQL 15+ (Supabase).
--
-- Cara menerapkan:
--   * Dashboard Supabase > SQL Editor, atau
--   * `supabase db push` (penamaan berkas sudah mengikuti Supabase CLI)
--
-- RLS diaktifkan di akhir berkas TANPA satu pun policy — artinya semua
-- tertutup rapat. Yang membukanya secukupnya adalah migrasi berikutnya,
-- 20260723000002_rls_and_rpc.sql. Jalankan keduanya berurutan; kalau hanya
-- berkas ini yang dijalankan, setiap query akan mengembalikan nol baris.

BEGIN;

CREATE EXTENSION IF NOT EXISTS citext;

-- ============================================================
-- 1. IDENTITY
-- ============================================================

CREATE TABLE IF NOT EXISTS users (
    id             uuid PRIMARY KEY,
    username       citext NOT NULL UNIQUE,
    email          citext UNIQUE,
    phone_e164     text   UNIQUE,
    password_hash  text,

    -- Wajib, bukan opsional: tanpa tanggal lahir tidak ada age gating, dan
    -- age gating adalah syarat rilis untuk aplikasi dengan konten pengguna.
    date_of_birth  date NOT NULL,

    account_status text NOT NULL DEFAULT 'active'
        CHECK (account_status IN ('active', 'suspended', 'deactivated', 'deleted')),
    is_private     boolean NOT NULL DEFAULT false,

    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    deleted_at     timestamptz,

    CONSTRAINT users_identity_present CHECK (email IS NOT NULL OR phone_e164 IS NOT NULL)
);

CREATE TABLE IF NOT EXISTS user_profiles (
    user_id         uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    display_name    text NOT NULL DEFAULT '',
    bio             text NOT NULL DEFAULT '',
    avatar_media_id uuid,
    cover_media_id  uuid,
    follower_count  integer NOT NULL DEFAULT 0,
    following_count integer NOT NULL DEFAULT 0,
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS user_settings (
    user_id               uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    dm_privacy            text NOT NULL DEFAULT 'everyone'
        CHECK (dm_privacy IN ('everyone', 'following', 'nobody')),
    story_privacy         text NOT NULL DEFAULT 'followers'
        CHECK (story_privacy IN ('public', 'followers', 'close_friends')),
    discoverable_by_phone boolean NOT NULL DEFAULT false,
    locale                text NOT NULL DEFAULT 'id',
    timezone              text NOT NULL DEFAULT 'Asia/Jakarta',
    notification_prefs    jsonb NOT NULL DEFAULT '{}'::jsonb
);

CREATE TABLE IF NOT EXISTS devices (
    id           uuid PRIMARY KEY,
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    platform     text NOT NULL CHECK (platform IN ('android', 'ios', 'web')),
    push_token   text,
    app_version  text,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    revoked_at   timestamptz
);

CREATE INDEX IF NOT EXISTS devices_user_idx ON devices (user_id) WHERE revoked_at IS NULL;

-- ============================================================
-- 2. SOCIAL GRAPH
-- ============================================================

CREATE TABLE IF NOT EXISTS follows (
    follower_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    followee_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    status      text NOT NULL DEFAULT 'accepted' CHECK (status IN ('pending', 'accepted')),
    created_at  timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (follower_id, followee_id),
    CONSTRAINT follows_no_self CHECK (follower_id <> followee_id)
);

-- Untuk menjawab "siapa saja pengikut X", arah sebaliknya dari primary key.
CREATE INDEX IF NOT EXISTS follows_followee_idx ON follows (followee_id, status);

CREATE TABLE IF NOT EXISTS blocks (
    blocker_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    blocked_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (blocker_id, blocked_id),
    CONSTRAINT blocks_no_self CHECK (blocker_id <> blocked_id)
);

-- ============================================================
-- 3. MEDIA
-- ============================================================

CREATE TABLE IF NOT EXISTS media_assets (
    id                uuid PRIMARY KEY,
    owner_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind              text NOT NULL CHECK (kind IN ('video', 'image', 'audio', 'voice_note')),

    -- Path di object storage (Supabase Storage / S3). Byte media tidak pernah
    -- masuk ke tabel ini.
    storage_key       text NOT NULL,
    mime_type         text NOT NULL,
    size_bytes        bigint NOT NULL,
    duration_ms       integer,
    width             integer,
    height            integer,
    blurhash          text,
    processing_status text NOT NULL DEFAULT 'pending'
        CHECK (processing_status IN ('pending', 'processing', 'ready', 'failed')),
    checksum_sha256   text,
    created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS media_assets_owner_idx ON media_assets (owner_id, created_at DESC);
CREATE INDEX IF NOT EXISTS media_assets_checksum_idx ON media_assets (checksum_sha256)
    WHERE checksum_sha256 IS NOT NULL;

ALTER TABLE user_profiles
    ADD CONSTRAINT user_profiles_avatar_fk FOREIGN KEY (avatar_media_id) REFERENCES media_assets(id) ON DELETE SET NULL,
    ADD CONSTRAINT user_profiles_cover_fk  FOREIGN KEY (cover_media_id)  REFERENCES media_assets(id) ON DELETE SET NULL;

-- ============================================================
-- 4. CHAT
-- ============================================================

CREATE TABLE IF NOT EXISTS conversations (
    id              uuid PRIMARY KEY,
    type            text NOT NULL CHECK (type IN ('direct', 'group')),
    title           text,
    avatar_media_id uuid REFERENCES media_assets(id) ON DELETE SET NULL,
    created_by      uuid REFERENCES users(id) ON DELETE SET NULL,

    -- Denormalisasi untuk daftar chat. Tanpa ini, membuka daftar chat berarti
    -- satu subquery agregat per percakapan.
    last_message_id uuid,
    last_message_at timestamptz,

    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS conversation_members (
    conversation_id      uuid NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    user_id              uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role                 text NOT NULL DEFAULT 'member' CHECK (role IN ('owner', 'admin', 'member')),
    last_read_message_id uuid,
    unread_count         integer NOT NULL DEFAULT 0,
    muted_until          timestamptz,
    joined_at            timestamptz NOT NULL DEFAULT now(),
    left_at              timestamptz,

    PRIMARY KEY (conversation_id, user_id)
);

-- Index utama untuk daftar chat: milik user, terbaru dulu.
CREATE INDEX IF NOT EXISTS conversation_members_user_idx
    ON conversation_members (user_id) WHERE left_at IS NULL;

CREATE INDEX IF NOT EXISTS conversations_recent_idx
    ON conversations (last_message_at DESC NULLS LAST);

CREATE TABLE IF NOT EXISTS messages (
    id                  uuid PRIMARY KEY,
    conversation_id     uuid NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    sender_id           uuid REFERENCES users(id) ON DELETE SET NULL,
    type                text NOT NULL DEFAULT 'text'
        CHECK (type IN ('text', 'media', 'voice_note', 'story_reply', 'call_event', 'system')),
    body                text,
    reply_to_message_id uuid REFERENCES messages(id) ON DELETE SET NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    edited_at           timestamptz,
    deleted_at          timestamptz
);

-- Index untuk pagination riwayat percakapan. id adalah UUIDv7 sehingga urut
-- secara waktu; kolom ini cukup untuk cursor tanpa index tambahan.
CREATE INDEX IF NOT EXISTS messages_conversation_idx
    ON messages (conversation_id, id DESC);

ALTER TABLE conversations
    ADD CONSTRAINT conversations_last_message_fk
    FOREIGN KEY (last_message_id) REFERENCES messages(id) ON DELETE SET NULL;

CREATE TABLE IF NOT EXISTS message_attachments (
    message_id uuid NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    media_id   uuid NOT NULL REFERENCES media_assets(id) ON DELETE CASCADE,
    position   smallint NOT NULL DEFAULT 0,

    PRIMARY KEY (message_id, media_id)
);

-- ============================================================
-- 5. TRUST & SAFETY + PRIVASI
-- ============================================================

CREATE TABLE IF NOT EXISTS reports (
    id          uuid PRIMARY KEY,
    reporter_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    target_type text NOT NULL CHECK (target_type IN ('user', 'reel', 'story', 'message', 'room', 'comment')),
    target_id   uuid NOT NULL,
    reason      text NOT NULL CHECK (reason IN ('spam', 'harassment', 'nudity', 'violence', 'csam', 'copyright', 'other')),
    detail      text,
    status      text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'triaged', 'actioned', 'dismissed')),
    priority    text NOT NULL DEFAULT 'normal' CHECK (priority IN ('low', 'normal', 'high', 'critical')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz
);

-- Antrean moderasi: yang belum selesai, paling genting dulu.
CREATE INDEX IF NOT EXISTS reports_queue_idx
    ON reports (status, priority, created_at) WHERE status IN ('open', 'triaged');

CREATE TABLE IF NOT EXISTS moderation_actions (
    id           uuid PRIMARY KEY,
    moderator_id uuid REFERENCES users(id) ON DELETE SET NULL, -- NULL = otomatis
    report_id    uuid REFERENCES reports(id) ON DELETE SET NULL,
    target_type  text NOT NULL,
    target_id    uuid NOT NULL,
    action       text NOT NULL CHECK (action IN ('warn', 'remove_content', 'shadow_limit', 'suspend', 'ban')),
    reason       text,
    expires_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS consents (
    id             uuid PRIMARY KEY,
    user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    purpose        text NOT NULL CHECK (purpose IN ('tos', 'privacy_policy', 'marketing', 'ai_training', 'call_recording')),

    -- Tanpa versi kebijakan, persetujuan tidak bisa dibuktikan saat audit:
    -- kita harus bisa menunjukkan teks mana yang disetujui pengguna.
    policy_version text NOT NULL,
    granted_at     timestamptz NOT NULL DEFAULT now(),
    revoked_at     timestamptz
);

CREATE INDEX IF NOT EXISTS consents_user_idx ON consents (user_id, purpose);

-- ============================================================
-- 6. NOTIFIKASI
-- ============================================================

CREATE TABLE IF NOT EXISTS notifications (
    id           uuid PRIMARY KEY,
    recipient_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    actor_id     uuid REFERENCES users(id) ON DELETE SET NULL,
    type         text NOT NULL CHECK (type IN ('follow', 'like', 'comment', 'mention', 'story_reply', 'room_live', 'system')),
    subject_type text,
    subject_id   uuid,
    read_at      timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS notifications_inbox_idx
    ON notifications (recipient_id, created_at DESC);

CREATE INDEX IF NOT EXISTS notifications_unread_idx
    ON notifications (recipient_id) WHERE read_at IS NULL;

-- ============================================================
-- 7. ROW LEVEL SECURITY
-- ============================================================
-- Diaktifkan tanpa policy apa pun, artinya: default tertutup rapat.
-- Role service milik backend melewati RLS, jadi aplikasi tetap berjalan.
-- Kalau nanti klien Kotlin diberi akses langsung ke Supabase, policy harus
-- ditulis dulu — dan tidak ada satu baris pun yang bocor sebelum itu.

ALTER TABLE users                ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_profiles        ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_settings        ENABLE ROW LEVEL SECURITY;
ALTER TABLE devices              ENABLE ROW LEVEL SECURITY;
ALTER TABLE follows              ENABLE ROW LEVEL SECURITY;
ALTER TABLE blocks               ENABLE ROW LEVEL SECURITY;
ALTER TABLE media_assets         ENABLE ROW LEVEL SECURITY;
ALTER TABLE conversations        ENABLE ROW LEVEL SECURITY;
ALTER TABLE conversation_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE messages             ENABLE ROW LEVEL SECURITY;
ALTER TABLE message_attachments  ENABLE ROW LEVEL SECURITY;
ALTER TABLE reports              ENABLE ROW LEVEL SECURITY;
ALTER TABLE moderation_actions   ENABLE ROW LEVEL SECURITY;
ALTER TABLE consents             ENABLE ROW LEVEL SECURITY;
ALTER TABLE notifications        ENABLE ROW LEVEL SECURITY;

COMMIT;
