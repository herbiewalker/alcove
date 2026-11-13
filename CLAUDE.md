# Alcove — Project Context for Claude Code

> A self-hosted, LAN-only server where a couple (or a family) can upload their **Tryst** backups,
> manage and edit their entries on a computer, and see a shared, opt-in view of each other's data.

Project name **Alcove**, Go module **`github.com/herbiewalker/alcove`**, container **`alcove`**.
**Size class:** **L · app-service** — per the DevPlaybook sizing matrix. Network service, multiple
users, the most sensitive data category the owner holds, persistent state and cryptography. Full doc
suite, `THREAT_MODEL` and `DATA_MODEL` included, all checklists at full weight. Adapter:
`docker-service` (the author's private playbook).
**License:** AGPL-3.0-or-later (see ADR D-2).

A sibling of **[Sanctum](https://github.com/herbiewalker/sanctum)** (a simple, public backup vault that
stays small and never shows data) and a companion to **[Tryst](https://github.com/herbiewalker/tryst)**
(a privacy-first, local-only Android tracker). Alcove consumes and, for edits, **produces** Tryst's
`TRYSTBK1` backup container. Read
[`tryst/docs/EXPORT_FORMAT.md`](../tryst/docs/EXPORT_FORMAT.md) before touching any crypto or any
backup reading or writing here.

**Status: design phase.** No feature code yet. Kickoff produced the scaffold and these documents; the
next step is the design-docs pass, then milestone M0. Do not write feature code before the design docs
and ADRs they feed are reviewed.

---

## Hard constraints (these OVERRIDE default behavior — do not violate)

> If a request conflicts with one of these, **say so and propose an alternative** — do not quietly
> comply. Each one that can be a CI check **is** one (principle 1).

1. **Tryst is never modified.** Alcove requires **zero changes** to the Tryst app. Tryst never opens a
   socket. Uploaded backups are stored **immutably** with a SHA-256 and are never rewritten; an edit
   creates a new revision and an export is always a **new file**, never an overwrite.
   *Guard:* review; round-trip and corruption tests (flip a byte, verification must fail); an
   emulator round trip proves a written backup restores in Tryst (never on a physical phone).

2. **LAN only.** One published port. No port forwarding, no public DNS, no reverse proxy to the
   internet. A private overlay (Tailscale or WireGuard) may be added later; an exposed port never.
   *Guard:* compose assertion (one port, no host network); `RUNBOOK.md`; review.

3. **No outbound network calls and no telemetry by default.** The only exception is an optional
   notification URL the admin sets. No analytics, no update pings, no third-party scripts or fonts.
   *Guard:* `scripts/guards.sh no-outbound` (allowlist for the one notifier package); CSP.

4. **Encrypted at rest; the key lives apart from the data.** No key or passphrase in the repo, the
   image, `docker-compose.yml`, or the data volume. The app **refuses to start** if its key file sits
   inside the data directory. (This replaces Sanctum's "key never at rest": Alcove starts unattended
   so members never wait on the admin. A locked mode with a typed passphrase stays an option, ADR D-4.)
   Decrypted user data exists only in memory or tmpfs, never on the data volume (D-16).
   *Guard:* `scripts/guards.sh no-secrets`; a startup test for the key-file location; trivy secret scan;
   `scripts/guards.sh no-plaintext` (armed when storage lands in M2).

5. **Sharing is opt-in and private by default.** Nothing is visible to another member until its owner
   shares it: per space, per person, per category, per entry, with a one-tap **pause** and a
   **share all**. Partner names are hidden in shared views by default. A photo's location data is kept
   but shown to others only when the owner turns that on for it.
   *Guard:* tests for the default (nothing visible) and for cross-user and cross-space isolation.

6. **The admin can see and search everything, and every admin action is logged.** Soft-deleted items,
   paused shares and every space are visible to the admin. Every admin view, search, edit, restore and
   export writes to an **append-only, hash-chained admin log**. The log records which entry and which
   fields, never the content itself.
   *Guard:* tests that each admin action writes a log entry; a chain-verification test.

7. **Nothing is lost silently.** Deletes are soft. A user can restore from "recently deleted" for
   **10 days**; the admin can restore for **45 days** (both configurable). After the window a purge is
   a real deletion. A restore on the server is not a restore on the phone.
   *Guard:* tests for soft delete, both windows, and purge.

8. **No content in logs, alerts or error messages.** Logs carry ids, field names and counts, never
   entry text, partner names, notes or photos.
   *Guard:* a log-scrape test over a full upload, edit and export of a synthetic dataset.

9. **Least-privilege container.** Non-root, read-only rootfs, all capabilities dropped, no host
   network, base images pinned **by digest**, memory and CPU limits, healthcheck, SIGTERM handled,
   strict same-origin Content-Security-Policy.
   *Guard:* `scripts/guards.sh digest-pin`, plus CI: non-root assertion, compose exposure and limits
   check, trivy vulnerability and secret scan on the built image.

10. **No personal information in the repo (it will go public).** No real names, hostnames, IPs, paths
    or data. Fixtures are **synthetic**, generated at test time, never committed. Anything personal
    belongs in a separate private instance repo.
    *Guard:* `scripts/guards.sh no-secrets` (blocks `*.tryst`, keys, `.env`); gitleaks in CI; review.

11. **Adults only, invite only.** No open sign-up. Accounts are created by the admin or by a space
    owner's invite, and the first-run screen states it is for consenting adults.
    *Guard:* a test that no unauthenticated route can create an account.

---

## What it does (v1 scope)

- **Upload** Tryst backups; keep every one as an immutable revision, verified by a real trial decrypt.
- **Profiles and invites.** Prefill a member profile from the Tryst `profile` in their backup (each field opt-in
  to share) and invite people to the server or a space with one-time, expiring, revocable invites (D-14).
- **Multiple independent spaces**, each with its own members. A space owner approves who joins, and a
  person can belong to several spaces.
- **Shared calendar overlay and compare metrics**, built only from what each member has chosen to share.
- **Edit entries on a computer.** Each person edits their own data and the admin can edit anyone's.
  Every edit is a new revision with a visible history.
- **Export an edited `.tryst`** to restore on the phone (a manual round trip, not live sync).
- **Recover deleted things** and manage users: add, disable, delete, reset, and handle people joining or
  leaving a space.
- **Admin console** with accounts, spaces, health, search over everything, and the admin log.
- **Share photos** between members (location kept, shown only when the owner allows it).

**Out of scope for v1:** social features (feed, comments, messaging, moderation), live sync (a
Tailscale-based sync is a possible later step), public internet exposure, any change to Tryst, video
(until Tryst supports it), native apps.

See [docs/REQUIREMENTS.md](docs/REQUIREMENTS.md).

---

## Tech stack (proposed; each choice gets an ADR)

- **Language / runtime:** Go (see [SETUP.md](SETUP.md) for the pin). Single service, single binary.
- **Crypto:** `tink-go` (`AesGcmHkdfStreaming`, the primitive Tryst writes), PBKDF2-HMAC-SHA256 and
  Argon2id from `golang.org/x/crypto`. Never hand-roll the streaming AEAD.
- **Storage:** SQLite via `modernc.org/sqlite` (pure Go, no cgo), encrypted at rest. The exact scheme
  (per-user data keys, how the working copy is protected) is decided in the design pass.
- **HTTP / UI:** stdlib `net/http` with server-rendered `html/template`, inline CSS, at most one
  same-origin script. No web framework. Reuse Sanctum's responsive, light/dark/auto UI approach.
- **Build:** `go build`, `make`, multi-stage `Dockerfile` to distroless, non-root. **Deploy:**
  `docker compose` on Unraid. **Test:** `go test ./...` with synthetic fixtures.

## Architecture

> Details in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). In one paragraph: each upload becomes an
> immutable, verified revision of a person's Tryst data. A generic reader turns a backup into
> per-table rows and passes every table and column it does not edit through untouched, so Tryst's
> schema can move without breaking Alcove. Members choose what to share into a space; views are
> computed from shared rows only. Edits are new revisions; an exporter writes a fresh, compatible
> `.tryst`. An append-only admin log records everything the admin does.

## Build & run

```
# one-command bootstrap (Windows dev box)
.\bootstrap.ps1

# common tasks (Linux and CI; there is no make on the Windows dev box)
make build          # compile to ./bin/
make test           # go test ./...
make lint           # go vet + gofmt check
make guards         # the constraint guards from CLAUDE.md
make check          # everything CI runs, locally
```

> Toolchain pins and environment gotchas live in [SETUP.md](SETUP.md) — read it before the first build.

## Lessons carried over from Sanctum

- A second copy of the key is **proved by opening it**, not by believing a table (Sanctum lost a
  passphrase this way).
- Once edits live here, **Alcove is a system of record**: it needs its own encrypted backup and a tested
  restore drill.
- Cut releases only with a release script that runs the gates and checks CI on the exact commit.
- Real hardware beats a green build: a live smoke script before every release.
- Never run instrumented tests with a physical phone attached; use the emulator.

## Key documents

- [docs/REQUIREMENTS.md](docs/REQUIREMENTS.md)
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
- [docs/THREAT_MODEL.md](docs/THREAT_MODEL.md)
- [docs/DATA_MODEL.md](docs/DATA_MODEL.md)
- [docs/ROADMAP.md](docs/ROADMAP.md)
- [docs/DECISIONS.md](docs/DECISIONS.md) — ADR log
- [CHANGELOG.md](CHANGELOG.md)
- [CRITICAL_ASSETS.md](CRITICAL_ASSETS.md) — what git deliberately doesn't hold
