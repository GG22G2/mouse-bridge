# Mouse Bridge installer for a new Windows machine.
#
# Copies the prebuilt daemon (bin\mouse-bridge.exe) and the browser extension
# (extension\) to %USERPROFILE%\.mouse-bridge\, then starts the daemon.
#
# Usage (from the repo root):
#   powershell -ExecutionPolicy Bypass -File install.ps1          # use the prebuilt exe
#   powershell -ExecutionPolicy Bypass -File install.ps1 -Build   # rebuild from source first (Go 1.26+)
#
# After this script finishes, load the unpacked extension in your browser
# (one-time, see the printed steps) and verify with:
#   curl http://127.0.0.1:10087/status   ->  "extension_connected":true

param([switch]$Build)

$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
$app  = Join-Path $env:USERPROFILE ".mouse-bridge"

# 1. Optionally build from source.
if ($Build) {
    Write-Host "[1/4] building from source (requires Go 1.26+)..."
    go build -o (Join-Path $root "bin\mouse-bridge.exe") ./cmd/mouse-bridge
    if ($LASTEXITCODE -ne 0) { throw "go build failed (Go 1.26+ required)" }
}

if (-not (Test-Path (Join-Path $root "bin\mouse-bridge.exe"))) {
    throw "bin\mouse-bridge.exe not found. Run with -Build (needs Go) or use a checkout that includes the prebuilt binary."
}

# 2. Stop a running daemon so the exe can be replaced, then install files.
Write-Host "[2/4] installing to $app ..."
& (Join-Path $root "bin\mouse-bridge.exe") stop 2>$null | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $app "bin") | Out-Null
Copy-Item (Join-Path $root "bin\mouse-bridge.exe") (Join-Path $app "bin\mouse-bridge.exe") -Force
New-Item -ItemType Directory -Force -Path (Join-Path $app "extension") | Out-Null
Copy-Item (Join-Path $root "extension\*") (Join-Path $app "extension") -Recurse -Force

# 3. Start the daemon.
Write-Host "[3/4] starting daemon on 127.0.0.1:10087 ..."
& (Join-Path $app "bin\mouse-bridge.exe") start

# 4. Browser extension instructions.
Write-Host "[4/4] done. One-time browser step:"
Write-Host "  1. Open edge://extensions (Edge) or chrome://extensions (Chrome)"
Write-Host "  2. Enable Developer mode (toggler on the extensions page)"
Write-Host "  3. 'Load unpacked' -> select:  $app\extension"
Write-Host "  4. Verify:  curl http://127.0.0.1:10087/status"
Write-Host "     (wait for `"extension_connected`":true - the extension reconnects within ~30s)"
