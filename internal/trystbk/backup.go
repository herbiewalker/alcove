package trystbk

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// Row is one table row exactly as it appeared in data.json: the column order is kept
// and every value is kept as its raw JSON. Nothing is interpreted, so a column Alcove
// does not know about survives a read and a later write untouched (D-7, F9), and a
// number keeps its exact spelling.
type Row struct {
	Keys   []string
	Values map[string]json.RawMessage
}

// Get returns the raw JSON for col.
func (r Row) Get(col string) (json.RawMessage, bool) {
	v, ok := r.Values[col]
	return v, ok
}

// String returns col as a string. ok is false when the column is absent or null.
func (r Row) String(col string) (s string, ok bool) {
	raw, present := r.Values[col]
	if !present || string(raw) == "null" {
		return "", false
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// Int returns col as an int64. ok is false when the column is absent, null or not an
// integer.
func (r Row) Int(col string) (int64, bool) {
	raw, present := r.Values[col]
	if !present || string(raw) == "null" {
		return 0, false
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// Table is one named table, rows in file order.
type Table struct {
	Name string
	Rows []Row
}

// Entry is a small ZIP entry carried through unread (settings.json and any future one).
type Entry struct {
	Name string
	Data []byte
}

// ExtraKey is a top-level data.json key Alcove does not know, kept raw.
type ExtraKey struct {
	Key   string
	Value json.RawMessage
}

// MediaEntry describes one media/<id> blob. Its bytes are not read until OpenMedia.
type MediaEntry struct {
	Name string // full ZIP name, for example "media/<id>"
	Size int64  // declared uncompressed size, from the central directory
}

// Backup is a fully read TRYSTBK1 container.
//
// The decrypted body is held in memory for the life of the value (hard constraint 4:
// plaintext only in memory or tmpfs). Call Close when finished so the buffer is zeroed.
type Backup struct {
	Header        Header
	SchemaVersion int
	Tables        []Table    // in file order
	Extra         []ExtraKey // unknown top-level data.json keys, in file order
	Settings      []byte     // settings.json, nil when the backup carries none
	Entries       []Entry    // other small entries carried through
	media         []MediaEntry
	zr            *zip.Reader
	body          []byte
}

// Table returns the named table, or nil.
func (b *Backup) Table(name string) *Table {
	for i := range b.Tables {
		if b.Tables[i].Name == name {
			return &b.Tables[i]
		}
	}
	return nil
}

// RowCount returns the number of rows in the named table (0 if absent).
func (b *Backup) RowCount(name string) int {
	if t := b.Table(name); t != nil {
		return len(t.Rows)
	}
	return 0
}

// Media lists the media entries without reading their bytes.
func (b *Backup) Media() []MediaEntry { return append([]MediaEntry(nil), b.media...) }

// ErrNoSuchMedia is returned by OpenMedia for a name that is not a media entry.
var ErrNoSuchMedia = errors.New("trystbk: no such media entry")

// ErrClosed is returned when a Backup is used after Close.
var ErrClosed = errors.New("trystbk: backup is closed")

// ErrMediaTooLarge is returned when a media entry exceeds MaxMediaEntryBytes.
var ErrMediaTooLarge = errors.New("trystbk: media entry exceeds the size bound")

// OpenMedia opens one media entry for reading, bounded by MaxMediaEntryBytes. The
// reader fails if the entry turns out larger than it declared.
func (b *Backup) OpenMedia(name string) (io.ReadCloser, error) {
	if b.zr == nil {
		return nil, ErrClosed
	}
	for _, f := range b.zr.File {
		if f.Name != name || !isMediaName(name) {
			continue
		}
		if f.UncompressedSize64 > lim.mediaEntry {
			return nil, fmt.Errorf("%w: %q", ErrMediaTooLarge, name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("trystbk: open media %q: %w", name, err)
		}
		return &boundedReadCloser{r: io.LimitReader(rc, int64(f.UncompressedSize64)+1), c: rc, limit: int64(f.UncompressedSize64)}, nil
	}
	return nil, ErrNoSuchMedia
}

// Close zeroes the decrypted body. The Backup must not be used afterwards.
func (b *Backup) Close() {
	for i := range b.body {
		b.body[i] = 0
	}
	b.body, b.zr, b.media = nil, nil, nil
}

// SchemaStatus reports whether the backup's Tryst schema is one Alcove knows.
func (b *Backup) SchemaStatus() SchemaSupport { return SchemaStatus(b.SchemaVersion) }

type boundedReadCloser struct {
	r     io.Reader
	c     io.Closer
	limit int64
	n     int64
}

func (b *boundedReadCloser) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.n += int64(n)
	if b.n > b.limit {
		return n, ErrMediaTooLarge
	}
	return n, err
}

func (b *boundedReadCloser) Close() error { return b.c.Close() }

func isMediaName(name string) bool {
	return len(name) > len(EntryMediaPrefix) && name[:len(EntryMediaPrefix)] == EntryMediaPrefix
}

// newZipReader wraps body for reading.
func newZipReader(body []byte) (*zip.Reader, error) {
	return zip.NewReader(bytes.NewReader(body), int64(len(body)))
}
