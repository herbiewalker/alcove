# Alcove — Decision Log (ADRs)

> **Status:** Current through v0.1.2 (2026-11-15): D-1 to D-43. Each entry's own status says whether it is built; the newest entries come after the open questions because they were appended as they were made. Newest at the bottom, open questions last. Never delete an ADR;
> supersede it with a new one. Format: Nygard-style, trimmed.

---

## D-1 — A separate repo, not a branch or fork of Sanctum
- **Status:** Accepted
- **Context:** Sanctum is a small, public, audited backup vault whose identity is that it never shows data
  (D-15 there, a v1 non-goal). Alcove shows data, has users, spaces and an admin.
- **Decision:** a new repo (`alcove`). Sanctum stays as it is.
- **Alternatives:** a long-lived branch (drifts, and public backup-only users would carry viewing code and
  attack surface); a fork (same drift plus copied release machinery).
- **Consequences:** a duplicated release and docs pipeline (reused as templates). Shared code is handled by D-9.

## D-2 — License: AGPL-3.0-or-later
- **Status:** Accepted
- **Context:** a network service others may host. The author's other server project uses AGPL-3.0.
- **Decision:** AGPL-3.0-or-later. Anyone may self-host it; anyone who modifies it and runs it for others
  must offer those users the source.
- **Alternatives:** MIT (permits closed hosted forks); GPL-3.0 (does not cover use as a hosted service).
- **Consequences:** Go dependencies must be compatible (Apache-2.0 and BSD are). Code copied from the
  MIT-licensed Sanctum keeps its notice. Tryst is GPL-3.0 but Alcove implements its format from the published spec.

## D-3 — Trust model: server-side, the admin can read everything
- **Status:** Accepted
- **Context:** a household that trusts its admin. Per-user keys that hide data from the admin were offered and declined.
- **Decision:** the server decrypts and indexes members' data. The admin can see and search all of it,
  including soft-deleted items and paused shares. Accountability comes from the admin log (D-6), not from cryptography.
- **Alternatives:** per-owner keys and browser-side decryption (stronger privacy from the admin, far more
  work, different UI).
- **Consequences:** the threat model states plainly that host root or the admin reads everything. Not zero-knowledge.

## D-4 — Key management: unattended by default, locked mode optional
- **Status:** Accepted (scheme details open, see O-1)
- **Context:** Sanctum's "type a passphrase after every restart" would make every member wait on the admin.
- **Decision:** by default the data key is read from a key file stored **outside** the data directory, so the
  server starts unattended. The app refuses to start if the key file is inside the data directory. A locked mode
  (typed passphrase, Sanctum-style) remains available as an option and is the default for public installs that prefer it.
- **Alternatives:** locked-only (bad availability); key in an env var or compose file (leaks via inspect and backups).
- **Consequences:** protects a pulled or RMA'd disk, not root on the live host. Constraint 4 replaces Sanctum's
  "key never at rest". The key file is a single root of failure and its second copy must be proved (CRITICAL_ASSETS).

## D-5 — Sharing model: private by default, opt-in at several levels (SUPERSEDED by D-21)
- **Status:** Accepted
- **Decision:** nothing is visible to another member until its owner shares it, per space, per person, per
  category and per entry. There is a one-tap **pause** and a **share all**. Partner names are hidden in shared
  views by default. Photo location data is kept; others see it only when the owner enables it per photo.
- **Alternatives:** share-everything-with-opt-out (unsafe default); aggregate-only (cannot share photos).
- **Consequences:** every view is computed from shared rows only, with isolation tests across users and spaces.
  What others already saw cannot be unseen; the UI says so.

## D-6 — The admin sees everything, and every admin action is logged
- **Status:** Accepted
- **Decision:** an append-only, hash-chained admin log records logins and failures, user, space and membership
  changes, uploads, edits, restores, exports, and every admin view or search. It stores which entry and which
  fields, never the content (before and after values live in the revision store). The log is admin-only; an
  optional read-only "activity on my data" view for members is **off by default**.
- **Consequences:** log retention is configurable; the chain is verifiable; an admin who can also alter the
  database could rewrite the chain, so the log deters and documents rather than prevents (see the threat model).

## D-7 — Edits are new revisions; exports are new files
- **Status:** Accepted
- **Context:** editing on a computer is easier than on a phone. Tryst restore inserts rows with
  `INSERT OR REPLACE` by id (per its export spec), so an edited backup should update the same entries on the phone.
- **Decision:** an upload is an immutable revision with a SHA-256. An edit is a change-set over a base
  revision, producing a new revision. Exporting writes a fresh `.tryst`; nothing is ever overwritten. Tables and
  columns Alcove does not edit pass through untouched, so Tryst's schema can move.
- **Alternatives:** live sync (out of scope, maybe later over a private overlay); editing the stored file in place (violates immutability).
- **Consequences:** restore does not propagate deletions to the phone, and phone-side changes to the same row after
  the backup are overwritten by a restore. The UI tracks which upload an edit is based on, warns when a newer upload
  exists, and shows a diff before export. The writer must be proven by an emulator round trip (never a physical phone).

## D-8 — Nothing is lost silently: soft delete with recovery windows
- **Status:** Accepted
- **Decision:** deletes are soft. A user can restore from "recently deleted" for **10 days**; the admin can
  restore for **45 days**. Both are configurable. After the window a purge removes the data for real. Deleted
  users and spaces get the same grace.
- **Consequences:** purge needs a test that data is actually gone. A server-side restore does not restore the phone.

## D-9 — Tryst reader: copy first, extract a shared library later
- **Status:** Accepted
- **Context:** Sanctum's reader lives under `internal/` (not importable) and deliberately only counts rows.
  Alcove needs a full reader and a writer.
- **Decision:** copy the small `trystbk` reader (MIT, same author) into Alcove and extend it. Extract a shared
  public module only once both projects need the same code and its API is stable.
- **Alternatives:** import Sanctum (not possible, `internal/`); extract first (changes Sanctum before we know the shape).
- **Consequences:** two copies for a while. The Tryst backup spec is the shared source of truth.

## D-10 — Multiple independent spaces, owner-approved membership, invite only
- **Status:** Accepted
- **Decision:** a server hosts several independent spaces (one per couple, or any group of partners). A person can belong to
  several. Anyone with an account can request to join a space and the space owner approves. There is no open
  sign-up: accounts are created by the admin or by invite, and first run states the service is for consenting adults.
- **Consequences:** roles are server admin, space owner and member, with last-admin and owner-handoff safeguards.
  Leaving a space removes that person's shared contributions immediately.

## D-11 — LAN only; a private overlay later
- **Status:** Accepted
- **Decision:** no inbound internet, no port forwarding. Tailscale or WireGuard may be added later as a private
  overlay and could carry optional server sync. Cookies gain `Secure` only behind TLS.
- **Consequences:** members must be on the home network (or the overlay) to upload.

## D-12 — Name: Alcove; neutral on a shared dashboard
- **Status:** Accepted
- **Decision:** "Alcove" (a private nook; each space is an alcove). Container and repo names give nothing away.
  The word "Tryst" does not appear in the name, to keep Tryst's listing and brand separate.

## D-13 — Stack: Go, standard library, server-rendered UI
- **Status:** Accepted (storage scheme open, O-1)
- **Decision:** Go (pinned in SETUP.md), stdlib `net/http` and `html/template`, SQLite via the pure-Go
  `modernc.org/sqlite`, `tink-go` and `golang.org/x/crypto` for crypto, distroless non-root image, Docker on Unraid.
  Reuse Sanctum's responsive light/dark/auto UI approach and strict same-origin CSP.
- **Alternatives:** a JS framework (large dependency and audit surface); Python (weaker static-binary story).
- **Consequences:** same toolchain, release script and smoke-test pattern as Sanctum.

---
## D-14 — Profiles come from Tryst; invites are one-time and offline
- **Status:** Accepted
- **Context:** a Tryst backup carries the user's own `profile` row (display name, photo, sex, gender, birth
  date, ethnicity, height, body type, orientation). Typing that again in a new app is tedious, but most of those
  fields are sensitive.
- **Decision:** a member may **prefill** their Alcove profile from the `profile` row of an uploaded backup.
  Display name and photo are offered by default; every other field is imported only if the member picks it.
  Each profile field has its own share setting and defaults to private (constraint 5). A space shows member
  cards built only from what is shared. **Invites** are single-use, expire (default 7 days), are stored as a
  hash, can be revoked and are rate-limited on redemption. The admin invites people to the server; a space
  owner invites people to a space, as a new or an existing account. No e-mail is sent: the link or code is
  handed over in person or through your own channel.
- **Alternatives:** import the whole profile automatically (leaks sensitive fields); open sign-up with approval
  (breaks invite-only); e-mail invites (an outbound dependency, against constraint 3).
- **Consequences:** a profile is a derived, editable copy tagged with the revision it came from; changing it in
  Alcove does not change Tryst. Invite links on a LAN are only as private as the channel they travel on.

## D-15 — Tryst restore semantics (resolves O-4; emulator proof still gates M6)
- **Status:** Accepted for the design. Read from Tryst's `BackupManager.kt` and `EXPORT_FORMAT.md` on 2026-11-12;
  must be confirmed on an emulator before the writer ships.
- **Context:** an edited backup only helps if restoring it on the phone does the right thing.
- **Findings:** Tryst's restore has **two modes**, chosen by a toggle in its restore dialog (the main restore
  starts with it on):
  1. **Wipe first (exact replacement).** Every table in the backup's table list is deleted, then the backup rows
     are inserted, all in one transaction that rolls back on failure. Entries deleted on the server disappear
     from the phone. **Anything on the phone that is newer than the backup is lost.**
  2. **Merge (advanced).** Rows are inserted with `INSERT OR REPLACE` by id. Edited rows overwrite; new rows are
     added; rows missing from the backup stay on the phone, so server-side **deletions do not propagate**. Replacing
     a parent row can cascade-delete its link rows (`encounter_partner` and similar); they come back only if
     the backup carries them, and link rows added on the phone since the backup are lost.
  Either mode inserts raw rows (no migrations replayed), backfills NOT-NULL columns from the live schema's
  defaults, re-points media paths, re-encrypts media under the device key, skips `recent_searches`, and restores
  settings only if the backup carries them.
- **Decision:** Alcove's export flow states which mode to use and why. For an export that contains deletions or
  must be exact, use **wipe first**, and require the user to confirm they exported a fresh backup from the phone
  first (otherwise newer phone entries are lost). For edits only, merge is safe. The diff screen shows what each
  mode would change. Alcove never assumes either mode silently.
- **Consequences:** an export is a guided procedure, not a button. Requirement F15 and F29 cover it. The emulator
  round trip tests both modes, including a deletion and a phone-only entry.

### D-15 addendum: S1 findings, observed on an emulator (2026-11-13)

Tested on an Android emulator (API 35) with Tryst 0.6.5 (container v2, database schema v19), using synthetic backups only.
Method: restore a backup, export a fresh one from the app, read it with Alcove's reader, and compare row by row.

1. **Wipe-first restore is exact.** Restoring the edited backup E onto a phone holding extra data: the edit applied, the new
   entry was added, the deleted entry and its photo were gone, the phone-only entry and the phone-only partner link were gone.
   The app's re-export matched E row for row. Wipe is the safe mode for an Alcove export.
2. **Merge restore is NOT safe.** The edit applied and the new entry was added, but (a) the entry deleted on the server stayed,
   as predicted, and (b) **every partner link (`encounter_partner`) not present in the backup was deleted**, including the
   links of entries that stayed. The cause: partners are restored first with `INSERT OR REPLACE`, which deletes each partner
   row and cascades to all link rows; only links carried by the backup come back. So a phone-only entry survived but lost
   its partners, and so did the entry the server had deleted. Merge silently damages data.
3. **ZIP entries must be DEFLATED.** Tryst reads the archive with a streaming `ZipInputStream`, which cannot read a STORED
   entry whose size comes after the data. A first sample restored every table but only 1 of 6 photos, with no error shown.
   Alcove's writer uses DEFLATE for every entry and puts `data.json` first (regression test added).
4. **Tryst's export is a faithful, lossless copy.** Restoring then exporting reproduced every row of every table. The only
   differences were cosmetic or device-specific: JSON strings escape `/` as `\/`, `media.encFilePath` is rewritten to the
   device path, and the media `mimeType` is re-escaped. Pass-through of unknown columns is therefore safe.
5. **Interop with a genuine Tryst container is proven.** Alcove's reader read the files the app itself exported (container v2,
   schema v19). The data was synthetic, so a run against a real private backup (the interop test) remains the user's call.
6. **The restore dialog shows counts** ("40 trysts, 3 partners and 6 photos"), which the export guide can quote as what to expect.

**Decision amendments:** the export guide **always recommends wipe first** and treats merge as unsafe (it warns about the partner-link
loss). The "fresh phone backup first" confirmation matters more, because a wipe loses anything newer on the phone. D-20 point 3
is superseded by this. Remaining risk: the app's restore behaviour can change in a later Tryst release, so the emulator round trip
is a recurring check, not a one-off.

## D-16 — Storage: a sealed in-memory database, per-user data keys, one master key (resolves O-1)
- **Status:** Accepted (implemented and measured in M2a, 2026-11-13)
- **Context:** the server must read, query and edit members' data without writing plaintext to disk, and start
  unattended (D-4). SQLCipher needs cgo (rejected in Sanctum D-5).
- **Decision:**
  1. **Structured data** (accounts, spaces, memberships, shares, revisions' change-sets, the row cache, the
     search index) lives in one SQLite database on **tmpfs**, sealed to the data directory as an AES-256-GCM blob
     (Sanctum's proven pattern, D-5 and D01 there): sealed on a short timer when changed and after every
     important write, with one previous generation kept.
  2. **Blobs** (original uploads and photos) are encrypted files in the data directory. Uploads are kept exactly as
     Tryst wrote them (already encrypted under the member's backup password). Photos are re-encrypted under the
     owner's **data key**.
  3. **Keys.** A 256-bit **master key** is read from a key file outside the data directory (D-4). Each user has a
     **data key** wrapped by the master key. Deleting a user's wrapped key and blobs is a verifiable purge
     (crypto-shred) and limits the blast radius of a bug.
  4. **Locked mode.** The key file itself may be wrapped by an Argon2id-derived key from a typed passphrase. If
     it is, the server starts locked until the passphrase is entered (Sanctum's behaviour). One mechanism, two modes.
  5. **Hard rule:** decrypted data exists only in memory or tmpfs, never on the data volume
     (`scripts/guards.sh no-plaintext`, armed when storage lands).
- **Alternatives:** SQLCipher (cgo); plaintext SQLite under filesystem encryption (depends on the host); encrypting
  each user's tables as separate blobs with no SQL (loses queries, sharing views and search).
- **Consequences:** the database is limited by RAM (megabytes for a household; photos are not in it). A crash can
  lose the last few seconds of structured changes, so uploads and edits are sealed immediately. Per-user data keys
  do not hide anything from the admin (D-3); they help purge and isolation.

## D-17 — Admin search: SQLite FTS5 inside the sealed database (resolves O-7)
- **Status:** Accepted (FTS5 verified in M2a)
- **Decision:** search is an FTS5 index in the same tmpfs database, sealed with it, so no plaintext index ever sits on
  disk. It covers notes, partner names, labels and profile text, for all users and spaces, including soft-deleted
  and paused content, for the admin. Members search only what they may see, through the sharing check.
  The admin log records that a search happened, its scope and result count, and the term's length, **not the term**
  (constraint 8). Whether to record the term is O-12.
- **Alternatives:** an external search engine (an extra service and plaintext index); scanning rows per query (slow, but acceptable at household scale as a fallback).
- **Consequences:** the index is rebuildable from the revisions.


## D-18 — Compare metrics are a versioned registry that grows with Tryst's Insights (resolves O-2)
- **Status:** Accepted
- **Context:** Tryst's Insights engine keeps changing. Catalog names (acts, kinks, positions, toys) are per-user custom
  labels, so they cannot be compared across members by name.
- **Decision:** each metric is a small named, versioned definition in a **registry**, built only on neutral derived
  fields (`startAt`, `durationMin`, `satisfactionRating`, counts of set members) so it survives schema changes. The v1
  set: (1) a calendar overlay with a day marker per member and a "both active" highlight; (2) per member and period:
  entries, active days, average rating, average duration; (3) longest weekly streak, busiest weekday and month; (4)
  trend lines side by side, month by month and year over year; (5) category diversity (distinct counts), with named
  top categories only where both members share them. New metrics are added to the registry as Tryst Insights grows; a
  metric records which Tryst schema versions it supports and is hidden, not wrong, on one it does not.
- **Consequences:** a periodic review step in the roadmap: when Tryst ships a new Insights card, decide whether it joins
  the registry. Metrics are computed only from shared rows (D-5).

## D-19 — Editing scope for v1 (resolves O-3)
- **Status:** Accepted
- **Decision:** members can **edit** an entry's `startAt`, `durationMin`, `note`, `satisfactionRating`, `mood`,
  `initiator`, `protectionUsed`, `orgasmCountSelf`, and the set fields (`practicesPerformed`, `practicesReceived`,
  `positions`, `kinks`, `contexts`, `occasions`, `toys`), choosing only from the member's **existing** catalog entries;
  rename a partner's `displayName` and edit simple partner fields; **add a brand-new entry** (a new UUID, inserted on
  restore); and delete and restore whole entries. Every edit sets `updatedAt`. **Read-only in v1:** the two per-orgasm
  maps, the catalog tables, attaching or detaching media, and settings.
- **Consequences:** a new entry carries a new id, so merge restore adds it and wipe restore keeps it. Creating new
  catalog entries is a later step because it changes more tables.

## D-20 — Small decisions: time zone, search logging, export default (resolves O-11, O-12, O-13)
- **Status:** Accepted
- **Decisions:** (1) Day boundaries use a **per-member time zone** that defaults to the server's and is shown in the UI.
  (2) **Nothing sensitive in the logs:** the admin log records that a search happened, its scope, result count and the
  term's length, never the term (constraint 8). (3) *(Superseded by the D-15 addendum: the export guide always recommends wipe first, never merge.)*

---
## D-21 — A space is collaborative: members see each other's entries (supersedes D-5, withdraws O-10)
- **Status:** Accepted
- **Context:** the per-category and per-entry sharing matrix and the optional partner-linking feature were judged too
  complicated. A space exists to see and compare everyone's entries together, including solo sessions, sessions with
  people outside the space, and sessions with several partners. Matching one event across two members' data would
  need linking; showing everyone's own entries side by side does not.
- **Decision:** inside a space, **every member sees every other member's entries by default**. Nothing is visible to
  non-members or in other spaces. Joining a space is the act of consent, so the join screen states exactly what will
  be shared. There is **no entry matching and no partner linking**: each entry belongs to the member who logged it, and
  the views (calendar overlay, compare metrics) simply show all members' entries. The small safeguards that stay:
  1. **Per-space settings** on a membership: include notes, include photos, show partner names, and a **pause**.
     Defaults: notes and photos on, partner names **hidden** (people who are not members never agreed to appear),
     photo location shown only when its owner enables it.
  2. **Mark an entry private** with one tap, so a single entry can stay out of the space.
  3. **Leaving** removes a member's contributions from the space at once.
  4. **Join approval is a per-space setting.** v1 default and only implemented mode: the **owner approves alone**.
     A stricter `all_members` mode (every current member must agree) is modelled in the data model and built later,
     for more social spaces where a newcomer sees everyone's existing entries.
- **Alternatives:** the per-category and per-entry matrix (D-5), partner linking and entry matching (O-10), a
  per-pair sharing model.
- **Consequences:** the sharing code shrinks to a visibility check on one space membership plus a per-entry flag.
  What a member has already shown to the space cannot be unseen, and the join and leave screens say so. Isolation
  tests now cover non-members and other spaces rather than each category. Partner linking can return later as a
  separate feature if wanted.

## D-22 — Mobile-first web UI and the phone workflow
- **Status:** Accepted
- **Context:** Tryst lives on a phone. The natural flow is: export a backup in Tryst, open Alcove in the phone's browser
  on the home network, upload it; later download the edited backup to the phone and restore it from Downloads.
- **Decision:** the web UI is **mobile-first**: designed at phone width first, large touch targets, no hover-only
  controls, light, dark and automatic themes, the same plain HTML and CSS approach as Sanctum with at most one
  same-origin script (upload progress). The upload page works with the Android file picker, shows a progress bar, and
  hints to keep the screen on during a large upload. Exports download with a clear filename
  (`alcove-<name>-<date>.tryst`), and the export guide explains how to restore from Downloads. The server's LAN address
  is shown prominently to the admin, with an optional QR code (F31).
- **Alternatives:** a native app or a PWA (more surface, store distribution); desktop-first pages (the phone is the main device here).
- **Consequences:** real-phone checks are part of the first live deploy and the audit. Plain HTTP on a LAN is fine for
  file upload and download; features that need a secure context (service workers, clipboard APIs) are not used.
  A phone browser may sleep mid-upload, so resumable uploads are tracked as a later item (O-14).

## D-23 — Transport security: secured wifi by default, admin-enabled options (resolves O-15)
- **Status:** Accepted
- **Context:** Alcove serves decrypted content over a home network, and a phone browser is the main client (D-22). Plain
  HTTP exposes logins, cookies and pages to anyone who can sniff that network (R14). Uploaded and exported files stay
  encrypted under the member's backup password either way.
- **Decision:** the **default is a secured, password-protected wifi with plain HTTP**. The admin can switch on these
  options in the admin console (Settings, step-up re-login required):
  1. **Reverse-proxy mode.** Trust the forwarded protocol and client address only from configured proxy addresses; mark
     cookies `Secure` and emit HSTS when the connection is encrypted. Runbook recipes: Caddy or Nginx Proxy Manager with a
     DNS-challenge certificate, and `tailscale serve`.
  2. **Allowed networks.** Refuse requests from outside a list of networks. The default is the private LAN ranges, so a
     request from a public address is rejected (this also enforces constraint 2 inside the app).
  3. **Insecure-connection banner** for the admin when the current connection is not encrypted, with a link to the fix.
  4. **Require-TLS mode** (off by default): refuse logins over plain HTTP.
  An environment value, when set, wins over the console and shows as "set by environment", which is also the break-glass
  path. Changing allowed networks asks for confirmation from the new address within 60 seconds and **reverts itself** if
  not confirmed, so the admin cannot lock themselves out.
- **Alternatives:** a built-in TLS mode with a local certificate authority (rejected: phones must install and trust the CA,
  which is awkward on Android, and the CA key becomes a new asset to protect); mandatory TLS (breaks the simple LAN setup).
- **Consequences:** the first live deploy can be encrypted from day one through a proxy or Tailscale, but does not have to be.
  CSRF tokens, `SameSite=Strict` cookies and short sessions apply in every mode. Built-in TLS is not planned.

## D-24 — Dependency and licence gate is an allowlist, not a scanner
- **Status:** Accepted
- **Context:** checklist 00 wants a licence and dependency check from M0. The usual scanner (`go-licenses`) is an extra
  tool that classifies the project's own AGPL licence as restricted and has trouble with the standard library, so it
  would fail for reasons unrelated to dependencies and would have to be installed in CI.
- **Decision:** `scripts/dependencies.allow` lists every approved third-party module with its SPDX licence. `scripts/guards.sh
  licenses` (pure bash and `go list`) fails if a module in the build is not listed, if `go list` itself fails, or if a
  listed licence is outside the allowed set. Adding a dependency is a deliberate, reviewed line.
- **Alternatives:** `go-licenses` (see above); no gate (a dependency could arrive unreviewed).
- **Consequences:** a small manual step per new dependency, which is also where its audit cost gets weighed. The gate
  was forced to fail on a bad licence and on a failing `go list` before being trusted.

## D-25 — The Tryst reader is lossless, lives with the writer, and bounds everything
- **Status:** Accepted
- **Context:** Alcove must read every row (Sanctum only counts), edit some fields and write a backup Tryst accepts. A reader
  that interprets rows into typed structs would drop any column it does not know and break on the next Tryst schema.
- **Decision:** (1) A `Row` keeps its column order and every value as **raw JSON**, a `Backup` keeps table order, unknown
  top-level `data.json` keys and unknown small ZIP entries, so re-serialising reproduces `data.json` byte for byte (tested).
  (2) `Seal` (the container writer) lives in the same package as the reader and uses the same primitives and parameters, and
  refuses any header Tryst would refuse. (3) Every size has an enforced bound (body, `data.json`, declared total size, entry
  count, row count, per-entry sizes), held in one `limits` value tests can lower. (4) A schema above `LatestKnownSchema` is
  held and readable but reported as `SchemaNewer` so editing and export stay off (F10). (5) Media bytes are never read at
  upload time, only on demand and bounded; the decrypted body is zeroed by `Close`.
- **Alternatives:** typed row structs (lossy, breaks on schema change); a separate writer package (the two could drift).
- **Consequences:** callers handle raw JSON through small accessors until an editor needs typed values. Holding the body in
  memory costs up to 512 MiB for a hostile file; the container memory limit is the backstop. The writer is only proven once
  an emulator restores its output (S1 and M6).

## D-26 — The master key file: text format, deliberate creation, fingerprint (implements D-4 and D-16)
- **Status:** Accepted
- **Context:** the master key seals the database and wraps every data key. Losing every copy loses everything, and Sanctum
  already lost a passphrase to a "second copy" that did not exist.
- **Decision:**
  1. **The server never creates a key.** `alcove keygen` does, deliberately; with no key file the server refuses to start and
     prints that command. `keygen -out` refuses to overwrite an existing file.
  2. **The file is one line of ASCII** (`alcove-key-1:<base64url>`), so it can be copied into a password manager and pasted back.
     An optional wrapped form (`alcove-key-1w:argon2id:t=,m=,p=:salt:nonce:ciphertext`) protects it with a passphrase using
     Argon2id and AES-256-GCM with the parameters bound in as associated data; parameters below the production floor
     (t 3, m 64 MiB, p 1) are refused when loading, as in Sanctum.
  3. **A plain key starts the server unattended. A wrapped key starts it locked.** The unlock page arrives with the web UI (M2b);
     M2a ships the loading, the `Unlock` call and the tests.
  4. **`alcove keycheck -file P` opens a key file and prints a fingerprint** (a short hash of the key, not the key). Two files with
     the same fingerprint hold the same key, which is how a second copy is proved by opening it.
  5. **The key must be outside the data directory,** checked before the key is even read, with symlinks resolved and a
     case-insensitive comparison on Windows and macOS.
  6. **No environment variable carries a key or a passphrase** (a test pins this).
- **Alternatives:** auto-create on first start (a silently created, never-backed-up key is the one unrecoverable mistake); a binary key
  file (cannot be copied to a password manager); a passphrase in an environment variable (leaks via `docker inspect`).
- **Consequences:** first setup has one extra step (run `keygen`, save a copy, `keycheck` it). The crypto-shred of a purged user
  (destroying their wrapped data key) makes their blobs unreadable on the live instance; older sealed generations and instance backups
  still contain that key until they age out, so a purge is complete only after the backups that predate it are gone.


## D-27 — Accounts, sessions and the web layer (implements D-6, D-22, D-23 in M2b)
- **Status:** Accepted
- **Context:** M2b adds the first accounts, the admin log and the web UI. Each piece has a security consequence that deserves a
  stated reason, because a household server that anyone on the wifi can reach is the threat (T2).
- **Decision:**
  1. **First run needs a one-time setup code.** With no accounts, every page redirects to `/setup`, which asks for a code printed
     once to the container log (Warn level, `setup_code`). Without it a device on the LAN could claim the server before its owner
     does. `/setup` is the only route that creates an account; it works once, then returns 404 (constraint 11, tested by hitting
     every POST route unauthenticated before and after first run).
  2. **Passwords are Argon2id** (64 MiB, three passes, two lanes), with the cost stored in each hash so it can be raised later and a
     weaker hash is upgraded at the next sign-in. At most two hashes run at once, so a burst cannot exhaust the container. An
     unknown or disabled account is checked against a dummy hash, so every failure takes the same time and says the same words.
     Passwords are 10 to 256 bytes; the policy is a floor, not a strength meter.
  3. **No per-account lockout.** Lockout lets anyone on the wifi lock the admin out. A per-address token bucket (ten tries, then
     one every 20 seconds) plus the hash cost slows guessing instead.
  4. **Sessions live in memory,** 32 random bytes, 30 minutes idle and 12 hours absolute, `HttpOnly`, `SameSite=Strict`, `Secure`
     when the connection is encrypted. A restart signs everyone out (R18). Each carries its own CSRF token.
  5. **Step-up** (re-enter the password, valid five minutes) guards the transport settings. Every admin view and action, including
     the log viewer and the settings page, appends an admin-log entry in the same transaction as the action.
  6. **CSRF:** every POST needs a token. Signed in, it is the session's token. Before sign-in it is an HMAC of a random pre-session
     cookie (signed double submit). A request with `Sec-Fetch-Site: cross-site` is refused outright.
  7. **The admin log is chained and anchored.** Each entry's hash covers the previous hash; a head anchor in `meta` is updated in
     the same transaction, so cutting entries off the end is caught; the database refuses `UPDATE` and `DELETE` on the table by
     trigger (the chain, not the trigger, is the real defence, and tests drop the trigger to prove it). Free-text fields accept only
     identifier characters, so content cannot be logged by accident (constraint 8).
  8. **The network gate runs before everything else** and a request from outside the allowed networks gets a bare 403. A direct
     loopback peer is let through so the container healthcheck works whatever the list says, but a same-host proxy forwarding a
     public client is still refused. Turning require-TLS on is accepted only from an already-encrypted connection, and an
     allowed-networks list that excludes the admin's own address is refused, because lockout is the failure to design out.
  9. **Static files revalidate with an ETag** (`no-cache`), so an upgrade shows at once. A one-hour cache was tried first and hid a
     stylesheet fix in testing.
- **Alternatives:** a setup code via environment variable (leaks in `docker inspect`); sessions in the database (adds a stolen-cookie
  store to disk for no gain at this scale); account lockout (a denial of service by any device on the wifi); inline styles with a
  nonce (a same-origin stylesheet lets the CSP stay `style-src 'self'` with no exceptions).
- **Consequences:** a typo in `ALCOVE_ALLOWED_NETWORKS` stops the start rather than opening the door. Environment values win over the
  console and are the break-glass path. TOTP, invites and account administration come in later milestones; until then the only
  account is the administrator's.

## D-28 — Instance backup and the restore drill (implements F23; M3, piece A)
- **Status:** Accepted
- **Context:** Once edits live here Alcove is a system of record (CLAUDE.md, lessons from Sanctum). The data directory is already
  ciphertext under the master key, and the one unrecoverable mistake is a key with no second copy or a backup nobody ever restored.
- **Decision:**
  1. **A backup is one archive of ciphertext files:** `manifest.json`, the sealed database and every blob, in a plain `tar` (no
     compression: ciphertext does not compress), plus a last entry `BACKUP.json` holding every file's size and SHA-256, and a sidecar
     `<name>.sha256` of the whole archive. **No second encryption layer.** The files are already sealed under the master key, and a
     second passphrase would be a second secret to lose. Consequence, said plainly: **a backup is useless without the master key**,
     which is why the key's second copy is proved before the first real upload.
  2. **Consistency by ordering.** The server seals the database, then copies the manifest and the sealed file while holding the seal
     lock (no seal can replace them mid-copy), and only afterwards copies the blobs. The rule that makes this safe, enforced where
     uploads are written (M3 piece B): **a blob is written before the row that refers to it.** Any blob the copied database mentions
     is therefore already on disk; an extra blob is a harmless orphan.
  3. **Where it goes:** `ALCOVE_BACKUP_DIR` (default `/backups`), a mounted volume that must not be inside the data directory and is
     scanned by the `no-plaintext` guard like the data volume. Unset or unmounted means the feature says so and does nothing. Files
     are written to `.part`, flushed, then renamed, mode 0600.
  4. **Triggered by a person, not a timer** (the owner prefers manual backups with a nudge, as with the Immich backups): an admin
     page with step-up, "Back up now", "Verify", "Run restore drill". Every action is in the admin log. A reminder when the last
     verified backup is older than 30 days comes with notifications (F24).
  5. **Verify** re-reads the archive and checks every hash and the sidecar. It needs no key. **Restore drill** extracts into a scratch
     directory, opens the store with a key, checks the admin-log chain, checks that every stored upload still matches its SHA-256,
     counts rows, and reports counts only. `alcove drill -backup F -key K` runs it from the command line with any key file, so a
     **key copy is proved by restoring a real backup with it**, which is stronger than comparing fingerprints.
  6. **Restore** is `alcove restore -backup F -to DIR`: it verifies, then extracts into an empty directory (refusing a non-empty one,
     a path that leaves the directory, or a duplicate name), needing no key. Putting the result in place of the data directory is a
     runbook step with the container stopped.
  7. **Retention:** keep the newest N verified backups (default 7), never delete the newest, prune only after a new backup verified.
- **Alternatives:** encrypting each backup with its own passphrase (a second secret; rejected above); a live copy of the data
  directory with no ordering rule (can hold a database that refers to a blob not yet copied); a built-in timer (the owner prefers a
  person in the loop, and a timer that silently fails is worse than a nudge).
- **Consequences:** backup size is the data size (photos dominate). Off-machine copies are safe to keep anywhere because they are
  ciphertext, but only the key file opens them, so the key file's second copy is a mandatory first step (CRITICAL_ASSETS).

## D-29 — Upload, revisions, stored backup passwords and the row cache (implements F7 to F10, F30; M3 piece B)
- **Status:** Accepted
- **Context:** this is where real Tryst data first enters. It has to work from a phone browser over wifi, keep every upload
  untouched, tell a wrong password from a damaged file, never leave half a record, and leave a read model that can be thrown away
  and rebuilt.
- **Decision:**
  1. **One streamed request.** The upload is a `multipart/form-data` POST read as a stream: the CSRF token and the options come
     first, the file last, and the token is checked before a byte of the file is accepted. The file goes straight into the blob
     store (verbatim, immutable, SHA-256 on the way) and is never held whole in memory before it is judged. The read deadline for
     this one route is raised so a slow wifi does not cut it off; every other route keeps the short one. The cap is the Tryst
     reader's own bound (512 MiB), and an oversize upload gets a fast, clear refusal.
  2. **Judge before recording.** In order: write the blob; refuse a duplicate (same owner and SHA-256) and delete the new copy;
     trial-decrypt with the password and read every table with the bounded reader; only then record. A wrong password, a file that
     is not a Tryst backup, a damaged one and a hostile one are different, plainly worded refusals, and each deletes the blob and
     records nothing (N7, N9). **The blob is always written before the row that refers to it** (the D-28 rule), and the rows
     (upload, revision, cache, search, password) commit in one transaction.
  3. **Revisions.** `upload` holds what was uploaded; `revision` is the history (kind `upload` or `edit`, a parent, who created
     it, a `deleted_at` for the soft delete of piece C). The head is the newest active revision. All revisions are kept; the admin
     can purge on purpose, logged. An older file uploaded later becomes the head: the head is the latest *upload*, not the latest
     *phone state*, and the page shows the dates so a person can tell.
  4. **Row cache and search index** are derived: every row of the head revision as its raw JSON (`cache_row`, keyed by owner, table
     and the row's `id`, or its position for link tables), the table order (`cache_head`), and one FTS row per text-bearing row.
     `Rebuild` re-reads the head revision's blob and refills both, so a damaged cache is never a data loss. A new upload replaces
     the owner's cache in the same transaction as the revision.
  5. **The backup password is stored, sealed** under the owner's data key (AES-256-GCM, the owner id as associated data), because
     rebuild, edit and export all need to open the owner's own file. A person can untick "remember this password"; then those
     features ask again. The password is never logged and never shown back.
  6. **A newer Tryst schema** (above `LatestKnownSchema`) is stored and cached read-only and flagged, so editing and export stay off
     (F10). A schema below 1 is refused.
  7. **Photos are not extracted yet.** Their bytes stay inside the stored upload; extraction under the owner's data key comes with
     photo viewing (M4). The count and names are recorded.
  8. **A progress bar needs one small same-origin script** (N11 allows exactly one), so the CSP gains `script-src 'self'` and
     nothing else. Without JavaScript the same form still uploads, just with no bar. The script never sees the password after
     submit and holds no state.
  9. **File names are display text only.** They are trimmed and bounded and never used to build a path or a header.
- **Alternatives:** buffering the upload in memory first (a 500 MiB request times the number of phones); storing before checking and
  marking "unverified" (leaves records that cannot be read); asking for the password every time (rebuild and export then need a
  person); parsing the whole backup into typed tables (lossy, D-25).
- **Consequences:** the cache is a second copy of the data in the sealed database (RAM on tmpfs), about the size of `data.json`.
  Unverified or failed uploads leave nothing but a log line with counts. Re-uploading a changed phone backup creates a new revision
  each time, which is the point.

## D-30 — Account lifecycle, soft delete, purge and the break-glass reset (implements F2, F3, F16, F21, N13; M3 piece C)
- **Status:** Accepted
- **Context:** people join, leave and forget passwords; data has to be recoverable for a while and then really gone; and a lost
  admin password must not mean losing everything (the M2b gap).
- **Decision:**
  1. **Account states without a table rebuild.** `user.status` stays `active` or `disabled`; a new `deleted_at` column makes a disabled
     account **deleted** (pending purge), and a new `must_change` column forces a password change at the next sign-in. Disable is
     instant and reversible and ends the person's sessions. **Delete requires disabled first** and starts the grace period; the admin
     can restore until the admin window ends. The last active admin can be neither disabled nor deleted, and nobody can do either to
     themselves, so the server can never be left with no administrator.
  2. **New accounts get a temporary password.** The admin creates a person with a role and the server makes a random temporary
     password, shown **once** on that page (never stored in the clear, never logged), and the person must change it at first sign-in
     before anything else works. Invite links (F27) come with spaces in piece D; this is the simpler path until then. An admin reset
     does the same.
  3. **Soft delete is a timestamp, in one place per thing.** An upload's revision gets `deleted_at`; so does an account. Deleting or
     restoring a revision can change which one is the head, so the cache and search index are rebuilt from the new head (from the
     remembered password) or, if that is not possible, cleared and flagged, never left showing a deleted revision's rows.
  4. **Two windows, both configurable:** the person can restore their own deleted upload for **10 days** (`ALCOVE_USER_RECOVERY_DAYS`),
     the admin can restore anything for **45** (`ALCOVE_ADMIN_RECOVERY_DAYS`, never less than the person's). Between the two a deleted
     item shows only to the admin, in an admin "Recently deleted" page (viewing it is logged).
  5. **Purge is real and runs by itself.** Hourly, and once at start, `internal/retention` deletes everything past the admin window:
     for a revision its blob and rows, and its search rows; for an account everything it owns (blobs, uploads, revisions, cache,
     search, remembered password, **its data key**, its sessions) and then the account row. Counts are written to the admin log as the
     system. A test proves the files and rows are gone (N13). Older instance backups still hold the data until they age out (R15, R19).
  6. **Download my data (F3).** A person can download a zip of every upload they have, exactly as Tryst wrote them (still encrypted
     with their own backup password), plus a short read-me. It asks for the password again (F21) because it is the most sensitive
     thing the site hands out. An admin cannot download another person's data through this route.
  7. **Step-up is available to everyone,** not only the admin: the same five-minute window guards a delete, a download and the admin
     account actions.
  8. **Break-glass reset:** `alcove reset-password -user NAME` opens the store directly with the key file, sets a new password read
     from standard input (or generates one), forces a change at next sign-in, ends that person's sessions, and writes an admin-log
     entry as the system. Because two processes must never write the same sealed database, **the data directory is locked**
     (`flock` on `.lock`, held until close); a second process, including this command while the server runs, gets a clear refusal and
     changes nothing. The runbook says to stop the container first.
- **Alternatives:** an `ALCOVE_RESET_ADMIN` environment variable (a secret in `docker inspect`, and easy to leave on); deleting
  immediately with no grace (loses the "nothing lost silently" promise); hard-deleting accounts and letting data dangle; a `deleted`
  status value (needs a table rebuild, against the additive-migration rule).
- **Consequences:** a person who deletes their only upload and had asked the server not to remember the password has no data and no
  cache until they upload again, which is the honest result. The admin can see that deleted things exist and restore them, and every
  such look is in the log.

## D-31 — Spaces, memberships, invites and join requests (implements F4 to F6, F27, F28 in part; M3 piece D)
- **Status:** Accepted
- **Context:** a space is the one place data is shared (D-21), so the rule "nothing is visible outside it" has to be one small piece
  of code that every later view goes through, and the ways in (an invite, a code, the admin) are the new attack surface.
- **Decision:**
  1. **Who:** any signed-in person can create a space and becomes its **owner**. A person can belong to many. The administrator
     can see and manage every space, and every look is logged.
  2. **One visibility gate.** `internal/spaces` answers "is this person an **active** member of this space, and as what?" and
     nothing else may answer it. Every space page and, from M4, every shared view asks it, and a non-member gets a plain **404**, so
     the existence of a space is never confirmed to an outsider. Members who are only `requested`, `left` or `removed`, members of a
     space that is deleted, and members whose account is disabled or deleted all fail it.
  3. **Three ways in, all consented.** (a) A **space invite**: a one-time code the owner makes (7 days, revocable, stored only as a
     hash) that lets a new person create an account or an existing person join. (b) A **space code**: a revocable shared code
     (stored hashed, shown once, regenerated to rotate) that lets a signed-in person *ask* to join; the owner approves or denies.
     (c) An **admin server invite**: a one-time code that creates an account with no space. Every join screen says exactly what will
     be shared and needs a ticked box; the version of that text is recorded on the membership. From M4 a member's entries are shown
     only if they have agreed to the **current** version, so changing the text re-asks everyone.
  4. **Redeeming is the only unauthenticated way to make an account** (constraint 11), so it is held tight: the code is 80 bits and
     only its SHA-256 is stored; a wrong, expired, used or revoked code all look the same and create nothing; redeeming is one
     transaction that checks and spends the code and creates the account together, so two people cannot spend one invite; attempts
     are rate-limited per address; and a test posts to every route with every kind of bad code and counts accounts.
  5. **Leaving, removal, handoff.** A member can leave at any time; the owner can remove a member; either ends that person's
     visibility at once (their entries will leave the space with them from M4). The last owner cannot leave: they transfer ownership
     to another active member, or delete the space. A removed or departed person must be invited or ask again.
  6. **Per-membership settings** (include notes, include photos, show partner names, pause) exist from now with the D-21 defaults and
     can be changed on the space page; nothing reads them until M4.
  7. **Deleting a space is soft** (owner restores for the user window, admin for the admin window) and purges with everything else.
     Purging an **account** removes its memberships and invites; spaces it solely owned pass to the longest-standing active member, or
     are removed if nobody is left.
  8. **No browsing.** There is no list of spaces except your own memberships (and the admin's). Space names are not unique, so a name
     cannot be probed.
- **Alternatives:** a public list of spaces to request to join (shows other groups' names, against constraint 5); one reusable invite
  link (cannot be revoked per person); the admin as the only space creator (every new space needs the admin).
- **Consequences:** a leaked space code lets a stranger *ask* but not enter, since the owner approves; a leaked invite lets one person
  in until it is spent, expires or is revoked, which the owner sees. Invite links on a LAN are only as private as the channel they
  travel on (D-14).

## Open questions
> Decisions deferred on purpose. Each is a future ADR. Do not let these rot silently.

- **O-1 — Storage scheme.** *Implemented in M2a (D-16, D-26) and measured; D-16 stands.*
- **O-2 — Compare metrics.** *Resolved in D-18.*
- **O-3 — Editable fields.** *Resolved in D-19.*
- **O-4 — Restore semantics.** *Resolved in D-15 and proven on an emulator in D-43 (wipe first exact, merge unsafe).*
- **O-5 — TOTP scope.** Admins only, or any user; recovery-code handling.
- **O-6 — Location features.** *Resolved in D-39: coordinates as text, shown only when the owner allows it, no map.*
- **O-7 — Admin search.** *FTS5 inside the sealed database works (M2a); term logging resolved in D-20. The admin search page itself is M8, not built.*
- **O-8 — Member activity view.** Whether to ship the optional "activity on my data" view and its default.
- **O-9 — Shared library extraction.** When and how to extract `trystbk` for both projects.
- **O-10 — Linking members to Tryst partners.** *Withdrawn in D-21 (the collaborative model needs no matching). May return later as its own feature.*
- **O-11 — Time zone.** *Resolved in D-20.*
- **O-12 — Admin search terms.** *Resolved in D-20: not recorded.*
- **O-13 — Restore default.** *Resolved in D-20.*
- **O-14 — Resumable uploads.** A phone browser can sleep or drop wifi during a large upload. First version: progress bar
  and a keep-the-screen-on hint. Chunked, resumable upload is a later step if it proves a problem.
- **O-15 — Optional TLS on the LAN.** *Resolved in D-23: secured wifi by default, with admin-enabled proxy mode, allowed networks, a banner and require-TLS mode. A built-in local CA is not planned.*

## D-32 — What members may see of each other: one read gate, the private flag, consent versions (implements F11; M4 piece A)
- **Status:** Accepted
- **Context:** D-21 says every member sees every other member's entries inside a space. Until now nothing read another
  member's data; M4 adds the first shared views (a list, then the calendar and compare views), so the rule has to live in
  exactly one place, like `spaces.Role` does for membership.
- **Decision:**
  1. **One read gate, `share.Entries`.** Every view of another person's entries goes through it. It checks the viewer with
     `spaces.Role` first, then shows only the entries of members who are **active**, whose account is active and not deleted,
     who have agreed to the **current** `ConsentVersion`, and who are **not paused**. The viewer's own entries obey the same
     rules, so a space always shows a member exactly what everyone else sees of them.
  2. **Per-entry private flag** (`entry_flag`, schema v6), keyed by owner and entry id so it survives new revisions. A private
     entry never appears in any space. New entries are shared by default (joining was the consent). The owner sets the flag
     from "My entries".
  3. **Include notes** removes the note text at the gate. Photos and partner names are not part of any view yet; their settings
     keep their meaning and are honoured when those views arrive.
  4. **Consent versions are enforced.** A member who agreed to an older wording is treated like a paused member until they
     agree again on a re-agree screen. The wording changes now (the old text said nothing is shared yet), so `ConsentVersion`
     becomes 2.
  5. **Leaving, removal, disabling, deleting and pausing take effect on the next request**, because the gate reads the live
     membership every time. Nothing is copied into a space.
  6. **The data comes from the owner's head revision cache** (`cache_row`), so an owner who deletes their latest upload also
     removes it from every space (the cache is rebuilt from the new head or cleared).
  7. A purged account's flags are purged with it.
- **Alternatives:** per-view checks (easy to forget one); copying shared rows into a space table (a second copy to purge and
  to keep in step); ignoring consent versions until the wording first changes (a rule that would never have been tested).
- **Consequences:** M4 pieces C and D read only through `share`. A test removes the gate's conditions one at a time to prove
  each is load-bearing.

## D-33 — Profiles: a derived, editable card with per-field sharing (implements F26, F28; M4 piece B)
- **Status:** Accepted
- **Context:** D-14 says a member may prefill a profile from the `profile` row of their backup, that every field except the name
  defaults to private, and that a space shows member cards built only from what each has chosen to share.
- **Decision:**
  1. **A profile is rows of `profile(owner, field, value, shared)`,** one per field from a fixed list in code: about, sex, gender,
     birth date, ethnicity, height, body type, orientation, location. A field with no row is empty. `shared` defaults to 0 (private).
  2. **The name is the account's display name** (the "name shown to others"). A person can change their own on the profile page and
     prefill can offer the backup's name, so there is one name everywhere.
  3. **Prefill is a choice, never automatic.** "Fill from my latest backup" reads the `profile` row already in the owner's row cache
     (no new decrypt), lists what it found, and imports only the fields the person ticks. The name is ticked by default; every other
     field is not. Importing never turns sharing on.
  4. **Birth date is shared as an age only** ("Age 34"). The date itself is stored for the owner, must be a real date, and must be an
     adult's (18 to 120 years); a younger or future date is refused (constraint 11).
  5. **Cards go through the same gate as entries.** `share.Cards` asks `spaces.Role`, requires the viewer's current consent, and shows a
     member's shared fields only while that member is active, has agreed to the current wording and is not paused. A member who is
     paused or on older wording still appears in the member list by name, with a card that says nothing is shown right now.
  6. **No profile photo yet.** An avatar needs an image-resizing step and belongs with photo sharing (D-21 photo rules); until then a
     card is text.
  7. Values are plain text, at most 120 characters (300 for "about"), rendered escaped. A purged account's profile rows are purged.
- **Alternatives:** import the whole profile automatically (leaks sensitive fields, D-14); share the exact birth date; a separate
  display name for cards (two names for one person).
- **Consequences:** the admin sees profile data like everything else, through admin views that are logged; no admin profile page is
  added in this piece. Changing a profile does not change Tryst or the stored backup.

## D-34 — The shared calendar: a month grid of markers through the same gate (implements F12; M4 piece C)
- **Status:** Accepted
- **Context:** D-18 lists a calendar overlay with a day marker per member and a "both active" highlight as the first compare view. D-32 says every
  view of other people's entries goes through `internal/share`.
- **Decision:**
  1. **One month per page,** Monday first, with the neighbouring days of the adjacent months shown so a week is never cut. `?month=YYYY-MM` and
     `?day=YYYY-MM-DD`; anything else, or a year outside 1970 to 2100, falls back to the current month. No script: months and days are plain links.
  2. **A marker per member per day:** the member's initial on a colour. Six colours cycle, and the letter is always there, so colour is never the only
     signal. A day with two or more members active is highlighted and says so in its accessible name.
  3. **Choosing a day lists that day's entries** (time, who, length, rating, and the note only if its owner includes notes), by `share.Entries`.
  4. **Same gate, one definition.** `share.Days` and `share.Entries` build on the same `scope`, and a test hides entries every way the gate can and
     checks that the calendar and the list stay in step. Paused, left, removed, disabled, deleted, older-wording and private entries never appear.
  5. **Days were UTC at first;** per-member time zones arrived in D-35 and now decide every day boundary.
  6. **The legend names only members whose cards are shown,** so a paused member is not listed as a colour.
- **Alternatives:** a script-driven calendar (needs a second script and a looser CSP); per-pair "both active" only (a space can have more than two members, so
  it is "two or more").
- **Consequences:** an entry near midnight local time can sit on a neighbouring UTC day until time zones arrive.

## D-35 — Compare metrics as a registry, and per-member time zones (implements F13, D-20 part 1; M4 piece D)
- **Status:** Accepted
- **Context:** D-18 defines the compare metrics as a versioned registry on neutral derived fields; D-20 says day boundaries use a per-member time zone.
- **Decision:**
  1. **`internal/compare` holds the registry.** A metric has an id, a title, a version, the Tryst schema range it understands, and a build function. A member
     whose backup is outside that range is named and left out of that metric, never shown wrongly. When Tryst's Insights grows, a periodic review decides
     whether a new card joins the registry.
  2. **Four metrics in v1:** *At a glance* (entries, active days, average rating, average length), *Rhythm* (longest streak of consecutive weeks, busiest weekday
     and month), *Trend* (entries per month for the last twelve months and per year, side by side with bars), *Variety* (how many different acts, positions,
     kinks, settings, occasions and toys each member used). The calendar overlay is the fifth, from piece C.
  3. **Counts only.** Catalog names are each person's own labels (D-18), so Variety counts distinct items and never shows a name; "named top categories where
     both share them" is left out of v1 and can return as its own decision. No metric reads a note.
  4. **The same gate.** Metrics are computed from `share.Entries` and `share.Roster` only, so paused, private, left and stale-consent entries never count.
  5. **Periods:** last 12 months (the default), this year, all time. The end of a period is exclusive.
  6. **Time zones.** Each person may set their own IANA zone on their profile; with none, the server's zone `ALCOVE_TZ` applies, and with that unset, UTC.
     Every day boundary (calendar days, active days, weekday, streak weeks, monthly trend) uses the owner's zone, and times are shown in it. Zone names work in
     the distroless image because the zone database is built into the binary. A name is validated against the database; an empty name removes the choice.
  7. **Supersedes D-34 point 5** (days were UTC until this piece).
- **Alternatives:** per-pair "head to head" metrics (a space can have more than two members); named top categories now (needs a labelling decision); a viewer's
  zone for everyone's entries (an entry would move day depending on who looks).
- **Consequences:** the calendar and entries pages now say times follow each person's zone. A person who travels changes their own zone and their past entries
  move days with it.

## D-36 — Telling people something is happening: one script on every page (implements N11 in a wider form)
- **Status:** Accepted
- **Context:** several actions make a person wait: an upload and its check, a backup, a verify, a restore drill, a rebuild, a download of all one's data, signing in (Argon2id), deleting and restoring an upload (the data is rebuilt). Until now only the upload had a bar and the Backups page had a plain notice. A page that goes quiet looks broken.
- **Decision:**
  1. **One same-origin script, `ui.js`, on every page,** replacing `upload.js`. It is still the only script (N11: at most one), still no inline script or handler, still `script-src 'self'`; what changes is that it loads everywhere. Every page works without it.
  2. **Any POST form shows a "Working..." bar** at the bottom of the screen with a running clock after three seconds, and its buttons are disabled (after the browser has read the form, so a button's own value is not lost) so a second click cannot repeat the action. A form can carry its own words in `data-busy`. The state is cleared if the page comes back from the browser's back/forward cache.
  3. **The upload keeps its real progress bar,** then switches to an animated bar with a clock for "Checking the backup", whose length is unknown.
  4. **A download says "Preparing your download" until the browser has really received it.** The script makes a short random token for that one request; the server echoes it into a short-lived, `SameSite=Strict`, non-secret cookie `alcove_dl` (only if it matches `[a-z0-9]{8,40}`) as the file starts; the script sees it, clears it and says "Your download has started". It is the one cookie the script reads, and a test pins that.
  5. **A running job** (backup, verify, drill) shows an animated bar, "Running for N min N s" counted from the job's start, and keeps refreshing itself without the script. Its buttons are disabled while it runs.
- **Alternatives:** polling a status endpoint for real percentages (needs the jobs to report progress, which backup, verify and drill do not yet); a script only on the pages that need it (almost all of them have a form); a server-sent progress stream (another route and a looser CSP for little gain).
- **Consequences:** bars for backup, verify and drill are animated, not percentages; real progress for them needs the jobs to report counts, which is a later change.

## D-37 — Opening one entry, read-only, for its owner and through a space (M4 follow-up; the read half of F14)
- **Status:** Accepted
- **Context:** the lists and the calendar showed only a summary of each entry, so a person could not open an encounter. Editing is M5; seeing is needed now.
- **Decision:**
  1. **Two pages, one builder.** `GET /entries/{id}` is the owner's own entry in full (`share.OwnDetail`, which reads only that person's rows).
     `GET /spaces/{id}/entry/{owner}/{entry}` is a member's entry through the space (`share.EntryDetail`). Both build the page with the same code.
  2. **The shared page uses the list's gate.** `EntryDetail` runs the same `check` and the same `scope` as `Entries`, narrowed to one owner and one entry, so
     every condition that hides an entry from the list (paused, left, removed, not admitted, older wording, disabled, deleted, private, space deleted) hides its page.
     A missing entry, an entry in another space, a wrong owner and an outsider all get the same 404 page.
  3. **The owner's choices apply to the page,** as they do to the list: no note unless they include notes (the page says they keep notes out); partners are not
     named unless they show partner names (the page says how many people, nothing more). Partner orgasm counts and any other per-partner detail are not shown.
  4. **Names, not ids.** Acts, positions, kinks, toys, occasions and finish locations are shown by their labels from the entry's own owner's catalogs; a token that
     points at a removed item shows "(a removed item)". Plain Tryst enums (mood, who started it, protection, settings) are shown in words. A label is looked up in
     the owner's catalog only, so two people who use the same id for different things never mix.
  5. **No raw row leaves the package.** The page struct carries resolved text only; the encounter's JSON is never passed to the template.
  6. **Photos are not shown yet** (a note on the page says so); they come with the photo milestone.
- **Consequences:** the lists and the calendar's day view now link to the entry. An entry is a read-only page until M5.

## D-38 — Search for members: own entries, and the notes a space shows (M8 in part; implements the member half of D-17)
- **Status:** Accepted
- **Context:** D-17 gave search an FTS5 index inside the sealed database, filled at upload time from notes, partner names and profile text, with "members search only what they may see". Nothing used it yet.
- **Decision:**
  1. **Two scopes, one page** (`/search`): *My own entries* and *Shared in <space>* for each space the person is active in.
  2. **Own search** matches the person's own notes and the names and notes of their own partners, and returns the entries (a partner match returns the entries that partner was in). It reads only that person's rows.
  3. **Space search matches notes only,** and an entry is a result only if `share.Entries` would list it for the viewer AND its owner includes notes: what is matched is the note, so an owner who keeps notes out cannot be found by them. Partner names are never searched in a space, because other members are not shown them by default (D-21, D-37). It uses the same `scope` as every other shared view, so every condition that hides an entry hides it from search.
  4. **A search is a POST.** What a person looks for is never in an address, so it stays out of browser history, any reverse proxy's log and a Referer, and the server never logs it (constraint 8; a test scrapes the log).
  5. **Safe by construction.** Every word is turned into a quoted string and the words are ANDed; the last word matches as a prefix. No operator, column filter or wildcard from the person reaches the engine, control characters are dropped, at most 8 words of 40 characters and 100 characters in all, at least 2. A hostile-input test runs a list of attack strings through both searches.
  6. **Results are text, never markup:** the matched words are marked by control characters inside SQLite's snippet and split into plain segments for the template to escape. At most 50 results, newest first, in each owner's own time zone. An entry that matches by note and by partner appears once.
  7. **Not in this piece:** the administrator's search over everything including deleted and paused content (D-17, logged by scope, count and term length only), searching by catalog labels (acts, toys), and profile text.
- **Consequences:** searching a space for a word tells a member nothing about notes they cannot read. A search that finds nothing is the same whether the entries do not exist or are hidden.

## D-39 — Photos: copied out encrypted, blurred until tapped, cleaned of metadata when shared, place off by default (M7; resolves O-6)
- **Status:** Accepted
- **Context:** photos live inside each person's stored upload, which is one encrypted container. Serving one means decrypting the whole backup, so photos have to be copied out, and a photo file can carry its own EXIF block with the GPS position, the camera, a thumbnail and comments. D-21 says a photo's location is shown only when its owner enables it.
- **Decision:**
  1. **Copied out at upload and rebuild.** After an upload is recorded, every encounter photo (a `media` row with an `encounterId`, bytes present in the container) is copied into the blob store, encrypted under the owner's data key with the same streaming primitive as everything else, and listed in the derived `media_blob` table (with the format found in the bytes, never the label). The upload itself is never touched. A new upload, a rebuild, a deletion that changes the current data, and a purge each replace or remove the copies; a photo that cannot be stored is counted, not fatal. Partner and profile pictures are not copied yet.
  2. **Visibility is the entry's.** Another member can open a photo only if the entry it belongs to is one the list would show them AND its owner includes photos. `share.OpenPhoto` builds on the same `scope` as every other shared view. A caption is text the owner typed, so it follows the note choice.
  3. **The place is off by default** and is its own per-space choice (`show_photo_location`). When on, the position is shown as plain numbers ("Taken at 48.8566, 2.3522"). There is no map: a map would need an outside tile service, which constraint 3 forbids. The owner always sees their own.
  4. **The file is cleaned for others.** A photo sent to anyone but its owner has every application segment (EXIF, XMP, IPTC, maker notes), comments, embedded thumbnails and trailing data removed (JPEG) or every text, time and EXIF chunk removed (PNG), whatever the owner's place choice, so the place cannot leak through the bytes. The colour profile is kept so the picture looks right. Any other format, or a file that does not parse, is not shared at all (fail closed); the owner can still download it as a file.
  5. **Blurred until tapped.** Photos appear blurred and a tap shows them; a second tap opens the picture. Without the script the link opens the picture. Pictures are served `no-store`, `nosniff`, with a sandboxing CSP, only as JPEG or PNG (decided from the bytes); anything else is an attachment.
  6. **Not in this piece:** thumbnails (a gallery came later, D-40) (full pictures are sent), partner and profile pictures, and photo captions edits (M5).
- **Alternatives:** decrypt the container on every request (seconds and up to half a gigabyte of memory per photo); serve the original bytes and rely on the place switch (leaks through EXIF); a map (outbound).
- **Consequences:** the data folder and the instance backups hold the photos twice (inside the upload and as the copies); a restore drill reads the copies back.

## D-40 — Photo galleries: one grid per person and per space, through the same gates (M7)
- **Status:** Accepted
- **Context:** D-39 shows photos only on their entry's page. People want to browse them together.
- **Decision:** `/gallery` lists the person's own pictures; `/spaces/{id}/gallery` lists what the space shows them. The space list is `share.Gallery`, built on the same `scope` as Entries and OpenPhoto, plus the owner's include-photos choice and only formats Alcove can clean (JPEG, PNG), so a picture is listed exactly where it could be opened. Captions follow the note choice; places are never listed (they stay on the entry page). Each tile is blurred until tapped (D-39), links to its entry, and loads lazily, 24 to a page, newest entry first. The pictures are served by the existing photo routes, unchanged.
- **Not done here (done in D-44):** real thumbnails. The grid fetched the full picture (cleaned for others), only as it scrolls into view and only 24 at a time. Thumbnails would mean decoding and re-encoding images (orientation, memory, a cache of derived files to protect) and get their own decision if the page proves slow on a phone.
- **Consequences:** no new data and no migration; the guard is the existing one plus `TestAPictureIsInTheSpaceGalleryOnlyWhereItsEntryIs`.

## D-41 — Show times and read birth dates the way Tryst does
- **Status:** Accepted
- **Context:** Tryst stores each start time as one instant (epoch milliseconds) with no zone, and draws everything in the phone's current zone, in the phone's locale (en-US: "Jan 2, 2026, 3:04 PM"). It stores a birth date as noon in the phone's zone. Alcove showed 24-hour ISO times and read a birth date as a UTC date.
- **Decision:** (1) entry times in lists, search, galleries and the entry page use the Tryst en-US style; technical times (admin log, uploads, backups) stay ISO 24-hour in UTC. (2) A birth date offered from a backup is read as a date in the person's own zone, so Tryst's local noon always lands on the picked day. (3) The profile page offers a button that fills the time zone field with the device's zone (read in the browser; nothing is sent until the form is saved). Alcove still fixes each person's zone instead of following a moving phone, so travel does not move old entries.
- **Not matched, on purpose:** "Today/Yesterday" headings (the server would need to know each viewer's today and they change under a cached page); the phone's locale (Alcove has no locale setting yet).

## D-42 — Editing: change records over an upload, replayed on rebuild (M5)
- **Status:** Accepted 2026-11-15. Slices 1 to 4 built (see the addenda below); export is M6.
- **Context:** D-19 fixed what may be edited. Today the row cache is derived from the head upload only, so an edit stored only in the cache would be lost by "Rebuild now". Uploads must never change (constraint 1), every admin action is logged without content (6), deletes are soft with 10 and 45 day windows (7), and every shared view reads the cache through the one gate.
- **Decision:**
  1. **An edit is a revision of kind `edit`** (the column already exists): its parent is the head, its `upload_id` is the same base upload, `created_by` is the person or the admin. Its content is rows in a new table `change` (schema v10, additive): revision, owner, table, row id, field, old value, new value, op (`set`, `add`, `delete`, `restore`). Values are JSON, so they are sealed with everything else, and a purge deletes them with the revision.
  2. **The cache is `replay(base upload, every live edit revision on that upload, in order)`.** An edit writes its change rows and updates the cache and the search index in one transaction; `Rebuild` re-reads the upload and replays the changes. A property test proves the two always agree (edit, rebuild, compare every row). Nothing else in Alcove changes: the share gate, galleries and search keep reading the cache.
  3. **A new upload starts a new chain.** Its edits-so-far are not carried over (no automatic rebase in v1: the phone is the truth for what it holds). Older edits stay in history. The upload page warns before a new upload that edits on the current one will not carry over and suggests exporting first (F15).
  4. **Scope is D-19.** A fixed list of fields, each validated (rating 1 to 5, duration in minutes, mood and initiator from Tryst's own values, protection a set, start time any real instant), the set fields chosen only from the member's own catalog rows, a partner's display name, new entries (new UUID) and whole-entry delete and restore. Everything else passes through byte for byte. A catalog row, per-orgasm maps, media and settings are read only.
  5. **Delete is a change (`delete`), not a removal:** the entry leaves every view and the export at once, shows under "Recently deleted entries", and is restored by a new revision (`restore`) inside the owner's 10 days or the admin's 45. After the window the stored old values of a deleted entry are purged (the fact of the deletion stays).
  6. **History and revert.** Each entry has a history (who, when, which fields, old and new) and a revert, which is just another edit setting the old values back; nothing is ever deleted from history. A person sees their own; the admin sees all.
  7. **Concurrency.** The form carries the revision it was opened on; saving over a newer one is refused with "this entry changed since you opened it".
  8. **Who may edit.** A person edits only their own data. The admin may edit anyone's, and the action goes in the admin log with the table, row id and field names only, never values (constraint 8). Every edit sets Tryst's `updatedAt` on the row.
  9. **Sharing follows automatically.** An edited note or rating appears in a space at once because the gate reads the cache; a private entry stays private; deleting an entry removes it from spaces. Editing never changes any photo.
- **Slices (each ends with tests, guards and CI):** (1) migration, `internal/edit` (validate, apply, replay), the rebuild proof, and editing a person's own entry fields (start time, length, rating, mood, initiator, protection, orgasm count, note) with history, diff and revert; (2) the set fields from the catalog and partner names; (3) add, delete and restore entries; (4) admin edit, the log entries, recovery from any revision, retention windows. Export stays M6.
- **Alternatives:** write edits straight into the cache (lost on rebuild); store a full copy of the rows per revision (large, and hard to diff); rebase edits onto a newer upload automatically (guesses which phone data is right).
- **Consequences:** the schema gains one table; the cache gains a replay path that must stay in step with direct apply (the property test guards it); a new upload makes older edits history only, which the upload page must say plainly.

### D-42 addendum: slice 1 as built
- **Stored:** schema v10 adds `change`. An edit revision shares its upload's `upload_id`; deleting or restoring an upload moves its edit revisions with it (same `deleted_at`), and a revision id that is not an upload cannot be deleted or restored as one. Lists of uploads, recently deleted uploads and the admin counts look at `kind = 'upload'` only. A purge removes the change rows with the upload, and an account purge removes all of that person's.
- **Cache:** `edit.Apply` writes the change rows, the cache row, the search text and `cache_head` in one transaction; `Rebuild` runs `edit.Replay` after `fillCache`. `updatedAt` is stamped from the revision time (seconds times 1000) in both paths, so they agree; `TestEditsSurviveARebuildExactly` compares every cache and search row after a rebuild.
- **Fields:** start time, length, rating, mood, initiator, protection, orgasm count (yours) and note, each checked against Tryst's own values and ranges. An untouched time keeps its stored seconds. Error messages name the field and the rule, never the value.
- **Pages:** `/entries/{id}/edit`, `/entries/{id}/history` (with undo). Owner only; anyone else gets the not-found page. The uploads page warns, when the current upload has edits, that a new upload does not carry them over.
- **Open:** a partner, set fields, new and deleted entries and admin editing are slices 2 to 4. Whether an edited note should re-share to a space someone already left is covered by the existing gate (it reads the cache).

### D-42 addendum: slice 2 as built
- **Set fields:** acts done, acts received, positions, kinks, settings (places), toys and occasions. A catalog set may only hold the person's own catalog rows (`custom:<id>`, checked against their own cache rows, never another person's) or an item the entry already holds, so an item removed from the catalog is not forced off by an unrelated edit. Settings use Tryst's own list of places. Clearing a set stores null. A group is saved only if the form showed it, so a person with an empty catalog never has a set cleared by accident.
- **Partners:** a name and a note, at `/partners`, with history and undo like entries. Naming a partner clears the anonymous flag and clearing the name sets it, written as its own change so a rebuild gives the same row. The flag cannot be edited directly.
- **Shape:** `internal/edit` now works on two tables (encounters, partners) through one apply and replay path; each change record names its table and row, and the search text of a renamed partner is refreshed the way an edited note is.

### D-42 addendum: slice 3 as built
- **Add:** a new entry gets a random UUID, the same columns in the same order as the person's own entries, and Alcove's time stamps. A column some of their entries leave empty is left empty; any other column gets the empty value of its type; the form's fields are set from what was typed. It needs at least one entry already in the backup to copy the layout from, and it cannot carry photos. The one change record holds the whole row. Whether Tryst restores such a row correctly is part of the M6 emulator round trip.
- **Delete:** an entry is removed from the cache together with the rows that hang on it (its partner links and photo rows, found by their `encounterId`), and every removed row is kept in the change records (`op = delete`, position in `new_value`). The entry leaves spaces, search, calendar, galleries and the entry page at once; the owner's direct photo URL stops working too. Partners, other entries and the upload are untouched.
- **Restore:** a new revision puts every row back at its old position (`op = restore`), inside the person's window (the retention setting, 10 days). After it, the values wiped by a purge or a window that has passed make it `ErrExpired`. Restoring an entry that is already there is refused.
- **Wipe:** the purge job wipes the stored values of deleted entries older than the administrator's window (45 days); the deletion stays in the history, and a rebuild keeps the entry deleted. The admin log line gains `,entries:N` only when something was wiped.
- **Pages:** Add an entry, Delete (a confirmation page first), Recently deleted entries (restore buttons while the window is open), all linked from My entries. Order of checks: not found before stale, so a request for someone else's entry is a 404, never a conflict.
- **Still open (slice 4):** see the next addendum.

### D-42 addendum: slice 4 as built
- **Pages:** the administrator's own list of People gets a Data button for each person, opening `/admin/people/{id}`: their entries, partners, and recently deleted entries. The edit, history, undo, delete and restore pages are the same handlers the person uses, run for the person named in the path (`who` in `internal/server/person.go`), so the rules, the checks and the stale-revision guard are shared. A person who does not exist, or any request from someone who is not the administrator, gets the not-found or refusal page.
- **Log (constraint 6):** every page that shows a person's data first writes `data.view` (target: the person, or the table and `person:row`; the field column holds only the kind of page). Every change writes `data.edit`, `data.revert`, `data.delete` or `data.restore` in the same transaction as the change, so both commit or neither does. The target is the table (`encounters`, `partners`) and `owner:row`; the fields are the field names touched, never a value. The log's own alphabet check refuses anything that could carry content.
- **Step-up:** a change asks for the administrator's password again, like every other admin change; looking does not.
- **Windows:** the administrator restores a deleted entry for the administrator window (45 days), the person for theirs (10). The purge wipes the stored values after the administrator window, as before. The person sees the administrator's edits in their history labelled "The administrator" and can undo them; the administrator sees "The person" and "An administrator".
- **Recovery from any revision:** the history lists every edit and each can be undone as a new edit; a whole earlier upload is recovered from Recently deleted uploads (M2).
- **Not done:** the administrator adding an entry on someone's behalf (a new entry is a personal act and needs the person's phone layout anyway); photos are not shown on the administrator's entry page; admin search over everything is M8.

## D-43 — Export: the cache written back as a fresh backup, verified, then a guided restore (M6)
- **Status:** Accepted 2026-11-15. Slices 1 to 3 built and proven (see the addenda at the end of this entry).
- **Context:** D-7 and D-15 fixed the idea (an export is a new file; wipe first; confirm a fresh phone backup first). Now there are edits, added entries and deleted entries (D-42), so the export has to carry exactly the cache and nothing a person deleted. The cache already holds every row of every table in the upload's own order (`cache_row`), so nothing needs a second source of truth.
- **Decision:**
  1. **Build from the cache, around the upload.** Open the head upload with the owner's remembered password (the rebuild path). Keep everything that is not a table row exactly as uploaded: `schemaVersion`, unknown top-level `data.json` keys, `settings.json`, other small entries, the container version. Replace each table's rows with the cache rows, same table order, same row order, same column order, every value as its stored JSON. Nothing is interpreted, so a column Alcove does not know passes through (D-7).
  2. **Photos follow the rows, not the upload.** Keep a `media/<id>` blob unless it was referenced by the upload and is now referenced by no remaining row (a deleted entry's photo). References are read from the four places Tryst's spec lists: `media.id`, `partners.photoMediaId`, `profile.photoMediaId`, `person_photo.mediaBlobId`. A blob the upload never referenced passes through. Reason: a wipe-first restore writes every blob in the file, so a deleted entry's photo left in the file would come back to the phone.
  3. **Written the way Tryst reads it.** One ZIP, every entry DEFLATED, `data.json` first (D-15 addendum 3), then `settings.json`, then media. Sealed with the existing `Seal`, same container version as the upload, a fresh salt, the default iteration count.
  4. **Verified before it is offered.** The finished file is read back with the real reader and the password it was sealed with, and every table's row count, the schema version and the media list must match what was meant to be written. A mismatch offers nothing and says so. Nothing is kept on the volume: the file is built in memory and streamed to the person once.
  5. **Password.** The person chooses it on the export page. The default is the same password as the backup they uploaded (they already know it, and Tryst asks for it at restore); they may type a different one, twice. An export is refused with no password remembered unless they type one. The password is never logged or shown.
  6. **Guided, not a button (F15, F29, F30).** `/export` shows: which upload it is based on and when; what changed since (counts of entries edited, added and deleted and partners renamed, with the field labels, no admin-log content rules apply because it is the person's own page); a warning that anything made on the phone since that upload is lost by a wipe-first restore and that the phone's own fresh backup should be exported first; the numbers Tryst's restore dialog should show ("N trysts, M partners and K photos"); and the steps (download, open from Downloads in Tryst, choose **replace my current data**, never merge, which loses partner links). The download button stays disabled until the person ticks that they exported a fresh backup from the phone.
  7. **Step-up and log.** An export asks for the password again (F21). A successful export writes `export.create` to the admin log (target: the person, fields: counts only), the same record any security event gets. The filename is `alcove-<name>-<date>.tryst`, sent as an attachment, never cached.
  8. **Whose.** A person exports their own data only. The administrator does not export other people's data in v1 (it would need their remembered password; a person can do it themselves).
  9. **Not exported:** the admin's view of deleted entries and everything in history. The file holds the current state only; history stays on the server.
  10. **Tryst schema newer than known:** export stays off (D-29), as editing does.
- **Slices (each ends with tests, guards and CI):** (1) `internal/export`: build, filter photos, seal, read back and compare; tests for an untouched upload reading back identical row for row, and for an edit, an added entry, a deleted entry with a photo, a renamed partner, and an unknown column and table. No pages. (2) The `/export` pages: the summary and diff, the confirmation, the password choice, step-up, the download, the log line, the log-scrape test over upload, edit and export (N4), the smoke script. (3) The emulator round trip (N8): export the synthetic edited backup, restore it in Tryst on the emulator with wipe first, and compare the app's own re-export row by row; also one merge run to confirm the guide's warning still holds. Then the restore guide text and the roadmap tick. Slice 3 needs an Android emulator and never a physical phone.
- **Alternatives:** write edits into the original ZIP in place (touches the immutable upload and mixes states); one global export password (would need a secret Alcove must keep); export without read-back (a bad file would only show up on the phone); leave deleted photos in the file (they return on restore).
- **Consequences:** an export needs the owner's data to be rebuildable from the stored upload, so a person with no remembered password types their backup password to export. The file is as large as the upload and is held in memory while built (bounded by the upload cap, 512 MiB). A later Tryst release can change restore behaviour, so slice 3's round trip is a recurring check, noted in the release checklist.

### D-43 addendum: slice 1 as built
- **Where:** `internal/export` is a pure function from a plan (the upload as read, the current rows, a password) to a finished file; `ingest.Service.Export` gathers the plan (the cache rows under one read, then the stored upload opened with the remembered or a given password). Nothing is written to disk and the upload is never touched (a test compares the stored files before and after).
- **Safeguards:** the file is read back with the real reader and compared to the plan (schema, container version, table list, every row's columns and values, settings, carried entries, the photo list) before it is returned; a mismatch returns `ErrCheck`. A schema newer than Alcove knows is refused. Export refuses a cache that is not the head revision's (`ErrCacheStale`).
- **Proved by tests:** an untouched upload reads back identical row for row with the same photo bytes; edits, an added entry, a deleted entry with its photo (left out of the file) and a partner rename are written; unknown tables, columns, top-level keys, small entries and an unreferenced photo pass through; the archive has `data.json` first and every entry DEFLATED; feeding an export back in as a fresh upload gives the same rows as the cache. Six deliberate breakages (keep every photo, STORED instead of DEFLATED, drop unknown keys, drop settings, wrong row order, and the read-back check itself) were each caught.
- **Not yet:** the pages, step-up, log line and download (slice 2); the emulator round trip (slice 3).

### D-43 addendum: slice 2 as built
- **Pages:** `/export` (linked from My data) shows what the file is based on and its age, the net changes since the upload (entries changed, added and deleted, partners changed, the fields touched, from `edit.Summarize`; a row added and deleted again, and the link and photo rows of a deleted entry, are not counted), the numbers Tryst's restore dialog should show, and the steps in Tryst's own wording (Settings, Backup & restore; "Replace my current data with the backup (recommended)", kept on). The form has the confirmation box, a password choice (the same as the upload's, or a new one typed twice, at least 8 characters), and asks for the upload's password only when none is remembered.
- **Rules:** the download is a POST with the token and a fresh step-up (`/export` is a valid step-up return); without the ticked confirmation nothing is built. The file is built, read back, then logged and sent: `export.create` carries counts only (`entries`, `partners`, `photos`, `left_out`), and the server log line carries the same. The name is `alcove-<username>-<date>.tryst`, `application/octet-stream`, `no-store`. A wrong password, a stale cache (ask the administrator to rebuild), a newer Tryst and a failed read-back each show a plain message and offer nothing.
- **Proved by tests:** the page, the summary numbers, the step-up, the confirmation, short and mismatched passwords (nothing logged), the downloaded file opening with the edit present and a deleted entry absent, a new password opening it and the old one not, kim's export holding kim's rows, no upload and signed-out getting nothing, and a typed password never reaching either log. Four deliberate breakages (no step-up, no confirmation, no log line, the password in the server log) were each caught. The smoke script gained block 4n, which runs against the built image in CI.

### D-43 addendum: slice 3, the emulator round trip (N8), 2026-11-15
- **Setup:** Tryst 0.6.5 (versionCode 21, schema 19, debug build from the Tryst repo, unmodified) on an Android 15 x86_64 emulator (read-only image, `emulator-5554`, no phone attached), driven through its accessibility tree (the app hides its window from screenshots). Synthetic data only: a base backup (10 entries, 3 partners, 4 photos), a different phone-only backup (6 entries, 2 partners, 2 photos) and the export built by Alcove after an edit, an added entry, a deleted entry that owned 2 photos, and a renamed partner.
- **Wipe first (the mode the guide requires): passed, twice.** Restoring the export with "Replace my current data with the backup (recommended)" on, over a phone holding the base and over a phone holding the unrelated backup, then having the app export again: **0 differences** table by table, column by column, photo by photo. The edit, the added entry (including the row layout Alcove made for it), the deleted entry and both of its photos, and the partner rename were all as intended, and none of the phone-only data survived. Tryst's own restore dialog said "10 trysts, 3 partners and 2 photos", exactly what the export page predicts. The slice 1 check that left deleted photos out of the file was necessary: with them in, a wipe restore would have restored the photos.
- **Merge (toggle off): still not safe, re-confirmed.** Over the base, the entry deleted here stayed with its photos (10 + 1 added = 11 entries, 4 photos) and its partner link was dropped (9 links, not 10). Over unrelated phone data nothing was lost, because replacing a partner only drops links when the same partner already exists on the phone. The guide keeps saying never to switch the toggle off.
- **Tryst crashed twice in about fourteen restores of the export** (and not in about twenty other restores and exports). The first was a native crash in a background thread in `CatalogAdoption.adoptUnknownIds` reading `Act.entries`; the second a null jump from the JIT code cache in Compose's `Color.convert` on the main thread. Both are in compiled app code on a 16K-page x86_64 image and unrelated to what the backup contains, and the next attempt of the same restore passed each time; they are recorded as emulator instability, not as an Alcove fault. The committed script retries a crashed step and reports the count. Worth a look on the Tryst side if it ever shows on a real phone.
- **Repeatable:** `scripts/emulator/roundtrip.ps1` (and `docs/RELEASE_CHECKS.md`) re-run the wipe-first check with fresh files and pass only with zero differences. It is part of the checklist for each Alcove release that touches `internal/export` and for each new Tryst release.
- **Not covered:** a real phone and real photos (never automated against a phone); a real private backup (the owner's call); very large backups; Tryst versions other than 0.6.5.

## D-44 — Thumbnails made on demand and kept only in memory; a shared photo keeps its rotation (M7)
- **Status:** Accepted
- **Context:** the galleries (D-40) load the full picture for every tile, 24 at a time, which is slow on a phone over wifi. Making a small picture means decoding untrusted images, which costs memory and time. Separately, cleaning a photo for sharing (D-39) removes the whole EXIF block, and that block holds the one number (Orientation) that says which way up a phone picture goes, so every portrait photo showed on its side to the people it was shared with.
- **Decision:**
  1. **Made on first request, never stored.** `/photos/{id}/thumb` and `/spaces/{id}/photo/{owner}/{media}/thumb` run the same gate as the picture route first (`share.OwnPhoto` / `share.OpenPhoto`), then decrypt the stored picture, shrink it to at most 360 pixels on the long side, and send a JPEG. The result is kept in an in-process cache (at most 512 entries and 32 MiB, reused for 10 minutes). Nothing is written to disk, so there is no new data to protect, migrate, back up or purge, and a restart simply makes them again. The cache is only read after the gate, so a private flag, a pause, leaving a space or turning photos off closes a cached thumbnail at once.
  2. **Drawn from pixels only.** A thumbnail is a fresh JPEG built from the decoded pixels, turned by the Orientation tag, with transparency drawn on white. No byte of the original's metadata can be in it (position, camera, comments); it is the same file for the owner and for others.
  3. **Bounded decoding.** The size is read from the header first and a picture over 50 million pixels is refused before anything is allocated; at most two pictures are decoded at once; the stored picture is read up to the existing 64 MiB bound. Anything that fails (not decodable, too large, a format other than JPEG or PNG) gets the full picture instead, through the existing route, so a tile is never broken.
  4. **The galleries load thumbnails and link to the picture.** The tile image is the thumbnail; the link, and the entry page, still use the full picture (blurred until tapped, D-39).
  5. **Rotation survives cleaning.** When a JPEG's EXIF block is dropped, its Orientation value (2 to 8) is written back in an EXIF block that holds nothing else (32 bytes, no other tag, no thumbnail). Value 1 or none writes no block. Cleaning twice still changes nothing.
- **Alternatives:** thumbnails written beside the photos at upload (a second encrypted file per photo, a schema change, a rebuild step for every existing photo, and a purge path); a client-side resize (the full picture still has to be downloaded); caching in the browser (photos are `no-store` on purpose); an external image tool or library (a new dependency and a larger attack surface for the same result).
- **Consequences:** no migration and nothing to do when updating. The first view of a gallery page decodes up to 24 pictures (a second or two on a small server); the next views are instant for ten minutes. Plain decoded thumbnails exist in memory for a short time after a person's photo is viewed, which is the same category as every decrypted value (constraint 4, D-16). Partner and profile pictures are not part of this decision.
- **Guard:** `internal/photo` tests for size, orientation (all eight turns are mapped, two checked by pixel), metadata-free output, transparency, truncated and oversized files; `TestAThumbnailIsClosedWhereItsPictureIsClosed` proves the gate wins over a warm cache.

## D-45 — Admin log filters and CSV export (M8 slice 1; completes F18)
- **Status:** Accepted
- **Context:** the log viewer pages through every line, newest first. With editing and export the log grows by a line per look and per change, so finding one person's or one day's lines by paging is not workable, and there was no way to keep a copy outside the server.
- **Decision:**
  1. **Filters** on the log page: who (a user name, or `system`), action (exact, or the start of one, so `data.` is the group), target type, and a from and to date (UTC, whole days). All optional, all combinable. Words are only ever bound as query parameters; a word outside the log's own safe alphabet, or a date that does not parse, is ignored with a plain note rather than a blank page, and an unknown user name matches nothing. `%` and `_` are plain characters. The filter is kept while paging.
  2. **Export** sends the matching lines (or the whole log with no filter), oldest first, as a CSV: seq, time, who, action, target type, target id, fields. It is a POST behind the CSRF check and a step-up, capped at 200,000 lines so memory stays bounded, and carries only what the page shows, never content. A cell that starts with `=`, `+`, `-`, `@` or a control character is prefixed with an apostrophe so a spreadsheet cannot run it as a formula.
  3. **The export is itself logged** (`adminlog.export`, fields `rows:N`, plus `filtered` when a filter was set); a filtered view is logged as `adminlog.view` with the field `filtered`. The filter words are not logged (constraint 8; the log's alphabet could not hold most of them anyway).
- **Alternatives:** a GET download (a link or an image tag could trigger it, and it writes a log line); an export of the raw hash columns (the chain is verified on the server by "Verify now"; the head hash is shown for noting elsewhere); free-text search over the log (the fields are identifiers, so prefix and exact matching covers the use).
- **Consequences:** no schema change and no migration. The export is a convenience copy; the tamper evidence still lives in the chain and the head hash.
- **Guard:** `internal/adminlog` filter tests (every field, wildcards as text, paging); server tests for the step-up gate, the CSV shape, the filter on export, the log line, member and signed-out refusals, and formula-safe cells.

## D-46 — Admin search over everything: current notes and partners, outside the sharing gate, logged by shape (M8 slice 2; completes the admin half of D-17)
- **Status:** Accepted
- **Context:** D-17 promised the administrator a search over every person's content. D-38 built the member half on the same FTS5 index. The administrator can already open anyone's entries (D-42), but only by going person by person.
- **Decision:**
  1. **One page, one scope: everyone.** `/admin/search` (administrators only) finds words in every person's current entry notes, and entries whose partner's name or note matches, whatever the owner shares: private, paused, notes kept out, in no space at all. Results link to the person's entry in the administrator's own view (`/admin/people/{id}/entries/{id}`), which already logs every look.
  2. **Its own package, not `internal/share`.** `share` is the one gate for other members (D-32) and its tests prove nothing leaks past it. The administrator's search deliberately reads past that gate, so it lives in `internal/adminsearch` with a single caller (the admin handler), and shares only the word handling (`share.Match`: every word quoted, ANDed, last one a prefix, no operator reaches the engine) and the snippet marking.
  3. **A POST, never an address.** The words stay out of history, proxy logs and Referer. The server does not log them.
  4. **Logged by shape, in the same transaction:** `admin.search`, target `search all`, fields `len:<characters>,hits:<count>`. The term is not recorded (constraint 8; O-12 stays "no"). A term that is refused (too short or too long) reads nothing and logs nothing.
  5. **What it does not find: deleted entries.** Deleting an entry removes its search rows (D-42), so an entry its owner deleted is not searchable until it is restored. The administrator reaches those through the person's page (Recently deleted), which lists them for the 45-day window. Pictures are not searched (they have no text); photo captions are not indexed.
- **Alternatives:** put it in `share` behind an admin flag (a flag one mistake from "everyone sees everything" in the one package that must not leak); keep deleted entries searchable (a second index of deleted text that the purge must also empty, for little use); log the term (turns the log into a copy of the content).
- **Consequences:** no schema change and no migration. The administrator's reach is unchanged (D-6: the admin sees everything); the search only makes it faster, and every use is on the record.
- **Guard:** `TestAdminSearchFindsEverythingAndLogsOnlyTheShape` (private and paused notes found by the administrator and not by a member; the log and the server log carry no words; a deleted entry is not found) and `TestAdminSearchIsAdminOnlyPostOnlyAndRefusesBadTerms`.

## D-47 — A health page: counts and states, and a plain list of what needs attention (M8 slice 3)
- **Status:** Accepted
- **Context:** the admin console showed three numbers. The things that go wrong quietly on a self-hosted server are things nobody looks at: no backup in a month, a backup never restored, a log chain never checked. Sanctum learned this the hard way (a key copy that was never proved).
- **Decision:**
  1. **`/admin/health`** (administrators only) lists, in four groups, storage and protection (database open, last sealed, schema, thumbnail cache), backups and checks (last instance backup, last restore drill, last admin-log check), what the server holds (accounts, uploads, photos, spaces, log lines, all counts) and network and settings (HTTPS required, proxy mode, a pending network change, default time zone, version and start time).
  2. **Each row has a level:** `ok`, `warn` or `info`. A warning means there is something to do and says what: no backup folder configured, never backed up, backup older than 30 days, never run a restore drill, admin log never checked, last check failed, check older than 30 days, a network change waiting to be confirmed. The page and the console home both show how many warnings there are.
  3. **Counts and states only.** No names, no content, nothing that could identify an entry. It reads; it changes nothing. Opening it is written to the admin log (`admin.health.view`), like every console page.
  4. **Not included, on purpose:** free disk space and memory (they need a platform-specific call; Unraid's own dashboard shows them better), and any outbound check (constraint 3). A seal that has not happened is `info`, not a warning, because an idle server has nothing to seal.
- **Alternatives:** a JSON `/healthz` with details (that endpoint is unauthenticated and stays a bare ok); alerts by email or webhook (the optional notification slice, F24).
- **Consequences:** no schema change. The 30-day line matches the monthly nudge the author's own backup scripts use.
- **Guard:** `TestHealthPageSaysWhatNeedsAttentionAndNothingPrivate` (a new server shows exactly three warnings; doing the three things clears them; a month later two return; no person is named; the view is logged) and `TestHealthShowsAFailedLogCheckAndIsAdminOnly`.
