<div align="center">

<img src="docs/img/alcove-banner.png" alt="Alcove" width="100%">

# Alcove

### A private, self-hosted space for couples to bring their Tryst backups together

![License](https://img.shields.io/badge/license-AGPL--3.0-blue)
![Platform](https://img.shields.io/badge/platform-Docker%20%2F%20Unraid-0969da)
![LAN only](https://img.shields.io/badge/network-LAN_only-2ea44f)
![Nothing leaks outside a space](https://img.shields.io/badge/spaces-isolated-2ea44f)

**Status: v0.1.3, built through the editing and export milestones plus most of the admin console (search, log filters, health, optional two-step sign-in), audited in-house (six lenses plus a delta-audit, all findings fixed), not yet independently audited.** The server runs, starts unattended with its key kept apart from the data, and has accounts, an admin console with a tamper-evident log, uploads of Tryst backups kept as revisions, encrypted instance backups with a tested restore, soft delete with a real purge, and private spaces joined by invite. Members see each other's entries (with private entries and pause honoured), profile cards, a shared calendar and compare views, and photos. You can **edit your entries on a computer** (and the administrator can edit anyone's, always logged) and **export a fresh `.tryst` to restore on your phone**. The export round trip is proven on a Tryst 0.6.5 **emulator** with synthetic data, **not yet on a real phone**. See [what is and is not proven](#what-is-and-is-not-proven).

</div>

---

## What & why

[Tryst](https://github.com/herbiewalker/tryst) keeps private records on one phone, with no cloud and no
network. That is the point, but it makes two things hard: seeing a shared history with a partner, and
fixing entries on something bigger than a phone screen. [Sanctum](https://github.com/herbiewalker/sanctum)
solves the backup half. **Alcove** is the other direction: a small server you run yourself where each
person uploads their backups, edits their entries on a computer, and chooses what to share.

### What you can do

- **Upload** a Tryst backup from your phone's browser over wifi. It is checked by a real trial decrypt, kept exactly as sent, and becomes your current data. Older uploads stay as history.
- **Share in spaces.** Make a space, invite people with a one-time code, and see each other's entries, a calendar overlay, compare views, profile cards and photos. Joining is the act of consent and the join screen says what is shared. You can pause, mark an entry private, or leave at any time.
- **Edit on a computer.** Change an entry's time, length, rating, mood, acts, positions, kinks, settings, toys, occasions or note; rename partners; add, delete and restore entries. Every change is a new version with a history and an undo. Your uploaded backup is never changed.
- **Send it back to the phone.** *Export to Tryst* writes a fresh backup of your current data (edits, additions and deletions included, deleted photos left out), checks it by reading it back, and tells you exactly how to restore it in Tryst.
- **Recover.** Deleted uploads and entries come back for 10 days (you) or 45 days (the administrator), then a purge really removes them.
- **Run it as the administrator.** Add and remove people, back up and restore the whole instance, set transport security, read the hash-chained admin log, and edit anyone's data. Every look and every change is logged by table, row and field name, never by content.

- **Not in scope:** social features, live sync, internet exposure, any change to Tryst. Video waits for Tryst to support it.
- **Promises:** a space is collaborative but **nothing leaves it**, the server is **LAN only**, it makes **no outbound
  calls** by default, data is **encrypted at rest** with the key kept apart from it, and the admin's
  access is total but **always logged**. Each is enforced by a CI check where it can be. Adults only, invite only.

## Getting started

**Run it** (Unraid or any Docker host). The image is `ghcr.io/herbiewalker/alcove`; `:stable` always points at the newest release, and an exact tag such as `0.1.3` pins a version. The short version:

1. Make the master key **once**, in a folder that is not the data folder, and keep a second copy somewhere that is not that machine. Prove the copy before the first real upload.
2. Start the container (the Unraid template in [`deploy/`](deploy/unraid-template.xml) or `docker compose`). It needs one port, a data folder, the key folder and a backup folder on a different disk.
3. Read the one-time setup code from the container log, open the server from a computer on your LAN and create the administrator.

[RUNBOOK.md](RUNBOOK.md) has every step, including the Unraid deploy (section 12), updating (13), editing and exporting back to the phone (14), and recovering from mistakes.

**Develop it.** Prerequisites: Go (the pin is in [SETUP.md](SETUP.md)) and Git for Windows or any POSIX shell.

```powershell
git clone https://github.com/herbiewalker/alcove.git
cd alcove
powershell -NoProfile -ExecutionPolicy Bypass -File .\bootstrap.ps1
```

Expect `BOOTSTRAP GREEN`. The server needs a master key file kept outside its data folder, and it
refuses to start without one:

```powershell
.\bin\alcove.exe keygen -out C:\alcove-dev\keys\alcove.key
$env:ALCOVE_KEY_FILE = "C:\alcove-dev\keys\alcove.key"; $env:ALCOVE_DATA_DIR = "C:\alcove-dev\data"
$env:ALCOVE_TMP_DIR = "C:\alcove-dev\tmp"; $env:ALCOVE_ALLOW_NONTMPFS_TMPDIR = "true"   # dev box only
$env:ALCOVE_ADDR = "127.0.0.1:8080"; .\bin\alcove.exe
```

```bash
curl http://127.0.0.1:8080/healthz
# {"state":"unlocked","status":"ok","version":"dev"}
```

On Linux or in CI, `make check` runs the same gates. `alcove test-backup` writes a synthetic Tryst backup so nothing needs real data.

## What is and is not proven

| Proven (tests, CI, the container smoke) | Proven by hand on an emulator | Not yet proven |
|-----------------------------------------|-------------------------------|----------------|
| Isolation between members, spaces and outsiders; the admin log chain; encryption at rest and the key kept apart; soft delete and purge; the instance backup and restore drill; editing and its rebuild equivalence; the export file reading back identical | An export restored in Tryst 0.6.5 with "Replace my current data" on matches the app's own re-export, table by table, in two start states (synthetic data). Merge mode is unsafe (it keeps deleted entries and drops partner links) | A real phone, real photos and a real backup of yours; Chrome on a real Android phone; other Tryst versions; two-step sign-in with a real authenticator app, the QR codes with a real phone camera and photo thumbnails on real phone photos; an independent security audit (M9); very large backups |

The round trip must be repeated for every Tryst release ([docs/RELEASE_CHECKS.md](docs/RELEASE_CHECKS.md)). Until you have done one yourself on a phone you care about, make a fresh backup in Tryst first and treat the first export as a trial.

## Documentation map

| Doc | What it owns |
|-----|--------------|
| [CLAUDE.md](CLAUDE.md) | the hard constraints, scope and the contract for an AI pair |
| [RUNBOOK.md](RUNBOOK.md) | first start, backups, proving the key copy, restoring, updating, editing and exporting, the Unraid deploy, locked-out recovery |
| [CHANGELOG.md](CHANGELOG.md) | what each release changed and what to do when updating |
| [SETUP.md](SETUP.md) | toolchain pins, environment, platform trap and fix list |
| [docs/REQUIREMENTS.md](docs/REQUIREMENTS.md) | scope, non-goals, requirements and the status of each |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | components and data flows |
| [docs/THREAT_MODEL.md](docs/THREAT_MODEL.md) | assets, adversaries, mitigations, accepted risk |
| [docs/DATA_MODEL.md](docs/DATA_MODEL.md) | entities, revisions, retention and migration rules |
| [docs/ROADMAP.md](docs/ROADMAP.md) | milestones and their exit criteria |
| [docs/DECISIONS.md](docs/DECISIONS.md) | the ADR log and open questions |
| [docs/RELEASE_CHECKS.md](docs/RELEASE_CHECKS.md) | the checks that need an emulator or a phone, run before releases |
| [docs/audits/](docs/audits/) | the audit round: one log per lens, a delta-audit and a summary |
| [CRITICAL_ASSETS.md](CRITICAL_ASSETS.md) | what git deliberately does not hold |

## Known limitations

- **No independent audit yet.** The constraints are enforced by tests and CI checks, and the project ran its own audit round in November 2026 (six single-lens passes and a delta-audit, logs and a summary in [`docs/audits/`](docs/audits/)). Nobody outside the project has reviewed it, and the accessibility work has not yet been tried with a real screen reader.
- **Server-side trust.** The host operator can read the data; there are no per-user keys that hide it from the admin.
- **Plain HTTP on the LAN.** Content crosses your home network unencrypted by default (a secured wifi is assumed). HTTPS is available through a reverse proxy or Tailscale, enabled by the admin (R14, D-23).
- **Manual phone round trip.** Edits reach the phone by exporting a backup and restoring it with "Replace my current data" on, not by sync. Anything added on the phone since the last upload is lost by that restore unless you export a fresh backup from the phone first (the page makes you confirm).
- **A new upload starts over.** Edits made on the previous upload are kept in its history but not carried onto the new one.
- **Not built yet:** notifications (deferred) and the administrator adding an entry on someone's behalf.
- **Tied to Tryst's backup format**, which changes with Tryst. Compatibility is a standing maintenance cost.

## License

AGPL-3.0-or-later, see [LICENSE](LICENSE). Alcove implements Tryst's backup format from
[the published spec](https://github.com/herbiewalker/tryst/blob/main/docs/EXPORT_FORMAT.md) rather than
reusing Tryst's code.
