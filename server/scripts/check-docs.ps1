# Memastikan dokumentasi tidak tertinggal dari kode.
#
#   powershell -ExecutionPolicy Bypass -File .\scripts\check-docs.ps1
#
# Membandingkan tiga hal terhadap docs/api.md:
#   1. rute REST di router.go
#   2. frame WebSocket masuk yang punya handler
#   3. event WebSocket keluar yang benar-benar disiarkan
#
# Aplikasi membangun kliennya dari docs/api.md. Endpoint atau event yang tidak
# tercatat di sana sama saja dengan tidak ada — dan sebaliknya, yang tercatat
# tetapi tidak ada di kode akan membuat klien memanggil sesuatu yang mustahil.
# Skrip ini menangkap keduanya.

$ErrorActionPreference = 'Continue'
$server = Split-Path $PSScriptRoot -Parent
$root   = Split-Path $server -Parent
$apiDoc = "$root\docs\api.md"

$fail = 0

function Report($ok, $label) {
    if ($ok) { return }
    Write-Host "  TERTINGGAL  $label" -ForegroundColor Red
    $script:fail++
}

Write-Host "`n--- rute REST ---"
$routes = (Select-String "$server\internal\transport\rest\router.go" `
    -Pattern '"(GET|POST|PATCH|DELETE|PUT) (/[^"]*)"' -AllMatches).Matches |
    ForEach-Object { $_.Groups[1].Value + " " + $_.Groups[2].Value } | Sort-Object -Unique
$routes += "GET /api/v1/ws"

$documented = (Select-String $apiDoc `
    -Pattern '^\| `(GET|POST|PATCH|DELETE|PUT)` \| `([^`]+)`' -AllMatches).Matches |
    ForEach-Object { $_.Groups[1].Value + " " + $_.Groups[2].Value } | Sort-Object -Unique

Write-Host ("  router={0}  api.md={1}" -f $routes.Count, $documented.Count)
foreach ($r in $routes)     { Report ($documented -contains $r) "rute belum didokumentasikan: $r" }
foreach ($d in $documented) { Report ($routes -contains $d)     "rute fiktif di api.md: $d" }

Write-Host "`n--- frame WebSocket masuk ---"
$konst = @{}
(Select-String "$server\internal\transport\ws\protocol\protocol.go" `
    -Pattern '^\s+(Type\w+)\s+= "([^"]+)"' -AllMatches).Matches |
    ForEach-Object { $konst[$_.Groups[1].Value] = $_.Groups[2].Value }

$handled = (Select-String "$server\internal\transport\ws\handlers.go" `
    -Pattern 'r\.Handle\(protocol\.(\w+)' -AllMatches).Matches |
    ForEach-Object { $konst[$_.Groups[1].Value] }

foreach ($f in $handled) {
    Report (Select-String $apiDoc -Pattern ([regex]::Escape("``$f``")) -Quiet) "frame belum didokumentasikan: $f"
}

# Konstanta yang terdefinisi tetapi tidak punya handler akan dibalas
# "unknown_type" — pernah terjadi dengan room.join/room.leave, dan sempat
# membuat orang mengira fiturnya ada.
$clientFrames = @('subscribe','unsubscribe','ping','message.send','message.read',
                  'typing.start','typing.stop','presence.query','room.chat')
foreach ($k in $konst.Values) {
    if ($clientFrames -contains $k -and $handled -notcontains $k) {
        Report $false "konstanta tanpa handler: $k"
    }
}

Write-Host "`n--- event WebSocket keluar ---"
$events = @('ready','ack','error','pong','message.new','message.read','typing',
            'presence.update','room.message','room.ended','room.participants',
            'room.speak_request','room.role_changed','notification.new')
foreach ($e in $events) {
    Report (Select-String $apiDoc -Pattern ([regex]::Escape("``$e``")) -Quiet) "event belum didokumentasikan: $e"
}

Write-Host "`n--- tautan antar-dokumen ---"
Get-ChildItem "$root\docs\*.md", "$root\server\*.md", "$root\server\deployments\nginx\*.md" -File |
ForEach-Object {
    $dir = $_.DirectoryName
    $rel = $_.FullName.Replace("$root\", '')
    foreach ($m in [regex]::Matches([IO.File]::ReadAllText($_.FullName), '\]\(([^)#]+\.(md|go|sql|yaml|yml|conf|ps1))[^)]*\)')) {
        $target = $m.Groups[1].Value
        if ($target -match '^https?://') { continue }
        Report (Test-Path (Join-Path $dir $target)) "tautan rusak di ${rel}: $target"
    }
}

Write-Host ""
if ($fail -eq 0) {
    Write-Host "  Dokumentasi selaras dengan kode." -ForegroundColor Green
} else {
    Write-Host "  $fail hal tertinggal. Perbaiki docs/api.md sebelum commit." -ForegroundColor Red
    exit 1
}
