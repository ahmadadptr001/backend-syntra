# Dokumentasi Syntra

**Seluruh dokumentasi proyek ada di folder ini.** Aplikasi mengacu ke sini —
tidak ada dokumen yang tersebar di `server/` selain `server/README.md` yang
membahas kode Go-nya sendiri.

---

## Mulai dari mana

| Kalau kamu… | Baca |
|---|---|
| menyambungkan aplikasi ke backend | **[`api.md`](api.md)** |
| mengerjakan layar voice room | [`voice-rooms.md`](voice-rooms.md) |
| mencari layar mana dilayani endpoint mana | [`app-backend-alignment.md`](app-backend-alignment.md) |
| mengerjakan sisi Android | [`catatan-untuk-app.md`](catatan-untuk-app.md) |
| mengerjakan sisi backend | [`catatan-untuk-backend.md`](catatan-untuk-backend.md) |
| menjalankan server di laptop | [`nginx.md`](nginx.md) |
| butuh model data | [`erd.md`](erd.md) |

---

## Isi

### [`api.md`](api.md) — kontrak integrasi

Sumber kebenaran untuk klien. Seluruh endpoint REST, seluruh frame WebSocket,
alur multi-langkah, bentuk data persis, batasan, dan urutan pemanggilan saat
aplikasi dibuka.

**Setiap perubahan endpoint wajib tercatat di sini.** Daftar rutenya
diverifikasi terhadap `server/internal/transport/rest/router.go` dan dijalankan
lewat `server/scripts/smoke.ps1`.

### [`voice-rooms.md`](voice-rooms.md) — voice room

Punya dokumen sendiri karena ada satu hal yang mengubah cara klien dibangun:
suara tidak lewat backend ini, melainkan LiveKit. Backend hanya menerbitkan
token. Chat di dalam room pun efemeral — hilang bersama room-nya.

### [`app-backend-alignment.md`](app-backend-alignment.md) — peta layar

Tiap layar aplikasi dipetakan ke endpoint yang melayaninya, lengkap dengan
status: sudah ada, belum ada, atau sisi mana yang perlu mengerjakan.

### [`catatan-untuk-app.md`](catatan-untuk-app.md) — untuk tim Android

Catatan aplikasi, temuan dari log produksi, kesalahan implementasi yang pernah
terjadi beserta perbaikannya, dan akun uji.

### [`catatan-untuk-backend.md`](catatan-untuk-backend.md) — untuk tim backend

Ditulis dari sisi aplikasi: kontrak yang app andalkan, dan apa yang masih
ditunggu.

### [`nginx.md`](nginx.md) — menjalankan di laptop

Urutan startup dari laptop baru dinyalakan, restart, log, port forwarding,
firewall, dan troubleshooting.

### [`erd.md`](erd.md) — model data

ERD per domain, konvensi penamaan, dan yang sengaja tidak disimpan di Postgres.

---

## Di luar folder ini

| Berkas | Isi |
|---|---|
| [`../server/README.md`](../server/README.md) | arsitektur kode Go, aturan lapisan, cara build |
| `../server/api/openapi.yaml` | kontrak REST formal |
| `../server/api/asyncapi.yaml` | kontrak WebSocket formal |
| `../server/migrations/` | SQL: skema, RLS policy, fungsi RPC |

---

## Aturan pemeliharaan

1. **Dokumen baru masuk ke `docs/`**, bukan ke `server/`. Aplikasi mengacu ke
   folder ini; dokumen yang tersebar membuat rujukan mudah basi.
2. **Perubahan endpoint wajib ikut mengubah [`api.md`](api.md)** — tabel
   ringkasan rute dan bagian detailnya, di giliran yang sama.
3. **Verifikasi sebelum menyatakan sesuatu jalan.** Jalankan
   `server/scripts/smoke.ps1`; kalau ada yang gagal, tulis apa adanya.
4. Kalau dokumen berbeda dengan kode, **kode yang benar** — dan itu bug di
   dokumennya.
