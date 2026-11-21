package trystbk

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/tink-crypto/tink-go/v2/streamingaead/subtle"
	"golang.org/x/crypto/pbkdf2"
)

// Well-known errors from the read path. Callers match on these to decide what to log
// (a wrong password is common; an oversize or zip-slip file is suspicious).
var (
	ErrWrongPassword  = errors.New("trystbk: decryption failed, wrong password or tampered stream")
	ErrOversize       = errors.New("trystbk: decrypted body exceeds MaxBackupBytes")
	ErrBadZip         = errors.New("trystbk: decrypted body is not a well-formed ZIP")
	ErrNoDataJSON     = errors.New("trystbk: backup is missing data.json")
	ErrZipSlip        = errors.New("trystbk: ZIP entry name escapes the archive root")
	ErrTooManyEntries = errors.New("trystbk: ZIP has an unreasonable number of entries (zip bomb?)")
	ErrZipBomb        = errors.New("trystbk: ZIP declares an unreasonable total size (zip bomb?)")
	ErrEntryTooLarge  = errors.New("trystbk: a ZIP entry exceeds its size bound")
)

// Read parses the header, derives the key from password, decrypts the AEAD stream into
// memory (bounded by MaxBackupBytes) and reads the inner ZIP: every table row, the
// small entries, and a list of media entries whose bytes stay unread until asked for.
//
// The plaintext lives only in memory, for the life of the returned Backup. Nothing is
// written to disk (hard constraint 4).
func Read(r io.Reader, password string) (*Backup, error) {
	h, err := ParseHeader(r)
	if err != nil {
		return nil, err
	}
	return ReadWithHeader(r, h, password)
}

// ReadWithHeader is the seam for a caller that has already parsed the header, for
// example to refuse a bad file cheaply before the expensive key derivation.
func ReadWithHeader(r io.Reader, h Header, password string) (*Backup, error) {
	// PBKDF2 is deliberately expensive: with 600k iterations it takes on the order of
	// 100 ms per attempt. A caller trying several saved passwords pays it per password.
	key := pbkdf2.Key([]byte(password), h.Salt[:], h.Iterations, KeySizeBytes, sha256.New)

	aead, err := subtle.NewAESGCMHKDF(key, HKDFAlg, KeySizeBytes, SegmentSize, FirstSegmentOffset)
	if err != nil {
		return nil, fmt.Errorf("trystbk: build AEAD: %w", err)
	}
	dec, err := aead.NewDecryptingReader(r, []byte(AssociatedData))
	if err != nil {
		return nil, wrapDecryptErr(err)
	}

	var body bytes.Buffer
	body.Grow(SegmentSize)
	// The first segment is read on its own: a wrong password or a damaged file fails here, before
	// anything large is reserved.
	if _, err := io.Copy(&body, io.LimitReader(dec, min(lim.body, SegmentSize))); err != nil {
		return nil, wrapDecryptErr(err)
	}
	// A bytes.Buffer that grows by doubling peaks at about 2.6 times the body (measured), so when the
	// reader is a file its size, which bounds the plaintext, is reserved once, with slack for the
	// final read (audit 2026-11-21).
	if f, ok := r.(interface{ Stat() (os.FileInfo, error) }); ok {
		if fi, err := f.Stat(); err == nil && fi.Size() > 0 {
			body.Grow(int(min(fi.Size(), lim.body)) + 2*SegmentSize)
		}
	}
	if rest := lim.body - int64(body.Len()); rest > 0 {
		if _, err := io.Copy(&body, io.LimitReader(dec, rest)); err != nil {
			return nil, wrapDecryptErr(err)
		}
	}
	// One-byte overflow probe: a legitimate backup ends exactly at EOF, one that hit the
	// cap has more bytes waiting.
	var overflow [1]byte
	if n, _ := dec.Read(overflow[:]); n > 0 {
		return nil, fmt.Errorf("%w: %d MiB", ErrOversize, lim.body>>20)
	}

	b, err := readZip(body.Bytes())
	if err != nil {
		for i := range body.Bytes() { // do not leave plaintext behind on a rejected file
			body.Bytes()[i] = 0
		}
		return nil, err
	}
	b.Header = h
	return b, nil
}

// wrapDecryptErr classifies AEAD failures. tink-go returns opaque errors on
// authentication failure; they all mean "this ciphertext did not decrypt under this key",
// which in practice is a wrong password or a tampered stream. The AEAD by design cannot
// tell those apart, so neither do we.
func wrapDecryptErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return fmt.Errorf("trystbk: stream truncated: %w", err)
	}
	return fmt.Errorf("%w (%v)", ErrWrongPassword, err)
}

// readZip walks the decrypted ZIP. Every entry name is validated before anything is
// opened (N10); media bytes are never read here; unknown small entries are carried.
func readZip(body []byte) (*Backup, error) {
	zr, err := newZipReader(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadZip, err)
	}
	if len(zr.File) > lim.zipEntries {
		return nil, fmt.Errorf("%w: %d", ErrTooManyEntries, len(zr.File))
	}

	b := &Backup{zr: zr, body: body}
	var total uint64
	var dataJSON bool

	for _, f := range zr.File {
		if err := validateEntryName(f.Name); err != nil {
			return nil, err
		}
		total += f.UncompressedSize64
		if total > lim.totalUncompressed {
			return nil, fmt.Errorf("%w: more than %d MiB", ErrZipBomb, lim.totalUncompressed>>20)
		}
		if strings.HasSuffix(f.Name, "/") { // a directory entry
			continue
		}

		switch {
		case f.Name == EntryDataJSON:
			if dataJSON {
				return nil, fmt.Errorf("%w: multiple data.json entries", ErrDataJSONMalformed)
			}
			dataJSON = true
			if f.UncompressedSize64 > uint64(lim.dataJSON) {
				return nil, fmt.Errorf("%w: data.json declares %d bytes", ErrEntryTooLarge, f.UncompressedSize64)
			}
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("%w: open data.json: %v", ErrDataJSONMalformed, err)
			}
			perr := parseDataJSON(rc, b)
			rc.Close()
			if perr != nil {
				return nil, fmt.Errorf("%w: %v", ErrDataJSONMalformed, perr)
			}

		case isMediaName(f.Name):
			b.media = append(b.media, MediaEntry{Name: f.Name, Size: int64(f.UncompressedSize64)})

		default: // settings.json and any future small entry
			if f.UncompressedSize64 > uint64(lim.smallEntry) {
				return nil, fmt.Errorf("%w: %q declares %d bytes", ErrEntryTooLarge, f.Name, f.UncompressedSize64)
			}
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("%w: open %q: %v", ErrBadZip, f.Name, err)
			}
			data, rerr := io.ReadAll(io.LimitReader(rc, lim.smallEntry+1))
			rc.Close()
			if rerr != nil {
				return nil, fmt.Errorf("%w: read %q: %v", ErrBadZip, f.Name, rerr)
			}
			if int64(len(data)) > lim.smallEntry {
				return nil, fmt.Errorf("%w: %q", ErrEntryTooLarge, f.Name)
			}
			if f.Name == EntrySettingsJSON {
				b.Settings = data
			} else {
				b.Entries = append(b.Entries, Entry{Name: f.Name, Data: data})
			}
		}
	}
	if !dataJSON {
		return nil, ErrNoDataJSON
	}
	return b, nil
}

// validateEntryName rejects Zip-Slip and any absolute or traversal path. The name must be
// a clean forward-slash relative path with no ".." component and no leading slash.
func validateEntryName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: empty name", ErrZipSlip)
	}
	if strings.ContainsRune(name, '\\') {
		return fmt.Errorf("%w: backslash in %q", ErrZipSlip, name)
	}
	if strings.HasPrefix(name, "/") {
		return fmt.Errorf("%w: absolute path %q", ErrZipSlip, name)
	}
	cleaned := path.Clean(name)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("%w: escapes root: %q", ErrZipSlip, name)
	}
	if path.IsAbs(cleaned) {
		return fmt.Errorf("%w: absolute after clean: %q", ErrZipSlip, name)
	}
	return nil
}
