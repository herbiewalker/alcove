package share

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/herbiewalker/alcove/internal/spaces"
)

func (r *rig) gallery(space, viewer string) ([]GalleryItem, int, error) {
	r.t.Helper()
	var items []GalleryItem
	var total int
	err := r.st.View(context.Background(), func(tx *sql.Tx) (e error) {
		items, total, e = Gallery(tx, space, "u"+viewer, spaces.ConsentVersion, 24, 0)
		return
	})
	return items, total, err
}

func TestTheOwnGalleryListsOnlyShowablePicturesOfTheirOwnEntries(t *testing.T) {
	r := newRig(t, "ann", "bob")
	r.withPhotos("ann")
	r.withPhotos("bob")
	var items []GalleryItem
	var total int
	if err := r.st.View(context.Background(), func(tx *sql.Tx) (err error) {
		items, total, err = OwnGallery(tx, "uann", 24, 0)
		return
	}); err != nil {
		t.Fatal(err)
	}
	// m1 shows; m2 is a format a browser cannot show; m3 sits on an entry that does not exist.
	if total != 1 || len(items) != 1 || items[0].MediaID != "m1" || items[0].Caption != "CAPTION-ONE" || !items[0].Mine || items[0].OwnerID != "uann" {
		t.Errorf("own gallery: total %d %+v", total, items)
	}
}

func TestTheSpaceGalleryFollowsTheOwnersChoices(t *testing.T) {
	r := newRig(t, "ann", "bob")
	sp := r.space("ann", "bob")
	r.withPhotos("bob")
	items, total, err := r.gallery(sp, "ann")
	if err != nil || total != 1 || len(items) != 1 || items[0].MediaID != "m1" || items[0].OwnerID != "ubob" || items[0].Caption != "CAPTION-ONE" || items[0].Mine {
		t.Fatalf("defaults: %v %d %+v", err, total, items)
	}
	r.exec(`UPDATE membership SET include_notes = 0 WHERE space_id = ? AND user_id = 'ubob'`, sp)
	if items, _, _ = r.gallery(sp, "ann"); len(items) != 1 || items[0].Caption != "" {
		t.Errorf("notes off: %+v", items)
	}
}

func TestAPictureIsInTheSpaceGalleryOnlyWhereItsEntryIs(t *testing.T) {
	hide := map[string]string{
		"paused":        `UPDATE membership SET paused = 1 WHERE space_id = ? AND user_id = 'ubob'`,
		"left":          `UPDATE membership SET state = 'left' WHERE space_id = ? AND user_id = 'ubob'`,
		"removed":       `UPDATE membership SET state = 'removed' WHERE space_id = ? AND user_id = 'ubob'`,
		"older wording": `UPDATE membership SET consent_version = 1 WHERE space_id = ? AND user_id = 'ubob'`,
		"photos off":    `UPDATE membership SET include_photos = 0 WHERE space_id = ? AND user_id = 'ubob'`,
	}
	for name, q := range hide {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, "ann", "bob")
			sp := r.space("ann", "bob")
			r.withPhotos("bob")
			r.exec(q, sp)
			if items, total, err := r.gallery(sp, "ann"); err != nil || total != 0 || len(items) != 0 {
				t.Errorf("still listed: %v %d %+v", err, total, items)
			}
		})
	}
	other := map[string]string{
		"private entry": `INSERT INTO entry_flag(owner_id, entry_id, private, updated_at) VALUES ('ubob','e1',1,0)`,
		"disabled":      `UPDATE user SET status = 'disabled' WHERE id = 'ubob'`,
		"deleted":       `UPDATE user SET deleted_at = 5 WHERE id = 'ubob'`,
		"rows gone":     `DELETE FROM media_blob WHERE owner_id = 'ubob'`,
		"cache gone":    `DELETE FROM cache_row WHERE owner_id = 'ubob' AND tbl = 'encounters'`,
	}
	for name, q := range other {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, "ann", "bob")
			sp := r.space("ann", "bob")
			r.withPhotos("bob")
			r.exec(q)
			if items, total, err := r.gallery(sp, "ann"); err != nil || total != 0 || len(items) != 0 {
				t.Errorf("still listed: %v %d %+v", err, total, items)
			}
		})
	}
	// An outsider, a viewer on older wording, and another space see nothing or are refused.
	r := newRig(t, "ann", "bob", "cy")
	sp := r.space("ann", "bob")
	r.withPhotos("bob")
	r.withPhotos("cy")
	if _, _, err := r.gallery(sp, "cy"); !errors.Is(err, ErrNotMember) {
		t.Errorf("an outsider: %v", err)
	}
	items, _, _ := r.gallery(sp, "ann")
	for _, it := range items {
		if it.OwnerID == "ucy" {
			t.Error("a person outside the space is in its gallery")
		}
	}
	r.exec(`UPDATE membership SET consent_version = 1 WHERE space_id = ? AND user_id = 'uann'`, sp)
	if _, _, err := r.gallery(sp, "ann"); !errors.Is(err, ErrNeedConsent) {
		t.Errorf("viewer on older wording: %v", err)
	}
}
