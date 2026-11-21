# Audit pass: build hardening

- **Date:** 2026-11-21
- **Lens:** build hardening (release configuration, container and compose hardening, CI and release pipeline, artifact contents)
- **Tree:** `main` at `8459b5f` (CI green)
- **Verified by running:** a release-flag build (`CGO_ENABLED=0 GOOS=linux -trimpath -ldflags="-s -w"`) built twice and compared; `go version -m` on the artifact; a search of the binary for personal paths and debug endpoints; `go list -deps` for debug packages. Docker is not installed on this machine, so the image itself was not built here; I read the Dockerfile, compose file and template and rely on the CI image job (green) for the build, the non-root check, the compose exposure checks and the smoke run.

## Scope read
`Dockerfile`, `.dockerignore`, `.gitignore`, `docker-compose.yml`, `.env.example`, `deploy/unraid-template.xml`, `Makefile`, `go.mod`,
`.github/workflows/ci.yml` and `release.yml`, `scripts/release.sh` and `scripts/guards.sh`, the built binary.

## Findings

| # | Severity | Where | Issue | Fix |
|---|----------|-------|-------|-----|
| 1 | should-fix | `.github/workflows/release.yml:68-120` | The release job holds `packages:write`, `contents:write` and `id-token:write`. Its own comment says third-party actions there are pinned to a commit for that reason, and Trivy is, but `docker/setup-buildx-action@v3`, `docker/login-action@v3`, `docker/build-push-action@v6`, `actions/attest-build-provenance@v2`, `actions/checkout@v7` and `actions/setup-go@v7` are movable tags. A moved or hijacked tag would run with the power to push the image people pull as `:stable`. | Pin each to a full commit SHA with the version in a comment (a Dependabot config can keep them current). Same for `ci.yml`, at lower priority. |
| 2 | should-fix | `docker-compose.yml` (limits block), `deploy/unraid-template.xml` `ExtraParams` | Memory is limited (`2G`) but swap is not. With only `--memory`, Docker allows swap up to the same size again, so on a host that has swap the decrypted database, photos and the master key held in RAM or tmpfs can be paged to the host's disk, which is what constraint 4 ("decrypted data only in memory or tmpfs") is meant to prevent. Matters only if the host has swap; Unraid often does not. | Add `memswap_limit: ${ALCOVE_MEM_LIMIT:-2G}` to compose and `--memory-swap=2g` to the template; extend the CI compose check to assert it. |
| 3 | nit | `.github/workflows/ci.yml` (govulncheck step) | `go install golang.org/x/vuln/cmd/govulncheck@latest` runs whatever is newest at that moment. CI only, no secrets in that job, but it is the one unpinned tool left. | Pin a version, bump it deliberately. |
| 4 | nit | `.env.example:50`, `SETUP.md:32`, `docs/THREAT_MODEL.md:123` | Three documents still say up to two backups are decrypted at once and size the memory limit on that. The input audit fix (`8459b5f`) made it one at a time, so the advice is now more conservative than needed. | Say "one at a time" and "keep `ALCOVE_MEM_LIMIT` above this size plus 1 GiB". |
| 5 | nit | `docker-compose.yml`, `deploy/unraid-template.xml` | No process limit. One process serves everything and the image has no shell, so the risk is small, but it is one line of defence in depth. | `pids_limit: 256` and `--pids-limit 256`. |

No blockers.

## What held up
- **Release build:** `-trimpath`, `-s -w`, `CGO_ENABLED=0`, static, version stamped from the tag. Two builds of the same tree gave the same SHA-256, so the build is reproducible. Size 17.7 MB.
- **Artifact contents:** no `pprof`, `expvar` or debug handlers in the import graph or the binary; no workstation paths, user names or drive letters in the binary; the module list in the binary matches `go.mod` and the licence gate.
- **Image:** base images pinned by digest and checked by a guard; distroless static with explicit `USER 65532:65532`; only `/usr/local/bin/alcove` copied in; `.dockerignore` keeps `.git`, `.env`, keys, `*.tryst`, `docs/` and `scripts/` out of the build context; self-probing `HEALTHCHECK`; the Go toolchain image sets `GOTOOLCHAIN=local`, so a version mismatch fails instead of downloading a compiler.
- **Runtime hardening:** read-only root, all capabilities dropped, `no-new-privileges`, non-root, tmpfs `noexec,nosuid,nodev`, one port bound to a chosen interface (default loopback), memory and CPU limits, bounded json-file logs; the same flags are in the Unraid template; CI asserts non-root, read-only, memory limit and exactly one published port against the real compose output, then smoke-tests the built image (no key, key inside data, wrong key, restart, ciphertext only, first run, network gate).
- **Pipeline:** release triggers only on a version tag, refuses a tag that is not on `main`, builds once by digest, guards and scans that digest (Trivy vulnerabilities and secrets), and only then applies the version and `:stable` tags; `:stable` moves only for the highest tag; SBOM and provenance attestation are produced; `scripts/release.sh` refuses a dirty tree, a HEAD that is not `origin/main`, a missing changelog heading or red CI on the exact commit.

## Verdict
This lens covered the release flags, reproducibility, the contents of the artifact, the image and runtime hardening, and the CI and release pipeline, by reading the files, building the release configuration twice and inspecting the result. It cannot cover: building and inspecting the image here (no Docker on this machine), the host's own settings (swap, Docker daemon, who can read container logs), a vulnerability scan more recent than CI's, and the GitHub account itself (branch protection, who can push tags). **Blockers outstanding: none.** Findings 1 and 2 are worth doing before the audit is called passed.

Off-lens note: `CHANGELOG.md` `[Unreleased]` does not yet mention this audit's fixes (admin-log gaps, Compare cap, hidden member list, display-name rule); worth adding before the next release.

## Resolution (same day, uncommitted)

- **1 fixed:** every third-party action in `ci.yml` and `release.yml` is pinned to a full commit SHA with its version in a comment (checkout, setup-go, docker setup-buildx, login, build-push, attest-build-provenance; Trivy already was). `.github/dependabot.yml` added so each pin moves only through a reviewed pull request.
- **2 fixed:** compose sets `mem_limit` and `memswap_limit` to the same value (swap off for the container), the Unraid template adds `--memory-swap=2g`, `scripts/guards.sh unraid-template` now requires it, and the CI compose check asserts `memswap_limit`.
- **3 fixed:** `govulncheck` pinned to `v1.10.0` (the newest module version, from `go list -m -versions`).
- **4 fixed:** `.env.example`, `SETUP.md` and `THREAT_MODEL.md` now say one backup is decrypted at a time.
- **5 fixed:** `pids_limit: 256` in compose, `--pids-limit=256` in the template (also required by the guard and asserted in CI).
- **Not verified locally:** Docker is not installed here, so `docker compose config` was not run on the new keys (`mem_limit` alongside `deploy.resources.limits.memory`, same value), and the workflow YAML was not parsed by a tool. The next CI run (image job: `docker compose config -q` and the new assertions) is the first real check. `scripts/guards.sh all` passes.

## Follow-up: first CI run of the fix failed, repaired

The first push (`3ca011a`) failed the image job at `docker compose config`: compose refuses a top-level `pids_limit` next to `deploy.resources.limits.pids` ("can't set distinct values"). The process limit now lives only under `deploy.resources.limits.pids: 256`, and the CI assertion looks for `pids: 256`. `mem_limit` and `memswap_limit` stay at the top level; if the next image job rejects `mem_limit` beside `deploy.resources.limits.memory`, drop `mem_limit` and keep `memswap_limit`.
