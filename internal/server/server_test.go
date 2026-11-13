package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHealthz(t *testing.T) {
	rec := get(Handler("test", nil), "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`"status":"ok"`, `"state":"unlocked"`, `"version":"test"`} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q is missing %s", body, want)
		}
	}
}

func TestHealthzReportsLocked(t *testing.T) {
	rec := get(Handler("v", func() string { return "locked" }), "/healthz")
	if !strings.Contains(rec.Body.String(), `"state":"locked"`) {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("healthz must not be cached")
	}
}

func TestUnknownRouteIs404(t *testing.T) {
	if rec := get(Handler("test", nil), "/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
