# Alcove — Data Model

> **Status:** Design intent (2026-11-12). Nothing is implemented. Two things are versioned separately:
> Alcove's **store schema** and Tryst's **database schema** (`schemaVersion` inside a backup, v19 at the time
> of writing). Alcove records the Tryst version it read and never dictates it.

## Principles

- **Uploads are immutable.** The original `TRYSTBK1` bytes are kept with their SHA-256 and never rewritten.
- **State is a chain of revisions.** An upload is a base revision. An edit is a change-set that produces a new
  revision. Nothing is mutated in place, so history, undo, diff and the 45 day admin recovery fall out of the design.
- **Generic over typed.** A backup is a set of tables of rows (`data.json`). Alcove reads and carries all of
  them. Only a named set of editable fields is understood; every other table and column survives a round trip untouched.
- **Everything is owner-scoped.** Every query takes an owner (and, for shared views, a space).
- **Multi-space from day 1**, even though v1 ships one deployment with a few spaces.

## Entities (draft)

| Entity | Notes |
|--------|-------|
| `user` | id, login, display name, role (admin or member), password hash (Argon2id), status (active, disabled; `deleted` and a TOTP secret arrive with their features), timestamps. **Built in M2b** |
| `session` | server-side, expiring, bound to a user; cleared on password reset. **Held in process memory, not in the database** (D-27): a restart signs everyone out |
| `space` | id, name, status, `join_approval` (`owner` in v1; `all_members` later) |
| `membership` | user, space, role (owner or member), state (requested, active, left, removed), timestamps |
| `invite` | one-time code or link stored as a hash, scope (server account or a space, with the role granted), target (new or existing account), expiry (default 7 days), creator, state (open, used, revoked, expired), redemption attempts |
| `profile` | one per user: display name, avatar media, and optional demographic fields copied from a Tryst `profile` row (sex, gender, birth date, ethnicity, height, body type, orientation). Each field carries its own share setting (default private) and a pointer to the revision it was imported from |
| `backup_password` | a member's stored Tryst backup password, encrypted, with label and optional hint |
| `upload` | id, owner, original filename, SHA-256, size, Tryst schema version, stored blob path, verified at |
| `revision` | id, owner, parent revision, kind (upload or edit), created by, created at |
| `change` | revision, table, row id, field, old value, new value, op (set, delete, restore) |
| `row_cache` | a materialized view of a revision's tables for fast reads (derivable, rebuildable) |
| `space_sharing` | per membership: include notes, include photos, show partner names, paused flag (defaults: notes and photos on, partner names hidden, not paused) |
| `entry_flag` | per entry: private (kept out of the space). Keyed by owner and entry id, so it survives new revisions |
| `join_consent` | per join request: the owner's decision and, in `all_members` mode, each current member's yes or no, with timestamps |
| `media` | owner, blob path, SHA-256, size, kind, has-location flag, location visibility |
| `tombstone` | what was deleted, by whom, when, and when it purges |
| `admin_log` | sequence, time, actor, action, target type and id, fields touched, previous hash, hash |
| `meta` | store schema version, created at, last log anchor |

## Retention

- **User bin:** 10 days. **Admin recovery:** 45 days. Both configurable.
- Purge removes the data and its blobs for real, and a test proves it. The admin log keeps ids and times only.
- A deleted user or space gets the same grace, then purges.

## Admin log integrity

Each entry stores `hash = H(previous hash || canonical entry)`. A verifier walks the chain. The head hash can be
exported or sent through the notifier so tampering is detectable (R8). The log never stores content.

## Migration rules (read before changing the schema)

**Store schema changes:**
1. Bump the schema version in `meta`.
2. Add a forward migration: additive and nullable. Never drop or repurpose a column.
3. Add a test that migrates a v(N-1) store and asserts the result.
4. Confirm the "rebuild the row cache from revisions" path produces the same schema.
5. Snapshot the data directory before any schema-touching upgrade; the runbook says how.

**Tryst schema changes** need nothing from Alcove when the reader stays generic. A newer unknown schema is
refused or opened read-only until it is added to the compatibility table and proven on an emulator.

## Open questions

See O-1 (encryption scheme and per-user keys), O-3 (editable fields), O-4 (restore semantics) and O-7 (search
index) in [DECISIONS.md](DECISIONS.md).

## Storage layout (D-16, proposed)

- **Sealed database:** one SQLite database on tmpfs holds `user`, `session`, `space`, `membership`, `invite`,
  `profile`, `backup_password`, `upload` (metadata), `revision`, `change`, `row_cache`, `space_sharing`, `entry_flag`, `join_consent`, `media`
  (metadata), `tombstone`, `admin_log` and `meta`, plus the FTS5 search index. It is sealed to the data directory as an
  AES-256-GCM blob with one previous generation.
- **Blobs:** original uploads (as Tryst wrote them) and photos (encrypted under the owner's data key) are files named
  by id. A purge deletes the blob and the user's wrapped data key.
- **Keys:** `user.wrapped_data_key` is the user's data key wrapped by the master key (from the key file).
- **Rebuildable:** `row_cache` and the search index are derived from the revisions and can be rebuilt after a failure.

## Riskiest assumption

That keeping each Tryst table generically as rows, with edits as change-sets, can be materialized back into a backup that
Tryst accepts for every supported schema version. This is the writer milestone's job to prove on an emulator.

## Implemented in M2a (store schema v1)

`schema_info` (the version), `meta` (small facts), `data_key` (owner id, the data key wrapped by the master key with the owner id as
associated data, created at) and the FTS5 table `search` (owner id, kind and ref as filters; `body` as the searchable text).
`manifest.json` in the data directory holds a random store id and the layout version, and binds every seal to it. Blobs live under
`blobs/<owner>/u/<id>` (uploads, verbatim, read-only) and `blobs/<owner>/m/<id>` (photos, encrypted under the owner's data key).
Owner ids and blob ids are limited to letters, digits, `-` and `_`, so a hostile name cannot leave its directory. The domain tables
arrive with M2b and M3, each as an additive forward migration.

## Implemented in M2b (store schema v2)

`user` (id, unique case-insensitive username, display name, role `admin` or `member`, status `active` or `disabled`, Argon2id password
hash, created at, last sign-in) and `admin_log` (sequence, time, actor, action, target type and id, fields touched, previous hash, hash).
Two triggers make the database itself refuse an `UPDATE` or `DELETE` on `admin_log`; the hash chain, not the triggers, is the real
defence, and the tests drop the triggers to prove the chain catches the edit. `meta` gains `adminlog.head` (the last sequence and hash,
updated in the same transaction as every entry, so cutting entries off the end is detected) and the console settings
`setting.allowed_networks`, `setting.proxy_mode`, `setting.trusted_proxies` and `setting.require_tls`. Sessions are not stored (D-27).
Free-text columns of `admin_log` accept only identifier characters, so no content can be logged by accident.

## Implemented in M3 piece A (no schema change)

`meta` gains `backup.last_ok` (unix time of the last verified backup) and `backup.last_drill` (`<unix time>:ok` or `:failed`). A
backup is not part of the store: it is a tar of the data directory's files (`manifest.json`, `alcove.sealed`, `blobs/<owner>/<kind>/<id>`)
plus `BACKUP.json` (every file's size and SHA-256) and a `<name>.sha256` checksum file next to it. The ordering rule that keeps a live
backup consistent: **a blob is written before the row that refers to it** (enforced by the upload code in piece B).

## Implemented in M3 piece B (store schema v3)

`upload` (id, owner, display file name, SHA-256, size, Tryst schema and its support status, row, entry and photo counts, time; unique per
owner and SHA-256), `revision` (id, owner, parent, kind `upload` or `edit`, the upload it is based on, who made it, time, `deleted_at` for
piece C), `backup_password` (owner, the password sealed with AES-256-GCM under the owner's data key, the owner id as associated data),
`cache_head` (owner, the revision it was built from, schema, table order) and `cache_row` (owner, table, row id or `#position`, position,
the row as raw JSON with its columns in file order). Everything except `upload`, `revision` and `backup_password` is derived and rebuilt
by `ingest.Rebuild`. The `search` table gains rows of kind `encounter`, `partner` and `profile` built from the note and name columns. The
head revision of an owner is the newest one with no `deleted_at`.

## Implemented in M3 piece C (store schema v4)

Two additive columns on `user`: `deleted_at` (a disabled account with this set is **deleted**, waiting for its purge) and `must_change`
(the person must choose a new password before anything else works). No table was rebuilt: the `status` check still allows only `active`
and `disabled`. `revision.deleted_at` (from v3) is now used: the soft delete of an upload. The data directory also holds an empty `.lock`
file whose advisory lock keeps a second process out. A purge deletes, in order: the revision and upload rows (one transaction), then the
stored files, then for an account all of its rows, its folder under `blobs/` and its data key; a sweep removes files with no row (older
than a day) and owners with no account, so a crash between the steps is finished by the next purge.

## Implemented in M7 (store schema v9)

`media_blob` (owner, media id, entry id, mime label, size, kind: 0 unknown, 1 JPEG, 2 PNG): the derived list of photos copied out of the current upload into the blob store (encrypted under the owner's data key), rebuilt with the cache. `membership.show_photo_location` (default 0).

## Implemented in M4 piece D (store schema v8)

`user_tz` (user, IANA zone name): no row means the server's default zone (`ALCOVE_TZ`, UTC if empty). Compare metrics are computed, never stored.

## Implemented in M4 piece B (store schema v7)

`profile` (owner, field, value, shared, time): one row per filled field from a fixed list (about, sex, gender, birth, ethnicity, height, body_type,
orientation, location); no row means empty; `shared` is 0 unless the owner ticked it. The name shown to others is `user.display_name`. Cards are
built by `share.Cards` and never read this table by another route.

## Implemented in M4 piece A (store schema v6)

`entry_flag` (owner, entry id, private, time), keyed by owner and entry id so it survives new uploads. Shared views read the owner's head-revision
`cache_row` through `share.Entries` and nothing else; `membership.consent_version` must equal the current wording for an owner's entries to appear.

## Implemented in M3 piece D (store schema v5)

`space` (id, name, creator, time, `join_approval`, the hash of the space code and when it was made, `deleted_at`), `membership` (space, user,
role `owner` or `member`, state `requested`, `active`, `left` or `removed`, the consent version agreed and when, and the D-21 sharing choices:
notes, photos, partner names, pause) and `invite` (id, the hash of the code, kind `space` or `server`, space, creator, expiry, state `open`,
`used` or `revoked`, who used it and when, a short note for the creator). Codes themselves are never stored. A space has exactly one active owner.
`spaces.Role` is the only code that decides visibility: it passes only an **active** member of a **live** space whose account is **active and
not deleted**. From M4 a member's entries are shown only if `consent_version` is the current one and `paused` is off.
