package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

var (
	queue     *Queue
	configDir string
)

func main() {
	log.SetFlags(log.Ltime)

	configDir = env("CONFIG_DIR", "/config")
	peliculaAPI := env("PELICULA_API_URL", "http://pelicula-api:8181")

	var err error
	queue, err = NewQueue(configDir)
	if err != nil {
		log.Fatalf("[queue] failed to initialize: %v", err)
	}
	log.Printf("[queue] loaded %d jobs from disk", len(queue.jobs))

	// Single worker processes jobs sequentially
	go RunWorker(queue, configDir, peliculaAPI)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /ping", handlePing)
	mux.HandleFunc("GET /api/procula/status", handleStatus)
	mux.HandleFunc("GET /api/procula/jobs", handleListJobs)
	mux.HandleFunc("POST /api/procula/jobs", handleCreateJob)
	mux.HandleFunc("GET /api/procula/jobs/{id}", handleGetJob)
	mux.HandleFunc("POST /api/procula/jobs/{id}/retry", handleRetryJob)
	mux.HandleFunc("POST /api/procula/jobs/{id}/cancel", handleCancelJob)
	mux.HandleFunc("GET /api/procula/storage", handleStorage)
	mux.HandleFunc("GET /api/procula/notifications", handleNotifications)
	mux.HandleFunc("GET /api/procula/settings", handleGetSettings)
	mux.HandleFunc("POST /api/procula/settings", handleSaveSettings)
	mux.HandleFunc("GET /", handleUI)
	mux.HandleFunc("GET /static/procula.css", handleUICSS)

	log.Println("[server] listening on :8282")
	if err := http.ListenAndServe(":8282", mux); err != nil {
		log.Fatal(err)
	}
}

func handlePing(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"status": "ok",
		"queue":  queue.Status(),
	})
}

func handleListJobs(w http.ResponseWriter, r *http.Request) {
	jobs := queue.List()
	writeJSON(w, jobs)
}

func handleCreateJob(w http.ResponseWriter, r *http.Request) {
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
	if !isAllowedPath(source.Path) {
		writeError(w, "path not under an allowed media directory", http.StatusBadRequest)
		return
	}

	job, err := queue.Create(source)
	if err != nil {
		writeError(w, "failed to create job: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("[api] created job %s for %s: %s", job.ID, job.Source.ArrType, job.Source.Title)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, job)
}

func handleGetJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := queue.Get(id)
	if !ok {
		writeError(w, "job not found", http.StatusNotFound)
		return
	}
	writeJSON(w, job)
}

func handleRetryJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := queue.Retry(id); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	job, _ := queue.Get(id)
	log.Printf("[api] retrying job %s (attempt %d)", id, job.RetryCount)
	writeJSON(w, job)
}

func handleCancelJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := queue.Cancel(id); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	job, _ := queue.Get(id)
	log.Printf("[api] cancelled job %s", id)
	writeJSON(w, job)
}

func handleStorage(w http.ResponseWriter, r *http.Request) {
	// Phase 2 — disk monitoring not yet implemented
	writeJSON(w, map[string]string{"status": "not_implemented"})
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
	if err := SaveSettings(s); err != nil {
		writeError(w, "failed to save settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	log.Printf("[settings] saved: validation=%v transcoding=%v catalog=%v notif=%s",
		s.ValidationEnabled, s.TranscodingEnabled, s.CatalogEnabled, s.NotifMode)
	writeJSON(w, s)
}

func handleNotifications(w http.ResponseWriter, r *http.Request) {
	feedPath := filepath.Join(configDir, "procula", "notifications_feed.json")
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
