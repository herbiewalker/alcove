# Audit pass: authorization and isolation (who can see or change what)

- **Date:** 2026-11-21
- **Lens:** authorization and isolation (single lens; fresh-eyes auditor, did not write the code)
- **Tree:** `main` at `ced8631` (D-51 activity page, D-52 profile and partner pictures), clean working tree
- **Verified by running:** `go test ./...` all packages ok; `scripts/guards.sh all` all six guards OK; one throwaway probe (deleted, not committed)

## Scope read
`internal/spaces` (Role, membership, invites, join), `internal/share` (scope, check, Entries, Days, Roster, Cards, EntryDetail,
OpenPhoto, Gallery, SearchSpace/Own, avatar.go), every route in `internal/server/server.go` and its handler, the wrapper
(level, CSRF, Sec-Fetch-Site), `identify`, two-step sign-in (`/login`, `/login/code`, pending tokens), step-up, admin account
actions, upload delete/restore/download ownership, admin person pages and picture/photo handlers, `adminlog.About` and the
activity page, `adminsearch`.

Grep of every SQL read of `cache_row`, `media_blob`, `avatar`, `profile`, `entry_flag` outside `internal/share`: only
`edit`, `ingest`, `profile`, `retention`, `adminsearch`, `server/export.go` and `server/health.go`. All but the admin search
and health count filter by the caller's own `owner_id`; `adminsearch` is the documented, logged admin path. No leak found.

## Findings

| # | Severity | Where | Issue | Fix |
|---|----------|-------|-------|-----|
| 1 | should-fix | `internal/server/edit.go:278-300` (`postEditEntry` error re-render), `:407` (`revertEdit` error re-render) | Constraint 6 says every admin view is logged. These re-render paths read the owner's data and show it with **no `data.view` line**. Probe: as admin, `POST /admin/people/{sam}/entries/{id}/edit` with a bogus `rev` returned 400 with a 14 KB form (current set selections via `tokenSet(cur[name])` plus the owner's catalog labels) and added **0** admin-log lines, without any earlier GET. The revert path renders the history page (old and new field values, including notes) when the revision is stale. A step-up password is needed, so this is an accountability gap, not an access gap. | Call `s.auditView(q, w, "edit"/"history", ...)` before rendering on these error paths (only when `w.admin`), or build the error form without current values. |
| 2 | should-fix | `internal/server/accounts.go:386-397` (restore upload) and `:408-414` (purge) | The data change commits in `retention` first and the admin-log line is written in a **separate** transaction afterwards. If the log write fails, the restore or purge has happened unlogged. Every other admin write (user actions, space delete/restore, invites, data edits) logs in the same transaction. | Let `RestoreUpload` and `Purge` accept a callback run inside their transaction (as `q.audit(..., func(tx))` already does for settings), or write the log line first. |
| 3 | nit | `internal/server/spaces.go:150-180` (`spacePage`) | A member on older consent wording gets no cards but still sees the member list (names, usernames, joined dates). The design text says they "see nobody else's entries"; names are not entries, so this may be intended. | Decide and document in D-32, or hide the list until they agree. |
| 4 | nit (design note) | `internal/share/share.go:90-100` (`check`) | A **paused** viewer still sees everyone else's entries and pictures (only their own are hidden). Consistent with the code comments and D-32 point 1, which says pause hides the member's own. Stated here so the owner confirms it is intended. | None if intended; else require `paused = 0` in `check`. |

No blockers.

## What held up (adversarial attempts that failed)
- Outsider, other-space member, left, removed, requested, disabled, deleted-account and signed-out visitors: every `/spaces/{id}/...`
  route calls `s.gate` (Role) first and answers the same 404; `postSpaceAction` checks Role before the step-up redirect and again
  inside the writing transaction; owner-only actions are re-checked in the transaction.
- Owner ids and media/partner ids from the URL are always combined with the space scope in SQL (`c.owner_id = ?` under `scope`,
  `m.space_id = ?`), so `/spaces/{A}/photo/{owner-in-B}/...` and entry or avatar IDOR return `ErrNoEntry`. `OpenPartnerPhoto` goes
  through an entry the space already shows, so a private or deleted entry hides the partner picture; names are not part of the test (R35).
- Paused, older-wording, private-flagged, soft-deleted (row removed from `cache_row`), disabled/deleted-account members drop out
  on the next request: all conditions live in `scope`, `OpenProfilePhoto`, `Roster`.
- New D-52 gates: profile picture requires active + unpaused + current wording + `include_photos` + live space/user + a cleanable
  format; thumbnails and strip-for-non-owner only after the gate; `/avatar/*` and `/photos/*` read only the caller's own rows.
- Admin pages: `forPerson` is admin-level; every GET view, edit, delete, restore, revert, partner, photo and picture look logs
  (`data.view`, `data.edit` ...) with ids and field names only; writes log in the same transaction; POSTs need step-up and CSRF.
- Activity page: `adminlog.About` requires `actor != person` and an exact or `id:` prefix target match on fixed-alphabet ids, filters by a
  vocabulary map enforced by a test, and returns fixed sentences plus field names, never values. 404 while the switch is off.
- Auth: `/login/code` accepts only an in-memory single-use pending token (path-scoped HttpOnly cookie), CSRF via the pre-session token,
  rate limited, lockout logged; a password alone never issues a session for a two-step account; `identify` re-reads status and role
  on every request; disable, delete, reset and two-step reset revoke sessions; password change revokes other sessions.
- Constraint 11: only `/setup` (first run, one-time code, only while no accounts) and `/join/accept` (a live single-use server invite,
  spent in the same transaction) create accounts; space codes need an existing account; covered by `TestNoUnauthenticatedRouteCreatesAnAccount`.
- Upload list, delete, restore and download are scoped to `q.id.user.ID`; the admin restore uses the 45-day window.

## Verdict
This lens covered the one visibility gate, every route's access level and path-id handling, admin powers and logging, the D-51
and D-52 additions, soft-delete ownership, joins and sign-in bypass paths, by reading the code and running the suite and guards.
It cannot cover: a live multi-user click-through on real data, timing and side-channel differences between 404 cases, SQLite
file-level access (crypto/storage lens), log content (secrets/logging lens), injection and template escaping (input lens),
and whether the design choices in 3 and 4 match the owner's intent. **Blockers outstanding: none.** Two should-fixes (1 and 2)
are accountability gaps in constraint 6; recommend fixing them before saying the audit passed, then a short delta-audit.

Off-lens notes: none worth chasing from this pass.

## Resolution (same day, uncommitted)

- **Finding 1 fixed.** `postEditEntry` and `revertEdit` now call `auditView` before rendering their error pages
  (`internal/server/edit.go`). Regression test `TestAdminErrorPagesThatShowDataAreLogged`; it fails without the fix.
- **Finding 2 fixed.** `retention.RestoreUpload` takes optional in-transaction hooks; the admin handler writes
  `upload.restore` through one, so a log failure undoes the restore (`internal/retention/retention.go`, `internal/server/accounts.go`).
  The manual purge now logs `retention.purge.run` (fields `manual`) **before** purging, so it can never run unlogged; the purge still
  writes its own `retention.purge` line with the counts in its own transaction. Rollback test added in `retention_test.go`.
  Side effect: the `purge.run` line no longer carries the counts (they are on the `retention.purge` line).
- Re-verified: `go vet`, `gofmt`, `go test ./...` and `scripts/guards.sh all` pass. Findings 3 and 4 remain open for the owner's decision.

## Delta-audit of the fixes (same day)

Range: uncommitted changes to `retention.go`, `accounts.go`, `edit.go` and their tests, on top of `ced8631`.
Lens: authorization and isolation, constraint 6 (every admin look and change logged, same transaction).

| Check | Result |
|---|---|
| Restore hook runs inside the restore transaction; a hook error rolls it back | OK (`Store.Update` rolls back on any error; rollback test passes) |
| Restore hook cannot be used by non-admins | OK (only `postAdminRestoreUpload` passes one; the owner path passes none) |
| Cache rebuild after commit fails | OK: restore and its log line are already committed together, so nothing is unlogged (before the fix this case was unlogged) |
| Manual purge logged before any deletion; purge's own system line kept | OK; vocabulary map in `activity.go` already lists `retention.purge.run` as hidden |
| Every other admin error path that re-renders stored data | OK: `postEditPartner` re-renders only posted values, `postDeleteEntry`/`postRestoreEntry` show a message only, new-entry paths are owner-only (`forOwn`), `revertEdit` is shared by entries and partners and now logs both |
| Tests | `go test -count=1` on retention, server, adminlog pass; full suite and `scripts/guards.sh all` passed after the fix |

New findings: none that need action. Two nits, both accepted behaviour:
- A restore of an upload that is already restored is a no-op and writes no log line (nothing was changed or shown).
- A refused admin edit now logs `data.view` with field `edit`, so the person's activity page says "opened the edit page"; accurate, since the page did show their data.

Verdict: findings 1 and 2 are resolved; no blockers from this lens. Findings 3 and 4 remain open for the owner.
