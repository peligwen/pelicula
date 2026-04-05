package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
)

var services *ServiceClients

func main() {
	log.SetFlags(log.Ltime)

	services = NewServiceClients("/config")

	// Auto-wire in background so the HTTP server starts immediately
	go func() {
		if err := AutoWire(services); err != nil {
			log.Printf("[autowire] error: %v", err)
		}
	}()

	// Watch for monitored content missing files and auto-search
	go StartMissingWatcher(services, 2*time.Minute)

	mux := http.NewServeMux()

	// Determine auth mode:
	//   PELICULA_AUTH=off (or empty/false) — no auth
	//   PELICULA_AUTH=true or =password    — single shared password (legacy)
	//   PELICULA_AUTH=users                — user model from /config/pelicula/users.json
	authEnv := os.Getenv("PELICULA_AUTH")
	var authMode string
	switch authEnv {
	case "users":
		authMode = "users"
	case "true", "password":
		authMode = "password"
	default:
		authMode = "off"
	}
	auth := NewAuth(authMode, os.Getenv("PELICULA_PASSWORD"), "/config/pelicula/users.json")

	// Auth endpoints (always accessible)
	mux.HandleFunc("/api/pelicula/auth/login", auth.HandleLogin)
	mux.HandleFunc("/api/pelicula/auth/logout", auth.HandleLogout)
	mux.HandleFunc("/api/pelicula/auth/check", auth.HandleCheck)
	// Webhook receiver must be accessible without session auth — *arr services
	// call this endpoint and cannot send a session cookie.
	mux.HandleFunc("/api/pelicula/hooks/import", handleImportHook)
	// Jellyfin refresh is called by Procula internally — no session auth needed.
	mux.HandleFunc("/api/pelicula/jellyfin/refresh", handleJellyfinRefresh)

	// viewer+: read-only dashboard data
	mux.Handle("/api/pelicula/status", auth.Guard(http.HandlerFunc(handleStatus)))
	mux.Handle("/api/pelicula/downloads", auth.Guard(http.HandlerFunc(handleDownloads)))
	mux.Handle("/api/pelicula/downloads/stats", auth.Guard(http.HandlerFunc(handleDownloadStats)))
	mux.Handle("/api/pelicula/processing", auth.Guard(http.HandlerFunc(handleProcessingProxy)))
	mux.Handle("/api/pelicula/notifications", auth.Guard(http.HandlerFunc(handleNotificationsProxy)))

	// manager+: search and add content, pause/resume downloads
	mux.Handle("/api/pelicula/search", auth.GuardManager(http.HandlerFunc(handleSearch)))
	mux.Handle("/api/pelicula/search/add", auth.GuardManager(http.HandlerFunc(handleSearchAdd)))
	mux.Handle("/api/pelicula/downloads/pause", auth.GuardManager(http.HandlerFunc(handleDownloadPause)))

	// admin only: destructive actions
	mux.Handle("/api/pelicula/downloads/cancel", auth.GuardAdmin(http.HandlerFunc(handleDownloadCancel)))

	log.Println("[server] listening on :8181")
	if err := http.ListenAndServe(":8181", mux); err != nil {
		log.Fatal(err)
	}
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Check indexer count from Prowlarr
	services.mu.RLock()
	prowlarrKey := services.ProwlarrKey
	services.mu.RUnlock()

	indexerCount := 0
	if prowlarrKey != "" {
		data, err := services.ArrGet(prowlarrURL, prowlarrKey, "/api/v1/indexer")
		if err == nil {
			var indexers []map[string]any
			if json.Unmarshal(data, &indexers) == nil {
				indexerCount = len(indexers)
			}
		}
	}

	status := map[string]any{
		"status":   "ok",
		"services": services.CheckHealth(),
		"wired":    services.IsWired(),
		"indexers": indexerCount,
	}
	writeJSON(w, status)
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
