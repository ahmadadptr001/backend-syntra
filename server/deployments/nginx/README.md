# nginx — reverse proxy Syntra

Konfigurasinya ada di [`nginx.conf`](nginx.conf).

**Panduan lengkap menjalankan proyek dari laptop baru dinyalakan ada di
[`../../nginx.md`](../../nginx.md)** — termasuk urutan startup, port forwarding,
firewall, dan troubleshooting.

## Ringkas

```powershell
cd C:\Users\user\Documents\PROJECTS\backend-syntra\server\deployments\nginx

nginx -p . -c nginx.conf            # jalankan
nginx -p . -c nginx.conf -t         # uji konfigurasi
nginx -p . -c nginx.conf -s reload  # muat ulang
nginx -p . -c nginx.conf -s quit    # hentikan
```

Alur: `internet → router :8081 → nginx :8081 → Go :8080`

## Yang perlu diperhatikan di nginx.conf

| Bagian | Kenapa ada |
|---|---|
| `map $http_upgrade $connection_upgrade` | tanpa ini upgrade WebSocket gagal dengan HTTP 400 |
| `proxy_read_timeout 3600s` di `/api/v1/ws` | nilai bawaan 60 detik memutus socket berkala, terlihat seperti gangguan jaringan acak |
| `proxy_buffering off` di rute yang sama | kalau menyala, nginx menahan frame sampai buffer penuh dan chat terasa tersendat |
| `limit_req 10r/s` | aplikasi belum punya rate limiting sendiri |
| `error_page` → JSON | penolakan tetap berbentuk JSON, bukan halaman HTML nginx |

TLS tidak ditangani di sini. Untuk HTTPS dibutuhkan nama domain — lihat bagian
6 di `nginx.md`.
