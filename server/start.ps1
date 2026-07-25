# Menyalakan seluruh tumpukan Syntra di laptop ini.
#
#   powershell -ExecutionPolicy Bypass -File .\start.ps1
#
# Log:
#   logs\backend.log        stdout server Go (log terstruktur)
#   logs\backend.err.log    stderr server Go (panic, kegagalan fatal)
#   deployments\nginx\logs\ access.log dan error.log milik nginx

$root  = "C:\Users\user\Documents\PROJECTS\syntra\backend-syntra\server"
$nginx = "$root\deployments\nginx"
$logs  = "$root\logs"

New-Item -ItemType Directory -Force $logs | Out-Null

# --- Redis ---
if ((Get-Service Memurai).Status -ne 'Running') { Start-Service Memurai }

# --- server Go ---
# Hentikan yang lama dulu supaya port 8080 tidak bentrok.
Get-Process syntra -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Milliseconds 500

# Redirect WAJIB. Tanpa ini, proses tersembunyi membuang seluruh log-nya ke
# ruang hampa — dan saat ada yang salah, tidak ada apa pun untuk dibaca.
#
# Catatan: Start-Process menimpa berkas log, bukan menambahkan. Isinya selalu
# dari sesi yang sedang berjalan. Kalau butuh riwayat lintas sesi, arsipkan
# dulu sebelum restart.
Start-Process "$root\bin\syntra.exe" `
    -WorkingDirectory $root `
    -WindowStyle Hidden `
    -RedirectStandardOutput "$logs\backend.log" `
    -RedirectStandardError  "$logs\backend.err.log"

# --- nginx ---
if (-not (Get-Process nginx -ErrorAction SilentlyContinue)) {
    Start-Process nginx -ArgumentList "-p", ".", "-c", "nginx.conf" `
        -WorkingDirectory $nginx -WindowStyle Hidden
}

Start-Sleep -Seconds 4

Write-Host ""
Write-Host "--- /readyz ---"
curl.exe -s http://localhost:8081/readyz
Write-Host ""
Write-Host ""
Write-Host "log backend : $logs\backend.log"
Write-Host "log nginx   : $nginx\logs\access.log"
Write-Host "pantau live : Get-Content '$logs\backend.log' -Wait -Tail 30"
