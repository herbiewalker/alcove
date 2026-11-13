// Package server holds Alcove's HTTP surface. At the scaffold stage it serves
// only /healthz; every real route arrives with its milestone.
package server

import (
	"encoding/json"
	"net/http"
)

// Handler returns the router. version is stamped at build time.
func Handler(version string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": version})
	})
	return mux
}
