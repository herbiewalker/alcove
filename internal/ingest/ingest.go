// Package ingest turns an uploaded Tryst backup into a revision (D-29, M3 piece B).
//
// The order is the point. The upload is written to the blob store first (verbatim, immutable,
// SHA-256 on the way in), then judged: duplicate, wrong password, not a backup, damaged. Only a
// backup that passes is recorded, in ONE transaction: the upload row, the revision, the derived
// row cache, the search index and the sealed password. A refusal deletes the blob and records
// nothing, so a failed upload leaves no trace but a log line with counts. The blob is always
// written before the row that refers to it (the D-28 rule that keeps live backups consistent).
//
// Hard constraints: the decrypted backup lives in memory only and is zeroed by Close (4); nothing
// here logs a password, a file name or any entry content (8).
package ingest

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/herbiewalker/alcove/internal/blobstore"
	"github.com/herbiewalker/alcove/internal/store"
	"github.com/herbiewalker/alcove/internal/trystbk"
)

// Refusals. Each has wording a person can act on; none carries file content.
var (
	ErrWrongPassword = errors.New("that is not the password this backup was made with")
	ErrNotBackup     = errors.New("this file is not a Tryst backup")
	ErrDamaged       = errors.New("this backup is damaged or not in a form Alcove can read")
	ErrTooLarge      = errors.New("this file is larger than Alcove accepts")
	ErrNoPassword    = errors.New("no backup password is stored for this person")
	ErrNoRevision    = errors.New("there is nothing uploaded yet")
)

// DuplicateError means the same file (same SHA-256) is already stored for this owner.
type DuplicateError struct {
	Deleted    bool // the earlier copy is in "recently deleted"
	ExistingID string
	UploadedAt time.Time
}

func (e *DuplicateError) Error() string {
	if e.Deleted {
		return "this exact file was uploaded on " + e.UploadedAt.UTC().Format("2006-01-02 15:04") + " UTC and is in Recently deleted: restore it there instead"
	}
	return "this exact file was already uploaded on " + e.UploadedAt.UTC().Format("2006-01-02 15:04") + " UTC"
}

// Service ingests uploads for one store.
type Service struct {
	St    *store.Store
	Blobs *blobstore.Store
	Now   func() time.Time
	// MaxBytes caps an upload; zero means the Tryst reader's own bound (512 MiB).
	MaxBytes int64
	// sem bounds how many backups are decrypted at once: each holds up to MaxBytes in memory.
	sem chan struct{}
}

// New builds a service. At most two backups are decrypted at the same time.
func New(st *store.Store, blobs *blobstore.Store) *Service {
	return &Service{St: st, Blobs: blobs, Now: time.Now, sem: make(chan struct{}, 2)}
}

func (s *Service) max() int64 {
	if s.MaxBytes > 0 {
		return s.MaxBytes
	}
	return trystbk.MaxBackupBytes
}

// Params describe one upload.
type Params struct {
	Owner     string // whose data this is
	CreatedBy string // who uploaded it (the owner, or an admin acting for them)
	Filename  string // display text only
	Password  string // the Tryst backup password, used for the trial decrypt
	Remember  bool   // keep the password, sealed, for rebuild, edit and export
}

// UploadInfo describes a stored upload and its revision.
type UploadInfo struct {
	ID, RevisionID, Filename, SHA256 string
	Size                             int64
	TrystSchema                      int
	SchemaStatus                     trystbk.SchemaSupport
	Rows, Encounters, Media          int
	UploadedAt                       time.Time
	Head                             bool
	Deleted                          bool
}

func newID(prefix string) (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

// CleanFilename keeps a file name as harmless display text: no path, no control characters, a
// bounded length. It is never used to build a path.
func CleanFilename(name string) string {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	var b strings.Builder
	n := 0
	for _, r := range name {
		if unicode.IsControl(r) || r == '<' || r == '>' || r == '"' {
			continue
		}
		b.WriteRune(r)
		if n++; n >= 120 {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		return "backup.tryst"
	}
	return out
}

// Upload stores, judges and records one backup. On any refusal nothing is recorded and the blob
// is deleted.
func (s *Service) Upload(ctx context.Context, p Params, body io.Reader) (UploadInfo, error) {
	if !store.ValidOwnerID(p.Owner) {
		return UploadInfo{}, store.ErrOwnerID
	}
	if has, err := s.St.HasDataKey(p.Owner); err != nil {
		return UploadInfo{}, err
	} else if !has {
		if err := s.St.CreateDataKey(p.Owner); err != nil && !errors.Is(err, store.ErrDataKeyExists) {
			return UploadInfo{}, err
		}
	}
	id, err := newID("up")
	if err != nil {
		return UploadInfo{}, err
	}
	sum, size, err := s.Blobs.PutUpload(p.Owner, id, body, s.max())
	if err != nil {
		if errors.Is(err, blobstore.ErrTooLarge) {
			return UploadInfo{}, ErrTooLarge
		}
		return UploadInfo{}, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = s.Blobs.Delete(p.Owner, id)
		}
	}()

	// Duplicate (same owner, same bytes).
	var dup DuplicateError
	var dupAt int64
	err = s.St.View(ctx, func(tx *sql.Tx) error {
		e := tx.QueryRow(`SELECT u.id, u.uploaded_at, COALESCE(r.deleted_at IS NOT NULL, 0) FROM upload u LEFT JOIN revision r ON r.upload_id = u.id WHERE u.owner_id = ? AND u.sha256 = ?`, p.Owner, sum).Scan(&dup.ExistingID, &dupAt, &dup.Deleted)
		if errors.Is(e, sql.ErrNoRows) {
			dup.ExistingID = ""
			return nil
		}
		return e
	})
	if err != nil {
		return UploadInfo{}, err
	}
	if dup.ExistingID != "" {
		dup.UploadedAt = time.Unix(dupAt, 0)
		return UploadInfo{}, &dup
	}

	// Trial decrypt and read every table with the bounded reader.
	bk, err := s.read(ctx, p.Owner, id, sum, p.Password)
	if err != nil {
		return UploadInfo{}, err
	}
	defer bk.Close()
	status := bk.SchemaStatus()
	if status == trystbk.SchemaInvalid {
		return UploadInfo{}, ErrDamaged
	}

	// Seal the password before the transaction: the data-key calls use the store's single
	// connection and must not run inside it.
	var sealed []byte
	if p.Remember {
		if sealed, err = s.sealPassword(p.Owner, p.Password); err != nil {
			return UploadInfo{}, err
		}
	}

	now := s.Now()
	revID, err := newID("rv")
	if err != nil {
		return UploadInfo{}, err
	}
	info := UploadInfo{ID: id, RevisionID: revID, Filename: CleanFilename(p.Filename), SHA256: sum, Size: size,
		TrystSchema: bk.SchemaVersion, SchemaStatus: status, UploadedAt: now.UTC().Truncate(time.Second), Head: true}
	for _, t := range bk.Tables {
		info.Rows += len(t.Rows)
	}
	if t := bk.Table("encounters"); t != nil {
		info.Encounters = len(t.Rows)
	}
	info.Media = len(bk.Media())

	err = s.St.Update(ctx, true, func(tx *sql.Tx) error {
		var parent sql.NullString
		if e := tx.QueryRow(`SELECT id FROM revision WHERE owner_id = ? AND deleted_at IS NULL ORDER BY created_at DESC, rowid DESC LIMIT 1`, p.Owner).Scan(&parent); e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if _, e := tx.Exec(`INSERT INTO upload(id, owner_id, filename, sha256, size, tryst_schema, schema_status, rows_total, encounters, media_count, uploaded_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`, id, p.Owner, info.Filename, sum, size, info.TrystSchema, int(status), info.Rows, info.Encounters, info.Media, now.Unix()); e != nil {
			return e
		}
		if _, e := tx.Exec(`INSERT INTO revision(id, owner_id, parent_id, kind, upload_id, created_by, created_at) VALUES (?,?,?,?,?,?,?)`,
			revID, p.Owner, parent, "upload", id, p.CreatedBy, now.Unix()); e != nil {
			return e
		}
		if e := fillCache(tx, p.Owner, revID, bk, now); e != nil {
			return e
		}
		if p.Remember {
			_, e := tx.Exec(`INSERT INTO backup_password(owner_id, sealed, updated_at) VALUES (?,?,?)
				ON CONFLICT(owner_id) DO UPDATE SET sealed = excluded.sealed, updated_at = excluded.updated_at`, p.Owner, sealed, now.Unix())
			return e
		}
		_, e := tx.Exec(`DELETE FROM backup_password WHERE owner_id = ?`, p.Owner) // "do not remember" forgets an old one too
		return e
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") { // a concurrent upload of the same file won the race
			return UploadInfo{}, &DuplicateError{UploadedAt: now}
		}
		return UploadInfo{}, err
	}
	keep = true
	return info, nil
}

// read opens the stored upload and decrypts it, mapping the reader's errors to refusals.
func (s *Service) read(ctx context.Context, owner, id, sum, password string) (*trystbk.Backup, error) {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	f, err := s.Blobs.OpenUpload(owner, id, sum)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	bk, err := trystbk.Read(f, password)
	if err != nil {
		switch {
		case errors.Is(err, trystbk.ErrWrongPassword):
			return nil, ErrWrongPassword
		case errors.Is(err, trystbk.ErrBadMagic), errors.Is(err, trystbk.ErrHeaderTruncated):
			return nil, ErrNotBackup
		default:
			return nil, fmt.Errorf("%w (%s)", ErrDamaged, reason(err))
		}
	}
	return bk, nil
}

// reason is a short, content-free label for a reader error.
func reason(err error) string {
	for _, e := range []error{trystbk.ErrUnsupportedVer, trystbk.ErrIterationsRange, trystbk.ErrOversize, trystbk.ErrBadZip,
		trystbk.ErrNoDataJSON, trystbk.ErrZipSlip, trystbk.ErrTooManyEntries, trystbk.ErrZipBomb, trystbk.ErrEntryTooLarge, trystbk.ErrDataJSONMalformed} {
		if errors.Is(err, e) {
			return strings.TrimPrefix(e.Error(), "trystbk: ")
		}
	}
	return "unreadable"
}

// ---- the sealed password ----

func (s *Service) gcm(owner string) (cipher.AEAD, error) {
	dek, err := s.St.DataKey(owner)
	if err != nil {
		return nil, err
	}
	defer dek.Zero()
	block, err := aes.NewCipher(dek.Bytes())
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func pwAAD(owner string) []byte { return []byte("alcove-backup-password|" + owner) }

func (s *Service) sealPassword(owner, pw string) ([]byte, error) {
	g, err := s.gcm(owner)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return g.Seal(nonce, nonce, []byte(pw), pwAAD(owner)), nil
}

// StoredPassword unseals the owner's remembered backup password. The caller must not log it.
func (s *Service) StoredPassword(ctx context.Context, owner string) (string, error) {
	var sealed []byte
	err := s.St.View(ctx, func(tx *sql.Tx) error {
		e := tx.QueryRow(`SELECT sealed FROM backup_password WHERE owner_id = ?`, owner).Scan(&sealed)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNoPassword
		}
		return e
	})
	if err != nil {
		return "", err
	}
	g, err := s.gcm(owner)
	if err != nil {
		return "", err
	}
	if len(sealed) < g.NonceSize() {
		return "", errors.New("ingest: the stored password is damaged")
	}
	pt, err := g.Open(nil, sealed[:g.NonceSize()], sealed[g.NonceSize():], pwAAD(owner))
	if err != nil {
		return "", errors.New("ingest: the stored password cannot be opened")
	}
	return string(pt), nil
}

// HasStoredPassword reports whether a password is remembered for owner.
func (s *Service) HasStoredPassword(ctx context.Context, owner string) bool {
	var n int
	_ = s.St.View(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT COUNT(*) FROM backup_password WHERE owner_id = ?`, owner).Scan(&n)
	})
	return n > 0
}

// ---- the derived cache ----

// rowJSON re-serialises one row with its columns in file order.
func rowJSON(r trystbk.Row) string {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range r.Keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(r.Values[k])
	}
	b.WriteByte('}')
	return b.String()
}

// searchable names the text columns indexed per table, and the kind they are filed under.
var searchable = map[string]struct {
	kind string
	cols []string
}{
	"encounters": {"encounter", []string{"note"}},
	"partners":   {"partner", []string{"displayName", "note"}},
	"profile":    {"profile", []string{"displayName", "note"}},
}

const maxIndexedBody = 8 << 10

// fillCache replaces an owner's derived data with the tables of bk, inside tx.
func fillCache(tx *sql.Tx, owner, revID string, bk *trystbk.Backup, now time.Time) error {
	if _, err := tx.Exec(`DELETE FROM cache_row WHERE owner_id = ?`, owner); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM search WHERE owner_id = ?`, owner); err != nil {
		return err
	}
	ins, err := tx.Prepare(`INSERT INTO cache_row(owner_id, tbl, rid, pos, data) VALUES (?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	idx, err := tx.Prepare(`INSERT INTO search(owner_id, kind, ref, body) VALUES (?,?,?,?)`)
	if err != nil {
		return err
	}
	defer idx.Close()

	order := make([]string, 0, len(bk.Tables))
	for _, t := range bk.Tables {
		order = append(order, t.Name)
		seen := make(map[string]bool, len(t.Rows))
		spec, indexed := searchable[t.Name]
		for pos, r := range t.Rows {
			rid := "#" + strconv.Itoa(pos)
			if id, ok := r.String("id"); ok && id != "" && !seen[id] {
				rid = id
			}
			seen[rid] = true
			if _, err := ins.Exec(owner, t.Name, rid, pos, rowJSON(r)); err != nil {
				return err
			}
			if !indexed {
				continue
			}
			var parts []string
			for _, c := range spec.cols {
				if v, ok := r.String(c); ok && strings.TrimSpace(v) != "" {
					parts = append(parts, v)
				}
			}
			if len(parts) == 0 {
				continue
			}
			body := strings.Join(parts, "\n")
			if len(body) > maxIndexedBody {
				body = body[:maxIndexedBody]
			}
			if _, err := idx.Exec(owner, spec.kind, rid, body); err != nil {
				return err
			}
		}
	}
	ob, _ := json.Marshal(order)
	_, err = tx.Exec(`INSERT INTO cache_head(owner_id, revision_id, tryst_schema, table_order, built_at) VALUES (?,?,?,?,?)
		ON CONFLICT(owner_id) DO UPDATE SET revision_id = excluded.revision_id, tryst_schema = excluded.tryst_schema,
		table_order = excluded.table_order, built_at = excluded.built_at`, owner, revID, bk.SchemaVersion, string(ob), now.Unix())
	return err
}

// Rebuild throws away an owner's row cache and search rows and refills them from the head
// revision's stored upload, using the remembered password. A damaged cache is therefore never a
// data loss (D-29).
func (s *Service) Rebuild(ctx context.Context, owner string) error {
	var revID, uploadID, sum string
	err := s.St.View(ctx, func(tx *sql.Tx) error {
		e := tx.QueryRow(`SELECT r.id, r.upload_id, u.sha256 FROM revision r JOIN upload u ON u.id = r.upload_id
			WHERE r.owner_id = ? AND r.deleted_at IS NULL ORDER BY r.created_at DESC, r.rowid DESC LIMIT 1`, owner).Scan(&revID, &uploadID, &sum)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNoRevision
		}
		return e
	})
	if err != nil {
		return err
	}
	pw, err := s.StoredPassword(ctx, owner)
	if err != nil {
		return err
	}
	bk, err := s.read(ctx, owner, uploadID, sum, pw)
	if err != nil {
		return err
	}
	defer bk.Close()
	now := s.Now()
	return s.St.Update(ctx, true, func(tx *sql.Tx) error { return fillCache(tx, owner, revID, bk, now) })
}

// RebuildAll rebuilds every owner that has a head revision and a stored password. It returns how
// many succeeded and the owners that could not be rebuilt (with the reason kept out of the list:
// reasons are logged by the caller as counts).
func (s *Service) RebuildAll(ctx context.Context) (done int, failed []string) {
	var owners []string
	_ = s.St.View(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT DISTINCT owner_id FROM revision WHERE deleted_at IS NULL ORDER BY owner_id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o string
			if err := rows.Scan(&o); err != nil {
				return err
			}
			owners = append(owners, o)
		}
		return rows.Err()
	})
	for _, o := range owners {
		if err := s.Rebuild(ctx, o); err != nil {
			failed = append(failed, o)
			continue
		}
		done++
	}
	return done, failed
}

// List returns an owner's uploads, newest first, marking the head.
func (s *Service) List(ctx context.Context, owner string) ([]UploadInfo, error) {
	var out []UploadInfo
	err := s.St.View(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT u.id, r.id, u.filename, u.sha256, u.size, u.tryst_schema, u.schema_status, u.rows_total, u.encounters,
			u.media_count, u.uploaded_at, r.deleted_at IS NOT NULL
			FROM revision r JOIN upload u ON u.id = r.upload_id WHERE r.owner_id = ? ORDER BY r.created_at DESC, r.rowid DESC`, owner)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var u UploadInfo
			var at int64
			var st int
			if err := rows.Scan(&u.ID, &u.RevisionID, &u.Filename, &u.SHA256, &u.Size, &u.TrystSchema, &st, &u.Rows, &u.Encounters, &u.Media, &at, &u.Deleted); err != nil {
				return err
			}
			u.SchemaStatus, u.UploadedAt = trystbk.SchemaSupport(st), time.Unix(at, 0).UTC()
			out = append(out, u)
		}
		return rows.Err()
	})
	for i := range out {
		if !out[i].Deleted {
			out[i].Head = true
			break
		}
	}
	return out, err
}

// CacheRows returns the cached rows of one table for owner, in file order, as raw JSON objects.
func (s *Service) CacheRows(ctx context.Context, owner, table string) ([]string, error) {
	var out []string
	err := s.St.View(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT data FROM cache_row WHERE owner_id = ? AND tbl = ? ORDER BY pos`, owner, table)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d string
			if err := rows.Scan(&d); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// ClearCache removes an owner's derived rows and search index (the cache for nothing: used when
// the owner has no current revision, or when it cannot be rebuilt right now).
func (s *Service) ClearCache(ctx context.Context, owner string) error {
	return s.St.Update(ctx, true, func(tx *sql.Tx) error { return clearCache(tx, owner) })
}

func clearCache(tx *sql.Tx, owner string) error {
	for _, q := range []string{`DELETE FROM cache_row WHERE owner_id = ?`, `DELETE FROM search WHERE owner_id = ?`, `DELETE FROM cache_head WHERE owner_id = ?`} {
		if _, err := tx.Exec(q, owner); err != nil {
			return err
		}
	}
	return nil
}

// Refresh brings an owner's derived data in line with the newest active revision after a delete or
// a restore. With no active revision the cache is cleared. When the cache cannot be rebuilt (no
// remembered password) it is cleared too, never left showing a revision that is no longer current;
// rebuilt reports whether it was refilled.
func (s *Service) Refresh(ctx context.Context, owner string) (rebuilt bool, err error) {
	err = s.Rebuild(ctx, owner)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrNoRevision), errors.Is(err, ErrNoPassword):
		return false, s.ClearCache(ctx, owner)
	default:
		return false, err
	}
}

// OpenUpload opens one of owner's stored uploads for download, verifying its SHA-256 (N6). The
// bytes are exactly what Tryst wrote, still encrypted with the owner's own backup password.
func (s *Service) OpenUpload(ctx context.Context, owner, uploadID string) (*os.File, UploadInfo, error) {
	var u UploadInfo
	var at int64
	var st int
	err := s.St.View(ctx, func(tx *sql.Tx) error {
		e := tx.QueryRow(`SELECT id, filename, sha256, size, tryst_schema, schema_status, rows_total, encounters, media_count, uploaded_at
			FROM upload WHERE id = ? AND owner_id = ?`, uploadID, owner).Scan(&u.ID, &u.Filename, &u.SHA256, &u.Size, &u.TrystSchema, &st, &u.Rows, &u.Encounters, &u.Media, &at)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNoRevision
		}
		return e
	})
	if err != nil {
		return nil, u, err
	}
	u.SchemaStatus, u.UploadedAt = trystbk.SchemaSupport(st), time.Unix(at, 0).UTC()
	f, err := s.Blobs.OpenUpload(owner, u.ID, u.SHA256)
	return f, u, err
}
