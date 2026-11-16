package server

import (
	"strings"
	"testing"
)

func TestTheGalleriesListPicturesBlurredAndFollowTheSpace(t *testing.T) {
	s := newSharing(t)
	entry, media, _ := withPhotos(t, s)
	own, shared := s.photoURL(entry, media)
	page := s.sam.do("GET", "/gallery", nil).Body.String()
	if !strings.Contains(page, `<figure class="photo blurred">`) || !strings.Contains(page, `src="`+own+`/thumb"`) || !strings.Contains(page, `href="/entries/`+entry) || !strings.Contains(page, `loading="lazy"`) {
		t.Fatalf("own gallery: %.600s", page)
	}
	// Someone else's gallery does not list sam's picture.
	if p := s.kim.do("GET", "/gallery", nil).Body.String(); strings.Contains(p, own) {
		t.Error("another person's picture is in a personal gallery")
	}
	sg := "/spaces/" + s.space + "/gallery"
	page = s.kim.do("GET", sg, nil).Body.String()
	if !strings.Contains(page, `src="`+shared+`/thumb"`) || strings.Contains(page, "GPS") {
		t.Fatalf("space gallery: %.600s", page)
	}
	// Photos off: nothing is listed.
	s.sam.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", map[string][]string{"notes": {"1"}})
	if p := s.kim.do("GET", sg, nil).Body.String(); strings.Contains(p, shared) {
		t.Error("a photo the owner keeps out is in the gallery")
	}
	// A signed-out visitor and a non-member get nothing.
	if w := s.e.client("192.168.1.99:1").do("GET", sg, nil); w.Code == 200 {
		t.Error("a signed-out visitor opened the gallery")
	}
}
