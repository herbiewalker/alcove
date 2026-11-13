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
| `user` | id, login, display name, role flag (admin), password hash, TOTP secret (encrypted), status (active, disabled, deleted), timestamps |
| `session` | server-side, expiring, bound to a user; cleared on password reset |
| `space` | id, name, status |
| `membership` | user, space, role (owner or member), state (requested, active, left, removed), timestamps |
| `invite` | one-time code or link stored as a hash, scope (server account or a space, with the role granted), target (new or existing account), expiry (default 7 days), creator, state (open, used, revoked, expired), redemption attempts |
| `profile` | one per user: display name, avatar media, and optional demographic fields copied from a Tryst `profile` row (sex, gender, birth date, ethnicity, height, body type, orientation). Each field carries its own share setting (default private) and a pointer to the revision it was imported from |
| `backup_password` | a member's stored Tryst backup password, encrypted, with label and optional hint |
| `upload` | id, owner, original filename, SHA-256, size, Tryst schema version, stored blob path, verified at |
| `revision` | id, owner, parent revision, kind (upload or edit), created by, created at |
| `change` | revision, table, row id, field, old value, new value, op (set, delete, restore) |
| `row_cache` | a materialized view of a revision's tables for fast reads (derivable, rebuildable) |
| `share_rule` | owner, space, scope (all, category, entry), visibility flags, paused flag |
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
