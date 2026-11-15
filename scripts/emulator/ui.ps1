# Drives Tryst on an Android EMULATOR by its accessibility tree, for the export round trip
# (N8, D-43). Dot-source it: . .\ui.ps1 -Work <scratch dir>
#
# It only ever talks to emulator-5554 (adb -s), and refuses to run if that device is not an
# emulator. Never point it at a phone.
#
# Tryst marks its window secure, so screenshots are black; everything here reads the UI tree.
# Assumes Tryst is installed on the emulator, a PIN of 123456 is set, and Auto-lock is 5 min.
param([string]$Work = (Join-Path $env:TEMP "alcove-roundtrip"))

$script:SER = "emulator-5554"
$script:WORK = $Work
$sdk = @($env:ANDROID_HOME, $env:ANDROID_SDK_ROOT, "$env:LOCALAPPDATA\Android\Sdk") | Where-Object { $_ -and (Test-Path $_) } | Select-Object -First 1
$script:adbExe = Join-Path $sdk "platform-tools\adb.exe"
New-Item -ItemType Directory -Force $script:WORK | Out-Null

function A { $ErrorActionPreference = "Continue"; & $script:adbExe -s $script:SER @args 2>&1 | ForEach-Object { "$_" } }

function Assert-Emulator {
  $dev = & $script:adbExe devices
  if (-not ($dev -match "$($script:SER)\s+device")) { throw "No emulator at $($script:SER). Start one; this script never uses a phone." }
  if ((A shell getprop ro.kernel.qemu) -ne "1" -and (A shell getprop ro.boot.qemu) -ne "1") { throw "$($script:SER) does not look like an emulator." }
}

function Get-Nodes {
  A shell uiautomator dump /sdcard/ui.xml | Out-Null
  A pull /sdcard/ui.xml (Join-Path $script:WORK "ui.xml") 2>&1 | Out-Null
  [xml]$x = Get-Content (Join-Path $script:WORK "ui.xml") -Raw -Encoding UTF8
  foreach ($n in $x.SelectNodes("//node")) {
    $b = [regex]::Match($n.bounds, '\[(\d+),(\d+)\]\[(\d+),(\d+)\]')
    [pscustomobject]@{ Text = $n.text; Desc = $n.'content-desc'; Id = $n.'resource-id'; Cls = ($n.class -replace '.*\.', ''); Click = $n.clickable; Checked = $n.checked
      X = [int](([int]$b.Groups[1].Value + [int]$b.Groups[3].Value) / 2); Y = [int](([int]$b.Groups[2].Value + [int]$b.Groups[4].Value) / 2) }
  }
}

function Tap-XY($x, $y) { A shell input tap $x $y }
function Key($k) { A shell input keyevent $k }
function Pause($s) { Start-Sleep $s }
function Type-Text([string]$t) { A shell input text ($t -replace ' ', '%s') }

# Exact text first, then a contains match (so "OK" is not found inside "look").
function Tap-Text([string]$t) {
  $all = @(Get-Nodes)
  $m = @($all | Where-Object { $_.Text -eq $t -or $_.Desc -eq $t })
  if ($m.Count -eq 0) { $m = @($all | Where-Object { $_.Text -like "*$t*" -or $_.Desc -like "*$t*" }) }
  if ($m.Count -eq 0) { return $false }
  Tap-XY $m[0].X $m[0].Y
  return $true
}

function Unlock {
  $n = @(Get-Nodes)
  if ($n | Where-Object { $_.Text -eq 'Unlock Tryst.' }) {
    foreach ($d in '1','2','3','4','5','6') { $k = $n | Where-Object { $_.Text -eq $d -and $_.Cls -eq 'TextView' } | Select-Object -First 1; Tap-XY $k.X $k.Y; Start-Sleep -Milliseconds 300 }
    Pause 3
  }
}

function Start-Tryst {
  if (-not (A shell pidof app.tryst)) { A shell am start -n app.tryst/.MainActivity | Out-Null; Pause 3 }
  Unlock
}

function Settings-Find([string]$label) {
  Start-Tryst
  for ($i = 0; $i -lt 4; $i++) {
    $n = @(Get-Nodes)
    if ($n | Where-Object { $_.Text -eq $label }) { return $true }
    if ($i -eq 0) { $s = $n | Where-Object { $_.Text -eq 'Settings' -and $_.Y -gt 2700 } | Select-Object -First 1; if ($s) { Tap-XY $s.X ($s.Y - 50); Pause 2 } }
    A shell input swipe 672 2300 672 700 400; Pause 1
  }
  return $false
}

# Restores a file already pushed to /sdcard/Download. $wipe is the "Replace my current data" toggle.
# Returns Tryst's own result line, for example "Brought in 10 trysts, 3 partners and 2 photos."
function Do-Import([string]$file, [string]$pw, [bool]$wipe) {
  if (-not (Settings-Find 'Import backup')) { throw "cannot find Import backup" }
  Tap-Text 'Import backup' | Out-Null; Pause 3
  if (-not (Tap-Text $file)) { throw "$file is not in the picker" }
  Pause 3
  $n = @(Get-Nodes)
  $row = $n | Where-Object { $_.Text -like 'Replace my current data*' } | Select-Object -First 1
  if (-not $row) { throw "the restore dialog did not open" }
  $box = $n | Where-Object { $_.Cls -eq 'View' -and $_.Click -eq 'true' -and [math]::Abs($_.Y - $row.Y) -lt 30 } | Select-Object -First 1
  if (($box.Checked -eq 'true') -ne $wipe) { Tap-XY $box.X $box.Y; Pause 1 }
  $e = Get-Nodes | Where-Object { $_.Cls -eq 'EditText' } | Select-Object -First 1
  Tap-XY $e.X $e.Y; Pause 1; Type-Text $pw; Pause 1; Key 4; Pause 1
  Tap-Text 'OK' | Out-Null; Pause 10
  if (-not (A shell pidof app.tryst)) { throw "Tryst crashed during the restore of $file" }
  $msg = (Get-Nodes | Where-Object { $_.Text -like 'Brought in*' } | Select-Object -First 1).Text
  Tap-Text 'OK' | Out-Null; Pause 2
  if (-not $msg) { throw "no restore result for $file" }
  $msg -replace ' Have a look.*', ''
}

# Has the app export a backup to /sdcard/Download/<name>.
function Do-Export([string]$name, [string]$pw) {
  if (-not (Settings-Find 'Export encrypted backup')) { throw "cannot find Export" }
  Tap-Text 'Export encrypted backup' | Out-Null; Pause 3
  $e = @(Get-Nodes | Where-Object { $_.Cls -eq 'EditText' })
  Tap-XY $e[0].X $e[0].Y; Pause 1; Type-Text $pw; Pause 1
  $e = @(Get-Nodes | Where-Object { $_.Cls -eq 'EditText' })
  Tap-XY $e[1].X $e[1].Y; Pause 1; Type-Text $pw; Pause 1; Key 4; Pause 1
  Tap-Text 'OK' | Out-Null; Pause 4
  $t = Get-Nodes | Where-Object { $_.Id -eq 'android:id/title' -and $_.Cls -eq 'EditText' } | Select-Object -First 1
  if (-not $t) { throw "no save screen" }
  Tap-XY $t.X $t.Y; Pause 1; Key 123; 1..60 | ForEach-Object { Key 67 }; Type-Text $name; Pause 1; Key 4; Pause 1
  Tap-Text 'SAVE' | Out-Null; Pause 12
  Unlock
  if (-not (A shell ls /sdcard/Download/ | Select-String -SimpleMatch $name)) { throw "the app did not write $name" }
}

