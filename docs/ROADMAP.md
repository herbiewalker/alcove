# Alcove — Roadmap

> **Status:** Draft (2026-11-12). Milestones in order; **foundations before features** (principle 2). Each has
> exit criteria and is not done until they are met. Crypto and storage come before views; the risky writer is
> gated by an emulator proof; the UI grows with each milestone.

---

## M0 — Scaffold, CI and container *(no features)*

- [x] Repo, AGPL-3.0, `.gitignore`, `.gitattributes`, `.editorconfig`
- [x] `CLAUDE.md` with hard constraints and size class, README with an honest status, SETUP, CRITICAL_ASSETS
- [x] Go module, a scaffold binary that serves `/healthz` and shuts down gracefully
- [x] `bootstrap.ps1`, `Makefile`, `scripts/guards.sh` (no-secrets, no-outbound, digest-pin), forced to fail then restored
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
- **Exit:** interop against a real backup on a private copy; every negative test fires.

## M2 — Core: storage, key, accounts, admin log

- [ ] Storage per D-16 (review it first): sealed tmpfs database with FTS5, per-user data keys, master key from a key file outside the data directory with the refuse-to-start check, optional passphrase-wrapped locked mode; arm `guards.sh no-plaintext`; measure RAM on a large synthetic dataset
- [ ] First-run admin; accounts; password hashing; sessions; rate limiting; step-up
- [ ] Append-only hash-chained admin log and its verifier (N5)
- [ ] Strict CSP, responsive UI shell (light, dark, auto) reusing Sanctum's approach
- **Exit:** a server restart needs no human unlock; no unauthenticated route creates an account; the chain verifies.

## M3 — Spaces, upload, revisions, recovery

- [ ] Spaces, memberships, invites (F27), join requests, leave and removal (F4 to F6)
- [ ] Upload and verify; immutable revisions; duplicate refusal (F7, F8)
- [ ] User lifecycle: add, disable, delete with grace, reset, data download (F2, F3)
- [ ] Soft delete with the 10 and 45 day windows and a real purge (F16, N13)
- **Exit:** isolation tests across spaces pass; a deleted entry restores inside the window and is gone after it.

## M4 — Sharing, calendar overlay, compare

- [ ] Metrics registry per D-18 (reviewed whenever Tryst Insights changes); space visibility, pause, per-entry private, include settings and the join consent rule (F5, F11; D-21)
- [ ] Profile prefill from the Tryst profile with per-field sharing, and member cards (F26, F28; D-14)
- [ ] Shared calendar overlay (F12), then compare metrics (F13)
- **Exit:** a non-member and another space see nothing; pause, the private flag and leave are honoured; the join consent rule holds.

## M5 — Editing

- [ ] Editing scope per D-19, including adding new entries; change-sets, revisions, history and diff in the browser (F14)
- [ ] Admin edit of anyone's data, logged; recovery from any revision
- **Exit:** edits never alter an upload; the admin log records every edit without content.

## M6 — Backup writer and export *(the riskiest milestone)*

- [ ] Confirm D-15 on an emulator first: both restore modes, a deletion, a phone-only entry, link-row cascade
- [ ] Writer: materialize a revision, re-encrypt as a fresh `TRYSTBK1`; base-revision warning and diff (F15)
- [ ] **Emulator round trip:** export an edited backup, restore it in Tryst, confirm the edits (N8). Never a physical phone.
- **Exit:** the round trip passes on every supported Tryst schema in the compatibility table.

## M7 — Photos

- [ ] Media store; blur until clicked; sharing; location kept and shown per photo (F19, R4); resolve O-6

## M8 — Admin console and operations

- [ ] Account, space and health pages; search over everything (O-7); log viewer and export (F17, F18)
- [ ] Optional TOTP (F20); optional notifications (F24); optional member activity view (F25, O-8)
- [ ] Encrypted instance backup and a restore drill (F23)

## M9 — Deploy to Unraid and release

- [ ] RUNBOOK, live smoke script, deploy on a real host, validation ladder on synthetic then real data
- [ ] Phase 3 audit passes (one lens per fresh session), then v0.1.0 via the release script
- **Exit:** the README's design-phase status line comes off, and not before.

---

## Deferred (v2 and beyond)

| Item | Why deferred | Gate |
|------|--------------|------|
| Server sync over Tailscale | the manual round trip comes first | D-11 |
| Video | Tryst does not support it yet | Tryst roadmap |
| Social features | out of scope for now | a new product decision |
| Per-user keys hidden from the admin | the trust model is server-side | D-3 |
| Shared Tryst library with Sanctum | wait for a stable API | O-9 |

## Riskiest assumption

That M6 (the writer) is a normal-sized milestone. It is the one most likely to expand, because it depends on Tryst's
schema and restore behaviour. Prove D-15 on an emulator before M5 ships any edit UI.
