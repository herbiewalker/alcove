package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Template rules from the UI/accessibility audit (2026-11-22), checked on the source so a new page
// cannot quietly break them.
func TestTemplatesKeepAccessibilityRules(t *testing.T) {
	files, err := filepath.Glob("templates/*.html")
	if err != nil || len(files) == 0 {
		t.Fatalf("no templates found: %v", err)
	}
	scroll := regexp.MustCompile(`<div class="scroll"[^>]*>`)
	generic := regexp.MustCompile(`<button[^>]*>(Delete|Restore|Disable|Enable|Cancel|Remove|Reset[^<]*|Make owner)</button>`)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, m := range scroll.FindAllString(src, -1) {
			if !strings.Contains(m, `tabindex="0"`) || !strings.Contains(m, `role="region"`) || !strings.Contains(m, `aria-label=`) {
				t.Errorf("%s: a scrolling table box must be focusable and named: %s", f, m)
			}
		}
		if n, c := strings.Count(src, "<table"), strings.Count(src, "<caption"); n != c {
			t.Errorf("%s: %d tables but %d captions", f, n, c)
		}
		if strings.Contains(src, "<th></th>") {
			t.Errorf("%s: an empty header cell has no name; use <span class=\"sr\">", f)
		}
		for _, m := range generic.FindAllString(src, -1) {
			if !strings.Contains(m, "aria-label=") {
				t.Errorf("%s: a row button needs a name that says which row: %s", f, m)
			}
		}
	}
	base, _ := os.ReadFile("templates/base.html")
	if !strings.Contains(string(base), `href="#main"`) || !strings.Contains(string(base), `<main id="main"`) {
		t.Error("base.html must keep the skip link and the main target")
	}
	css, _ := os.ReadFile("static/app.css")
	if strings.Count(string(css), "--field-line:") < 3 {
		t.Error("the field border token is missing from a theme")
	}
}
