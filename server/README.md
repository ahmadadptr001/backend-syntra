# Syntra Server

Satu codebase Go yang melayani **REST API dan WebSocket sekaligus**, dalam satu
proses dan satu binary.

- Modul: `github.com/ahmadadptr001/backend-syntra`
- Basis data: **Supabase online**, diakses lewat API HTTP (PostgREST + GoTrue) — bukan koneksi PostgreSQL langsung
- Realtime: WebSocket sendiri (`gorilla/websocket`) + Redis Pub/Sub untuk fanout antar-instance
- Klien: Android (Kotlin)

> **Seluruh dokumentasi ada di [`../docs/`](../docs/README.md).** Berkas ini
> khusus membahas kode Go-nya; untuk menyambungkan aplikasi, kontrak API,
> voice room, dan cara menjalankan server, mulai dari sana.

| Cari apa | Di mana |
|---|---|
| Kontrak API untuk klien | [`../docs/api.md`](../docs/api.md) |
| Voice room | [`../docs/voice-rooms.md`](../docs/voice-rooms.md) |
| Menjalankan di laptop | [`../docs/nginx.md`](../docs/nginx.md) |
| Model data | [`../docs/erd.md`](../docs/erd.md) |
| Indeks seluruh dokumen | [`../docs/README.md`](../docs/README.md) |
| Kontrak formal | [`api/openapi.yaml`](api/openapi.yaml), [`api/asyncapi.yaml`](api/asyncapi.yaml) |

**Menambah atau mengubah endpoint?** Perbarui
[`../docs/api.md`](../docs/api.md) di giliran yang sama — tabel ringkasan rute
dan bagian detailnya. Aplikasi mengacu ke dokumen itu.

---

## Kenapa socket dan API digabung

Bukan karena lebih hemat repo. Karena keduanya **wajib memakai aturan bisnis yang
sama**.

Kirim pesan lewat `POST /conversations/{id}/messages` dan lewat frame
`message.send` mendarat di fungsi yang sama persis: `chat.Service.SendMessage`.
Pengecekan keanggotaan, batas panjang, dan pembuatan id hanya ditulis satu kali.

Kalau dipisah jadi dua layanan, cepat atau lambat salah satu jalur akan
ketinggalan saat aturan berubah — dan yang lebih longgar menjadi celah keamanan
yang tidak kelihatan sampai ada yang memanfaatkannya.

---

## Struktur folder

```
server/
├── cmd/syntra/            titik masuk; setipis mungkin
│
├── internal/
│   ├── app/               perakitan seluruh komponen + urutan startup/shutdown
│   ├── config/            satu-satunya tempat yang membaca environment
│   ├── auth/              identitas pemanggil + verifier Supabase Auth
│   │
│   ├── domain/            ATURAN BISNIS — inti aplikasi
│   │   ├── chat/          percakapan, pesan, riwayat
│   │   ├── story/         status 24 jam, dikelompokkan per orang
│   │   ├── media/         alur unggah tiga langkah
│   │   ├── presence/      online / last seen
│   │   ├── room/          voice room (metadata + token SFU)
│   │   └── user/          direktori pengguna + follow
│   │
│   ├── repository/
│   │   ├── supabase/      implementasi port penyimpanan lewat RPC Supabase
│   │   └── redisstore/    state efemeral: presence
│   │
│   ├── transport/         adaptor masuk
│   │   ├── rest/          router, middleware, handler HTTP
│   │   │   ├── handler/   terjemahan HTTP <-> service
│   │   │   ├── httpx/     bentuk response & decode request
│   │   │   └── middleware/ request id, log, recover, CORS, auth
│   │   └── ws/            hub, client, router frame, handler, publisher
│   │       └── protocol/  format amplop frame
│   │
│   ├── platform/          pembungkus infrastruktur, bebas dari domain
│   │   ├── supabase/      klien HTTP PostgREST + GoTrue
│   │   ├── cache/         klien Redis
│   │   ├── pubsub/        fanout antar-instance lewat Redis
│   │   ├── livekit/       token akses media server (voice room)
│   │   └── logger/        slog
│   │
│   └── pkg/               utilitas kecil tanpa dependensi
│       ├── id/            UUIDv7
│       └── topic/         penamaan kanal pub/sub
│
├── migrations/            SQL: skema, RLS policy, fungsi RPC
├── api/                   kontrak OpenAPI + AsyncAPI
├── deployments/           Dockerfile, docker-compose
├── Makefile
└── .env.example
```

Seluruh kode berada di bawah `internal/`, jadi tidak ada bagian dari server ini
yang bisa diimpor proyek lain secara tidak sengaja.

### Soal path import

Ada dua jenis import yang bentuknya mirip:

- `github.com/gorilla/websocket`, `github.com/redis/go-redis/v9` — **dependensi
  sungguhan**, diunduh `go mod tidy`, terdaftar di `go.mod`.
- `github.com/ahmadadptr001/backend-syntra/internal/...` — **package proyek ini
  sendiri**. Prefix itu adalah nama modul di `go.mod`, bukan alamat unduhan.
  Potong prefiksnya dan sisanya adalah path folder:
  `.../internal/domain/chat` = `server/internal/domain/chat/`.

### Aturan dependensi

```
transport ──►  domain  ◄── repository
    │            ▲             │
    └────────────┼─────────────┘
              platform
```

Satu aturan yang tidak boleh dilanggar: **`domain` tidak mengimpor apa pun dari
`transport`, `repository`, atau `platform`.**

Domain mendeklarasikan apa yang ia butuhkan sebagai interface — `Repository`
untuk menyimpan, `Publisher` untuk menyiarkan — dan lapisan luar yang
menyesuaikan diri. Cek cepat kalau ragu:

```bash
go list -deps ./internal/domain/... | grep -E 'transport|repository|platform'   # harus kosong
```

---

## Cara kerja akses data

Ini bagian yang paling berbeda dari backend Go pada umumnya, jadi baca sekali
sebelum menulis query.

**Tidak ada driver PostgreSQL di proyek ini.** Tidak ada `pgx`, tidak ada
`database/sql`, tidak ada connection pool. Yang ada adalah klien HTTP ke dua API
Supabase:

| API | Path | Dipakai untuk |
|---|---|---|
| PostgREST | `/rest/v1/...` | tabel dan fungsi database |
| GoTrue | `/auth/v1/...` | verifikasi token pengguna |

Alur satu permintaan:

```
POST /api/v1/conversations/{id}/messages
      ↓  rest/router.go              routing
      ↓  middleware.Auth             verifikasi JWT ke /auth/v1/user, simpan token di Principal
      ↓  handler/chat.go             decode body, panggil service
      ↓  domain/chat/service.go      aturan bisnis: validasi, cek keanggotaan
      ↓  repository/supabase/chat.go POST /rest/v1/rpc/send_message + JWT pengguna
      ↓  Supabase                    fungsi send_message() jalan dalam satu transaksi
```

Dua konsekuensi yang menentukan desainnya:

**1. Tidak ada transaksi lintas-permintaan.** Satu panggilan HTTP = satu
transaksi. Karena itu operasi yang harus atomik dibungkus sebagai fungsi
database dan dipanggil lewat RPC. `send_message()` menyimpan pesan, memperbarui
ringkasan percakapan, dan menaikkan unread anggota lain — kalau ketiganya
dirangkai sebagai tiga panggilan HTTP, kegagalan di tengah meninggalkan pesan
yang tersimpan tapi tidak pernah muncul di daftar chat.

**2. Server tidak punya hak istimewa.** Lihat bagian berikut.

---

## Catatan Supabase — anon key dan RLS

**Anon key tidak memberi hak akses apa pun.** Ia mengidentifikasi proyek, bukan
pengguna, dan memang dirancang untuk ditanam di aplikasi klien — sifatnya publik.
Yang menentukan baris mana yang boleh disentuh adalah **RLS**, dievaluasi
terhadap `auth.uid()` dari JWT pengguna.

Artinya alurnya begini, dan tidak bisa dipotong:

1. Klien Kotlin login lewat **Supabase Auth**, mendapat JWT.
2. Klien mengirim JWT itu ke server ini (`Authorization: Bearer <jwt>`).
3. Server memverifikasinya ke `/auth/v1/user`, lalu **menyimpannya di
   `auth.Principal.Token`**.
4. Setiap panggilan repository meneruskan JWT itu ke Supabase.
5. `auth.uid()` terisi → policy RLS lolos → data terbaca.

Kalau langkah 3–4 terlewat, `auth.uid()` bernilai NULL dan **setiap query
mengembalikan nol baris tanpa pesan error yang jelas**. Ini penyebab nomor satu
dari gejala "kodenya benar tapi datanya kosong". `APIError.IsDeniedByRLS()` ada
untuk membantu mengenalinya.

### Konsekuensi yang perlu diterima

| Hal | Implikasi |
|---|---|
| Pekerjaan latar tanpa pengguna | Tidak bisa. Butuh `SUPABASE_SERVICE_ROLE_KEY` (melewati RLS) — isi hanya kalau memang dibutuhkan |
| `AUTH_DEV_BYPASS=true` | Token palsu bukan JWT Supabase → semua query data ditolak RLS. Bypass hanya berguna untuk menguji routing dan socket |
| Migrasi dari server | Tidak bisa; server tidak memegang kredensial database. Pakai SQL Editor atau Supabase CLI |
| Latensi | Setiap query jadi satu round-trip HTTPS, bukan koneksi pool yang hangat |

Kalau nanti terasa membatasi, jalan keluarnya adalah beralih ke koneksi
PostgreSQL langsung memakai sandi database + `pgx`. Yang berubah hanya
`internal/platform/` dan `internal/repository/` — domain dan transport tidak
tersentuh sama sekali. Itu memang gunanya batas lapisan di atas.

### Migrasi

Dua berkas, jalankan berurutan:

| Berkas | Isi |
|---|---|
| `20260723000001_init_mvp.sql` | tabel MVP + index, RLS diaktifkan tanpa policy (tertutup rapat) |
| `20260723000002_rls_and_rpc.sql` | fungsi RPC + policy RLS + hak eksekusi |

Terapkan lewat Dashboard Supabase → SQL Editor, atau `supabase db push`.
`make migrate` hanya mencetak petunjuk ini — server memang tidak bisa
menjalankannya sendiri.

Satu catatan teknis di berkas kedua: `is_conversation_member()` dibuat
`SECURITY DEFINER` karena ia dipakai di dalam policy tabel
`conversation_members` sendiri. Kalau ia tunduk pada RLS, mengevaluasi policy
akan memanggil fungsi yang kembali mengevaluasi policy — rekursi tak berujung.
Ini jebakan Supabase yang paling sering ditemui orang.

---

## Menjalankan

### Prasyarat

- Go 1.23+
- Proyek Supabase online (ambil URL + anon key di Dashboard → Project Settings → API)
- Redis (`make up` menyediakannya lewat Docker)

### Langkah

```bash
cd server
cp .env.example .env      # isi SUPABASE_URL dan SUPABASE_ANON_KEY

go mod tidy               # WAJIB dulu: membuat go.sum
go vet ./...              # pastikan kompilasi bersih
go run ./cmd/syntra       # server di :8080
```

Migrasi dijalankan di Supabase Dashboard → SQL Editor, **berurutan**:

| Berkas | Isi |
|---|---|
| `20260723000001_init_mvp.sql` | tabel MVP, index, RLS diaktifkan tanpa policy |
| `20260723000002_rls_and_rpc.sql` | fungsi RPC chat + policy + hak eksekusi |
| `20260723000003_conversation_display.sql` | nama & cuplikan pesan di daftar chat |
| `20260723000004_app_features.sql` | story, riwayat pesan, buat percakapan, media, direktori |
| `20260723000005_follow_and_receipts.sql` | follow/unfollow, profil + status follow, bahan ✓✓ |
| `20260723000006_fix_get_messages.sql` | perbaikan bug kolom ambigu di riwayat pesan |
| `20260723000007_voice_rooms.sql` | voice room: tabel + fungsi peran & kapasitas |

Selain itu, buat bucket **`media`** di Dashboard → Storage dan setel **public**,
kalau tidak alur unggah media akan gagal.

### Verifikasi cepat

```bash
curl localhost:8080/healthz
curl localhost:8080/readyz     # 503 kalau Supabase/Redis belum tersambung

# JWT diambil dari hasil login Supabase Auth di klien.
TOKEN="eyJhbGciOi..."
curl localhost:8080/api/v1/conversations -H "Authorization: Bearer $TOKEN"

# WebSocket
websocat "ws://localhost:8080/api/v1/ws?token=$TOKEN"
> {"type":"subscribe","ref":"1","data":{"topics":["conversation:<uuid>"]}}
> {"type":"message.send","ref":"2","data":{"conversation_id":"<uuid>","body":"halo"}}
```

Untuk menguji fanout antar-instance — bagian yang paling sering rusak diam-diam —
jalankan dua proses sekaligus:

```bash
make up-cluster           # :8080 dan :8081
```

Sambungkan satu klien ke masing-masing port. Kalau pesan dari :8080 tidak sampai
ke :8081, berarti jalur Redis Pub/Sub-nya bermasalah, bukan kode chat-nya.

---

## Protokol WebSocket

Semua frame memakai satu amplop:

```json
{"type": "message.send", "ref": "c-42", "data": {"conversation_id": "...", "body": "halo"}}
```

`ref` ditentukan klien dan dikembalikan apa adanya pada `ack`/`error`, sehingga
pesan optimistik di UI bisa langsung diganti dengan yang otoritatif dari server.

Autentikasi terjadi saat handshake HTTP — bukan lewat frame — memakai middleware
yang sama dengan REST.

**Langganan topik diotorisasi satu per satu.** Ini titik paling rawan di seluruh
lapisan socket: tanpa pemeriksaan, siapa pun yang punya token valid bisa
mengirim `subscribe` ke `conversation:<id-orang-lain>` dan ikut menyimak
percakapan yang bukan miliknya. Aturannya ada di `authorizeTopic`
(`internal/transport/ws/handlers.go`) — kanal `user:` hanya untuk diri sendiri,
`conversation:` hanya untuk anggota, dan topik yang belum punya aturan **ditolak
secara default**.

Heartbeat memakai ping/pong level protokol, bukan frame aplikasi. Server
mengirim ping tiap `WS_PING_INTERVAL` dan memutus koneksi yang tidak membalas
dalam `WS_PONG_WAIT`.

---

## Menambah fitur baru

Contoh: menambahkan reaksi pesan.

1. **Domain** — tambah entitas dan method di `internal/domain/chat`, lalu
   tambahkan method yang dibutuhkan ke interface `Repository`.
2. **Migrasi** — tambah tabel dan **fungsi RPC**-nya di `migrations/`, lengkap
   dengan `REVOKE`/`GRANT EXECUTE`. Kalau operasinya menyentuh lebih dari satu
   tabel, ia harus jadi fungsi — bukan beberapa panggilan HTTP.
3. **Repository** — panggil RPC itu di `internal/repository/supabase/`.
   Kompilasi akan gagal sampai selesai; itu memang gunanya
   `var _ chat.Repository = (*ChatRepository)(nil)`.
4. **Transport** — daftarkan frame di `RegisterHandlers` dan/atau rute di
   `rest.NewRouter`. Handler-nya harus tipis: decode, panggil service, balas.
5. **Kontrak** — perbarui `api/asyncapi.yaml` / `api/openapi.yaml`.
6. **Dokumentasi** — perbarui [`../docs/api.md`](../docs/api.md): tabel
   ringkasan rute DAN bagian detail endpointnya. Ini wajib, bukan opsional —
   aplikasi membangun kliennya dari dokumen itu.

Kalau sebuah handler mulai berisi `if` yang menentukan siapa boleh melakukan
apa, aturan itu salah tempat — pindahkan ke service.

---

## Konfigurasi

Seluruh konfigurasi lewat environment variable; daftar lengkap beserta
penjelasannya ada di [`.env.example`](.env.example). Konfigurasi yang salah
membuat proses **gagal saat startup**, bukan saat request pertama masuk.

Yang paling sering keliru:

| Variabel | Catatan |
|---|---|
| `SUPABASE_ANON_KEY` | wajib; tanpa ini startup ditolak |
| `SUPABASE_SERVICE_ROLE_KEY` | **melewati RLS**; biarkan kosong kecuali benar-benar dibutuhkan |
| `WS_PING_INTERVAL` | harus lebih kecil dari `WS_PONG_WAIT`, kalau tidak koneksi sehat ikut diputus |
| `AUTH_DEV_BYPASS` | **melumpuhkan autentikasi**; startup ditolak kalau `true` di staging/production |
| `HTTP_CORS_ORIGINS` | wajib eksplisit di production; tidak ada wildcard |
| `WS_ALLOWED_ORIGINS` | pemeriksaan origin mencegah Cross-Site WebSocket Hijacking |

---

## Status

Sudah ada dan berfungsi (`go vet` bersih, binary terbukti jalan):

- Konfigurasi tervalidasi, logging terstruktur, graceful shutdown, pembaca `.env`
- Klien Supabase: PostgREST + RPC + Storage, penerusan JWT pengguna
- Autentikasi lewat Supabase Auth (`/auth/v1/user`) dengan cache ber-TTL
- Hub WebSocket: registry, topik, heartbeat, backpressure, fanout antar-instance
- Middleware REST lengkap (termasuk `Hijack` passthrough yang dibutuhkan upgrade WS)
- **Chat** — daftar percakapan, riwayat pesan, kirim, tandai dibaca, buat
  percakapan pribadi (idempoten) dan grup
- **Story** — unggah, daftar terkelompok per orang, tandai ditonton
- **Media** — alur unggah tiga langkah lewat Supabase Storage
- **Presence** — online/last seen di Redis dengan TTL, plus siaran `presence.update`
- **Direktori pengguna & follow** — pencarian username untuk hasil scan QR,
  follow/unfollow, daftar following
- **Voice room** — buat/gabung/keluar, peran & raise hand, chat efemeral,
  token LiveKit. Suara TIDAK lewat backend; lihat [../docs/voice-rooms.md](../docs/voice-rooms.md)
- **Bahan indikator ✓✓** — posisi baca lawan bicara di daftar percakapan
- Migrasi: skema MVP, RLS policy, dan seluruh fungsi RPC

Belum ada, dan disengaja:

- Verifikasi JWT secara lokal lewat JWKS. Sekarang tiap token yang belum
  ter-cache berarti satu panggilan ke Supabase; ganti kalau trafik sudah tinggi
- Rate limiting per pengguna (yang ada baru per IP di nginx)
- Ubah/hapus pesan, kelola anggota grup, setujui permintaan follow
- Reels, panggilan 1:1, AI
- Push notification (FCM) saat aplikasi tertutup
- Test. Struktur ini dibuat supaya mudah dites (port berupa interface, service
  bebas dari HTTP), tapi belum ada satu pun yang ditulis

Daftar lengkap dan terkini ada di [`../docs/api.md`](../docs/api.md) §12.
