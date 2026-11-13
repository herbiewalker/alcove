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
- [x] `bootstrap.ps1`, `Makefile`, `scripts/guards.sh` (no-secrets, no-outbound, no-plaintext stub, digest-pin), forced to fail then restored
- [x] Design docs seeded: REQUIREMENTS, ARCHITECTURE, THREAT_MODEL, DATA_MODEL, DECISIONS, this file
- [ ] Dockerfile (multi-stage, distroless, digest-pinned, non-root), `docker-compose.yml`, `.env.example`
- [ ] CI: build, vet, gofmt, `go test -race`, the guards, image checks, trivy
- [ ] Release script (reuse Sanctum's) and a live smoke script skeleton
- **Exit:** a clean checkout reaches a green build and a green CI run with every guard armed.

## M1 — Tryst reader *(read only)*

- [ ] Copy and extend `trystbk` (D-9): header parse with bounds, PBKDF2, streaming AEAD, ZIP walk with Zip-Slip checks
- [ ] Generic table reader for `data.json`; schema-version recording; supported-version table
- [ ] A synthetic-backup generator for tests (never real data)
- [ ] Forced negative tests: wrong password, flipped byte, truncation, absurd KDF count, Zip-Slip, zip bomb
- **Exit:** interop against a real backup on a private copy; every negative test fires; a generated backup restores in
  Tryst on an emulator (so our test data is proven realistic).

## S1 — Round-trip spike *(de-risks the edit idea before anything depends on it)*

- [ ] A throwaway minimal writer: take a backup read by M1, change one entry, add one, delete one, re-encrypt as `TRYSTBK1`
- [ ] On an **emulator** (never a physical phone), restore it in both Tryst modes and record what happens: edit applied,
      new entry added, deleted entry gone (wipe) or kept (merge), a phone-only entry lost (wipe) or kept (merge),
      link rows after a parent replace (D-15)
- [ ] Write the findings into `DECISIONS.md` (confirm or amend D-15)
- **Exit:** D-15 is confirmed on a device, or the edit design is revised before M2 starts. The spike code is discarded.

## M2a — Storage foundation

- [ ] Storage per D-16 (review it first): sealed tmpfs database with FTS5, per-user data keys, master key from a key file
      outside the data directory with the refuse-to-start check, optional passphrase-wrapped locked mode
- [ ] Arm `scripts/guards.sh no-plaintext`; measure RAM and seal time on a large synthetic dataset
- [ ] Rebuild path for the row cache and search index from revisions
- **Exit:** a restart needs no human unlock (default mode); the key file inside the data directory refuses to start;
  the plaintext guard passes and was forced to fail; wrong key fails cleanly.

## M2b — Accounts, admin log, UI shell

- [ ] First-run admin; accounts; password hashing; sessions; rate limiting; step-up re-login
- [ ] Append-only hash-chained admin log, its verifier (N5), and a **basic log viewer**
- [ ] Strict CSP and a **mobile-first** responsive UI shell (light, dark, auto), reusing Sanctum's approach (D-22)
- [ ] Transport security per D-23: CSRF tokens, allowed networks (private ranges by default), proxy mode, insecure-connection banner and require-TLS mode, with the auto-revert on allowed-network changes (F32, N17)
- **Exit:** no unauthenticated route creates an account; the chain verifies and a tampered entry is detected; the shell
  is usable on a phone-width screen with the keyboard and touch.

## M3 — Spaces, upload, revisions, recovery *(real data starts here)*

- [ ] Spaces, memberships, invites (F27), join requests, leave and removal (F4 to F6)
- [ ] Upload and verify; immutable revisions; duplicate refusal (F7, F8). **Upload works from a phone browser over the LAN
      (F30)**, with a progress bar
- [ ] User lifecycle: add, disable, delete with grace, reset, data download (F2, F3), with **basic admin pages**
- [ ] Soft delete with the 10 and 45 day windows and a real purge (F16, N13)
- [ ] **Encrypted instance backup and a restore drill (F23)**, and the data key's second copy **proved by opening it**
      (CRITICAL_ASSETS) before the first real upload
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

- [ ] Writer: materialize a revision, re-encrypt as a fresh `TRYSTBK1`; base-revision warning and diff (F15)
- [ ] Guided restore procedure with both modes explained and the "fresh phone backup first" confirmation (F29, D-15)
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
