# Syntra — Entity Relationship Diagram

Status: draft v0.1 · Target DB: PostgreSQL 16 · Backend: Go

Dokumen ini menurunkan sketsa "flow kasar" di PRD (story · reels · chat · in-room)
menjadi model data, sekaligus menutup bab yang belum ada di PRD: AI, trust & safety,
privasi/kepatuhan, dan notifikasi.

ERD dipecah per domain. Menggabungkan ~45 entitas dalam satu diagram membuatnya
tidak terbaca, dan batas antar-domain di bawah ini sengaja dibuat sejajar dengan
batas service kalau nanti dipecah.

---

## 0. Konvensi

- PK selalu `uuid` (v7, time-ordered) supaya aman untuk sharding dan tidak membocorkan
  jumlah baris. Pengecualian: tabel event bervolume sangat tinggi memakai `bigint`
  identity karena lebih murah untuk index.
- Semua timestamp `timestamptz`, disimpan UTC.
- Soft delete lewat `deleted_at`, bukan hard delete — dibutuhkan untuk moderasi dan
  jejak audit.
- Counter (`like_count`, dst.) adalah kolom denormalisasi yang di-maintain lewat
  outbox/worker, bukan `COUNT(*)` saat baca.
- `enum` di bawah ditulis sebagai tipe untuk keterbacaan; implementasinya pakai
  lookup table atau `text` + `CHECK`, jangan `ENUM` native Postgres (migrasinya menyakitkan).

---

## 1. Identity & Social Graph

Fondasi. Semua domain lain bergantung ke sini.

```mermaid
erDiagram
    USERS ||--o| USER_PROFILES : has
    USERS ||--o| USER_SETTINGS : has
    USERS ||--o{ USER_AUTH_IDENTITIES : "logs in via"
    USERS ||--o{ DEVICES : owns
    USERS ||--o{ SESSIONS : holds
    USERS ||--o{ FOLLOWS : follows
    USERS ||--o{ BLOCKS : blocks
    USERS ||--o{ MUTES : mutes
    USERS ||--o{ CLOSE_FRIENDS : lists
    DEVICES ||--o{ SESSIONS : "authenticated on"

    USERS {
        uuid id PK
        citext username UK
        citext email UK "nullable, unique when set"
        text phone_e164 UK "nullable"
        text password_hash "argon2id, nullable if OAuth-only"
        date date_of_birth "wajib: age gating"
        enum account_status "active|suspended|deactivated|deleted"
        boolean is_private
        timestamptz created_at
        timestamptz deleted_at
    }

    USER_PROFILES {
        uuid user_id PK_FK
        text display_name
        text bio
        uuid avatar_media_id FK
        uuid cover_media_id FK
        int follower_count "denormalized"
        int following_count "denormalized"
        timestamptz updated_at
    }

    USER_SETTINGS {
        uuid user_id PK_FK
        enum dm_privacy "everyone|following|nobody"
        enum story_privacy "public|followers|close_friends"
        enum room_invite_privacy "everyone|following|nobody"
        boolean discoverable_by_phone
        text locale
        text timezone
        jsonb notification_prefs
    }

    USER_AUTH_IDENTITIES {
        uuid id PK
        uuid user_id FK
        enum provider "google|apple|password"
        text provider_uid
        timestamptz linked_at
    }

    DEVICES {
        uuid id PK
        uuid user_id FK
        enum platform "android|ios|web"
        text push_token "FCM/APNs"
        text app_version
        timestamptz last_seen_at
        timestamptz revoked_at
    }

    SESSIONS {
        uuid id PK
        uuid user_id FK
        uuid device_id FK
        text refresh_token_hash
        inet ip_address
        timestamptz expires_at
        timestamptz revoked_at
    }

    FOLLOWS {
        uuid follower_id PK_FK
        uuid followee_id PK_FK
        enum status "pending|accepted"
        timestamptz created_at
    }

    BLOCKS {
        uuid blocker_id PK_FK
        uuid blocked_id PK_FK
        timestamptz created_at
    }

    MUTES {
        uuid muter_id PK_FK
        uuid muted_id PK_FK
        boolean mute_posts
        boolean mute_stories
    }

    CLOSE_FRIENDS {
        uuid owner_id PK_FK
        uuid friend_id PK_FK
        timestamptz created_at
    }
```

Catatan desain:

- `date_of_birth` wajib, bukan opsional. Tanpa ini tidak ada age gating, dan age gating
  adalah syarat rilis Play Store untuk aplikasi UGC.
- `FOLLOWS.status` menangani akun privat (request follow). Kalau `is_private=false`,
  status langsung `accepted`.
- `BLOCKS` harus dicek di **setiap** query pembacaan konten. Ini beban query yang
  gampang terlupa — sebaiknya jadi satu layer di repository, bukan diulang per handler.

---

## 2. Media (shared)

Satu tabel media dipakai bersama oleh reels, story, chat, dan avatar. Ini keputusan
penting: kalau tiap domain punya tabel media sendiri, pipeline transcoding dan
moderasi harus ditulis empat kali.

```mermaid
erDiagram
    USERS ||--o{ MEDIA_ASSETS : uploads
    MEDIA_ASSETS ||--o{ MEDIA_VARIANTS : "transcodes into"
    MEDIA_ASSETS ||--o| MODERATION_RESULTS : "scanned by"

    MEDIA_ASSETS {
        uuid id PK
        uuid owner_id FK
        enum kind "video|image|audio|voice_note"
        text storage_key "object storage path"
        text mime_type
        bigint size_bytes
        int duration_ms "null for image"
        int width
        int height
        text blurhash "placeholder saat loading"
        enum processing_status "pending|processing|ready|failed"
        text checksum_sha256 "dedup"
        timestamptz created_at
    }

    MEDIA_VARIANTS {
        uuid id PK
        uuid media_id FK
        enum variant "source|hls|360p|720p|1080p|thumbnail|waveform"
        text storage_key
        bigint size_bytes
        int bitrate_kbps
    }
```

Catatan desain:

- **File binary tidak pernah masuk Postgres.** Yang disimpan hanya `storage_key` ke
  object storage (S3/GCS/R2). Upload pakai presigned URL langsung dari klien ke storage,
  backend hanya menerbitkan URL dan menerima callback — jangan proxy byte video lewat Go.
- `checksum_sha256` memungkinkan dedup. Untuk aplikasi video ini bukan optimasi kecil:
  konten viral yang di-repost ribuan kali bisa berbagi satu blob.
- `processing_status` wajib ada karena reels tidak bisa tayang sebelum transcoding selesai.
  Ini yang bikin reels beda dari chat: ada state asinkron di tengah alur publish.

---

## 3. Reels

```mermaid
erDiagram
    USERS ||--o{ REELS : authors
    MEDIA_ASSETS ||--o| REELS : "rendered as"
    AUDIO_TRACKS ||--o{ REELS : "used in"
    REELS ||--o{ REEL_LIKES : receives
    REELS ||--o{ REEL_COMMENTS : receives
    REELS ||--o{ REEL_SAVES : "bookmarked in"
    REELS ||--o{ REEL_VIEWS : logs
    REELS ||--o{ REEL_HASHTAGS : tagged
    HASHTAGS ||--o{ REEL_HASHTAGS : appears
    REEL_COMMENTS ||--o{ REEL_COMMENTS : "replies to"
    REEL_COMMENTS ||--o{ REEL_COMMENT_LIKES : receives

    REELS {
        uuid id PK
        uuid author_id FK
        uuid media_id FK
        uuid audio_track_id FK "nullable"
        text caption
        enum visibility "public|followers|private"
        enum status "draft|published|removed"
        boolean comments_enabled
        int like_count
        int comment_count
        int view_count
        int share_count
        timestamptz published_at
        timestamptz deleted_at
    }

    AUDIO_TRACKS {
        uuid id PK
        uuid media_id FK
        uuid original_reel_id FK "nullable, asal sound"
        text title
        text artist
        boolean is_licensed
        int use_count
    }

    REEL_LIKES {
        uuid reel_id PK_FK
        uuid user_id PK_FK
        timestamptz created_at
    }

    REEL_COMMENTS {
        uuid id PK
        uuid reel_id FK
        uuid author_id FK
        uuid parent_comment_id FK "nullable, 1 level saja"
        text body
        int like_count
        timestamptz created_at
        timestamptz deleted_at
    }

    REEL_COMMENT_LIKES {
        uuid comment_id PK_FK
        uuid user_id PK_FK
    }

    REEL_SAVES {
        uuid user_id PK_FK
        uuid reel_id PK_FK
        timestamptz created_at
    }

    REEL_VIEWS {
        bigint id PK
        uuid reel_id FK
        uuid viewer_id FK "nullable jika anonim"
        int watch_ms
        boolean completed
        timestamptz viewed_at "PARTITION KEY"
    }

    HASHTAGS {
        uuid id PK
        citext tag UK
        int usage_count
    }

    REEL_HASHTAGS {
        uuid reel_id PK_FK
        uuid hashtag_id PK_FK
    }
```

Catatan desain:

- `AUDIO_TRACKS` adalah mekanik yang bikin format short-video menyebar (satu sound dipakai
  ribuan orang). Kalau tabel ini dilewat, reels cuma jadi galeri video biasa. `is_licensed`
  penting karena audio berhak cipta adalah sumber takedown nomor satu.
- `REEL_VIEWS` adalah tabel terpanas di sistem — satu baris per tayangan. **Wajib partisi
  per hari** dan sebaiknya tidak di Postgres primary sama sekali; alirkan ke ClickHouse
  atau BigQuery lewat queue, simpan agregatnya saja di `REELS.view_count`. Kalau ini
  dibiarkan di tabel biasa, database akan mati jauh sebelum produknya laku.
- `parent_comment_id` dibatasi satu level. Threading tak terbatas terlihat keren di ERD
  dan menyiksa saat query.

---

## 4. Stories

```mermaid
erDiagram
    USERS ||--o{ STORIES : posts
    MEDIA_ASSETS ||--o| STORIES : "rendered as"
    STORIES ||--o{ STORY_VIEWS : "seen by"
    STORIES ||--o{ STORY_REACTIONS : receives
    STORIES ||--o{ STORY_MENTIONS : mentions

    STORIES {
        uuid id PK
        uuid author_id FK
        uuid media_id FK
        enum visibility "public|followers|close_friends"
        jsonb overlays "sticker, teks, posisi"
        int view_count
        timestamptz created_at
        timestamptz expires_at "created_at + 24h"
    }

    STORY_VIEWS {
        uuid story_id PK_FK
        uuid viewer_id PK_FK
        timestamptz viewed_at
    }

    STORY_REACTIONS {
        uuid id PK
        uuid story_id FK
        uuid user_id FK
        text emoji
        uuid message_id FK "reply masuk sebagai DM"
    }

    STORY_MENTIONS {
        uuid story_id PK_FK
        uuid mentioned_user_id PK_FK
    }
```

Catatan desain:

- `expires_at` bukan berarti baris dihapus jam 24:00. Query pembacaan memfilter
  `expires_at > now()`, dan job harian memindahkan yang kedaluwarsa ke arsip. Alasannya:
  moderasi dan permintaan hukum bisa datang setelah story hilang dari UI.
- Balasan story mendarat di `MESSAGES` (domain 5), bukan tabel sendiri. Inilah titik
  sambung "story → chat" yang di sketsa digambar sebagai panah dari feed ke layar hijau.

---

## 5. Chat

```mermaid
erDiagram
    CONVERSATIONS ||--o{ CONVERSATION_MEMBERS : includes
    USERS ||--o{ CONVERSATION_MEMBERS : "member of"
    CONVERSATIONS ||--o{ MESSAGES : contains
    USERS ||--o{ MESSAGES : sends
    MESSAGES ||--o{ MESSAGE_ATTACHMENTS : carries
    MEDIA_ASSETS ||--o{ MESSAGE_ATTACHMENTS : "attached as"
    MESSAGES ||--o{ MESSAGE_REACTIONS : receives
    MESSAGES ||--o{ MESSAGE_RECEIPTS : tracked
    MESSAGES ||--o{ MESSAGES : "replies to"

    CONVERSATIONS {
        uuid id PK
        enum type "direct|group"
        text title "null untuk direct"
        uuid avatar_media_id FK
        uuid created_by FK
        uuid last_message_id FK "denormalized untuk list chat"
        timestamptz last_message_at "index utama sorting"
        timestamptz created_at
    }

    CONVERSATION_MEMBERS {
        uuid conversation_id PK_FK
        uuid user_id PK_FK
        enum role "owner|admin|member"
        uuid last_read_message_id FK
        int unread_count "denormalized"
        timestamptz muted_until
        timestamptz joined_at
        timestamptz left_at
    }

    MESSAGES {
        uuid id PK "uuid v7: sortable"
        uuid conversation_id FK
        uuid sender_id FK
        enum type "text|media|voice_note|story_reply|call_event|system"
        text body "nullable"
        uuid reply_to_message_id FK
        uuid story_id FK "nullable, konteks story_reply"
        timestamptz created_at "PARTITION KEY"
        timestamptz edited_at
        timestamptz deleted_at
    }

    MESSAGE_ATTACHMENTS {
        uuid message_id PK_FK
        uuid media_id PK_FK
        smallint position
    }

    MESSAGE_REACTIONS {
        uuid message_id PK_FK
        uuid user_id PK_FK
        text emoji
    }

    MESSAGE_RECEIPTS {
        uuid message_id PK_FK
        uuid user_id PK_FK
        timestamptz delivered_at
        timestamptz read_at
    }
```

Catatan desain:

- **`MESSAGE_RECEIPTS` hanya untuk grup.** Untuk chat 1:1, `last_read_message_id` di
  `CONVERSATION_MEMBERS` sudah cukup dan jauh lebih murah — receipt per-pesan per-anggota
  tumbuh O(pesan × anggota). Untuk grup 200 orang, satu pesan = 200 baris. Kalau centang
  biru per-anggota tidak masuk MVP, buang tabel ini dulu.
- `MESSAGES` pakai UUIDv7 supaya urut secara waktu, jadi pagination cursor-based bisa
  langsung pakai PK tanpa index tambahan.
- Presence (online/typing) **tidak ada di ERD ini** dan memang tidak boleh masuk Postgres —
  itu state efemeral, tempatnya di Redis dengan TTL.
- Keputusan yang belum diambil dan harus diambil sebelum coding: **E2EE atau tidak.**
  Kalau ya, `body` jadi ciphertext, butuh tabel `USER_PREKEYS` / `DEVICE_KEYS` (Signal
  protocol), dan konsekuensinya moderasi sisi server jadi mustahil serta search pesan
  harus di klien. Ini trade-off produk, bukan trade-off teknis — perlu diputuskan di PRD.

---

## 6. Voice Rooms & Calls

Ini bagian "in room" di sketsa — yang paling belum terdefinisi di PRD.

```mermaid
erDiagram
    USERS ||--o{ ROOMS : hosts
    ROOMS ||--o{ ROOM_PARTICIPANTS : has
    USERS ||--o{ ROOM_PARTICIPANTS : joins
    ROOMS ||--o{ ROOM_SPEAK_REQUESTS : receives
    ROOMS ||--o| MEDIA_ASSETS : "recorded to"
    CONVERSATIONS ||--o{ CALLS : "call within"
    CALLS ||--o{ CALL_PARTICIPANTS : has

    ROOMS {
        uuid id PK
        uuid host_id FK
        text title
        text topic
        enum visibility "public|followers|invite_only"
        enum status "scheduled|live|ended"
        boolean is_recorded
        uuid recording_media_id FK
        int max_participants
        int peak_participant_count
        text sfu_room_id "id di media server"
        timestamptz scheduled_at
        timestamptz started_at
        timestamptz ended_at
    }

    ROOM_PARTICIPANTS {
        uuid id PK
        uuid room_id FK
        uuid user_id FK
        enum role "host|moderator|speaker|listener"
        boolean is_muted
        timestamptz joined_at
        timestamptz left_at
    }

    ROOM_SPEAK_REQUESTS {
        uuid room_id PK_FK
        uuid user_id PK_FK
        enum status "pending|approved|denied"
        timestamptz requested_at
    }

    CALLS {
        uuid id PK
        uuid conversation_id FK
        uuid initiator_id FK
        enum kind "audio|video"
        enum status "ringing|ongoing|ended|missed|declined"
        int duration_seconds
        timestamptz started_at
        timestamptz ended_at
    }

    CALL_PARTICIPANTS {
        uuid call_id PK_FK
        uuid user_id PK_FK
        timestamptz joined_at
        timestamptz left_at
    }
```

Catatan desain:

- **Audio tidak lewat Postgres dan tidak lewat Go.** Tabel ini hanya menyimpan metadata
  sesi; media stream ditangani SFU (LiveKit / mediasoup / Janus). `sfu_room_id` adalah
  jembatannya. Backend Go berperan sebagai penerbit token join dan pemegang otoritas peran.
- `ROOM_PARTICIPANTS` sengaja punya PK sendiri, bukan composite — satu orang bisa
  keluar-masuk room berkali-kali dalam satu sesi, dan tiap sesi perlu tercatat.
- `is_recorded` punya implikasi hukum: perekaman suara butuh consent eksplisit dari
  semua peserta di banyak yurisdiksi. Kalau fitur ini masuk, `CONSENTS` di domain 8 harus
  ikut dipakai.

---

## 7. AI Layer

Domain ini menjawab kontradiksi terbesar di PRD: nama "Syntra" menjanjikan AI synthesis,
tapi belum ada satu pun fitur AI yang didefinisikan. Struktur di bawah cukup generik untuk
menampung use case apa pun yang nanti dipilih, tanpa memaksa keputusan sekarang.

```mermaid
erDiagram
    USERS ||--o{ AI_JOBS : requests
    AI_JOBS ||--o| AI_RESULTS : produces
    USERS ||--o| USER_INTEREST_VECTORS : profiled
    CONTENT_EMBEDDINGS }o--|| MEDIA_ASSETS : embeds

    AI_JOBS {
        uuid id PK
        uuid user_id FK
        enum kind "caption_gen|transcribe|translate|summarize_room|moderate|recommend"
        enum subject_type "reel|story|message|room|media"
        uuid subject_id
        enum status "queued|running|succeeded|failed"
        text model_id "audit: model mana yang dipakai"
        int input_tokens
        int output_tokens
        numeric cost_usd
        text error
        timestamptz created_at
        timestamptz completed_at
    }

    AI_RESULTS {
        uuid job_id PK_FK
        jsonb payload "bentuk tergantung kind"
        numeric confidence
        boolean accepted_by_user "sinyal kualitas"
    }

    CONTENT_EMBEDDINGS {
        uuid id PK
        enum content_type "reel|story|audio|profile"
        uuid content_id
        vector embedding "pgvector, 768/1536 dim"
        text model_id
        timestamptz created_at
    }

    USER_INTEREST_VECTORS {
        uuid user_id PK_FK
        vector embedding
        timestamptz updated_at
    }
```

Catatan desain:

- `cost_usd` dan token count ada di skema sejak awal dengan sengaja. Biaya inferensi AI
  adalah biaya variabel per pengguna; kalau tidak diukur dari hari pertama, tidak akan
  ketahuan sampai tagihannya datang.
- `CONTENT_EMBEDDINGS` pakai `pgvector`. Selama korpus masih di bawah ~1 juta item, ini
  cukup dan menghemat satu komponen infrastruktur. Di atas itu, pindah ke vector DB khusus.
- `accepted_by_user` adalah kolom kecil yang bernilai besar: ini satu-satunya sinyal
  murah untuk mengukur apakah fitur AI-nya benar-benar berguna atau cuma dekorasi.

---

## 8. Trust & Safety, Privasi, Notifikasi

Bab yang paling sering ditunda dan paling mahal kalau ditunda.

```mermaid
erDiagram
    USERS ||--o{ REPORTS : files
    REPORTS ||--o{ MODERATION_ACTIONS : "resolved by"
    MODERATION_ACTIONS ||--o{ APPEALS : "contested by"
    MODERATION_RESULTS }o--|| USERS : "flags content of"
    USERS ||--o{ CONSENTS : grants
    USERS ||--o{ DATA_REQUESTS : submits
    USERS ||--o{ NOTIFICATIONS : receives
    USERS ||--o{ AUDIT_LOGS : "acted in"

    REPORTS {
        uuid id PK
        uuid reporter_id FK
        enum target_type "user|reel|story|message|room|comment"
        uuid target_id
        enum reason "spam|harassment|nudity|violence|csam|copyright|other"
        text detail
        enum status "open|triaged|actioned|dismissed"
        enum priority "low|normal|high|critical"
        timestamptz created_at
        timestamptz resolved_at
    }

    MODERATION_RESULTS {
        uuid id PK
        enum subject_type "media|reel|story|message|comment"
        uuid subject_id
        text provider "vendor/model klasifikasi"
        jsonb labels "skor per kategori"
        enum decision "allow|limit|block|review"
        timestamptz created_at
    }

    MODERATION_ACTIONS {
        uuid id PK
        uuid moderator_id FK "null jika otomatis"
        uuid report_id FK
        enum target_type "user|reel|story|message|room|comment"
        uuid target_id
        enum action "warn|remove_content|shadow_limit|suspend|ban"
        text reason
        timestamptz expires_at "null = permanen"
        timestamptz created_at
    }

    APPEALS {
        uuid id PK
        uuid action_id FK
        uuid user_id FK
        text statement
        enum status "pending|upheld|overturned"
        timestamptz created_at
    }

    CONSENTS {
        uuid id PK
        uuid user_id FK
        enum purpose "tos|privacy_policy|marketing|ai_training|call_recording"
        text policy_version
        timestamptz granted_at
        timestamptz revoked_at
    }

    DATA_REQUESTS {
        uuid id PK
        uuid user_id FK
        enum kind "export|deletion"
        enum status "pending|processing|completed|rejected"
        text export_storage_key
        timestamptz requested_at
        timestamptz completed_at
    }

    NOTIFICATIONS {
        uuid id PK
        uuid recipient_id FK
        uuid actor_id FK
        enum type "follow|like|comment|mention|story_reply|room_live|system"
        enum subject_type "reel|story|message|room|user"
        uuid subject_id
        timestamptz read_at
        timestamptz created_at
    }

    AUDIT_LOGS {
        bigint id PK
        uuid actor_id FK
        text action
        text subject_type
        uuid subject_id
        jsonb metadata
        inet ip_address
        timestamptz created_at
    }
```

Catatan desain:

- `REPORTS.reason` memuat `csam` sebagai kategori terpisah dengan `priority=critical`.
  Ini bukan detail kosmetik: kategori itu punya kewajiban pelaporan hukum dan SLA
  penanganan yang berbeda total dari spam, dan harus bisa dirutekan secara khusus.
- `CONSENTS` menyimpan `policy_version`. Consent tanpa versi kebijakan tidak berguna
  saat audit — kamu perlu bisa membuktikan pengguna menyetujui teks yang mana.
- `DATA_REQUESTS` mengimplementasikan hak akses dan hak hapus di UU PDP. Menambahkannya
  sekarang berarti satu tabel; menambahkannya setelah 20 tabel penuh data berarti proyek
  tersendiri.
- `AUDIT_LOGS` khusus aksi bernilai tinggi (login, ganti password, aksi moderasi, akses
  data), bukan log semua request.

---

## 9. Pemetaan fase

ERD lengkap di atas **bukan scope MVP**. Ini peta tujuan; yang dibangun duluan sebaiknya
satu pilar saja, sesuai catatan di analisis PRD.

> **Urutan di bawah sudah direvisi.** Setelah membaca `catatan-untuk-app.md`, ternyata
> **story sudah menjadi bagian layar utama** di aplikasi Android yang dibangun —
> bukan fitur fase 2. Urutan pengerjaan yang berlaku sekarang ada di
> [`app-backend-alignment.md`](app-backend-alignment.md) §7. Tabel di bawah tetap
> berguna sebagai pengelompokan entitas per domain.

| Fase | Domain | Entitas | Sudah dibuat? |
|---|---|---|---|
| **MVP** | Identity, Media, Chat, T&S minimum | `USERS`, `USER_PROFILES`, `USER_SETTINGS`, `DEVICES`, `FOLLOWS`, `BLOCKS`, `MEDIA_ASSETS`, `CONVERSATIONS`, `CONVERSATION_MEMBERS`, `MESSAGES`, `MESSAGE_ATTACHMENTS`, `REPORTS`, `MODERATION_ACTIONS`, `CONSENTS`, `NOTIFICATIONS` | ✅ migrasi 1 |
| **MVP (naik dari fase 2)** | Story | `STORIES`, `STORY_VIEWS` | ✅ migrasi 4 |
| **Fase 2** | Reels + sisa story | `STORY_REACTIONS`, `REELS`, `REEL_LIKES`, `REEL_COMMENTS`, `REEL_VIEWS`, `HASHTAGS`, `AUDIO_TRACKS`, `MODERATION_RESULTS` | belum |
| **Fase 3** | Voice rooms | `ROOMS`, `ROOM_PARTICIPANTS`, `ROOM_SPEAK_REQUESTS`, `CALLS`, `CALL_PARTICIPANTS` |
| **Fase 4** | AI layer | `AI_JOBS`, `AI_RESULTS`, `CONTENT_EMBEDDINGS`, `USER_INTEREST_VECTORS` |
| **Sesuai kebutuhan** | Compliance & appeal | `DATA_REQUESTS`, `APPEALS`, `AUDIT_LOGS`, `MUTES`, `CLOSE_FRIENDS` |

---

## 10. Yang sengaja TIDAK masuk Postgres

Sama pentingnya dengan yang masuk:

| Data | Tempatnya | Alasan |
|---|---|---|
| Presence, typing indicator | Redis + TTL | Efemeral, write sangat sering, tidak perlu durabel |
| Unread badge realtime | Redis counter | Denormalisasi di Postgres cukup sebagai sumber kebenaran |
| Feed timeline | Redis / precomputed store | Fanout-on-write; `SELECT` join saat baca tidak akan sanggup |
| Byte video/audio/gambar | Object storage + CDN | Sudah jelas |
| `REEL_VIEWS` mentah | ClickHouse / BigQuery | Volume analitik, bukan volume transaksional |
| Rate limit counter | Redis | — |

---

## 11. Keputusan yang masih menggantung

Harus dijawab di PRD sebelum migrasi pertama ditulis:

1. **E2EE untuk chat: ya atau tidak.** Ini mengubah `MESSAGES` secara fundamental dan
   menentukan apakah moderasi sisi server mungkin dilakukan.
2. **Apa fitur AI yang sebenarnya.** Domain 7 saat ini adalah wadah kosong yang dirancang
   fleksibel — tapi wadah kosong tidak bisa dijadwalkan.
3. **Reels: rekomendasi algoritmik atau kronologis.** Jawaban "algoritmik" berarti
   `CONTENT_EMBEDDINGS` dan pipeline ranking naik dari fase 4 ke fase 2.
4. **Target geografi.** Menentukan region data residency dan apakah GDPR ikut berlaku
   di samping UU PDP.
5. **Single-region atau multi-region.** Kalau multi, `uuid` v7 dan strategi partisi di
   atas sudah siap; kalau tidak, sebagian kompleksitas bisa dibuang sekarang.
