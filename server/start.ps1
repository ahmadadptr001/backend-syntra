# Menyalakan seluruh tumpukan Syntra di laptop ini.
#
#   powershell -ExecutionPolicy Bypass -File .\start.ps1
#   powershell -ExecutionPolicy Bypass -File .\start.ps1 -NoBuild   # pakai binary yang ada
#
# Skrip ini SELALU mengompilasi ulang dulu. Tanpa itu, `git pull` yang membawa
# perubahan Go tidak akan pernah terpasang: skrip hanya me-restart binary lama,
# server balas 404 untuk endpoint baru, dan app terlihat "rusak" tanpa sebab.
#
# Log:
#   logs\backend.log        stdout server Go (log terstruktur)
#   logs\backend.err.log    stderr server Go (panic, kegagalan fatal)
#   deployments\nginx\logs\ access.log dan error.log milik nginx

param(
    [switch]$NoBuild
)

$ErrorActionPreference = "Stop"

$root  = $PSScriptRoot          # ikut lokasi berkas, bukan path yang dipatok
$nginx = "$root\deployments\nginx"
$logs  = "$root\logs"
$bin   = "$root\bin\syntra.exe"

New-Item -ItemType Directory -Force $logs | Out-Null

# --- kompilasi ---------------------------------------------------------------
# Dibangun ke berkas SEMENTARA dulu. Kalau kompilasi gagal, server lama tetap
# jalan dan tidak ada yang tersentuh - jauh lebih baik daripada mematikan server
# lalu baru tahu kodenya tidak bisa dikompilasi.
if (-not $NoBuild) {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        Write-Host "Go tidak ditemukan di PATH. Pasang Go atau jalankan dengan -NoBuild." -ForegroundColor Red
        exit 1
    }

    Write-Host "Mengompilasi server Go..." -ForegroundColor Cyan
    $tmp = "$root\bin\syntra.new.exe"
    Remove-Item $tmp -Force -ErrorAction SilentlyContinue

    Push-Location $root
    & go build -trimpath -ldflags="-s -w" -o $tmp ./cmd/syntra
    $ok = $LASTEXITCODE -eq 0
    Pop-Location

    if (-not $ok -or -not (Test-Path $tmp)) {
        Write-Host "KOMPILASI GAGAL. Server lama dibiarkan jalan apa adanya." -ForegroundColor Red
        exit 1
    }
    Write-Host "Kompilasi berhasil." -ForegroundColor Green
} else {
    if (-not (Test-Path $bin)) {
        Write-Host "-NoBuild dipakai tapi $bin belum ada." -ForegroundColor Red
        exit 1
    }
    Write-Host "Melewati kompilasi (-NoBuild): memakai binary yang sudah ada." -ForegroundColor Yellow
    Write-Host "  dibuat: $((Get-Item $bin).LastWriteTime)"
}

# --- Redis -------------------------------------------------------------------
$memurai = Get-Service Memurai -ErrorAction SilentlyContinue
if (-not $memurai) {
    Write-Host "Layanan Memurai (Redis) tidak terpasang. Server akan gagal di /readyz." -ForegroundColor Red
    exit 1
}
if ($memurai.Status -ne 'Running') { Start-Service Memurai }

# --- server Go ---------------------------------------------------------------
# Hentikan yang lama dulu supaya port 8080 tidak bentrok DAN supaya berkas exe
# tidak terkunci saat ditukar.
Get-Process syntra -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Milliseconds 700

if (-not $NoBuild) {
    $tmp = "$root\bin\syntra.new.exe"
    if (Test-Path $bin) { Move-Item $bin "$root\bin\syntra-prev.exe" -Force }
    Move-Item $tmp $bin -Force
}

# Redirect WAJIB. Tanpa ini, proses tersembunyi membuang seluruh log-nya ke
# ruang hampa - dan saat ada yang salah, tidak ada apa pun untuk dibaca.
#
# Catatan: Start-Process menimpa berkas log, bukan menambahkan. Isinya selalu
# dari sesi yang sedang berjalan. Kalau butuh riwayat lintas sesi, arsipkan
# dulu sebelum restart.
Start-Process $bin `
    -WorkingDirectory $root `
    -WindowStyle Hidden `
    -RedirectStandardOutput "$logs\backend.log" `
    -RedirectStandardError  "$logs\backend.err.log"

# --- nginx -------------------------------------------------------------------
if (-not (Test-Path "$nginx\nginx.conf")) {
    Write-Host "nginx.conf tidak ada di $nginx" -ForegroundColor Red
    exit 1
}
if (-not (Get-Process nginx -ErrorAction SilentlyContinue)) {
    Start-Process nginx -ArgumentList "-p", ".", "-c", "nginx.conf" `
        -WorkingDirectory $nginx -WindowStyle Hidden
}

# --- tunggu siap -------------------------------------------------------------
# Menunggu kondisi nyata, bukan durasi tebakan. Start-Sleep tetap yang paling
# sering salah: 4 detik kadang kurang saat laptop sibuk, dan selalu kelamaan
# saat tidak.
$ready = $null
for ($i = 0; $i -lt 20; $i++) {
    Start-Sleep -Milliseconds 500
    if (-not (Get-Process syntra -ErrorAction SilentlyContinue)) {
        Write-Host ""
        Write-Host "Server MATI beberapa saat setelah start. Isi $logs\backend.err.log :" -ForegroundColor Red
        Get-Content "$logs\backend.err.log" -Tail 30 -ErrorAction SilentlyContinue
        exit 1
    }
    $ready = curl.exe -s -m 3 http://localhost:8081/readyz
    if ($ready -and $ready -match '"ready"\s*:\s*true') { break }
}

Write-Host ""
Write-Host "--- /readyz ---"
if ($ready) { Write-Host $ready } else { Write-Host "(tidak ada balasan dari :8081 - cek nginx)" -ForegroundColor Yellow }
Write-Host ""
Write-Host "binary      : $bin  ($((Get-Item $bin).LastWriteTime))"
Write-Host "log backend : $logs\backend.log"
Write-Host "log nginx   : $nginx\logs\access.log"
Write-Host "pantau live : Get-Content '$logs\backend.log' -Wait -Tail 30"
