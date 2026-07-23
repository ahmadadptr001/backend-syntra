# Uji asap seluruh endpoint REST.
#
#   powershell -ExecutionPolicy Bypass -File .\scripts\smoke.ps1
#
# Memanggil setiap endpoint yang tercantum di docs/api.md dan melaporkan mana
# yang benar-benar jalan. Dipakai untuk memastikan dokumentasi tidak berbohong.
#
# Catatan: jangan memakai `curl.exe -d '{"a":"b"}'` di PowerShell — tanda kutip
# di dalamnya dirusak sebelum sampai ke curl, dan hasilnya 400 yang menyesatkan
# seolah endpointnya bermasalah. Skrip ini memakai Invoke-RestMethod.

param(
    # Ikut menguji pendaftaran. Dilewati secara bawaan karena Supabase
    # membatasi jumlah signup per jam.
    [switch]$WithRegister
)

$ErrorActionPreference = 'Continue'
$root = Split-Path $PSScriptRoot -Parent
$base = if ($env:SYNTRA_BASE) { $env:SYNTRA_BASE } else { 'http://localhost:8081' }
$api  = "$base/api/v1"

# --- baca .env ---
$cfg = @{}
foreach ($line in [IO.File]::ReadAllLines("$root\.env")) {
    $l = $line.TrimStart([char]0xFEFF).Trim()
    if ($l -eq '' -or $l.StartsWith('#')) { continue }
    $i = $l.IndexOf('='); if ($i -lt 1) { continue }
    $v = $l.Substring($i + 1)
    $j = $v.IndexOf(' #'); if ($j -ge 0) { $v = $v.Substring(0, $j) }
    $cfg[$l.Substring(0, $i).Trim()] = $v.Trim()
}

function Get-Jwt($email, $password) {
    $body = @{ email = $email; password = $password } | ConvertTo-Json
    (Invoke-RestMethod "$($cfg['SUPABASE_URL'])/auth/v1/token?grant_type=password" `
        -Headers @{ apikey = $cfg['SUPABASE_ANON_KEY'] } -Method Post `
        -ContentType 'application/json' -Body $body).access_token
}

$script:pass = 0
$script:fail = 0
$script:hdr  = @{}

# Test-Endpoint menerima status yang diharapkan.
#
# `expect` ada karena sebagian endpoint memang seharusnya menolak: refresh token
# palsu wajib 401. Tanpa parameter ini, penolakan yang benar akan terhitung
# sebagai kegagalan dan laporan uji jadi berbohong ke arah sebaliknya.
function Test-Endpoint($label, $method, $url, $body, $expect = 0) {
    try {
        if ($null -ne $body) {
            $null = Invoke-RestMethod -Uri $url -Method $method -Headers $script:hdr `
                -ContentType 'application/json' -Body ($body | ConvertTo-Json) -ErrorAction Stop
        } else {
            $null = Invoke-RestMethod -Uri $url -Method $method -Headers $script:hdr -ErrorAction Stop
        }

        if ($expect -ne 0) {
            $script:fail++
            Write-Host ("  FAIL  " + $label + "  -> berhasil, padahal seharusnya $expect") -ForegroundColor Red
            return
        }
        $script:pass++
        Write-Host ("  OK    " + $label) -ForegroundColor Green
    } catch {
        $code = $_.Exception.Response.StatusCode.value__
        $msg = ''
        try {
            $sr = New-Object IO.StreamReader($_.Exception.Response.GetResponseStream())
            $msg = ($sr.ReadToEnd() | ConvertFrom-Json).error.message
        } catch {}

        if ($expect -ne 0 -and $code -eq $expect) {
            $script:pass++
            Write-Host ("  OK    " + $label + "  (menolak dengan benar: $code)") -ForegroundColor Green
            return
        }
        $script:fail++
        Write-Host ("  FAIL  " + $label + "  -> " + $code + " " + $msg) -ForegroundColor Red
    }
}

Write-Host "`n--- PUBLIK ---"
$script:hdr = @{}
Test-Endpoint "GET  /healthz" GET "$base/healthz" $null
Test-Endpoint "GET  /readyz"  GET "$base/readyz"  $null

$rnd = Get-Random -Maximum 99999

# Pendaftaran dilewati secara bawaan: Supabase membatasi jumlah signup per jam
# (free tier tanpa SMTP kustom), jadi menjalankannya berulang kali justru
# membuat uji ini gagal karena 429 — bukan karena endpointnya rusak.
# Jalankan dengan -WithRegister untuk ikut mengujinya.
if ($WithRegister) {
    Test-Endpoint "POST /auth/register" POST "$api/auth/register" `
        @{ email = "smoke$rnd@syntra.app"; password = 'rahasia123'; username = "smoke$rnd" }
} else {
    Write-Host "  SKIP  POST /auth/register  (pakai -WithRegister; Supabase membatasi signup per jam)" -ForegroundColor Yellow
}
Test-Endpoint "POST /auth/login" POST "$api/auth/login" `
    @{ email = 'budi@syntra.app'; password = 'budi123456' }
Test-Endpoint "POST /auth/refresh (token palsu ditolak)" POST "$api/auth/refresh" `
    @{ refresh_token = 'sengaja-salah' } 401

# --- terproteksi ---
$jwt = Get-Jwt 'budi@syntra.app' 'budi123456'
$script:hdr = @{ Authorization = "Bearer $jwt" }
$citra = (Invoke-RestMethod "$api/users/citra" -Headers $script:hdr).data.id

Write-Host "`n--- CHAT ---"
Test-Endpoint "GET  /conversations" GET "$api/conversations" $null
$conv = (Invoke-RestMethod "$api/conversations" -Method Post -Headers $script:hdr `
    -ContentType 'application/json' -Body (@{ type = 'direct'; user_id = $citra } | ConvertTo-Json)).data.id
Write-Host "  OK    POST /conversations (direct, idempoten)" -ForegroundColor Green
$script:pass++
Test-Endpoint "GET  /conversations/{id}/messages" GET "$api/conversations/$conv/messages" $null
Test-Endpoint "POST /conversations/{id}/messages" POST "$api/conversations/$conv/messages" `
    @{ type = 'text'; body = 'uji asap' }
Test-Endpoint "POST /conversations (group)" POST "$api/conversations" `
    @{ type = 'group'; title = "Grup Uji $rnd"; member_ids = @($citra) }

Write-Host "`n--- STORY ---"
Test-Endpoint "GET  /stories" GET "$api/stories" $null
Test-Endpoint "GET  /stories/me" GET "$api/stories/me" $null
Test-Endpoint "GET  /stories/me?include_expired" GET "$api/stories/me?include_expired=true" $null
Test-Endpoint "DEL  /stories/{id} (tidak ada -> 404)" DELETE `
    "$api/stories/00000000-0000-0000-0000-000000000000" $null 404

Write-Host "`n--- PENGGUNA & FOLLOW ---"
Test-Endpoint "GET  /users/{username}" GET "$api/users/citra" $null
Test-Endpoint "GET  /users/me/following" GET "$api/users/me/following" $null
Test-Endpoint "POST /users/{username}/follow" POST "$api/users/citra/follow" $null
Test-Endpoint "DEL  /users/{username}/follow" DELETE "$api/users/citra/follow" $null

Write-Host "`n--- MEDIA ---"
Test-Endpoint "POST /media/upload-url" POST "$api/media/upload-url" `
    @{ kind = 'image'; extension = 'jpg' }

Write-Host "`n--- VOICE ROOM ---"
Test-Endpoint "GET  /rooms" GET "$api/rooms" $null
$createdRoom = (Invoke-RestMethod "$api/rooms" -Method Post -Headers $script:hdr `
    -ContentType 'application/json' -Body (@{ title = "Room Uji $rnd" } | ConvertTo-Json)).data
$room = $createdRoom.id

# Create harus langsung mengembalikan token SFU untuk hostnya, supaya klien
# tidak perlu memanggil /join lagi hanya untuk tersambung ke audio sendiri.
if ($createdRoom.join.role -eq 'host' -and $createdRoom.join.can_publish) {
    Write-Host "  OK    POST /rooms (host langsung join, can_publish=true)" -ForegroundColor Green
    $script:pass++
} else {
    Write-Host "  FAIL  POST /rooms  -> host tidak langsung tersambung" -ForegroundColor Red
    $script:fail++
}

Test-Endpoint "POST /rooms/{id}/join" POST "$api/rooms/$room/join" $null
Test-Endpoint "GET  /rooms/{id}/participants" GET "$api/rooms/$room/participants" $null
Test-Endpoint "PATCH /rooms/{id}/mute" PATCH "$api/rooms/$room/mute" @{ muted = $true }
Test-Endpoint "POST /rooms/{id}/raise-hand" POST "$api/rooms/$room/raise-hand" $null
Test-Endpoint "GET  /rooms/{id}/speak-requests" GET "$api/rooms/$room/speak-requests" $null
Test-Endpoint "POST /rooms/{id}/invite" POST "$api/rooms/$room/invite" @{ user_id = $citra }

# Citra harus benar-benar bergabung dulu sebelum bisa dinaikkan perannya —
# set_room_role menolak target yang bukan peserta aktif, dan itu memang benar.
$jwtCitra = Get-Jwt 'citra@syntra.app' 'citra123456'
$null = Invoke-RestMethod "$api/rooms/$room/join" -Method Post -Headers @{ Authorization = "Bearer $jwtCitra" }

Test-Endpoint "PATCH /rooms/{id}/participants" PATCH "$api/rooms/$room/participants" `
    @{ user_id = $citra; role = 'speaker' }
Test-Endpoint "POST /rooms/{id}/leave" POST "$api/rooms/$room/leave" $null

Write-Host "`n--- AUTH (logout terakhir; mencabut sesi) ---"
Test-Endpoint "POST /auth/logout" POST "$api/auth/logout" $null

Write-Host ""
Write-Host ("  LULUS: $script:pass    GAGAL: $script:fail")
if ($script:fail -gt 0) { exit 1 }
