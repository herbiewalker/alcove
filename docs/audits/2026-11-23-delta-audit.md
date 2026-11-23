# Audit pass: delta-audit of the whole audit round

- **Date:** 2026-11-23
- **Lens:** delta-audit: only what changed after the audited tree
- **Range:** `ced8631..527dd10` (everything since the tree the first lens audited; 60 files, +849 / -154). That is the five audit-fix commits (`f564221` to `a9c275a`), the compose repair `92ad3f7`, and the four merged Dependabot bumps (`7245683`, `cde1496`, `51fa5b4`, `527dd10`). Documentation and audit logs were skimmed only for claims that the code does not back.
- **Verified by:** reading the full Go diff (non-test); reading the template, CSS and script diffs; resolving the four new action pins against their release tags through the GitHub API; reading the `actions/attest` v4 README for its permission needs; CI is green on `527dd10` (both jobs). Docker, a browser and a screen reader are not available here.

## Scope read
`internal/server` (accounts, compare, edit, entries, handlers, server, spaces), `internal/share`, `internal/retention`, `internal/ingest`, `internal/store`, `internal/trystbk`, `internal/auth`, the templates, `app.css`, `ui.js`, `.github/workflows/*`, `.github/dependabot.yml`, `.gitleaks.toml`, `docker-compose.yml`, `deploy/unraid-template.xml`, `scripts/guards.sh`.

## Findings

| # | Severity | Where | Issue | Fix |
|---|----------|-------|-------|-----|
| 1 | should-fix | `.github/workflows/release.yml` (attest step, `actions/attest-build-provenance` now v4.2.2) | v4 is a wrapper over `actions/attest`, which with `push-to-registry: true` also writes an artifact-metadata storage record. Its README asks for `artifact-metadata: write` and says storage records work only for organization-owned repositories. This repository is owned by a personal account and the job has no `artifact-metadata` permission. The step runs after the image is tagged, so if it fails the release stops with the tags already live (the workflow's own comment warns about this order). Not run: this only shows on a real release. | Add `create-storage-record: false` to that step (stable behaviour on a personal repository, no new permission). Then watch the first release run. |
| 2 | should-fix | `internal/ingest/ingest.go:265-270` (`read`), docs in `SETUP.md:32`, `.env.example:50`, `THREAT_MODEL.md:123` | The one-slot semaphore is released when `read` returns, but the decrypted body stays in memory while the caller parses and stores it (`ingest.go:190`, `:480`, `export.go:79`). So one upload can be in its processing phase while a second is decrypting: two bodies at once. The text "one at a time, size the memory limit above one backup plus 1 GiB" is true of the decrypt step only. With the 2 GiB default it still fits (about 1.1 GiB peak), so this is a wrong statement and a thin margin, not a live failure. | Either hold the slot until the caller is done (return a release function from `read`, defer it in the three callers), or say "up to two" in the three documents. The first makes the documents true. |
| 3 | nit | `.github/dependabot.yml` comment, process | The file says a pin moves "only when someone reads and merges" the pull request. Four major bumps (buildx 3 to 4, login 3 to 4, build-push 6 to 7, attest 2 to 4) were merged on green CI alone, and CI does not run the release job. Pins are correct (all four SHAs match their release tags) and inputs the workflow uses did not change, but finding 1 shows what a release-only path can hide. | Before the next tag, read the release notes of the four actions, and treat the first release run as the real check. A dry-run release workflow on a branch would close this gap for good. |

No blockers.

## What held up (checked in the diff)
- **Admin log atomicity:** `RestoreUpload` runs the log hook inside the restore transaction, so a restore cannot succeed unlogged and a failed log undoes it. `postAdminPurge` logs who asked before the purge, and `Purge` writes its own `retention.purge` line with the counts. `q.audit` and the hook use the same `adminlog.Append`, same actor.
- **Edit and history error paths:** the admin's look is logged before the error re-render in both places; for a member `auditView` is a no-op.
- **Space consent:** `NeedAgree` clears `Members` and `Requests`, and the template shows no member sections in that state.
- **Compare:** the new `Query.Owner` filter is added to the same `WHERE` that every gate uses, args stay in order, total is counted with the filter, and the cut is announced. Each per-member read still goes through `share.Entries`, so no metric can count an entry the list would not show.
- **Display names:** `CleanDisplayName` is the only writer of `display_name` (`Create` and `SetDisplayName`; no other `INSERT` or `UPDATE` touches the column), and both callers handle `ErrBadDisplayName`. Existing names are not rewritten.
- **Snapshots:** the sweep runs after the single-instance lock is taken on open, and before the lock is released on close, so it cannot remove another process's file. It matches only `snapshot.*.sqlite` inside the store's own tmpfs directory.
- **Reader:** the first segment is read alone, so a wrong password fails before anything large is reserved; the reservation is `min(file size, limit)` plus slack, so a hostile file cannot ask for more than the limit; no behaviour change on a reader without `Stat`.
- **Panic log:** logs the type only.
- **Pins:** `buildx f87e599` = v4.4.1, `login 8aa5831` = v4.8.0, `build-push c3c9e26` = v7.4.0, `attest 4d10147` = v4.2.2, each resolved from the release tag. The inputs `release.yml` uses (`provenance`, `sbom`, `outputs`, `subject-*`, `push-to-registry`) still exist, and the job keeps `attestations: write` and `id-token: write`.
- **Compose:** `mem_limit`, `memswap_limit` and `deploy.resources.limits.pids` are accepted by `docker compose config` (CI image job green on three commits); guards, template and CI assertions agree.
- **Accessibility changes:** every aria-label field already existed in the same template scope; `inert` is cleared on return; the script's new variables are declared before use; no inline styles or handlers were added (CSP unchanged); a source-level test now guards the template rules.
- **Tests and gates:** `go test ./...` and `scripts/guards.sh all` pass; CI is green on `527dd10`.

## Verdict
- **Covered:** every changed Go file, template, stylesheet and script, the workflow and compose changes, and the four new action pins.
- **Cannot cover without more tooling:** a real release run (finding 1 and the new action majors), Docker locally, a real browser or screen reader for the script and layout changes.
- **Blockers:** none. Two should-fix items: add `create-storage-record: false` before the next tag, and make the one-at-a-time memory claim true (or true to the documents).

## Resolution (same day)
- **1 fixed:** `create-storage-record: false` on the attestation step (the input exists in v4.2.2's `action.yml`), with a comment saying why.
- **2 fixed:** `read` now returns a release function and holds the slot until the caller has closed the backup (`Upload`, `Rebuild` and `Export` each `defer release()` before `defer bk.Close()`). `TestOnlyOneBackupIsHeldInMemoryAtATime` shows a second read waits while the first is held, that releasing twice is harmless, and that a refused read gives the slot back. The three documents now say what the code does.
- **3 addressed:** the `dependabot.yml` comment now says CI does not cover the release job, and `docs/RELEASE_CHECKS.md` has a short checklist for the release run after a bump to an action `release.yml` uses. A dry-run release workflow would close the gap for good; not done, it needs a decision about what it may push.
- **Still open:** the first real release run is the check for findings 1 and 3.
