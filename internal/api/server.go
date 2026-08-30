// Package api implements the AgentReg REST API (HTTP only: decode request,
// call internal/registry, encode response). It never imports a concrete
// store implementation directly.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/pkulkarni/apreg/internal/api/middleware"
	"github.com/pkulkarni/apreg/internal/registry"
)

type Server struct {
	reg *registry.Registry
}

// NewHandler builds the full /v1 API routing table.
func NewHandler(reg *registry.Registry) http.Handler {
	s := &Server{reg: reg}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/users", s.handleSignup)
	mux.HandleFunc("POST /v1/tokens", s.handleCreateToken)

	mux.HandleFunc("PUT /v1/plugins/{scope}/{name}/{version}", middleware.RequireAuth(reg, s.handlePublish))
	mux.HandleFunc("GET /v1/plugins/{scope}/{name}", s.handleGetPlugin)
	mux.HandleFunc("GET /v1/plugins/{scope}/{name}/{version}", s.handleGetVersion)
	mux.HandleFunc("GET /v1/plugins/{scope}/{name}/{version}/download", s.handleDownload)
	mux.HandleFunc("GET /v1/search", s.handleSearch)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func rawJSON(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage("null")
	}
	return json.RawMessage(s)
}
