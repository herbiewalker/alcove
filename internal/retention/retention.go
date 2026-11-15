// Package retention implements soft delete, restore and the real purge (D-8, D-30, F16, N13).
//
// Nothing is deleted at once. A person's own deleted upload can be restored by them for the user
// window (10 days by default) and by the admin for the admin window (45 days). Past the admin
// window, Purge deletes for real: the stored file, the rows, the search index, the remembered
// password and, for a deleted account, its data key. It also sweeps the leftovers a crash could
// leave between "the rows were removed" and "the files were removed", so a purge always finishes.
package retention

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/herbiewalker/alcove/internal/adminlog"
	"github.com/herbiewalker/alcove/internal/blobstore"
	"github.com/herbiewalker/alcove/internal/edit"
	"github.com/herbiewalker/alcove/internal/ingest"
	"github.com/herbiewalker/alcove/internal/store"
)

// Default windows (D-8).
const (
	DefaultUserWindow  = 10 * 24 * time.Hour
	DefaultAdminWindow = 45 * 24 * time.Hour

	// orphanAge is how old a stray file must be before the sweep removes it. An upload is written
	// to disk before its row exists, so a young file may simply be an upload in progress.
	orphanAge = 24 * time.Hour
)

// Errors.
var (
	ErrNotFound = errors.New("retention: no such upload")
	ErrExpired  = errors.New("retention: the recovery window for this upload has ended")
	ErrNotYours = errors.New("retention: that upload belongs to someone else")
)

// Windows are the two recovery periods.
type Windows struct{ User, Admin time.Duration }

// Normalize fills defaults and keeps the admin window at least as long as the user's.
func (w Windows) Normalize() Windows {
	if w.User <= 0 {
		w.User = DefaultUserWindow
	}
	if w.Admin <= 0 {
		w.Admin = DefaultAdminWindow
	}
	if w.Admin < w.User {
		w.Admin = w.User
	}
	return w
}

// Service does the work for one store.
type Service struct {
	St    *store.Store
	Blobs *blobstore.Store
	Ing   *ingest.Service
	Now   func() time.Time
	W     Windows
}

// New builds a service with the default windows.
func New(st *store.Store, blobs *blobstore.Store, ing *ingest.Service) *Service {
	return &Service{St: st, Blobs: blobs, Ing: ing, Now: time.Now, W: Windows{}.Normalize()}
}

func (s *Service) windows() Windows { return s.W.Normalize() }

// Outcome says what happened to the cache after a delete or restore.
type Outcome struct {
	CacheRebuilt bool // the owner's data was refilled from the newest active upload
	NowEmpty     bool // there is no active upload left
}

// Deleted describes one soft-deleted upload.
type Deleted struct {
	Owner, RevisionID, UploadID, Filename string
	Size                                  int64
	Entries                               int
	DeletedAt, Until                      time.Time // Until is when the relevant window ends
}

// DeleteUpload soft-deletes one of owner's uploads.
func (s *Service) DeleteUpload(ctx context.Context, owner, revisionID string) (Outcome, error) {
	now := s.Now()
	err := s.St.Update(ctx, true, func(tx *sql.Tx) error {
		var o, kind string
		var del sql.NullInt64
		if e := tx.QueryRow(`SELECT owner_id, deleted_at, kind FROM revision WHERE id = ?`, revisionID).Scan(&o, &del, &kind); errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		} else if e != nil {
			return e
		} else if kind != "upload" {
			return ErrNotFound
		}
		if o != owner {
			return ErrNotYours
		}
		if del.Valid {
			return nil // already deleted: idempotent
		}
		// The edits made on this upload go with it (D-42).
		_, e := tx.Exec(`UPDATE revision SET deleted_at = ? WHERE upload_id = (SELECT upload_id FROM revision WHERE id = ?) AND deleted_at IS NULL`, now.Unix(), revisionID)
		return e
	})
	if err != nil {
		return Outcome{}, err
	}
	return s.refresh(ctx, owner)
}

func (s *Service) refresh(ctx context.Context, owner string) (Outcome, error) {
	rebuilt, err := s.Ing.Refresh(ctx, owner)
	if err != nil {
		return Outcome{}, err
	}
	out := Outcome{CacheRebuilt: rebuilt}
	err = s.St.View(ctx, func(tx *sql.Tx) error {
		var n int
		if e := tx.QueryRow(`SELECT COUNT(*) FROM revision WHERE owner_id = ? AND deleted_at IS NULL AND kind = 'upload'`, owner).Scan(&n); e != nil {
			return e
		}
		out.NowEmpty = n == 0
		return nil
	})
	return out, err
}

// RestoreUpload brings a soft-deleted upload back. A person may restore their own within the user
// window; the admin (byAdmin) may restore anyone's within the admin window.
func (s *Service) RestoreUpload(ctx context.Context, owner, revisionID string, byAdmin bool) (Outcome, error) {
	now, w := s.Now(), s.windows()
	err := s.St.Update(ctx, true, func(tx *sql.Tx) error {
		var o, kind string
		var del sql.NullInt64
		if e := tx.QueryRow(`SELECT owner_id, deleted_at, kind FROM revision WHERE id = ?`, revisionID).Scan(&o, &del, &kind); errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		} else if e != nil {
			return e
		} else if kind != "upload" {
			return ErrNotFound
		}
		if !byAdmin && o != owner {
			return ErrNotYours
		}
		if !del.Valid {
			return nil // nothing to restore
		}
		limit := w.User
		if byAdmin {
			limit = w.Admin
		}
		if now.Sub(time.Unix(del.Int64, 0)) > limit {
			return ErrExpired
		}
		owner = o
		_, e := tx.Exec(`UPDATE revision SET deleted_at = NULL WHERE upload_id = (SELECT upload_id FROM revision WHERE id = ?) AND deleted_at = ?`, revisionID, del.Int64)
		return e
	})
	if err != nil {
		return Outcome{}, err
	}
	return s.refresh(ctx, owner)
}

// ListDeleted returns soft-deleted uploads. For a person (admin false) that is their own within the
// user window. For the admin (owner empty) it is everyone's within the admin window.
func (s *Service) ListDeleted(ctx context.Context, owner string, admin bool) ([]Deleted, error) {
	now, w := s.Now(), s.windows()
	limit := w.User
	if admin {
		limit = w.Admin
	}
	var out []Deleted
	err := s.St.View(ctx, func(tx *sql.Tx) error {
		q := `SELECT r.owner_id, r.id, u.id, u.filename, u.size, u.encounters, r.deleted_at
			FROM revision r JOIN upload u ON u.id = r.upload_id WHERE r.deleted_at IS NOT NULL AND r.kind = 'upload'`
		var args []any
		if !admin || owner != "" {
			q += ` AND r.owner_id = ?`
			args = append(args, owner)
		}
		q += ` ORDER BY r.deleted_at DESC`
		rows, err := tx.Query(q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d Deleted
			var at int64
			if err := rows.Scan(&d.Owner, &d.RevisionID, &d.UploadID, &d.Filename, &d.Size, &d.Entries, &at); err != nil {
				return err
			}
			d.DeletedAt = time.Unix(at, 0).UTC()
			d.Until = d.DeletedAt.Add(limit)
			if now.After(d.Until) {
				continue
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// Result counts what a purge removed.
type Result struct {
	Uploads, Accounts, Spaces, Orphans int
	Entries                            int // deleted entries whose stored values were wiped (D-42)
}

// Any reports whether anything was removed.
func (r Result) Any() bool { return r.Uploads+r.Accounts+r.Spaces+r.Orphans+r.Entries > 0 }

// Fields renders the counts for the admin log (identifier characters only).
func (r Result) Fields() string {
	s := fmt.Sprintf("uploads:%d,accounts:%d,spaces:%d,orphans:%d", r.Uploads, r.Accounts, r.Spaces, r.Orphans)
	if r.Entries > 0 {
		s += fmt.Sprintf(",entries:%d", r.Entries)
	}
	return s
}

// Purge really deletes everything past the admin window, then sweeps crash leftovers. It is safe
// to run at any time and as often as you like. When anything was removed it writes one admin-log
// entry as the system (counts only).
func (s *Service) Purge(ctx context.Context) (Result, error) {
	var res Result
	now, w := s.Now(), s.windows()
	cutoff := now.Add(-w.Admin).Unix()

	// 1. Deleted uploads past the window: rows first, files after (so a crash leaves a harmless
	//    stray file for the sweep, never a row pointing at nothing).
	type victim struct{ owner, uploadID string }
	var uploads []victim
	if err := s.St.Update(ctx, true, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT r.owner_id, r.upload_id FROM revision r WHERE r.deleted_at IS NOT NULL AND r.deleted_at <= ?`, cutoff)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v victim
			if err := rows.Scan(&v.owner, &v.uploadID); err != nil {
				_ = rows.Close()
				return err
			}
			uploads = append(uploads, v)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, v := range uploads {
			if _, err := tx.Exec(`DELETE FROM change WHERE revision_id IN (SELECT id FROM revision WHERE upload_id = ?)`, v.uploadID); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM revision WHERE upload_id = ?`, v.uploadID); err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM upload WHERE id = ?`, v.uploadID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return res, err
	}
	for _, v := range uploads {
		if err := s.Blobs.Delete(v.owner, v.uploadID); err != nil {
			return res, err
		}
	}
	res.Uploads = len(uploads)

	// 1b. Deleted entries past the administrator window: wipe the stored values, keep the fact.
	if err := s.St.Update(ctx, true, func(tx *sql.Tx) error {
		n, e := edit.PurgeDeleted(tx, cutoff)
		res.Entries = n
		return e
	}); err != nil {
		return res, err
	}

	// 2. Deleted accounts past the window: everything they own, then the account.
	var gone []string
	if err := s.St.View(ctx, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id FROM user WHERE deleted_at IS NOT NULL AND deleted_at <= ?`, cutoff)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			gone = append(gone, id)
		}
		return rows.Err()
	}); err != nil {
		return res, err
	}
	for _, id := range gone {
		if err := s.purgeOwner(ctx, id); err != nil {
			return res, err
		}
		res.Accounts++
	}

	// 3. Deleted spaces past the window: the space, its memberships and its invites.
	ns, err := s.purgeSpaces(ctx, cutoff)
	if err != nil {
		return res, err
	}
	res.Spaces = ns

	// 4. Sweep what a crash could have left: files with no row, owners with no account.
	n, err := s.sweep(ctx, now)
	if err != nil {
		return res, err
	}
	res.Orphans = n

	if res.Any() {
		err = s.St.Update(ctx, true, func(tx *sql.Tx) error {
			_, e := adminlog.Append(tx, now, adminlog.System, "retention.purge", "retention", "all", res.Fields())
			return e
		})
	}
	return res, err
}

// purgeOwner removes every trace of one account's data and then the account. The rows go first,
// in one transaction; the files and the data key go after, and the sweep finishes the job if the
// process dies in between.
func (s *Service) purgeOwner(ctx context.Context, owner string) error {
	err := s.St.Update(ctx, true, func(tx *sql.Tx) error {
		if err := leaveSpaces(tx, owner); err != nil {
			return err
		}
		for _, q := range []string{
			`DELETE FROM cache_row WHERE owner_id = ?`, `DELETE FROM cache_head WHERE owner_id = ?`,
			`DELETE FROM search WHERE owner_id = ?`, `DELETE FROM backup_password WHERE owner_id = ?`,
			`DELETE FROM entry_flag WHERE owner_id = ?`, `DELETE FROM media_blob WHERE owner_id = ?`, `DELETE FROM profile WHERE owner_id = ?`, `DELETE FROM user_tz WHERE user_id = ?`,
			`DELETE FROM change WHERE owner_id = ?`, `DELETE FROM revision WHERE owner_id = ?`, `DELETE FROM upload WHERE owner_id = ?`,
			`DELETE FROM user WHERE id = ?`,
		} {
			if _, err := tx.Exec(q, owner); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := s.Blobs.DeleteOwner(owner); err != nil {
		return err
	}
	if err := s.St.DestroyDataKey(owner); err != nil && !errors.Is(err, store.ErrNoDataKey) {
		return err
	}
	return nil
}

// sweep removes upload files with no row (older than a day) and the files and data keys of owners
// that no longer have an account.
func (s *Service) sweep(ctx context.Context, now time.Time) (int, error) {
	known := map[string]bool{}  // upload ids with a row
	owners := map[string]bool{} // accounts that exist
	keyed := map[string]bool{}  // owners with a data key
	err := s.St.View(ctx, func(tx *sql.Tx) error {
		for _, p := range []struct {
			q   string
			dst map[string]bool
		}{{`SELECT id FROM upload`, known}, {`SELECT id FROM user`, owners}, {`SELECT owner_id FROM data_key`, keyed}} {
			rows, err := tx.Query(p.q)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					_ = rows.Close()
					return err
				}
				p.dst[id] = true
			}
			if err := rows.Close(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	n := 0
	root := filepath.Join(s.St.DataDir(), "blobs")
	ents, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		owner := e.Name()
		if !owners[owner] {
			if old, _ := dirOlderThan(filepath.Join(root, owner), now, orphanAge); old {
				if err := s.Blobs.DeleteOwner(owner); err != nil {
					return n, err
				}
				n++
			}
			continue
		}
		up, err := os.ReadDir(filepath.Join(root, owner, "u"))
		if err != nil {
			continue
		}
		for _, f := range up {
			if f.IsDir() || strings.HasPrefix(f.Name(), ".incoming-") || known[f.Name()] {
				continue
			}
			if info, err := f.Info(); err == nil && now.Sub(info.ModTime()) > orphanAge {
				if err := s.Blobs.Delete(owner, f.Name()); err != nil {
					return n, err
				}
				n++
			}
		}
	}
	for owner := range keyed {
		if owners[owner] {
			continue
		}
		if err := s.St.DestroyDataKeyIfOlderThan(owner, now.Add(-orphanAge)); err == nil {
			n++
		}
	}
	return n, nil
}

// dirOlderThan reports whether everything in dir was last modified before now-age.
func dirOlderThan(dir string, now time.Time, age time.Duration) (bool, error) {
	old := true
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if info, e := d.Info(); e == nil && now.Sub(info.ModTime()) <= age {
			old = false
		}
		return nil
	})
	return old, err
}

// purgeSpaces removes spaces deleted before cutoff, with their memberships and invites.
func (s *Service) purgeSpaces(ctx context.Context, cutoff int64) (int, error) {
	n := 0
	err := s.St.Update(ctx, true, func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT id FROM space WHERE deleted_at IS NOT NULL AND deleted_at <= ?`, cutoff)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, id := range ids {
			if err := removeSpace(tx, id); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

func removeSpace(tx *sql.Tx, id string) error {
	for _, q := range []string{`DELETE FROM invite WHERE space_id = ?`, `DELETE FROM membership WHERE space_id = ?`, `DELETE FROM space WHERE id = ?`} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return nil
}

// leaveSpaces removes a purged account from every space (D-31). A space it solely owned passes to
// the longest-standing active member, or is removed when nobody is left.
func leaveSpaces(tx *sql.Tx, owner string) error {
	rows, err := tx.Query(`SELECT space_id FROM membership WHERE user_id = ? AND role = 'owner' AND state = 'active'`, owner)
	if err != nil {
		return err
	}
	var owned []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		owned = append(owned, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range owned {
		var heir string
		err := tx.QueryRow(`SELECT m.user_id FROM membership m JOIN user u ON u.id = m.user_id
			WHERE m.space_id = ? AND m.user_id <> ? AND m.state = 'active' AND u.status = 'active' AND u.deleted_at IS NULL
			ORDER BY m.joined_at, m.created_at LIMIT 1`, id, owner).Scan(&heir)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if err := removeSpace(tx, id); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			if _, err := tx.Exec(`UPDATE membership SET role = 'owner' WHERE space_id = ? AND user_id = ?`, id, heir); err != nil {
				return err
			}
		}
	}
	for _, q := range []string{`DELETE FROM membership WHERE user_id = ?`, `DELETE FROM invite WHERE created_by = ?`} {
		if _, err := tx.Exec(q, owner); err != nil {
			return err
		}
	}
	return nil
}
