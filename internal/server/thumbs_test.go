package server

import (
	"bytes"
	"image/jpeg"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestOwnThumbnailIsASmallCleanJPEG(t *testing.T) {
	s := newSharing(t)
	entry, media, original := withPhotos(t, s)
	own, _ := s.photoURL(entry, media)
	w := s.sam.do("GET", own+"/thumb", nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("own thumbnail: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	if _, err := jpeg.Decode(bytes.NewReader(w.Body.Bytes())); err != nil {
		t.Fatalf("the thumbnail does not decode: %v", err)
	}
	for _, leak := range []string{"GPSMARK", "COMMENTMARK", "Exif"} {
		if bytes.Contains(w.Body.Bytes(), []byte(leak)) {
			t.Errorf("the thumbnail of the owner's own photo carries %q", leak)
		}
	}
	if bytes.Equal(w.Body.Bytes(), original) {
		t.Error("the thumbnail is the original file")
	}
	h := w.Header()
	if h.Get("Cache-Control") != "no-store" || h.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
		t.Errorf("thumbnail headers: %v", h)
	}
	if w := s.kim.do("GET", own+"/thumb", nil); w.Code != 404 {
		t.Errorf("another person fetched a thumbnail through the own route: %d", w.Code)
	}
	if w := s.sam.do("GET", "/photos/does-not-exist/thumb", nil); w.Code != 404 {
		t.Errorf("made-up id: %d", w.Code)
	}
	if w := s.e.client("192.168.1.99:1").do("GET", own+"/thumb", nil); w.Code == 200 {
		t.Error("a signed-out visitor fetched a thumbnail")
	}
}

// A thumbnail is behind exactly the gates of the picture, and a cached one never outlives them.
func TestAThumbnailIsClosedWhereItsPictureIsClosed(t *testing.T) {
	s := newSharing(t)
	entry, media, _ := withPhotos(t, s)
	_, shared := s.photoURL(entry, media)
	thumb := shared + "/thumb"
	w := s.kim.do("GET", thumb, nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("shared thumbnail: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	for _, leak := range []string{"GPSMARK", "COMMENTMARK", "Exif"} {
		if bytes.Contains(w.Body.Bytes(), []byte(leak)) {
			t.Errorf("a shared thumbnail carries %q", leak)
		}
	}
	// It is now cached; every closing of the gate must still win over the cache.
	code := func() int { return s.kim.do("GET", thumb, nil).Code }
	s.sam.post("/entries", "/entries/private", url.Values{"id": {entry}, "private": {"1"}})
	if c := code(); c != 404 {
		t.Errorf("a cached thumbnail of a private entry was served: %d", c)
	}
	s.sam.post("/entries", "/entries/private", url.Values{"id": {entry}, "private": {"0"}})
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"notes": {"1"}, "photos": {"1"}, "paused": {"1"}})
	if c := code(); c != 404 {
		t.Errorf("a cached thumbnail of a paused member was served: %d", c)
	}
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"notes": {"1"}})
	if c := code(); c != 404 {
		t.Errorf("a cached thumbnail the owner keeps out was served: %d", c)
	}
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", url.Values{"notes": {"1"}, "photos": {"1"}})
	c1 := s.lou.do("GET", thumb, nil)
	c2 := s.kim.do("GET", "/spaces/"+s.space+"/photo/"+s.e.idOf("lou")+"/"+media+"/thumb", nil)
	c3 := s.kim.do("GET", "/spaces/"+s.space+"/photo/"+s.e.idOf("sam")+"/nope/thumb", nil)
	if c1.Code != 404 || c2.Code != 404 || c3.Code != 404 {
		t.Errorf("refusals: %d %d %d", c1.Code, c2.Code, c3.Code)
	}
	s.kim.post("/spaces/"+s.space, "/spaces/"+s.space+"/leave", url.Values{})
	if c := code(); c != 404 {
		t.Errorf("a member who left fetched a cached thumbnail: %d", c)
	}
}

func TestGalleriesLoadThumbnailsAndLinkToThePicture(t *testing.T) {
	s := newSharing(t)
	entry, media, _ := withPhotos(t, s)
	own, shared := s.photoURL(entry, media)
	mine := s.sam.do("GET", "/gallery", nil).Body.String()
	if !strings.Contains(mine, `<img src="`+own+`/thumb"`) || !strings.Contains(mine, `href="`+own+`"`) {
		t.Errorf("the own gallery does not use the thumbnail: %.600s", mine)
	}
	theirs := s.kim.do("GET", "/spaces/"+s.space+"/gallery", nil).Body.String()
	if !strings.Contains(theirs, `<img src="`+shared+`/thumb"`) || !strings.Contains(theirs, `href="`+shared+`"`) {
		t.Errorf("the space gallery does not use the thumbnail: %.600s", theirs)
	}
}

func TestThumbnailCacheIsBoundedAndExpires(t *testing.T) {
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	c := newThumbCache(func() time.Time { return now })
	c.put("a", []byte("one"))
	if got, ok := c.get("a"); !ok || string(got) != "one" {
		t.Fatal("a stored thumbnail is not found")
	}
	now = now.Add(thumbKeep + time.Second)
	if _, ok := c.get("a"); ok {
		t.Error("an old thumbnail was reused")
	}
	for i := 0; i < thumbMaxCount+50; i++ {
		c.put(string(rune('A'+i%26))+strings.Repeat("x", i), []byte("d"))
	}
	if c.order.Len() > thumbMaxCount {
		t.Errorf("the cache holds %d entries, limit %d", c.order.Len(), thumbMaxCount)
	}
	big := bytes.Repeat([]byte{1}, thumbMaxBytes/2+1)
	c.put("big1", big)
	c.put("big2", big)
	if c.bytes > thumbMaxBytes {
		t.Errorf("the cache holds %d bytes, limit %d", c.bytes, thumbMaxBytes)
	}
}
