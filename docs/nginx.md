# Menjalankan Syntra dengan nginx — dari laptop baru dinyalakan

Runbook praktis. Ikuti dari atas ke bawah setiap kali laptop baru dihidupkan.

```
internet ──► router 36.65.127.163:8081 ──┐   (port forwarding)
                                          ├──► nginx :8081 ──► Go :8080
LAN      ──► 192.168.1.174:8081 ─────────┘                        │
                                                        ┌─────────┴─────────┐
                                                     Supabase           Redis :6379
                                                     (online)           (Memurai, lokal)
```

nginx menangani reverse proxy, upgrade WebSocket, dan rate limiting.
Router meneruskan trafik dari internet ke laptop ini.

---

## ⚡ Paling cepat — kalau laptop sudah pernah disetup

Backend ini terdiri dari **tiga proses**: Redis (Memurai), server Go, dan nginx.
Kamu tidak perlu menyalakannya satu per satu — `start.ps1` melakukan ketiganya
sekaligus lalu memverifikasi:

```powershell
cd C:\Users\user\Documents\PROJECTS\backend-syntra\server
powershell -ExecutionPolicy Bypass -File .\start.ps1
```

**Berhasil** kalau baris terakhir menampilkan:

```json
{"redis":"ok","supabase":"ok","ready":true}
```

Itu saja untuk menjalankan. Kalau kamu baru **mengubah kode Go**, build dulu
sebelum `start.ps1`:

```powershell
go build -o bin\syntra.exe .\cmd\syntra
```

`start.ps1` aman dijalankan berulang — ia menghentikan proses lama sebelum
menyalakan yang baru, jadi ini juga cara **restart**. Selesai. Bagian-bagian di
bawah hanya kamu butuhkan saat pertama kali menyiapkan laptop, saat membuka
akses dari internet, atau saat ada yang bermasalah.

---

## Prasyarat — sekali pasang di laptop baru

Sebelum `start.ps1` bisa jalan, empat hal ini harus ada. Cek dulu; kalau semua
lolos, langsung ke Quick Start di atas.

| Yang dibutuhkan | Cek | Kalau belum ada |
|---|---|---|
| **Go** (untuk build server) | `go version` | pasang dari [go.dev/dl](https://go.dev/dl/) |
| **nginx** | `nginx -v` | pasang, pastikan `nginx` ada di PATH |
| **Memurai** (Redis untuk Windows) | `Get-Service Memurai` | pasang dari [memurai.com](https://www.memurai.com/); ia otomatis jadi service |
| **Berkas `.env` sudah terisi** | `Test-Path server\.env` | `Copy-Item server\.env.example server\.env`, lalu isi `SUPABASE_*`, `REDIS_URL`, dan (untuk panggilan) `LIVEKIT_*` |

Build pertama kali sekaligus mengunduh dependensi Go:

```powershell
cd C:\Users\user\Documents\PROJECTS\backend-syntra\server
go build -o bin\syntra.exe .\cmd\syntra
```

> **Supabase & migrasi database.** Server bicara ke Supabase online lewat HTTP —
> tidak ada database lokal yang perlu dinyalakan. Tapi fitur-fitur baru butuh
> fungsi SQL-nya dijalankan dulu di **Supabase → SQL Editor** (berkas di
> `server/migrations/`). Tanpa itu, endpoint terkait membalas `404` walau server
> jalan normal.

---

## 0. Yang sudah otomatis jalan

Tidak perlu disentuh — berjalan sebagai Windows Service dengan startup
**Automatic**:

| Service | Fungsi | Cek |
|---|---|---|
| `Memurai` | Redis di port 6379 | `Get-Service Memurai` |

Kalau mati: `Start-Service Memurai`

Yang **harus dinyalakan manual** setiap kali: server Go dan nginx.

---

## 1. Nyalakan server Go

```powershell
cd C:\Users\user\Documents\PROJECTS\backend-syntra\server
.\bin\syntra.exe
```

Kalau ada perubahan kode sejak terakhir kali, build ulang dulu:

```powershell
go build -o bin\syntra.exe .\cmd\syntra
```

Berhasil kalau muncul:

```
level=INFO msg="server berjalan" addr=:8080 ws_path=/api/v1/ws env=production ...
```

Perhatikan `env_file` di baris itu — memastikan `.env` yang terbaca memang yang kamu kira.

> Jendela ini harus tetap terbuka. Menutupnya = server mati.
>
> Untuk latar belakang, **sertakan redirect log** — tanpa itu seluruh log hilang
> dan tidak ada yang bisa dibaca saat terjadi masalah:
>
> ```powershell
> Start-Process .\bin\syntra.exe -WorkingDirectory (Get-Location) -WindowStyle Hidden `
>   -RedirectStandardOutput logs\backend.log -RedirectStandardError logs\backend.err.log
> ```
>
> Lebih praktis: pakai `start.ps1` di bagian 4, yang sudah melakukannya.

## 2. Nyalakan nginx

```powershell
cd C:\Users\user\Documents\PROJECTS\backend-syntra\server\deployments\nginx
nginx -p . -c nginx.conf
```

nginx tidak mencetak apa pun kalau berhasil — itu normal. Pastikan dengan:

```powershell
Get-Process nginx        # harus ada 2 proses: master + worker
```

Perintah pengelolaan lain, semuanya dari folder yang sama:

```powershell
nginx -p . -c nginx.conf -t        # uji konfigurasi tanpa menjalankan
nginx -p . -c nginx.conf -s reload # muat ulang setelah nginx.conf diubah
nginx -p . -c nginx.conf -s quit   # hentikan dengan rapi
```

Log ada di `deployments/nginx/logs/`.

## 3. Verifikasi

```powershell
curl.exe http://localhost:8081/healthz
curl.exe http://localhost:8081/readyz
```

`/readyz` harus menyebut `"supabase":"ok"` dan `"redis":"ok"`.

Uji endpoint ber-token — ambil JWT dan panggil sekaligus:

```powershell
$cfg = @{}; foreach ($l in [IO.File]::ReadAllLines("C:\Users\user\Documents\PROJECTS\backend-syntra\server\.env")) {
  $l=$l.TrimStart([char]0xFEFF).Trim(); if(!$l -or $l.StartsWith('#')){continue}
  $i=$l.IndexOf('='); if($i -lt 1){continue}
  $v=$l.Substring($i+1); $j=$v.IndexOf(' #'); if($j -ge 0){$v=$v.Substring(0,$j)}
  $cfg[$l.Substring(0,$i).Trim()]=$v.Trim() }

$jwt = (Invoke-RestMethod "$($cfg['SUPABASE_URL'])/auth/v1/token?grant_type=password" `
  -Headers @{apikey=$cfg['SUPABASE_ANON_KEY']} -Method Post -ContentType 'application/json' `
  -Body '{"email":"admin@syntra.app","password":"admin123"}').access_token

Invoke-RestMethod "http://localhost:8081/api/v1/conversations" -Headers @{Authorization="Bearer $jwt"}
```

Token berlaku 1 jam.

---

## 4. Skrip sekali jalan — `start.ps1`

Sudah tersedia di `server/start.ps1`. Ia menyalakan Memurai, server Go, dan
nginx sekaligus, lalu memverifikasi `/readyz`.

```powershell
cd C:\Users\user\Documents\PROJECTS\backend-syntra\server
powershell -ExecutionPolicy Bypass -File .\start.ps1
```

Skrip ini juga **mengarahkan log ke berkas**. Tanpa itu, proses yang berjalan
tersembunyi membuang seluruh log-nya ke ruang hampa, dan saat ada yang salah
tidak ada apa pun untuk dibaca:

| Berkas | Isi |
|---|---|
| `logs\backend.log` | stdout server Go (log terstruktur) |
| `logs\backend.err.log` | stderr (panic, kegagalan fatal) |
| `deployments\nginx\logs\access.log` | akses nginx |
| `deployments\nginx\logs\error.log` | error nginx |

---

## 4b. Restart

### Restart semuanya — cara biasa

`start.ps1` **aman dijalankan berulang kali**: ia menghentikan server lama lebih
dulu, jadi tidak akan bentrok di port 8080.

```powershell
cd C:\Users\user\Documents\PROJECTS\backend-syntra\server
powershell -ExecutionPolicy Bypass -File .\start.ps1
```

Berhasil kalau muncul `{"redis":"ok","supabase":"ok","ready":true}`.

### Restart server Go saja

Yang paling sering dibutuhkan — setelah mengubah kode atau `.env`:

```powershell
cd C:\Users\user\Documents\PROJECTS\backend-syntra\server

go build -o bin\syntra.exe .\cmd\syntra          # kalau kode berubah
Get-Process syntra -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Process .\bin\syntra.exe -WorkingDirectory (Get-Location) -WindowStyle Hidden `
  -RedirectStandardOutput logs\backend.log -RedirectStandardError logs\backend.err.log
```

nginx tidak perlu disentuh — ia hanya meneruskan trafik, jadi backend yang
berganti proses tetap dilayani.

> **Perubahan `.env` hanya terbaca saat startup.** Mengeditnya tanpa restart
> tidak berpengaruh sama sekali.

### Reload nginx saja

Setelah mengubah `nginx.conf`. Pakai `reload`, bukan restart — koneksi yang
sedang berjalan tidak terputus:

```powershell
cd C:\Users\user\Documents\PROJECTS\backend-syntra\server\deployments\nginx
nginx -p . -c nginx.conf -t        # uji dulu
nginx -p . -c nginx.conf -s reload
```

Jangan lewati `-t`. Reload dengan konfigurasi rusak akan ditolak dan nginx tetap
memakai yang lama — tetapi kalau nginx sempat berhenti, ia tidak akan bisa
menyala lagi sampai konfigurasinya benar.

### Memaksa berhenti kalau macet

```powershell
Get-Process syntra,nginx -ErrorAction SilentlyContinue | Stop-Process -Force
```

Lalu jalankan `start.ps1`. Cara ini melewati graceful shutdown, jadi koneksi
WebSocket yang sedang aktif diputus tanpa pemberitahuan — pakai hanya kalau
cara normal gagal.

### Memastikan restart benar-benar terjadi

```powershell
Get-Process syntra,nginx | Select-Object Id,ProcessName,StartTime
```

Periksa `StartTime`. Kalau masih menunjukkan waktu lama, prosesnya belum
berganti — dan server lama yang masih hidup akan diam-diam terus melayani
permintaan dengan kode serta konfigurasi lama. Ini pemeriksaan yang paling
sering terlewat.

Cek juga baris awal log:

```powershell
Get-Content logs\backend.log | Select-Object -First 5
```

`env_file` pada baris `server berjalan` menunjukkan `.env` mana yang terbaca —
berguna saat nilai konfigurasi ternyata bukan yang kamu kira.
---

## 5. Membuka akses dari luar

Dua langkah, keduanya **sekali saja** — tapi harus dua-duanya. Melewatkan salah
satu membuat koneksi dari luar gagal tanpa pesan yang jelas.

### Langkah 1 — Windows Firewall

Buka PowerShell **sebagai Administrator**:

```powershell
New-NetFirewallRule -DisplayName "Syntra nginx 8081" -Direction Inbound `
  -Protocol TCP -LocalPort 8081 -Action Allow -Profile Private,Public
```

`Public` disertakan karena profil Wi-Fi laptop ini terdeteksi sebagai Public,
dan Windows memblokir semua inbound di profil itu.

Setelah ini, perangkat di Wi-Fi yang sama sudah bisa mengakses
`http://192.168.1.174:8081` — cukup untuk menguji klien Android.

### Langkah 2 — Port forwarding di router

Buka `http://192.168.1.1` di browser, login, cari menu **Port Forwarding** /
**Virtual Server** / **NAT** (istilahnya berbeda-beda tiap merek). Tambahkan:

| Kolom | Nilai |
|---|---|
| Protokol | TCP |
| Port eksternal | `8081` |
| IP internal | `192.168.1.174` |
| Port internal | `8081` |

Setelah tersimpan, alamat publiknya:

```
http://36.65.127.163:8081
```

### Uji dari luar

Hairpin NAT sering tidak bekerja, jadi menguji dari laptop ini sendiri bisa
memberi hasil menyesatkan. Uji dari **jaringan lain** — paling gampang lewat
data seluler di ponsel, matikan Wi-Fi:

```
http://36.65.127.163:8081/healthz
```

### Kalau tidak bisa dihubungi dari luar

Urutkan pemeriksaannya seperti ini, jangan diacak:

1. **IP laptop berubah?** `ipconfig`. DHCP router bisa memberi alamat berbeda
   setelah reboot, dan aturan forwarding jadi menunjuk ke mesin yang salah.
   Pasang DHCP reservation di router supaya `192.168.1.174` tetap.
2. **IP publik berubah?** IndiHome memberi IP dinamis — bisa berganti setelah
   router restart. Cek: `Invoke-RestMethod https://api.ipify.org?format=json`
3. **ISP memblokir port masuk?** Sebagian layanan residensial memblokir port
   rendah (80, 443). Port 8081 biasanya aman, tapi kalau semua langkah di atas
   benar dan tetap tidak tembus, ini penyebab yang tersisa.

---

## 6. Dua batasan yang perlu disadari

> **Kedua batasan di bawah hilang kalau memakai Cloudflare Tunnel** — alamat
> HTTPS tetap, tanpa port forwarding, kebal IP dinamis. Panduannya di
> [`cloudflare-tunnel.md`](cloudflare-tunnel.md). Bagian di bawah tetap relevan
> untuk mode port-forwarding langsung.


**Trafiknya HTTP polos, bukan HTTPS.** JWT pengguna melintas dalam bentuk yang
bisa dibaca siapa pun di jalur jaringan. Untuk demo tertutup masih bisa
diterima; untuk dipakai orang lain, tidak.

TLS butuh nama domain — Let's Encrypt tidak menerbitkan sertifikat untuk alamat
IP. Kalau nanti dibutuhkan: ambil domain gratis (DuckDNS) atau domain murah,
arahkan ke IP publik ini, lalu terbitkan sertifikat dan tambahkan blok
`listen 443 ssl` di `nginx.conf`. DuckDNS sekaligus menyelesaikan masalah IP
dinamis, karena ia memperbarui alamatnya otomatis.

**IP publik ini dinamis.** `36.65.127.163` bisa berganti kapan saja router
restart. Setiap kali berganti, `HTTP_CORS_ORIGINS` dan `WS_ALLOWED_ORIGINS` di
`.env` ikut harus diperbarui, dan klien harus diarahkan ulang. DDNS
menghilangkan kerepotan ini.

---

## 7. Mode publik vs mode lokal

`.env` sekarang disetel untuk **mode publik**:

```bash
APP_ENV=production
AUTH_DEV_BYPASS=false
HTTP_CORS_ORIGINS=http://36.65.127.163:8081,http://192.168.1.174:8081,http://localhost:8081
WS_ALLOWED_ORIGINS=http://36.65.127.163:8081,http://192.168.1.174:8081,http://localhost:8081
```

Dengan `APP_ENV=production`, server **menolak start** kalau `AUTH_DEV_BYPASS`
masih `true` atau `HTTP_CORS_ORIGINS` kosong. Itu memang tujuannya — salah
konfigurasi berhenti di startup, bukan jadi celah yang baru ketahuan setelah
dipakai orang.

Untuk kembali ke mode pengembangan santai:

```powershell
Copy-Item .env.pre-funnel .env -Force
```

Bedanya: `APP_ENV=development` dan `AUTH_DEV_BYPASS=true`, sehingga header
`X-Debug-User` berlaku sebagai identitas tanpa perlu login. **Jangan pernah
memakai setelan itu sambil port forwarding aktif** — satu perintah `curl` sudah
cukup bagi siapa pun untuk menyamar jadi pengguna mana pun.

---

## 8. Mematikan

```powershell
cd C:\Users\user\Documents\PROJECTS\backend-syntra\server\deployments\nginx
nginx -p . -c nginx.conf -s quit
Get-Process syntra -ErrorAction SilentlyContinue | Stop-Process -Force
```

Memurai biarkan saja — ia service dan tidak mengganggu.

Untuk **restart**, bukan mematikan, lihat bagian 4b — `start.ps1` sudah
menangani penghentian proses lama sendiri.

Kalau ingin menutup akses dari internet tanpa mematikan apa pun: hapus aturan
port forwarding di router. Itu satu-satunya pintu dari luar.

---

## 9. Kalau ada masalah

| Gejala | Penyebab | Solusi |
|---|---|---|
| `bind: Only one usage of each socket address` | server lama masih jalan | `Get-Process syntra \| Stop-Process -Force`, lalu `start.ps1` |
| Perubahan kode/`.env` tidak berpengaruh | proses lama masih melayani | cek `StartTime` — lihat bagian 4b |
| `logs\backend.log` kosong | dijalankan tanpa redirect | pakai `start.ps1`, atau sertakan `-RedirectStandardOutput` |
| nginx: `could not open error log file` | folder `logs/` belum ada | `New-Item -ItemType Directory logs` di folder nginx |
| `502` dari nginx | server Go tidak jalan di :8080 | ulangi langkah 1 |
| `503` di `/readyz` | Supabase atau Redis tidak tersambung | lihat isi `dependencies` di respons |
| `401 token tidak disertakan` | request tanpa header `Authorization` | browser tidak bisa mengirim header — pakai curl/Postman. `?token=` hanya berlaku untuk `/api/v1/ws` |
| WebSocket gagal, `403` | Origin tidak ada di `WS_ALLOWED_ORIGINS` | tambahkan origin klien, restart server |
| WebSocket putus tiap ~60 detik | `proxy_read_timeout` bawaan | sudah 3600s di `nginx.conf`; pastikan nginx sudah reload |
| Chat terasa tersendat | `proxy_buffering` menyala | sudah `off` di rute `/api/v1/ws` |
| Server menolak start, keluhan config | `APP_ENV=production` + setelan tidak lengkap | baca pesannya — ia menyebut kunci mana yang salah |
| Dari LAN bisa, dari internet tidak | port forwarding belum ada / IP laptop berubah | lihat bagian 5 |

---

## 10. Satu hal yang tidak bisa diperbaiki konfigurasi

Ini laptop, bukan server. Begitu laptop tidur, mati, atau pindah jaringan,
layanannya hilang — dan IP publiknya bisa berganti. Cocok untuk demo dan
pengujian klien Kotlin; untuk sesuatu yang diandalkan orang lain, backend ini
perlu pindah ke VPS.
