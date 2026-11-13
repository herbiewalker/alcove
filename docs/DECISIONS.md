# Alcove — Decision Log (ADRs)

> **Status:** Kickoff set and design pass 1 done, 2026-11-12 (D-15 to D-23; O-2, O-3, O-4, O-11 to O-13 resolved; O-10 withdrawn; O-1 and O-7 proposed). Newest at the bottom, open questions last. Never delete an ADR;
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

## D-16 — Storage: a sealed in-memory database, per-user data keys, one master key (resolves O-1)
- **Status:** Proposed
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
- **Status:** Proposed
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
  term's length, never the term (constraint 8). (3) The export guide **recommends wipe first** when an export has
  deletions or must be exact, merge otherwise, always behind the "fresh phone backup first" confirmation (D-15).

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


## Open questions
> Decisions deferred on purpose. Each is a future ADR. Do not let these rot silently.

- **O-1 — Storage scheme.** *Proposed in D-16; awaiting review.*
- **O-2 — Compare metrics.** *Resolved in D-18.*
- **O-3 — Editable fields.** *Resolved in D-19.*
- **O-4 — Restore semantics.** *Resolved from code in D-15; emulator proof gates M6.*
- **O-5 — TOTP scope.** Admins only, or any user; recovery-code handling.
- **O-6 — Location features.** Whether and how to show photo locations on a map, and the default visibility.
- **O-7 — Admin search.** *Proposed in D-17; term logging resolved in D-20.*
- **O-8 — Member activity view.** Whether to ship the optional "activity on my data" view and its default.
- **O-9 — Shared library extraction.** When and how to extract `trystbk` for both projects.
- **O-10 — Linking members to Tryst partners.** *Withdrawn in D-21 (the collaborative model needs no matching). May return later as its own feature.*
- **O-11 — Time zone.** *Resolved in D-20.*
- **O-12 — Admin search terms.** *Resolved in D-20: not recorded.*
- **O-13 — Restore default.** *Resolved in D-20.*
- **O-14 — Resumable uploads.** A phone browser can sleep or drop wifi during a large upload. First version: progress bar
  and a keep-the-screen-on hint. Chunked, resumable upload is a later step if it proves a problem.
- **O-15 — Optional TLS on the LAN.** *Resolved in D-23: secured wifi by default, with admin-enabled proxy mode, allowed networks, a banner and require-TLS mode. A built-in local CA is not planned.*
