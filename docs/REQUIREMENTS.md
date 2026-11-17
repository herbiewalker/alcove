# Alcove — Requirements

> **Status:** Kickoff draft (2026-11-12), kept current; see "Status of each requirement" near the end for what v0.1.2 has built. Tracks `CLAUDE.md`'s hard constraints as
> testable NFRs. IDs are stable; add, never renumber.

## Problem & users

- **Problem:** Tryst keeps private records on one phone with no cloud and no network. A couple cannot see a
  shared history, and entries are tedious to fix on a phone. Sanctum stores backups but deliberately shows nothing.
- **Users:** couples: people who are partners and want to see each other's logs. Each couple has its own independent space, and a person can belong to more than one (for example with more than one partner). One admin runs the server.
- **Success criteria (v1):**
  1. Two people each upload a backup and see a shared calendar overlay built only from what each chose to share.
  2. A third person joins a different space, and neither space can see the other.
  3. A person edits entries on a computer, exports a `.tryst`, and restores it into Tryst on an emulator with the edits applied.
  4. The admin can add and remove users, reset a login, restore a deleted entry, and read the admin log.
  5. A server restart needs no human unlock.
  6. The constraint guards and isolation tests pass in CI.

## In scope (v1)

- **R1 Accounts and spaces.** Admin-created or invited accounts, independent spaces, owner-approved joining,
  people in several spaces, leaving and removal.
- **R2 Upload and revisions.** Verified, immutable uploads with a revision history per person.
- **R3 Sharing.** A space is collaborative: members see each other's entries (D-21). Nothing is visible outside the space. Pause, per-entry private, and consent at join.
- **R4 Views.** Shared calendar overlay and compare metrics.
- **R5 Editing.** Edit entries in the browser as new revisions with history, diff and recovery.
- **R6 Export.** Write a fresh, Tryst-compatible `.tryst` from a revision.
- **R7 Photos.** Share photos between members; location kept and shown only when the owner allows.
- **R8 Admin.** Console for accounts, spaces, health, search over everything, and the admin log.
- **R9 Recovery.** Soft delete with a 10 day user window and a 45 day admin window.
- **R11 Phone workflow.** Mobile-first pages so a backup can be uploaded from, and an edited backup downloaded to, a phone over the LAN (D-22).
- **R10 Profiles and invites.** Prefill a member profile from the Tryst profile in their backup (each field opt-in to share), and invite people to the server or a space with one-time, expiring, revocable invites.

## Non-goals (v1)

- Social features: feed, comments, messaging, moderation.
- Live sync (a Tailscale-based sync is a possible later step).
- Public internet exposure, port forwarding, a hosted service.
- Any change to the Tryst app.
- Video, until Tryst supports it.
- Native apps, push notifications, e-mail or SMS.

## Functional requirements

| ID | Requirement | Priority |
|----|-------------|----------|
| F1 | First run creates the admin; no open sign-up anywhere (constraint 11) | Must |
| F2 | Admin can create, disable, delete and reset users; a user is disabled before deletion and has a grace period | Must |
| F3 | A user can download all of their own data before deletion | Must |
| F4 | Create independent spaces; each has an owner; a person may belong to several | Must |
| F5 | Any user can request to join a space; the join screen states exactly what will be shared; the owner approves (D-21); an `all_members` approval mode is a later per-space setting | Must |
| F6 | Leaving or removal stops that person's sharing at once and removes their shared contributions from the space | Must |
| F7 | Upload a `.tryst`; verify by trial decrypt with the member's stored password; refuse duplicates by SHA-256 | Must |
| F8 | Every upload is an immutable revision; edits create new revisions; full history per person | Must |
| F9 | Read any supported Tryst schema generically; unknown tables and columns survive a round trip untouched | Must |
| F10 | Refuse or open read-only any backup with a schema newer than supported | Must |
| F11 | Inside a space every member sees every other member's entries; per-membership settings (include notes, include photos, show partner names, pause) and a per-entry private flag; partner names hidden by default (D-21) | Must |
| F12 | Calendar overlay showing every member's entries in the space (solo, with others and multi-partner entries all appear as the logging member's own) | Must |
| F13 | Compare metrics across members of a space (the registry in D-18) | Should |
| F14 | Edit an entry's editable fields (O-3) in the browser; each save is a revision with a diff | Must |
| F15 | Export a revision to a fresh `.tryst`; show which upload it is based on, warn if a newer upload exists, show a diff first, and give a guided restore procedure (D-15) | Must |
| F16 | Soft delete everywhere; "recently deleted" restore for the user (10 days) and for the admin (45 days); purge is real | Must |
| F17 | Admin can view, search and edit anything, including deleted and paused content | Must |
| F18 | Append-only, hash-chained admin log of all admin actions and security events, filterable and exportable | Must |
| F19 | Share photos between members; blur until clicked; location kept, shown only when allowed | Should |
| F20 | Optional TOTP for admins, with recovery codes | Should |
| F21 | Step-up re-login before export and delete | Should |
| F22 | A "hide screen" button that blanks the page to a neutral view | Could |
| F23 | Encrypted instance backup and a tested restore drill | Must |
| F24 | Optional notifications to the admin (failed logins, errors, no backup in 30 days) via one configured URL | Could |
| F25 | Optional member-visible "activity on my data" view, off by default | Could |
| F26 | A member can prefill their Alcove profile from the `profile` table of their uploaded backup (display name and photo by default; other fields only if chosen). Each profile field has its own share setting and defaults to private | Should |
| F27 | Invites: an admin invites a person to the server; a space owner invites a person to a space (new or existing account). Invites are single-use, expire (default 7 days), are stored hashed, can be revoked, and are rate-limited on redemption. No e-mail is sent; the link or code is handed over in person or through your own channel | Must |
| F28 | A space shows its members as profile cards built from what each has chosen to share, plus pending invites and join requests for the owner | Should |
| F29 | The export guide tells the user to restore with "replace my current data" (wipe first), warns that merge loses partner links, and requires the user to confirm they exported a fresh backup from the phone first (D-15 addendum) | Must |
| F30 | **Phone workflow over the LAN.** A person can export a backup in Tryst on the phone, open Alcove in the phone's browser on the same network, and upload it; and can download an edited `.tryst` to the phone and restore it from Downloads in Tryst. The pages are mobile-first and touch-friendly (D-22) | Must |
| F31 | Optional QR codes for the server address and for invites, generated locally with no outbound call | Could |
| F32 | Admin settings for transport security (D-23): reverse-proxy mode, allowed networks (default: private LAN ranges), an insecure-connection banner, and a require-TLS mode. Changes need step-up; allowed-network changes auto-revert unless confirmed within 60 seconds; an environment value wins and is shown as such | Should |
| F33 | `alcove keygen` creates the master key (never overwriting one) and `alcove keycheck` proves a copy by printing a fingerprint; the server never creates a key; the key file must be outside the data directory (D-26) | Must |

## Non-functional requirements

| ID | Requirement | How it is verified |
|----|-------------|--------------------|
| N1 | A non-member or another space never sees a member's entries; a paused membership and a private-flagged entry are never shown to the space | Tests: non-member and cross-space isolation, pause, per-entry private, leave removes contributions |
| N2 | Data is encrypted at rest; the key is stored apart from the data; the app refuses to start if the key file is inside the data directory | Startup test; `guards.sh no-secrets`; trivy |
| N3 | No outbound network calls or telemetry by default | `guards.sh no-outbound`; CSP |
| N4 | Logs, alerts and errors carry no content | Log-scrape test over a full upload, edit and export |
| N5 | Every admin action writes an admin log entry; the chain verifies | Tests per action; chain-verification test |
| N6 | A tampered stored upload fails verification | Test: flip a byte |
| N7 | A wrong password fails cleanly without a partial write | Test |
| N8 | An exported backup restores in Tryst with the edits applied | Emulator round trip (never a physical phone) |
| N9 | A hostile backup (absurd KDF count, Zip-Slip names, zip bomb, truncated stream) is rejected, never crashes | Forced tests |
| N10 | Container is non-root, read-only, caps dropped, digest-pinned, one port, limits, healthcheck, SIGTERM | CI checks and a live smoke script |
| N11 | Strict same-origin CSP; at most one same-origin script | Test |
| N12 | Accounts cannot be created without authentication; sessions are rate-limited and expire | Tests |
| N13 | Purge actually removes data after the window | Test |
| N14 | Mobile-first and responsive in any current browser at any width; light, dark or automatic theme; large touch targets; the upload and download flows work in a phone browser (Chrome on Android at minimum) | Layout checks at phone, tablet and desktop widths; a real-phone check at the first live deploy |
| N15 | No personal information in the repo | `guards.sh no-secrets`, gitleaks, review |
| N16 | Decrypted user data exists only in memory or tmpfs, never on the data volume (D-16) | `scripts/guards.sh no-plaintext`: ingest and edit a synthetic dataset, then scan the data directory for ZIP magic, JSON and sentinel strings |
| N17 | With allowed networks at the default, a request from a public address is refused; in proxy mode forwarded headers are trusted only from configured proxies; in require-TLS mode a login over plain HTTP is refused; an unconfirmed allowed-network change reverts | Tests for each mode |

## Status of each requirement (v0.1.2, 2026-11-15)

Built means it works and has a test. "Partly" and "Not built" say what is missing.

| Built | Requirements |
|-------|--------------|
| Yes | F1 to F13, F16, F21, F23, F26 to F29, F32, F33 |
| Yes, with a change | **F14** history shows each change's old and new values with an undo, not a side-by-side diff. **F15** the export page shows the base upload and what changed (counts and field names) and gives the guided steps; the "newer upload exists" warning became "a new upload starts over, and the page warns before it" (D-42), because a newer upload replaces the chain |
| Partly | **F17** the administrator can view, search and edit anyone's entries and partners (all logged) but cannot see their photos there or search deleted entries (D-46). **F18** is done: the log, its viewer, filters, CSV export and "verify now" (D-45). **F19** photos are shared, blurred, cleaned and gallery-listed; thumbnails and partner or profile pictures are not built (M7). **F30** upload from a phone browser is checked in a phone-sized browser and in CI; the export download in a real phone browser is not yet tried |
| Not built | F20 (TOTP), F22 (hide screen), F24 (notifications), F25 (activity view), F31 (QR codes) |

| NFR | Status |
|-----|--------|
| N1 to N7, N9 to N13, N15 to N17 | Verified by the tests and guards named in the table above, run in CI |
| N4 | Log-scrape tests cover an upload, an admin edit and an export (counts only, no content or passwords) |
| N8 | Proven on a Tryst 0.6.5 emulator, wipe first, with synthetic data (D-43 addendum). Not on a real phone; repeat for each Tryst release (docs/RELEASE_CHECKS.md) |
| N14 | Checked in a phone-sized browser while building; Chrome on a real Android phone is not yet tried |

Success criterion 3 (edit, export, restore on an emulator) is met. Criteria 1, 2, 4, 5 and 6 are met in tests and the container smoke; living with it for real is the owner's step (RUNBOOK 12).

## Riskiest assumption

That people will accept a manual export and restore procedure (D-15) instead of sync. If the guided flow feels
heavy, the edit feature goes unused. Validate the flow on an emulator and with a real user before polishing it.
