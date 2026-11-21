# Audit pass: input and injection

- **Date:** 2026-11-21
- **Lens:** input and injection (SQL, path traversal, template/markup injection, header and redirect handling, parsing of untrusted files, bounds on attacker-controlled sizes and counts)
- **Tree:** `main` at `02759c1` (CI green)
- **Verified by running:** a 40 s fuzz of `photo.Strip` (about 9 million executions), a 15 s fuzz of `share.Match`, a memory probe of the Compare page with a 50,000-entry upload, a buffer-growth probe; all probes were throwaway files and are deleted. No tree changes.

## Scope read
Every `Query`/`Exec` with built-up SQL text; every `PathValue`, `Query().Get` and `PostForm.Get` in `internal/server`; blob and backup path handling;
redirects and `Content-Disposition`; `html/template` use and the two `template.HTML`/`template.URL` exceptions; the one script (`ui.js`);
`netsec` header handling; Tryst backup reading (`trystbk`: header, KDF count, zip, JSON), photo stripping and thumbnails; field validators in `edit`, `auth`, `profile`, `spaces`; HTTP server limits.

## Findings

| # | Severity | Where | Issue | Fix |
|---|----------|-------|-------|-----|
| 1 | should-fix | `internal/server/compare.go:40`, `share.Entries` with `Query{}` | The Compare page loads **every shared entry of every member** into memory. Measured: a 4.8 MB upload with 50,000 entries makes one `GET /spaces/{id}/compare` allocate **440 MiB** and take 1.1 s (about 9 KB per entry). The reader allows up to 5,000,000 rows and 128 MiB of `data.json`, so a member with a large backup, or any member who opens Compare after them, can push one request past the container's 2 GB limit and get it OOM-killed (everyone is signed out). Invite-only, so a hostile or careless member, not a stranger. | Compute the metrics per member in SQL or in a streaming pass over the period only, or cap the rows read (for example 20,000 per member) and say so on the page. Consider a per-account cap on entry count at upload. |
| 2 | should-fix | `internal/trystbk/read.go:58-62` (`bytes.Buffer`), `internal/ingest/ingest.go:75` (`sem` of 2) | A backup body is decrypted into a `bytes.Buffer` that grows by doubling. Measured: reading 200 MiB used about 520 MiB of process memory (2.6 times). At the 512 MiB limit one upload peaks near 1.3 GiB, and two concurrent uploads (the semaphore allows 2) exceed the 2 GB container limit. A member crafts a valid backup with their own password and uploads it (the file is as large as its body). Not reproduced at full size (needs more than 2 GiB of RAM); the ratio is measured. | Pre-size the buffer from the file length, or stream the zip; drop the semaphore to 1 or lower `MaxBackupBytes` (the largest real backup seen is 34 MB). |
| 3 | nit | `internal/auth/users.go:90-102` vs `SetDisplayName` (`:230-241`) | Display names are validated in two places. `SetDisplayName` rejects control characters; `Create` (used by first-run, admin create and join) does not, and both count bytes while the message says characters. Neither blocks bidirectional-override characters, so a name can be made to read as another member's. Output is always HTML-escaped, so this is not injection, only spoofing and odd data. | One shared validator: rune count, no control or format (bidi, zero-width) characters, used by every path. |
| 4 | nit | `internal/server/entries.go:35`, `handlers.go:518` | `?page=` accepts any integer; `(n-1)*50` can overflow to a negative `OFFSET`, which SQLite treats as 0. No crash, only a wrong page. | Clamp `page` to a sensible maximum (for example 1,000,000). |

No blockers.

## What held up
- **SQL:** every dynamic fragment is a fixed string; every value, including the `IN (...)` lists, the log filter, and FTS terms, is bound. `share.Match` quotes each word and was fuzzed with no failure.
- **Paths:** blob ids and owner ids pass a strict allowlist and are also looked up in the database first; backup names must equal their own `filepath.Base`, match the prefix and suffix, and be in the listing; Zip-Slip names are refused in the reader; static files come from a fixed registry.
- **Markup:** `html/template` throughout; only two `template.*` exceptions, both built from integers or `url.Values.Encode`; CSP `script-src 'self'`; `ui.js` has no `innerHTML`, `eval` or DOM sinks.
- **Redirects and headers:** every redirect target is a constant, a map lookup, or a database id after the gate; `Content-Disposition` names come from a restricted username regexp or fixed text; the download token cookie is a strict `[a-z0-9]{8,40}`.
- **Hostile backup:** KDF count bounded (100,000 to 5,000,000), decrypted body, `data.json`, entry sizes, entry count and row count all capped; declared zip sizes are enforced by the Go reader; upload size capped by `MaxBytesReader`; error text reaching logs is reduced to a fixed label.
- **Images:** the JPEG and PNG stripper is a streaming parser with no unbounded allocation (fuzzed, no crash, no output larger than input); thumbnails cap at 50 million pixels and 2 at a time.
- **Network input:** `X-Forwarded-For` is read from the right and only through a trusted proxy; forms are capped at 64 KiB; header, read and write timeouts are set.

## Verdict
This lens covered injection into SQL, files, markup, headers and redirects, and the size and count bounds of every input the code reads, by reading the code, fuzzing the two hand-written parsers and measuring the Compare and buffer costs. It cannot cover: a fuzz of the full Tryst reader with real Tryst output, full-size memory behaviour on the real 2 GB container (findings 1 and 2 are measured at smaller scale), browser quirks, and the proxy in front of it. **Blockers outstanding: none.** Findings 1 and 2 are member-triggered denial of service and worth fixing before the audit is called passed.

## Resolution (same day, uncommitted)

- **1 fixed:** Compare reads at most 10,000 entries per member (newest first) through a new optional `share.Query.Owner`, still inside the one gate, and the page says "Only the newest 10,000 entries are counted for ..." when it cuts. The same 50,000-entry probe now allocates 111 MiB instead of 440 MiB (the figure is total allocation during the request, not live heap). Test `TestCompareIsBoundedPerMemberAndSaysSo`.
- **2 fixed:** the first segment is decrypted on its own (a wrong password fails before anything large is reserved), then the buffer is reserved once from the file size with slack, so it no longer doubles; backups are decrypted one at a time instead of two. Test `TestReadFromAFileMatchesReadFromMemory`. The 2.6 times figure was measured on the old path; the new peak was not re-measured at full size.
- **3 fixed:** one `auth.CleanDisplayName` (valid UTF-8, 1 to 80 characters, no control and no bidirectional-override characters, zero-width joiners kept) used by account creation, first run, join and the profile; the first-run and join pages show a plain message. Tests in `auth_test.go` and `input_audit_test.go` (the latter fails without the change).
- **4 fixed:** `?page=` is clamped to 1,000,000 for entries, galleries and the admin log. Test `TestHugePageNumbersAreClamped`.
- Re-verified: `go vet`, `gofmt`, `go test ./...` and `scripts/guards.sh all` pass.
