# The export round trip (N8, D-43 slice 3): restore an Alcove export in Tryst on an Android
# EMULATOR with "Replace my current data" on, have the app export again, and compare the two
# row by row. It must be re-run for every Tryst release and before every Alcove release that
# touches internal/export (see docs/RELEASE_CHECKS.md).
#
#   .\scripts\emulator\roundtrip.ps1 [-Work <dir>]
#
# Needs: Go; an emulator running at emulator-5554 with Tryst installed, a PIN of 123456 and
# Auto-lock set to 5 min. Synthetic data only. Never a physical phone: the driver refuses.
param([string]$Work = (Join-Path $env:TEMP "alcove-roundtrip"))
$ErrorActionPreference = "Continue"  # failures are explicit throws; adb writes progress to stderr
$repo = Resolve-Path (Join-Path $PSScriptRoot "..\..")
. (Join-Path $PSScriptRoot "ui.ps1") -Work $Work
Assert-Emulator

$dir = Join-Path $Work "files"
New-Item -ItemType Directory -Force $dir | Out-Null
$env:ALCOVE_ROUNDTRIP_DIR = $dir
Push-Location $repo
try {
  go test ./internal/ingest -run TestRoundTripEmit -count=1 | Out-Host
  if ($LASTEXITCODE -ne 0) { throw "could not write the round-trip files" }

  foreach ($f in "base", "phone", "export") { A push (Join-Path $dir "$f.tryst") "/sdcard/Download/$f.tryst" | Out-Null }
  $stamp = Get-Date -Format "HHmmss"  # a fresh name every run: Android renames a clash to "name (1)" and we would compare the old file
  $pw = @{ base = "round-trip-base-1"; phone = "round-trip-phone-1"; export = "round-trip-export-1" }
  $failed = 0

  # This emulator image's runtime crashes Tryst now and then, in code that has nothing to do with the
  # backup (Compose drawing, a Room getter). A crashed step is retried and counted, never ignored.
  $crashes = 0
  function Step([scriptblock]$s) {
    for ($try = 1; $try -le 3; $try++) {
      try { return (& $s) } catch {
        if ($_.Exception.Message -notmatch "crashed") { throw }
        $script:crashes++
        Write-Host "  (Tryst crashed, restarting it and retrying: attempt $try of 3)"
        Start-Tryst
      }
    }
    throw "Tryst kept crashing"
  }

  function Compare-Reexport([string]$label, [string]$name) {
    A pull "/sdcard/Download/$name" (Join-Path $dir $name) | Out-Null
    $env:ALCOVE_ROUNDTRIP_CHECK = "export.tryst:$($pw.export):${name}:round-trip-reexport-1"
    $out = go test ./internal/ingest -run TestRoundTripCompare -count=1 -v 2>&1 | Out-String
    $line = ($out -split "`n" | Where-Object { $_ -match "differences;" } | Select-Object -First 1)
    if ($out -match "--- PASS" -and $line -match " 0 differences") { Write-Host "PASS  $label :: $($line.Trim())" } else { Write-Host "FAIL  $label"; Write-Host $out; $script:failed++ }
  }

  foreach ($start in @(@("base", "a phone that holds the upload this export came from"), @("phone", "a phone that holds different, phone-only data"))) {
    Write-Host "--- start state: $($start[1])"
    Write-Host ("restore " + $start[0] + ": " + (Step { Do-Import "$($start[0]).tryst" $pw[$start[0]] $true }))
    Write-Host ("restore export (replace my current data): " + (Step { Do-Import "export.tryst" $pw.export $true }))
    $name = "reexport-$stamp-$($start[0]).tryst"
    Step { Do-Export $name "round-trip-reexport-1" }
    Compare-Reexport "wipe-first over $($start[0])" $name
  }
}
finally { Pop-Location }
if ($failed -gt 0) { Write-Host "ROUND TRIP FAILED ($failed)"; exit 1 }
Write-Host "ROUND TRIP PASSED: the app's own export matches what was restored, in both start states. Tryst crashes retried: $crashes."




