# Penyelarasan App ↔ Backend

Hasil membaca [`../server/catatan-untuk-app.md`](../server/catatan-untuk-app.md) dan
mencocokkannya dengan backend yang sudah ada.

> **Status: sebagian besar gap sudah ditutup.** Dokumen ini awalnya adalah
> analisis kesenjangan; kolom status di bawah sudah diperbarui setelah backend
> menyusul. Kontrak endpoint yang berlaku ada di [`api.md`](api.md).

Temuan awalnya: **UI-nya jauh lebih maju daripada backend-nya.** Aplikasi
Android sudah punya story viewer lengkap, presence, delivery status, dan
add-story dari galeri — sementara backend baru punya satu irisan chat.

Yang paling perlu disesuaikan ternyata bukan kode app, melainkan **urutan
pengerjaan backend**: rencana fase di `erd.md` menaruh story di fase 2 padahal
ia sudah jadi bagian inti layar utama.

---

## 1. Peta per layar

| Layar app | Yang dibutuhkan | Status backend |
|---|---|---|
| Daftar chat | nama, avatar, cuplikan pesan, waktu, unread | ✅ `GET /api/v1/conversations` |
| Daftar chat — titik hijau | presence realtime | ✅ `presence.query` + `presence.update` |
| Daftar chat — `typing…` | indikator mengetik | ✅ `typing.start` / `typing.stop` |
| Daftar chat — ✓✓ | status terkirim/dibaca per pesan | ❌ belum; baru ada `unread_count` |
| Story row | daftar orang + jumlah story + sudah ditonton | ✅ `GET /api/v1/stories`, sudah terkelompok |
| Story viewer | media per orang, urut, tandai ditonton | ✅ `POST /api/v1/stories/{id}/view` |
| Tambah story | upload foto/video | ✅ alur media tiga langkah + `POST /api/v1/stories` |
| Chat detail — riwayat | daftar pesan berpaginasi | ✅ `GET /api/v1/conversations/{id}/messages` |
| Chat detail — kirim | kirim pesan | ✅ `message.send` + `POST .../messages` |
| Chat detail — top bar | nama, avatar, presence lawan bicara | ✅ lengkap |
| Scan QR | identitas pengguna dari kode | ✅ `GET /api/v1/users/{username}` |
| Menu — New group | buat percakapan grup | ✅ `POST /api/v1/conversations` |
| Memulai chat baru | percakapan pribadi idempoten | ✅ `POST /api/v1/conversations` |
| Pencarian | filter nama & isi pesan | ✅ cukup difilter lokal untuk MVP |
| Story row — siapa yang muncul | daftar following | ✅ `POST`/`DELETE /users/{username}/follow`, `GET /users/me/following` |
| Daftar chat — ✓✓ (bahan) | posisi baca lawan bicara | ✅ `counterpart_last_read_id` di `GET /conversations` |
| Menu — Starred messages | tandai pesan | ❌ belum ada, juga belum ada di ERD |
| Shorts | daftar reel | ❌ belum ada (app juga masih placeholder) |
| Tab Rooms | voice room + chat efemeral | ✅ backend siap; app masih statis — lihat [voice-rooms.md](voice-rooms.md) |
| Tab Calls | panggilan 1:1 | ❌ placeholder di kedua sisi |

---

## 2. Empat gap yang dulu membuat app tidak bisa lepas dari data dummy

**Keempatnya sudah ditutup.** Bagian ini disimpan karena menjelaskan *kenapa*
endpoint-endpoint itu berbentuk seperti sekarang.

### a. Tidak ada riwayat pesan

`ChatDetailScreen` membuka sebuah percakapan dan langsung membutuhkan daftar
pesan. Backend hanya bisa **mengirim** pesan, tidak bisa mengembalikannya.

Dibutuhkan: `GET /api/v1/conversations/{id}/messages?before=<cursor>&limit=50`

Ini juga yang menutup lubang Redis Pub/Sub yang bersifat at-most-once: setiap
kali socket terhubung kembali, klien menyinkronkan ulang lewat endpoint ini.
Tanpa itu, pesan yang lewat saat koneksi putus hilang selamanya.

### b. Tidak ada cara membuat percakapan

Tidak ada `POST /api/v1/conversations`. Artinya percakapan hanya bisa muncul
kalau dimasukkan manual lewat SQL. Menu **New group** di app tidak punya
sasaran, dan memulai chat baru dengan seseorang pun tidak bisa.

Untuk chat pribadi, endpoint ini harus **idempoten**: memanggilnya dua kali
dengan orang yang sama harus mengembalikan percakapan yang sudah ada, bukan
membuat yang kedua. Kalau tidak, satu pasang pengguna bisa berakhir dengan
beberapa percakapan pribadi paralel — dan itu jenis kerusakan data yang sulit
dirapikan setelah terjadi.

### c. Tidak ada upload media

Alur "tambah story" di app memilih foto/video dari galeri. Tidak ada satu pun
endpoint yang menerima media. Tabel `media_assets` sudah ada di ERD, tetapi
belum ada jalan untuk mengisinya.

Rancangan yang sudah dicatat di ERD: klien meminta presigned URL, mengunggah
langsung ke object storage, lalu memberi tahu backend. Byte media tidak pernah
melewati proses Go. Karena databasenya Supabase, Supabase Storage adalah
pilihan paling langsung — ia sudah menyediakan signed upload URL.

### d. Story sepenuhnya belum ada

Ini ketidakcocokan perencanaan terbesar. Di `erd.md` §9 story ditempatkan di
fase 2, padahal di app ia **bagian dari layar pertama** — story row ada tepat
di bawah header, dan viewer-nya sudah dibangun lengkap dengan auto-advance,
progress per segmen, dan penanda sudah ditonton.

Rencana fase harus mengikuti kenyataan itu, bukan sebaliknya.

---

## 3. Presence — sudah ada

App menampilkan tiga status: titik hijau (online), `typing…`, dan
`last seen recently`. Ketiganya kini terlayani.

Cara kerjanya:

- Frame `presence.query` untuk menanyakan sekumpulan pengguna sekaligus, dan
  `presence.update` yang disiarkan saat status berubah
- Disimpan di **Redis dengan TTL**, bukan Postgres. Ini state efemeral yang
  berubah tiap kali seseorang membuka aplikasi, dan riwayatnya tidak punya
  nilai apa pun. TTL juga jadi pengaman: kalau proses server mati mendadak,
  statusnya kedaluwarsa sendiri alih-alih membuat semua orang tampak online
  selamanya
- Disiarkan ke **topik percakapan**, bukan topik pengguna — yang peduli pada
  status seseorang adalah lawan bicaranya, bukan perangkat lain miliknya

Yang belum dijaga: presence saat ini terlihat oleh semua lawan bicara tanpa
kecuali. `user_settings` sudah menyediakan tempat untuk aturan privasinya,
tapi aturannya belum ditegakkan.

---

## 4. Delivery status ✓✓

App menampilkan indikator terkirim. ERD sudah punya `MESSAGE_RECEIPTS`, dengan
catatan penting yang tetap berlaku: **tabel itu hanya untuk grup**. Untuk chat
1:1, `last_read_message_id` di `conversation_members` sudah cukup dan jauh
lebih murah — receipt per-pesan per-anggota tumbuh sebesar (jumlah pesan ×
jumlah anggota).

Untuk MVP: cukup dua keadaan — terkirim ke server, dan sudah dibaca lawan
bicara. Keduanya bisa diturunkan dari data yang sudah ada tanpa tabel baru.

---

## 5. Scan QR — sudah ada

Fitur ini menyiratkan sesuatu yang tadinya belum dirancang: **setiap pengguna
perlu identitas yang bisa dibagikan**. Bentuk yang dipilih adalah deep link
berisi username, misalnya `syntra://u/<username>`, karena username sudah unik
di tabel `users`.

Alur lengkapnya:

```
scan  →  GET /api/v1/users/{username}  →  tampilkan profil
      →  POST /api/v1/conversations {"type":"direct","user_id":...}
      →  buka layar chat
```

Jangan menaruh UUID mentah di dalam QR — username lebih pendek, lebih mudah
dipindai, dan tidak membocorkan struktur internal.

---

## 6. Perbedaan model data

| App | Backend | Catatan |
|---|---|---|
| `Conversation.name` | `title` | sudah diselaraskan — untuk direct berisi nama lawan bicara |
| `Conversation.gradient` | `avatar_media_id` | gradien adalah dekorasi lokal; jadikan cadangan saat avatar kosong, diturunkan dari hash id agar warnanya stabil |
| `Conversation.sent` | — | ❌ status terkirim/dibaca per pesan belum ada |
| `Presence` enum | `presence.query` / `presence.update` | ✅ `online` + `last_seen` |
| `ActivePerson.posts` | `stories[]` di grup story | ✅ `len(stories)` = jumlah segmen ring |
| `StoryImage` sealed | `media_kind` + `media_url` | `Res`/`Bitmap`/`Video` adalah urusan klien |
| `Message.fromMe` | `sender_id` | diturunkan klien: `sender_id == user saya` |
| `Message.time` | `created_at` | app memformat; backend selalu mengirim UTC |

Satu hal yang perlu dijaga saat menyambungkan: app memakai `data class`
in-memory tanpa id. Begitu terhubung ke backend, setiap entitas butuh `id`
supaya pesan optimistik di layar bisa diganti dengan yang otoritatif lewat
`ref` → `ack` (lihat [`api.md`](api.md) §9).

---

## 7. Urutan fase yang direvisi

Menggantikan `erd.md` §9 untuk hal yang menyangkut urutan pengerjaan.

**Fase 1 — membuat app lepas dari data dummy — ✅ SELESAI**

1. ✅ `GET /conversations/{id}/messages` — riwayat + sinkronisasi ulang
2. ✅ `POST /conversations` — mulai chat pribadi (idempoten) & buat grup
3. ✅ Presence: `presence.query`/`presence.update` + Redis TTL
4. ✅ Upload media lewat Supabase Storage
5. ✅ Story: `POST /stories`, `GET /stories` (terkelompok), `POST /stories/{id}/view`
6. ✅ `GET /users/{username}` untuk hasil scan QR

**Fase 1b — ✅ dua teratas selesai**

1. ✅ **Follow/unfollow.** `POST`/`DELETE /users/{username}/follow` dan
   `GET /users/me/following`. Ini tadinya pemblokir story row: `list_stories`
   hanya menampilkan story dari orang yang sudah diikuti, jadi tanpa cara
   membangun daftar following, story row selalu berisi milik sendiri saja.
2. ✅ **Bahan indikator ✓✓.** `counterpart_last_read_id` di `GET /conversations`.
   Diturunkan dari `last_read_message_id` — tanpa tabel receipt per pesan.
3. Ubah/hapus pesan, dan kelola anggota grup setelah dibuat
4. Menyetujui permintaan follow untuk akun privat
5. Push notification lewat FCM saat aplikasi tertutup

**Fase 2 — Shorts.** Reels, audio track, like, komentar. Tab-nya sudah ada di
app tetapi masih placeholder, jadi tidak menghambat.

**Fase 3 — Rooms & Calls.** Sesuai ERD; kedua tab masih placeholder.

**Fase 4 — AI.** Belum ada jejaknya di app sama sekali. Ini menguatkan catatan
di analisis PRD: nama "Syntra" menjanjikan AI synthesis, tapi sampai UI pun
belum ada satu fitur AI yang terdefinisi.

---

## 8. Yang tidak perlu diubah

Beberapa hal yang sudah cocok dan sebaiknya dibiarkan:

- **Tema gelap & Raleway** — murni urusan klien
- **Pencarian lokal** — untuk ratusan percakapan, memfilter di klien lebih
  responsif daripada bolak-balik ke server. Pindahkan ke server hanya kalau
  riwayat sudah tidak muat di memori
- **Navigasi berbasis state** — tidak berpengaruh ke backend
- **Rooms & Calls sebagai placeholder** — sejalan dengan fase 3
- **Story foto 5 detik** — aturan tampilan, bukan data

---

## 9. Satu keputusan yang masih menggantung

`catatan-untuk-app.md` menyebut "integrasi backend/real-time (mis. Firebase)" sebagai
ide pengembangan. Itu perlu dicoret sekarang — backend-nya sudah ada, memakai
Go + Supabase + WebSocket sendiri. Klien Kotlin akan memakai:

- **Supabase Auth SDK** untuk login (menghasilkan JWT)
- **OkHttp/Ktor** ke `https://<host>/api/v1/...` untuk REST
- **OkHttp WebSocket** ke `wss://<host>/api/v1/ws` untuk realtime

Bukan Firebase. Satu-satunya bagian Google yang tetap dipakai adalah FCM untuk
push notification saat aplikasi tertutup — dan itu belum dirancang di kedua sisi.
