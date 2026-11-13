# Alcove — Decision Log (ADRs)

> **Status:** Kickoff set, 2026-11-12. Newest at the bottom, open questions last. Never delete an ADR;
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

## D-5 — Sharing model: private by default, opt-in at several levels
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


## Open questions
> Decisions deferred on purpose. Each is a future ADR. Do not let these rot silently.

- **O-1 — Storage encryption scheme.** Per-user or per-space data keys wrapped by a master key (fast purge and
  export) versus one key; how the decrypted working copy and the search index are protected at rest.
- **O-2 — The compare-metrics set.** Which metrics, defined against which Tryst fields, and how they behave
  across Tryst schema versions.
- **O-3 — Editable fields.** The exact list of fields editable in v1; everything else passes through untouched.
- **O-4 — Restore semantics.** Confirm on an emulator that restoring an edited backup updates rows by id, adds
  new ones, and what happens on conflict and on deletion. This gates the writer milestone.
- **O-5 — TOTP scope.** Admins only, or any user; recovery-code handling.
- **O-6 — Location features.** Whether and how to show photo locations on a map, and the default visibility.
- **O-7 — Admin search index.** How search over decrypted content is built and protected at rest.
- **O-8 — Member activity view.** Whether to ship the optional "activity on my data" view and its default.
- **O-9 — Shared library extraction.** When and how to extract `trystbk` for both projects.
- **O-10 — Linking members to Tryst partners.** A member may appear as a named partner in someone else's
  entries. Whether to let two members link "this partner is that account", to match shared encounters from both
  sides in the compare view, and how that interacts with hiding partner names.
