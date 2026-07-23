# Syntra API — Kontrak Integrasi

Dokumen rujukan untuk siapa pun yang menyambungkan klien ke backend Syntra.
Berisi seluruh endpoint REST, seluruh frame WebSocket, alur multi-langkah, dan
bentuk data persisnya.

Kalau dokumen ini berbeda dengan kode, **kode yang benar** — dan itu bug di
dokumen ini yang harus diperbaiki.

> **Untuk yang mengubah backend:** setiap penambahan, perubahan, atau
> penghapusan endpoint **wajib ikut mengubah dokumen ini** — tabel ringkasan
> rute di §3 *dan* bagian detail endpointnya. Aplikasi membangun kliennya dari
> sini; endpoint yang tidak tercatat sama saja dengan tidak ada.
>
> Verifikasi dengan dua skrip sebelum commit:
> `server/scripts/check-docs.ps1` (dokumen vs kode) dan
> `server/scripts/smoke.ps1` (endpoint benar-benar jalan).

- Base URL lokal: `http://localhost:8081` (lewat nginx) atau `:8080` (langsung)
- Semua path REST berawalan `/api/v1`
- Semua timestamp **RFC3339 UTC**, contoh `2026-07-23T09:12:04Z`
- Semua id **UUID v7** — terurut secara waktu, jadi bisa dipakai sebagai cursor

---

## 1. Autentikasi

Token adalah **JWT dari Supabase Auth**, bukan token terbitan server ini.

```
Klien login via Supabase SDK   →  JWT
        ↓
Authorization: Bearer <jwt>    →  backend Syntra
        ↓
verifikasi ke /auth/v1/user    →  Supabase   (hasil di-cache 1 menit)
        ↓
JWT diteruskan ke tiap query   →  auth.uid() terisi  →  RLS lolos
```

Kalau JWT tidak ikut terkirim, `auth.uid()` bernilai NULL dan **semua query
mengembalikan nol baris tanpa error yang jelas.** Itu penyebab nomor satu dari
gejala "kodenya benar tapi datanya kosong".

### Cara mengirim token

| Cara | Berlaku untuk |
|---|---|
| `Authorization: Bearer <jwt>` | REST dan WebSocket — cara utama |
| `?token=<jwt>` | **hanya** `/api/v1/ws`, karena WebSocket API di browser tidak bisa menyetel header |
| `X-Debug-User: <user-id>` | hanya kalau `AUTH_DEV_BYPASS=true` dan `APP_ENV` bukan production |

### Endpoint auth

**Pakai endpoint backend ini, jangan memanggil Supabase langsung.** Alasannya
bukan kerapian: Supabase Auth hanya membuat baris di `auth.users`, sedangkan
tabel aplikasi (`users`, `user_profiles`, `user_settings`) tidak ikut terisi.
Akun hasil pendaftaran langsung ke Supabase **bisa login tetapi tidak punya
profil** — setiap query mengembalikan kosong tanpa pesan yang menjelaskan.

Ini bukan kemungkinan teoretis; sempat terjadi 4 baris `auth.users` berbanding
3 `public.users` di proyek ini.

#### `POST /api/v1/auth/register`

```json
{
  "email": "budi@syntra.app",
  "password": "rahasia123",
  "username": "budi",
  "display_name": "Budi Santoso",
  "date_of_birth": "1998-05-12"
}
```

Hanya `email` dan `password` yang wajib. Username kosong akan diturunkan dari
email, dan bentrok diselesaikan dengan sufiks angka — pendaftaran tidak pernah
gagal karena nama sudah dipakai.

Balasan `201` berisi sesi yang langsung bisa dipakai (lihat bentuk di bawah).

Kegagalan: `409` email sudah terdaftar · `400` kata sandi kurang dari 6
karakter atau email tidak valid · **`429` batas pendaftaran Supabase** — free
tier tanpa SMTP kustom hanya mengizinkan beberapa signup per jam.

#### `POST /api/v1/auth/login`

```json
{ "email": "budi@syntra.app", "password": "budi123456" }
```

```json
{ "data": {
    "access_token": "eyJhbGciOi...",
    "refresh_token": "dxh2wralne4u",
    "token_type": "bearer",
    "expires_in": 3600,
    "user_id": "6f77d0ad-...",
    "email": "budi@syntra.app",
    "username": "budi",
    "display_name": "Budi Santoso"
} }
```

Login juga menjadi jaring pengaman: kalau akun ternyata belum punya profil, ia
dibuat di sini.

Kegagalan: `401` email atau kata sandi salah. Pesan sengaja tidak membedakan
keduanya — memberi tahu mana yang salah berarti memberi tahu penyerang akun
mana yang benar-benar ada.

#### `POST /api/v1/auth/refresh`

```json
{ "refresh_token": "dxh2wralne4u" }
```

Access token berumur **1 jam**. Panggil endpoint ini saat menerima `401`, lalu
ulangi permintaan yang gagal. `401` di sini berarti pengguna harus login ulang.

#### `POST /api/v1/auth/logout`

Butuh `Authorization: Bearer <token>`. Balasan `204`, dan tetap `204` meski
token sudah kedaluwarsa — dari sudut pandang pengguna, "keluar" tidak boleh
bisa gagal.

#### `pending_confirmation`

Kalau proyek Supabase mewajibkan konfirmasi email, `register` membalas dengan
`access_token` kosong dan `pending_confirmation: true`. Akunnya sudah dibuat;
arahkan pengguna ke kotak masuknya, dan profil akan terbentuk saat login pertama.

---

## 2. Bentuk response

Semua endpoint REST memakai pembungkus yang sama.

```jsonc
{ "data": { } }                                    // sukses
{ "data": [ ], "meta": { "count": 12 } }           // sukses berpaginasi
{ "error": { "code": "forbidden",
             "message": "...",
             "request_id": "019f8e5a-..." } }      // gagal
```

`request_id` juga dikirim sebagai header `X-Request-ID` dan muncul di setiap
baris log server. Sertakan nilai itu saat melaporkan masalah.

### Kode error

Identik antara REST dan WebSocket, sehingga klien cukup punya **satu**
penerjemah error untuk kedua transport.

| Kode | HTTP | Arti | Yang sebaiknya dilakukan klien |
|---|---|---|---|
| `bad_request` | 400 | payload/parameter salah | perbaiki permintaan; jangan diulang apa adanya |
| `unauthorized` | 401 | token tidak ada/kedaluwarsa | refresh token lalu ulangi |
| `forbidden` | 403 | tidak berhak | jangan diulang |
| `not_found` | 404 | tidak ada | jangan diulang |
| `conflict` | 409 | bentrok status | belum dipakai |
| `payload_too_large` | 413 | body > 1 MB | perkecil |
| `rate_limited` | 429 | terlalu sering | mundur eksponensial |
| `internal` | 500 | kesalahan server | ulangi dengan backoff |
| `unknown_type` | — | khusus WS: tipe frame tak dikenal | bug klien |

---

## 3. Ringkasan seluruh rute

Seluruh baris di tabel ini **diverifikasi jalan** lewat `server/scripts/smoke.ps1`.

| Method | Path | Auth |
|---|---|---|
| `GET` | `/healthz` | — |
| `GET` | `/readyz` | — |
| `POST` | `/api/v1/auth/register` | — |
| `POST` | `/api/v1/auth/login` | — |
| `POST` | `/api/v1/auth/refresh` | — |
| `POST` | `/api/v1/auth/logout` | ✅ |
| `GET` | `/api/v1/conversations` | ✅ |
| `POST` | `/api/v1/conversations` | ✅ |
| `GET` | `/api/v1/conversations/{id}/messages` | ✅ |
| `POST` | `/api/v1/conversations/{id}/messages` | ✅ |
| `DELETE` | `/api/v1/conversations/{id}/messages` | ✅ |
| `DELETE` | `/api/v1/messages/{id}` | ✅ |
| `GET` | `/api/v1/stories` | ✅ |
| `POST` | `/api/v1/stories` | ✅ |
| `GET` | `/api/v1/stories/me` | ✅ |
| `POST` | `/api/v1/stories/{id}/view` | ✅ |
| `GET` | `/api/v1/stories/{id}/viewers` | ✅ |
| `DELETE` | `/api/v1/stories/{id}` | ✅ |
| `GET` | `/api/v1/users/{username}` | ✅ |
| `POST` | `/api/v1/users/{username}/follow` | ✅ |
| `DELETE` | `/api/v1/users/{username}/follow` | ✅ |
| `GET` | `/api/v1/users/me/following` | ✅ |
| `GET` | `/api/v1/users/me/follow-requests` | ✅ |
| `POST` | `/api/v1/users/{username}/follow/approve` | ✅ |
| `POST` | `/api/v1/users/{username}/follow/reject` | ✅ |
| `GET` | `/api/v1/rooms` | ✅ |
| `POST` | `/api/v1/rooms` | ✅ |
| `POST` | `/api/v1/rooms/{id}/join` | ✅ |
| `POST` | `/api/v1/rooms/{id}/leave` | ✅ |
| `POST` | `/api/v1/rooms/{id}/end` | ✅ |
| `GET` | `/api/v1/rooms/{id}/requests` | ✅ |
| `POST` | `/api/v1/rooms/{id}/requests/{user_id}/approve` | ✅ |
| `POST` | `/api/v1/rooms/{id}/requests/{user_id}/reject` | ✅ |
| `GET` | `/api/v1/rooms/{id}/participants` | ✅ |
| `PATCH` | `/api/v1/rooms/{id}/participants` | ✅ |
| `POST` | `/api/v1/rooms/{id}/raise-hand` | ✅ |
| `DELETE` | `/api/v1/rooms/{id}/raise-hand` | ✅ |
| `GET` | `/api/v1/rooms/{id}/speak-requests` | ✅ |
| `POST` | `/api/v1/rooms/{id}/invite` | ✅ |
| `PATCH` | `/api/v1/rooms/{id}/mute` | ✅ |
| `GET` | `/api/v1/notifications` | ✅ |
| `GET` | `/api/v1/notifications/unread-count` | ✅ |
| `POST` | `/api/v1/notifications/read` | ✅ |
| `POST` | `/api/v1/media/upload-url` | ✅ |
| `POST` | `/api/v1/media/{id}/confirm` | ✅ |
| `GET` | `/api/v1/ws` | ✅ |

Sumbernya: [`server/internal/transport/rest/router.go`](../server/internal/transport/rest/router.go).

---

## 4. Kesehatan

### `GET /healthz`

Tidak menyentuh dependensi apa pun. Selalu 200 selama proses hidup.

```json
{ "status": "ok", "version": "dev" }
```

### `GET /readyz`

Memeriksa Supabase dan Redis, batas 3 detik. `200` kalau sehat, `503` kalau tidak.

```json
{ "ready": true, "version": "dev",
  "dependencies": { "supabase": "ok", "redis": "ok" } }
```

---

## 5. Percakapan

### `GET /api/v1/conversations`

Daftar chat, terbaru dulu. Inilah sumber data layar utama.

| Query | Default | Catatan |
|---|---|---|
| `limit` | 30 | maksimum 100 |
| `before` | sekarang | RFC3339; ambil dari `meta.next_before` |

```json
{
  "data": [{
    "id": "019f8e5a-...",
    "type": "direct",
    "title": "Reza Ramadhan",
    "avatar_media_id": "019f8e12-...",
    "counterpart_id": "4e127292-...",
    "counterpart_last_read_id": "019f8e5a-...",
    "unread_count": 3,
    "last_message_preview": "oke besok ya",
    "last_message_type": "text",
    "last_message_sender_id": "4e127292-...",
    "last_message_at": "2026-07-23T09:12:04Z",
    "created_at": "2026-07-20T02:00:00Z"
  }],
  "meta": { "count": 1, "next_before": "2026-07-23T09:12:04Z" }
}
```

Catatan penting untuk klien:

- **`title` sudah berisi nama lawan bicara** untuk `type: "direct"` — klien
  tidak perlu mencari siapa peserta lain. Untuk grup berisi judul grup.
- **`counterpart_id`** hanya ada pada `direct`. Pakai ini untuk kueri presence
  dan untuk membuka profil.
- **`last_message_type`** berguna saat `last_message_preview` kosong: media
  tidak punya teks, jadi tampilkan "📷 Foto" berdasarkan tipe.
- **`counterpart_last_read_id`** adalah bahan indikator ✓✓ untuk chat `direct`.
  Karena id memakai UUIDv7 yang terurut waktu, klien cukup membandingkan:
  `pesan.id <= counterpart_last_read_id` berarti **sudah dibaca**. Tidak perlu
  tabel receipt per pesan — untuk grup 200 orang, satu pesan akan berarti 200
  baris receipt.
- Pagination memakai cursor waktu, bukan `offset`. Daftar ini berubah urutan
  setiap ada pesan masuk, dan `offset` akan melewatkan atau menggandakan baris.

### `POST /api/v1/conversations`

Memulai percakapan. **Untuk `direct`, operasi ini idempoten** — memanggilnya
lagi dengan orang yang sama mengembalikan percakapan yang sudah ada. Klien
boleh memanggilnya setiap kali pengguna menekan "kirim pesan" tanpa memeriksa
dulu.

```jsonc
// chat pribadi
{ "type": "direct", "user_id": "4e127292-..." }

// grup
{ "type": "group", "title": "Tim Syntra",
  "member_ids": ["4e127292-...", "0192f3a1-..."] }
```

```json
{ "data": { "id": "019f8e5a-..." } }
```

`403 forbidden` kalau salah satu pihak memblokir yang lain.

### `GET /api/v1/conversations/{id}/messages`

Riwayat pesan, **terbaru dulu**.

| Query | Default | Catatan |
|---|---|---|
| `limit` | 50 | maksimum 100 |
| `before` | — | **id pesan**, bukan waktu; ambil dari `meta.next_before` |

```json
{
  "data": [{
    "id": "019f8e5a-...",
    "conversation_id": "019f8e12-...",
    "sender_id": "4e127292-...",
    "type": "text",
    "body": "halo",
    "reply_to_id": null,
    "created_at": "2026-07-23T09:12:04Z",
    "edited_at": null,
    "is_deleted": false
  }],
  "meta": { "count": 1, "next_before": "019f8e5a-..." }
}
```

Cursor memakai **id**, bukan `created_at`. Dua pesan bisa punya waktu yang
identik di percakapan ramai, dan cursor waktu akan melewatkan salah satunya.

`is_deleted: true` berarti `body` sengaja dikosongkan — tampilkan "pesan ini
dihapus" di posisi itu, jangan sembunyikan barisnya.

**Endpoint ini juga alat sinkronisasi ulang.** Redis Pub/Sub bersifat
at-most-once: kalau ada instance server restart, event yang lewat saat itu
hilang. Karena itu setiap kali socket terhubung kembali, klien wajib memanggil
endpoint ini untuk percakapan yang sedang terbuka.

### `POST /api/v1/conversations/{id}/messages`

Jalur cadangan mengirim pesan saat WebSocket sedang terputus.

```json
{ "type": "text", "body": "halo", "reply_to_id": null }
```

Balasan `201` berisi objek pesan yang sama bentuknya dengan di riwayat.

Memanggil service yang sama persis dengan frame `message.send`, jadi validasi
dan otorisasinya identik. Batas panjang teks: **4000 karakter**.

### `DELETE /api/v1/messages/{id}`

Menghapus satu pesan. Hanya pengirimnya — `403` untuk yang lain. Balasan `204`.

**Soft delete.** Barisnya tetap muncul di riwayat dengan `is_deleted: true` dan
`body` kosong, supaya urutan pesan tidak berlubang bagi peserta lain. Tampilkan
sebagai "pesan ini dihapus".

### `DELETE /api/v1/conversations/{id}/messages`

Mengosongkan riwayat percakapan **hanya untuk pemanggil**. Balasan `204`.

Peserta lain tetap melihat percakapannya utuh — menghapus pesan dari layar orang
lain bukan wewenang siapa pun di dalam percakapan. Yang dicatat adalah batas
baca, jadi pesan baru setelah ini tetap muncul seperti biasa.

---

## 6. Story

### `GET /api/v1/stories`

Story aktif (< 24 jam) dari diri sendiri dan orang yang diikuti, **sudah
dikelompokkan per orang** dan diurutkan — milik sendiri paling depan.

Klien tidak perlu mengelompokkan atau mengurutkan apa pun.

```json
{
  "data": [{
    "author_id": "4e127292-...",
    "username": "admin",
    "display_name": "Admin",
    "avatar_media_id": "019f8e12-...",
    "is_current_user": true,
    "all_viewed": false,
    "unviewed_count": 2,
    "latest_story_at": "2026-07-23T08:00:00Z",
    "stories": [{
      "id": "019f8e77-...",
      "media_id": "019f8e70-...",
      "media_kind": "image",
      "media_url": "https://<ref>.supabase.co/storage/v1/object/public/media/...",
      "duration_ms": 0,
      "viewed": false,
      "created_at": "2026-07-23T07:30:00Z",
      "expires_at": "2026-07-24T07:30:00Z"
    }]
  }]
}
```

Pemetaan langsung ke UI story row:

| Field | Dipakai untuk |
|---|---|
| satu elemen `data` | satu avatar di story row |
| `len(stories)` | jumlah segmen pada ring |
| `all_viewed` | ring abu (true) atau berwarna (false) |
| `stories[].viewed` | segmen mana yang sudah ditonton |
| `media_kind` | `image` → tampil 5 detik; `video` → putar sampai selesai |
| `duration_ms` | durasi video, untuk progress bar |

### `POST /api/v1/stories`

Media harus **sudah diunggah dan dikonfirmasi** lebih dulu (bagian 8).

```json
{ "media_id": "019f8e70-...", "visibility": "followers" }
```

`visibility`: `public` | `followers` | `close_friends`. Default `followers`.
`403` kalau media bukan milik pemanggil.

### `POST /api/v1/stories/{id}/view`

Menandai sudah ditonton. Balasan `204`. Idempoten — menonton ulang tidak
menaikkan counter dua kali.

### `GET /api/v1/stories/me`

Story milik sendiri. Berbeda dari `GET /stories`: di sini yang menarik adalah
berapa orang menonton dan apakah masih tayang — bahan untuk layar arsip dan
untuk memilih mana yang mau dihapus.

| Query | Default | Arti |
|---|---|---|
| `include_expired` | `false` | `true` menyertakan yang sudah lewat 24 jam |

```json
{ "data": [{
    "id": "019f8e77-...",
    "media_id": "019f8e70-...",
    "media_kind": "image",
    "media_url": "https://.../object/public/media/...",
    "duration_ms": 0,
    "view_count": 12,
    "is_expired": false,
    "created_at": "2026-07-23T07:30:00Z",
    "expires_at": "2026-07-24T07:30:00Z"
}], "meta": { "count": 1 } }
```

### `GET /api/v1/stories/{id}/viewers`

Siapa saja yang menonton sebuah story, **terbaru dulu**.

**Hanya pemilik story yang boleh melihatnya** — `403` untuk siapa pun yang lain.
Membukanya ke penonton lain berarti memberi tahu siapa saja yang menyimak
seseorang, dan itu tidak pernah mereka setujui.

| Query | Default | Catatan |
|---|---|---|
| `limit` | 50 | maksimum 200 |
| `before_at` | — | RFC3339; ambil dari `meta.next_before_at` |
| `before_id` | — | ambil dari `meta.next_before_id` |

```json
{
  "data": [{
    "user_id": "d5891ae6-...",
    "username": "citra",
    "display_name": "Citra Dewi",
    "avatar_url": "https://.../object/public/media/...",
    "viewed_at": "2026-07-23T08:41:12Z"
  }],
  "meta": {
    "count": 1,
    "next_before_at": "2026-07-23T08:41:12Z",
    "next_before_id": "d5891ae6-..."
  }
}
```

**Cursor-nya berpasangan — kirim `before_at` DAN `before_id` bersamaan.** Dua
orang bisa menonton pada milidetik yang sama, dan cursor berbasis waktu saja
akan melewatkan salah satunya saat berpindah halaman.

Untuk **jumlah** penonton, tidak perlu memanggil endpoint ini: `view_count`
sudah ada di [`GET /api/v1/stories/me`](#get-apiv1storiesme). Panggil `viewers`
hanya saat pengguna membuka daftarnya.

`404` kalau story tidak ada atau sudah dihapus.

### `DELETE /api/v1/stories/{id}`

Menghapus story sendiri, foto maupun video. Balasan `204`.

`403` kalau story milik orang lain · `404` kalau tidak ada atau sudah dihapus.

Dua hal yang perlu diketahui soal perilakunya:

**Story disembunyikan, bukan dimusnahkan.** Barisnya tetap ada dengan
`deleted_at` terisi. Permintaan moderasi bisa datang setelah story hilang dari
layar, dan menghapus barisnya seketika membuat itu mustahil dijawab. Baris yang
sudah lama kedaluwarsa dibuang terpisah setelah masa tenggang tujuh hari.

**Medianya tidak ikut dihapus.** Satu `media_asset` boleh dipakai berkali-kali —
dikirim ulang sebagai pesan, misalnya — sehingga menghapus byte-nya bersama
story akan merusak tautan yang masih dipakai di tempat lain. Media yatim
dibersihkan oleh job terpisah, juga setelah masa tenggang.

Catatan tontonan (`story_views`) ikut dibuang: setelah story hilang, daftar
penontonnya tidak berguna dan hanya menyimpan siapa melihat apa tanpa alasan.

---

## 7. Direktori pengguna & follow

### `GET /api/v1/users/{username}`

Sasaran hasil **scan QR**. Kode QR sebaiknya berisi username
(mis. `syntra://u/admin`), bukan UUID — lebih pendek, lebih mudah dipindai,
dan tidak membocorkan struktur internal.

```json
{ "data": {
    "id": "4e127292-...",
    "username": "admin",
    "display_name": "Admin",
    "avatar_media_id": "019f8e12-...",
    "follower_count": 12,
    "following_count": 30,
    "follow_status": "accepted",
    "is_self": false
} }
```

`follow_status` menentukan label tombol di layar profil:

| Nilai | Arti | Label tombol |
|---|---|---|
| `""` | belum diikuti | Follow |
| `"pending"` | menunggu persetujuan (akun privat) | Requested |
| `"accepted"` | sudah diikuti | Following |

Pencarian tidak membedakan huruf besar-kecil. `404` kalau tidak ada.

Alur lengkap scan QR:
`scan` → `GET /users/{username}` → tampilkan profil → `POST /conversations`
(`type: direct`) → buka layar chat.

### `POST /api/v1/users/{username}/follow`

Mulai mengikuti. **Idempoten** — menekan tombol dua kali tidak menggandakan
apa pun dan tidak menggelembungkan counter.

Balasannya adalah profil lengkap dengan `follow_status` terbaru, jadi klien
bisa langsung memperbarui tombol tanpa memuat ulang profil.

Akun publik langsung `accepted`; akun privat menjadi `pending` sampai disetujui.
`403` kalau salah satu pihak memblokir yang lain.

### `DELETE /api/v1/users/{username}/follow`

Berhenti mengikuti. Idempoten. Balasannya sama bentuknya.

### `GET /api/v1/users/me/following`

Daftar orang yang diikuti, urut menurut nama tampil.

```json
{ "data": [{
    "id": "4e127292-...",
    "username": "admin",
    "display_name": "Admin",
    "avatar_media_id": "019f8e12-...",
    "follow_status": "accepted",
    "followed_at": "2026-07-23T09:00:00Z"
}], "meta": { "count": 1 } }
```

### `GET /api/v1/users/me/follow-requests`

Permintaan follow yang menunggu keputusan — hanya relevan untuk akun privat
(`is_private: true`). Bentuknya sama dengan `me/following`.

### `POST /api/v1/users/{username}/follow/approve`
### `POST /api/v1/users/{username}/follow/reject`

Menyetujui atau menolak. Balasan `204`.

Tanpa kedua endpoint ini, akun privat menghasilkan status `pending` yang
menggantung selamanya: peminta tidak pernah menjadi pengikut, jadi tidak pernah
bisa melihat story. Kalau aplikasi belum punya layar ini, pakai akun publik.

> **Kalau story seseorang tidak muncul di story row, periksa endpoint ini
> dulu.** `GET /stories` hanya menampilkan story dari orang yang ada di daftar
> ini dengan status `accepted` — itu penyebab paling sering, bukan bug di story.

---

## 7b. Notifikasi

### `GET /api/v1/notifications`

Terbaru dulu. Cursor `before` adalah **id notifikasi** — id memakai UUIDv7 yang
terurut waktu, jadi tidak melewatkan baris ketika dua notifikasi lahir pada
milidetik yang sama.

| Query | Default | Catatan |
|---|---|---|
| `limit` | 30 | maksimum 100 |
| `before` | — | id notifikasi dari `meta.next_before` |

```json
{ "data": [{
    "id": "019f8f93-...",
    "type": "follow",
    "actor_id": "6f77d0ad-...",
    "actor_username": "budi",
    "actor_name": "Budi Santoso",
    "actor_avatar_url": "https://.../object/public/media/...",
    "subject_type": "user",
    "subject_id": "d5891ae6-...",
    "is_read": false,
    "created_at": "2026-07-23T09:12:04Z"
}], "meta": { "count": 1, "next_before": "019f8f93-..." } }
```

`type`: `follow` · `like` · `comment` · `mention` · `story_reply` · `room_live` · `system`

### `GET /api/v1/notifications/unread-count`

```json
{ "data": { "unread": 7 } }
```

Dipisah dari daftarnya karena badge dipanggil jauh lebih sering dan jawabannya
jauh lebih kecil.

### `POST /api/v1/notifications/read`

```jsonc
{ "notification_id": "019f8f93-..." }   // satu
{ }                                      // semua
```

```json
{ "data": { "marked": 7 } }
```

### Event realtime `notification.new`

Disiarkan ke topik `user:<id>` saat notifikasi baru dibuat:

```json
{ "type": "notification.new", "data": {
    "id": "019f8f93-...",
    "type": "follow",
    "actor_id": "6f77d0ad-...",
    "subject_type": "user",
    "subject_id": "d5891ae6-...",
    "created_at": "2026-07-23T09:12:04Z"
} }
```

Payload-nya ringkas dengan sengaja — cukup untuk menaikkan badge dan
menampilkan toast. Untuk isi lengkapnya, muat `GET /notifications`.

Notifikasi **tidak dibuat** kalau penerimanya adalah pelaku itu sendiri, atau
kalau salah satu pihak memblokir yang lain. Keputusan itu ada di sisi database
supaya aturannya tidak terduplikasi.

---

## 8. Media — alur tiga langkah

**Byte media tidak pernah melewati server Go.** Kalau ia jadi perantara, satu
unggahan video 50 MB menahan memori dan goroutine tanpa memberi nilai apa pun.

```
1. POST /api/v1/media/upload-url      → dapat media_id + upload_url
2. PUT byte ke upload_url             → langsung ke Supabase Storage
3. POST /api/v1/media/{id}/confirm    → metadata tercatat di database
```

Langkah 3 wajib. Sebelum itu `media_id` belum ada di database, dan story atau
pesan yang menunjuk kepadanya akan ditolak.

### Langkah 1 — `POST /api/v1/media/upload-url`

```json
{ "kind": "image", "extension": "jpg" }
```

`kind`: `image` | `video` | `audio` | `voice_note`

```json
{ "data": {
    "media_id": "019f8e70-...",
    "bucket": "media",
    "storage_key": "image/4e127292-.../019f8e70-....jpg",
    "upload_url": "https://<ref>.supabase.co/storage/v1/object/upload/sign/media/...?token=...",
    "token": "..."
} }
```

`storage_key` disusun server, bukan klien — kalau klien yang menentukan, ia
bisa menulis ke path milik orang lain.

### Langkah 2 — unggah

`PUT` byte mentah ke `upload_url`, dengan `Content-Type` sesuai berkas.
Tidak perlu header autentikasi lain; token sudah tertanam di URL.

### Langkah 3 — `POST /api/v1/media/{id}/confirm`

```json
{
  "kind": "image",
  "storage_key": "image/4e127292-.../019f8e70-....jpg",
  "mime_type": "image/jpeg",
  "size_bytes": 184320,
  "width": 1080,
  "height": 1920,
  "duration_ms": 0
}
```

```json
{ "data": { "id": "019f8e70-...", "url": "https://.../object/public/media/..." } }
```

> **Prasyarat:** bucket `media` harus sudah dibuat di Supabase Dashboard →
> Storage, dan disetel **public** agar `url` di atas bisa dibuka klien.

---

## 9. WebSocket

### Handshake

```
GET /api/v1/ws
Authorization: Bearer <jwt>        (atau ?token=<jwt>)
```

Autentikasi terjadi saat handshake HTTP, bukan lewat frame. Koneksi tanpa
identitas valid ditolak `401` sebelum di-upgrade.

Frame pertama dari server:

```json
{ "type": "ready", "data": {
    "client_id": "019f8e5a-...",
    "user_id": "4e127292-...",
    "heartbeat_interval_ms": 25000,
    "max_frame_bytes": 65536
} }
```

### Amplop frame

Semua frame, dua arah, memakai bentuk yang sama:

```jsonc
{
  "type": "message.send",   // wajib
  "ref":  "c-42",           // opsional, ditentukan klien
  "data": { },              // payload
  "error": { },             // hanya pada type "error"
  "ts": 1784794180000       // milidetik, diisi server
}
```

`ref` dikembalikan apa adanya pada `ack`/`error`. Klien memakainya untuk
mencocokkan balasan dengan permintaan, sehingga pesan optimistik di layar bisa
diganti dengan yang otoritatif dari server begitu `ack` tiba.

### Frame klien → server

| `type` | `data` | Efek |
|---|---|---|
| `subscribe` | `{"topics":["conversation:<id>"]}` | berlangganan; **diotorisasi per topik** |
| `unsubscribe` | `{"topics":[...]}` | berhenti berlangganan |
| `ping` | — | dibalas `pong` |
| `message.send` | `{"conversation_id","type?","body","reply_to_id?"}` | simpan + siarkan pesan |
| `message.read` | `{"conversation_id","message_id"}` | reset unread, sinkron antar-perangkat |
| `typing.start` | `{"conversation_id"}` | siarkan indikator mengetik |
| `typing.stop` | `{"conversation_id"}` | hentikan indikator |
| `presence.query` | `{"user_ids":[...]}` | tanya status online sekumpulan orang |
| `room.chat` | `{"room_id","body"}` | pesan di voice room — **efemeral, tidak disimpan** |

### Frame server → klien

| `type` | Kapan |
|---|---|
| `ready` | koneksi terbentuk |
| `ack` | permintaan ber-`ref` berhasil |
| `error` | permintaan gagal, lihat `error.code` |
| `pong` | balasan `ping` |
| `message.new` | pesan baru pada topik percakapan yang dilanggan |
| `message.read` | perangkat lain milik pengguna yang sama menandai sudah dibaca |
| `typing` | anggota lain sedang mengetik |
| `presence.update` | seseorang menjadi online/offline |
| `room.message` | pesan baru di voice room yang sedang dilanggan |
| `room.ended` | room ditutup (host keluar) — **tutup layar & putuskan LiveKit** |
| `room.participants` | daftar peserta berubah — dikirim utuh, ganti daftar lokal |
| `room.speak_request` | ada yang mengangkat tangan (untuk host & moderator) |
| `room.role_changed` | peran seseorang berubah — lihat `needs_rejoin` |
| `room.join_decided` | permintaan masuk room disetujui/ditolak |
| `notification.new` | notifikasi baru untuk kamu (topik `user:<id>`) |

Empat event `room.*` di atas disiarkan ke topik `room:<id>`. Bentuk payload dan
alur lengkapnya ada di [`voice-rooms.md`](voice-rooms.md).

Satu yang tidak boleh dilewat: **`room.role_changed` membawa `needs_rejoin`.**
Kalau bernilai `true`, klien harus memanggil `POST /rooms/{id}/join` lagi untuk
mendapat token SFU baru — token lama diterbitkan dengan `canPublish: false` dan
tidak akan bisa menyalakan mikrofon meski tombolnya sudah muncul.

Masuk dan keluar voice room dilakukan lewat **REST**, bukan frame — lihat
[`voice-rooms.md`](voice-rooms.md).

### Topik dan otorisasinya

Format: `<jenis>:<uuid>`

| Topik | Siapa yang boleh | Status |
|---|---|---|
| `user:<id>` | hanya pemiliknya | aktif — dilanggan otomatis saat connect |
| `conversation:<id>` | anggota percakapan | aktif — diperiksa keanggotaannya |
| `room:<id>` | peserta aktif voice room | aktif — membawa chat efemeral |
| `reel:<id>` | — | **ditolak**, fase berikutnya |

Otorisasi dilakukan **per topik**, bukan per koneksi. Tanpa itu, siapa pun yang
punya token valid bisa `subscribe` ke `conversation:<id-orang-lain>` dan
menyimak percakapan yang bukan miliknya. Topik yang belum punya aturan ditolak
secara default.

Batas: 50 topik per frame `subscribe`, 200 topik per koneksi.

### Presence

```jsonc
// klien bertanya
{"type":"presence.query","ref":"p1","data":{"user_ids":["4e127292-...","0192f3a1-..."]}}

// server menjawab
{"type":"ack","ref":"p1","data":[
  {"user_id":"4e127292-...","online":true},
  {"user_id":"0192f3a1-...","online":false,"last_seen":"2026-07-23T08:40:00Z"}
]}

// dan menyiarkan saat berubah
{"type":"presence.update","data":{"user_id":"4e127292-...","online":true}}
```

Klien menanyakan seluruh `counterpart_id` di daftar chat dalam **satu** frame,
bukan satu per satu.

Presence disimpan di Redis dengan TTL, bukan di database — kalau proses server
mati mendadak, statusnya kedaluwarsa sendiri alih-alih membuat semua orang
tampak online selamanya.

### Heartbeat

Memakai ping/pong level protokol WebSocket, bukan frame aplikasi. Server
mengirim ping tiap 25 detik dan memutus koneksi yang tidak membalas dalam 60
detik. OkHttp menanganinya otomatis.

---

## 10. Alur proses

### Kirim pesan, dari socket sampai penerima

```mermaid
sequenceDiagram
    participant A as Klien A
    participant S1 as Server :8080
    participant DB as Supabase
    participant R as Redis
    participant S2 as Server (instance lain)
    participant B as Klien B

    A->>S1: {"type":"message.send","ref":"c-42",...}
    S1->>S1: validasi + cek keanggotaan
    S1->>DB: rpc/send_message (+ JWT pengguna)
    Note over DB: satu transaksi:<br/>insert pesan,<br/>update ringkasan percakapan,<br/>naikkan unread anggota lain
    DB-->>S1: ok
    S1-->>A: {"type":"ack","ref":"c-42","data":{"id","created_at"}}
    S1->>S1: kirim message.new ke pelanggan lokal
    S1->>R: PUBLISH conversation:<id>
    R->>S2: diteruskan
    S2->>B: {"type":"message.new", ...}
```

Tiga hal yang terlihat di sini:

**Ack membawa `id` dan `created_at` final** — klien mengganti pesan optimistik
di layar tanpa menunggu `message.new`.

**Pengirim menerima DUA frame: `ack` dan `message.new`.** Siaran itu tidak
dikecualikan untuk pengirim, karena perangkat lain miliknya memang harus ikut
menerimanya. Klien wajib **dedup berdasarkan `id`** — abaikan `message.new`
yang id-nya sudah ada di daftar. Tanpa itu, pesan tampil dua kali di layar
pengirim; gejala yang sudah pernah muncul.

**Siaran gagal tidak membatalkan pesan.** Pesannya sudah durabel. Mengembalikan
error justru membuat pengirim mengira gagal lalu mengirim ulang — duplikat,
bukan perbaikan.

**Redis Pub/Sub at-most-once.** Lubang itu ditutup dengan sinkronisasi ulang
lewat `GET .../messages` saat reconnect, bukan dengan mengandalkan pub/sub.

### Urutan yang disarankan saat aplikasi dibuka

```
1. POST /api/v1/auth/login                    → access_token + refresh_token
2. GET  /api/v1/conversations                 → isi layar utama
3. GET  /api/v1/stories                       → isi story row
4. WS   connect                               → tunggu frame "ready"
5. WS   subscribe conversation:<id> (yang terlihat di layar)
6. WS   presence.query dengan seluruh counterpart_id
7. Saat chat dibuka: GET .../messages
8. Saat reconnect: ulangi langkah 5–7
9. Saat menerima 401: POST /auth/refresh, lalu ulangi permintaan yang gagal
```

## 10b. Menguji sendiri

Seluruh endpoint di dokumen ini bisa diverifikasi sekaligus:

```powershell
cd server
powershell -ExecutionPolicy Bypass -File .\scripts\smoke.ps1
```

Tambahkan `-WithRegister` untuk ikut menguji pendaftaran — dilewati secara
bawaan karena Supabase membatasi jumlah signup per jam.

> Jangan menguji dengan `curl.exe -d '{"a":"b"}'` di PowerShell. Tanda kutip di
> dalamnya dirusak sebelum sampai ke curl, dan hasilnya `400 body bukan JSON
> yang valid` — menyesatkan, seolah endpointnya bermasalah. Pakai
> `Invoke-RestMethod`, atau simpan body ke berkas lalu `--data-binary "@file"`.

---

## 11. Batasan

| Batas | Nilai |
|---|---|
| Body JSON REST | 1 MB |
| Panjang pesan teks | 4000 karakter |
| Judul grup | 100 karakter |
| Anggota grup sekali buat | 256 |
| Ukuran frame WebSocket | 64 KB |
| Halaman percakapan | 30 default, 100 maks |
| Halaman pesan | 50 default, 100 maks |
| Topik per frame `subscribe` | 50 |
| Total topik per koneksi | 200 |
| Buffer kirim per koneksi | 64 frame |

Kalau buffer kirim penuh, **koneksi diputus, bukan diblokir** — klien yang
lambat tidak boleh menahan siaran ke seluruh topik. Klien akan reconnect lalu
menyinkronkan ulang.

Rate limit nginx: 10 r/s dengan burst 20 per alamat IP.

---

## 11b. Voice room

Punya dokumen sendiri karena ada satu hal yang mengubah cara klien dibangun:
**suara tidak lewat backend ini.** Audio ditangani LiveKit; backend hanya
menerbitkan token. Chat di dalam room pun bersifat efemeral — hilang bersama
room-nya.

→ **[`voice-rooms.md`](voice-rooms.md)**

---

## 12. Yang belum ada

- Event peserta masuk/keluar room (chat room sudah jalan; event peserta belum)
- Ubah/hapus pesan
- Kelola anggota grup setelah dibuat (tambah/keluarkan/keluar)
- Menyetujui permintaan follow untuk akun privat — statusnya bisa jadi
  `pending`, tapi belum ada endpoint untuk menyetujuinya
- Daftar follower (kebalikan `me/following`)
- Push notification (FCM) saat aplikasi tertutup
- Starred messages
- Reels, voice room, panggilan
- Rate limiting per pengguna (yang ada baru per IP di nginx)
- Privasi presence — saat ini status online terlihat oleh semua lawan bicara
  tanpa kecuali; `user_settings` sudah menyediakan tempat aturannya

---

## 13. Peta ke kode

| Mau lihat | Berkas |
|---|---|
| Semua rute REST | `server/internal/transport/rest/router.go` |
| Semua frame WebSocket | `server/internal/transport/ws/handlers.go` |
| Konstanta protokol | `server/internal/transport/ws/protocol/protocol.go` |
| Bentuk response & kode error | `server/internal/transport/rest/httpx/httpx.go` |
| Aturan bisnis chat | `server/internal/domain/chat/service.go` |
| Aturan bisnis story | `server/internal/domain/story/story.go` |
| Alur media | `server/internal/domain/media/media.go` |
| Presence | `server/internal/domain/presence/presence.go` |
| Panggilan ke Supabase | `server/internal/repository/supabase/` |
| Fungsi SQL | `server/migrations/` |
| Registry koneksi & topik | `server/internal/transport/ws/hub.go` |

Dokumen terkait: [`erd.md`](erd.md) untuk model data,
[`app-backend-alignment.md`](app-backend-alignment.md) untuk pemetaan
layar aplikasi ke endpoint.
