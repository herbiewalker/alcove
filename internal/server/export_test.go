package server

import (
	"bytes"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/herbiewalker/alcove/internal/netsec"
	"github.com/herbiewalker/alcove/internal/trystbk"
	"github.com/herbiewalker/alcove/internal/trystbk/synth"
)

const samBackupPW = "Backup-Password-A" // what newSharing uploaded sam's backup with

func stepUp(t *testing.T, c *client, name string) {
	t.Helper()
	if w := c.post("/stepup", "/stepup", url.Values{"password": {name + " has a long password"}, "next": {"/export"}}); w.Code != 303 {
		t.Fatalf("step-up: %d", w.Code)
	}
}

func TestAnExportDownloadsAFileThatOpensWithTheEditsAndNothingDeleted(t *testing.T) {
	s := newSharing(t)
	page := s.sam.do("GET", "/export", nil)
	body := page.Body.String()
	for _, want := range []string{"Export to Tryst", "Replace my current data with the backup (recommended)", "I exported a fresh backup from my phone first", "<b>7</b> trysts", "not changed anything"} {
		if page.Code != 200 || !strings.Contains(body, want) {
			t.Fatalf("the export page lacks %q (%d)", want, page.Code)
		}
	}
	// Edit one entry, delete another.
	id, _ := samEntry(t, s)
	edit := "/entries/" + id + "/edit"
	form := s.sam.do("GET", edit, nil).Body.String()
	const note = "EXPORT-NOTE-SECRET-XYZ"
	if w := s.sam.post(edit, edit, url.Values{"rev": {formRev(t, form)}, "when": {extractWhen(t, form)}, "note": {note}}); w.Code != 303 {
		t.Fatalf("edit: %d", w.Code)
	}
	var ids, x []string
	if err := queryStrings(s.e, `SELECT rid, rid FROM cache_row WHERE owner_id = ? AND tbl = 'encounters' AND rid <> '`+id+`' ORDER BY pos LIMIT 1`, s.e.idOf("sam"), &ids, &x); err != nil || len(ids) != 1 {
		t.Fatal(err)
	}
	gone := ids[0]
	dp := s.sam.do("GET", "/entries/"+gone+"/delete", nil).Body.String()
	if w := s.sam.post("/entries", "/entries/"+gone+"/delete", url.Values{"rev": {formRev(t, dp)}}); w.Code != 303 {
		t.Fatalf("delete: %d", w.Code)
	}
	body = s.sam.do("GET", "/export", nil).Body.String()
	for _, want := range []string{"1 entry changed", "1 entry deleted", "<b>6</b> trysts"} {
		if !strings.Contains(body, want) {
			t.Errorf("the summary lacks %q", want)
		}
	}

	ok := url.Values{"fresh": {"1"}, "use": {"same"}}
	// A change needs a fresh step-up, and nothing is offered without the confirmation.
	if w := s.sam.post("/export", "/export", ok); w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/stepup") {
		t.Fatalf("export without step-up: %d %s", w.Code, w.Header().Get("Location"))
	}
	stepUp(t, s.sam, "sam")
	logged := len(logRows(t, s))
	if w := s.sam.post("/export", "/export", url.Values{"use": {"same"}}); w.Code != 400 || !strings.Contains(w.Body.String(), "Tick the box") {
		t.Errorf("no confirmation: %d", w.Code)
	}
	if w := s.sam.post("/export", "/export", url.Values{"fresh": {"1"}, "use": {"new"}, "pw": {"short"}, "pw2": {"short"}}); w.Code != 400 || !strings.Contains(w.Body.String(), "at least 8") {
		t.Errorf("a short password: %d", w.Code)
	}
	if w := s.sam.post("/export", "/export", url.Values{"fresh": {"1"}, "use": {"new"}, "pw": {"long enough one"}, "pw2": {"long enough two"}}); w.Code != 400 || !strings.Contains(w.Body.String(), "not the same") {
		t.Errorf("mismatched passwords: %d", w.Code)
	}
	if len(logRows(t, s)) != logged {
		t.Error("a refused export wrote to the admin log")
	}

	// The real one, with the same password as the upload.
	r := s.sam.post("/export", "/export", ok)
	if r.Code != 200 || r.Header().Get("Content-Type") != "application/octet-stream" || r.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("export: %d %s %s", r.Code, r.Header().Get("Content-Type"), r.Header().Get("Cache-Control"))
	}
	if !regexp.MustCompile(`^attachment; filename="alcove-sam-\d{8}\.tryst"$`).MatchString(r.Header().Get("Content-Disposition")) {
		t.Errorf("filename: %q", r.Header().Get("Content-Disposition"))
	}
	if !bytes.HasPrefix(r.Body.Bytes(), []byte("TRYSTBK1")) {
		t.Fatal("the download is not a Tryst backup")
	}
	bk, err := trystbk.Read(bytes.NewReader(r.Body.Bytes()), samBackupPW)
	if err != nil {
		t.Fatalf("the download does not open: %v", err)
	}
	defer bk.Close()
	if bk.RowCount("encounters") != 6 {
		t.Errorf("%d entries in the file, want 6", bk.RowCount("encounters"))
	}
	var sawNote bool
	for _, row := range bk.Table("encounters").Rows {
		if v, _ := row.String("id"); v == gone {
			t.Error("a deleted entry is in the export")
		}
		if n, _ := row.String("note"); n == note {
			sawNote = true
		}
	}
	if !sawNote {
		t.Error("the edit is not in the export")
	}

	// One log line: counts only. And nothing sensitive reached the server log (N4).
	n := 0
	for _, e := range logRows(t, s) {
		if e.Action == "export.create" {
			n++
			if e.Actor != s.e.idOf("sam") || e.TargetType != "user" || !strings.HasPrefix(e.Fields, "entries:6,partners:") {
				t.Errorf("export log: %+v", e)
			}
		}
		if strings.Contains(e.Fields+e.TargetID, note) {
			t.Error("the admin log carries content")
		}
	}
	if n != 1 {
		t.Fatalf("expected one export.create, got %d", n)
	}
	out := s.e.logs.String()
	for _, secret := range []string{note, samBackupPW, "long enough", "Synthetic", "tryst-2026", "sam has a long password"} {
		if strings.Contains(out, secret) {
			t.Errorf("the server log contains %q", secret)
		}
	}
	if !strings.Contains(out, "export created") {
		t.Error("an export should leave a line with counts")
	}

	// A new password: the file opens with it and not with the old one.
	r = s.sam.post("/export", "/export", url.Values{"fresh": {"1"}, "use": {"new"}, "pw": {"brand new password"}, "pw2": {"brand new password"}})
	if r.Code != 200 {
		t.Fatalf("export with a new password: %d %.200s", r.Code, r.Body.String())
	}
	if _, err := trystbk.Read(bytes.NewReader(r.Body.Bytes()), samBackupPW); err == nil {
		t.Error("the old password opened an export sealed under a new one")
	}
	if b2, err := trystbk.Read(bytes.NewReader(r.Body.Bytes()), "brand new password"); err != nil {
		t.Errorf("the new password does not open it: %v", err)
	} else {
		b2.Close()
	}
}

func TestOnlyTheOwnerCanExportAndOnlyWithAnUpload(t *testing.T) {
	s := newSharing(t)
	stepUp(t, s.kim, "kim")
	// Kim exports kim's own data: the file holds kim's rows, not sam's.
	r := s.kim.post("/export", "/export", url.Values{"fresh": {"1"}, "use": {"same"}})
	if r.Code != 200 {
		t.Fatalf("kim's export: %d", r.Code)
	}
	if bk, err := trystbk.Read(bytes.NewReader(r.Body.Bytes()), "Backup-Password-B"); err != nil || bk.RowCount("encounters") != 5 {
		t.Fatalf("kim's export: %v", err)
	} else {
		bk.Close()
	}
	// A person with no upload, and a signed-out visitor, get nothing.
	zed := person(t, s.e, s.admin, "zed")
	if w := zed.do("GET", "/export", nil); w.Code != 404 || !strings.Contains(w.Body.String(), "Nothing to export") {
		t.Errorf("no upload: %d", w.Code)
	}
	if w := s.e.client("192.168.1.77:1").do("GET", "/export", nil); w.Code == 200 {
		t.Error("a signed-out visitor opened the export page")
	}
	if w := s.e.client("192.168.1.77:1").do("POST", "/export", url.Values{"fresh": {"1"}}); w.Code == 200 {
		t.Error("a signed-out visitor exported")
	}
}

func TestAPasswordNotKeptMustBeTypedAndIsChecked(t *testing.T) {
	e := newEnv(t, netsec.Env{})
	admin := adminReady(t, e)
	lee := person(t, e, admin, "lee")
	res := synthFile(t, synth.Options{Seed: 5, Encounters: 3, Password: "Lee-Backup-Password"})
	if w := lee.upload(res.File, "b.tryst", res.Password, false, false); w.Code != 303 {
		t.Fatalf("upload: %d", w.Code)
	}
	stepUp(t, lee, "lee")
	if b := lee.do("GET", "/export", nil).Body.String(); !strings.Contains(b, `name="upw"`) {
		t.Error("the page does not ask for a password that is not kept")
	}
	f := url.Values{"fresh": {"1"}, "use": {"same"}}
	if w := lee.post("/export", "/export", f); w.Code != 400 || !strings.Contains(w.Body.String(), "type it below") {
		t.Errorf("no password given: %d", w.Code)
	}
	f.Set("upw", "wrong password")
	if w := lee.post("/export", "/export", f); w.Code != 400 || !strings.Contains(w.Body.String(), "not the password") {
		t.Errorf("a wrong password: %d", w.Code)
	}
	f.Set("upw", res.Password)
	if w := lee.post("/export", "/export", f); w.Code != 200 {
		t.Fatalf("the right password: %d", w.Code)
	}
	if strings.Contains(e.logs.String(), "Lee-Backup-Password") || strings.Contains(e.logs.String(), "wrong password") {
		t.Error("a typed password reached the log")
	}
}
