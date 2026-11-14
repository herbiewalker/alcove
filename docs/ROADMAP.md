# Alcove — Roadmap

> **Status:** Phase 1 roadmap, approved 2026-11-12 with the review-pass changes (round-trip spike, M2 split,
> instance backup before real data, early admin pages, first live deploy, authorization audit, mobile-first UI).
> Milestones in order; **foundations before features** (principle 2). Each has exit criteria and is not done until
> they are met. Crypto and storage come before views; the risky writer is proven early by a spike and finished late;
> the web UI is mobile-first from the first page.

---

## M0 — Scaffold, CI and container *(no features)*

- [x] Repo, AGPL-3.0, `.gitignore`, `.gitattributes`, `.editorconfig`
- [x] `CLAUDE.md` with hard constraints and size class, README with an honest status, SETUP, CRITICAL_ASSETS
- [x] Go module, a scaffold binary that serves `/healthz` and shuts down gracefully
- [x] `bootstrap.ps1`, `Makefile`, `scripts/guards.sh` (no-secrets, no-outbound, no-plaintext stub, digest-pin, licenses), each forced to fail then restored
- [x] Design docs seeded: REQUIREMENTS, ARCHITECTURE, THREAT_MODEL, DATA_MODEL, DECISIONS, this file
- [x] Dockerfile (multi-stage, distroless, digest-pinned, non-root), `docker-compose.yml`, `.env.example`, `.dockerignore`
- [x] CI workflows written: build, vet, gofmt, `go test -race`, the guards, govulncheck, dependency and licence gate, image checks, a container smoke (starts, healthy, clean shutdown), trivy. **Green run pending a push**
- [x] `scripts/release.sh` and `release.yml` (adapted from Sanctum); `scripts/smoke-live.sh` skeleton (three real checks, later ones print SKIP)
- **Exit:** a clean checkout reaches a green build and a green CI run with every guard armed.

## M1 — Tryst reader *(read only)*

- [x] Copy and extend `trystbk` (D-9): header parse with bounds, PBKDF2, streaming AEAD, ZIP walk with Zip-Slip checks (`internal/trystbk`, MIT notice kept)
- [x] Generic, lossless table reader for `data.json` (raw values, column and table order, unknown keys and entries kept); schema-version recording and `SchemaStatus` (D-25)
- [x] Synthetic-backup generator `internal/trystbk/synth`, shaped by Tryst's v19 Room schema, deterministic per seed, plus `trystbk.Seal` (the container writer M6 builds on)
- [x] Forced negative tests: wrong password, flipped and truncated ciphertext, tampered header, absurd KDF count, Zip-Slip, zip bomb (entry count and declared size), oversize body, malformed data.json, too many rows. Six code mutations each made a test fail
- **Exit:** interop against a real backup on a private copy (still the user's call; a genuine Tryst-written container was read on 2026-11-13 from an emulator export); every negative test fires; a generated backup restores in
  Tryst on an emulator (proven 2026-11-13: restored and re-exported row for row).

## S1 — Round-trip spike *(de-risks the edit idea before anything depends on it)*

- [x] A throwaway minimal writer: take a backup read by M1, change one entry, add one, delete one, re-encrypt as `TRYSTBK1` (done 2026-11-13; the code is kept outside the repo)
- [x] On an **emulator** (never a physical phone), restore it in both Tryst modes and record what happens: edit applied,
      new entry added, deleted entry gone (wipe) or kept (merge), a phone-only entry lost (wipe) or kept (merge),
      link rows after a parent replace (D-15)
- [x] Write the findings into `DECISIONS.md` (confirm or amend D-15): wipe confirmed exact, **merge found unsafe (deletes partner links)**, ZIP must be DEFLATE
- **Exit:** met 2026-11-13. D-15 is confirmed on an emulator for wipe-first restore and amended for merge (D-15 addendum). The edit design stands: exports are restored with wipe first.

## M2a — Storage foundation

- [x] Storage per D-16: sealed tmpfs database (`internal/store`) with FTS5 search, per-user data keys wrapped by the master key, an atomic seal with one previous generation (`internal/seal`), the master key file with the refuse-to-start check and the optional passphrase-wrapped form (`internal/keyfile`, D-26), `alcove keygen` and `keycheck`
- [x] Blob store (`internal/blobstore`): uploads verbatim and immutable with a SHA-256, photos encrypted under the owner's data key, owner and id validated
- [x] `scripts/guards.sh no-plaintext` armed (`TestGuard_NoPlaintext`, forced to fail by making the seal leak a plaintext copy); the scanner itself is tested against each signature it claims to catch
- [x] Measured on a Windows dev box (a Linux tmpfs figure comes from CI and the live smoke): **50,000 entries with 50,000 search rows**: database 29 MiB, seal 145 ms, reopen 187 ms, search 51 ms. **200,000 of each**: 116 MiB, seal 532 ms, reopen 777 ms, search 219 ms. A household is a small fraction of that, and photos are not in the database. On tmpfs the live database is the RAM cost (SQLite memory sits outside the Go heap, so the Go heap figure is not the one to read)
- [ ] Rebuild path for the row cache and search index from revisions *(moved to M3, where revisions first exist)*
- **Exit:** met 2026-11-13 locally and in tests (CI and the live smoke confirm it on Linux and in the image): a restart needs no human unlock (default mode); the key file inside the data directory refuses to start; the plaintext guard passes and was forced to fail; a wrong key fails cleanly and leaves the sealed data untouched.

## M2b — Accounts, admin log, UI shell

- [x] First-run admin (one-time setup code from the server log, D-27); accounts; Argon2id password hashing; sessions; rate limiting; step-up re-login; password change that signs out other devices
- [x] Append-only hash-chained admin log (`internal/adminlog`), its verifier (N5) and a **basic log viewer** with a "verify now" button. The database also refuses edits and deletes of log rows (trigger), and a head anchor catches entries cut off the end
- [x] Strict CSP and a **mobile-first** responsive UI shell (light, dark, auto): no inline script or style, no external font or image, 44 px touch targets, no sideways scroll at 375 px (checked in a phone-sized browser, which caught and fixed one overflow)
- [x] The Alcove logo: `tools/logocrop` makes the square A-and-heart mark and favicons from the master logo. One tile with its own deep-purple background serves both themes (a separate light variant was judged unnecessary: the rounded tile reads well on light and dark)
- [x] Transport security per D-23: CSRF tokens (per-session, and a signed double-submit token before sign-in) plus a cross-site fetch check, allowed networks (private ranges, loopback and Tailscale by default), proxy mode, insecure-connection banner and require-TLS mode, with the 60-second auto-revert on allowed-network changes (F32, N17). Environment values win and show as "set by environment"
- [x] The unlock page for a passphrase-wrapped key (promised in M2a), rate limited
- [ ] Optional TOTP for admins *(not in this milestone; F20 stays with M9, O-5 open)*
- **Exit:** met 2026-11-13 in tests and in a phone-sized browser (the image smoke in CI and the live smoke confirm it on Linux): no unauthenticated route creates an account (every POST route is hit unauthenticated, before and after first run); the chain verifies and eight kinds of tampering are detected; the shell is usable on a phone-width screen with the keyboard and touch.

## M3 — Spaces, upload, revisions, recovery *(real data starts here)*

- [ ] Spaces, memberships, invites (F27), join requests, leave and removal (F4 to F6)
- [x] **Piece B, built 2026-11-14: upload, revisions, stored passwords and the row cache (D-29).** `internal/ingest` and the My data page.
      The upload is one streamed request (token first, file last, token checked before a byte of the file is read), written to the blob
      store, judged (duplicate, wrong password, not a backup, damaged, hostile header, oversize) and only then recorded in one
      transaction with its revision, the derived row cache, the search index and the sealed password. A refusal records nothing. The
      cache and search rebuild from the head revision (admin button, logged). A progress bar needs the project's one same-origin script
      (CSP gains `script-src 'self'` and `connect-src 'self'` and nothing else). Checked in a phone-sized browser, both the refusal and
      the success path. `alcove test-backup` writes synthetic backups so none of this needs real data
- [ ] **Still open from piece B:** a real backup uploaded from a real phone over the real LAN (F30), which needs the first live deploy
- [x] Rebuild path: the row cache and search index can be rebuilt from revisions (moved here from M2a)
- [ ] User lifecycle: add, disable, delete with grace, reset, data download (F2, F3), with **basic admin pages**
- [ ] Soft delete with the 10 and 45 day windows and a real purge (F16, N13)
- [x] **Piece A, built 2026-11-14: instance backup and a restore drill (F23, D-28).** `internal/backup` (one tar of ciphertext with a
      hashed index and a sidecar checksum, consistent by ordering, never overwriting), the admin **Backups** page with background jobs
      (back up, verify, restore drill, retention), and `alcove backup-verify`, `restore` and `drill`. The same flow is smoke-tested
      end to end on the real image (`scripts/smoke-backup.sh`, in CI and the live smoke). Still **your step**: prove the key's second
      copy with `drill` and a real backup (CRITICAL_ASSETS) before the first real upload
- Order of the remaining pieces (decided with the owner): **B** upload, revisions, backup passwords and the rebuildable row cache;
  **C** user lifecycle, soft delete and purge; **D** spaces, invites and join requests. Revisions are kept forever and the admin can
  purge them on purpose (logged)
- **Exit:** isolation tests across spaces pass; a deleted entry restores inside the window and is gone after it; an
  instance backup restores to a scratch location and reads back; a real backup uploads from a phone browser.

## M4 — Collaborative spaces, overlay, compare

- [ ] Metrics registry per D-18 (reviewed whenever Tryst Insights changes); space visibility, pause, per-entry private,
      include settings and owner approval of joins (F5, F11; D-21); the `all_members` mode is a later per-space setting
- [ ] Profile prefill from the Tryst profile with per-field sharing, and member cards (F26, F28; D-14)
- [ ] Calendar overlay (F12), then compare metrics (F13)
- **Exit:** a non-member and another space see nothing; pause, the private flag and leave are honoured; owner approval
  of joins holds; layout and keyboard checks pass at phone, tablet and desktop widths.

## L1 — First live deploy *(read-only features; the validation ladder starts here)*

- [ ] `RUNBOOK`, `docker-compose`, the live smoke script, deploy on the real host, LAN-only, limits applied
- [ ] Validation ladder (principle 11): synthetic data, then one real backup, then the full history
- [ ] Checked from a real phone browser: upload a backup from the phone, view the overlay
- [ ] Runbook recipes for HTTPS (Caddy or Nginx Proxy Manager, `tailscale serve`); the default stays secured wifi (D-23)
- **Exit:** the two of you use it for real. The README status line says what is and is not proven.

## M5 — Editing

- [ ] Editing scope per D-19, including adding new entries; change-sets, revisions, history and diff in the browser (F14)
- [ ] Admin edit of anyone's data, logged; recovery from any revision
- **Exit:** edits never alter an upload; the admin log records every edit without content.

## M6 — Backup writer and export *(the riskiest milestone; S1 has already de-risked the concept)*

- [ ] Writer: materialize a revision, re-encrypt as a fresh `TRYSTBK1` (all entries DEFLATED, `data.json` first); base-revision warning and diff (F15)
- [ ] Guided restore procedure: wipe first only, merge warned against, plus the "fresh phone backup first" confirmation (F29, D-15 addendum)
- [ ] **Phone download flow (F30):** the export downloads on a phone with a clear filename, and the guide says how to
      restore it from Downloads in Tryst
- [ ] **Emulator round trip:** export an edited backup, restore it in Tryst in both modes, confirm the edits (N8).
      Never a physical phone.
- **Exit:** the round trip passes for Tryst schema v19 (and each further version only after it is added to the
  compatibility table and proven).

## M7 — Photos

- [ ] Media store; blur until clicked; sharing; location kept and shown per photo (F19, R4); resolve O-6
- **Exit:** photos upload and view on a phone and a desktop; location is shown only when its owner allows it.

## M8 — Admin console and operations

- [ ] Settings page for the transport-security options (D-23); account, space and health pages (full); search over everything (D-17); log viewer filters and export (F17, F18)
- [ ] Optional TOTP (F20); optional notifications (F24); optional member activity view (F25, O-8)
- [ ] Optional QR codes for the server address and invites, generated locally (F31)
- **Exit:** the admin can find and fix a user's problem without a shell; every admin action is in the log.

## M9 — Harden and release

- [ ] Phase 3 audit passes, one lens per fresh session: **authorization and isolation (who can see what)**, crypto and
      storage, secrets and logging, input and injection, build hardening, accessibility and mobile
- [ ] Final live validation on the real host and a real phone, then v0.1.0 via the release script
- **Exit:** the README's design-phase status line comes off, and not before.

---

## Recurring

- **Tryst compatibility check on every Tryst release:** update the supported-schema table, the metrics registry (D-18)
  and the writer, and re-run the emulator round trip.

## Deferred (v2 and beyond)

| Item | Why deferred | Gate |
|------|--------------|------|
| Server sync over Tailscale | the manual round trip comes first | D-11 |
| Resumable or chunked uploads | a phone browser may sleep mid-upload; the first version shows progress and a keep-the-screen-on hint | O-14 |
| Video | Tryst does not support it yet | Tryst roadmap |
| Social features | out of scope for now | a new product decision |
| `all_members` join approval | owner approval is enough for now | D-21 |
| Per-user keys hidden from the admin | the trust model is server-side | D-3 |
| Shared Tryst library with Sanctum | wait for a stable API | O-9 |

## Riskiest assumption

That the manual export and restore procedure is acceptable day to day. S1 proves it works; the live deploy (L1) and M6
show whether it is pleasant. If it feels heavy, the edit features go unused and the server-sync step moves up.
