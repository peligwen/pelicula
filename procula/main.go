package procula

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// configDir is the global config directory.
// Set once at startup from CONFIG_DIR env var.
var configDir string

// appDB is the global SQLite database handle set at startup.
var appDB *sql.DB

// Version is the current Procula version, injected at build time via -ldflags.
var Version = "dev"

// proculaAPIKey is the shared secret required on mutating (POST) requests.
// Empty means auth is disabled (backward-compatible with existing installs
// that don't have PROCULA_API_KEY set).
var proculaAPIKey string

// requireAPIKey is middleware that enforces X-API-Key on mutating endpoints.
// When proculaAPIKey is empty it is a no-op so old installs keep working.
func requireAPIKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if proculaAPIKey != "" && r.Header.Get("X-API-Key") != proculaAPIKey {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// Server holds the dependencies for HTTP handlers.
type Server struct {
	queue       *Queue
	db          *sql.DB
	configDir   string
	peliculaAPI string
}

// Run is the application entry point, called from cmd/procula/main.go.
func Run() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	configDir = env("CONFIG_DIR", "/config")
	peliculaAPI := env("PELICULA_API_URL", "http://pelicula-api:8181")
	proculaAPIKey = env("PROCULA_API_KEY", "")

	db, err := OpenDB(filepath.Join(configDir, "procula.db"))
	if err != nil {
		slog.Error("database initialization failed", "component", "main", "error", err)
		os.Exit(1)
	}
	appDB = db

	if os.Getenv("PROCULA_ALLOW_JSON_MIGRATION") == "1" {
		migrateAllJSON(db, configDir)
	}

	// One-shot: import any existing JSONL notifications feed into SQLite.
	migrateNotificationsFeedToDB(db, configDir)

	q, err := NewQueue(db)
	if err != nil {
		slog.Error("queue initialization failed", "component", "main", "error", err)
		os.Exit(1)
	}

	jobs := q.List(ListFilter{})
	slog.Info("queue loaded", "component", "queue", "job_count", len(jobs))

	el, err := NewEventLog(configDir)
	if err != nil {
		slog.Error("event log initialization failed", "component", "main", "error", err)
		os.Exit(1)
	}
	eventLog = el

	// Seed default transcode profiles on first startup (no-op if profiles exist).
	SeedDefaultProfiles(configDir)

	// Load library registry from pelicula-api. Falls back to built-in defaults
	// (movies + tv) if the API is not yet reachable.
	loadLibraries(peliculaAPI)

	// Single worker processes jobs sequentially
	registerBuiltinActions()
	go RunWorker(q, configDir, peliculaAPI)
	go RunStorageMonitor(configDir)
	go RunUpdateChecker(configDir)
	// Archive terminal jobs older than 30 days, every 24 hours.
	go runArchiveLoop(q)

	srv := &Server{queue: q, db: db, configDir: configDir, peliculaAPI: peliculaAPI}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /ping", srv.handlePing)
	mux.HandleFunc("GET /api/procula/status", srv.handleStatus)
	mux.HandleFunc("GET /api/procula/jobs", srv.handleListJobs)
	mux.HandleFunc("POST /api/procula/jobs", requireAPIKey(srv.handleCreateJob))
	mux.HandleFunc("GET /api/procula/jobs/{id}", srv.handleGetJob)
	mux.HandleFunc("POST /api/procula/jobs/{id}/retry", requireAPIKey(srv.handleRetryJob))
	mux.HandleFunc("POST /api/procula/jobs/{id}/cancel", requireAPIKey(srv.handleCancelJob))
	mux.HandleFunc("POST /api/procula/jobs/{id}/resub", requireAPIKey(srv.handleResubJob))
	mux.HandleFunc("GET /api/procula/storage", handleStorage)
	mux.HandleFunc("POST /api/procula/storage/scan", requireAPIKey(handleStorageScan))
	mux.HandleFunc("GET /api/procula/updates", handleUpdates)
	mux.HandleFunc("GET /api/procula/notifications", srv.handleNotifications)
	mux.HandleFunc("GET /api/procula/settings", srv.handleGetSettings)
	mux.HandleFunc("POST /api/procula/settings", requireAPIKey(srv.handleSaveSettings))
	mux.HandleFunc("GET /api/procula/profiles", srv.handleListProfiles)
	mux.HandleFunc("POST /api/procula/profiles", requireAPIKey(srv.handleSaveProfile))
	mux.HandleFunc("DELETE /api/procula/profiles/{name}", requireAPIKey(srv.handleDeleteProfile))
	mux.HandleFunc("GET /api/procula/dualsub-profiles", srv.handleListDualSubProfiles)
	mux.HandleFunc("POST /api/procula/dualsub-profiles", requireAPIKey(srv.handleSaveDualSubProfile))
	mux.HandleFunc("PUT /api/procula/dualsub-profiles/{name}", requireAPIKey(srv.handleSaveDualSubProfile))
	mux.HandleFunc("DELETE /api/procula/dualsub-profiles/{name}", requireAPIKey(srv.handleDeleteDualSubProfile))
	mux.HandleFunc("GET /api/procula/subtitle-tracks", srv.handleSubtitleTracks)
	mux.HandleFunc("DELETE /api/procula/dualsub-sidecars", requireAPIKey(srv.handleDeleteDualSubSidecar))
	mux.HandleFunc("POST /api/procula/subtitles/search", requireAPIKey(srv.handleSubSearch))
	mux.HandleFunc("POST /api/procula/transcode", requireAPIKey(srv.handleManualTranscode))
	mux.HandleFunc("GET /api/procula/events", srv.handleListEvents)
	mux.HandleFunc("POST /api/procula/actions", requireAPIKey(srv.handleCreateAction))
	mux.HandleFunc("GET /api/procula/actions/registry", srv.handleListActionRegistry)
	mux.HandleFunc("GET /api/procula/catalog/flags", srv.handleCatalogFlags)
	mux.HandleFunc("GET /api/procula/blocked-releases", srv.handleListBlockedReleases)
	mux.HandleFunc("DELETE /api/procula/blocked-releases/{id}", requireAPIKey(srv.handleDeleteBlockedRelease))

	slog.Info("listening", "component", "main", "addr", ":8282")
	serveWithShutdown(":8282", mux)
}

func serveWithShutdown(addr string, handler http.Handler) {
	srv := &http.Server{Addr: addr, Handler: handler}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server exited", "component", "main", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	slog.Info("shutdown signal received", "component", "main")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "component", "main", "error", err)
	}
}

// isLibraryPath returns true for any path under /media/ (the library root).
// Used to restrict manual transcode and subtitle ops to already-imported library files.
func isLibraryPath(path string) bool {
	return path == "/media" || strings.HasPrefix(path, "/media/")
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// runArchiveLoop deletes terminal jobs older than 30 days every 24 hours.
// Runs as a background goroutine; exits when the process is killed.
func runArchiveLoop(q *Queue) {
	const retention = 30 * 24 * time.Hour
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		n, err := q.ArchiveOldJobs(retention)
		if err != nil {
			slog.Warn("archive: failed to delete old jobs", "component", "archive", "error", err)
			continue
		}
		if n > 0 {
			slog.Info("archive: deleted old terminal jobs", "component", "archive", "count", n)
		}
	}
}
