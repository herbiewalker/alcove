# Alcove — Threat Model

> **Status:** v1 draft (2026-11-12), aligned with D-3 to D-6 and D-10 to D-11. Keep current: a feature that adds
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
| T3 | **A member overreaching** | A valid account trying to see another member's or another space's unshared data | High |
| T4 | **A member who saves or leaks** what was shared | Legitimately saw it; can screenshot or forward | Accepted (R2) |
| T5 | **Offline media** | A pulled or RMA'd disk, a copied backup of the data directory | High |
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
| T1 opens the page on a shared screen | Authentication, short idle timeout, "hide screen" button, photos blurred until clicked, step-up re-login for export and delete |
| T2 reaches the port | LAN only, one published port, rate-limited login, strict CSP, no open sign-up |
| T3 reads unshared data | Every view reads through the sharing check; default is nothing visible; isolation tests across users and spaces (N1) |
| T5 reads a pulled disk | Data encrypted at rest; the key file lives outside the data directory and the app refuses to start otherwise (N2) |
| T6 sends a hostile backup | Header bounds, Zip-Slip validation, size and entry caps, streaming, clean rejection (N9) |
| T7 modifies stored uploads | SHA-256 recorded and re-verified; AEAD fails on a flipped byte; uploads are immutable (N6) |
| T8 steals a password | Optional TOTP for admins, session expiry and rate limits, step-up for sensitive actions |
| Admin misuse | Hash-chained admin log of every view, search, edit, restore and export (N5) |
| Logs leak content | Logs carry ids, field names and counts only; a log-scrape test (N4) |
| A bad export corrupts the phone | Emulator-proven writer, diff and base-revision warning before export, immutable originals kept (N8) |
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
  reach the phone. Mitigated by the diff, the base-revision warning and keeping originals. A data-loss risk,
  not a disclosure one.
- **R6 — Tryst schema drift** can produce an export the app rejects or misreads. Mitigated by generic
  pass-through of unknown columns, refusing newer schemas, and a compatibility table tested on an emulator.
- **R7 — The key is on the same host** as the data unless the operator separates them. A key file on the
  same disk as the data protects nothing against a pulled disk. The runbook says where to put it.
- **R8 — The admin log can be rewritten by someone who can write the database.** The chain makes tampering
  detectable if the head is anchored elsewhere (exported or notified); it does not prevent it.
- **R9 — Hints and notes are free text** a member may fill with something sensitive. Logs never contain them.
