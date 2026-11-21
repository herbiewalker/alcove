# Audit pass: secrets, storage and logging

- **Date:** 2026-11-21
- **Lens:** secrets, storage and logging (checklist 02; covers the "crypto and storage" and "secrets and logging" lenses together, at the depth the code allows without a live host)
- **Tree:** `main` at `6856f74` (CI green on it)
- **Verified by running:** `scripts/guards.sh all` (incl. `no-plaintext`, `no-secrets`); a broad log-scrape probe (throwaway, deleted); `git grep` over all 73 commits for key, token and private-key patterns (none found)

## Scope read
`internal/keyfile`, `internal/seal`, `internal/store` (open, seal, serialize, data keys), `internal/blobstore`, `internal/auth`
(passwords, sessions), `internal/totp` store and recovery codes, `internal/backup` writer, every `log.*`/`slog` and `fmt.Fprint*`
call in `internal` and `cmd`, every `adminlog.Append` and `q.audit` call and the `adminlog` field alphabet, cookies and cache
headers in `internal/server/server.go`, `docker-compose.yml`, `Dockerfile`, CI and release workflows, error values that wrap uploaded-file content.

## Findings

| # | Severity | Where | Issue | Fix |
|---|----------|-------|-------|-----|
| 1 | should-fix | `CLAUDE.md:87`, `docs/REQUIREMENTS.md:99` vs `.github/workflows/*.yml` | Constraint 10 names "gitleaks in CI" as a guard, but no workflow, script or Makefile target runs it. What exists: `guards.sh no-secrets` (tracked file names and a narrow regex) and Trivy secret scan of the **built image** only. Nothing scans repo history, which matters because the repo is to go public. My own pattern grep over all commits found nothing, so this is a missing guard, not a leak. | Add a gitleaks (or `trivy fs --scanners secret`) step over the full history to `ci.yml`, or change the two documents to say what actually runs. |
| 2 | nit | `internal/server/server.go:267` | The panic handler logs `r.URL.Path` and `fmt.Sprint(v)`. A panic value built from request data (for example `panic(err)` wrapping input) would put content in the log (constraint 8). Today's runtime panics carry no content. | Log only `fmt.Sprintf("%T", v)` plus a short hash or the route pattern. |
| 3 | nit | `internal/store/store.go:144-146` | Start-up removes a leftover `alcove.db` and its journal files from `TmpDir` but not a leftover `snapshot.*.sqlite` (the plaintext copy made by `serialize` for each seal). On tmpfs it dies with the container; with `ALCOVE_ALLOW_NONTMPFS_TMPDIR` (dev) a crash mid-seal leaves a plaintext database on disk. | Also remove `snapshot.*.sqlite` at `Open` and in `Close`. |
| 4 | nit | `internal/backup/backup.go:182` | The backup archive is ciphertext, but its file list names `blobs/<owner id>/...` entries with sizes, so whoever reads the backup share can count users, uploads and photos. Ids are opaque, so this is metadata only. | Note it in `THREAT_MODEL.md` under T5, or name entries by index in the archive. |

No blockers.

## What held up
- **Key handling:** key file refused inside the data dir (symlink and case aware), written `O_EXCL` mode 0400, never created by the server; wrapped form uses Argon2id with parameters bound as AAD and a floor on load; fingerprint is a salted hash.
- **At rest:** live database only on tmpfs (start refuses a non-tmpfs `TmpDir`); sealed with AES-256-GCM, random 96-bit nonce, header and manifest hash as AAD, atomic write with a kept previous generation; `VACUUM INTO` means deleted rows are not in the sealed file; per-user data keys wrapped under the master key with the owner id as AAD; photos encrypted per owner with the media id as AAD; stored backup passwords sealed under the owner's key. `no-plaintext` guard passes.
- **Passwords and sessions:** Argon2id with parameters stored in each hash and rehash on login; dummy verify for unknown accounts; 256-bit session and CSRF tokens held in memory only, 30-minute idle and 12-hour absolute limits; HttpOnly, SameSite=Strict cookies; every rendered page is `no-store`; recovery codes are 59-bit random, stored as hashes, TOTP secret never logged.
- **Logging (constraint 8):** all 63 log call sites carry ids, counts, reasons or IPs only. Upload reader errors are reduced to a content-free label by `ingest.reason` before they can reach a message. The admin log accepts only `[A-Za-z0-9_.:,@-]` in every text field and rejects anything else, so it cannot carry a sentence. A broad probe (edits, a stale edit, an admin edit, searches, a missing photo, export, profile text) left a log with only the start-up, first-admin and upload lines and none of the test notes, names, filenames or passwords.
- **Repo and image:** compose carries paths and ports only, key mounted read-only from outside the data volume, no secret variables; no key, token or private-key pattern in any of the 73 commits.

## Accepted by design (documented, listed so they stay a conscious choice)
- The one-time setup code is written to the log at start-up while no account exists, and is cleared once used.
- An uploaded backup is stored as Tryst wrote it, so on a stolen disk without the key file it is protected by the member's own backup password alone (THREAT_MODEL T5). The server cannot strengthen that.

## Verdict
This lens covered key custody, sealing, blob encryption, credentials, sessions, every log and error path, the admin-log alphabet and the repo and CI for secrets, by reading the code and running the guards and a scrape probe. It cannot cover: the real host (swap and core dumps on the Unraid box, file permissions of the mounted key and data paths, the Docker log driver and who can read it), a restore drill with a real key, long-term nonce and key-rotation behaviour, and anything decided inside Tryst's own encryption. **Blockers outstanding: none.** Finding 1 is the one worth doing before saying the audit passed; 2 to 4 are small.

## Resolution (same day, uncommitted)

- **1 fixed:** `ci.yml` now checks out full history and runs gitleaks v8.30.1 over it, the binary pinned by version and SHA-256.
  Not run locally (running a downloaded binary was declined), so the first CI run is the first real result; a false positive on a test
  fixture would need a `.gitleaks.toml` allow rule.
- **2 fixed:** the panic handler logs the panic's type, not its value (`server.go`).
- **3 fixed:** `store.Open` and `Close` remove leftover `snapshot.*.sqlite` files; test `TestCrashLeftoverSnapshotIsSweptOnOpen`.
- **4 documented:** T5 in `THREAT_MODEL.md` now states the backup file-list metadata.
