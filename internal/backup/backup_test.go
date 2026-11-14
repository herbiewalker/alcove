package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/herbiewalker/alcove/internal/adminlog"
	"github.com/herbiewalker/alcove/internal/auth"
	"github.com/herbiewalker/alcove/internal/blobstore"
	"github.com/herbiewalker/alcove/internal/keyfile"
	"github.com/herbiewalker/alcove/internal/store"
)

var t0 = time.Date(2026, 11, 14, 9, 30, 0, 0, time.UTC)

const (
	uploadPayload = "TRYSTBK1-pretend-ciphertext-0123456789-upload-payload-0123456789"
	photoPlain    = "PHOTO-PLAINTEXT-SENTINEL-must-never-appear-in-a-backup"
	displaySecret = "Sentinel Display Name Zed"
)

type fixture struct {
	t      *testing.T
	st     *store.Store
	key    keyfile.Key
	data   string
	backup string
	blobs  *blobstore.Store
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	k, _, err := keyfile.Generate()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	f := &fixture{t: t, key: k, data: filepath.Join(root, "data"), backup: filepath.Join(root, "backups")}
	if err := os.MkdirAll(f.backup, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(store.Config{DataDir: f.data, TmpDir: filepath.Join(root, "tmp"), Key: k, AllowNonTmpfsTmp: true, SealEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f.st, f.blobs = st, blobstore.New(f.data)

	// An account, two log entries, one upload and one encrypted photo for the same owner.
	var u auth.User
	if err := st.Update(context.Background(), true, func(tx *sql.Tx) (err error) {
		if u, err = auth.Create(tx, t0, "alice", displaySecret, auth.RoleAdmin, "hash"); err != nil {
			return err
		}
		if _, err = adminlog.Append(tx, t0, u.ID, "admin.first_run", "user", u.ID, "username"); err != nil {
			return err
		}
		_, err = adminlog.Append(tx, t0.Add(time.Minute), u.ID, "session.login", "user", u.ID, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateDataKey(u.ID); err != nil {
		t.Fatal(err)
	}
	dek, err := st.DataKey(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.blobs.PutUpload(u.ID, "up1", strings.NewReader(uploadPayload), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.blobs.PutMedia(u.ID, "ph1", dek.Bytes(), strings.NewReader(photoPlain), 0); err != nil {
		t.Fatal(err)
	}
	dek.Zero()
	return f
}

func (f *fixture) create(at time.Time) Info {
	f.t.Helper()
	info, err := Create(f.st, f.backup, at)
	if err != nil {
		f.t.Fatalf("Create: %v", err)
	}
	return info
}

func readAll(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCreateVerifyRestoreRoundTrip(t *testing.T) {
	f := newFixture(t)
	info := f.create(t0)
	if !strings.HasPrefix(info.Name, Prefix+"20261114T093000Z-") || !strings.HasSuffix(info.Name, Suffix) || !info.Sidecar {
		t.Fatalf("name/sidecar: %+v", info)
	}
	if info.Files != 4 { // manifest, sealed, one upload, one photo
		t.Fatalf("files = %d", info.Files)
	}
	if st, _ := os.Stat(info.Path); st.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("backup file is world-readable: %v", st.Mode())
	}
	if _, err := Verify(info.Path); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	to := filepath.Join(t.TempDir(), "restored")
	if _, err := Restore(info.Path, to); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	for _, rel := range []string{"manifest.json", "alcove.sealed"} {
		if !bytes.Equal(readAll(t, filepath.Join(f.data, rel)), readAll(t, filepath.Join(to, rel))) {
			t.Errorf("%s differs after restore", rel)
		}
	}
	got := readAll(t, filepath.Join(to, "blobs", firstDir(t, filepath.Join(f.data, "blobs")), "u", "up1"))
	if string(got) != uploadPayload {
		t.Errorf("upload differs after restore: %q", got)
	}
	if _, err := os.Stat(filepath.Join(to, indexName)); err == nil {
		t.Error("BACKUP.json must not be extracted")
	}
}

func firstDir(t *testing.T, p string) string {
	t.Helper()
	ents, err := os.ReadDir(p)
	if err != nil || len(ents) == 0 {
		t.Fatalf("no entries in %s: %v", p, err)
	}
	return ents[0].Name()
}

func TestDrillProvesRestoreWithTheRightKey(t *testing.T) {
	f := newFixture(t)
	info := f.create(t0)
	scratch := t.TempDir()
	rep, err := Drill(info.Path, f.key, DrillOptions{Scratch: scratch, AllowNonTmpfs: true})
	if err != nil {
		t.Fatalf("Drill: %v (%v)", err, rep.Problems)
	}
	if rep.Users != 1 || rep.LogEntries != 2 || rep.UploadBlobs != 1 || rep.MediaBlobs != 1 || rep.UploadBytes != int64(len(uploadPayload)) {
		t.Fatalf("report: %+v", rep)
	}
	if ents, _ := os.ReadDir(scratch); len(ents) != 0 {
		t.Errorf("the drill left its scratch folder behind: %v", ents)
	}
}

func TestDrillWithAnotherKeyFailsCleanly(t *testing.T) {
	f := newFixture(t)
	info := f.create(t0)
	other, _, _ := keyfile.Generate()
	scratch := t.TempDir()
	if _, err := Drill(info.Path, other, DrillOptions{Scratch: scratch, AllowNonTmpfs: true}); err == nil || !strings.Contains(err.Error(), "does not open") {
		t.Fatalf("a wrong key must fail the drill: %v", err)
	}
	if ents, _ := os.ReadDir(scratch); len(ents) != 0 {
		t.Errorf("scratch left behind: %v", ents)
	}
}

func TestDrillFindsAnUnreadablePhoto(t *testing.T) {
	f := newFixture(t)
	// Break a photo's data key AFTER the backup would be taken, by destroying it first: the backup
	// then holds a photo nobody can decrypt, which a drill must report rather than skip.
	owner := firstDir(t, filepath.Join(f.data, "blobs"))
	if err := f.st.DestroyDataKey(owner); err != nil {
		t.Fatal(err)
	}
	info := f.create(t0)
	rep, err := Drill(info.Path, f.key, DrillOptions{Scratch: t.TempDir(), AllowNonTmpfs: true})
	if !errors.Is(err, ErrDrillFailed) || len(rep.Problems) == 0 {
		t.Fatalf("expected a failed drill, got %v %+v", err, rep)
	}
}

// N6 for backups: a flipped byte anywhere in the stored files, or a truncation, fails Verify.
func TestTamperedOrTruncatedArchiveIsRefused(t *testing.T) {
	f := newFixture(t)
	info := f.create(t0)
	orig := readAll(t, info.Path)

	flip := func(name string, at int) {
		t.Run(name, func(t *testing.T) {
			b := append([]byte(nil), orig...)
			b[at] ^= 0x01
			p := filepath.Join(t.TempDir(), "alcove-backup-20261114T093000Z-x.tar")
			if err := os.WriteFile(p, b, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Verify(p); err == nil {
				t.Fatal("a flipped byte was not detected")
			}
			if _, err := Restore(p, filepath.Join(t.TempDir(), "r")); err == nil {
				t.Fatal("Restore accepted a flipped byte")
			}
		})
	}
	flip("upload payload", bytes.Index(orig, []byte(uploadPayload))+5)
	flip("index", bytes.LastIndex(orig, []byte(`"sha256"`))+12)
	flip("sealed database", bytes.Index(orig, []byte("alcove.sealed"))+700)
	flip("manifest", bytes.Index(orig, []byte(`"store_id"`))+14)

	for _, keep := range []int{len(orig) - 1, len(orig) - 600, len(orig) / 2, 700, 10, 0} {
		p := filepath.Join(t.TempDir(), "alcove-backup-20261114T093000Z-x.tar")
		if err := os.WriteFile(p, orig[:keep], 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(p); err == nil {
			t.Errorf("a truncation to %d bytes was accepted", keep)
		}
	}

	// Anything that does not change a file but changes the archive is caught by the sidecar.
	b := append([]byte(nil), orig...)
	b = append(b, make([]byte, 1024)...)
	p := filepath.Join(t.TempDir(), "x.tar")
	_ = os.WriteFile(p, b, 0o600)
	_ = os.WriteFile(p+sidecar, readAll(t, info.Path+sidecar), 0o600)
	if _, err := Verify(p); err == nil {
		t.Error("an archive that no longer matches its checksum file was accepted")
	}
}

type ent struct {
	name string
	body string
	typ  byte
}

// craft builds a tar the way a hostile or buggy writer might, with a matching index.
func craft(t *testing.T, ents []ent, withIndex bool) string {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	var idx index
	idx.Format, idx.Created, idx.StoreID = formatVer, t0.Format(time.RFC3339), "abcd"
	for _, e := range ents {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.name, Mode: 0o600, Size: int64(len(e.body)), Typeflag: typ, ModTime: t0}
		if typ == tar.TypeSymlink {
			hdr.Linkname, hdr.Size = "/etc/passwd", 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Size > 0 {
			_, _ = tw.Write([]byte(e.body))
		}
		sum := sha256.Sum256([]byte(e.body))
		idx.Files = append(idx.Files, fileEntry{Name: e.name, Size: int64(len(e.body)), SHA256: hex.EncodeToString(sum[:])})
	}
	if withIndex {
		raw, _ := json.Marshal(idx)
		_ = tw.WriteHeader(&tar.Header{Name: indexName, Mode: 0o600, Size: int64(len(raw)), Typeflag: tar.TypeReg, ModTime: t0})
		_, _ = tw.Write(raw)
	}
	_ = tw.Close()
	p := filepath.Join(t.TempDir(), "alcove-backup-20261114T093000Z-evil.tar")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// N9 in spirit: a hostile archive cannot write outside the target or smuggle in odd entries.
func TestHostileArchivesAreRefused(t *testing.T) {
	base := []ent{{name: manifestName, body: `{"store_id":"abcd"}`}, {name: sealedName, body: "x"}}
	bad := map[string]string{
		"parent traversal":        "../evil",
		"absolute":                "/etc/cron.d/evil",
		"traversal inside blobs":  "blobs/a/b/../../../evil",
		"too shallow":             "blobs/a/c",
		"too deep":                "blobs/a/u/b/c",
		"backslash":               `blobs\a\u\b`,
		"upload temp file":        "blobs/a/u/.incoming-123",
		"unknown top-level":       "run.sh",
		"trailing dot segment":    "blobs/a/u/.",
		"double slash":            "blobs//u/x",
		"windows drive":           "C:/evil",
		"manifest in a subfolder": "x/manifest.json",
	}
	for label, name := range bad {
		t.Run(label, func(t *testing.T) {
			p := craft(t, append(append([]ent{}, base...), ent{name: name, body: "evil"}), true)
			to := filepath.Join(t.TempDir(), "r")
			if _, err := Restore(p, to); err == nil {
				t.Fatalf("%q was accepted", name)
			}
			if _, err := os.Stat(to); err == nil {
				t.Errorf("a refused restore left %s behind", to)
			}
		})
	}
	t.Run("symlink entry", func(t *testing.T) {
		p := craft(t, append(append([]ent{}, base...), ent{name: "blobs/a/u/link", typ: tar.TypeSymlink}), true)
		if _, err := Verify(p); !errors.Is(err, ErrBadEntry) {
			t.Fatalf("symlink: %v", err)
		}
	})
	t.Run("duplicate entry", func(t *testing.T) {
		p := craft(t, append(append([]ent{}, base...), ent{name: sealedName, body: "y"}), true)
		if _, err := Verify(p); err == nil {
			t.Fatal("duplicate accepted")
		}
	})
	t.Run("no index", func(t *testing.T) {
		if _, err := Verify(craft(t, base, false)); err == nil {
			t.Fatal("archive without an index accepted")
		}
	})
	t.Run("missing database", func(t *testing.T) {
		if _, err := Verify(craft(t, base[:1], true)); err == nil {
			t.Fatal("archive without the sealed database accepted")
		}
	})
	t.Run("well-formed control", func(t *testing.T) {
		p := craft(t, append(append([]ent{}, base...), ent{name: "blobs/a/u/b", body: "ok"}), true)
		if _, err := Verify(p); err != nil {
			t.Fatalf("a valid archive was refused: %v", err)
		}
	})
}

func TestRestoreRefusesANonEmptyTarget(t *testing.T) {
	f := newFixture(t)
	info := f.create(t0)
	to := t.TempDir()
	if err := os.WriteFile(filepath.Join(to, "keep.txt"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(info.Path, to); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("restore into a non-empty folder: %v", err)
	}
	if string(readAll(t, filepath.Join(to, "keep.txt"))) != "mine" {
		t.Fatal("the existing file was touched")
	}
	file := filepath.Join(t.TempDir(), "afile")
	_ = os.WriteFile(file, nil, 0o600)
	if _, err := Restore(info.Path, file); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("restore onto a file: %v", err)
	}
	if _, err := Restore(info.Path, t.TempDir()); err != nil {
		t.Fatalf("an empty existing folder must be accepted: %v", err)
	}
}

func TestCreateRefusesBadDestinations(t *testing.T) {
	f := newFixture(t)
	if _, err := Create(f.st, filepath.Join(f.data, "backups"), t0); !errors.Is(err, ErrNoDir) && !errors.Is(err, ErrInsideData) {
		t.Fatalf("a folder that does not exist: %v", err)
	}
	inside := filepath.Join(f.data, "backups")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(f.st, inside, t0); !errors.Is(err, ErrInsideData) {
		t.Fatalf("a folder inside the data directory: %v", err)
	}
	if _, err := Create(f.st, f.data, t0); !errors.Is(err, ErrInsideData) {
		t.Fatalf("the data directory itself: %v", err)
	}
	if _, err := Create(f.st, filepath.Join(t.TempDir(), "missing"), t0); !errors.Is(err, ErrNoDir) {
		t.Fatalf("a missing folder: %v", err)
	}
	f.create(t0)
	if _, err := Create(f.st, f.backup, t0); err == nil {
		t.Fatal("a second backup in the same second overwrote or reused a name")
	}
	if ents, _ := os.ReadDir(f.backup); len(ents) != 2 { // one tar + its sidecar, no .part
		t.Fatalf("leftovers: %v", ents)
	}
}

// Constraint 4: a backup holds no plaintext, only ciphertext and the verbatim upload.
func TestBackupContainsNoPlaintext(t *testing.T) {
	f := newFixture(t)
	info := f.create(t0)
	b := readAll(t, info.Path)
	for _, secret := range []string{photoPlain, displaySecret, "alice", "admin.first_run", "SQLite format 3", "PHOTO-PLAINTEXT"} {
		if bytes.Contains(b, []byte(secret)) {
			t.Errorf("the backup contains %q in the clear", secret)
		}
	}
	if !bytes.Contains(b, []byte(uploadPayload)) {
		t.Error("the upload must be stored verbatim (already encrypted under the member's password)")
	}
}

func TestInProgressUploadsAreNotBackedUp(t *testing.T) {
	f := newFixture(t)
	owner := firstDir(t, filepath.Join(f.data, "blobs"))
	tmp := filepath.Join(f.data, "blobs", owner, "u", ".incoming-12345")
	if err := os.WriteFile(tmp, []byte("half an upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	info := f.create(t0)
	if info.Files != 4 || bytes.Contains(readAll(t, info.Path), []byte("half an upload")) {
		t.Fatalf("an in-progress upload was captured: %+v", info)
	}
}

// D-28 consistency: a backup taken while the database and blobs are changing still restores and
// passes a drill. (This is the test the race detector in CI is for.)
func TestBackupDuringWritesIsConsistent(t *testing.T) {
	f := newFixture(t)
	owner := firstDir(t, filepath.Join(f.data, "blobs"))
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			id := "w" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26))
			// Blob first, then the row that mentions it (the D-28 ordering rule).
			_, _, _ = f.blobs.PutUpload(owner, id, strings.NewReader("payload-"+id), 0)
			_ = f.st.Update(context.Background(), i%3 == 0, func(tx *sql.Tx) error {
				_, err := adminlog.Append(tx, t0.Add(time.Duration(i)*time.Second), owner, "entry.touch", "entry", id, "notes")
				return err
			})
		}
	}()
	var infos []Info
	for i := 0; i < 5; i++ {
		infos = append(infos, f.create(t0.Add(time.Duration(i)*time.Second)))
	}
	close(stop)
	wg.Wait()
	for _, info := range infos {
		if _, err := Verify(info.Path); err != nil {
			t.Fatalf("%s: %v", info.Name, err)
		}
		if _, err := Drill(info.Path, f.key, DrillOptions{Scratch: t.TempDir(), AllowNonTmpfs: true}); err != nil {
			t.Fatalf("%s: drill: %v", info.Name, err)
		}
	}
}

func TestListFindPrune(t *testing.T) {
	f := newFixture(t)
	var names []string
	for i := 0; i < 5; i++ {
		names = append(names, f.create(t0.Add(time.Duration(i)*time.Hour)).Name)
	}
	_ = os.WriteFile(filepath.Join(f.backup, "notes.txt"), []byte("not a backup"), 0o600)
	_ = os.WriteFile(filepath.Join(f.backup, Prefix+"garbage"+Suffix), []byte("x"), 0o600)

	list, err := List(f.backup)
	if err != nil || len(list) != 5 || list[0].Name != names[4] || list[4].Name != names[0] {
		t.Fatalf("list (newest first): %v %v", list, err)
	}
	if _, err := Find(f.backup, names[2]); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"../" + names[2], names[2] + "/..", "notes.txt", "", filepath.Join(f.backup, names[2])} {
		if _, err := Find(f.backup, bad); err == nil {
			t.Errorf("Find accepted %q", bad)
		}
	}

	gone, err := Prune(f.backup, 2)
	if err != nil || len(gone) != 3 {
		t.Fatalf("prune: %v %v", gone, err)
	}
	list, _ = List(f.backup)
	if len(list) != 2 || list[0].Name != names[4] || list[1].Name != names[3] {
		t.Fatalf("after prune: %v", list)
	}
	for _, g := range gone {
		if _, err := os.Stat(filepath.Join(f.backup, g+sidecar)); err == nil {
			t.Errorf("sidecar of %s was left", g)
		}
	}
	if string(readAll(t, filepath.Join(f.backup, "notes.txt"))) != "not a backup" {
		t.Error("prune touched an unrelated file")
	}
	if gone, _ := Prune(f.backup, 0); len(gone) != 1 { // never fewer than one
		t.Fatalf("prune to zero removed %v", gone)
	}
	if list, _ := List(f.backup); len(list) != 1 || list[0].Name != names[4] {
		t.Fatalf("the newest backup must always survive: %v", list)
	}
	var _ io.Reader
}

func TestCleanStaleRemovesOnlyOldLeftovers(t *testing.T) {
	f := newFixture(t)
	good := f.create(t0)
	old := time.Now().Add(-3 * time.Hour)
	mk := func(name string, dir bool, mod time.Time) string {
		p := filepath.Join(f.backup, name)
		if dir {
			_ = os.MkdirAll(filepath.Join(p, "data"), 0o700)
		} else {
			_ = os.WriteFile(p, []byte("x"), 0o600)
		}
		_ = os.Chtimes(p, mod, mod)
		return p
	}
	oldPart := mk(Prefix+"20260101T000000Z-aa.tar.part", false, old)
	newPart := mk(Prefix+"20260102T000000Z-bb.tar.part", false, time.Now())
	oldDrill := mk(".drill-111", true, old)
	newDrill := mk(".drill-222", true, time.Now())
	other := mk("notes.txt", false, old)
	n, err := CleanStale(f.backup, time.Hour)
	if err != nil || n != 2 {
		t.Fatalf("removed %d, %v", n, err)
	}
	for _, p := range []string{oldPart, oldDrill} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived", p)
		}
	}
	for _, p := range []string{newPart, newDrill, other, good.Path, good.Path + sidecar} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed: %v", p, err)
		}
	}
}
