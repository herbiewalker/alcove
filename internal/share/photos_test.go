package share

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/herbiewalker/alcove/internal/photo"
	"github.com/herbiewalker/alcove/internal/spaces"
)

// withPhotos gives a person's entry "e1" two stored photos: a JPEG with a caption and a place, and
// one in a format Alcove cannot clean (kind 0). It also puts a photo on another entry.
func (r *rig) withPhotos(owner string) {
	r.t.Helper()
	r.fullEntry(owner)
	row := func(tbl, rid string, pos int, data string) {
		r.exec(`INSERT INTO cache_row(owner_id, tbl, rid, pos, data) VALUES (?,?,?,?,?)`, "u"+owner, tbl, rid, pos, data)
	}
	row("media", "m1", 0, `{"id":"m1","encounterId":"e1","caption":"CAPTION-ONE","latitude":48.8566,"longitude":2.3522}`)
	row("media", "m2", 1, `{"id":"m2","encounterId":"e1","caption":null,"latitude":null,"longitude":null}`)
	row("media", "m3", 2, `{"id":"m3","encounterId":"other","caption":"other entry","latitude":1.0,"longitude":2.0}`)
	r.exec(`INSERT INTO media_blob(owner_id, media_id, entry_id, mime, size, kind) VALUES (?,?,?,?,?,?)`, "u"+owner, "m1", "e1", "image/jpeg", 10, int(photo.JPEG))
	r.exec(`INSERT INTO media_blob(owner_id, media_id, entry_id, mime, size, kind) VALUES (?,?,?,?,?,?)`, "u"+owner, "m2", "e1", "image/heic", 10, int(photo.Unknown))
	r.exec(`INSERT INTO media_blob(owner_id, media_id, entry_id, mime, size, kind) VALUES (?,?,?,?,?,?)`, "u"+owner, "m3", "other", "image/png", 10, int(photo.PNG))
}

func (r *rig) openPhoto(space, viewer, owner, id string) (photo.Kind, error) {
	r.t.Helper()
	var k photo.Kind
	err := r.st.View(context.Background(), func(tx *sql.Tx) (e error) {
		k, e = OpenPhoto(tx, space, "u"+viewer, spaces.ConsentVersion, "u"+owner, id)
		return
	})
	return k, err
}

func TestTheOwnerSeesAllTheirPhotosWithPlaceAndCaption(t *testing.T) {
	r := newRig(t, "ann")
	r.withPhotos("ann")
	var d Detail
	if err := r.st.View(context.Background(), func(tx *sql.Tx) (err error) { d, err = OwnDetail(tx, "uann", "e1"); return }); err != nil {
		t.Fatal(err)
	}
	if len(d.Photos) != 2 || d.PhotosHidden != 0 {
		t.Fatalf("photos: %+v hidden=%d", d.Photos, d.PhotosHidden)
	}
	if d.Photos[0].ID != "m1" || d.Photos[0].Caption != "CAPTION-ONE" || d.Photos[0].Place != "48.8566, 2.3522" || !d.Photos[0].Viewable {
		t.Errorf("first photo: %+v", d.Photos[0])
	}
	if d.Photos[1].ID != "m2" || d.Photos[1].Viewable || d.Photos[1].Place != "" {
		t.Errorf("second photo: %+v", d.Photos[1])
	}
	// A photo of another entry is not on this one, and OwnPhoto reads only the owner's rows.
	r2 := newRig(t, "ann", "bob")
	r2.withPhotos("ann")
	err := r2.st.View(context.Background(), func(tx *sql.Tx) error { _, e := OwnPhoto(tx, "ubob", "m1"); return e })
	if !errors.Is(err, ErrNoEntry) {
		t.Errorf("another person's photo through OwnPhoto: %v", err)
	}
	err = r2.st.View(context.Background(), func(tx *sql.Tx) error {
		k, e := OwnPhoto(tx, "uann", "m1")
		if k != photo.JPEG {
			t.Errorf("kind %v", k)
		}
		return e
	})
	if err != nil {
		t.Error(err)
	}
}

func TestSharedPhotosFollowTheOwnersChoices(t *testing.T) {
	r := newRig(t, "ann", "bob")
	sp := r.space("ann", "bob")
	r.withPhotos("bob")
	get := func() Detail {
		d, err := r.detail(sp, "ann", "bob", "e1")
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	// Defaults: photos and notes on, place off.
	d := get()
	if len(d.Photos) != 2 || d.Photos[0].Caption != "CAPTION-ONE" || d.Photos[0].Place != "" {
		t.Errorf("defaults: %+v", d.Photos)
	}
	if d.Photos[1].Viewable {
		t.Error("a photo Alcove cannot clean is offered for viewing")
	}
	// The owner turns the place on.
	r.exec(`UPDATE membership SET show_photo_location = 1 WHERE space_id = ? AND user_id = 'ubob'`, sp)
	if d = get(); d.Photos[0].Place != "48.8566, 2.3522" || d.Photos[1].Place != "" {
		t.Errorf("place on: %+v", d.Photos)
	}
	// Notes off: the caption goes with them, the photo stays.
	r.exec(`UPDATE membership SET include_notes = 0 WHERE space_id = ? AND user_id = 'ubob'`, sp)
	if d = get(); len(d.Photos) != 2 || d.Photos[0].Caption != "" {
		t.Errorf("notes off: %+v", d.Photos)
	}
	// Photos off: none are listed, and the page can say how many are kept back.
	r.exec(`UPDATE membership SET include_photos = 0 WHERE space_id = ? AND user_id = 'ubob'`, sp)
	if d = get(); len(d.Photos) != 0 || d.PhotosHidden != 2 {
		t.Errorf("photos off: %d listed, %d hidden", len(d.Photos), d.PhotosHidden)
	}
}

func TestAPhotoOpensOnlyWhereItsEntryDoes(t *testing.T) {
	hide := map[string]func(r *rig, sp string){
		"paused": func(r *rig, sp string) {
			r.exec(`UPDATE membership SET paused = 1 WHERE space_id = ? AND user_id = 'ubob'`, sp)
		},
		"left": func(r *rig, sp string) {
			r.exec(`UPDATE membership SET state = 'left' WHERE space_id = ? AND user_id = 'ubob'`, sp)
		},
		"removed": func(r *rig, sp string) {
			r.exec(`UPDATE membership SET state = 'removed' WHERE space_id = ? AND user_id = 'ubob'`, sp)
		},
		"older wording": func(r *rig, sp string) {
			r.exec(`UPDATE membership SET consent_version = 1 WHERE space_id = ? AND user_id = 'ubob'`, sp)
		},
		"disabled": func(r *rig, sp string) { r.exec(`UPDATE user SET status = 'disabled' WHERE id = 'ubob'`) },
		"deleted":  func(r *rig, sp string) { r.exec(`UPDATE user SET deleted_at = 5 WHERE id = 'ubob'`) },
		"private entry": func(r *rig, sp string) {
			r.exec(`INSERT INTO entry_flag(owner_id, entry_id, private, updated_at) VALUES ('ubob','e1',1,0)`)
		},
		"photos off": func(r *rig, sp string) {
			r.exec(`UPDATE membership SET include_photos = 0 WHERE space_id = ? AND user_id = 'ubob'`, sp)
		},
		"rows gone": func(r *rig, sp string) { r.exec(`DELETE FROM media_blob WHERE owner_id = 'ubob'`) },
		"cache gone": func(r *rig, sp string) {
			r.exec(`DELETE FROM cache_row WHERE owner_id = 'ubob' AND tbl = 'encounters'`)
		},
	}
	for name, do := range hide {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, "ann", "bob")
			sp := r.space("ann", "bob")
			r.withPhotos("bob")
			if k, err := r.openPhoto(sp, "ann", "bob", "m1"); err != nil || k != photo.JPEG {
				t.Fatalf("setup: %v %v", k, err)
			}
			do(r, sp)
			if _, err := r.openPhoto(sp, "ann", "bob", "m1"); !errors.Is(err, ErrNoEntry) {
				t.Errorf("the photo is still open: %v", err)
			}
		})
	}
	r := newRig(t, "ann", "bob", "cy")
	sp := r.space("ann", "bob")
	r.withPhotos("bob")
	r.withPhotos("cy")
	for _, c := range []struct {
		name                 string
		viewer, owner, media string
		want                 error
	}{
		{"an outsider", "cy", "bob", "m1", ErrNotMember},
		{"an owner who is not in the space", "ann", "cy", "m1", ErrNoEntry},
		{"a photo that does not exist", "ann", "bob", "nope", ErrNoEntry},
		{"a format Alcove cannot clean", "ann", "bob", "m2", ErrNoEntry},
		{"another owner's id under bob", "ann", "ann", "m1", ErrNoEntry},
	} {
		if _, err := r.openPhoto(sp, c.viewer, c.owner, c.media); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	// A photo of a different entry resolves through its own entry: m3 sits on an entry that does not exist.
	if _, err := r.openPhoto(sp, "ann", "bob", "m3"); !errors.Is(err, ErrNoEntry) {
		t.Errorf("a photo whose entry is not in the cache: %v", err)
	}
	r.exec(`UPDATE membership SET consent_version = 1 WHERE space_id = ? AND user_id = 'uann'`, sp)
	if _, err := r.openPhoto(sp, "ann", "bob", "m1"); !errors.Is(err, ErrNeedConsent) {
		t.Errorf("viewer on older wording: %v", err)
	}
}

func TestAPhotoOfADeletedEntryIsNotServedToItsOwnerEither(t *testing.T) {
	r := newRig(t, "ann")
	r.withPhotos("ann")
	own := func() error {
		return r.st.View(context.Background(), func(tx *sql.Tx) error { _, e := OwnPhoto(tx, "uann", "m1"); return e })
	}
	if err := own(); err != nil {
		t.Fatalf("setup: %v", err)
	}
	r.exec(`DELETE FROM cache_row WHERE owner_id = 'uann' AND tbl = 'encounters' AND rid = 'e1'`)
	if err := own(); !errors.Is(err, ErrNoEntry) {
		t.Errorf("a photo of a deleted entry: %v", err)
	}
}
