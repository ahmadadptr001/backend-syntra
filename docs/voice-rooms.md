# Voice Rooms — cara kerjanya

Dokumen ini menjelaskan satu hal yang sering disalahpahami saat pertama kali
menyambungkan fitur voice room, lalu memberi kontrak lengkapnya.

---

## 1. Suara tidak lewat backend ini

Backend Syntra **tidak mengalirkan audio**, dan tidak akan pernah.

Audio realtime butuh UDP, jitter buffer, echo cancellation, dan koreksi paket
hilang. WebSocket berjalan di atas TCP, yang menjamin urutan dan pengiriman
ulang — bagus untuk pesan, buruk untuk suara. Satu paket yang hilang akan
menahan semua paket sesudahnya sampai berhasil dikirim ulang, dan hasilnya
suara terpotong-potong dengan jeda yang makin lama makin panjang.

Karena itu audio ditangani **SFU** (Selective Forwarding Unit) — di proyek ini
LiveKit. Backend Syntra berperan sebagai **otoritas**: ia menentukan siapa
boleh masuk dan siapa boleh bicara, lalu menerbitkan token yang membuktikannya.

```
                    ┌──────────────────────────────┐
   klien A ────────►│                              │
   (audio, WebRTC)  │   LiveKit SFU                │◄──────── klien B
                    │   meneruskan audio           │        (audio, WebRTC)
                    └──────────────────────────────┘
                                  ▲
                                  │ token: "boleh masuk room X,
                                  │         boleh/tidak boleh bicara"
                                  │
   klien A ──── POST /rooms/{id}/join ───► backend Syntra ───► Supabase
                                                              (siapa, peran apa)
```

Backend tidak pernah melihat satu byte pun audio.

---

## 2. Kenapa tokennya penting

Token bukan formalitas. Ia satu-satunya yang menegakkan dua aturan:

- **Siapa boleh mendengarkan.** Room `followers` hanya bisa dimasuki pengikut;
  orang yang diblokir tidak bisa masuk sama sekali.
- **Siapa boleh bicara.** Token untuk pendengar diterbitkan dengan
  `canPublish: false`. LiveKit menolak audio dari koneksi itu di sisi server.

Menyembunyikan tombol mikrofon di aplikasi **bukan** pengamanan — siapa pun
yang memodifikasi klien bisa mengaktifkannya kembali. Yang benar-benar
menghalangi adalah token.

Konsekuensi yang perlu diingat saat membangun UI: **setelah peran seseorang
naik menjadi speaker, ia harus memanggil `join` lagi** untuk mendapat token
baru. Token lama diterbitkan dengan `canPublish: false` dan tidak akan bisa
menyalakan mikrofon meski tombolnya sudah muncul.

---

## 3. Menyiapkan LiveKit

Tanpa ini, room tetap bisa dibuat dan didaftar, tapi **tidak akan ada suara**.
Respons `join` akan mengembalikan `sfu_token` kosong, dan `GET /rooms`
mengirim `meta.sfu_ready: false`.

### Cara tercepat — LiveKit Cloud

1. Daftar di [cloud.livekit.io](https://cloud.livekit.io) (ada tier gratis)
2. Buat project
3. Salin **API Key**, **API Secret**, dan **WebSocket URL**
4. Isi di `.env`:

```bash
LIVEKIT_API_KEY=APIxxxxxxxx
LIVEKIT_API_SECRET=xxxxxxxxxxxxxxxx
LIVEKIT_URL=wss://nama-project.livekit.cloud
```

5. Restart server. Peringatan "LiveKit belum dikonfigurasi" akan hilang dari log.

### Self-host untuk pengembangan

```bash
docker run --rm -p 7880:7880 -p 7881:7881 -p 7882:7882/udp \
  livekit/livekit-server --dev
```

Mode `--dev` memakai kunci tetap `devkey` / `secret`:

```bash
LIVEKIT_API_KEY=devkey
LIVEKIT_API_SECRET=secret
LIVEKIT_URL=ws://localhost:7880
```

> Port **7882/udp** wajib terbuka. Kalau hanya port TCP yang dibuka, koneksi
> akan terbentuk tetapi tidak ada suara yang mengalir — gejala yang membingungkan
> karena semuanya tampak berhasil.

---

## 4. Endpoint

Semua butuh `Authorization: Bearer <jwt>`.

| Method | Path | Untuk |
|---|---|---|
| `GET` | `/api/v1/rooms` | daftar room yang sedang berlangsung |
| `POST` | `/api/v1/rooms` | buat room baru (pemanggil jadi host) |
| `POST` | `/api/v1/rooms/{id}/join` | masuk + **dapat token SFU** |
| `POST` | `/api/v1/rooms/{id}/leave` | keluar (host keluar = room berakhir) |
| `GET` | `/api/v1/rooms/{id}/participants` | daftar peserta, host paling atas |
| `PATCH` | `/api/v1/rooms/{id}/participants` | ubah peran (host/moderator saja) |
| `POST` | `/api/v1/rooms/{id}/raise-hand` | minta izin bicara |
| `PATCH` | `/api/v1/rooms/{id}/mute` | ubah status bisu sendiri |

### `GET /api/v1/rooms`

```json
{
  "data": [{
    "id": "019f8e77-...",
    "host_id": "4e127292-...",
    "host_username": "admin",
    "host_name": "Admin",
    "title": "Ngobrol santai",
    "topic": "teknologi",
    "visibility": "public",
    "participant_count": 12,
    "speaker_count": 3,
    "max_participants": 50,
    "started_at": "2026-07-23T12:00:00Z"
  }],
  "meta": { "count": 1, "sfu_ready": true }
}
```

**Periksa `meta.sfu_ready` sebelum menampilkan tombol Join.** Kalau `false`,
media server belum dikonfigurasi dan bergabung tidak akan menghasilkan suara.

### `POST /api/v1/rooms`

```json
{ "title": "Ngobrol santai", "topic": "teknologi", "visibility": "public" }
```

`visibility`: `public` | `followers` | `invite_only`. Default `public`.

### `POST /api/v1/rooms/{id}/join`

Ini endpoint terpenting.

```json
{ "data": {
    "room_id": "019f8e77-...",
    "role": "listener",
    "can_publish": false,
    "sfu_room_id": "019f8e77-...",
    "sfu_token": "eyJhbGciOiJIUzI1NiJ9...",
    "sfu_url": "wss://nama-project.livekit.cloud"
} }
```

Dua field terakhir yang membuat suara terdengar. Klien menyambung ke `sfu_url`
memakai `sfu_token` lewat SDK LiveKit.

`sfu_token` kosong berarti media server belum dikonfigurasi — keanggotaan
tetap tercatat, tapi tidak akan ada suara. Jangan mencoba menyambung dengan
token kosong.

Kegagalan: `404` room tidak ada / sudah berakhir · `403` diblokir atau bukan
pengikut · `409` room penuh.

### `PATCH /api/v1/rooms/{id}/participants`

```json
{ "user_id": "6f77d0ad-...", "role": "speaker" }
```

`role`: `moderator` | `speaker` | `listener`. Hanya host dan moderator yang
boleh. Peran `host` tidak bisa diberikan — ia melekat pada pembuat room.

Setelah ini, klien yang bersangkutan **harus memanggil `join` lagi** untuk
mendapat token dengan `canPublish: true`.

### `PATCH /api/v1/rooms/{id}/mute`

```json
{ "muted": true }
```

Ini **hanya mencatat status untuk ditampilkan** ke peserta lain. Membisukan
mikrofon yang sebenarnya dilakukan SDK LiveKit di sisi klien. Panggil keduanya
bersamaan, kalau tidak tampilan akan berbohong.

Pendengar tidak bisa menyalakan mikrofon sendiri — ditolak `403`.

---

## 5. Alur di sisi klien

```kotlin
// 1. daftar room
val rooms = api.getRooms()
if (!rooms.meta.sfuReady) { /* sembunyikan tombol Join */ }

// 2. bergabung — dapat token
val join = api.joinRoom(roomId)

// 3. sambung ke media server; DI SINI suara mulai terdengar
val room = LiveKit.create(appContext)
room.connect(join.sfuUrl, join.sfuToken)

// 4. kalau boleh bicara, nyalakan mikrofon
if (join.canPublish) {
    room.localParticipant.setMicrophoneEnabled(true)
}

// 5. berlangganan kanal room — untuk chat teks & event peserta
ws.send("""{"type":"subscribe","data":{"topics":["room:$roomId"]}}""")

// 6. chat di dalam room (efemeral — jangan simpan ke Room/DataStore)
ws.send("""{"type":"room.chat","data":{"room_id":"$roomId","body":"halo"}}""")

// 7. keluar — dua-duanya
room.disconnect()
api.leaveRoom(roomId)
```

> Chat room jangan di-cache ke penyimpanan lokal. Ia dirancang hilang bersama
> room-nya; menyimpannya di klien akan menampilkan riwayat yang tidak dimiliki
> peserta lain, dan itu membingungkan.

Dependensi Android: `implementation "io.livekit:livekit-android:<versi>"`

Izin yang dibutuhkan:

```xml
<uses-permission android:name="android.permission.RECORD_AUDIO" />
<uses-permission android:name="android.permission.MODIFY_AUDIO_SETTINGS" />
<uses-permission android:name="android.permission.INTERNET" />
```

`RECORD_AUDIO` adalah izin runtime — harus diminta lewat dialog sebelum
menyalakan mikrofon, bukan hanya dideklarasikan di manifest.

---

## 6. Chat di dalam room — efemeral

Peserta aktif berlangganan topik `room:<uuid>`, lalu bisa saling berkirim pesan
teks lewat WebSocket.

**Pesan ini tidak pernah disimpan.** Ia disiarkan ke peserta yang sedang
terhubung, lalu hilang. Begitu room berakhir, seluruh percakapannya lenyap
selamanya — tidak ada riwayat untuk dimuat, dan tidak ada endpoint untuk
mengambilnya kembali.

Ini perbedaan mendasar dari chat biasa:

| | Chat percakapan | Chat voice room |
|---|---|---|
| Disimpan | ya, di `messages` | **tidak, sama sekali** |
| Riwayat | `GET .../messages` | tidak ada |
| Punya id pesan | ya (UUIDv7) | tidak |
| Ada `ack` | ya | tidak |
| Sinkronisasi ulang saat reconnect | ya | tidak — pesan yang lewat saat terputus memang hilang |
| Umur | permanen | selama room hidup |

Kirim:

```json
{"type":"room.chat","data":{"room_id":"019f8e77-...","body":"halo semua"}}
```

Terima:

```json
{"type":"room.message","data":{
  "room_id":"019f8e77-...",
  "sender_id":"4e127292-...",
  "body":"halo semua",
  "created_at":"2026-07-23T12:05:00Z"
}}
```

Batas panjang: **500 karakter**.

Karena tidak ada penyimpanan, tidak ada pula id pesan maupun `ack`. Klien yang
bergabung di tengah room hanya melihat pesan sejak ia masuk — itu perilaku yang
diinginkan, bukan cacat yang perlu ditambal dengan riwayat.

Otorisasinya bersandar pada langganan topik: kalau klien belum berlangganan
`room:<id>`, ia belum lolos pemeriksaan keanggotaan dan tidak bisa menyiarkan
ke sana.

**Kanal ini tidak membawa audio.** Suaranya lewat SFU.

---

## 7. Batasan dan yang belum ada

| Batas | Nilai |
|---|---|
| Peserta per room | 50 |
| Panjang judul | 100 karakter |
| Panjang pesan chat room | 500 karakter |
| Umur token SFU | 6 jam |

Belum ada:

- **Event peserta masuk/keluar.** Topik `room:<id>` sudah membawa chat teks,
  tetapi backend belum menyiarkan event saat seseorang masuk, keluar, atau naik
  peran — klien perlu polling `GET /participants` untuk itu. Ini yang paling
  layak dikerjakan berikutnya, dan jalur siarannya sudah ada.
- Perekaman room (`is_recorded` sudah ada di skema, belum dipakai). Perlu
  diingat: merekam suara butuh persetujuan eksplisit semua peserta di banyak
  yurisdiksi — tabel `consents` sudah menyediakan tempatnya.
- Room terjadwal (`scheduled_at` ada, alurnya belum)
- `invite_only` diperlakukan sama dengan `public` saat bergabung; undangannya
  belum dibuat
- Panggilan 1:1 (tabel `calls` di ERD belum diimplementasikan)

---

## 8. Kalau tidak ada suara

Urutkan pemeriksaannya:

1. **`meta.sfu_ready` bernilai `false`?** LiveKit belum dikonfigurasi. Lihat §3.
2. **`sfu_token` kosong di respons join?** Sama, penyebabnya di atas.
3. **`can_publish` bernilai `false`?** Perannya masih listener. Minta host
   menaikkan peran, lalu **panggil `join` lagi**.
4. **Izin `RECORD_AUDIO` belum diberikan?** Cek dialog izin Android.
5. **Self-host, port 7882/udp tertutup?** Koneksi terbentuk tapi audio tidak
   mengalir. Ini gejala yang paling membingungkan karena semuanya tampak normal.
6. **Terdengar tapi terpotong-potong?** Klien kemungkinan jatuh ke mode TCP
   relay. Periksa firewall UDP di jaringan tersebut.
