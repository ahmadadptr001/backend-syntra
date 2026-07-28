# tunnel.ps1 - Cloudflare Tunnel untuk Syntra.
#
# Memberi backend ini SATU alamat HTTPS tetap (mis. https://api.contoh.com) yang
# tidak berubah walau laptop pindah Wi-Fi atau IP publik berganti. Laptop
# menyambung KELUAR ke Cloudflare, jadi TIDAK perlu port forwarding maupun
# membuka firewall.
#
# SEKALI, manual (lihat docs/cloudflare-tunnel.md):
#   1. Punya akun Cloudflare + satu domain (zone) di sana.
#   2. cloudflared tunnel login      -> buka browser, pilih domainnya.
#
# Lalu:
#   .\tunnel.ps1 -Setup -Hostname api.contoh.com   # sekali: buat tunnel + DNS + config
#   .\tunnel.ps1 -Run                              # tiap kali: jalankan tunnel (latar)
#   .\tunnel.ps1 -Status                           # cek jalan/tidak
#   .\tunnel.ps1 -Stop                             # hentikan
#
# Uji cepat tanpa domain/login (URL acak *.trycloudflare.com, berubah tiap jalan):
#   .\tunnel.ps1 -Quick
#
# Catatan: skrip ini sengaja ASCII-only. Windows PowerShell 5.1 membaca berkas
# tanpa BOM sebagai ANSI, dan karakter seperti em-dash bisa salah-terbaca jadi
# tanda kutip yang memecah parser.

param(
    [switch]$Setup,
    [switch]$Run,
    [switch]$Quick,
    [switch]$Status,
    [switch]$Stop,
    [string]$Hostname,
    [string]$Name     = "syntra",
    [string]$Local    = "http://localhost:8081",
    # QUIC (UDP) sering diblokir/dibatasi di jaringan rumah/seluler dan bikin
    # koneksi tunnel putus-putus (control stream failure). http2 (TCP) jauh
    # lebih tahan. Ganti ke "quic" atau "auto" hanya kalau jaringanmu mendukung.
    [string]$Protocol = "http2"
)

$ErrorActionPreference = "Stop"
$cfDir   = Join-Path $env:USERPROFILE ".cloudflared"
$cfg     = Join-Path $cfDir "config.yml"
$logsDir = Join-Path $PSScriptRoot "logs"
$log     = Join-Path $logsDir "cloudflared.log"

function Require-Cloudflared {
    if (-not (Get-Command cloudflared -ErrorAction SilentlyContinue)) {
        Write-Host "cloudflared belum terpasang. Pasang dengan:" -ForegroundColor Red
        Write-Host "  winget install --id Cloudflare.cloudflared"
        exit 1
    }
}

function Require-Login {
    if (-not (Test-Path (Join-Path $cfDir "cert.pem"))) {
        Write-Host "Belum login ke Cloudflare. Jalankan dulu (sekali):" -ForegroundColor Red
        Write-Host "  cloudflared tunnel login"
        Write-Host "Lalu ulangi perintah ini."
        exit 1
    }
}

function Get-TunnelId {
    param([string]$TunnelName)
    # cloudflared menulis ke stderr saat belum login; dengan ErrorActionPreference
    # Stop itu jadi terminating, jadi dibungkus try/catch dan dikembalikan null.
    $raw = $null
    try { $raw = & cloudflared tunnel list --output json 2>$null } catch { return $null }
    if (-not $raw) { return $null }
    try { $list = ($raw -join "`n") | ConvertFrom-Json } catch { return $null }
    foreach ($t in $list) { if ($t.name -eq $TunnelName) { return $t.id } }
    return $null
}

function Test-Origin {
    # Tunnel yang sehat di depan origin yang mati tetap balas 502, dan itu
    # terlihat persis seperti tunnelnya yang rusak. Cek dulu, sekali, di sini.
    $code = & curl.exe -s -o NUL -w "%{http_code}" -m 4 "$Local/healthz" 2>$null
    if ($code -ne "200") {
        Write-Host "PERINGATAN: $Local/healthz balas '$code', bukan 200." -ForegroundColor Yellow
        Write-Host "  Tunnel akan jalan tapi semua permintaan balas 502. Nyalakan dulu:" -ForegroundColor Yellow
        Write-Host "    .\start.ps1"
        Write-Host ""
    }
}

Require-Cloudflared

# --------------------------------------------------------------------------
if ($Setup) {
    Require-Login
    if (-not $Hostname) {
        Write-Host "Wajib sertakan -Hostname, mis:" -ForegroundColor Red
        Write-Host "  .\tunnel.ps1 -Setup -Hostname api.contoh.com"
        exit 1
    }

    # Buat tunnel kalau belum ada (idempoten, aman diulang).
    $id = Get-TunnelId $Name
    if (-not $id) {
        Write-Host "Membuat tunnel '$Name'..." -ForegroundColor Cyan
        cloudflared tunnel create $Name | Out-Host
        $id = Get-TunnelId $Name
    } else {
        Write-Host "Tunnel '$Name' sudah ada ($id), dipakai ulang." -ForegroundColor Yellow
    }
    if (-not $id) { Write-Host "Gagal mendapatkan id tunnel." -ForegroundColor Red; exit 1 }

    $cred = Join-Path $cfDir "$id.json"

    # Tulis config.yml ke lokasi default cloudflared. UTF-8 tanpa BOM supaya
    # parser YAML tidak tersandung. Disusun sebagai array baris lalu digabung.
    $yaml = @(
        "tunnel: $id",
        "credentials-file: $cred",
        "",
        "ingress:",
        "  - hostname: $Hostname",
        "    service: $Local",
        "  - service: http_status:404",
        ""
    ) -join "`r`n"
    [IO.File]::WriteAllText($cfg, $yaml)
    Write-Host "config.yml ditulis ke $cfg" -ForegroundColor Green

    # Arahkan DNS: buat CNAME hostname -> tunnel.cfargotunnel.com di zone-mu.
    Write-Host "Mengarahkan DNS $Hostname ..." -ForegroundColor Cyan
    cloudflared tunnel route dns $Name $Hostname | Out-Host

    Write-Host ""
    Write-Host "=== SELESAI setup. Dua hal berikutnya: ===" -ForegroundColor Green
    Write-Host "1) Tambahkan origin HTTPS ini ke server\.env pada HTTP_CORS_ORIGINS dan WS_ALLOWED_ORIGINS, lalu restart server Go:"
    Write-Host "     https://$Hostname" -ForegroundColor Cyan
    Write-Host "2) Jalankan tunnelnya:"
    Write-Host "     .\tunnel.ps1 -Run"
    Write-Host ""
    Write-Host "Arahkan aplikasi Android ke:  https://$Hostname   (WS: wss://$Hostname/api/v1/ws)"
    exit 0
}

# --------------------------------------------------------------------------
if ($Run) {
    Require-Login
    if (-not (Test-Path $cfg)) {
        Write-Host "config.yml belum ada. Jalankan '-Setup -Hostname ...' dulu." -ForegroundColor Red
        exit 1
    }
    New-Item -ItemType Directory -Force $logsDir | Out-Null
    Test-Origin

    # Hentikan yang lama supaya tidak dobel.
    Get-Process cloudflared -ErrorAction SilentlyContinue | Stop-Process -Force
    Start-Sleep -Milliseconds 300

    Start-Process cloudflared -ArgumentList "tunnel", "run", "--protocol", $Protocol, $Name `
        -WindowStyle Hidden `
        -RedirectStandardOutput $log `
        -RedirectStandardError  "$log.err"

    Start-Sleep -Seconds 3
    if (Get-Process cloudflared -ErrorAction SilentlyContinue) {
        Write-Host "Tunnel '$Name' berjalan. Log: $log" -ForegroundColor Green
        Write-Host "pantau live : Get-Content '$log' -Wait -Tail 30"
    } else {
        Write-Host "Tunnel gagal start. Cek $log.err" -ForegroundColor Red
    }
    exit 0
}

# --------------------------------------------------------------------------
# Quick tunnel: TANPA akun/domain/login. Dapat URL HTTPS acak *.trycloudflare.com
# yang langsung jalan. Untuk uji cepat saja: URL-nya BERUBAH tiap dijalankan dan
# tidak untuk dipakai tetap. Untuk alamat tetap, pakai -Setup + -Run.
if ($Quick) {
    New-Item -ItemType Directory -Force $logsDir | Out-Null
    $qlog = Join-Path $logsDir "quicktunnel.log"
    Test-Origin

    Get-Process cloudflared -ErrorAction SilentlyContinue | Stop-Process -Force
    Start-Sleep -Milliseconds 300

    Start-Process cloudflared -ArgumentList "tunnel", "--no-autoupdate", "--protocol", $Protocol, "--url", $Local `
        -WindowStyle Hidden -RedirectStandardOutput "$qlog.out" -RedirectStandardError $qlog

    # Versi cloudflared berbeda-beda menaruh banner URL di stderr atau stdout.
    # Dicari di keduanya supaya skrip tidak menggantung 25 detik lalu menyerah
    # padahal tunnelnya sudah jalan.
    $both = @($qlog, "$qlog.out")

    Write-Host "Menunggu URL quick tunnel (URL ini ACAK & berubah tiap dijalankan)..." -ForegroundColor Yellow
    $url = $null
    for ($i = 0; $i -lt 25; $i++) {
        Start-Sleep -Seconds 1
        $present = $both | Where-Object { Test-Path $_ }
        if ($present) {
            $m = Select-String -Path $present -Pattern 'https://[a-z0-9-]+\.trycloudflare\.com' -AllMatches | Select-Object -First 1
            if ($m) { $url = $m.Matches[0].Value; break }
        }
        if (-not (Get-Process cloudflared -ErrorAction SilentlyContinue)) {
            Write-Host "cloudflared mati sebelum memberi URL. Isi $qlog :" -ForegroundColor Red
            Get-Content $qlog -Tail 20 -ErrorAction SilentlyContinue
            exit 1
        }
    }
    if (-not $url) { Write-Host "URL belum muncul. Cek $qlog" -ForegroundColor Red; exit 1 }

    # Tunggu koneksi edge benar-benar terdaftar, kalau tidak akses awal balas 1033.
    for ($i = 0; $i -lt 15; $i++) {
        $present = $both | Where-Object { Test-Path $_ }
        if ($present -and (Select-String -Path $present -Pattern 'Registered tunnel connection' -Quiet)) { break }
        Start-Sleep -Seconds 1
    }

    Write-Host ""
    Write-Host "API (sementara): $url" -ForegroundColor Green
    Write-Host "  REST : $url/api/v1/..."
    Write-Host "  WS   : $($url -replace '^https','wss')/api/v1/ws"
    Write-Host "  cek  : $url/healthz"
    Write-Host "Berhenti: .\tunnel.ps1 -Stop"
    exit 0
}

# --------------------------------------------------------------------------
if ($Status) {
    $p = Get-Process cloudflared -ErrorAction SilentlyContinue
    if ($p) {
        # StartTime melempar Access denied kalau prosesnya milik sesi lain.
        $since = try { $p[0].StartTime } catch { "?" }
        Write-Host "cloudflared JALAN (PID $($p[0].Id), mulai $since)" -ForegroundColor Green
    } else {
        Write-Host "cloudflared TIDAK jalan." -ForegroundColor Yellow
    }

    $code = & curl.exe -s -o NUL -w "%{http_code}" -m 4 "$Local/healthz" 2>$null
    if ($code -eq "200") { Write-Host "origin $Local : OK" -ForegroundColor Green }
    else                 { Write-Host "origin $Local : $code (jalankan .\start.ps1)" -ForegroundColor Yellow }

    $id = Get-TunnelId $Name
    if ($id) { cloudflared tunnel info $Name 2>$null | Out-Host }
    exit 0
}

# --------------------------------------------------------------------------
if ($Stop) {
    $p = Get-Process cloudflared -ErrorAction SilentlyContinue
    if (-not $p) { Write-Host "cloudflared memang tidak jalan." -ForegroundColor Yellow; exit 0 }
    $p | Stop-Process -Force
    Write-Host "cloudflared dihentikan." -ForegroundColor Green
    exit 0
}

Write-Host "Pakai salah satu: -Setup -Hostname HOST | -Run | -Quick | -Status | -Stop"
Write-Host "Lihat docs/cloudflare-tunnel.md untuk langkah lengkap."
