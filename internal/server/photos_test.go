package server

import (
	"bytes"
	"context"
	"database/sql"
	"image"
	"image/color"
	"image/jpeg"
	"net/url"
	"strings"
	"testing"

	"github.com/herbiewalker/alcove/internal/trystbk/synth"
)

// exifJPEG is a real JPEG with an EXIF block (a GPS position) and a comment inserted after the start marker.
func exifJPEG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := synthImage()
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	seg := func(marker byte, payload string) []byte {
		b := []byte{0xFF, marker, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}
		return append(b, payload...)
	}
	out := append([]byte{}, raw[:2]...)
	out = append(out, seg(0xE1, "Exif\x00\x00GPSMARK-48.8566N-2.3522E")...)
	out = append(out, seg(0xFE, "COMMENTMARK")...)
	return append(out, raw[2:]...)
}

// withPhotos gives sam a second upload with photos, then replaces his first photo by one that has EXIF.
// It returns the entry, the photo (with EXIF) and the photo bytes.
func withPhotos(t *testing.T, s *sharing) (entry, media string, original []byte) {
	t.Helper()
	res := synthFile(t, synth.Options{Seed: 91, Encounters: 6, Photos: 2, Password: "Sam-Photo-Backup-1"})
	if w := s.sam.upload(res.File, "photos.tryst", res.Password, true, false); w.Code != 303 {
		t.Fatalf("upload: %d", w.Code)
	}
	uid := s.e.idOf("sam")
	var entries, medias []string
	if err := queryStrings(s.e, `SELECT entry_id, media_id FROM media_blob WHERE owner_id = ? ORDER BY media_id`, uid, &entries, &medias); err != nil || len(medias) != 2 {
		t.Fatalf("photos stored: %v %v", medias, err)
	}
	entry, media = entries[0], medias[0]
	original = exifJPEG(t)
	// Put the EXIF picture in place of the stored one, encrypted the way an upload would be.
	svc := s.e.srv.ingestFor(s.e.st)
	dek, err := s.e.st.DataKey(uid)
	if err != nil {
		t.Fatal(err)
	}
	key := dek.Bytes()
	dek.Zero()
	if err := svc.Blobs.DeleteMedia(uid); err != nil {
		t.Fatal(err)
	}
	for _, m := range medias {
		if _, err := svc.Blobs.PutMedia(uid, m, key, bytes.NewReader(original), 0); err != nil {
			t.Fatal(err)
		}
	}
	return entry, media, original
}

func (s *sharing) photoURL(entry, media string) (own, shared string) {
	return "/photos/" + media, "/spaces/" + s.space + "/photo/" + s.e.idOf("sam") + "/" + media
}

func TestOwnPhotosAreShownBlurredAndServedExactly(t *testing.T) {
	s := newSharing(t)
	entry, media, original := withPhotos(t, s)
	page := s.sam.do("GET", "/entries/"+url.PathEscape(entry), nil).Body.String()
	own, _ := s.photoURL(entry, media)
	if !strings.Contains(page, `<figure class="photo blurred">`) || !strings.Contains(page, `src="`+own+`"`) {
		t.Fatalf("the entry page does not show the photo blurred: %.400s", page)
	}
	w := s.sam.do("GET", own, nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" || !bytes.Equal(w.Body.Bytes(), original) {
		t.Fatalf("own photo: %d %s (%d bytes, want %d)", w.Code, w.Header().Get("Content-Type"), w.Body.Len(), len(original))
	}
	h := w.Header()
	if h.Get("Cache-Control") != "no-store" || h.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("photo headers: %v", h)
	}
	// The owner's own copy still has its EXIF: it is theirs and unchanged.
	if !bytes.Contains(w.Body.Bytes(), []byte("GPSMARK")) {
		t.Error("the owner's own photo was altered")
	}
	// Nobody else can fetch it through the own-photo route, and a made-up id is a 404.
	if w := s.kim.do("GET", own, nil); w.Code != 404 {
		t.Errorf("another person fetched it: %d", w.Code)
	}
	if w := s.sam.do("GET", "/photos/does-not-exist", nil); w.Code != 404 {
		t.Errorf("made-up id: %d", w.Code)
	}
	if w := s.e.client("192.168.1.99:1").do("GET", own, nil); w.Code == 200 {
		t.Error("a signed-out visitor fetched a photo")
	}
}

func TestSharedPhotosHaveNoMetadataAndFollowTheOwnersChoices(t *testing.T) {
	s := newSharing(t)
	entry, media, _ := withPhotos(t, s)
	_, shared := s.photoURL(entry, media)
	w := s.kim.do("GET", shared, nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("shared photo: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	for _, leak := range []string{"GPSMARK", "COMMENTMARK", "Exif"} {
		if bytes.Contains(w.Body.Bytes(), []byte(leak)) {
			t.Errorf("a shared photo still carries %q", leak)
		}
	}
	if _, err := jpeg.Decode(bytes.NewReader(w.Body.Bytes())); err != nil {
		t.Errorf("the cleaned photo does not decode: %v", err)
	}
	page := s.kim.do("GET", "/spaces/"+s.space+"/entry/"+s.e.idOf("sam")+"/"+url.PathEscape(entry), nil).Body.String()
	if !strings.Contains(page, `src="`+shared+`"`) || !strings.Contains(page, "blurred") {
		t.Error("the shared entry page does not show the photo")
	}
	// Photos off: the page says so and the photo itself is gone.
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"notes": {"1"}})
	page = s.kim.do("GET", "/spaces/"+s.space+"/entry/"+s.e.idOf("sam")+"/"+url.PathEscape(entry), nil).Body.String()
	if strings.Contains(page, shared) || !strings.Contains(page, "kept out of this space") {
		t.Error("photos are shown although the owner left them out")
	}
	if w := s.kim.do("GET", shared, nil); w.Code != 404 {
		t.Errorf("a photo the owner keeps out was served: %d", w.Code)
	}
}

func TestPhotoPlaceIsShownOnlyWhenTheOwnerAllowsIt(t *testing.T) {
	s := newSharing(t)
	entry, media, _ := withPhotos(t, s)
	s.e.exec(`UPDATE cache_row SET data = json_set(data, '$.latitude', 48.8566, '$.longitude', 2.3522, '$.caption', 'CAPTION-TEXT') WHERE owner_id = ? AND tbl = 'media' AND rid = ?`, s.e.idOf("sam"), media)
	entryURL := "/spaces/" + s.space + "/entry/" + s.e.idOf("sam") + "/" + url.PathEscape(entry)
	page := s.kim.do("GET", entryURL, nil).Body.String()
	if strings.Contains(page, "48.8566") || strings.Contains(page, "Taken at") {
		t.Error("a photo's place is shown by default")
	}
	if !strings.Contains(page, "CAPTION-TEXT") {
		t.Error("the caption should follow the note choice (on)")
	}
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"notes": {"1"}, "photos": {"1"}, "photoplace": {"1"}})
	if page = s.kim.do("GET", entryURL, nil).Body.String(); !strings.Contains(page, "Taken at 48.8566, 2.3522") {
		t.Error("the place is not shown although the owner turned it on")
	}
	// Notes off: the caption goes, the place choice stays its own.
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"photos": {"1"}, "photoplace": {"1"}})
	if page = s.kim.do("GET", entryURL, nil).Body.String(); strings.Contains(page, "CAPTION-TEXT") {
		t.Error("a caption is shown although notes are left out")
	}
	// The owner always sees their own place and caption.
	own := s.sam.do("GET", "/entries/"+url.PathEscape(entry), nil).Body.String()
	if !strings.Contains(own, "Taken at 48.8566, 2.3522") || !strings.Contains(own, "CAPTION-TEXT") {
		t.Error("the owner does not see their own place and caption")
	}
}

func TestAPhotoIsClosedWhereItsEntryIsClosed(t *testing.T) {
	s := newSharing(t)
	entry, media, _ := withPhotos(t, s)
	_, shared := s.photoURL(entry, media)
	code := func() int { return s.kim.do("GET", shared, nil).Code }
	if code() != 200 {
		t.Fatal("setup")
	}
	s.sam.post("/entries", "/entries/private", url.Values{"id": {entry}, "private": {"1"}})
	if c := code(); c != 404 {
		t.Errorf("a photo of a private entry was served: %d", c)
	}
	s.sam.post("/entries", "/entries/private", url.Values{"id": {entry}, "private": {"0"}})
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"notes": {"1"}, "photos": {"1"}, "paused": {"1"}})
	if c := code(); c != 404 {
		t.Errorf("a paused member's photo was served: %d", c)
	}
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"notes": {"1"}, "photos": {"1"}})
	// An outsider and a made-up space or owner or photo are all the same page.
	c1 := s.lou.do("GET", shared, nil)
	c2 := s.kim.do("GET", "/spaces/"+s.space+"/photo/"+s.e.idOf("lou")+"/"+media, nil)
	c3 := s.kim.do("GET", "/spaces/"+s.space+"/photo/"+s.e.idOf("sam")+"/nope", nil)
	if c1.Code != 404 || c2.Code != 404 || c3.Code != 404 {
		t.Errorf("refusals: %d %d %d", c1.Code, c2.Code, c3.Code)
	}
	// Leaving closes it at once.
	s.kim.post("/spaces/"+s.space, "/spaces/"+s.space+"/leave", url.Values{})
	if c := code(); c != 404 {
		t.Errorf("a member who left fetched a photo: %d", c)
	}
}

func TestAPictureAlcoveCannotCleanIsNeverShared(t *testing.T) {
	s := newSharing(t)
	entry, media, _ := withPhotos(t, s)
	_, shared := s.photoURL(entry, media)
	own, _ := s.photoURL(entry, media)
	s.e.exec(`UPDATE media_blob SET kind = 0 WHERE owner_id = ? AND media_id = ?`, s.e.idOf("sam"), media)
	if c := s.kim.do("GET", shared, nil).Code; c != 404 {
		t.Errorf("an uncleanable picture was shared: %d", c)
	}
	page := s.kim.do("GET", "/spaces/"+s.space+"/entry/"+s.e.idOf("sam")+"/"+url.PathEscape(entry), nil).Body.String()
	if strings.Contains(page, shared) || !strings.Contains(page, "cannot be shown here") {
		t.Error("the shared page offers a picture it cannot clean")
	}
	// The owner can still download it, as a download and never rendered.
	w := s.sam.do("GET", own, nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "application/octet-stream" || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Errorf("own download: %d %s %s", w.Code, w.Header().Get("Content-Type"), w.Header().Get("Content-Disposition"))
	}
}

func TestADamagedStoredPhotoIsNeverSentHalfDone(t *testing.T) {
	s := newSharing(t)
	entry, media, _ := withPhotos(t, s)
	_, shared := s.photoURL(entry, media)
	// Swap the stored bytes for something that is not a picture but is labelled as one.
	uid := s.e.idOf("sam")
	svc := s.e.srv.ingestFor(s.e.st)
	dek, _ := s.e.st.DataKey(uid)
	key := dek.Bytes()
	dek.Zero()
	_ = svc.Blobs.Delete(uid, media)
	if _, err := svc.Blobs.PutMedia(uid, media, key, strings.NewReader("\xff\xd8\xffthis is not really a jpeg"), 0); err != nil {
		t.Fatal(err)
	}
	if w := s.kim.do("GET", shared, nil); w.Code != 404 || bytes.Contains(w.Body.Bytes(), []byte("not really")) {
		t.Errorf("a broken picture was sent: %d", w.Code)
	}
}

func synthImage() image.Image {
	img := image.NewRGBA(image.Rect(0, 0, 32, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 8), uint8(y * 10), 120, 255})
		}
	}
	return img
}

// queryStrings runs a two-column query with one argument and appends the columns to a and b.
func queryStrings(e *env, q, arg string, a, b *[]string) error {
	return e.st.View(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.Query(q, arg)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var x, y string
			if err := rows.Scan(&x, &y); err != nil {
				return err
			}
			*a, *b = append(*a, x), append(*b, y)
		}
		return rows.Err()
	})
}
