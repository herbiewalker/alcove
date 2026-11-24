# Pre-release audit round: summary

**Dates:** 2026-11-21 to 2026-11-23
**Method:** DevPlaybook `prompts/P03-audit-pass.md` and `checklists/02-pre-release-audit.md`. One lens per pass, a findings table with severity, location and fix, no fix without the owner's approval, then a delta-audit of the whole round.
**State at the end:** `main` at `3a318da`, CI green on the exact commit, working tree clean.

## Result

Six passes, 31 findings: 15 should-fix and 16 nit. No blocker was found in any pass, and none of the findings was an exploitable access hole. Every finding was either fixed or settled by an owner decision, and each fix went out in its own commit with green CI. The per-pass logs in this folder hold the evidence.

| Pass | Log | Findings (should-fix / nit) | Fix commits |
|---|---|---|---|
| Authorization and isolation | `2026-11-21-authorization-isolation.md` | 4 (2 / 2) | `f564221`, `6856f74` |
| Secrets, storage and logging | `2026-11-21-secrets-storage-logging.md` | 4 (1 / 3) | `72e5151`, `02759c1` |
| Input and injection | `2026-11-21-input-injection.md` | 4 (2 / 2) | `8459b5f` |
| Build hardening | `2026-11-21-build-hardening.md` | 5 (2 / 3) | `3ca011a`, `92ad3f7` |
| UI and accessibility | `2026-11-22-ui-accessibility.md` | 11 (6 / 5) | `a9c275a` |
| Delta-audit of the round | `2026-11-23-delta-audit.md` | 3 (2 / 1) | `3a318da` |

## What mattered most

- **Admin accountability (constraint 6).** Two error re-render paths showed an owner's data to the admin without a log line, and restore and purge wrote their log line in a separate transaction. Both are now logged, and restore and purge log atomically.
- **Memory exhaustion by a member.** The Compare page loaded every shared entry (440 MiB for a 50,000-entry backup), and a backup body was decrypted into a buffer that grew to about 2.6 times its size. Compare is now bounded, and one backup is held in memory at a time. The delta-audit then found the slot was released too early, so it is now held until the body is closed.
- **Missing guard for constraint 10.** The docs named gitleaks in CI but nothing ran it. It now scans full history in CI.
- **Release supply chain.** The release job's third-party actions were movable tags while the job holds package, contents and id-token write. All are pinned to commit SHAs, kept current by Dependabot, and the pins were checked against their release tags.
- **Swap and process limits.** Memory had no swap limit, which could page decrypted data to the host disk. Added `memswap_limit` and a process limit. The first attempt put `pids_limit` where compose rejected it, CI caught it, and it now lives under `deploy.resources.limits`.
- **Accessibility.** Form field borders failed WCAG 3:1 contrast, row buttons had identical names, the busy banner announced every second, the hide screen left the page reachable by keyboard and screen reader, and there was no skip link. All fixed, with a test that keeps the template and CSS rules in place.

## Owner decisions recorded

- A paused viewer still sees other members' entries (only their own are hidden). Confirmed as intended, D-32.
- A member on older consent wording no longer sees the member list until they agree to the current wording.

## Held up under attack

Cross-space and non-member isolation, the owner-approval rule, the per-entry private flag, leave and pause, step-up checks on admin edits, the hash-chained admin log, the content-free log rule, the allowed-networks refusal, and crypto and backup-reading corruption handling all survived adversarial probing in their passes. Each log lists the attempts.

## Not covered, and why

1. **Manual accessibility pass.** Needs a real phone and a screen reader. The audit worked from the source, computed contrast ratios and the tests, not from assistive technology.
2. **The release workflow has never run with the four new action majors.** CI does not execute `release.yml`. The attest step runs after tags are applied, so a failure there leaves live tags. `create-storage-record: false` was added because storage records need an organization-owned repository. Watch the attest step on the first release. A dry-run release workflow on a branch would close this gap for good.
3. **Export round trip on a real phone.** Proven on a Tryst emulator only, as the project's rules require.
4. **Full-size memory runs.** The decrypt memory ratio was measured at 200 MiB, not at the 512 MiB limit (needs more than 2 GiB of RAM on the test machine).

## Verdict

Covered: the six lenses above on the code as of `3a318da`. Cannot cover from here: assistive technology behaviour, the real release run, a real phone. Blockers: none open.
