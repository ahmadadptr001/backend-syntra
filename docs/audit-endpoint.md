# Audit Endpoint — apa yang ada, apa yang belum

Hasil pemeriksaan menyeluruh: setiap tabel di skema dicocokkan dengan endpoint
yang benar-benar terdaftar di router, dan setiap layar aplikasi dicocokkan
dengan apa yang melayaninya.

Metode: daftar `CREATE TABLE` dari `server/migrations/`, daftar rute dari
`server/internal/transport/rest/router.go`, daftar frame dari
`server/internal/transport/ws/handlers.go`, dibandingkan dengan layar di
[`catatan-untuk-app.md`](catatan-untuk-app.md).

---

## Ringkasan

| | Jumlah |
|---|---|
| Endpoint REST terdaftar | 33 |
| Frame WebSocket masuk | 9 |
| Event WebSocket keluar | 13 |
| Tabel di skema | 21 |
| **Tabel tanpa endpoint apa pun** | **6** ← temuan utama |

> **Pembaruan 2026-07-24:** lima dari enam sudah ditutup (notifications, blocks,
> devices, reports, user_settings). Yang tersisa: `message_attachments`
> (lampiran media pada pesan) dan `consents` (persetujuan UU PDP).

---

## 1. Temuan terbesar: enam tabel tanpa jalan masuk

Tabel-tabel ini sudah ada sejak migrasi pertama, sebagian bahkan dipakai di
query lain — tetapi tidak ada satu pun cara membaca atau mengisinya.

| Tabel | Akibatnya | Status |
|---|---|---|
| `notifications` | Konstanta protokol `notification.new` **sudah ada tapi tidak pernah disiarkan**. Layar notifikasi mustahil dibangun | ✅ diperbaiki |
| `blocks` | Dicek di hampir **setiap** query (story, room, chat, follow), tetapi tidak ada cara memblokir — jadi pemeriksaan itu selama ini tidak pernah berarti apa-apa | ✅ diperbaiki |
| `devices` | Push token tidak pernah bisa disimpan; FCM mustahil dipasang | ✅ diperbaiki |
| `reports` | Kewajiban trust & safety yang saya tandai sejak analisis PRD pertama — tetap tidak bisa dipakai | ✅ diperbaiki |
| `user_settings` | Tidak ada cara membaca atau mengubah preferensi privasi | ✅ GET/PATCH /users/me |
| `message_attachments` | Layar chat punya tombol lampiran, tetapi media **tidak bisa dilampirkan ke pesan** | ❌ belum |
| `consents` | Persetujuan UU PDP tidak pernah tercatat | ❌ belum |
| `moderation_actions` | Untuk panel moderator; belum dibutuhkan | ❌ sengaja |

---

## 2. Yang ditambahkan setelah audit

### Profil sendiri

```
GET   /api/v1/users/me       profil + preferensi privasi
PATCH /api/v1/users/me       ubah nama, bio, avatar, privasi
```

`GET /users/{username}` hanya mengembalikan data publik. Layar pengaturan butuh
email dan preferensi — yang justru tidak boleh terlihat orang lain. Karena itu
keduanya dipisah, bukan digabung dengan flag.

### Notifikasi

```
GET   /api/v1/notifications              daftar, terbaru dulu
GET   /api/v1/notifications/unread-count badge
POST  /api/v1/notifications/read         tandai satu / semua
```

Plus event realtime **`notification.new`** — konstantanya sudah ada sejak awal
tetapi tidak pernah ada yang menyiarkannya.

### Blokir

```
POST   /api/v1/users/{username}/block
DELETE /api/v1/users/{username}/block
GET    /api/v1/users/me/blocked
```

Memblokir **memutus follow dua arah**. Membiarkannya berarti yang diblokir
masih menerima story dan pembaruan dari orang yang memblokirnya.

### Perangkat & laporan

```
POST   /api/v1/devices        daftarkan push token
DELETE /api/v1/devices/{id}   cabut
POST   /api/v1/reports        laporkan konten atau pengguna
```

Laporan `csam` otomatis berprioritas `critical` — kewajiban hukum dan SLA-nya
berbeda total dari spam, jadi harus bisa dirutekan khusus.

---

## 3. Realtime — keadaan sebenarnya

### Frame masuk (klien → server) — 9, semua berfungsi

`subscribe` · `unsubscribe` · `ping` · `message.send` · `message.read` ·
`typing.start` · `typing.stop` · `presence.query` · `room.chat`

### Event keluar (server → klien) — 13

| Event | Dipicu oleh | Status |
|---|---|---|
| `ready` | koneksi terbentuk | ✅ |
| `ack` / `error` / `pong` | balasan frame | ✅ |
| `message.new` | pesan baru | ✅ |
| `message.read` | dibaca di perangkat lain | ✅ |
| `typing` | indikator mengetik | ✅ |
| `presence.update` | online/offline | ✅ |
| `room.message` | chat efemeral di room | ✅ |
| `room.ended` | host keluar | ✅ |
| `room.participants` | peserta berubah | ✅ |
| `room.speak_request` | angkat tangan | ✅ |
| `room.role_changed` | peran berubah | ✅ |
| **`notification.new`** | notifikasi baru | ✅ **baru** |

### Konstanta mati yang saya buang

`room.join` dan `room.leave` terdefinisi di protokol tetapi **tidak punya
handler** — mengirimnya dibalas `unknown_type`. Masuk dan keluar room memang
lewat REST, bukan frame. Konstantanya dihapus supaya tidak menyesatkan.

### Realtime yang masih belum ada

| Event | Kenapa berguna | Prioritas |
|---|---|---|
| `conversation.new` | percakapan baru tidak muncul sampai daftar dimuat ulang | sedang |
| `message.deleted` / `message.edited` | menunggu fitur ubah/hapus pesan | rendah |
| `story.new` | story dari yang diikuti tidak muncul seketika | rendah |

---

## 4. Yang masih belum ada

Diurutkan menurut dampaknya ke layar yang sudah dibangun.

### Menghambat layar yang sudah ada

1. **Lampiran media pada pesan.** Layar chat punya tombol lampiran, tabel
   `message_attachments` ada, tetapi `POST .../messages` tidak menerima
   `media_ids`. Foto dan voice note belum bisa dikirim.
2. **Daftar pengikut.** Baru ada `me/following`; kebalikannya belum.
3. **Pencarian pengguna.** Baru bisa dicari lewat username persis. Layar "chat
   baru" butuh pencarian sebagian.

### Melengkapi fitur yang sudah jalan

4. **Ubah/hapus pesan** — `edited_at` dan `deleted_at` sudah ada di skema dan
   sudah dikirim ke klien, tetapi tidak ada cara mengisinya.
5. **Kelola anggota grup** — tambah, keluarkan, keluar sendiri.
6. **Bisukan percakapan** — kolom `muted_until` ada, tidak dipakai.
7. **Setujui permintaan follow** untuk akun privat. Status bisa `pending`,
   tetapi belum ada cara menyetujuinya, jadi permintaannya menggantung.
8. **Tandai dibaca lewat REST** — baru ada lewat WebSocket. Perlu jalur
   cadangan seperti kirim pesan.

### Belum ada di kedua sisi

9. Lupa kata sandi / atur ulang
10. Ganti kata sandi & ganti email
11. Persetujuan UU PDP (`consents`)
12. Ekspor & hapus akun (hak akses / hak hapus)
13. Reels, panggilan 1:1
14. Rate limiting per pengguna

---

## 5. Cara menjaga daftar ini tetap benar

```powershell
cd server
powershell -ExecutionPolicy Bypass -File .\scripts\smoke.ps1
```

Dan pemeriksaan silang router ↔ dokumen — kalau angkanya berbeda, salah satu
tertinggal:

```powershell
# jumlah rute di router
(Select-String server\internal\transport\rest\router.go `
  -Pattern '"(GET|POST|PATCH|DELETE|PUT) (/[^"]*)"' -AllMatches).Matches.Count

# jumlah rute di api.md
(Select-String docs\api.md `
  -Pattern '^\| `(GET|POST|PATCH|DELETE|PUT)` \|' -AllMatches).Matches.Count
```
