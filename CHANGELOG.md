# Changelog

All notable changes to Alcove are recorded here.
Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) · Versioning: [SemVer](https://semver.org/).

> The version is never hand-written into a tag. The tag is the last step of a release script.

## [Unreleased]

### Added
- M3 piece B, upload and revisions (D-29, F7 to F10, F30): the My data page and a streamed upload route (CSRF token checked before the file, a longer read deadline for that one route, a fast 413), `internal/ingest` (the file is stored verbatim, then judged, then recorded in one transaction with its revision, row cache, search index and sealed password; any refusal records nothing), duplicate refusal by SHA-256, a newer Tryst schema kept read-only, a rebuild of the cache and search from the stored upload (admin button, logged), a progress bar (the one same-origin script; the CSP gains `script-src` and `connect-src` for `'self'` only), `alcove test-backup` for synthetic backups, and an upload, refusals and rebuild flow in the end-to-end smoke. Schema v3.
- M3 piece A, instance backup and restore drill (D-28, F23): one archive of ciphertext per backup with a hashed index and a checksum file, taken consistently while the server runs and never overwriting; an admin Backups page with background jobs for back up, verify and restore drill, and retention; `alcove backup-verify`, `restore` and `drill` for use with no server, including proving a key copy by restoring a real backup with it; `RUNBOOK.md`. The plaintext guard now also covers backups, and the whole flow is smoke-tested end to end on the real image in CI and the live smoke.
- M2b accounts, admin log and UI shell: first-run setup with a one-time code from the server log, Argon2id sign-in, sessions with step-up, a hash-chained admin log with a viewer and a verify button, the mobile-first light/dark/auto UI with the Alcove mark, strict CSP and CSRF, the network gate (allowed networks, proxy mode, require-TLS, a 60-second self-reverting change) and its environment overrides, and the web unlock page for a passphrase-wrapped key (D-27, schema v2). Smoke checks in CI and `scripts/smoke-live.sh` cover first run, the headers and the gate on the real image.
- M2a storage foundation: the master key file (`alcove keygen`, `alcove keycheck`, optional passphrase-wrapped form), the sealed in-memory database with full-text search and per-user data keys (`internal/store`, `internal/seal`), the blob store, startup that refuses to run with the key inside the data directory, a missing or wrong key, or a scratch directory that is not tmpfs, and the armed `no-plaintext` guard. Smoke checks in CI and `scripts/smoke-live.sh` cover the real image.
- S1 round-trip spike (emulator, Tryst 0.6.5, schema v19): wipe-first restore is exact, merge restore deletes partner links, ZIP entries must be DEFLATED. Findings in the D-15 addendum; synthetic generator now writes DEFLATED media entries (regression test).
- The Alcove logo: `docs/img/alcove-logo.png` (master), `deploy/icon.png` (512 px, for Unraid and the README).
- M1 Tryst reader: `internal/trystbk` (header, bounded decrypt, Zip-Slip and zip-bomb checks, lossless generic table reader, `Seal` writer, `SchemaStatus`) and the synthetic backup generator `internal/trystbk/synth`. Adapted from Sanctum's reader (MIT notice kept in `internal/trystbk/NOTICE`). First third-party dependencies: tink-go and x/crypto.
- A guard that fails when a file that `.gitignore` excludes is tracked (it caught a build artifact force-added by mistake in the kickoff commit; the file is untracked now but remains in that commit's history).
- M0 container and CI: Dockerfile (distroless, non-root, digest-pinned), compose file, `.env.example`, CI and release workflows, release and live smoke scripts, and a dependency and licence gate (`scripts/dependencies.allow`).
- Project kickoff: AGPL-3.0 license, hard constraints and size class in `CLAUDE.md`, scaffold server with
  `/healthz`, bootstrap script, Makefile and constraint guards, and the seeded design documents
  (requirements, architecture, threat model, data model, roadmap, decision log).
