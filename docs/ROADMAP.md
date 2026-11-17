# Alcove — Roadmap

> **Status:** Phase 1 roadmap, approved 2026-11-12 with the review-pass changes (round-trip spike, M2 split,
> instance backup before real data, early admin pages, first live deploy, authorization audit, mobile-first UI).
> Milestones in order; **foundations before features** (principle 2). Each has exit criteria and is not done until
> they are met. Crypto and storage come before views; the risky writer is proven early by a spike and finished late;
> the web UI is mobile-first from the first page.
>
> **Updated 2026-11-15 (v0.1.2):** M0 to M6 are done. M7 (photos) is partly done, M8 (admin search, log filters, optional TOTP and notifications) and M9 (the audit) are not started, and the live validation (L1) is the owner's.


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
- [x] Rebuild path for the row cache and search index from revisions *(moved to M3, where revisions first existed; edits are replayed on it since M5)*
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

- [x] **Piece D, built 2026-11-14: spaces, memberships, invites and join requests (D-31).** `internal/spaces` with ONE visibility gate (`Role`) that every space page uses and every later shared view will; an outsider gets the same 404 as for a space that does not exist. Any signed-in person can start a space. Three ways in, all through a join screen that states exactly what is shared and needs a ticked box (its version is recorded): a one-time space invite (new or existing account), a revocable space code that only lets a person ask, and an admin server invite. Codes are 80-bit and stored only as hashes; redeeming spends the invite and creates the account in one transaction; attempts are rate limited. Leave, remove, hand over and delete (soft, with the same windows) work; the admin sees and manages every space with each look logged; a purged account leaves its spaces and hands on ownership. Isolation, constraint 11 and each guard are proven by tests and by mutation
- [x] **Piece B, built 2026-11-14: upload, revisions, stored passwords and the row cache (D-29).** `internal/ingest` and the My data page.
      The upload is one streamed request (token first, file last, token checked before a byte of the file is read), written to the blob
      store, judged (duplicate, wrong password, not a backup, damaged, hostile header, oversize) and only then recorded in one
      transaction with its revision, the derived row cache, the search index and the sealed password. A refusal records nothing. The
      cache and search rebuild from the head revision (admin button, logged). A progress bar needs the project's one same-origin script
      (CSP gains `script-src 'self'` and `connect-src 'self'` and nothing else). Checked in a phone-sized browser, both the refusal and
      the success path. `alcove test-backup` writes synthetic backups so none of this needs real data
- [ ] **Still open from piece B:** a real backup uploaded from a real phone over the real LAN (F30), which needs the first live deploy
- [x] Rebuild path: the row cache and search index can be rebuilt from revisions (moved here from M2a)
- [x] **Piece C, built 2026-11-14: accounts, soft delete, purge and the break-glass reset (D-30).** The admin People page (add with a
      temporary password shown once and a forced change at first sign-in; disable, enable, reset, delete with a grace period, restore),
      a person's own delete, restore and "download all my data" (step-up for everyone), the admin's Recently deleted page, the
      `internal/retention` purge (hourly and on demand; real deletion of files, rows, search, remembered password and data key, plus a
      sweep for crash leftovers), `alcove reset-password`, and a data-directory lock so two processes can never share a store. Schema v4
      (additive columns). Each security behaviour was proven by mutation: the purge's file and key deletions, the forced-change gate,
      session revocation on disable, and step-up before a person's delete
- [x] Soft delete with the 10 and 45 day windows and a real purge (F16, N13)
- [x] **Piece A, built 2026-11-14: instance backup and a restore drill (F23, D-28).** `internal/backup` (one tar of ciphertext with a
      hashed index and a sidecar checksum, consistent by ordering, never overwriting), the admin **Backups** page with background jobs
      (back up, verify, restore drill, retention), and `alcove backup-verify`, `restore` and `drill`. The same flow is smoke-tested
      end to end on the real image (`scripts/smoke-backup.sh`, in CI and the live smoke). Still **your step**: prove the key's second
      copy with `drill` and a real backup (CRITICAL_ASSETS) before the first real upload
- Order of the remaining pieces (decided with the owner): **B** upload, revisions, backup passwords and the rebuildable row cache;
  **C** user lifecycle, soft delete and purge; **D** spaces, invites and join requests. Revisions are kept forever and the admin can
  purge them on purpose (logged)
- **Exit (met 2026-11-14 in tests, in a phone-sized browser and in the container smoke, except the last item):** isolation tests across spaces pass; a deleted entry restores inside the window and is gone after it; an
  instance backup restores to a scratch location and reads back; a real backup uploads from a phone browser.

## M4 — Collaborative spaces, overlay, compare

- [x] **Piece A (D-32):** the shared-entries gate, the per-entry private flag, include-notes, consent versions with a re-agree screen, a shared list and "My entries". Pieces B (profiles), C (calendar) and D (compare) follow
- [x] **Piece B (D-33):** profile page, prefill from the backup with per-field choice, per-field sharing, birth date as an age, member cards. No avatar yet (arrives with photo sharing)
- [x] **Piece C (D-34):** the calendar overlay, with the "more than one member active" highlight and a day view
- [x] **Piece D (D-35):** the compare metrics registry (At a glance, Rhythm, Trend, Variety), the Compare page, per-member time zones
- [x] **Open an entry (D-37):** a read-only entry page for the owner and, through the gate, for space members
- [x] **Search (D-38):** members search their own entries and the notes a space shows them
- [x] Metrics registry per D-18 (reviewed whenever Tryst Insights changes); space visibility, pause, per-entry private,
      include settings and owner approval of joins (F5, F11; D-21); the `all_members` mode is a later per-space setting
- [x] Profile prefill from the Tryst profile with per-field sharing, and member cards (F26, F28; D-14)
- [x] Calendar overlay (F12), then compare metrics (F13)
- **Exit:** a non-member and another space see nothing; pause, the private flag and leave are honoured; owner approval
  of joins holds; layout and keyboard checks pass at phone, tablet and desktop widths.

## L1 — First live deploy *(read-only features; the validation ladder starts here)*

- [x] `RUNBOOK` (section 12), `docker-compose`, the Unraid template and its guard, the live smoke script. **Still to do on the real host:** deploy, LAN-only, limits applied
- [ ] Deploy on the real host (releases 0.1.0 to 0.1.2 are published; this is the owner's step, see RUNBOOK 12 and 13)
- [ ] Validation ladder (principle 11): synthetic data, then one real backup, then the full history
- [ ] Checked from a real phone browser: upload a backup from the phone, view the overlay
- [x] Runbook recipes for HTTPS (Caddy or Nginx Proxy Manager, `tailscale serve`); the default stays secured wifi (D-23)
- **Exit:** the two of you use it for real. The README status line says what is and is not proven.

## M5 — Editing

- [x] Editing scope per D-19, including adding new entries; change-sets, revisions, history and diff in the browser (F14). **Slices 1 to 3 done (D-42): own-entry fields, set fields, partner names, add, delete and restore, history, undo.**
- [x] Admin edit of anyone's data, logged; recovery from any revision. **Slice 4 done (D-42): /admin/people, every look and change in the admin log, the 45-day restore.** Not done: the administrator adding an entry for someone, and viewing photos (admin search is M8).
- **Exit:** edits never alter an upload; the admin log records every edit without content.

## M6 — Backup writer and export *(the riskiest milestone; S1 has already de-risked the concept)*

> Design accepted in D-43 (2026-11-15). Slices: (1) the writer and read-back (**done**), (2) the export pages (**done**), (3) the emulator round trip (**done**, D-43 addendum).

- [x] Writer: materialize a revision, re-encrypt as a fresh `TRYSTBK1` (all entries DEFLATED, `data.json` first); base-revision warning and diff (F15)
- [x] Guided restore procedure: wipe first only, merge warned against, plus the "fresh phone backup first" confirmation (F29, D-15 addendum)
- [ ] **Phone download flow (F30):** the export downloads with a clear filename and the page says how to restore it from Downloads in Tryst (CI proves the download; trying it in a phone browser is still to do)
- [x] **Emulator round trip:** done on Tryst 0.6.5 (D-43 addendum): wipe first gives 0 differences in two start states; merge re-confirmed unsafe. Re-run with `scripts/emulator/roundtrip.ps1` (docs/RELEASE_CHECKS.md). Never a physical phone.
- **Exit:** the round trip passes for Tryst schema v19 (and each further version only after it is added to the
  compatibility table and proven). Met 2026-11-15 on an emulator for Tryst 0.6.5.

## M7 — Photos

- [x] **First piece (D-39):** photos copied out encrypted at upload, blurred until tapped, shared through the space with the entry, place off by default, metadata stripped for others, O-6 resolved (text coordinates, no map). **Gallery (D-40) done.** **Still to do:** thumbnails, partner and profile pictures
- [x] Media store; blur until clicked; sharing; location kept and shown per photo (F19, R4); O-6 resolved (D-39)
- [x] Thumbnails (D-44): made on demand, kept only in memory; shared photos keep their rotation
- [ ] Partner pictures and profile pictures
- **Exit:** photos upload and view on a phone and a desktop; location is shown only when its owner allows it.

## M8 — Admin console and operations

- [x] Settings page for transport security (D-23), People and Spaces pages: built earlier.
- [x] Log viewer filters and CSV export (F18, D-45)
- [ ] A fuller health page; search over everything (D-17, F17)
- [ ] Optional TOTP (F20); optional notifications (F24); optional member activity view (F25, O-8)
- [ ] Optional QR codes for the server address and invites, generated locally (F31)
- **Exit:** the admin can find and fix a user's problem without a shell; every admin action is in the log.

## M9 — Harden and release

- [ ] Phase 3 audit passes, one lens per fresh session: **authorization and isolation (who can see what)**, crypto and
      storage, secrets and logging, input and injection, build hardening, accessibility and mobile
- [ ] Final live validation on the real host and a real phone, then a release that says the audit passed (0.1.0 to 0.1.2 shipped earlier, before it)
- **Exit:** the README says the audit has passed, and not before.

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
