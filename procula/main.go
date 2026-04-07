package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// configDir is the global config directory used by settings.go.
// Set once at startup from CONFIG_DIR env var.
var configDir string

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
	queue     *Queue
	configDir string
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	configDir = env("CONFIG_DIR", "/config")
	peliculaAPI := env("PELICULA_API_URL", "http://pelicula-api:8181")
	proculaAPIKey = env("PROCULA_API_KEY", "")

	q, err := NewQueue(configDir)
	if err != nil {
		slog.Error("queue initialization failed", "component", "main", "error", err)
		os.Exit(1)
	}
	slog.Info("queue loaded", "component", "queue", "job_count", len(q.jobs))

	// Single worker processes jobs sequentially
	go RunWorker(q, configDir, peliculaAPI)
	go RunStorageMonitor(configDir)
	go RunUpdateChecker(configDir)

	srv := &Server{queue: q, configDir: configDir}

	mux := http.NewServeMux()

	mux.HandleFunc("GET /ping", srv.handlePing)
	mux.HandleFunc("GET /api/procula/status", srv.handleStatus)
	mux.HandleFunc("GET /api/procula/jobs", srv.handleListJobs)
	mux.HandleFunc("POST /api/procula/jobs", requireAPIKey(srv.handleCreateJob))
	mux.HandleFunc("GET /api/procula/jobs/{id}", srv.handleGetJob)
	mux.HandleFunc("POST /api/procula/jobs/{id}/retry", requireAPIKey(srv.handleRetryJob))
	mux.HandleFunc("POST /api/procula/jobs/{id}/cancel", requireAPIKey(srv.handleCancelJob))
	mux.HandleFunc("GET /api/procula/storage", handleStorage)
	mux.HandleFunc("GET /api/procula/updates", handleUpdates)
	mux.HandleFunc("GET /api/procula/notifications", srv.handleNotifications)
	mux.HandleFunc("GET /api/procula/settings", handleGetSettings)
	mux.HandleFunc("POST /api/procula/settings", requireAPIKey(handleSaveSettings))
	mux.HandleFunc("GET /", handleUI)
	mux.HandleFunc("GET /static/procula.css", handleUICSS)

	slog.Info("listening", "component", "main", "addr", ":8282")
	if err := http.ListenAndServe(":8282", mux); err != nil {
		slog.Error("server exited", "component", "main", "error", err)
		os.Exit(1)
	}
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"status": "ok",
		"queue":  s.queue.Status(),
	})
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	jobs := s.queue.List()
	writeJSON(w, jobs)
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB
	var source JobSource
	if err := json.NewDecoder(r.Body).Decode(&source); err != nil {
		writeError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}

	if source.Path == "" || source.ArrType == "" {
		writeError(w, "path and arr_type are required", http.StatusBadRequest)
		return
	}
	if !isAllowedJobPath(source.Path) {
		writeError(w, "path not under an allowed media directory", http.StatusBadRequest)
		return
	}

	job, err := s.queue.Create(source)
	if err != nil {
		writeError(w, "failed to create job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	slog.Info("job created", "component", "api", "job_id", job.ID, "arr_type", job.Source.ArrType, "title", job.Source.Title)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, job)
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.queue.Get(id)
	if !ok {
		writeError(w, "job not found", http.StatusNotFound)
		return
	}
	writeJSON(w, job)
}

func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.queue.Retry(id); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	job, _ := s.queue.Get(id)
	slog.Info("job retry", "component", "api", "job_id", id, "attempt", job.RetryCount)
	writeJSON(w, job)
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.queue.Cancel(id); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	job, _ := s.queue.Get(id)
	slog.Info("job cancelled", "component", "api", "job_id", id)
	writeJSON(w, job)
}

func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	feedPath := filepath.Join(s.configDir, "procula", "notifications_feed.json")
	data, err := os.ReadFile(feedPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, []NotificationEvent{})
			return
		}
		writeError(w, "failed to read notifications", http.StatusInternalServerError)
		return
	}
	var events []NotificationEvent
	if err := json.Unmarshal(data, &events); err != nil {
		writeJSON(w, []NotificationEvent{})
		return
	}
	writeJSON(w, events)
}

func handleStorage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, buildStorageReport())
}

func handleUpdates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, getCachedUpdate())
}

func handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, GetSettings())
}

func handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var s PipelineSettings
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		writeError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Validate notification mode
	switch s.NotifMode {
	case "internal", "apprise", "direct":
	default:
		s.NotifMode = "internal"
	}
	// Clamp storage thresholds to [0, 100] and ensure warning < critical.
	if s.StorageWarningPct < 0 {
		s.StorageWarningPct = 0
	}
	if s.StorageCriticalPct > 100 {
		s.StorageCriticalPct = 100
	}
	if s.StorageWarningPct >= s.StorageCriticalPct {
		s.StorageWarningPct = s.StorageCriticalPct - 1
		if s.StorageWarningPct < 0 {
			s.StorageWarningPct = 0
		}
	}
	if err := SaveSettings(s); err != nil {
		writeError(w, "failed to save settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("settings saved", "component", "settings", "validation", s.ValidationEnabled, "transcoding", s.TranscodingEnabled, "catalog", s.CatalogEnabled, "notif_mode", s.NotifMode)
	writeJSON(w, s)
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
