// Package trystbk reads and writes Tryst's `TRYSTBK1` encrypted backup container.
//
// Provenance: the container constants, header parser, Zip-Slip validation and the
// bounded decrypt are adapted from the Sanctum project's `internal/trystbk`
// (MIT, same author, see NOTICE). Alcove extends it in two ways Sanctum
// deliberately does not: it reads the table rows (Sanctum only counts them), and it
// writes containers (for the synthetic test generator now and the export flow in M6).
// ADR D-9 records the copy-first, extract-later decision.
//
// This file is the format specification expressed as constants, and nothing else.
// It is transcribed from Tryst's docs/EXPORT_FORMAT.md, which is the contract Alcove
// implements against rather than sharing code with the app.
//
// The container:
//
//	┌──────────────── cleartext header, 29 bytes ────────────────┐
//	│ magic       8 bytes   ASCII "TRYSTBK1"                     │
//	│ version     1 byte    format version (1 or 2)              │
//	│ salt       16 bytes   random, for the password KDF         │
//	│ iterations  4 bytes   big-endian int32, PBKDF2 count       │
//	└────────────────────────────────────────────────────────────┘
//	  ↓ everything after the header is one AEAD stream
//	AES-256-GCM-HKDF streaming, 1 MiB segments, AD = "tryst-backup-v1",
//	key = PBKDF2-HMAC-SHA256(password, salt, iterations) → 32 bytes
//	  ↓ whose plaintext is a ZIP containing
//	data.json      { "schemaVersion": N, "tables": { <table>: [ {col: value}, … ] } }
//	media/<id>     decrypted photo bytes, re-encrypted by the container
//	settings.json  optional (container v2): non-sensitive app preferences
//
// If this file and Tryst's actual behaviour ever disagree, the app's behaviour wins and
// the spec is a Tryst-side bug to file.
package trystbk

// Header layout. HeaderSize is asserted against its parts in the tests, so a future
// format revision cannot silently desynchronise the offsets.
const (
	// Magic is the 8-byte ASCII prefix every container starts with.
	Magic = "TRYSTBK1"

	MagicSize      = 8
	VersionSize    = 1
	SaltSize       = 16
	IterationsSize = 4

	// HeaderSize is the total cleartext prefix before the AEAD stream begins.
	HeaderSize = MagicSize + VersionSize + SaltSize + IterationsSize // 29

	// FormatVersion1 is the original container version.
	FormatVersion1 = 1

	// FormatVersion2 is what Tryst writes since QOL-5 (v0.6.x). The crypto is
	// byte-for-byte the same as v1; the only difference is an optional settings.json
	// entry inside the ZIP.
	FormatVersion2 = 2
)

// Streaming AEAD parameters. These must match what Tryst encrypts with exactly; a
// mismatch does not produce a helpful error, it produces an authentication failure
// indistinguishable from a wrong password.
const (
	// AssociatedData is bound into every segment's authentication tag.
	AssociatedData = "tryst-backup-v1"

	// HKDFAlg is the hash used for HKDF key derivation inside the stream.
	HKDFAlg = "SHA256"

	// KeySizeBytes is the AES-256 key size, and also the PBKDF2 output length.
	KeySizeBytes = 32

	// SegmentSize is the ciphertext segment size: 1 MiB.
	SegmentSize = 1 << 20

	// FirstSegmentOffset is 0 because the AEAD reader is handed a stream that already
	// has the cleartext header consumed.
	FirstSegmentOffset = 0
)

// PBKDF2 iteration bounds.
//
// The iteration count is read from the container header, so it is ATTACKER-CONTROLLED:
// a crafted backup could claim math.MaxInt32 and freeze the service deriving a key that
// will never be used. Tryst bounds it on import for exactly this reason and Alcove
// mirrors the bound. The lower bound matters too: a backup claiming 1 iteration is
// corrupt or hostile, and the writer must never produce one Tryst would refuse.
const (
	MinIterations = 100_000
	MaxIterations = 5_000_000

	// DefaultIterations is what Tryst currently writes. Always read the real count
	// from the header when reading.
	DefaultIterations = 600_000
)

// Size bounds. All of them exist because a backup is untrusted input (THREAT_MODEL T6).
const (
	// MaxBackupBytes bounds the DECRYPTED body held in memory. Real backups are
	// ~10^2 MB (largest seen: 34 MB); 512 MiB leaves room for video later while a
	// hostile file still cannot exhaust the container's memory limit.
	MaxBackupBytes = 1 << 29 // 512 MiB

	// MaxDataJSONBytes bounds data.json, the JSON dump of the whole database.
	MaxDataJSONBytes = 128 << 20 // 128 MiB

	// MaxTotalUncompressedBytes bounds the sum of all entries' declared uncompressed
	// sizes, rejecting a zip bomb from the central directory before anything is read.
	MaxTotalUncompressedBytes = 2 << 30 // 2 GiB

	// MaxSmallEntryBytes bounds entries other than data.json and media that are
	// carried through unread (settings.json and any future small entry).
	MaxSmallEntryBytes = 1 << 20 // 1 MiB

	// MaxMediaEntryBytes bounds one photo or video blob read on demand.
	MaxMediaEntryBytes = 256 << 20 // 256 MiB

	// MaxZipEntries bounds how many entries are iterated.
	MaxZipEntries = 200_000

	// MaxRows bounds the total number of table rows across data.json.
	MaxRows = 5_000_000
)

// Expected ZIP entries.
const (
	EntryDataJSON     = "data.json"
	EntrySettingsJSON = "settings.json"
	EntryMediaPrefix  = "media/"
)
