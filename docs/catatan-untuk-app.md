# Syntra

Aplikasi chat Android bergaya modern (mirip WhatsApp) yang dibangun sepenuhnya dengan
**Jetpack Compose**. Syntra menampilkan daftar percakapan, story/status ala WhatsApp
(dengan foto & video), layar percakapan, halaman Shorts, serta fitur scan barcode,
pencarian, dan menu — semuanya dalam tema gelap `#121212` dengan font **Raleway**.

---

## 🟢 Feed global: room.created + reel.new/deleted (2026-07-24, ronde 8)

Sisa event realtime dari poin 11 sudah beres — **cakupan realtime kini lengkap**.
Karena room & reel baru tidak terikat satu percakapan/pengguna, ada **dua topik
feed global** yang app langgan lewat frame `subscribe`:

| Topik | Kapan langgan | Event | Payload |
|---|---|---|---|
| `rooms:all` | tab Rooms terbuka | `room.created` | `{room_id,title,host_id,host_name,participant_count,visibility}` |
| `reels:all` | tab Shorts terbuka | `reel.new` | `{reel_id,author_id}` (ambil detail via `GET /reels/{id}`) |
| `reels:all` | tab Shorts terbuka | `reel.deleted` | `{reel_id}` |

```
{"type":"subscribe","data":{"topics":["rooms:all"]}}     // saat buka tab Rooms
{"type":"unsubscribe","data":{"topics":["rooms:all"]}}   // saat pindah tab
```

Dengan ini **polling 8-detik daftar room bisa dibuang**. Hanya konten **publik**
yang diumumkan (room `followers`/`invite_only` & reel `followers`/`private`
tidak) — jadi tak ada kebocoran visibilitas. **Tanpa migrasi** (murni Go, sudah
aktif di server yang jalan).

---

## 🌐 Peta realtime menyeluruh — supaya app "mengerti" (2026-07-24, ronde 7)

Arah proyek: **Syntra realtime end-to-end** — tiap layar ikut berubah sendiri
tanpa refresh, kecuali hal yang memang tidak masuk akal di-realtime-kan. Bagian
ini adalah **satu sumber kebenaran** cakupan realtime, biar app tahu persis mana
yang bisa diandalkan hidup dan mana yang masih perlu tarik-ulang.

### Cara kerja singkat (model langganan)

- Satu koneksi `wss://<host>/api/v1/ws`. Saat connect, app **otomatis** dilanggan
  ke topik pribadinya `user:<id>` (notifikasi, sinkron antar-perangkat).
- Topik lain **dilanggan manual** sesuai layar yang terbuka:
  `conversation:<id>` (chat dibuka), `room:<id>` (voice room), `reel:<id>`
  (menonton reel — counter like/komentar), dan **feed global** `rooms:all`
  (tab Rooms terbuka) & `reels:all` (tab Shorts terbuka). Lepas langganan feed
  global saat pindah tab.
- Saat reconnect, **tarik ulang** state layar yang terbuka (`GET .../messages`
  dll.) — Pub/Sub bersifat at-most-once, jadi yang lewat saat putus bisa hilang.

### ✅ Sudah live (andalkan, jangan polling)

| Topik | Event | Untuk |
|---|---|---|
| `conversation:<id>` | `message.new` | pesan masuk |
| `conversation:<id>` | `message.updated` | pesan diedit (ganti di tempat) |
| `conversation:<id>` | `message.deleted` | pesan dihapus (tandai "dihapus") |
| `conversation:<id>` | `message.reaction` | reaksi tambah/ganti/hapus |
| `conversation:<id>` | `typing` | indikator mengetik |
| `conversation:<id>` | `presence.update` | online/last-seen lawan bicara |
| `conversation:<id>` | `conversation.updated` | grup berubah (judul/avatar/anggota) |
| `conversation:<id>` | `call.incoming/answered/ended` | panggilan |
| `user:<id>` | `message.read` | ✓✓ sinkron antar-perangkat sendiri |
| `user:<id>` | `notification.new` | notifikasi (lonceng) |
| `user:<id>` | `user.updated` | nama/foto profil sinkron antar-perangkat sendiri |
| `user:<id>` | `story.new` | **BARU** — story baru dari yang di-follow/ajak chat; refresh story row via `GET /stories` |
| `room:<id>` | `room.ended/participants/speak_request/role_changed/join_decided/message` | voice room |
| `reel:<id>` | (counter like/komentar per reel yang ditonton) | interaksi reel |
| `rooms:all` | `room.created` | **BARU** — room **publik** baru muncul di Voice Hub; polling 8-detik bisa dibuang |
| `reels:all` | `reel.new`, `reel.deleted` | **BARU** — reel **publik** baru/terhapus di feed Shorts |

> **Cakupan realtime kini lengkap** untuk semua yang masuk akal di-realtime-kan.
> Room `followers`/`invite_only` dan reel `followers`/`private` sengaja TIDAK
> diumumkan ke feed global (mencegah bocor visibilitas) — keduanya tetap muncul
> lewat `GET /rooms` / `GET /reels` seperti biasa.

### 🚫 Sengaja TIDAK realtime (jangan tunggu event-nya)

- **Riwayat pesan lama** — ditarik via `GET .../messages` (realtime hanya untuk
  pesan sejak socket tersambung).
- **Pencarian** — difilter lokal.
- **Feed reel lama / daftar room saat pertama buka** — ditarik via `GET` sekali;
  realtime hanya untuk perubahan sesudahnya.
- **Chat di dalam voice room** — efemeral, disiarkan lalu hilang; tidak disimpan.

---

## 📡 Realtime hapus & reaksi pesan (2026-07-24, ronde 6)

Poin 11.1 & 11.2 di `pesan-untuk-backend.md`: hapus pesan & reaksi belum punya
siaran WS, jadi perangkat lawan bicara baru tahu setelah buka ulang chat.
Sekarang keduanya disiarkan ke topik `conversation:<id>`:

| Event | Payload | Aksi di app |
|---|---|---|
| `message.deleted` | `{ conversation_id, message_id }` | tandai pesan itu "pesan ini dihapus" seketika |
| `message.reaction` | `{ conversation_id, message_id, user_id, emoji }` | perbarui reaksi; `emoji` kosong = reaksi dihapus |

Tidak ada perubahan REST — endpoint `DELETE /messages/{id}` dan
`PUT /messages/{id}/reaction` tetap sama, hanya kini ikut menyiarkan event.

### 🔴 Migrasi tertunda — HARUS dijalankan berurutan

Info dari kalian: migrasi terakhir yang dijalankan di Supabase baru
**`22_sfu_webhook`**. Berarti berikut ini **belum jalan** — dan endpoint terkait
(edit pesan, starred, privasi presence, hapus media, plus siaran hapus/reaksi
baru) akan gagal sampai dijalankan di **Supabase → SQL Editor**, urut:

```
server/migrations/20260724000023_list_followers.sql
server/migrations/20260724000024_edit_message.sql
server/migrations/20260724000025_starred_messages.sql
server/migrations/20260724000026_presence_privacy.sql
server/migrations/20260724000027_delete_media.sql
server/migrations/20260724000028_realtime_delete_reaction.sql
server/migrations/20260724000029_story_audience.sql
```

> **Praktis:** semua di atas sudah digabung ke **satu berkas sekali-jalan** —
> `server/scripts/apply-pending-migrations.sql`. Tempel seluruh isinya ke
> Supabase SQL Editor lalu Run. Aman diulang (CREATE OR REPLACE / IF NOT EXISTS).

> Migrasi 29 (`story_audience`) dibutuhkan siaran **`story.new`**. Sampai
> dijalankan, story tetap tersimpan normal — hanya siaran realtime-nya yang
> belum jalan (app masih perlu refresh manual untuk melihat story baru).

> Khusus migrasi 28: ia mengubah `delete_message` & `react_to_message` agar
> mengembalikan `conversation_id`. Server yang sudah diperbarui **membutuhkannya**
> — sampai 28 dijalankan, `DELETE /messages/{id}` dan `PUT .../reaction` akan
> membalas `404`. Jadi jalankan 28 bersamaan dengan restart server.

---

## 🗑️ Hapus media — `DELETE /media/{id}` sekarang ADA (2026-07-24, ronde 5)

Poin 5 di `pesan-untuk-backend.md`: "foto lama tetap tertinggal di storage".
Sudah beres. Endpoint yang kalian **sudah panggil** (`deleteMedia(oldId)`) kini
bukan no-op lagi:

```
DELETE /api/v1/media/{id}     → 204   (pemilik saja)
```

Ia menghapus **baris metadata sekaligus berkasnya** dari object storage. Alur
ganti avatar yang kalian pakai sudah pas apa adanya — tidak perlu diubah:

```
upload avatar baru → PATCH /users/me {avatar_media_id: baru} → DELETE /media/{lama}
```

Yang perlu diketahui saat menyambungkan:

- Panggil `DELETE` **setelah** `PATCH /users/me` menunjuk avatar baru. Kalau id
  lama masih dipakai (masih jadi avatar, atau terlanjur dikirim sebagai pesan/
  story/reel), backend menolak dengan **`409 conflict`** alih-alih merusak yang
  menunjuknya — jadi urutannya penting.
- `403` = media bukan milikmu. `404` = sudah tidak ada — **perlakukan sebagai
  sudah bersih**, bukan error yang perlu ditampilkan.
- Kalau `DELETE` sesekali gagal (jaringan), tidak apa-apa: media yatim tetap
  dibersihkan otomatis backend setelah masa tenggang.

Butuh **migrasi `20260724000027_delete_media.sql`** dijalankan pemilik backend.
Kontrak lengkap di [`api.md`](api.md) §8 — `DELETE /api/v1/media/{id}`.

---

## 🔧 Keandalan panggilan (2026-07-24, ronde 4): webhook LiveKit

**Tidak ada yang perlu kalian ubah** — ini murni perbaikan sisi backend, tapi
menyelesaikan satu masalah yang pasti kalian temui saat menguji panggilan.

**Masalahnya:** kalau lawan bicara menutup aplikasi paksa, HP-nya mati, atau
jaringannya putus di tengah panggilan, `POST /calls/{id}/leave` tak pernah
terkirim. Akibatnya panggilan tersangkut `ongoing` selamanya — banner "sedang
menelepon" tak pernah hilang, dan `GET /conversations/{id}/call` terus
mengembalikan panggilan hantu.

**Perbaikannya:** LiveKit sekarang mengabari backend saat peserta benar-benar
terputus, dan backend menutup panggilannya otomatis lalu menyiarkan
`call.ended` (reason `disconnected`) ke `conversation:<id>`. Jadi:

- Dengarkan `call.ended` seperti biasa — kini ia juga muncul untuk putus koneksi
  tak terduga, bukan cuma saat orang menekan tombol "akhiri".
- Saat menerima `call.ended`, tutup layar panggilan & putuskan LiveKit. Itu
  cukup; tak perlu polling `GET .../call` untuk memastikan.

Butuh **migrasi `20260724000022_sfu_webhook.sql`** dijalankan pemilik backend,
plus konfigurasi webhook di dashboard LiveKit. Detail teknis di
[`api.md`](api.md) — `POST /api/v1/sfu/webhook`.

---

## 🆕 Gelombang fitur baru (2026-07-24, ronde 3): chat WA-style, panggilan, Shorts

Tiga kelompok fitur besar baru mendarat di backend. **Semuanya butuh migrasi
SQL dijalankan dulu** (lihat daftar di bawah); sampai itu, endpoint-nya membalas
`404`. Kontrak lengkap tiap endpoint ada di [`api.md`](api.md) — di sini
ringkasan supaya kalian tahu apa yang sekarang bisa disambung.

### 1. Chat & grup ala WhatsApp — `api.md` §5b

Dulu grup hanya bisa dibuat lalu diam. Sekarang lengkap:

- **Info & atur grup**: `GET`/`PATCH /conversations/{id}` (judul/avatar,
  khusus admin), `POST /conversations/{id}/leave` (keluar; pemilik keluar →
  kepemilikan pindah otomatis).
- **Anggota**: `GET`/`POST /conversations/{id}/members`, `DELETE`/`PATCH
  .../members/{user_id}` (tambah/keluarkan/ubah peran). Admin-only; ubah peran
  owner-only.
- **Reaksi emoji**: `PUT /messages/{id}/reaction` (emoji kosong = hapus),
  `GET /conversations/{id}/reactions?message_ids=a,b,c`.
- **Bisukan**: `PUT /conversations/{id}/mute`.
- **Lampiran media di pesan**: kirim `media_ids` (maks 10) di
  `POST /conversations/{id}/messages`. Balasan & riwayat kini punya
  `attachments` (daftar URL siap tampil).
- **Pesan sistem**: pesan `type: "system"` `sender_id: null` — anggota
  masuk/keluar dll. Tampilkan rata tengah, bukan gelembung.
- **Realtime baru**: event `conversation.updated` di topik `conversation:<id>`
  → muat ulang detail/anggota grup.

### 2. Telepon & video call — `api.md` §5c

Layar Calls kalian yang masih placeholder sekarang punya backend. Panggilan
memakai **LiveKit yang sama** dengan voice room (sfu_token → sambung ke SFU;
audio/video tak lewat backend).

- `POST /calls` (`{conversation_id, kind: "audio"|"video"}`) — mulai/gabung.
- `POST /calls/{id}/answer` · `/decline` · `/leave` (sertakan
  `?conversation_id=<id>` agar lawan bicara dapat siaran).
- `GET /conversations/{id}/call` — panggilan aktif untuk tombol "gabung".
- **Realtime**: `call.incoming` (berdering!), `call.answered`, `call.ended`
  di topik `conversation:<id>`.

### 3. Reels / Shorts — `api.md` §8b

Tab Shorts kalian sekarang bisa disambung ke data nyata. Feed kronologis
(cursor `before_at`+`before_id`), video vertikal.

- `GET /reels` (feed) · `POST /reels` (buat dari `media_id` video milik sendiri).
- `GET /reels/{id}` · `GET /reels/me` · `GET /reels/saved` ·
  `GET /users/{username}/reels`.
- Interaksi: `PUT`/`DELETE /reels/{id}/like`, `.../save`,
  `POST /reels/{id}/view` (aman dipanggil tiap tampil — di-dedup).
- Komentar: `GET`/`POST /reels/{id}/comments`,
  `DELETE /reels/{id}/comments/{comment_id}` (balasan 1 tingkat).

### ⚠️ Batas media baru — tolong validasi di aplikasi SEBELUM unggah

Supaya storage tidak meledak, konfirmasi media yang kelewat besar/panjang
**ditolak**: gambar 10MB, **video 100MB & maks 3 menit**, audio 20MB, voice
note 16MB. Periksa ukuran/durasi sebelum mengunggah agar tidak buang kuota
hanya untuk ditolak (`413`/`400`) di langkah konfirmasi.

### 🔴 Migrasi yang HARUS dijalankan pemilik backend (Supabase → SQL Editor)

```
server/migrations/20260724000014_chat_wa_features.sql
server/migrations/20260724000015_calls.sql
server/migrations/20260724000016_reels.sql
```

Jalankan berurutan. Sampai dijalankan, endpoint chat-grup/panggilan/reels
membalas `404`. Setelah itu semuanya langsung hidup tanpa perubahan aplikasi.

---

## ⚠️ SATU LANGKAH YANG MEMBLOKIR SEMUANYA (2026-07-24, ronde 2)

Kalian menguji lagi dan menemukan hapus-pesan, approve-follow, reset-raise-hand,
dan hapus-room "belum ada". **Semuanya sudah dibuat di backend** — handler-nya
siap. Yang kurang cuma satu: **fungsi SQL-nya belum dijalankan di Supabase.**

**Pemilik backend harus menjalankan migrasi ini di Supabase → SQL Editor:**

```
server/migrations/20260724000013_room_end_and_requests.sql
```

Sampai itu dijalankan, endpoint room/chat/follow yang baru akan membalas 404.
Setelah dijalankan, semuanya langsung hidup — tidak perlu perubahan di aplikasi.

### Path yang kalian pakai — semuanya SUDAH saya cocokkan

Kalian menguji path yang sedikit berbeda dari yang saya buat. Daripada meminta
kalian mengubah kode, saya tambahkan **alias** supaya path kalian jalan apa adanya:

| Yang kalian panggil | Status |
|---|---|
| `DELETE /conversations/{id}/messages/{message_id}` | ✅ ada (alias `DELETE /messages/{id}`) |
| `DELETE /rooms/{id}` | ✅ ada (alias `POST /rooms/{id}/end`) |
| `GET /users/me/follow-requests` | ✅ ada |
| `POST /users/{username}/follow/approve` | ✅ ada |
| `DELETE /rooms/{id}/raise-hand` | ✅ ada |
| `POST /rooms/{id}/invite` (skema body) | ✅ kini terdokumentasi di voice-rooms.md |

Jadi tidak ada yang perlu kalian ubah — cukup tunggu migrasi 13 dijalankan.

### Koreksi kalian soal host-exit — betul

Kalian benar: `POST /rooms/{id}/leave` oleh host memang sudah mengakhiri room.
Terima kasih sudah mengoreksi. `POST /rooms/{id}/end` (atau `DELETE /rooms/{id}`)
adalah tambahan untuk menutup room **tanpa** harus keluar dulu.

---

## ⚡ Balasan atas `pesan-untuk-backend.md` (2026-07-24)

Ketujuh poin sudah ditangani. **Tiga di antaranya ternyata sudah beres** sebelum
kalian menguji — pengujian dilakukan sebelum backend restart terakhir.

> **Wajib lebih dulu:** jalankan migrasi `20260724000013_room_end_and_requests`
> di Supabase SQL Editor. Handler-nya sudah siap, tetapi endpoint room/chat/follow
> yang baru akan membalas 404 sampai fungsi SQL-nya ada.

### 🔴 1. Akhiri room — **ADA sekarang**

```
POST /api/v1/rooms/{id}/end     → 204   (host saja)
```

Setelahnya: room hilang dari `GET /rooms`, seluruh peserta dikeluarkan, event
`room.ended` disiarkan, dan `GET /rooms/{id}/participants` membalas **404** —
persis yang kalian minta, app tinggal menangkap 404 itu.

`leave` juga sudah benar: kalau host yang keluar, room ikut berakhir. Room
terbengkalai (host hilang jaringan) ditutup otomatis tiap 5 menit. Room lama
sisa pengujian sudah dibersihkan.

### 🔴 2. Izin masuk room — **ADA**, untuk `visibility: "invite_only"`

```
POST /api/v1/rooms/{id}/join   → 202 { "data": { "status": "pending" } }
```

`sfu_token` **ditahan** sampai host menyetujui — persis kekhawatiran kalian soal
"tidak bisa dipalsukan dari app". Penahanan ada di server.

```
GET  /api/v1/rooms/{id}/requests                      daftar menunggu (host)
POST /api/v1/rooms/{id}/requests/{user_id}/approve    → 204
POST /api/v1/rooms/{id}/requests/{user_id}/reject     → 204
```

Yang disetujui **harus memanggil `join` lagi** untuk mendapat token. Event
`room.join_decided` disiarkan supaya app tahu kapan mencoba lagi — layar tahan
"menunggu izin" kalian tinggal mendengarkan itu.

Room `public` dan `followers` tetap langsung `status: "joined"`.

### 🟡 3. Turunkan angkat tangan — **dua cara**

- Otomatis: bendera turun sendiri saat peran naik jadi `speaker`. **Ini sudah
  jalan sejak sebelum kalian menguji** — coba periksa ulang.
- Manual: `DELETE /api/v1/rooms/{id}/raise-hand` → 204, untuk yang berubah pikiran.

### 🟡 4. `host_id` berupa JWT — **sudah UUID**

Sudah diverifikasi: `POST /rooms` mengembalikan `host_id` berupa UUID 36
karakter, dan `sub` pada `sfu_token` juga UUID. Akar masalahnya `AUTH_DEV_BYPASS`
yang menyalakan mode debug — sudah dimatikan. `max_participants` juga sudah 50,
bukan 0. **Tidak ada lagi kebocoran JWT di identity LiveKit.**

### 🟡 5. Hapus pesan — **ADA**

```
DELETE /api/v1/messages/{id}                 hapus satu pesan (pengirimnya saja)
DELETE /api/v1/conversations/{id}/messages   kosongkan riwayat (hanya bagimu)
```

Pesan yang dihapus tetap muncul di riwayat dengan `is_deleted: true` dan `body`
kosong — tampilkan "pesan ini dihapus". Yang kedua mengosongkan layar **hanya
untukmu**; peserta lain tetap melihat percakapan utuh, karena menghapus pesan
dari layar orang lain bukan wewenang siapa pun.

### 🟡 6. Story orang lain — **konfirmasi: bukan bug**

Benar dugaan kalian. `GET /stories` menampilkan story dari yang di-follow
(`accepted`) **dan** dari lawan bicara di chat. Untuk akun privat yang
menghasilkan `pending`, kini ada cara menyetujuinya:

```
GET  /api/v1/users/me/follow-requests
POST /api/v1/users/{username}/follow/approve   → 204
POST /api/v1/users/{username}/follow/reject    → 204
```

### Catatan kecil: register duplikat — **diperbaiki**

Betul, dulu balas 201. Supabase memang membalas 200 dengan `identities` kosong
untuk email terdaftar (anti-enumerasi), yang tampak seperti sukses. Backend kini
mendeteksinya dan membalas **409 conflict** — sudah diverifikasi. **Tidak ada
user duplikat yang terbentuk**; Supabase tidak pernah benar-benar membuatnya.

---

> **Status data:** di dalam aplikasi ini, seluruh data (chat, pesan, story) masih
> **dummy/in-memory** dan hilang saat aplikasi ditutup.
>
> **Backend-nya sudah ada dan siap disambungkan** — Go + Supabase + WebSocket,
> berada di repo yang sama. Yang tersisa adalah pekerjaan di sisi klien:
> mengganti sumber data in-memory dengan panggilan ke API.
>
> Mulai dari [`../docs/api.md`](../docs/api.md) — kontrak integrasi lengkap,
> berisi seluruh endpoint, frame WebSocket, dan urutan pemanggilan saat
> aplikasi dibuka. Pemetaan tiap layar di dokumen ini ke endpoint yang
> melayaninya ada di
> [`../docs/app-backend-alignment.md`](../docs/app-backend-alignment.md).
> Khusus voice room, baca [`../docs/voice-rooms.md`](../docs/voice-rooms.md) —
> ada satu hal di sana yang mengubah cara layar Rooms harus dibangun.

---

## Catatan penyambungan — hal yang mengubah asumsi

Empat hal yang perlu diketahui sebelum mulai mengganti sumber data. Semuanya
sudah tersedia di backend.

### 1. Voice room: suara TIDAK lewat backend

Backend tidak mengalirkan audio dan tidak akan pernah — audio realtime butuh
UDP/WebRTC, sedangkan WebSocket berjalan di atas TCP. Suara ditangani **LiveKit**;
backend hanya menerbitkan token yang menentukan siapa boleh masuk dan siapa
boleh bicara.

Artinya untuk layar Rooms, app perlu menambahkan dependensi:

```gradle
implementation "io.livekit:livekit-android:<versi>"
```

plus izin runtime `RECORD_AUDIO`. Alurnya:
`POST /rooms/{id}/join` → dapat `sfu_url` + `sfu_token` → `room.connect(...)`
→ suara terdengar.

Satu jebakan: **setelah peran naik jadi speaker, harus panggil `join` lagi.**
Token lama diterbitkan dengan `canPublish: false` dan tidak akan bisa menyalakan
mikrofon meski tombolnya sudah muncul.

Kalau `meta.sfu_ready` bernilai `false` di `GET /rooms`, media server belum
dikonfigurasi — sembunyikan tombol Join, karena bergabung tidak akan
menghasilkan suara.

### 2. Chat di dalam room bersifat efemeral

Berbeda total dari chat percakapan biasa. Pesan room **tidak pernah disimpan**:
disiarkan lewat WebSocket, lalu hilang. Begitu room berakhir, percakapannya
lenyap selamanya.

| | Chat percakapan | Chat voice room |
|---|---|---|
| Disimpan | ya | **tidak** |
| Riwayat | `GET .../messages` | tidak ada |
| Id pesan & `ack` | ya | tidak ada |
| Sinkronisasi saat reconnect | ya | tidak — yang lewat memang hilang |

Kirim `{"type":"room.chat","data":{"room_id":"...","body":"..."}}`, terima
`room.message`. Maksimum 500 karakter.

**Jangan meng-cache chat room ke Room/DataStore.** Ia dirancang hilang; kalau
disimpan, app akan menampilkan riwayat yang tidak dimiliki peserta lain.

### 3. Indikator ✓✓ sudah bisa digambar

`GET /conversations` mengembalikan `counterpart_last_read_id`. Karena id memakai
UUIDv7 yang terurut waktu, cukup bandingkan:

```kotlin
val sudahDibaca = pesan.id <= conversation.counterpartLastReadId
```

Tidak perlu tabel receipt, tidak perlu endpoint tambahan.

### 4. Follow sudah ada — dan story bergantung padanya

`POST` / `DELETE /users/{username}/follow`, dan `GET /users/me/following`.

Yang perlu diperhatikan: **`GET /stories` hanya menampilkan story dari orang
yang sudah diikuti dengan status `accepted`.** Kalau story row terlihat kosong
selain milik sendiri, itu bukan bug story — periksa daftar following dulu.

`GET /users/{username}` sekarang juga mengembalikan `follow_status`
(`""` / `pending` / `accepted`) untuk menentukan label tombol
Follow / Requested / Following.

> Akun privat menghasilkan status `pending`, dan **belum ada endpoint untuk
> menyetujuinya**. Untuk sementara pakai akun publik — kalau tidak, permintaan
> follow menggantung dan story tetap tidak muncul.

---

## Temuan dari log — hal yang perlu diperbaiki di aplikasi

Ditulis 2026-07-23 setelah membaca log backend saat aplikasi mencoba tersambung
dari perangkat `192.168.1.2` (okhttp/4.12.0).

### 1. Pesan tampil dua kali di layar pengirim — perlu dedup

**Gejala:** pengirim melihat pesannya dua kali, penerima hanya satu.

**Penyebab:** ini perilaku backend yang disengaja, bukan bug. Saat mengirim
lewat WebSocket, pengirim menerima **dua** frame:

1. `ack` — berisi `id` dan `created_at` final dari server
2. `message.new` — siaran ke seluruh pelanggan topik percakapan, **termasuk
   pengirim sendiri**

Siaran itu tidak bisa dilewatkan begitu saja: perangkat lain milik pengirim —
tablet, atau ponsel kedua — memang harus ikut menerimanya. Yang dikecualikan
seharusnya satu koneksi, bukan satu pengguna.

**Perbaikan di aplikasi:** kunci daftar pesan dengan `id`, bukan menambah
membabi buta.

```kotlin
// saat mengirim: tampilkan optimistik dengan id sementara
val tempId = "local-${System.currentTimeMillis()}"
messages.add(Message(id = tempId, body = text, pending = true))
ws.send("""{"type":"message.send","ref":"$tempId","data":{...}}""")

// saat "ack" tiba: GANTI yang sementara, jangan tambah
onAck { ref, data ->
    val i = messages.indexOfFirst { it.id == ref }
    if (i >= 0) messages[i] = messages[i].copy(id = data.id, pending = false)
}

// saat "message.new" tiba: abaikan kalau id-nya sudah ada
onMessageNew { msg ->
    if (messages.none { it.id == msg.id }) messages.add(msg)
}
```

Pemeriksaan `none { it.id == msg.id }` itu yang menghilangkan duplikatnya.
Sekaligus membuat aplikasi tahan terhadap frame yang terkirim ulang saat
jaringan tidak stabil.

### 2. `POST /auth/login` balas 401 tiga kali berturut-turut

Log menunjukkan tiga percobaan gagal dari perangkat yang sama dalam 25 detik.
Backend menolak dengan benar — kredensialnya memang tidak cocok.

Yang perlu diperiksa di aplikasi:

- Email/kata sandi yang dikirim benar. Akun uji ada di tabel bawah.
- **Body dikirim sebagai JSON**, bukan form-encoded:
  `Content-Type: application/json` dengan isi `{"email":"...","password":"..."}`
- Kalau memakai `@FormUrlEncoded` di Retrofit, backend akan gagal mem-parsenya.

Pesan `401` sengaja tidak membedakan "email tidak terdaftar" dari "kata sandi
salah" — membedakannya berarti memberi tahu penyerang akun mana yang ada.

### 3. `POST /auth/register` balas 429 dua kali

Bukan kesalahan aplikasi maupun backend. **Supabase membatasi jumlah
pendaftaran per jam** pada proyek free tier tanpa SMTP kustom.

Yang perlu ditangani di aplikasi: tampilkan pesan "coba lagi beberapa saat",
bukan "terjadi kesalahan". Kode errornya `rate_limited`, bukan `internal` —
jadi bisa dibedakan.

Jangan mengulang otomatis dengan cepat; itu justru memperpanjang masa blokir.

### 4. Kesalahan konfigurasi backend yang sempat menyesatkan

Dua gejala berikut **sudah diperbaiki di backend**, tetapi disebutkan agar
tidak salah didiagnosis kalau muncul lagi:

| Gejala | Penyebab | Status |
|---|---|---|
| `invalid input syntax for type uuid` saat `subscribe` | `AUTH_DEV_BYPASS=true` membuat `user_id` berisi seluruh JWT | ✅ bypass dimatikan; `DevVerifier` kini menolak JWT dengan pesan jelas |
| `participant identity length exceeds limits: max length 256` saat join room | akar yang sama — identity LiveKit berisi JWT, bukan UUID | ✅ ikut sembuh; ditambah pemeriksaan panjang di penerbit token |

Kalau salah satu muncul lagi, periksa `AUTH_DEV_BYPASS` di `.env` server lebih
dulu — bukan kode aplikasi.

### 5. Sepuluh laporan pengujian voice room & story — hasil pemilahan

Dari 10 laporan, **5 murni backend** (sudah diperbaiki), **4 murni aplikasi**,
**1 gabungan**.

#### ✅ Sudah diperbaiki di backend

| # | Laporan | Penyebab |
|---|---|---|
| 1a | Peserta masuk tanpa izin pemilik | `invite_only` diperlakukan sama dengan `public`. Kini ditegakkan lewat tabel `room_invites` + `POST /rooms/{id}/invite` |
| 1b | Room hantu masih ada | Host yang keluar tanpa memanggil `leave` (aplikasi tertutup, baterai habis) meninggalkan room `live` selamanya. Kini ada pembersih otomatis tiap 5 menit, dan data lama sudah dirapikan |
| 2b | Angkat tangan tidak muncul di sisi host | Permintaan tersimpan tetapi **tidak ada endpoint untuk membacanya** — UI host mustahil dibuat. Kini ada `GET /rooms/{id}/speak-requests` + siaran `room.speak_request` |
| 6 | Room terhapus tapi peserta masih bisa akses | `leave_room` hanya mengeluarkan si pemanggil; peserta lain tetap tercatat aktif. Kini seluruh peserta ikut dikeluarkan, dan `join` ke room `ended` ditolak `404` |
| 9 | Foto profil peserta tidak bisa ditampilkan | Backend mengirim `avatar_media_id`, dan klien tidak punya cara mengubah id jadi URL. Kini mengirim `avatar_url` siap pakai |
| 10a | Story orang lain tidak muncul | `list_stories` hanya menampilkan story dari yang **diikuti**, sementara data menunjukkan 0 baris follow. Kini juga menampilkan story dari **lawan bicara** — orang yang sudah berbagi percakapan |

Endpoint & event baru:

```
GET  /api/v1/rooms/{id}/speak-requests    daftar yang mengangkat tangan
POST /api/v1/rooms/{id}/invite            undang ke room invite_only
```

Event WebSocket baru di topik `room:<id>`:

| Event | Kapan | Yang harus dilakukan aplikasi |
|---|---|---|
| `room.ended` | host keluar / room ditutup | tutup layar room, putuskan LiveKit, tampilkan pesan |
| `room.participants` | ada perubahan peserta | ganti daftar peserta dengan isi payload |
| `room.speak_request` | seseorang angkat tangan | tampilkan notifikasi di UI host |
| `room.role_changed` | peran berubah | kalau `needs_rejoin: true`, **panggil `join` lagi** |

`room.role_changed` membawa `needs_rejoin`. Ini penting: token SFU lama
diterbitkan dengan `canPublish: false`, jadi peserta yang baru dipromosikan
**tidak akan bisa menyalakan mikrofon** sampai ia mengambil token baru — meski
tombolnya sudah muncul. Itu penyebab keluhan "lawan bicara tidak dapat
berbicara" pada laporan #2.

#### ⚠️ Perlu dikerjakan di aplikasi

| # | Laporan | Kenapa ini sisi aplikasi |
|---|---|---|
| 2a | "Kamu belum menjadi speaker" | Backend **benar** menolak: pendengar memang tidak boleh menyalakan mikrofon. Setelah host menyetujui, aplikasi harus memanggil `join` lagi untuk token baru |
| 3 | Ikon mikrofon kurang jelas | Murni tampilan. Status `is_muted` per peserta sudah dikirim backend |
| 4 | Speaker kurang nyaring, perlu kontrol volume | Volume diatur SDK LiveKit di perangkat, tidak melewati backend sama sekali |
| 5 | Perlu popup peringatan saat pemilik keluar | Murni tampilan. Backend tidak tahu dan tidak perlu tahu soal dialog |
| 7 | Room baru harus refresh dulu | `POST /rooms` **sudah mengembalikan data room lengkap**. Sisipkan langsung ke daftar, jangan menunggu muat ulang |
| 10b | Bar story abu-abu setelah ditonton | `viewed` per story dan `all_viewed` per orang sudah dikirim sejak awal |

#### 🔀 Gabungan

**#8 — pembuat room langsung masuk dengan mikrofon menyala.**

Sisi backend sudah dibereskan: `POST /rooms` kini **langsung memasukkan
pembuatnya sebagai host** dan mengembalikan token SFU dalam field `join`:

```json
{ "data": {
    "id": "...", "title": "...", "participant_count": 1,
    "join": {
      "role": "host", "can_publish": true,
      "sfu_url": "wss://...", "sfu_token": "eyJ..."
    }
} }
```

Aplikasi tidak perlu memanggil `/join` lagi — cukup `room.connect(sfu_url,
sfu_token)` lalu `setMicrophoneEnabled(true)`.

Soal "card paling top": `GET /rooms` sudah mengurutkan terbaru dulu, jadi room
yang baru dibuat memang berada di posisi teratas.

---

### 6. Hapus story — endpoint baru

Sebelumnya **tidak ada cara apa pun** menghapus story, foto maupun video.
Sekarang ada:

```
GET    /api/v1/stories/me      story sendiri (+ view_count, is_expired)
DELETE /api/v1/stories/{id}    hapus story sendiri
```

`GET /stories/me?include_expired=true` menyertakan yang sudah lewat 24 jam —
untuk layar arsip.

Yang perlu diketahui saat membangun UI-nya:

- **Konfirmasi sebelum menghapus.** Tidak ada pembatalan; begitu terhapus,
  story hilang dari layar semua orang.
- **`view_count` hanya ada di `/stories/me`**, bukan di `GET /stories`. Jumlah
  penonton adalah informasi pemilik, bukan informasi publik.
- Setelah `204`, buang story itu dari daftar lokal — tidak perlu memuat ulang.
- **Medianya tidak ikut terhapus.** Kalau UI menawarkan "hapus permanen
  termasuk berkasnya", itu belum ada di backend — beri tahu kalau memang
  dibutuhkan.

### 7. Alamat backend berubah saat ganti Wi-Fi

Base URL menunjuk alamat laptop di jaringan lokal, jadi ia **berubah setiap
kali laptop pindah Wi-Fi**.

```
sebelumnya : http://192.168.1.174:8081
sekarang   : http://192.168.1.6:8081
```

Simpan di `local.properties` lalu baca lewat `BuildConfig`, supaya cukup ganti
satu baris tanpa menyentuh kode.

Cara yang lebih tahan lama: pakai alamat Tailscale **`100.77.80.61`** — tidak
pernah berubah meski pindah jaringan, asalkan ponsel juga login Tailscale.

---

### Akun uji yang sudah tersedia

| Email | Password | Username |
|---|---|---|
| `admin@syntra.app` | `admin123` | admin |
| `budi@syntra.app` | `budi123456` | budi |
| `citra@syntra.app` | `citra123456` | citra |

Login lewat Supabase Auth (bukan ke backend ini) untuk memperoleh JWT — caranya
ada di [`../docs/api.md`](../docs/api.md) §1.

---

## Daftar Isi

- [Teknologi](#teknologi)
- [Struktur Proyek](#struktur-proyek)
- [Cara Menjalankan](#cara-menjalankan)
- [Arsitektur & Navigasi](#arsitektur--navigasi)
- [Alur Aplikasi per Layar](#alur-aplikasi-per-layar)
  - [1. Layar Chat (daftar percakapan)](#1-layar-chat-daftar-percakapan)
  - [2. Story / Status Viewer](#2-story--status-viewer)
  - [3. Menambah Story (foto/video)](#3-menambah-story-fotovideo)
  - [4. Layar Percakapan (Chat Detail)](#4-layar-percakapan-chat-detail)
  - [5. Layar Shorts](#5-layar-shorts)
  - [6. Scan Barcode / QR](#6-scan-barcode--qr)
  - [7. Pencarian](#7-pencarian)
  - [8. Menu Titik-Tiga](#8-menu-titik-tiga)
- [Tema, Warna & Font](#tema-warna--font)
- [Model Data](#model-data)
- [Izin & Ketergantungan Runtime](#izin--ketergantungan-runtime)
- [Keterbatasan & Ide Pengembangan](#keterbatasan--ide-pengembangan)

---

## Teknologi

| Bagian | Detail |
|--------|--------|
| Bahasa | Kotlin |
| UI | Jetpack Compose (Material 3) |
| Min SDK | 26 (Android 8.0) |
| Target SDK | 37 |
| Font | Raleway (variable font, bundel lokal) |
| Ikon | `material-icons-extended` |
| Scanner | Google Code Scanner (`play-services-code-scanner`) |
| Media | `VideoView` (video story), Android Photo Picker |

---

## Struktur Proyek

```
app/src/main/java/com/example/syntra/
├── MainActivity.kt        # Entry point; host tab (Chat/Shorts) + state navigasi bawah
├── ChatScreen.kt          # Layar Chat: header, story row, daftar chat, story viewer, add-story, search, scan, menu
├── ChatDetailScreen.kt    # Layar percakapan (bubble chat + input bar)
├── ShortsScreen.kt        # Layar Shorts (video vertikal ala TikTok/Reels)
├── NexusNav.kt            # Bottom navigation bar bersama (enum NexusTab + NexusBottomBar)
└── ui/theme/
    ├── Color.kt           # Palet warna Syntra (background #121212, aksen biru, dll.)
    ├── Theme.kt           # SyntraTheme: skema warna gelap + Raleway sebagai font default
    └── Type.kt            # Definisi FontFamily Raleway + Typography Material 3

app/src/main/res/
├── font/raleway_variable.ttf     # Font Raleway (variable weight)
├── drawable/story_*.jpg          # Foto placeholder untuk story bawaan
├── values/colors.xml             # syntra_background = #121212
└── values/themes.xml             # Theme.Syntra (windowBackground #121212, status/nav bar transparan)
```

---

## Cara Menjalankan

1. Buka proyek di **Android Studio** (versi terbaru).
2. Jalankan **Sync Gradle** (mengunduh dependency, termasuk scanner & ikon).
3. Pilih perangkat/emulator. Untuk fitur **scan barcode**, gunakan emulator/HP yang
   memiliki **Google Play Services** (mis. image emulator "Google Play").
4. Tekan **Run** ▶.

> Preview Compose tersedia (`@Preview`) di `ChatScreen.kt` & `ShortsScreen.kt`
> untuk melihat UI tanpa menjalankan emulator penuh.

---

## Arsitektur & Navigasi

Aplikasi menggunakan **satu Activity** (`MainActivity`) dan navigasi berbasis **state**
(bukan Navigation Component). Perpindahan antar layar dilakukan dengan menampilkan/menyembunyikan
composable secara kondisional.

```
MainActivity
 └── SyntraTheme
      └── NexusApp                     // menyimpan tab terpilih (NexusTab)
           ├── ChatScreen              // saat tab == CHAT / ROOMS / CALLS
           │    ├── overlay StoryViewer      // saat sebuah story diklik
           │    └── overlay ChatDetailScreen // saat sebuah chat diklik
           └── ShortsScreen            // saat tab == SHORTS
```

- **Bottom Navigation** (`NexusBottomBar`) punya 4 tab: **Chat, Shorts, Rooms, Calls**.
  Chat & Shorts sudah berisi layar; Rooms & Calls sementara menampilkan layar Chat (placeholder).
- **Story Viewer** dan **Chat Detail** ditampilkan sebagai **overlay layar penuh** di atas
  `ChatScreen`, dan ditutup dengan tombol Back perangkat / gestur / tombol close.

---

## Alur Aplikasi per Layar

### 1. Layar Chat (daftar percakapan)

Layar utama saat aplikasi dibuka (`ChatScreen.kt`).

**Susunan dari atas ke bawah:**
- **Header** — ikon **scan** (kiri), judul **"Syntra"**, ikon **search** & **titik-tiga** (kanan).
- **Story row** (`ActiveRow`) — deretan avatar bundar berisi foto. Ring di sekeliling avatar
  berupa **segmen garis** yang jumlahnya = jumlah story yang di-post orang tersebut.
- **Daftar percakapan** (`ConversationRow`) — tiap baris: avatar, nama, cuplikan pesan,
  waktu, badge jumlah pesan belum dibaca, indikator status (online = titik hijau,
  typing = teks miring biru, terkirim = ✓✓).
- **Tombol + mengambang** (kanan bawah) — untuk menambah story.
- **Bottom navigation**.

**Interaksi:**
- Ketuk sebuah **story** → membuka [Story Viewer](#2-story--status-viewer).
- Ketuk sebuah **chat** → membuka [Chat Detail](#4-layar-percakapan-chat-detail).
- Ketuk **+** → membuka [alur tambah story](#3-menambah-story-fotovideo).
- Ketuk **scan / search / titik-tiga** → lihat bagian masing-masing di bawah.

### 2. Story / Status Viewer

Composable `StoryViewer` — pengalaman menonton story layar penuh ala **Status WhatsApp**.

**Fitur & alur:**
- **Progress bar segmen** di atas — satu bar per story milik orang tersebut.
- **Auto-advance:**
  - Story **foto**: berpindah otomatis setelah **5 detik**.
  - Story **video**: **diputar sampai selesai** dulu (progress bar mengikuti posisi
    pemutaran video nyata) baru berpindah.
- **Navigasi manual:** ketuk sisi **kanan** = story berikutnya, sisi **kiri** = sebelumnya.
  Setelah story terakhir orang terakhir → viewer tertutup.
- **Perpindahan otomatis antar-orang** (Lena → Marcus → …), persis WhatsApp.
- **Swipe ke atas untuk menutup** — konten mengalami **transisi besar → kecil**
  (mengecil, memudar, sudut membulat). Jika tarikan melewati ambang → lanjut menyusut lalu
  tertutup; jika belum → memantul kembali (spring). Ada juga animasi **fade + scale-up** saat
  viewer pertama kali dibuka.
- **Tanda "sudah ditonton" (seen):** setelah semua story seseorang selesai ditonton, ring
  berwarna pada avatarnya di daftar **hilang** dan diganti garis abu tipis (seperti WhatsApp/Instagram).
- Tombol **Back** perangkat & tombol **✕** juga menutup viewer.

### 3. Menambah Story (foto/video)

Alur dari tombol **+** di layar Chat:

1. Membuka **Android Photo Picker** (`PickVisualMedia` mode `ImageAndVideo`) — tanpa perlu
   izin runtime.
2. Pengguna memilih **foto atau video**:
   - **Foto** → di-decode menjadi bitmap (`StoryImage.Bitmap`).
   - **Video** → frame pertama diekstrak sebagai thumbnail (`MediaMetadataRetriever`),
     disimpan sebagai `StoryImage.Video(uri, thumbnail)`.
3. Story baru **ditambahkan di depan** story row dengan label **"Your story"** dan bisa
   langsung diklik untuk ditonton (video akan diputar via `VideoView`).

### 4. Layar Percakapan (Chat Detail)

Composable `ChatDetailScreen` — dibuka saat sebuah chat diklik.

**Susunan:**
- **Top bar ringkas** — avatar + nama + status (`online` / `typing…` / `last seen recently`),
  lalu ikon **video call, call, titik-tiga**. Nama panjang otomatis dipotong dengan **ellipsis**
  (mis. `reza ramadhan start` → `reza ramadhan…`).
- **Daftar pesan** — gelembung chat: pesan masuk rata kiri (abu), pesan sendiri rata kanan
  (biru), masing-masing berjam. Ada chip tanggal **"Today"**.
- **Input bar** — kolom teks (placeholder "Message") dengan ikon emoji/lampiran/kamera, dan
  tombol bulat yang berubah **mic ↔ kirim** tergantung ada tidaknya teks.

**Interaksi:**
- Mengetik lalu **kirim** → pesan baru ditambahkan dan daftar **auto-scroll** ke bawah.
- Keyboard mendorong input ke atas (`imePadding`).
- Tombol **Back** perangkat kembali ke daftar chat.

### 5. Layar Shorts

Composable `ShortsScreen` — tampilan video vertikal ala TikTok/Reels (saat ini area video
masih placeholder).

**Elemen:**
- Header "Nexus" + ikon search + avatar.
- Caption: username (`@quantum_flow`), tombol **Follow**, deskripsi, dan baris audio.
- **Action rail** kanan: like (❤️ 24.5k), komentar (💬 842), share, dan thumbnail audio.
- Bottom navigation (tab Shorts aktif).

### 6. Scan Barcode / QR

Ikon **scan** di kiri-atas header Chat.

- Menggunakan **Google Code Scanner** (`GmsBarcodeScanning`) — membuka UI scanner penuh
  dari Google, **tanpa** menangani izin/kamera manual.
- Hasil scan (`barcode.rawValue`) ditampilkan lewat **Toast**.
- Bila gagal (mis. tanpa Play Services) → Toast error.

### 7. Pencarian

Ikon **search** di header Chat.

- Header berubah menjadi **kolom pencarian** dengan **auto-fokus** (keyboard langsung muncul).
- Daftar chat **terfilter secara langsung** berdasarkan **nama** atau **isi pesan**
  (case-insensitive). Story row disembunyikan selama mencari.
- Jika tidak ada hasil → teks **"No conversations found"**.
- Ikon **✕** mengosongkan teks; **panah kembali** atau tombol **Back** perangkat keluar dari
  mode pencarian.

### 8. Menu Titik-Tiga

Ikon **titik-tiga** di header Chat.

- Membuka **DropdownMenu** dengan opsi: **New group**, **Starred messages**, **Settings**.
- Saat ini setiap opsi memunculkan **Toast** (placeholder) — siap disambungkan ke aksi nyata.

---

## Tema, Warna & Font

- **Tema gelap tetap** (dynamic color dimatikan) agar tampilan konsisten.
- **Background utama `#121212`** diterapkan di semua layer, termasuk `windowBackground`
  di `themes.xml` supaya tidak ada kedip putih saat aplikasi dibuka.
- Palet kunci (`Color.kt`):
  - `NexusBackground` `#121212` — latar utama
  - `NexusSurface` / `NexusSurfaceElevated` — permukaan/kartu & gelembung pesan masuk
  - `NexusAccent` `#3B68F5` / `NexusAccentSoft` — aksen biru (tombol, timestamp, bubble keluar)
  - `NexusRing` — ring story
  - `NexusOnline` — titik status online
- **Font Raleway** dipasang sebagai satu *variable font* dan dijadikan **default seluruh teks**
  melalui `Typography` + `LocalTextStyle` di `SyntraTheme`.

---

## Model Data

Semua data didefinisikan sebagai `data class` in-memory:

- **`Conversation`** — `name`, `message`, `time`, `gradient`, `unread`, `presence`, `sent`.
- **`Presence`** — enum `NONE` / `ONLINE` / `TYPING`.
- **`ActivePerson`** (story) — `name`, `photo: StoryImage`, `posts` (jumlah story).
- **`StoryImage`** (sealed) — `Res` (drawable bawaan), `Bitmap` (foto galeri), `Video` (uri + thumbnail).
- **`Message`** (di Chat Detail) — `text`, `fromMe`, `time`.

Daftar awal (`conversations`, `defaultActivePeople`) berisi contoh statis; story tambahan dan
pesan baru disimpan di `mutableStateList`/`mutableStateOf` selama sesi berjalan.

---

## Izin & Ketergantungan Runtime

- **Tidak butuh izin kamera manual** untuk scan (ditangani Google Code Scanner) maupun untuk
  memilih media (Android Photo Picker).
- **Google Play Services** diperlukan agar scan barcode berfungsi. Modul scanner di-*prefetch*
  lewat `meta-data com.google.mlkit.vision.DEPENDENCIES = barcode_ui` di `AndroidManifest.xml`.
- `android:windowSoftInputMode="adjustResize"` agar input chat & search terdorong keyboard.

---

## Keterbatasan & Ide Pengembangan

Keterbatasan saat ini (karena fokus pada UI/UX):

- Semua data **dummy & tidak persisten** (hilang setelah app ditutup).
- Story tambahan, status "seen", dan pesan baru hanya bertahan **selama sesi**.
- Tab **Rooms** sudah punya layarnya sendiri (Voice Hub), tetapi masih data
  statis dan tombol Join belum aktif. **Backend-nya kini sudah siap** — lihat
  catatan penyambungan §1 di atas dan
  [`../docs/voice-rooms.md`](../docs/voice-rooms.md). Tab **Calls** masih
  placeholder di kedua sisi.
- Area video **Shorts** masih placeholder; item menu titik-tiga masih Toast.
- Durasi progress bar video mengikuti durasi pemutaran, tetapi story foto tetap 5 detik.

Ide pengembangan lanjutan:

- **Menyambungkan ke backend Syntra** — bukan Firebase. Backend-nya sudah ada:
  - **Supabase Auth SDK** untuk login → menghasilkan JWT
  - **OkHttp/Ktor** ke `https://<host>/api/v1/...` untuk REST
  - **OkHttp WebSocket** ke `wss://<host>/api/v1/ws` untuk realtime
  - Satu-satunya bagian Google yang tetap relevan adalah **FCM** untuk push
    notification saat aplikasi tertutup, dan itu belum dirancang di kedua sisi
- Penyimpanan lokal (Room/DataStore) sebagai cache offline. Ini tetap
  dibutuhkan meski backend sudah ada — daftar chat harus tampil seketika saat
  aplikasi dibuka, sebelum jaringan menjawab.
- Navigation Component + multi-module untuk skala lebih besar.
- Implementasi nyata Shorts (ExoPlayer), Rooms, Calls, dan aksi menu — ketiganya
  juga belum ada di backend, jadi bisa dikerjakan berbarengan.
