package ingest

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/herbiewalker/alcove/internal/trystbk"
	"github.com/herbiewalker/alcove/internal/trystbk/synth"
)

// originalPhoto reads one photo straight out of the synthetic container.
func originalPhoto(t *testing.T, res *synth.Result, id string) []byte {
	t.Helper()
	bk, err := trystbk.Read(bytes.NewReader(res.File), res.Password)
	if err != nil {
		t.Fatal(err)
	}
	defer bk.Close()
	rc, err := bk.OpenMedia(trystbk.EntryMediaPrefix + id)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	return b
}

func (r *rig) storedPhoto(t *testing.T, id string) []byte {
	t.Helper()
	dek, err := r.st.DataKey(owner)
	if err != nil {
		t.Fatal(err)
	}
	defer dek.Zero()
	rc, err := r.svc.Blobs.OpenMedia(owner, id, dek.Bytes())
	if err != nil {
		t.Fatalf("open stored photo %s: %v", id, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (r *rig) photoFiles() []string {
	var out []string
	for _, f := range r.blobFiles() {
		if filepath.Base(filepath.Dir(f)) == "m" {
			out = append(out, filepath.Base(f))
		}
	}
	sort.Strings(out)
	return out
}

func TestUploadCopiesPhotosOutEncrypted(t *testing.T) {
	r := newRig(t)
	res := build(t, synth.Options{Seed: 41, Encounters: 8, Photos: 3})
	info, err := r.upload(res, true)
	if err != nil {
		t.Fatal(err)
	}
	if info.Photos.Stored != 3 || info.Photos.Skipped != 0 || info.Media != 3 {
		t.Fatalf("photos: %+v media=%d", info.Photos, info.Media)
	}
	if n := r.count("SELECT COUNT(*) FROM media_blob WHERE owner_id = ?", owner); n != 3 {
		t.Fatalf("%d media_blob rows", n)
	}
	files := r.photoFiles()
	if len(files) != 3 {
		t.Fatalf("photo files: %v", files)
	}
	for _, id := range res.MediaIDs {
		want := originalPhoto(t, res, id)
		if got := r.storedPhoto(t, id); !bytes.Equal(got, want) || len(want) == 0 {
			t.Errorf("photo %s does not round trip", id)
		}
		// On disk it is ciphertext: no JPEG header, none of the picture bytes.
		for _, f := range r.blobFiles() {
			if filepath.Base(f) == id {
				raw, _ := os.ReadFile(f)
				if bytes.Contains(raw, want[:8]) || bytes.HasPrefix(raw, []byte{0xFF, 0xD8}) {
					t.Errorf("photo %s is on the data volume in the clear", id)
				}
			}
		}
	}
	// Each row names its entry, and that entry is in the cached encounters.
	if n := r.count(`SELECT COUNT(*) FROM media_blob b WHERE NOT EXISTS (SELECT 1 FROM cache_row c WHERE c.owner_id = b.owner_id AND c.tbl = 'encounters' AND c.rid = b.entry_id)`); n != 0 {
		t.Errorf("%d photos point at an entry that is not there", n)
	}
}

func TestANewUploadReplacesThePhotos(t *testing.T) {
	r := newRig(t)
	first := build(t, synth.Options{Seed: 42, Encounters: 5, Photos: 2})
	second := build(t, synth.Options{Seed: 43, Encounters: 5, Photos: 1})
	if _, err := r.upload(first, true); err != nil {
		t.Fatal(err)
	}
	old := r.photoFiles()
	if _, err := r.upload(second, true); err != nil {
		t.Fatal(err)
	}
	now := r.photoFiles()
	if len(now) != 1 || now[0] != second.MediaIDs[0] {
		t.Fatalf("after the second upload: %v (was %v)", now, old)
	}
	if n := r.count("SELECT COUNT(*) FROM media_blob"); n != 1 {
		t.Errorf("%d rows after the second upload", n)
	}
}

func TestRebuildBringsBackLostPhotosAndClearCacheRemovesThem(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	res := build(t, synth.Options{Seed: 44, Encounters: 6, Photos: 2})
	if _, err := r.upload(res, true); err != nil {
		t.Fatal(err)
	}
	// Lose the files and the rows, as a damaged volume or a bad migration might.
	if err := r.svc.Blobs.DeleteMedia(owner); err != nil {
		t.Fatal(err)
	}
	r.exec(t, `DELETE FROM media_blob`)
	if len(r.photoFiles()) != 0 {
		t.Fatal("setup: photos still on disk")
	}
	if err := r.svc.Rebuild(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if len(r.photoFiles()) != 2 || r.count("SELECT COUNT(*) FROM media_blob") != 2 {
		t.Errorf("rebuild did not restore the photos: %v", r.photoFiles())
	}
	for _, id := range res.MediaIDs {
		if !bytes.Equal(r.storedPhoto(t, id), originalPhoto(t, res, id)) {
			t.Errorf("photo %s differs after a rebuild", id)
		}
	}
	// With no current data the derived photos go too.
	if err := r.svc.ClearCache(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if len(r.photoFiles()) != 0 || r.count("SELECT COUNT(*) FROM media_blob") != 0 {
		t.Error("clearing the cache left photos behind")
	}
	// The stored upload itself is untouched.
	if len(r.uploadFiles()) != 1 {
		t.Error("the upload was removed")
	}
}

func TestAnUploadWithNoPhotosStoresNone(t *testing.T) {
	r := newRig(t)
	info, err := r.upload(build(t, synth.Options{Seed: 45, Encounters: 4}), true)
	if err != nil || info.Photos.Stored != 0 || len(r.photoFiles()) != 0 || r.count("SELECT COUNT(*) FROM media_blob") != 0 {
		t.Errorf("%v %+v", err, info.Photos)
	}
}

func (r *rig) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if err := r.st.Update(context.Background(), false, func(tx *sql.Tx) error { _, err := tx.Exec(q, args...); return err }); err != nil {
		t.Fatal(err)
	}
}
