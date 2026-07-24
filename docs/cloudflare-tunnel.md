# Alamat tetap dengan Cloudflare Tunnel

Tujuan: satu alamat HTTPS yang **tidak pernah berubah** — mis.
`https://api.syntra.contoh.com` — supaya tidak perlu ganti IP di aplikasi tiap
kali laptop pindah Wi-Fi atau IP publik IndiHome berganti.

```
HP (Wi-Fi / data seluler mana pun)
      │  https://api.syntra.contoh.com
      ▼
  Cloudflare edge  ── TLS/HTTPS gratis, otomatis
      │  tunnel keluar (laptop yang menyambung, bukan sebaliknya)
      ▼
  cloudflared (laptop) ──► nginx :8081 ──► Go :8080
```

Kenapa ini, bukan port forwarding + DuckDNS:

- **Tidak perlu port forwarding** di router, **tidak perlu buka Windows
  Firewall.** Laptop yang menyambung keluar ke Cloudflare.
- **Jalan dari jaringan apa pun** — Wi-Fi rumah, kafe, atau data seluler HP.
  Tidak bergantung pada IP publik, jadi kebal CGNAT IndiHome.
- **HTTPS gratis & otomatis.** JWT tidak lagi melintas sebagai teks polos
  (dua batasan di [`nginx.md`](nginx.md) §6 hilang sekaligus).
- **Alamatnya tetap** selama nama domainnya tetap.

`cloudflared` **sudah terpasang** di laptop ini. Yang tersisa hanya langkah
akun/domain (sekali) lalu dua perintah skrip.

---

## Uji cepat tanpa domain (opsional) — `-Quick`

Kalau hanya ingin **link API yang langsung jalan sekarang** tanpa repot akun/
domain, pakai quick tunnel. Backend (Go + nginx) harus sudah jalan dulu.

```powershell
cd C:\Users\user\Documents\PROJECTS\syntra\backend-syntra\server
.\tunnel.ps1 -Quick
```

Ia mencetak URL HTTPS acak seperti `https://kata-acak.trycloudflare.com`.

> **Ini bukan alamat tetap.** URL-nya **berubah setiap kali dijalankan** dan mati
> begitu laptop tidur/cloudflared berhenti. Cocok untuk uji sekali jalan, bukan
> untuk dipasang di aplikasi secara permanen — untuk itu pakai `-Setup` + `-Run`
> dengan domain (di bawah). Aplikasi Android (OkHttp) tidak mengirim header
> `Origin`, jadi REST & WebSocket tetap jalan walau URL acak ini belum terdaftar
> di `HTTP_CORS_ORIGINS`/`WS_ALLOWED_ORIGINS`.

---

## Langkah 1 — punya domain di Cloudflare (sekali, manual)

Cloudflare Tunnel butuh satu **domain (zone)** di akun Cloudflare-mu. Pilih
salah satu:

| Cara | Biaya | Catatan |
|---|---|---|
| **Domain murah** (Cloudflare Registrar, Namecheap, dll.) | ~Rp15–150rb/th | **Paling praktis** — langsung aktif, tidak ada masa tunggu. Cloudflare Registrar jual seharga modal. |
| **`eu.org` gratis** (`nic.eu.org`) | Gratis, permanen | Benar-benar gratis, tapi persetujuan bisa 1–7 hari. Setelah aktif, delegasikan nameserver-nya ke Cloudflare. |

Langkahnya:

1. Buat akun gratis di <https://dash.cloudflare.com>.
2. Tambahkan domainmu ke Cloudflare (**Add a site**), pilih paket **Free**.
3. Ganti **nameserver** domain di registrar-nya ke dua NS yang Cloudflare beri.
   Tunggu sampai status domain di Cloudflare jadi **Active** (biasanya beberapa
   menit–jam).

> Hostname yang akan dipakai backend nanti: **`api.<domainmu>`**
> (mis. `api.syntra.contoh.com`). Tidak perlu membuat DNS record-nya manual —
> `tunnel.ps1 -Setup` yang membuatnya.

## Langkah 2 — login cloudflared (sekali, manual)

Di PowerShell biasa:

```powershell
cloudflared tunnel login
```

Browser terbuka → login → **pilih domain** yang tadi kamu tambahkan. Ini
menyimpan sertifikat ke `~/.cloudflared/cert.pem`. Cukup sekali per laptop.

## Langkah 3 — setup tunnel (sekali)

```powershell
cd C:\Users\user\Documents\PROJECTS\syntra\backend-syntra\server
.\tunnel.ps1 -Setup -Hostname api.<domainmu>
```

Skrip ini otomatis: membuat tunnel `syntra`, menulis `~/.cloudflared/config.yml`
(hostname → `http://localhost:8081`), dan membuat DNS record
`api.<domainmu> → <tunnel>.cfargotunnel.com`.

Setelah selesai ia mencetak **origin HTTPS** yang harus kamu tambahkan ke
`.env` (langkah 4).

## Langkah 4 — daftarkan origin di `.env`

Server menolak Origin yang tidak terdaftar (CORS & WebSocket). Tambahkan alamat
HTTPS baru ke **dua** baris di `server\.env`:

```bash
HTTP_CORS_ORIGINS=...,https://api.<domainmu>
WS_ALLOWED_ORIGINS=...,https://api.<domainmu>
```

Lalu **restart server Go** (perubahan `.env` hanya terbaca saat start):

```powershell
Get-Process syntra -ErrorAction SilentlyContinue | Stop-Process -Force
# lalu jalankan start.ps1 / bin\syntra.exe seperti biasa
```

## Langkah 5 — jalankan tunnel

Tiap kali laptop menyala (setelah nginx + Go jalan):

```powershell
.\tunnel.ps1 -Run       # jalan di latar, log ke deployments\nginx\..\logs
.\tunnel.ps1 -Status    # cek jalan/tidak
.\tunnel.ps1 -Stop      # hentikan
```

Uji dari HP (matikan Wi-Fi, pakai data seluler):

```
https://api.<domainmu>/healthz
https://api.<domainmu>/readyz
```

---

## Yang berubah di aplikasi Android

Cukup ganti base URL — sekali, dan tidak akan berubah lagi:

```
REST : https://api.<domainmu>/api/v1/...
WS   : wss://api.<domainmu>/api/v1/ws
```

Tidak ada lagi `http://192.168.1.x:8081` atau IP publik yang berganti-ganti.
Karena sudah HTTPS/WSS, Android juga tidak perlu izin cleartext traffic lagi.

---

## Kalau ada masalah

| Gejala | Penyebab | Solusi |
|---|---|---|
| `-Setup` bilang "belum login" | `cert.pem` belum ada | jalankan `cloudflared tunnel login` |
| DNS route gagal | domain belum **Active** di Cloudflare | tunggu status Active, ulangi `-Setup` |
| `502`/`error 1033` dari Cloudflare | nginx/Go tidak jalan, atau tunnel mati | pastikan `start.ps1` jalan, lalu `.\tunnel.ps1 -Run` |
| WebSocket gagal `403` | origin belum terdaftar | tambahkan `https://api.<domainmu>` ke `WS_ALLOWED_ORIGINS`, restart Go |
| Alamat lama masih dipakai | build/klien belum diarahkan | ganti base URL aplikasi ke `https://api.<domainmu>` |

> **Ini tetap laptop, bukan server.** Begitu laptop tidur/mati, tunnel ikut
> mati dan alamatnya tidak menjawab (Cloudflare balas `1033`). Alamatnya tetap,
> tapi ketersediaannya mengikuti laptop. Untuk yang benar-benar selalu-hidup,
> pindahkan backend ke VPS — `deployments/caddy/Caddyfile` sudah disiapkan untuk
> itu (TLS otomatis dari nama domain yang sama).
