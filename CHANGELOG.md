# Changelog

All notable changes to Alcove are recorded here.
Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) · Versioning: [SemVer](https://semver.org/).

> The version is never hand-written into a tag. The tag is the last step of a release script.

## [Unreleased]

### Added
- M1 Tryst reader: `internal/trystbk` (header, bounded decrypt, Zip-Slip and zip-bomb checks, lossless generic table reader, `Seal` writer, `SchemaStatus`) and the synthetic backup generator `internal/trystbk/synth`. Adapted from Sanctum's reader (MIT notice kept in `internal/trystbk/NOTICE`). First third-party dependencies: tink-go and x/crypto.
- A guard that fails when a file that `.gitignore` excludes is tracked (it caught a build artifact force-added by mistake in the kickoff commit; the file is untracked now but remains in that commit's history).
- M0 container and CI: Dockerfile (distroless, non-root, digest-pinned), compose file, `.env.example`, CI and release workflows, release and live smoke scripts, and a dependency and licence gate (`scripts/dependencies.allow`).
- Project kickoff: AGPL-3.0 license, hard constraints and size class in `CLAUDE.md`, scaffold server with
  `/healthz`, bootstrap script, Makefile and constraint guards, and the seeded design documents
  (requirements, architecture, threat model, data model, roadmap, decision log).
