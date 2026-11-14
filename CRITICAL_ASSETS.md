# Alcove — Critical Assets Inventory

> The stuff git **deliberately doesn't hold**. If the dev machine died today, every repo restores from the
> remote and none of these do. This file inventories them; it **never contains the values themselves**
> (locations and metadata only).
>
> Two rules: (1) every asset has a **second copy** somewhere that isn't this machine, **proved by
> opening it**; (2) anything with an expiry or a pending rotation also gets an `OPEN_LOOPS.md` entry in
> the DevPlaybook.

## Inventory (planned; nothing is deployed yet)

| Asset | Type | Lives at | Second copy | Expires / rotate | Loses what if lost |
|-------|------|----------|-------------|------------------|--------------------|
| **Data key file** | encryption key | a one-line text file on the host **outside** the data directory (see CLAUDE.md constraint 4 and D-26); on Unraid the flash drive, separate from the array | a copy in a password manager or other offline storage, proved by opening it | on suspected exposure (rotation procedure in the runbook) | **every stored backup and edit becomes unreadable. Unrecoverable** |
| **Admin account password** (and TOTP secret + recovery codes, if enabled) | credential | the admin's password manager | recovery codes stored offline | on exposure | admin access. Recoverable: `alcove reset-password` (RUNBOOK section 7) works with the server stopped and needs the data folder and the key file, so it is as safe as the host itself |
| **First-run setup code** | one-time code | the container log, only while there are no accounts | none needed (a new code is made at every start until the first account exists) | cleared when the first account is made | nothing: it is transient. Anyone who can read the log can read it, which is the point |
| **Alcove instance backup** | encrypted snapshot of the data directory | a second disk or machine, not the same array | a second, offline copy | per the backup schedule | edits and revisions made on the server (**Alcove becomes a system of record once it holds edits**) |
| **Members' Tryst backup passwords** | passphrases stored inside the app so it can open uploads | encrypted inside the data store; each member's own password manager | each member's own copy | per member | the ability to open that member's uploaded backups |
| **Notification URL** (optional) | credential (webhook or ntfy topic or token) | the container environment on the host; never in git | password manager | rotate if it leaks | alerts stop, or a stranger can post to the channel |
| `.env` for the deploy | runtime config | on the host, gitignored | regenerate from `.env.example` | on exposure | nothing secret: paths and ports only, by design |

## Notes

- **The data key file is a single root of failure by design.** Sanctum lost a passphrase because its
  inventory claimed a second copy that did not exist. The check below is mandatory before the first real
  upload, not a formality.
- **Alcove is a system of record for edits.** Unlike Sanctum, the phone is no longer the only source of
  truth once edits are made on the server, so the instance backup is load-bearing.
- This file is safe to commit: it holds locations, never values.

## Checks

- [ ] The data key's second copy has been **opened and used**: run `drill -backup <a real backup> -key <the copy>` (RUNBOOK section 3). Matching fingerprints are not enough; restoring a real backup with the copy is the proof.
- [ ] An instance backup has been restored to a scratch location and the data read back (`drill`, or the Restore drill button, both read everything back and delete what they made).
- [ ] Nothing in this file is a secret value.
- [ ] Re-audit at each release: did this release mint a new key, token or artifact that belongs here?
