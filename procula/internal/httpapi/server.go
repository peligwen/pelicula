// Package httpapi provides the HTTP server for the procula API.
// The Server type wires the mux and injects dependencies into handlers.
// Route definitions and handler logic that reference root-package types
// are forwarded via the Handler() method which accepts an external http.Handler
// for the routes still owned by the root package during migration.
package httpapi

import (
	"encoding/json"
	"net/http"

	"procula/internal/actions"
	"procula/internal/catalog"
	"procula/internal/config"
	"procula/internal/queue"
)

// Server holds the dependencies injected into HTTP handlers.
type Server struct {
	q      *queue.Queue
	reg    *actions.Registry
	el     *catalog.EventLog
	cfg    config.Config
	apiKey string
}

// New creates a Server with the given dependencies.
func New(q *queue.Queue, reg *actions.Registry, el *catalog.EventLog, cfg config.Config) *Server {
	return &Server{
		q:      q,
		reg:    reg,
		el:     el,
		cfg:    cfg,
		apiKey: cfg.ProculaAPIKey,
	}
}

// Handler returns an http.Handler for the procula API.
// During migration, the caller may pass a fallback handler that covers routes
// still implemented in the root package; use http.NewServeMux() + mux.Handle
// to compose. This method registers only the routes owned by this package.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", s.handlePing)
	mux.HandleFunc("GET /api/procula/status", s.handleStatus)
	mux.HandleFunc("GET /api/procula/events", s.handleListEvents)
	mux.HandleFunc("GET /api/procula/actions/registry", s.handleListActionRegistry)
	return mux
}

// requireAPIKey is middleware that enforces X-API-Key on mutating endpoints.
// When apiKey is empty it is a no-op (backward-compatible).
func (s *Server) requireAPIKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.apiKey != "" && r.Header.Get("X-API-Key") != s.apiKey {
			writeError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Server) handlePing(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`)) //nolint:errcheck
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"status": "ok",
		"queue":  s.q.Status(),
	})
}

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	if s.el == nil {
		writeJSON(w, map[string]any{"events": []any{}, "total": 0})
		return
	}
	catalog.HandleListEvents(s.el, w, r, writeJSON)
}

func (s *Server) handleListActionRegistry(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, s.reg.List())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}
