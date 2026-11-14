# Alcove — Decision Log (ADRs)

> **Status:** Kickoff set and design pass 1 done, 2026-11-12 (D-15 to D-26; O-2, O-3, O-4, O-11 to O-13 resolved; O-10 withdrawn; O-1 and O-7 proposed). Newest at the bottom, open questions last. Never delete an ADR;
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
- **Decision:** a server hosts several independent spaces (a couple, a family branch). A person can belong to
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
  handed over in person or through the family's own channel.
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
- **O-4 — Restore semantics.** *Resolved from code in D-15; emulator proof gates M6.*
- **O-5 — TOTP scope.** Admins only, or any user; recovery-code handling.
- **O-6 — Location features.** Whether and how to show photo locations on a map, and the default visibility.
- **O-7 — Admin search.** *FTS5 inside the sealed database works (M2a); term logging resolved in D-20.*
- **O-8 — Member activity view.** Whether to ship the optional "activity on my data" view and its default.
- **O-9 — Shared library extraction.** When and how to extract `trystbk` for both projects.
- **O-10 — Linking members to Tryst partners.** *Withdrawn in D-21 (the collaborative model needs no matching). May return later as its own feature.*
- **O-11 — Time zone.** *Resolved in D-20.*
- **O-12 — Admin search terms.** *Resolved in D-20: not recorded.*
- **O-13 — Restore default.** *Resolved in D-20.*
- **O-14 — Resumable uploads.** A phone browser can sleep or drop wifi during a large upload. First version: progress bar
  and a keep-the-screen-on hint. Chunked, resumable upload is a later step if it proves a problem.
- **O-15 — Optional TLS on the LAN.** *Resolved in D-23: secured wifi by default, with admin-enabled proxy mode, allowed networks, a banner and require-TLS mode. A built-in local CA is not planned.*
