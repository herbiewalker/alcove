package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/herbiewalker/alcove/internal/trystbk/synth"
)

// samWithPictures gives Sam a backup in which the profile and every partner has a picture, and
// returns it with the id of one entry that has a partner.
func samWithPictures(t *testing.T, s *sharing) (res *synth.Result, entry string) {
	t.Helper()
	res = synthFile(t, synth.Options{Seed: 62, Encounters: 7, Partners: 2, Avatars: true, Password: "Sam-Avatar-Backup-1"})
	if w := s.sam.upload(res.File, "b.tryst", res.Password, true, false); w.Code != 303 {
		t.Fatalf("upload with pictures: %d", w.Code)
	}
	var enc, partner []string
	if err := queryStrings(s.e, `SELECT json_extract(data, '$.encounterId'), json_extract(data, '$.partnerId') FROM cache_row WHERE owner_id = ? AND tbl = 'encounter_partner' ORDER BY pos`, s.e.idOf("sam"), &enc, &partner); err != nil || len(enc) == 0 {
		t.Fatalf("no entry with a partner: %v", err)
	}
	return res, enc[0]
}

func (s *sharing) setSpace(c *client, form url.Values) {
	s.e.t.Helper()
	if w := c.post("/spaces/"+s.space, "/spaces/"+s.space+"/settings", form); w.Code != 303 {
		s.e.t.Fatalf("space settings: %d", w.Code)
	}
}

func isJPEG(b []byte) bool { return len(b) > 3 && b[0] == 0xff && b[1] == 0xd8 && b[2] == 0xff }

// D-52: a member's profile picture and their partners' pictures are open to the space under the
// owner's "include photos" choice, independent of partner names (R35), and to no one outside it.
func TestProfileAndPartnerPicturesAreOpenToTheSpaceAndNoOneElse(t *testing.T) {
	s := newSharing(t)
	res, entry := samWithPictures(t, s)
	sam := s.e.idOf("sam")
	profileURL := "/spaces/" + s.space + "/avatar/" + sam
	partnerURL := func(pid string) string { return "/spaces/" + s.space + "/partner-photo/" + sam + "/" + pid }

	// The member list shows Sam's card with a picture, to Kim; the picture is served, small and clean.
	if page := s.kim.do("GET", "/spaces/"+s.space, nil).Body.String(); !strings.Contains(page, `src="`+profileURL+`"`) {
		t.Fatalf("Sam's card has no picture on Kim's page")
	}
	w := s.kim.do("GET", profileURL, nil)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" || !isJPEG(w.Body.Bytes()) || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("Kim fetching Sam's profile picture: %d %s", w.Code, w.Header().Get("Content-Type"))
	}

	// A partner's picture is shown with the entry that partner is in, although partner names are hidden by default.
	page := s.kim.do("GET", "/spaces/"+s.space+"/entry/"+sam+"/"+entry, nil).Body.String()
	if !strings.Contains(page, "/partner-photo/") {
		t.Fatalf("the entry shows no partner picture: %.800s", page)
	}
	if strings.Contains(page, "Synthetic Partner") {
		t.Error("a partner's name is shown although names are hidden")
	}
	served := 0
	for _, pid := range res.PartnerIDs {
		if w := s.kim.do("GET", partnerURL(pid), nil); w.Code == 200 && isJPEG(w.Body.Bytes()) {
			served++
		}
	}
	if served == 0 {
		t.Error("no partner picture could be fetched by a member of the space")
	}

	// Outsiders: another space's member, a non-member, a signed-out visitor.
	for name, c := range map[string]*client{"lou (other space)": s.lou, "signed out": s.e.client("192.168.1.98:1")} {
		for _, u := range []string{profileURL, partnerURL(res.PartnerIDs[0])} {
			if w := c.do("GET", u, nil); w.Code == 200 {
				t.Errorf("%s reached %s", name, u)
			}
		}
	}
	// A guessed id gets nothing.
	if w := s.kim.do("GET", partnerURL("no-such-partner"), nil); w.Code != 404 {
		t.Errorf("an unknown partner: %d", w.Code)
	}
	if w := s.kim.do("GET", "/spaces/"+s.space+"/avatar/"+s.e.idOf("kim"), nil); w.Code != 404 {
		t.Errorf("a member with no picture: %d", w.Code)
	}

	// The owner sees their own, by the own routes.
	if w := s.sam.do("GET", "/avatar/profile", nil); w.Code != 200 || !isJPEG(w.Body.Bytes()) {
		t.Errorf("Sam's own profile picture: %d", w.Code)
	}
	if w := s.sam.do("GET", "/avatar/partner/"+res.PartnerIDs[0], nil); w.Code != 200 {
		t.Errorf("Sam's own partner picture: %d", w.Code)
	}
	if w := s.kim.do("GET", "/avatar/profile", nil); w.Code != 404 {
		t.Errorf("Kim reached a picture by the own route: %d", w.Code)
	}
	if !strings.Contains(s.sam.do("GET", "/profile", nil).Body.String(), `src="/avatar/profile"`) {
		t.Error("Sam's profile page does not show Sam's picture")
	}

	// Include photos off: gone for the space, still there for the owner.
	s.setSpace(s.sam, url.Values{"notes": {"1"}})
	for _, u := range []string{profileURL, partnerURL(res.PartnerIDs[0])} {
		if w := s.kim.do("GET", u, nil); w.Code != 404 {
			t.Errorf("with photos off, %s gave %d", u, w.Code)
		}
	}
	if page := s.kim.do("GET", "/spaces/"+s.space, nil).Body.String(); strings.Contains(page, profileURL) {
		t.Error("the card still shows a picture with photos off")
	}
	if w := s.sam.do("GET", "/avatar/profile", nil); w.Code != 200 {
		t.Errorf("the owner lost their own picture: %d", w.Code)
	}
	// On again, and a pause hides them too.
	s.setSpace(s.sam, url.Values{"notes": {"1"}, "photos": {"1"}})
	if w := s.kim.do("GET", profileURL, nil); w.Code != 200 {
		t.Errorf("after turning photos back on: %d", w.Code)
	}
	s.setSpace(s.sam, url.Values{"notes": {"1"}, "photos": {"1"}, "paused": {"1"}})
	if w := s.kim.do("GET", profileURL, nil); w.Code != 404 {
		t.Errorf("a paused member's picture: %d", w.Code)
	}
	s.setSpace(s.sam, url.Values{"notes": {"1"}, "photos": {"1"}})

	// A new upload without pictures removes them.
	plain := synthFile(t, synth.Options{Seed: 63, Encounters: 7, Password: "Sam-Plain-Backup-1"})
	if w := s.sam.upload(plain.File, "b.tryst", plain.Password, true, false); w.Code != 303 {
		t.Fatalf("plain upload: %d", w.Code)
	}
	if w := s.sam.do("GET", "/avatar/profile", nil); w.Code != 404 {
		t.Errorf("the old picture survived a new upload: %d", w.Code)
	}
	if w := s.kim.do("GET", profileURL, nil); w.Code != 404 {
		t.Errorf("the old picture is still shown to the space: %d", w.Code)
	}
}

// D-52: the administrator sees every photo and picture through the person pages, each opening is one
// log line without content, and the owner's activity page lists it.
func TestTheAdministratorSeesPicturesAndEachLookIsLogged(t *testing.T) {
	s := newSharing(t)
	res, entry := samWithPictures(t, s)
	sam := s.e.idOf("sam")
	base := "/admin/people/" + sam

	page := s.admin.do("GET", base+"/entries/"+entry, nil).Body.String()
	if !strings.Contains(page, base+"/avatar/partner/") {
		t.Fatalf("the administrator's entry page shows no partner picture: %.600s", page)
	}
	for _, c := range []struct{ path, what string }{
		{base + "/avatar", "profile-photo"},
		{base + "/avatar/partner/" + res.PartnerIDs[0], "partner-photo"},
	} {
		before := logCount(t, s, "data.view")
		w := s.admin.do("GET", c.path, nil)
		if w.Code != 200 || !isJPEG(w.Body.Bytes()) {
			t.Fatalf("%s: %d", c.path, w.Code)
		}
		if logCount(t, s, "data.view") != before+1 {
			t.Errorf("%s is not logged once", c.path)
		}
		var found bool
		for _, l := range logRows(t, s) {
			if l.Action == "data.view" && l.Fields == c.what && strings.HasPrefix(l.TargetID, sam+":") && l.Actor == s.e.idOf("owner") {
				found = true
			}
		}
		if !found {
			t.Errorf("no data.view line with %q for %s", c.what, c.path)
		}
	}
	// An entry photo: needs a backup with photos.
	photos := synthFile(t, synth.Options{Seed: 64, Encounters: 7, Photos: 2, Password: "Sam-Photos-Backup-1"})
	if w := s.sam.upload(photos.File, "b.tryst", photos.Password, true, false); w.Code != 303 {
		t.Fatalf("upload: %d", w.Code)
	}
	if w := s.admin.do("GET", base+"/photos/"+photos.MediaIDs[0], nil); w.Code != 200 || !isJPEG(w.Body.Bytes()) {
		t.Errorf("the administrator's entry photo: %d", w.Code)
	}
	if logCount(t, s, "data.view") == 0 {
		t.Error("the photo look is not logged")
	}
	for _, l := range logRows(t, s) {
		if strings.Contains(l.Fields+l.TargetID, "image") || strings.Contains(l.Fields, "JFIF") {
			t.Errorf("the log carries content: %+v", l)
		}
	}

	// Nobody else gets these routes.
	if w := s.kim.do("GET", base+"/avatar", nil); w.Code == 200 {
		t.Errorf("a member reached the administrator's route: %d", w.Code)
	}
	if w := s.e.client("192.168.1.97:1").do("GET", base+"/avatar", nil); w.Code == 200 {
		t.Errorf("a signed-out visitor reached the administrator's route: %d", w.Code)
	}

	// The owner sees it on the activity page once an administrator switches that on.
	if w := s.admin.post("/admin/settings", "/admin/settings/activity", url.Values{"activity": {"1"}}); w.Code != 303 {
		t.Fatalf("switch: %d", w.Code)
	}
	act := s.sam.do("GET", "/account/activity", nil).Body.String()
	if !strings.Contains(act, "opened your profile picture") || !strings.Contains(act, "opened one of your photos") && !strings.Contains(act, "opened a partner") {
		t.Errorf("the activity page does not list the picture looks: %.1500s", act)
	}
}
