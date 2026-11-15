package retention

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/adminlog"
	"github.com/herbiewalker/alcove/internal/auth"
	"github.com/herbiewalker/alcove/internal/blobstore"
	"github.com/herbiewalker/alcove/internal/edit"
	"github.com/herbiewalker/alcove/internal/ingest"
	"github.com/herbiewalker/alcove/internal/keyfile"
	"github.com/herbiewalker/alcove/internal/spaces"
	"github.com/herbiewalker/alcove/internal/store"
	"github.com/herbiewalker/alcove/internal/trystbk/synth"
)

var t0 = time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)

type rig struct {
	t     *testing.T
	st    *store.Store
	ing   *ingest.Service
	svc   *Service
	blobs *blobstore.Store
	clock time.Time
}

const day = 24 * time.Hour

func newRig(t *testing.T) *rig {
	t.Helper()
	k, _, err := keyfile.Generate()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	st, err := store.Open(store.Config{DataDir: filepath.Join(root, "data"), TmpDir: filepath.Join(root, "tmp"), Key: k, AllowNonTmpfsTmp: true, SealEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := &rig{t: t, st: st, clock: t0, blobs: blobstore.New(st.DataDir())}
	r.ing = ingest.New(st, r.blobs)
	r.ing.Now = func() time.Time { return r.clock }
	r.svc = New(st, r.blobs, r.ing)
	r.svc.Now = func() time.Time { return r.clock }
	r.addUser("ualice")
	r.addUser("ubob")
	return r
}

func (r *rig) addUser(id string) {
	r.t.Helper()
	err := r.st.Update(context.Background(), true, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO user(id, username, display_name, role, status, pw_hash, created_at) VALUES (?,?,?,?,?,?,?)`,
			id, id, id, "member", "active", "h", r.clock.Unix())
		return err
	})
	if err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) upload(owner string, seed uint64, entries int, remember bool) ingest.UploadInfo {
	r.t.Helper()
	res, err := synth.Build(synth.Options{Seed: seed, Encounters: entries, Password: "pw-" + owner})
	if err != nil {
		r.t.Fatal(err)
	}
	info, err := r.ing.Upload(context.Background(), ingest.Params{Owner: owner, CreatedBy: owner, Filename: "f.tryst", Password: res.Password, Remember: remember}, bytes.NewReader(res.File))
	if err != nil {
		r.t.Fatalf("upload: %v", err)
	}
	return info
}

func (r *rig) count(q string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.st.View(context.Background(), func(tx *sql.Tx) error { return tx.QueryRow(q, args...).Scan(&n) }); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) blobFiles(owner string) []string {
	var out []string
	_ = filepath.WalkDir(filepath.Join(r.st.DataDir(), "blobs", owner), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func (r *rig) cachedEntries(owner string) int {
	rows, err := r.ing.CacheRows(context.Background(), owner, "encounters")
	if err != nil {
		r.t.Fatal(err)
	}
	return len(rows)
}

func TestWindowsNormalize(t *testing.T) {
	if w := (Windows{}).Normalize(); w.User != 10*day || w.Admin != 45*day {
		t.Fatalf("defaults: %+v", w)
	}
	if w := (Windows{User: 20 * day, Admin: 5 * day}).Normalize(); w.Admin != 20*day {
		t.Fatalf("the admin window must never be shorter than the user's: %+v", w)
	}
}

func TestDeletingTheHeadFallsBackToThePreviousUpload(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a := r.upload("ualice", 1, 5, true)
	r.clock = r.clock.Add(time.Hour)
	b := r.upload("ualice", 2, 8, true)
	if r.cachedEntries("ualice") != 8 {
		t.Fatal("setup: the head should be the second upload")
	}
	out, err := r.svc.DeleteUpload(ctx, "ualice", b.RevisionID)
	if err != nil || !out.CacheRebuilt || out.NowEmpty {
		t.Fatalf("delete head: %+v %v", out, err)
	}
	if got := r.cachedEntries("ualice"); got != 5 {
		t.Fatalf("after deleting the head the cache must hold the previous upload (5), got %d", got)
	}
	if r.count("SELECT COUNT(*) FROM search WHERE owner_id = 'ualice' AND kind = 'encounter'") > 5 {
		t.Error("search rows of the deleted upload remain")
	}
	list, _ := r.ing.List(ctx, "ualice")
	if !list[0].Deleted || list[1].ID != a.ID || !list[1].Head {
		t.Fatalf("list: %+v", list)
	}
	// Restore brings the newer one back as head.
	out, err = r.svc.RestoreUpload(ctx, "ualice", b.RevisionID, false)
	if err != nil || !out.CacheRebuilt || r.cachedEntries("ualice") != 8 {
		t.Fatalf("restore: %+v %v entries=%d", out, err, r.cachedEntries("ualice"))
	}
}

func TestDeletingTheOnlyUploadClearsEverythingDerived(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	res, berr := synth.Build(synth.Options{Seed: 3, Encounters: 6, Password: "pw-ualice"})
	if berr != nil {
		t.Fatal(berr)
	}
	a, uerr := r.ing.Upload(ctx, ingest.Params{Owner: "ualice", CreatedBy: "ualice", Filename: "f.tryst", Password: res.Password, Remember: true}, bytes.NewReader(res.File))
	if uerr != nil {
		t.Fatal(uerr)
	}
	out, err := r.svc.DeleteUpload(ctx, "ualice", a.RevisionID)
	if err != nil || !out.NowEmpty || out.CacheRebuilt {
		t.Fatalf("%+v %v", out, err)
	}
	for _, q := range []string{"SELECT COUNT(*) FROM cache_row", "SELECT COUNT(*) FROM cache_head", "SELECT COUNT(*) FROM search"} {
		if n := r.count(q); n != 0 {
			t.Errorf("%s = %d: a deleted upload is still readable", q, n)
		}
	}
	// The deleted file is still on disk (soft delete) and still counts for the duplicate check.
	if len(r.blobFiles("ualice")) != 1 {
		t.Fatal("a soft delete must keep the file")
	}
	_, err = r.ing.Upload(ctx, ingest.Params{Owner: "ualice", CreatedBy: "ualice", Filename: "again", Password: res.Password}, bytes.NewReader(res.File))
	var dup *ingest.DuplicateError
	if !errors.As(err, &dup) || !dup.Deleted || !strings.Contains(dup.Error(), "Recently deleted") {
		t.Fatalf("re-uploading a deleted file: %v", err)
	}
}

func TestDeletingWithoutARememberedPasswordNeverShowsADeletedUpload(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.upload("ualice", 4, 4, true)
	r.clock = r.clock.Add(time.Hour)
	b := r.upload("ualice", 5, 9, false) // the head, and the password is no longer remembered
	out, err := r.svc.DeleteUpload(ctx, "ualice", b.RevisionID)
	if err != nil || out.CacheRebuilt || out.NowEmpty {
		t.Fatalf("%+v %v", out, err)
	}
	if n := r.cachedEntries("ualice"); n != 0 {
		t.Fatalf("with no password to rebuild from, the cache must be cleared, not left at %d rows", n)
	}
}

func TestRestoreWindows(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a := r.upload("ualice", 6, 3, true)
	if _, err := r.svc.DeleteUpload(ctx, "ualice", a.RevisionID); err != nil {
		t.Fatal(err)
	}
	// Someone else cannot touch it.
	if _, err := r.svc.DeleteUpload(ctx, "ubob", a.RevisionID); !errors.Is(err, ErrNotYours) {
		t.Fatalf("bob deleting alice's upload: %v", err)
	}
	if _, err := r.svc.RestoreUpload(ctx, "ubob", a.RevisionID, false); !errors.Is(err, ErrNotYours) {
		t.Fatalf("bob restoring alice's upload: %v", err)
	}
	if _, err := r.svc.RestoreUpload(ctx, "ualice", "rv-nope", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown revision: %v", err)
	}

	// Day 11: past the user window, inside the admin window.
	r.clock = t0.Add(11 * day)
	if _, err := r.svc.RestoreUpload(ctx, "ualice", a.RevisionID, false); !errors.Is(err, ErrExpired) {
		t.Fatalf("a person restoring on day 11: %v", err)
	}
	// The person no longer sees it, the admin still does.
	if l, _ := r.svc.ListDeleted(ctx, "ualice", false); len(l) != 0 {
		t.Errorf("the user's bin on day 11: %+v", l)
	}
	l, _ := r.svc.ListDeleted(ctx, "", true)
	if len(l) != 1 || l[0].Owner != "ualice" || !l[0].Until.Equal(t0.Add(45*day)) {
		t.Fatalf("the admin's bin on day 11: %+v", l)
	}
	// Day 46: past the admin window too.
	r.clock = t0.Add(46 * day)
	if _, err := r.svc.RestoreUpload(ctx, "", a.RevisionID, true); !errors.Is(err, ErrExpired) {
		t.Fatalf("the admin on day 46: %v", err)
	}
	if l, _ := r.svc.ListDeleted(ctx, "", true); len(l) != 0 {
		t.Errorf("the admin's bin on day 46: %+v", l)
	}
	// Day 20: the admin can.
	r.clock = t0.Add(20 * day)
	if out, err := r.svc.RestoreUpload(ctx, "", a.RevisionID, true); err != nil || !out.CacheRebuilt {
		t.Fatalf("the admin on day 20: %+v %v", out, err)
	}
	if r.cachedEntries("ualice") != 3 {
		t.Error("the restored upload is not the current data again")
	}
	// Restoring twice is harmless.
	if _, err := r.svc.RestoreUpload(ctx, "ualice", a.RevisionID, false); err != nil {
		t.Fatalf("second restore: %v", err)
	}
}

func TestListDeletedIsPerPersonForAPerson(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a := r.upload("ualice", 7, 2, true)
	b := r.upload("ubob", 8, 2, true)
	_, _ = r.svc.DeleteUpload(ctx, "ualice", a.RevisionID)
	_, _ = r.svc.DeleteUpload(ctx, "ubob", b.RevisionID)
	if l, _ := r.svc.ListDeleted(ctx, "ualice", false); len(l) != 1 || l[0].Owner != "ualice" {
		t.Fatalf("alice's bin: %+v", l)
	}
	if l, _ := r.svc.ListDeleted(ctx, "", true); len(l) != 2 {
		t.Fatalf("the admin sees both: %+v", l)
	}
	if l, _ := r.svc.ListDeleted(ctx, "ubob", true); len(l) != 1 || l[0].Owner != "ubob" {
		t.Fatalf("the admin filtered to bob: %+v", l)
	}
}

// N13: after the window the data is really gone.
func TestPurgeRemovesAnUploadForRealAfterTheAdminWindow(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	keep := r.upload("ualice", 9, 4, true)
	r.clock = r.clock.Add(time.Hour)
	gone := r.upload("ualice", 10, 7, true)
	if _, err := r.svc.DeleteUpload(ctx, "ualice", gone.RevisionID); err != nil {
		t.Fatal(err)
	}
	if len(r.blobFiles("ualice")) != 2 {
		t.Fatal("setup")
	}

	r.clock = t0.Add(time.Hour).Add(44 * day)
	if res, err := r.svc.Purge(ctx); err != nil || res.Any() {
		t.Fatalf("a purge on day 44 must remove nothing: %+v %v", res, err)
	}
	r.clock = t0.Add(time.Hour).Add(46 * day)
	res, err := r.svc.Purge(ctx)
	if err != nil || res.Uploads != 1 || res.Accounts != 0 || res.Orphans != 0 {
		t.Fatalf("purge: %+v %v", res, err)
	}
	files := r.blobFiles("ualice")
	if len(files) != 1 || !strings.HasSuffix(files[0], keep.ID) {
		t.Fatalf("the purged file is still on disk, or the wrong one went: %v", files)
	}
	if r.count("SELECT COUNT(*) FROM upload WHERE id = ?", gone.ID) != 0 || r.count("SELECT COUNT(*) FROM revision WHERE id = ?", gone.RevisionID) != 0 {
		t.Error("the purged upload's rows are still there")
	}
	if r.count("SELECT COUNT(*) FROM upload WHERE id = ?", keep.ID) != 1 || r.cachedEntries("ualice") != 4 {
		t.Error("the purge touched the upload it should have kept")
	}
	// The log line has counts and nothing else; a second purge does nothing.
	var action, fields, actor string
	_ = r.st.View(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT action, fields, actor FROM admin_log ORDER BY seq DESC LIMIT 1`).Scan(&action, &fields, &actor)
	})
	if action != "retention.purge" || fields != "uploads:1,accounts:0,spaces:0,orphans:0" || actor != adminlog.System {
		t.Errorf("log entry: %s %s %s", action, fields, actor)
	}
	if res, _ := r.svc.Purge(ctx); res.Any() {
		t.Errorf("a second purge removed %+v", res)
	}
	var vres adminlog.Result
	var verr error
	_ = r.st.View(ctx, func(tx *sql.Tx) error { vres, verr = adminlog.Verify(tx); return nil })
	if verr != nil || vres.Entries == 0 {
		t.Errorf("the admin log chain after a purge: %+v %v", vres, verr)
	}
}

func TestPurgeRemovesADeletedAccountAndItsKey(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.upload("ualice", 11, 5, true)
	r.upload("ubob", 12, 6, true)
	if err := r.st.Update(ctx, false, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO entry_flag(owner_id, entry_id, private, updated_at) VALUES ('ualice','e1',1,0), ('ubob','e2',1,0)`)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO profile(owner_id, field, value, shared, updated_at) VALUES ('ualice','sex','x',1,0), ('ubob','sex','y',1,0)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Alice is disabled, then deleted.
	_ = r.st.Update(ctx, true, func(tx *sql.Tx) error {
		if err := auth.SetStatus(tx, "ualice", auth.StatusDisabled); err != nil {
			return err
		}
		return auth.MarkDeleted(tx, "ualice", r.clock)
	})
	r.clock = t0.Add(44 * day)
	if res, _ := r.svc.Purge(ctx); res.Any() {
		t.Fatalf("a purge on day 44 removed %+v", res)
	}
	if _, err := r.st.DataKey("ualice"); err != nil {
		t.Fatalf("the data key must survive until the purge: %v", err)
	}

	r.clock = t0.Add(46 * day)
	res, err := r.svc.Purge(ctx)
	if err != nil || res.Accounts != 1 || res.Orphans != 0 {
		t.Fatalf("purge: %+v %v", res, err)
	}
	for _, q := range []string{
		"SELECT COUNT(*) FROM user WHERE id = 'ualice'", "SELECT COUNT(*) FROM upload WHERE owner_id = 'ualice'",
		"SELECT COUNT(*) FROM revision WHERE owner_id = 'ualice'", "SELECT COUNT(*) FROM cache_row WHERE owner_id = 'ualice'",
		"SELECT COUNT(*) FROM cache_head WHERE owner_id = 'ualice'", "SELECT COUNT(*) FROM search WHERE owner_id = 'ualice'",
		"SELECT COUNT(*) FROM backup_password WHERE owner_id = 'ualice'", "SELECT COUNT(*) FROM data_key WHERE owner_id = 'ualice'",
		"SELECT COUNT(*) FROM entry_flag WHERE owner_id = 'ualice'", "SELECT COUNT(*) FROM profile WHERE owner_id = 'ualice'",
	} {
		if n := r.count(q); n != 0 {
			t.Errorf("%s = %d after the purge", q, n)
		}
	}
	if len(r.blobFiles("ualice")) != 0 {
		t.Error("alice's files are still on disk")
	}
	if _, err := os.Stat(filepath.Join(r.st.DataDir(), "blobs", "ualice")); err == nil {
		t.Error("alice's folder is still on disk")
	}
	if _, err := r.st.DataKey("ualice"); !errors.Is(err, store.ErrNoDataKey) {
		t.Errorf("alice's data key survived: %v", err)
	}
	// Bob is untouched.
	if r.count("SELECT COUNT(*) FROM user WHERE id = 'ubob'") != 1 || len(r.blobFiles("ubob")) != 1 || r.cachedEntries("ubob") != 6 {
		t.Error("the purge disturbed another account")
	}
	if _, err := r.st.DataKey("ubob"); err != nil {
		t.Errorf("bob's key: %v", err)
	}
	if _, err := r.ing.StoredPassword(ctx, "ubob"); err != nil {
		t.Errorf("bob's remembered password: %v", err)
	}
}

func TestAnAccountThatWasNotDeletedIsNeverPurged(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.upload("ualice", 13, 3, true)
	_ = r.st.Update(ctx, true, func(tx *sql.Tx) error { return auth.SetStatus(tx, "ualice", auth.StatusDisabled) })
	r.clock = t0.Add(400 * day)
	if res, err := r.svc.Purge(ctx); err != nil || res.Any() {
		t.Fatalf("a merely disabled account was purged: %+v %v", res, err)
	}
	if r.count("SELECT COUNT(*) FROM user WHERE id = 'ualice'") != 1 || len(r.blobFiles("ualice")) != 1 {
		t.Fatal("disabled data was removed")
	}
}

// A crash between "the rows were removed" and "the files were removed" must not leave data behind.
func TestSweepFinishesWhatACrashLeft(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a := r.upload("ualice", 14, 3, true)
	old := t0.Add(-3 * day)
	age := func(p string) {
		mode := os.FileMode(0o600)
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			mode = 0o700 // a directory without the execute bit cannot be read
		}
		_ = os.Chmod(p, mode)
		_ = os.Chtimes(p, old, old)
	}

	udir := filepath.Join(r.st.DataDir(), "blobs", "ualice", "u")
	stray := filepath.Join(udir, "upstray0000")
	young := filepath.Join(udir, "upyoung0000")
	partial := filepath.Join(udir, ".incoming-123")
	for _, p := range []string{stray, young, partial} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	age(stray)
	age(partial)
	// An owner whose account no longer exists: files and a key.
	ghost := filepath.Join(r.st.DataDir(), "blobs", "ughost", "u")
	_ = os.MkdirAll(ghost, 0o700)
	_ = os.WriteFile(filepath.Join(ghost, "upold"), []byte("x"), 0o600)
	age(filepath.Join(ghost, "upold"))
	age(ghost)
	age(filepath.Dir(ghost))
	if err := r.st.CreateDataKey("ughost"); err != nil {
		t.Fatal(err)
	}
	_ = r.st.Update(ctx, true, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE data_key SET created_at = ? WHERE owner_id = 'ughost'`, old.Unix())
		return err
	})
	// A brand-new owner's key must not be swept (an account may be mid-creation).
	if err := r.st.CreateDataKey("unewbie"); err != nil {
		t.Fatal(err)
	}

	res, err := r.svc.Purge(ctx)
	if err != nil || res.Orphans < 3 {
		t.Fatalf("purge: %+v %v", res, err)
	}
	for _, p := range []string{stray, filepath.Join(ghost, "upold")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived the sweep", p)
		}
	}
	for _, p := range []string{young, partial, filepath.Join(udir, a.ID)} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was swept but must be kept: %v", p, err)
		}
	}
	if _, err := r.st.DataKey("ughost"); !errors.Is(err, store.ErrNoDataKey) {
		t.Errorf("the ghost's data key survived: %v", err)
	}
	if _, err := r.st.DataKey("unewbie"); err != nil {
		t.Errorf("a new key was swept: %v", err)
	}
	if _, err := r.st.DataKey("ualice"); err != nil {
		t.Errorf("an existing account's key was swept: %v", err)
	}
	// And the accounts' real upload still reads.
	if r.cachedEntries("ualice") != 3 {
		t.Error("the sweep damaged live data")
	}
}

// ---- spaces (D-31) ----

func (r *rig) newSpace(owner, name string) spaces.Space {
	r.t.Helper()
	var s spaces.Space
	if err := r.st.Update(context.Background(), true, func(tx *sql.Tx) (err error) { s, err = spaces.Create(tx, r.clock, name, owner); return }); err != nil {
		r.t.Fatal(err)
	}
	return s
}

func (r *rig) joinSpace(space, user string) {
	r.t.Helper()
	if err := r.st.Update(context.Background(), true, func(tx *sql.Tx) error { return spaces.JoinViaInvite(tx, r.clock, space, user, true) }); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) gate(space, user string) (string, bool) {
	var role string
	var ok bool
	_ = r.st.View(context.Background(), func(tx *sql.Tx) error { role, ok = spaces.Role(tx, space, user); return nil })
	return role, ok
}

func (r *rig) deleteAccount(id string) {
	r.t.Helper()
	if err := r.st.Update(context.Background(), true, func(tx *sql.Tx) error {
		if err := auth.SetStatus(tx, id, auth.StatusDisabled); err != nil {
			return err
		}
		return auth.MarkDeleted(tx, id, r.clock)
	}); err != nil {
		r.t.Fatal(err)
	}
}

func TestPurgeRemovesDeletedSpacesAfterTheWindow(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	gone := r.newSpace("ualice", "Gone")
	kept := r.newSpace("ualice", "Kept")
	r.joinSpace(gone.ID, "ubob")
	_ = r.st.Update(ctx, true, func(tx *sql.Tx) error {
		if _, _, err := spaces.CreateInvite(tx, r.clock, spaces.KindSpace, gone.ID, "ualice", "", 0); err != nil {
			return err
		}
		return spaces.Delete(tx, r.clock, gone.ID)
	})
	r.clock = t0.Add(44 * day)
	if res, _ := r.svc.Purge(ctx); res.Spaces != 0 {
		t.Fatalf("a purge inside the window removed a space: %+v", res)
	}
	r.clock = t0.Add(46 * day)
	res, err := r.svc.Purge(ctx)
	if err != nil || res.Spaces != 1 || res.Orphans != 0 {
		t.Fatalf("purge: %+v %v", res, err)
	}
	for _, q := range []string{"SELECT COUNT(*) FROM space WHERE id = '" + gone.ID + "'", "SELECT COUNT(*) FROM membership WHERE space_id = '" + gone.ID + "'",
		"SELECT COUNT(*) FROM invite WHERE space_id = '" + gone.ID + "'"} {
		if n := r.count(q); n != 0 {
			t.Errorf("%s = %d after the purge", q, n)
		}
	}
	if _, ok := r.gate(kept.ID, "ualice"); !ok {
		t.Error("the purge removed a live space")
	}
}

func TestPurgingAnAccountRemovesItFromSpacesAndHandsOnOwnership(t *testing.T) {
	r := newRig(t)
	r.addUser("ucarol")
	r.addUser("udave")
	ctx := context.Background()

	// Alice owns a space with Bob (joined first) and Carol; she also belongs to Dave's space.
	ours := r.newSpace("ualice", "Ours")
	r.joinSpace(ours.ID, "ubob")
	r.clock = r.clock.Add(time.Hour)
	r.joinSpace(ours.ID, "ucarol")
	daves := r.newSpace("udave", "Daves")
	r.joinSpace(daves.ID, "ualice")
	// Alice's own invite to her space, and a space only she is in.
	_ = r.st.Update(ctx, true, func(tx *sql.Tx) error {
		_, _, err := spaces.CreateInvite(tx, r.clock, spaces.KindSpace, ours.ID, "ualice", "stale", 0)
		return err
	})
	lonely := r.newSpace("ualice", "Lonely")

	r.deleteAccount("ualice")
	r.clock = t0.Add(46 * day)
	res, err := r.svc.Purge(ctx)
	if err != nil || res.Accounts != 1 {
		t.Fatalf("purge: %+v %v", res, err)
	}
	// Ownership passed to the longest-standing active member (Bob), and Carol is still in.
	if role, ok := r.gate(ours.ID, "ubob"); !ok || role != spaces.RoleOwner {
		t.Errorf("Bob after the hand-on: %q %v", role, ok)
	}
	if role, ok := r.gate(ours.ID, "ucarol"); !ok || role != spaces.RoleMember {
		t.Errorf("Carol after the hand-on: %q %v", role, ok)
	}
	if n := r.count(`SELECT COUNT(*) FROM membership WHERE space_id = ? AND role = 'owner' AND state = 'active'`, ours.ID); n != 1 {
		t.Errorf("%d owners", n)
	}
	// Nothing of Alice's is left: memberships, her invites, the space only she was in.
	if n := r.count(`SELECT COUNT(*) FROM membership WHERE user_id = 'ualice'`); n != 0 {
		t.Errorf("her memberships remain: %d", n)
	}
	if n := r.count(`SELECT COUNT(*) FROM invite WHERE created_by = 'ualice'`); n != 0 {
		t.Errorf("her invites remain: %d", n)
	}
	if n := r.count(`SELECT COUNT(*) FROM space WHERE id = ?`, lonely.ID); n != 0 {
		t.Error("a space nobody is left in remains")
	}
	// Dave's space is untouched and has simply lost a member.
	if role, ok := r.gate(daves.ID, "udave"); !ok || role != spaces.RoleOwner {
		t.Error("another person's space was disturbed")
	}
}

// An edit lives with its upload (D-42): it comes back when its upload is the head again, goes with
// the upload when that is deleted, returns on restore, and is purged with it.
func TestEditsFollowTheirUploadThroughDeleteRestoreAndPurge(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a := r.upload("ualice", 31, 5, true)
	rows, _ := r.ing.CacheRows(ctx, "ualice", "encounters")
	var first map[string]json.RawMessage
	_ = json.Unmarshal([]byte(rows[0]), &first)
	var entry string
	_ = json.Unmarshal(first["id"], &entry)
	edited := func() bool {
		return r.count(`SELECT COUNT(*) FROM cache_row WHERE owner_id = 'ualice' AND data LIKE '%EDIT-MARK%'`) == 1
	}
	r.clock = r.clock.Add(time.Minute)
	var head string
	_ = r.st.View(ctx, func(tx *sql.Tx) (err error) { head, _, err = edit.Head(tx, "ualice"); return })
	if err := r.st.Update(ctx, true, func(tx *sql.Tx) error {
		_, _, e := edit.Apply(tx, r.clock, "ualice", "ualice", entry, head, map[string]string{"note": "EDIT-MARK"})
		return e
	}); err != nil || !edited() {
		t.Fatalf("setup: %v", err)
	}
	// A newer upload hides the edit (a new chain); deleting it brings the older chain, edit included, back.
	r.clock = r.clock.Add(time.Hour)
	b := r.upload("ualice", 32, 8, true)
	if edited() {
		t.Fatal("the edit shows in a newer upload")
	}
	if _, err := r.svc.DeleteUpload(ctx, "ualice", b.RevisionID); err != nil || !edited() {
		t.Fatalf("after deleting the newer upload the edit should be back: %v", err)
	}
	// Deleting the upload the edit sits on removes the edit's effect too, and restoring brings it back.
	if _, err := r.svc.DeleteUpload(ctx, "ualice", a.RevisionID); err != nil {
		t.Fatal(err)
	}
	if edited() || r.count(`SELECT COUNT(*) FROM revision WHERE owner_id = 'ualice' AND kind = 'edit' AND deleted_at IS NULL`) != 0 {
		t.Fatal("an edit stayed live after its upload was deleted")
	}
	list, _ := r.ing.List(ctx, "ualice")
	if len(list) != 2 {
		t.Errorf("the upload list shows %d rows, want the 2 uploads", len(list))
	}
	if deleted, _ := r.svc.ListDeleted(ctx, "ualice", false); len(deleted) != 2 {
		t.Errorf("recently deleted shows %d, want the 2 uploads", len(deleted))
	}
	if _, err := r.svc.RestoreUpload(ctx, "ualice", a.RevisionID, false); err != nil || !edited() {
		t.Fatalf("restoring the upload should bring its edit back: %v", err)
	}
	// An edit's revision id is not an upload: it cannot be deleted or restored as one.
	var editRev string
	_ = r.st.View(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id FROM revision WHERE owner_id = 'ualice' AND kind = 'edit'`).Scan(&editRev)
	})
	if _, err := r.svc.DeleteUpload(ctx, "ualice", editRev); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting an edit as an upload: %v", err)
	}
	// Purge removes the change rows with the upload.
	if _, err := r.svc.DeleteUpload(ctx, "ualice", a.RevisionID); err != nil {
		t.Fatal(err)
	}
	r.clock = r.clock.Add(46 * day)
	if res, err := r.svc.Purge(ctx); err != nil || res.Uploads < 1 {
		t.Fatalf("purge: %+v %v", res, err)
	}
	if r.count(`SELECT COUNT(*) FROM change`) != 0 || r.count(`SELECT COUNT(*) FROM revision WHERE kind = 'edit'`) != 0 {
		t.Error("the purge left the edit behind")
	}
}

// A deleted entry can be restored until the administrator's window ends; then its stored values are
// wiped (D-42). The purge reports it in the log as a count, nothing else.
func TestPurgeWipesDeletedEntriesAfterTheAdminWindow(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.upload("ualice", 41, 5, true)
	rows, _ := r.ing.CacheRows(ctx, "ualice", "encounters")
	var first map[string]json.RawMessage
	_ = json.Unmarshal([]byte(rows[0]), &first)
	var entry string
	_ = json.Unmarshal(first["id"], &entry)
	r.clock = r.clock.Add(time.Minute)
	var head string
	_ = r.st.View(ctx, func(tx *sql.Tx) (err error) { head, _, err = edit.Head(tx, "ualice"); return })
	if err := r.st.Update(ctx, true, func(tx *sql.Tx) error {
		_, e := edit.Delete(tx, r.clock, "ualice", "ualice", entry, head)
		return e
	}); err != nil {
		t.Fatal(err)
	}
	deletedAt := r.clock
	r.clock = deletedAt.Add(44 * day)
	if res, err := r.svc.Purge(ctx); err != nil || res.Any() {
		t.Fatalf("a purge on day 44 must remove nothing: %+v %v", res, err)
	}
	r.clock = deletedAt.Add(46 * day)
	res, err := r.svc.Purge(ctx)
	if err != nil || res.Entries != 1 || res.Uploads != 0 {
		t.Fatalf("purge: %+v %v", res, err)
	}
	if r.count(`SELECT COUNT(*) FROM change WHERE op = 'delete' AND old_value <> ''`) != 0 {
		t.Error("the deleted entry's values were not wiped")
	}
	if r.count(`SELECT COUNT(*) FROM change WHERE op = 'delete'`) == 0 {
		t.Error("the fact of the deletion was removed")
	}
	var fields string
	_ = r.st.View(ctx, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT fields FROM admin_log ORDER BY seq DESC LIMIT 1`).Scan(&fields)
	})
	if fields != "uploads:0,accounts:0,spaces:0,orphans:0,entries:1" {
		t.Errorf("log fields: %s", fields)
	}
	if res, _ := r.svc.Purge(ctx); res.Any() {
		t.Errorf("a second purge removed %+v", res)
	}
}
