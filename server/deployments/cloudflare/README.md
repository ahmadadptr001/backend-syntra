# CDN media lewat Cloudflare (memangkas egress Supabase)

Tujuan: menyajikan semua media (avatar, story, reel, lampiran chat) dari **edge-cache
Cloudflare**, bukan langsung dari Supabase Storage. Setelah objek ter-cache, Supabase
hanya kena **satu tarikan** (cache-fill); sisanya dilayani Cloudflare **gratis**. Ini
solusi untuk limit **cached egress** Supabase.

File yang bekerja sama:
- `media-proxy.js` — Worker yang memetakan `cdn.syntra.fun/media/<key>` → objek publik Supabase, lalu Cloudflare men-cache-nya.
- Backend: `PublicURL()` (config `SUPABASE_MEDIA_CDN_BASE`) yang mengirim URL `cdn.syntra.fun` ke app.

Backend & app **tidak perlu diubah lagi** — app cuma memakai URL apa pun yang dikirim backend (dan sudah punya cache lokal sendiri: `VideoCache` + Coil).

---

## Urutan (JANGAN dibalik)

### 1. Deploy Worker
**Cara A — Dashboard (tanpa CLI):**
1. Cloudflare Dashboard → **Workers & Pages** → **Create** → **Create Worker**.
2. Nama: `syntra-media-proxy` → **Deploy** (isi bawaan) → **Edit code**.
3. Hapus semua, tempel isi `media-proxy.js`, **Deploy**.
4. Worker → **Settings** → **Domains & Routes** → **Add** → **Custom domain**: `cdn.syntra.fun`.
   (Cloudflare otomatis membuat DNS + route. Zona `syntra.fun` harus di akun yang sama — sama seperti `api.syntra.fun`.)

**Cara B — CLI (wrangler):**
```
npm i -g wrangler
cd server/deployments/cloudflare
wrangler login
wrangler deploy
```
(Route `cdn.syntra.fun/*` sudah diset di `wrangler.toml`.)

### 2. Verifikasi Worker live
Ambil satu storage key yang ada (mis. dari kolom `media_assets.storage_key`, contoh
`image/<uid>/<mediaid>.jpg`), lalu:
```
curl -I "https://cdn.syntra.fun/media/<key>"          # harus 200
curl -I "https://cdn.syntra.fun/media/<key>" | findstr cf-cache-status   # tarikan kedua: HIT
```
`cf-cache-status: HIT` di tarikan kedua = cache jalan (egress Supabase berhenti untuk objek itu).

### 3. Nyalakan di backend
Setelah Worker terbukti live, baru set env dan restart:
```
# .env
SUPABASE_MEDIA_CDN_BASE=https://cdn.syntra.fun
```
Lalu rebuild + restart (ingat: build ke bin\syntra.exe, lihat memori deploy gotcha):
```
go build -trimpath -ldflags="-s -w" -o bin/syntra.exe ./cmd/syntra
.\start.ps1
```
Sejak ini, semua `avatar_url` / `media_url` yang dikirim backend berbentuk
`https://cdn.syntra.fun/media/...`.

> ⚠️ **Jangan set env di langkah 3 sebelum langkah 1–2 selesai.** Kalau URL menunjuk
> CDN yang belum ada, media akan rusak.

---

## Catatan
- **Bucket `media` harus publik** (memang sudah — backend memakai URL publik).
- Objek lama tetap bisa diakses lewat URL Supabase langsung; app hanya beralih ke CDN
  untuk URL BARU yang dikirim backend setelah env aktif. Cache lokal app akan menarik
  ulang sekali ke URL CDN, lalu berhenti.
- Egress **bulan berjalan** yang sudah terlanjur tidak berkurang — ini mencegah bulan
  depan. Untuk pulih sekarang: tunggu reset kuota / upgrade di Supabase.
- Rollback: kosongkan `SUPABASE_MEDIA_CDN_BASE`, rebuild, restart → kembali ke Supabase langsung.
