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
                                                          │ sharing rules ─► SPACE VIEWS      │
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
| `internal/auth` | accounts, password hashing, sessions, rate limiting, TOTP, step-up |
| `internal/spaces` | spaces, memberships, invites and join requests, roles, member profile cards |
| `internal/profile` | profile prefill from a Tryst `profile` row, per-field share settings |
| `internal/sharing` | sharing rules and the visibility check every view goes through |
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
5. **Share.** A member's sharing rules select rows; views read only through the sharing check. Isolation is
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

## Storage and keys (D-16, proposed)

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

Tryst restores in one of two modes. **Wipe first** replaces everything, so server deletions propagate and any phone
entry newer than the backup is lost. **Merge** overwrites rows by id and adds new ones, but deletions do not
propagate and link rows can cascade. The export guide asks the user to confirm a fresh phone backup first, recommends a
mode, and shows a diff of what each mode would change.

## Riskiest assumption

That a sealed in-memory database plus per-user data keys is enough at household scale without surprising RAM use or
losing recent writes on a crash. Mitigation: seal on every important write; measure with a synthetic large dataset in M2.
