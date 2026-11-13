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

## Known toolchain gaps on this dev box

| Gap | Consequence | Where it gets verified instead |
|-----|-------------|--------------------------------|
| No Docker | cannot build the image or validate compose locally | CI's image job |
| No `make` | `make` targets are CI and Linux only | CI; `bootstrap.ps1` locally |
| No C toolchain | no `-race` locally | CI |
| Windows signals | graceful shutdown unverifiable locally | a live smoke script on a Docker host |
| No real `.tryst` in the repo (by design) | real-file interop is not part of CI | synthetic backups in tests; real-file interop proven by hand on a private copy |
