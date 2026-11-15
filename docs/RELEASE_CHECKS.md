# Checks that need real hardware or an emulator

CI cannot run these. Do them by hand and record the result in the release notes.

## The export round trip (N8)

Run it **before every Alcove release that touches `internal/export`, and for every new Tryst release** (a later Tryst can change how a restore behaves, D-15).

```powershell
.\scripts\emulator\roundtrip.ps1
```

What it does, with synthetic data only:

1. Writes three files with Alcove's own code (`go test ./internal/ingest -run TestRoundTripEmit`): a base backup, a different phone-only backup, and an **export** made from the base after an edit, an added entry, a deleted entry that owns a photo, and a renamed partner.
2. On the emulator, restores the base (then, in a second pass, the phone-only backup), then restores the export with **Replace my current data with the backup** on.
3. Has the Tryst app export again, and compares that file with the export column by column (`TestRoundTripCompare`; the device rewrites photo paths and re-escapes strings, which are not counted).

It passes only with **0 differences** in both passes: the edit, the added entry, the deletion and its photos, and the partner rename are all on the phone, and nothing from the phone-only data is left.

Rules: an **emulator** at `emulator-5554` only (the driver refuses anything else, and `adb` is always called with `-s`). Never a physical phone. Tryst must be installed, have the PIN `123456`, and Auto-lock set to 5 min.

Known: the x86_64 16K emulator image sometimes crashes Tryst in code unrelated to the file (seen in Compose drawing and a Room getter). The script restarts Tryst and retries the step, and prints how many retries it needed. A real failure is a difference in the comparison, never a retry.

### Merge mode (not part of the script)

Checked by hand once, on Tryst 0.6.5 (D-43 addendum): restoring an export with the toggle **off** keeps entries that were deleted here, keeps their photos, and drops the partner links the file does not carry. That is why the export page tells people never to switch it off.
