# Alcove — Architecture

> **Status:** Design intent (2026-11-12). Nothing below is implemented beyond the scaffold. Update it in
> the same change that makes it stale (principle 3).

## Stack

| Layer | Choice | Why |
|-------|--------|-----|
| Language | Go (pin in [SETUP.md](../SETUP.md)) | static binary, distroless, non-root, read-only rootfs nearly free |
| Crypto | `tink-go` `AesGcmHkdfStreaming`, PBKDF2 and Argon2id from `golang.org/x/crypto` | the primitive Tryst writes, never hand-rolled |
| Storage | SQLite via `modernc.org/sqlite` (pure Go), encrypted at rest | no cgo; scheme in O-1 |
| HTTP / UI | stdlib `net/http`, `html/template`, inline CSS, at most one same-origin script | smallest audit surface; reuses Sanctum's UI approach |
| Deploy | `docker compose` on Unraid | same as Sanctum |

## Shape

```
   phone (Tryst, offline)            home network                  Alcove container
 ┌─────────────┐  export   ┌──────────────────────┐       ┌───────────────────────────────────┐
 │  .tryst     │ ────────► │ browser upload (LAN) │ ────► │ upload ─► verify ─► REVISION      │
 └─────────────┘           └──────────────────────┘       │              (immutable)          │
        ▲                                                  │   reader ─► rows (generic tables) │
        │ restore edited .tryst                            │       │                           │
        │                                                  │   change-sets (edits) ─► REVISION │
 ┌─────────────┐  download ┌──────────────────────┐       │       │                           │
 │  .tryst     │ ◄──────── │ browser (LAN)        │ ◄──── │   writer ─► fresh .tryst          │
 └─────────────┘           └──────────────────────┘       │                                   │
                                                          │ space visibility ─► SPACE VIEWS  │
                                                          │ admin log (hash chain)            │
                                                          └───────────────┬───────────────────┘
                                                                          ▼
                                                             data dir (encrypted)   key file (elsewhere)
```

## Planned components

| Package | Responsibility |
|---------|----------------|
| `cmd/alcove` | wiring, config from the environment, signals, graceful shutdown, `-healthcheck` |
| `internal/config` | env-only config; refuses to start if the key file is inside the data directory |
| `internal/trystbk` | `TRYSTBK1` reader and writer: header, PBKDF2, streaming AEAD, ZIP and generic `data.json` tables |
| `internal/store` | encrypted persistence: revisions, change-sets, media, soft-delete tombstones |
| `internal/backup`, `internal/backupcli` | instance backup (one tar of ciphertext, hashed index, sidecar checksum), verify, restore, retention, and the restore drill; the command-line tools (M3 piece A, D-28) |
| `internal/ingest` | M3 piece B: store an upload verbatim, judge it (duplicate, password, format, size), record it as a revision with its derived row cache, search index and sealed password in one transaction; rebuild the derived data (D-29) |
| `internal/retention`, `internal/admincli` | M3 piece C: soft delete and restore (user and admin windows), the real purge and its crash-leftover sweep, and the break-glass `reset-password` (D-30) |
| `internal/auth` | accounts, Argon2id password hashing, in-memory sessions with a step-up window, sign-in rate limiting (M2b). TOTP arrives later |
| `internal/netsec` | D-23 transport security: allowed networks, proxy mode and client-address resolution, require-TLS, and the self-reverting network change (M2b) |
| `internal/server` | M2b web layer: the middleware chain (security headers, network gate, locked and first-run gates), CSRF, templates and static files, sign-in, first run, the admin console, log viewer and network settings |
| `internal/spaces` | spaces, memberships, invites and join requests, roles, member profile cards |
| `internal/profile` | profile prefill from a Tryst `profile` row, per-field share settings |
| `internal/sharing` | the visibility check every view goes through: space membership, pause, per-entry private flag, include settings |
| `internal/views` | calendar overlay and compare metrics computed from shared rows |
| `internal/edit` | change-sets, diff, history, restore from revisions |
| `internal/adminlog` | append-only hash-chained log and its verifier |
| `internal/retention` | soft-delete windows and purge |
| `internal/httpapi` | routes, templates, CSP, static files |
| `internal/notify` | optional single-URL notifier (the only outbound client) |

## Key data flows

1. **Upload.** Read the cleartext header, bound the iteration count, SHA-256 the file, trial-decrypt with the
   member's stored password, read tables generically, then store the original bytes and record a base revision.
   Duplicates are refused before any KDF work.
2. **Edit.** The browser edits an entry; the server records a change-set over a base revision (table, row id,
   field, old, new) and produces a new revision. The admin log records the action without content.
3. **Export.** Materialize a revision's tables (edited rows plus every untouched table and column), re-encrypt
   as a fresh `TRYSTBK1`, and offer it as a new file. Show the base upload, warn if a newer one exists, and
   show a diff first. Requires step-up re-login.
4. **Profile and invite.** Prefill reads the `profile` row of a verified upload and proposes display name and photo; every other field is imported only if the member ticks it, each with its own private-by-default share setting. An invite is a random one-time token (only its hash is stored) with a scope, expiry and role; redeeming it creates an account or a membership. No e-mail is sent.
5. **Share.** Views read every member's entries in a space through the visibility check (membership, pause, the per-entry private flag, include settings). Isolation is
   tested across users and spaces.
6. **Recover.** Deletes write tombstones; a user restores within 10 days, the admin within 45; retention purges later.

## Cross-cutting concerns

- **Config** is environment-only and holds no secrets. The key is a file outside the data directory (D-4).
- **Logging** to stdout, structured, never with content (N4).
- **Trust boundary.** Uploads are untrusted input: a crafted header, Zip-Slip names, a zip bomb or a truncated
  stream must be a rejection with a log line, not a crash (N9).
- **Persistence and migrations** are in [DATA_MODEL.md](DATA_MODEL.md).
- **Schema drift.** Tryst's schema moves. The reader is generic; only a named set of editable fields is typed,
  and an unsupported newer schema is refused or opened read-only (F10).

## Storage and keys (D-16 and D-26, implemented in M2a)

```
 key file (outside the data dir) ──► master key ──┬─► seals the database blob
                                                  └─► wraps each user's data key ─► encrypts that user's photos
 tmpfs:  alcove.db (SQLite, FTS5 index inside)  ──sealed every few seconds and after important writes──►  data dir
 data dir:  sealed database (+ previous generation) · original uploads (as Tryst wrote them) · encrypted photos
```

- **Structured data** (accounts, spaces, shares, change-sets, row cache, search index) lives in SQLite on tmpfs and is
  sealed to the data directory, with one previous generation kept (Sanctum's pattern). Uploads and edits seal at once.
- **Blobs** are files: uploads exactly as Tryst wrote them, photos re-encrypted under the owner's data key.
- **Locked mode.** If the key file is itself wrapped by an Argon2id key from a typed passphrase, the server starts locked.
- **No plaintext on the volume.** Decrypted data lives only in memory or tmpfs; `guards.sh no-plaintext` proves it.
- **Search** is FTS5 in the same sealed database (D-17).

## Restore modes and the export flow (D-15)

Tryst restores in one of two modes. **Wipe first** replaces everything exactly (server deletions propagate; any phone entry newer
than the backup is lost). **Merge** overwrites rows by id and adds new ones, but deletions do not propagate and it deletes every
partner link not in the backup (observed on an emulator, D-15 addendum). The export guide asks the user to confirm a fresh phone
backup first, then always recommends wipe first, and shows a diff of what the restore would change.

## Riskiest assumption

That a sealed in-memory database plus per-user data keys is enough at household scale without surprising RAM use or
losing recent writes on a crash. Mitigation: seal on every important write; measure with a synthetic large dataset in M2.

## Web UI and the phone workflow (D-22)

Server-rendered, mobile-first pages with inline CSS and at most one same-origin script (upload progress). The phone flow:
export in Tryst, open Alcove in the phone browser on the LAN, upload; later download the edited `.tryst` and restore it from
Downloads in Tryst. Uploads stream to the server with a progress bar; exports download with a clear filename. Reuse
Sanctum's layout, themes and strict CSP.

## The Tryst reader and writer (`internal/trystbk`, D-25)

`trystbk.Read(r, password)` validates the 29-byte header (magic, version, KDF bounds) before any key derivation, derives the
key, decrypts the AEAD stream into a bounded in-memory buffer, then walks the inner ZIP: every entry name is validated, data.json
is parsed into tables of **raw-JSON rows** in file order, small unknown entries and unknown top-level keys are carried, and media
entries are listed but only read on demand. `Backup.Close` zeroes the plaintext. `trystbk.Seal` writes a container with the same
primitives. `trystbk/synth` generates deterministic synthetic backups shaped like Tryst's v19 schema for every test in the project.

### Packages added in M2a

| Package | What it does |
|---------|--------------|
| `internal/keyfile` | the master key file: generate, plain and passphrase-wrapped text forms, load, fingerprint, and the outside-the-data-directory check |
| `internal/keycli` | the `alcove keygen` and `alcove keycheck` commands |
| `internal/seal` | AES-256-GCM seal of the database snapshot, bound to the manifest, atomic write with one previous generation and crash recovery |
| `internal/store` | the SQLite database on tmpfs: migrations, durable and lazy writes, the background sealer, per-user data keys, full-text search |
| `internal/blobstore` | uploads verbatim and immutable with a SHA-256; photos encrypted under the owner's data key |
| `internal/config`, `internal/app` | environment settings and the startup sequence that refuses to start when a hard constraint would be broken |

Start-up order: configuration, then the key-outside-data check, then reading the key (a wrapped key stops here and waits for an unlock),
then the tmpfs check, then unsealing or creating the store, then listening. A refused start creates nothing.
