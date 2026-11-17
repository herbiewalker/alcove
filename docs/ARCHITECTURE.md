# Alcove — Architecture

> **Status:** Describes what is built as of v0.1.2 (2026-11-15), milestones M0 to M6 and the first piece of M7. The
> decisions behind each part are in [DECISIONS.md](DECISIONS.md). Update it in the same change that makes it stale (principle 3).

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

## Components

Every package below exists. The newest ones are `edit` (M5), `export` (M6) and `photo` (M7). The `server` package is the only one that speaks HTTP; everything else is plain Go that the tests drive directly.

| Package | Responsibility |
|---------|----------------|
| `cmd/alcove` | wiring, config from the environment, signals, graceful shutdown, `-healthcheck`, and the sub-commands (`keygen`, `keycheck`, `backup-verify`, `restore`, `drill`, `reset-password`, `test-backup`) |
| `internal/app`, `internal/config` | the start-up sequence and the environment-only settings; refuses to start when a hard constraint would be broken (key inside the data directory, no tmpfs) |
| `internal/keyfile`, `internal/keycli` | the master key file (generate, optional passphrase wrapping, fingerprint, the outside-the-data check) and the `keygen` and `keycheck` commands |
| `internal/seal`, `internal/store` | the sealed, in-memory SQLite database (D-16): migrations (schema v10), per-user data keys, full-text search, an atomic seal with one previous generation |
| `internal/blobstore` | uploads verbatim and immutable with a SHA-256; photos encrypted under the owner's data key |
| `internal/trystbk` (+ `synth`) | the `TRYSTBK1` reader and writer: header, PBKDF2, streaming AEAD, ZIP and generic `data.json` tables; `synth` makes deterministic synthetic backups for every test |
| `internal/ingest` | store an upload verbatim, judge it, record it as a revision with its derived row cache, search index and sealed password in one transaction; rebuild the cache (and replay edits); `Export` gathers an export plan |
| `internal/edit` | how a person's entries change (D-19, D-42): validated edits, add, delete and restore, history and undo, replay on rebuild, and the change summary the export page shows |
| `internal/export` | the cache written back as a fresh backup (D-43): photo filtering, the DEFLATE archive, sealing, and the read-back check before anything is offered |
| `internal/photo` | photo bytes on their way to other people: format checks, cleaning that keeps only the rotation (D-39, D-44), and thumbnails drawn from decoded pixels (D-44) |
| `internal/share` | the ONE read gate for other people's entries (D-32): membership, pause, consent wording, the private flag, include settings; the owner's own list and detail, the calendar days, galleries (D-40) and search |
| `internal/spaces` | spaces, memberships, invites, join requests and `Role`, the one visibility gate (D-31) |
| `internal/profile` | a member's profile fields, their share flags, the adult birth date rule, and what the backup's profile row offers (D-33) |
| `internal/compare` | the compare metrics registry, versioned and schema-aware, built on `share` output only (D-35) |
| `internal/auth` | accounts, Argon2id hashing, in-memory sessions with a step-up window, sign-in rate limiting |
| `internal/netsec` | transport security (D-23): allowed networks, proxy mode and client address, require-TLS, the self-reverting network change |
| `internal/adminlog` | the append-only, hash-chained admin log, its verifier and its filters (D-45); free-text fields are restricted to identifiers so content cannot reach it |
| `internal/retention`, `internal/admincli` | soft delete and restore (10 and 45 day windows), the real purge and its crash sweep, and the break-glass `reset-password` |
| `internal/backup`, `internal/backupcli` | the instance backup (a tar of ciphertext with a hashed index), verify, restore, retention, the restore drill, and their commands (D-28) |
| `internal/server` | the web layer: security headers, network gate, CSRF, templates and the one script; sign-in, accounts, spaces, entries, galleries, editing, the export pages, and the admin console (`/admin/people` for editing anyone's data, with every look and change logged) |
| `tools/logocrop` | makes the square header mark and favicons from the master logo |

Not built: `internal/notify` (the optional single-URL notifier, F24) and TOTP (F20). Both are optional and sit in M8.

## Key data flows

1. **Upload.** Read the cleartext header, bound the iteration count, SHA-256 the file, trial-decrypt with the
   member's stored password, read tables generically, then store the original bytes and record a base revision.
   Duplicates are refused before any KDF work.
2. **Edit.** The browser edits an entry (or a partner, or adds or deletes a whole entry); the server records an edit revision over the upload (D-42):
   `change` rows of table, row id, field, old and new value, written in the same transaction as the cache row and search text they change. A rebuild
   re-reads the upload and replays every live edit, and a property test proves both paths agree. When the administrator edits, the same
   transaction writes an admin log line naming the table, row and fields, never the values.
3. **Export.** Open the head upload with the owner's password, keep everything that is not a table row as uploaded, put in the cache rows
   (edits, additions and deletions included), leave out photos whose only entries were deleted, write one DEFLATED archive with `data.json` first, seal
   it, and read it back with the real reader before offering it (D-43). The page shows the base upload, what changed and the restore steps, and needs
   a ticked "fresh phone backup first" box and step-up re-login. One admin log line records counts only.
4. **Profile and invite.** Prefill reads the `profile` row of a verified upload and proposes display name and photo; every other field is imported only if the member ticks it, each with its own private-by-default share setting. An invite is a random one-time token (only its hash is stored) with a scope, expiry and role; redeeming it creates an account or a membership. No e-mail is sent.
5. **Share.** Views read every member's entries in a space through the visibility check (membership, pause, the per-entry private flag, include settings). Isolation is
   tested across users and spaces.
6. **Recover.** Deletes are soft: an upload or an entry is restorable by its owner within 10 days and by the administrator within 45; the purge then really removes it (an entry's stored values are wiped, the fact of the deletion stays).

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
backup first, then always recommends wipe first, and summarises what changed since the upload (the export page, D-43). The emulator round trip (D-43 addendum, Tryst 0.6.5) proved wipe first exact and merge still unsafe; `scripts/emulator/roundtrip.ps1` repeats it.

## Riskiest assumption

That a sealed in-memory database plus per-user data keys is enough at household scale without surprising RAM use or
losing recent writes on a crash. Mitigation: seal on every important write; measure with a synthetic large dataset in M2.

## Web UI and the phone workflow (D-22)

Server-rendered, mobile-first pages with inline CSS and at most one same-origin script (upload and download progress, the time-zone button). The phone flow:
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
