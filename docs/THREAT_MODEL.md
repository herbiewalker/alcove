# Alcove — Threat Model

> **Status:** Kept current through v0.1.2 (2026-11-15; R28 to R30 added for editing and export). Not yet independently audited (M9). Aligned with D-3 to D-6 and D-10 to D-11. Keep current: a feature that adds
> an asset or a surface changes this file (principle 3). Unlike Sanctum, Alcove shows content, so this is a
> new model, not a copy.

## Assets to protect

- **A1 — Encounter data and notes.** The most sensitive category a household holds.
- **A2 — Photos and their location metadata.**
- **A3 — Third parties.** Entries name other people who never consented to being on a server.
- **A4 — Members' stored Tryst backup passwords.** Compromise means A1 and A2 in full.
- **A5 — The data key.**
- **A6 — Sharing choices.** What each person has chosen to expose is itself sensitive.
- **A7 — The admin log.** Integrity matters more than secrecy.
- **A8 — Availability and integrity of the user's own data**, including edits made on the server.
- **A9 — The existence of the service.**

## Adversaries in scope

| ID | Adversary | Capability | Priority |
|----|-----------|------------|----------|
| T1 | **Curious household member** or guest | On the LAN, can open the page or an unlocked screen; no admin rights | High |
| T2 | **Another LAN device** | Can reach the port and scan; no host access | High |
| T3 | **A non-member or another space reaching in** | A valid account trying to see a space it does not belong to, or a member trying to see entries marked private or paused | High |
| T4 | **A member who saves or leaks** what was shared | Legitimately saw it; can screenshot or forward | Accepted (R2) |
| T5 | **Offline media** | A pulled or RMA'd disk, a copied backup of the data directory. A backup archive is ciphertext, but its file list shows opaque owner ids, file counts and sizes, so a reader of the backup share can count users, uploads and photos (audit 2026-11-21) | High |
| T6 | **A hostile backup file** | Crafted headers, absurd KDF counts, Zip-Slip, zip bombs, truncated streams | Medium |
| T7 | **A tamperer** with write access to the data directory | Can modify or delete files | Medium |
| T8 | **A stolen session or password** | LAN credential theft, shoulder surfing | Medium |

## Out of scope (and why)

- **The admin, or root on the host.** Trusted by design (D-3). Their access is total but logged (D-6).
- **Compromise of a member's phone.** Tryst's own threat model owns that boundary.
- **Targeted, well-resourced attackers**, and traffic analysis on the LAN. The model is the household and the LAN.

## Threats → mitigations

| Threat | Mitigation |
|--------|-----------|
| T1 opens the page on a shared screen | Authentication, short idle timeout, photos blurred until clicked, step-up re-login for export and delete a "hide screen" button, F22, D-48) |
| T2 reaches the port | LAN only, one published port, rate-limited login, strict CSP, no open sign-up |
| T3 reads outside their space | Every view reads through the visibility check; isolation tests across non-members, spaces, pause and the private flag (N1) |
| T5 reads a pulled disk | Data encrypted at rest; the key file lives outside the data directory and the app refuses to start otherwise (N2) |
| T6 sends a hostile backup | Header bounds, Zip-Slip validation, size and entry caps, streaming, clean rejection (N9) |
| T7 modifies stored uploads | SHA-256 recorded and re-verified; AEAD fails on a flipped byte; uploads are immutable (N6) |
| T8 steals a password | Argon2id hashing, 30-minute idle and 12-hour absolute sessions, sign-in rate limiting, step-up for sensitive actions, password change signs out other devices (M2b). Optional two-step sign-in for admins: TOTP with recovery codes (D-49, R17, R33) |
| Admin misuse | Hash-chained admin log of every view, search, edit, restore and export (N5) |
| Logs leak content | Logs carry ids, field names and counts only; a log-scrape test (N4) |
| A bad export corrupts the phone | The writer is proven on an emulator (N8, D-43): wipe first restores it exactly. The file is read back and checked before it is offered, the page shows what changed and requires a "fresh phone backup first" confirmation, and originals are never changed |
| An export leaks | Step-up re-login, built per request and never kept on the server, `no-store`, sealed under a password the person chooses, counts-only log line (R28) |
| The admin edits or deletes someone's data | Every look and change is an admin log line (table, row, field names), changes need step-up, the person sees and can undo it in their history, deletions are restorable for 10 and 45 days (R29) |
| Third parties exposed | Partner names hidden in shared views by default |

## Residual risks

> The risks knowingly accepted. Naming them is the point.

- **R1 — Admin or host root reads everything.** Accepted (D-3). Reduced by the admin log and by running it
  on a host you control.
- **R2 — Shared content cannot be unseen or kept from a screenshot.** Revoking stops future viewing only.
  The UI says so.
- **R3 — The data key is a single root.** Lose the key file and every upload and edit is unreadable. Mitigated
  by a second copy proved by opening it (CRITICAL_ASSETS) and an instance backup.
- **R4 — Photo location metadata is kept** (a deliberate feature choice). A location can reveal a home address.
  Mitigated by showing a photo's location only when its owner enables it, and a visible marker on photos that carry one.
- **R5 — A restore overwrites newer phone-side edits to the same entries**, and deletions on the server do not
  reach the phone. Mitigated by the export page (it says which upload the file is based on and what changed, and requires a confirmation that a fresh phone backup was taken first) and by keeping originals. A data-loss risk,
  not a disclosure one.
- **R6 — Tryst schema drift** can produce an export the app rejects or misreads. Mitigated by generic
  pass-through of unknown columns, refusing newer schemas, and a compatibility table tested on an emulator.
- **R7 — The key is on the same host** as the data unless the operator separates them. A key file on the
  same disk as the data protects nothing against a pulled disk. The runbook says where to put it.
- **R8 — The admin log can be rewritten by someone who can write the database.** The chain makes tampering
  detectable if the head is anchored elsewhere (exported or notified); it does not prevent it.
- **R9 — Hints and notes are free text** a member may fill with something sensitive. Logs never contain them.
- **R10 — Invite links.** An invite is a bearer token. Anyone who sees the link before it is used can redeem
  it. Mitigated by single use, short expiry (default 7 days), revocation, hashed storage, redemption rate
  limits, and joins that still need the space owner's approval. The admin log records issue, use and revoke.
- **R11 — Profile fields are sensitive.** Sex, gender, birth date, ethnicity, body type and orientation can be
  imported from a Tryst profile. Mitigated by importing only display name and photo by default, a separate
  share setting per field (sensitive fields are never imported unless chosen), and showing others only what is shared.

## Updates from the design pass (2026-11-12)

- **R5 revised (S1, observed).** Wipe-first restore replaces everything exactly and loses any phone entry newer than the backup. Merge restore deletes every partner link not in the backup (it also leaves server-side deletions on the phone), so Alcove never recommends it. Both are data-loss risks, not disclosure ones. Mitigated by the "fresh phone backup first" confirmation, the diff, the guided wipe procedure and keeping every original upload.
- **R7 revised.** With the unattended default the master key is on the host. A key file on the same disk as the data
  protects nothing against a pulled disk; the runbook puts it in its own folder owned by the container user, with copies on separate media (the Unraid flash drive and one off the machine). The container cannot read the root-only flash drive, so the running copy shares the array with the data: an attacker with the whole array gets both, which is why the data key and disk-level protection of the host still matter.
- **R12 — Search terms in the admin log.** Recording what the admin searched for would put content in the log
  (O-12). The default records scope, result count and term length only. The trade-off is less accountability.
- **R13 — Everyone in a space sees everything, by design (D-21).** A new member sees all existing entries at once, so the owner approves each join (a stricter all-members mode comes later) and the join screen says what will be shared. A member who regrets it can pause or leave, but what was seen cannot be unseen. Third parties named in entries are protected only by partner names being hidden by default.

## Riskiest assumption

That the household's real adversary is a curious person on the LAN and not the admin. If a member ever needs privacy
from the admin, D-3 must be revisited (per-owner keys, browser-side decryption), which is a large change.
- **R14 — Plain HTTP on the LAN (D-11, D-22).** Phone uploads and downloads, passwords and viewed content cross the
  home network unencrypted, so anyone able to sniff that network (a compromised device, a guest on an open wifi) can read
  them (T2). The default accepts this on a secured, password-protected wifi. Options in D-23: reverse-proxy mode (Caddy, Nginx Proxy Manager or `tailscale serve`) for real HTTPS, allowed networks (private LAN ranges by default), an insecure-connection banner and a require-TLS mode. Cookies are `HttpOnly` and `SameSite=Strict` with CSRF tokens in every mode. The
  README and runbook say so plainly. Uploaded files are still encrypted under the member's backup password, but the
  decrypted views are not.
- **R16 — First-run claim by a device on the LAN (D-27).** Until the first administrator exists, anyone who can reach the
  server could race to create it. The setup page therefore needs a one-time code that exists only in the container log, so
  claiming the server needs both network access and log access (the operator). The code is logged on purpose at the Warn
  level, once per start while there are no accounts, and is cleared when the account is made. Anyone who can read that
  log can already read far more, so this adds no new reader.
- **R17 — Password guessing (D-27, D-49).** A device on the LAN can guess passwords. Argon2id makes each
  guess cost about 100 to 200 ms and 64 MiB, a per-address limit allows ten tries and then one every 20 seconds, and a
  password must be at least 10 characters. There is deliberately **no per-account lockout**, because that would let anyone
  on the wifi lock the admin out. A distributed guesser on many addresses is slowed, not stopped. An administrator can turn on two-step sign-in (D-49, R33), which makes a stolen or guessed password not enough; it is optional, so a long passphrase is still the control for anyone who leaves it off, and the admin log records every admin sign-in. A lost password is
  recoverable through the break-glass reset (R21).
- **R18 — Sessions end on restart (D-27).** They live in memory by design (nothing to steal from disk, nothing to
  purge), so a container restart signs everyone out. Accepted: a phone user signs in again.
- **R19 — A backup folder holds all the data, in ciphertext (D-28).** Anyone who copies it and also gets the key file reads
  everything, so the key file and the backup folder must not live together (the compose file mounts them apart, and the key
  lives in its own folder, with a copy on the Unraid flash drive). A backup is useless without the key, which is also why the key's second copy
  is proved with `drill` before the first real upload. Backups older than a purge still contain the purged user's wrapped data key
  (R15) until retention removes them.
- **R20 — Backup passwords are stored, and a hostile upload is read in memory (D-29).** A remembered Tryst backup password is sealed under
  the owner's data key, which the master key unwraps, so the host operator and the admin can open it, as they can open everything else
  (R1). A member who wants less can untick "remember": rebuild, edit and export then ask again. A hostile upload is bounded by the reader
  (header iteration cap, ZIP and size limits, 512 MiB), at most two are decrypted at once, and a refused one is deleted and recorded
  nowhere, so the cost of a bad upload is time and a few hundred MiB of memory for a moment, not stored data.
- **R21 — The break-glass reset and a shared data folder (D-30).** `alcove reset-password` needs the data folder and the key file, so
  anyone who has those could already read everything (R1); it adds no new reader. It is recorded in the admin log by the system, forces
  a change at the next sign-in, and is refused while the server runs (a lock on the data folder), so it cannot corrupt a live store. A
  temporary password given to a new person is known to the administrator until the person changes it, which the forced change keeps
  short; it is shown once, never stored in the clear and never logged.
- **R24 — Profile fields are sensitive and easy to over-share (D-33).** Sex, gender, orientation, ethnicity, location and a birth date are
  among the most identifying things a person can show. Mitigations: everything is private until ticked per field, importing never ticks it,
  the birth date is shown only as an age and must be an adult's, cards go through `share.Cards` (membership, pause, consent), values are
  escaped on output, and a person writes only their own rows. A space member who has seen a card cannot unsee it (R23).
- **R32 — The administrator's search reads past the sharing gate (D-46).** It is meant to, but the one package that must never leak (`share`, D-32) must not gain an "everyone" mode. Mitigations: the search is its own package with one caller; it is administrators only and a POST; every search is logged in the same transaction by length and count, never the words; opening a result is logged by the existing person-data view; refused terms read nothing; the words reach the engine only as quoted strings. Accepted: the administrator can find anything current by its words, which is the trust the whole design states; deleted entries are not searchable.
- **R31 — Thumbnails decode untrusted images (D-44).** Making a small picture means decoding a file someone uploaded. Mitigations: the size is read from the header and anything over 50 million pixels is refused before allocation; at most two decodes run at once; the stored picture is read up to a fixed bound; a failure sends the picture instead and logs only the owner id; the thumbnail is drawn from pixels so it carries no metadata; thumbnails live only in a size and age bounded memory cache, are read only after the same gate as the picture, and are never written to disk. Accepted: a thumbnail of a deleted or hidden photo can sit in memory for up to ten minutes after the gate closes (it cannot be served, the gate runs first) and a restart clears it.
- **R27 — A photo carries its own secrets (D-39).** A photo file can hold its GPS position, camera, time and a thumbnail even when the owner keeps the place off. Mitigations: everything but the picture is stripped before any non-owner receives the bytes (JPEG and PNG, tested against EXIF, XMP, IPTC, maker notes, comments, trailing data and malformed files); other formats are not shared; the format is read from the bytes; photos are encrypted at rest under the owner's key, served `no-store` with `nosniff` and a sandboxing CSP, and visible only where their entry is; the place is its own choice, off by default. The owner's own copy is untouched. Residual: a picture's content is itself identifying, and what a viewer has seen cannot be unseen.
- **R26 — Search can reveal what a view hides (D-38).** A search that matched hidden text would confirm it exists even if the page never shows it. Mitigations: the space search is built on the same `scope` as the list plus the owner's note choice, so a hidden entry or a note kept out of the space never matches; partner names are not searched in a space; the index is read only through `share`; the term is a quoted string (no operators), sent by POST and never logged; hostile input is tested. The own-data search reads only the searcher's rows. The administrator's search over everything is separate, logged and not built yet.
- **R25 — The entry page shows more than the list (D-37).** An entry's page carries every detail, so it is the likeliest place to leak. Mitigations: it uses the list's own
  gate, narrowed to one owner and entry; the page struct holds resolved text only, never the row; the note and partner names obey the owner's choices; catalog labels are read
  from the entry owner's tables only; every refusal is the same 404; tests close the page in every way the list is closed and check that content hidden by choice is absent
  from the struct and the page. Photos, whose location is sensitive, stay out until the photo milestone.
- **R23 — One read gate for shared entries (D-32).** Every view of another person's entries is `share.Entries`; a new view that reads
  `cache_row` for someone else by another route would leak. Mitigations: the gate asks `spaces.Role` first, reads live membership on every
  request (nothing is copied, so leave, removal, pause, disable and delete are instant), requires the current consent wording of both the
  viewer and the owner, honours the private flag and include-notes, and has tests that remove each condition in turn. Review rule: no code
  outside `internal/share` selects another owner's rows. What a viewer has already seen cannot be unseen, and the administrator sees every
  entry through their own logged views (R1).
- **R22 — Invites and codes are bearer secrets (D-31).** Whoever holds a live invite can use it once, and whoever holds a space code can ask
  to join (the owner still approves). Both travel by a channel Alcove does not control, so a code sent over a shared chat is as private as
  that chat. Mitigations: 80 bits, one use, 1 to 30 days, revocable, stored only as a hash, shown once, rate-limited, and every outcome
  looks the same to a guesser. A leaked invite is visible to its owner (state "used") and cancelled with one tap; a leaked space code is
  replaced by making a new one. The administrator can see every space, and each look is in the admin log (R1).
- **R15 — A purge is complete only after the backups that predate it age out (D-26).** Destroying a user's wrapped data key makes their
  photos unreadable on the live instance. Older sealed generations and instance backups still contain that key, so they can still
  open the blobs they also contain. Mitigated by retention on instance backups and by saying so plainly in the runbook.
- **R28 — An export is the person's whole history in one file (D-43).** The file is sealed under a password the person picks (the same as the upload's by default, or a new one of at least 8 characters), built for one request, never stored on the server, sent `no-store`, and asked for only after a fresh step-up and a ticked confirmation. After that it lives in the phone's Downloads and the browser's download list, protected only by its password and Tryst's key derivation (600,000 PBKDF2 rounds), so a short password is the weak point; the page says so and there is no recovery. The admin log records only that an export happened and four counts. Not in scope: the administrator exporting someone else's data (not built, so there is no new reader).
- **R29 — The administrator can edit and delete anyone's entries (D-42 slice 4).** Accepted with R1, and bounded: every page that shows a person's data writes `data.view` before it renders, every change writes `data.edit`, `data.revert`, `data.delete` or `data.restore` in the same transaction as the change (so neither exists without the other), the log names the table, `owner:row` and field names and nothing else, changes need a fresh step-up, and the person sees "The administrator" in the entry's history and can undo it. A deletion can be restored by the person for 10 days and by the administrator for 45, then the stored values are wiped. Since D-52 the administrator also sees the person's photos, profile picture and partners' pictures through the person pages (cleaned of metadata, each opening logged as `data.view` and shown on the person's activity page, D-51), and still cannot add entries for someone.
- **R30 — Restoring is still a manual, destructive step on the phone (D-15, D-43).** Alcove cannot see or undo what the phone does with a file. The proven path is Tryst's "Replace my current data with the backup" (exact on Tryst 0.6.5 in two start states); merge keeps deleted entries and their photos and drops partner links, so the page tells people never to switch it off. Anything added on the phone after the base upload is lost by a wipe restore unless a fresh phone backup was taken first, which is why the confirmation exists. The proof is on an emulator with synthetic data; a real phone, real photos and other Tryst versions are not covered, and the check must be repeated for each Tryst release (docs/RELEASE_CHECKS.md).
- **R33 — Two-step sign-in and locking yourself out (D-49).** The second factor is a TOTP secret kept in the sealed database, so anyone who can read the database already reads everything (R1) and the secret adds no new reader; it never reaches a log or an error. What is new is the risk of the owner losing the phone. The way back, in order: a one-time recovery code; another administrator clearing it from People (step-up, logged `user.totp.reset`); `alcove reset-2fa -user NAME` with the server stopped (needs the data folder and the key, logged `user.totp.reset.cli`). A person with the password and no phone and no recovery codes, and no other admin and no server access, is locked out by design. Wrong codes are limited per account (five, then a ten-minute lock) but only after a correct password, so a stranger cannot use the lock to keep the owner out (D-27). A code cannot be replayed. Turning it on is only saved after a correct code, so a typo cannot enable a factor nobody has. Accepted: step-up stays password-only; a session hijacked after both steps keeps working until it ends.
- **R34 — The member activity page is a window onto the admin log (D-51).** When an administrator switches it on, a person reads the log lines that name them as the target and were made by someone else. Mitigations: it is a read of the existing log, so it holds nothing the log does not (ids, table names, field names, never values); one query (`adminlog.About`) filters by the person's own id as an exact or `id:` prefix match, so one person's lines cannot appear on another's page (tested with ids that start alike); it is a member page, so a signed-out visitor gets the sign-in redirect and everyone gets a 404 while it is off; a test fails if a new admin action is neither listed on the page nor on the hidden list with a reason. Accepted: it is not complete (an administrator's search is logged by shape, not per person, D-46, and the page says so); it shows that an administrator looked, which is its purpose; a person's own account being created shows field names such as `password` (names only); the administrator can switch it off, but that change is itself a log line.
- **R35 — A partner's picture is shown to the space even when the partner's name is hidden (D-52, planned).** The owner's rule is that photos are open to anyone in the space, so a partner's avatar follows "include photos", not "show partner names". The partner is a third party who never agreed to appear and a face identifies them more than a name. Mitigations: the owner controls it per space (turn off include photos) and by not importing the picture; the pages and the join screen say it plainly; metadata is stripped, the picture is blurred until tapped, served `no-store`, and only members with current consent, never a non-member or another space, can open it. Accepted: members of the space can see a partner's face; administrators can see it anywhere, and each look is logged.
