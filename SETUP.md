# Alcove — Environment Setup

> Everything needed to take a **clean checkout to a green build**, including the traps. Principle 8:
> write the **fix**, not just the name of the trap. Update this the moment something burns time; same
> change, same commit.

## Toolchain

| Tool | Version | Install / where |
|------|---------|-----------------|
| Go | **1.26.11** (the `go` directive in `go.mod` is the source of truth) | `winget install --id GoLang.Go` |
| Git for Windows | any recent | provides the Git Bash the guards need |
| Docker | only for image work | not installed on the Windows dev box; image work happens in CI and on a Docker host |

## Environment variables / local config

- Nothing is required to build or test.
- Running the server needs a master key file. Create it once and prove a second copy:
  ```powershell
  .\bin\alcove.exe keygen -out C:\alcove-dev\keys\alcove.key
  .\bin\alcove.exe keycheck -file C:\alcove-dev\keys\alcove.key
  ```
  (in a container: `docker run --rm -v <host folder>:/keys <image> keygen -out /keys/alcove.key`).
- `ALCOVE_ADDR` (default `:8080`) is the listen address. `ALCOVE_DATA_DIR` (`/data`), `ALCOVE_KEY_FILE`
  (`/run/keys/alcove.key`) and `ALCOVE_TMP_DIR` (`/tmp/alcove`) are the paths. The key file must be
  outside the data directory.
- `ALCOVE_ALLOW_NONTMPFS_TMPDIR=true` lets the scratch directory be an ordinary folder. Use it on a dev
  box only: on Linux the server otherwise refuses a scratch directory that is not tmpfs, so decrypted
  data cannot reach disk.
- `ALCOVE_SEAL_EVERY_SECONDS` (default 10) is how often a changed database is sealed.
- Instance backups (D-28): `ALCOVE_BACKUP_DIR` (default `/backups`, mounted from `ALCOVE_BACKUP_PATH` in compose; must not be inside the data directory) and `ALCOVE_BACKUP_KEEP` (default 7). With no folder the Backups page says so. Command-line tools for use with no server: `alcove backup-verify -file F`, `alcove restore -backup F -to DIR`, `alcove drill -backup F -key K`. See `RUNBOOK.md`.
- Uploads (D-29): `ALCOVE_MAX_UPLOAD_MB` (default and ceiling 512, the Tryst reader bound). Up to two backups are decrypted at once, each held in memory while it is read, so keep `ALCOVE_MEM_LIMIT` above twice this plus 1 GiB. `alcove test-backup -out F` writes a synthetic backup (fake data) for trying Alcove without a real one.
- Time zone (D-35): `ALCOVE_TZ` is the starting zone for people who have not chosen their own (a name like `Europe/Berlin`; empty means UTC). The administrator can save a default in the console (Admin, Network and security) that beats it and survives a container rebuild. Each person can set their own on their profile.
- Soft delete (D-30): `ALCOVE_USER_RECOVERY_DAYS` (default 10) and `ALCOVE_ADMIN_RECOVERY_DAYS` (default 45, never shorter than the first). Past the second window a purge that runs by itself every hour removes the data for good. `alcove reset-password -user NAME [-generate]` is the break-glass for a lost admin password; stop the server first (the data folder is locked, and the command is refused if the server is running).
- **First run:** with no accounts the server logs a one-time setup code (`setup_code`, Warn level). Open the server in a
  browser, enter the code and create the administrator. In Unraid read it in the container's log. The code is not stored and a
  new one is made at each start until the first account exists.
- Transport security (D-23), all optional and all winning over the admin console when set (the break-glass path):
  `ALCOVE_ALLOWED_NETWORKS` (CIDR ranges; default is the private LAN ranges, loopback and Tailscale),
  `ALCOVE_TRUSTED_PROXIES` (proxy addresses whose forwarded client address is believed), `ALCOVE_PROXY_MODE` and
  `ALCOVE_REQUIRE_TLS` (`true` or `false`). A typo stops the start instead of opening the door.
- The header mark and favicons are generated from the master logo: `go run ./tools/logocrop docs/img/alcove-icon-512.png internal/server/static`.
- Secrets never go in env vars in the repo. The data key lives in a key file kept **outside** the data
  directory (see CLAUDE.md constraint 4). `.env` files are gitignored.

## Bootstrap (one command)

```powershell
.\bootstrap.ps1
```

Verify: it prints `BOOTSTRAP GREEN`. On Linux or in CI, `make check` does the same.

## Platform gotchas (trap → fix)

Carried over from Sanctum, where each one cost time.

- **PowerShell refuses to run `bootstrap.ps1` ("running scripts is disabled").** → Run it as above with
  `-ExecutionPolicy Bypass` for that one invocation; do not change the machine-wide policy.
- **There is no `make` in Git Bash on Windows.** → Use `.\bootstrap.ps1`. The `Makefile` is for CI and
  Linux. Both call `scripts/guards.sh`, so the guards cannot drift apart.
- **There is no `python` on this box** (the Microsoft Store alias intercepts it). → Bash and PowerShell
  only; do not write tooling that assumes Python.
- **`winget install` does not refresh `PATH` in an open shell.** → Open a new shell, or let
  `bootstrap.ps1` fall back to `$env:ProgramFiles\Go\bin\go.exe`.
- **`go test -race` needs cgo and there is no C toolchain on stock Windows.** → The race detector runs in
  CI on Linux. `bootstrap.ps1` runs `go test` without `-race` on purpose.
- **`kill -TERM` from Git Bash does not deliver a real SIGTERM** to a native Windows binary, so graceful
  shutdown cannot be verified here. → Verify with `docker stop` on a Docker host.
- **Windows line endings break the container and the scripts.** `.gitattributes` forces LF everywhere
  except `*.ps1`. → Do not override `core.autocrlf`; this repo sets it to `false`.
- **PowerShell 5.1 `Set-Content` and `Out-File -Encoding utf8` write a UTF-8 BOM** that breaks parsers.
  → Write files with `[System.IO.File]::WriteAllText($path, $text, (New-Object System.Text.UTF8Encoding($false)))`.
- **Never run `connectedDebugAndroidTest` (Tryst's instrumented tests) with a physical phone attached.**
  It uninstalls the app and wipes its data. → Set `ANDROID_SERIAL=emulator-5554` first. Alcove's own
  backup-writer proof runs against an emulator only.
- **The emulator round trip (`scripts/emulator/roundtrip.ps1`, docs/RELEASE_CHECKS.md) needs the Android SDK, an AVD at `emulator-5554`, and PowerShell.** Tryst hides its window from screenshots, so the driver reads the accessibility tree (`uiautomator dump`) instead. In Git Bash, `adb` mangles `/sdcard/...` paths: prefix the command with `MSYS_NO_PATHCONV=1` or use PowerShell. Android renames a save that clashes with an existing file to `name (1).tryst`, so give every run a fresh file name. The 16K x86_64 image sometimes crashes Tryst in unrelated code; the script retries and reports it.
- **PowerShell parses `":text"` as a scope-qualified variable.** → Write `":text"`.

## Known toolchain gaps on this dev box

| Gap | Consequence | Where it gets verified instead |
|-----|-------------|--------------------------------|
| No Docker | cannot build the image or validate compose locally | CI's image job |
| No `make` | `make` targets are CI and Linux only | CI; `bootstrap.ps1` locally |
| No C toolchain | no `-race` locally | CI |
| Windows signals | graceful shutdown unverifiable locally | a live smoke script on a Docker host |
| No emulator in CI | the export round trip (N8) cannot run there | `scripts/emulator/roundtrip.ps1` by hand before each release that touches `internal/export`, and for each Tryst release |
| No real `.tryst` in the repo (by design) | real-file interop is not part of CI | synthetic backups in tests; real-file interop proven by hand on a private copy |
