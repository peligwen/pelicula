package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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

func main() {
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

	q, err := NewQueue(db)
	if err != nil {
		slog.Error("queue initialization failed", "component", "main", "error", err)
		os.Exit(1)
	}

	jobs := q.List()
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
	if at := r.URL.Query().Get("action_type"); at != "" {
		writeJSON(w, s.queue.ListByActionType(at))
		return
	}
	writeJSON(w, s.queue.List())
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
	emitEvent(PipelineEvent{
		Type:      EventJobRetried,
		JobID:     id,
		Title:     job.Source.Title,
		Year:      job.Source.Year,
		MediaType: job.Source.Type,
		Details:   map[string]any{"retry_count": job.RetryCount},
		Message:   "Job queued for retry",
	})
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
	emitEvent(PipelineEvent{
		Type:      EventJobCancelled,
		JobID:     id,
		Title:     job.Source.Title,
		Year:      job.Source.Year,
		MediaType: job.Source.Type,
		Message:   "Job cancelled",
	})
	writeJSON(w, job)
}

// handleResubJob re-triggers subtitle acquisition for a job that has already
// been processed. It calls Bazarr to re-search for subtitles using the job's
// *arr IDs. The job itself is not re-enqueued.
func (s *Server) handleResubJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.queue.Get(id)
	if !ok {
		writeError(w, "job not found", http.StatusNotFound)
		return
	}
	bazarrSearchSubtitles(r.Context(), s.configDir, job)
	slog.Info("subtitle re-acquisition triggered", "component", "api", "job_id", id, "arr_type", job.Source.ArrType)
	writeJSON(w, map[string]string{"status": "triggered"})
}

// handleSubSearch triggers Bazarr subtitle search for a library file that is
// not tied to a Procula job. The caller supplies arr_type, arr_id, and
// (for episodes) episode_id directly, typically resolved by the middleware
// querying Radarr/Sonarr by file path.
func (s *Server) handleSubSearch(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		ArrType   string `json:"arr_type"`
		ArrID     int    `json:"arr_id"`
		EpisodeID int    `json:"episode_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ArrType == "" || req.ArrID == 0 {
		writeError(w, "arr_type and arr_id are required", http.StatusBadRequest)
		return
	}
	// Construct a minimal Job to reuse bazarrSearchSubtitles.
	syntheticJob := &Job{
		ID: "manual-resub",
		Source: JobSource{
			ArrType:   req.ArrType,
			ArrID:     req.ArrID,
			EpisodeID: req.EpisodeID,
		},
	}
	bazarrSearchSubtitles(r.Context(), s.configDir, syntheticJob)
	writeJSON(w, map[string]string{"status": "triggered"})
}

func (s *Server) handleNotifications(w http.ResponseWriter, r *http.Request) {
	events := ReadFeed(s.configDir)
	if events == nil {
		events = []NotificationEvent{}
	}
	writeJSON(w, events)
}

func handleStorage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, buildStorageReport())
}

func handleStorageScan(w http.ResponseWriter, r *http.Request) {
	computeFolderSizes()
	writeJSON(w, buildStorageReport())
}

func handleUpdates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, getCachedUpdate())
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, GetSettings(s.db))
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	// Start from current settings so partial payloads (e.g. only storage
	// thresholds, or only pipeline toggles) don't zero out unrelated fields.
	settings := GetSettings(s.db)
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		writeError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Validate notification mode
	switch settings.NotifMode {
	case "internal", "apprise", "direct":
	default:
		settings.NotifMode = "internal"
	}
	switch settings.DualSubTranslator {
	case "argos", "none":
	default:
		settings.DualSubTranslator = "none"
	}
	if len(settings.DualSubPairs) == 0 {
		settings.DualSubPairs = []string{"en-es"}
	}
	// Clamp storage thresholds to [0, 100] and ensure warning < critical.
	if settings.StorageWarningPct < 0 {
		settings.StorageWarningPct = 0
	}
	if settings.StorageCriticalPct > 100 {
		settings.StorageCriticalPct = 100
	}
	if settings.StorageWarningPct >= settings.StorageCriticalPct {
		settings.StorageWarningPct = settings.StorageCriticalPct - 1
		if settings.StorageWarningPct < 0 {
			settings.StorageWarningPct = 0
		}
	}
	if err := SaveSettings(s.db, settings); err != nil {
		writeError(w, "failed to save settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("settings saved", "component", "settings",
		"validation", settings.ValidationEnabled,
		"transcoding", settings.TranscodingEnabled,
		"catalog", settings.CatalogEnabled,
		"notif_mode", settings.NotifMode,
	)
	writeJSON(w, settings)
}

func (s *Server) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := LoadProfiles(s.configDir)
	if err != nil {
		writeError(w, "failed to load profiles: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if profiles == nil {
		profiles = []TranscodeProfile{}
	}
	writeJSON(w, profiles)
}

func (s *Server) handleSaveProfile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var p TranscodeProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if p.Name == "" {
		writeError(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := SaveProfile(s.configDir, p); err != nil {
		writeError(w, "failed to save profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, p)
}

func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := DeleteProfile(s.configDir, name); err != nil {
		writeError(w, "failed to delete profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleManualTranscode creates a transcoding-only job for an existing library file.
// The file must already be under /media/ (not /downloads).
func (s *Server) handleManualTranscode(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req struct {
		Path    string `json:"path"`
		Profile string `json:"profile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Path == "" || req.Profile == "" {
		writeError(w, "path and profile are required", http.StatusBadRequest)
		return
	}

	// Manual transcode is only valid for files already in the library.
	clean := filepath.Clean(req.Path)
	if !isLibraryPath(clean) {
		writeError(w, "path must be under /media/", http.StatusBadRequest)
		return
	}

	// Stat the file to confirm it exists and get its size.
	fi, err := os.Stat(clean)
	if err != nil {
		writeError(w, "file not found or not accessible", http.StatusBadRequest)
		return
	}
	if fi.IsDir() {
		writeError(w, "path must be a file, not a directory", http.StatusBadRequest)
		return
	}

	// Derive a human-readable title from the parent directory (Plex-style naming).
	// Skip the parent if it is a known library root slug (e.g. "movies", "tv", "anime") —
	// in that case the filename itself is the best available title.
	title := strings.TrimSuffix(fi.Name(), filepath.Ext(fi.Name()))
	if parent := filepath.Base(filepath.Dir(clean)); parent != "" {
		isLibraryRoot := false
		for _, lib := range getProculaLibraries() {
			if parent == lib.Slug {
				isLibraryRoot = true
				break
			}
		}
		if !isLibraryRoot {
			title = parent
		}
	}

	source := JobSource{
		Path:    clean,
		Size:    fi.Size(),
		Title:   title,
		ArrType: "radarr", // placeholder; manual jobs aren't tied to an arr instance
		Type:    "movie",
	}

	job, err := s.queue.Create(source)
	if err != nil {
		writeError(w, "failed to create job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := s.queue.Update(job.ID, func(j *Job) {
		j.ManualProfile = req.Profile
	}); err != nil {
		writeError(w, "failed to set profile: "+err.Error(), http.StatusInternalServerError)
		return
	}

	job, _ = s.queue.Get(job.ID)
	slog.Info("manual transcode job created", "component", "api", "job_id", job.ID, "profile", req.Profile, "title", title)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, job)
}

// handleCreateAction creates an action-bus job from an ActionRequest.
// When ?wait=N is set (max 10 seconds) the handler blocks until the job
// reaches a terminal state and returns the result inline.
func (s *Server) handleCreateAction(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req ActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	def := Lookup(req.Action)
	if def == nil {
		writeError(w, "unknown action: "+req.Action, http.StatusBadRequest)
		return
	}

	params := map[string]any{}
	for k, v := range req.Params {
		params[k] = v
	}
	if req.Target.Path != "" {
		params["path"] = req.Target.Path
	}
	if req.Target.ArrType != "" {
		params["arr_type"] = req.Target.ArrType
	}
	if req.Target.ArrID != 0 {
		params["arr_id"] = float64(req.Target.ArrID)
	}
	if req.Target.EpisodeID != 0 {
		params["episode_id"] = float64(req.Target.EpisodeID)
	}

	title := ""
	if req.Target.Path != "" {
		title = filepath.Base(req.Target.Path)
	}
	source := JobSource{
		Path:    req.Target.Path,
		ArrType: req.Target.ArrType,
		ArrID:   req.Target.ArrID,
		Type:    mediaTypeFromPath(req.Target.Path),
		Title:   title,
	}

	job, err := s.queue.createActionJob(source, req.Action, params)
	if err != nil {
		writeError(w, "create job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	waitSecs := 0
	if v := r.URL.Query().Get("wait"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			waitSecs = n
		}
	}
	if waitSecs > 10 {
		waitSecs = 10
	}
	if waitSecs > 0 {
		final, err := s.queue.Wait(job.ID, time.Duration(waitSecs)*time.Second)
		if err != nil && final == nil {
			writeError(w, err.Error(), http.StatusGatewayTimeout)
			return
		}
		res := ActionResult{JobID: final.ID, State: string(final.State), Error: final.Error, Result: final.Result}
		writeJSON(w, res)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, ActionResult{JobID: job.ID, State: string(StateQueued)})
}

// handleListActionRegistry returns all registered actions as JSON.
func (s *Server) handleListActionRegistry(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, List())
}

// handleCatalogFlags returns every row in the catalog_flags table,
// sorted error > warn > info, newest first within each bucket.
func (s *Server) handleCatalogFlags(w http.ResponseWriter, r *http.Request) {
	rows, err := AllFlagged(s.db)
	if err != nil {
		writeError(w, "flags query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []CatalogFlagRow{}
	}
	writeJSON(w, map[string]any{"rows": rows})
}

// handleListBlockedReleases returns all rows in blocked_releases, newest first.
func (s *Server) handleListBlockedReleases(w http.ResponseWriter, r *http.Request) {
	rows, err := ListBlockedReleases(s.db)
	if err != nil {
		writeError(w, "query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []BlockedRelease{}
	}
	writeJSON(w, rows)
}

// handleDeleteBlockedRelease removes a blocked release by id and calls
// middleware to delete the entry from *arr's blocklist.
func (s *Server) handleDeleteBlockedRelease(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	var id int64
	if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil || id == 0 {
		writeError(w, "invalid id", http.StatusBadRequest)
		return
	}

	blocklistID, err := DeleteBlockedRelease(s.db, id)
	if err != nil {
		writeError(w, err.Error(), http.StatusNotFound)
		return
	}

	if blocklistID > 0 {
		// Best-effort: remove from *arr blocklist via middleware.
		req, _ := http.NewRequest(http.MethodDelete,
			fmt.Sprintf("%s/api/pelicula/catalog/blocklist/%d", s.peliculaAPI, blocklistID),
			nil)
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 400 {
				slog.Warn("failed to remove *arr blocklist entry", "component", "replace",
					"blocklist_id", blocklistID)
			}
		} else {
			slog.Warn("failed to remove *arr blocklist entry", "component", "replace",
				"blocklist_id", blocklistID)
		}
	}

	writeJSON(w, map[string]any{"deleted": id})
}

func (s *Server) handleListDualSubProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := ListDualSubProfiles(s.db)
	if err != nil {
		writeError(w, "failed to list profiles: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, profiles)
}

func (s *Server) handleSaveDualSubProfile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var p DualSubProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	// On PUT, the URL segment is the authoritative name.
	if urlName := r.PathValue("name"); urlName != "" {
		p.Name = urlName
	}
	if p.Name == "" {
		writeError(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := SaveDualSubProfile(s.db, p); err != nil {
		if strings.HasPrefix(err.Error(), "cannot ") {
			writeError(w, err.Error(), http.StatusBadRequest)
		} else {
			writeError(w, "failed to save profile: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, p)
}

func (s *Server) handleDeleteDualSubProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := DeleteDualSubProfile(s.db, name); err != nil {
		if strings.HasPrefix(err.Error(), "cannot ") {
			writeError(w, err.Error(), http.StatusBadRequest)
		} else {
			writeError(w, "failed to delete profile: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSubtitleTracks(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, "path is required", http.StatusBadRequest)
		return
	}
	clean := filepath.Clean(path)
	if !isLibraryPath(clean) {
		writeError(w, "path must be under /media/", http.StatusBadRequest)
		return
	}
	tracks := subtitleTracksForPath(clean)
	if tracks == nil {
		tracks = []SubtitleTrack{}
	}
	dualsubs := dualsubSidecarsForPath(clean)
	if dualsubs == nil {
		dualsubs = []DualSubSidecar{}
	}

	var embedded []EmbeddedTrack
	streams, err := probeSubStreams(clean)
	if err == nil {
		embedded = filterTextEmbeddedTracks(streams)
	} else {
		slog.Warn("probe embedded subs failed", "path", clean, "error", err)
	}
	if embedded == nil {
		embedded = []EmbeddedTrack{}
	}

	writeJSON(w, map[string]any{
		"tracks":          tracks,
		"dualsubs":        dualsubs,
		"embedded_tracks": embedded,
	})
}

// handleDeleteDualSubSidecar removes a single dual-sub ASS sidecar file.
// The caller passes the exact sidecar file path (from the dualsubs list returned
// by handleSubtitleTracks). Only paths under /media/ are accepted.
func (s *Server) handleDeleteDualSubSidecar(w http.ResponseWriter, r *http.Request) {
	var body struct {
		File string `json:"file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.File == "" {
		writeError(w, "file is required", http.StatusBadRequest)
		return
	}
	clean := filepath.Clean(body.File)
	if !isLibraryPath(clean) {
		writeError(w, "path must be under /media/", http.StatusBadRequest)
		return
	}
	if !strings.HasSuffix(clean, ".ass") {
		writeError(w, "file must be an .ass file", http.StatusBadRequest)
		return
	}
	if err := os.Remove(clean); err != nil {
		if os.IsNotExist(err) {
			writeError(w, "file not found", http.StatusNotFound)
			return
		}
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("deleted dual-sub sidecar", "component", "dualsub", "file", clean)
	writeJSON(w, map[string]any{"deleted": clean})
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
