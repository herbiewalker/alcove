package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/herbiewalker/alcove/internal/trystbk/synth"
)

// Compare reads at most compareCap entries of one member and says so (audit 2026-11-21, input lens).
func TestCompareIsBoundedPerMemberAndSaysSo(t *testing.T) {
	s := newSharing(t)
	if w := s.kim.do("GET", "/spaces/"+s.space+"/compare", nil); w.Code != 200 || strings.Contains(w.Body.String(), "Only the newest") {
		t.Fatalf("a normal space: %d, unexpected cut notice", w.Code)
	}
	res := synthFile(t, synth.Options{Seed: 77, Encounters: compareCap + 50, Password: "Backup-Password-Big"})
	if w := s.sam.upload(res.File, "big.tryst", res.Password, true, false); w.Code != 303 {
		t.Fatalf("upload: %d", w.Code)
	}
	w := s.kim.do("GET", "/spaces/"+s.space+"/compare", nil)
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, "Only the newest 10,000 entries are counted for") {
		t.Fatalf("the cut was not announced: %d", w.Code)
	}
	// The list itself is still complete and paged.
	if _, list := s.list(s.kim, s.space); !strings.Contains(list, "in all") {
		t.Error("the entries list broke")
	}
}

// A huge page number is clamped, not turned into a negative offset.
func TestHugePageNumbersAreClamped(t *testing.T) {
	s := newSharing(t)
	for _, p := range []string{"9223372036854775807", "99999999999999999999", "-5", "abc"} {
		for _, path := range []string{"/entries", "/spaces/" + s.space + "/entries", "/gallery"} {
			if w := s.sam.do("GET", path+"?page="+url.QueryEscape(p), nil); w.Code != 200 {
				t.Errorf("%s?page=%s gave %d", path, p, w.Code)
			}
		}
	}
	if w := s.admin.do("GET", "/admin/log?page=9223372036854775807", nil); w.Code != 200 {
		t.Errorf("admin log with a huge page: %d", w.Code)
	}
}

// A person cannot take a name with direction-changing characters at sign-up or in the profile.
func TestDisplayNamesWithBidiCharactersAreRefused(t *testing.T) {
	s := newSharing(t)
	if w := s.sam.post("/profile", "/profile", url.Values{"name": {"Sam‮evil"}}); w.Code == 303 {
		t.Error("the profile accepted a direction override in the name")
	}
	if u, err := s.e.userByName("sam"); err != nil || strings.Contains(u.DisplayName, "‮") {
		t.Error("the stored name carries a direction override")
	}
}
