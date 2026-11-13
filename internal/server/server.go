// Package server holds Alcove's HTTP surface. Until the web UI lands (M2b) it serves only
// /healthz; every real route arrives with its milestone.
package server

import (
	"encoding/json"
	"net/http"
)

// Handler returns the router. version is stamped at build time. state reports "unlocked" or
// "locked" (the store is open or not); it may be nil in tests, which reads as "unlocked".
func Handler(version string, state func() string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		st := "unlocked"
		if state != nil {
			st = state()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "state": st, "version": version})
	})
	return mux
}
