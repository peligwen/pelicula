// Package api holds the HTTP handlers behind /api/*: status, search,
// downloads, jobs, settings and the *arr import webhook.
//
// Authentication (login, sessions) lives in internal/auth. This package only
// asks it to guard routes (Guard); every route is for the one signed-in
// administrator. Everything else the handlers talk to is a narrow interface
// defined here, so the concrete clients are wired in by cmd/pelicula-server
// and unit tests can use plain fakes.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"pelicula/internal/config"
	"pelicula/internal/store"
)

// ArrClient is the slice of the Sonarr/Radarr/Prowlarr client the handlers use.
type ArrClient interface {
	Ping(ctx context.Context) error
	TriggerCommand(ctx context.Context, payload map[string]any) error
	GetMovie(ctx context.Context, id int) (map[string]any, error)
	LookupMovie(ctx context.Context, term string) ([]map[string]any, error)
	LookupMovieByTmdbID(ctx context.Context, tmdbID int) ([]map[string]any, error)
	AddMovie(ctx context.Context, payload map[string]any) (map[string]any, error)
	GetSeriesByID(ctx context.Context, id int) (map[string]any, error)
	LookupSeries(ctx context.Context, term string) ([]map[string]any, error)
	AddSeries(ctx context.Context, payload map[string]any) (map[string]any, error)
	GetQualityProfiles(ctx context.Context) ([]map[string]any, error)
	GetAllQueueRecords(ctx context.Context) ([]map[string]any, error)
	DeleteQueueItem(ctx context.Context, id int, removeFromClient, blocklist bool) error
}

// Torrent is one qBittorrent torrent as the downloads endpoint reports it.
// The JSON tags are the response shape of GET /api/downloads.
type Torrent struct {
	Hash     string  `json:"hash"`
	Name     string  `json:"name"`
	State    string  `json:"state"`
	Category string  `json:"category"`
	Progress float64 `json:"progress"`
	Dlspeed  int64   `json:"dlspeed"`
	Upspeed  int64   `json:"upspeed"`
	Eta      int64   `json:"eta"`
	Size     int64   `json:"size"`
}

// TransferInfo is qBittorrent's global transfer speed (bytes/s).
type TransferInfo struct {
	DlSpeed int64 `json:"dl_speed"`
	UpSpeed int64 `json:"up_speed"`
}

// QBTClient is the slice of the qBittorrent client the handlers use. The real
// client has its own Torrent/TransferInfo types, so the integrator wraps it
// in a thin adapter that converts to the types above.
type QBTClient interface {
	Ping(ctx context.Context) error
	ListTorrents(ctx context.Context) ([]Torrent, error)
	GetTransferInfo(ctx context.Context) (*TransferInfo, error)
	StopTorrent(ctx context.Context, hash string) error
	StartTorrent(ctx context.Context, hash string) error
	DeleteTorrent(ctx context.Context, hash string, deleteFiles bool) error
}

// Pinger is the slice of the Jellyfin client the handlers use (the status
// probe). *jellyfin.Client satisfies it.
type Pinger interface {
	Ping(ctx context.Context) error
}

// VPNStatus is gluetun's view of the tunnel's public identity.
type VPNStatus struct {
	PublicIP string
	Country  string
	City     string
}

// GluetunClient is the slice of the gluetun control client the handlers use.
type GluetunClient interface {
	Ping(ctx context.Context) error
	GetPublicIP(ctx context.Context) (*VPNStatus, error)
	GetForwardedPort(ctx context.Context) (int, error)
	GetTunnelStatus(ctx context.Context) (string, error)
}

// Guard wraps a handler so that only a signed-in session reaches it
// (auth.Auth satisfies this).
type Guard interface {
	Guard(h http.Handler) http.Handler
}

// Server carries everything the handlers need. Build one, set the fields
// (nil is fine for anything VPN-only), then call Routes. A Server must not be
// copied after first use.
type Server struct {
	Cfg      config.Config
	Store    *store.Store
	Auth     Guard
	Sonarr   ArrClient
	Radarr   ArrClient
	Prowlarr ArrClient     // nil without VPN
	QBT      QBTClient     // nil without VPN
	Gluetun  GluetunClient // nil without VPN
	Jellyfin Pinger
	Wired    func() bool // autowire finished
	Kick     func()      // wake the pipeline worker after enqueue (may be nil)
	Log      *slog.Logger

	statusMu   sync.Mutex
	statusAt   time.Time
	statusSnap statusSnapshot
}

// Routes registers every route this package owns. Auth-owned routes
// (/api/auth/*) are registered by auth.
func (s *Server) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.Handle("GET /api/status", s.guard(s.handleStatus))

	mux.Handle("GET /api/search", s.guard(s.handleSearch))
	mux.Handle("POST /api/search/add", s.guard(s.handleSearchAdd))

	mux.Handle("GET /api/downloads", s.guard(s.handleListDownloads))
	mux.Handle("POST /api/downloads/{hash}/pause", s.guard(s.handlePauseDownload))
	mux.Handle("POST /api/downloads/{hash}/resume", s.guard(s.handleResumeDownload))
	mux.Handle("DELETE /api/downloads/{hash}", s.guard(s.handleDeleteDownload))

	mux.Handle("GET /api/jobs", s.guard(s.handleListJobs))
	mux.Handle("POST /api/jobs/{id}/retry", s.guard(s.handleRetryJob))

	mux.Handle("GET /api/settings", s.guard(s.handleGetSettings))
	mux.Handle("PUT /api/settings", s.guard(s.handlePutSettings))

	// Authenticated by the shared secret header, not a session.
	mux.HandleFunc("POST /api/hooks/import", s.handleImportHook)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"wired":   s.wired(),
		"version": s.Cfg.Version,
	})
}

// ── plumbing ─────────────────────────────────────────────────────────────────

// guard wraps h with the auth Guard. With no Guard configured it fails closed
// rather than serving the route unauthenticated.
func (s *Server) guard(h http.HandlerFunc) http.Handler {
	if s.Auth == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "auth is not configured")
		})
	}
	return s.Auth.Guard(h)
}

func (s *Server) wired() bool { return s.Wired != nil && s.Wired() }

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Server) kick() {
	if s.Kick != nil {
		s.Kick()
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func noContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// readJSON decodes a request body (max 1 MB) into v. An empty body is an
// error unless optional. On failure it writes the response and returns false.
func readJSON(w http.ResponseWriter, r *http.Request, v any, optional bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	err := json.NewDecoder(r.Body).Decode(v)
	if err == nil || (optional && errors.Is(err, io.EOF)) {
		return true
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return false
	}
	writeError(w, http.StatusBadRequest, "invalid JSON body")
	return false
}

// ── tolerant accessors for decoded *arr JSON ─────────────────────────────────

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func num(m map[string]any, key string) int { return int(num64(m, key)) }

func num64(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}

func flag(m map[string]any, key string) bool {
	b, _ := m[key].(bool)
	return b
}
