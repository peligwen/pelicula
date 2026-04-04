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

	// Auth middleware wraps all handlers when enabled
	authEnabled := os.Getenv("PELICULA_AUTH") == "true"
	password := os.Getenv("PELICULA_PASSWORD")
	auth := NewAuth(authEnabled, password)

	// Auth endpoints (always accessible)
	mux.HandleFunc("/api/pelicula/auth/login", auth.HandleLogin)
	mux.HandleFunc("/api/pelicula/auth/logout", auth.HandleLogout)
	mux.HandleFunc("/api/pelicula/auth/check", auth.HandleCheck)

	// Protected endpoints
	mux.Handle("/api/pelicula/status", auth.Guard(http.HandlerFunc(handleStatus)))
	mux.Handle("/api/pelicula/search", auth.Guard(http.HandlerFunc(handleSearch)))
	mux.Handle("/api/pelicula/search/add", auth.Guard(http.HandlerFunc(handleSearchAdd)))
	mux.Handle("/api/pelicula/downloads", auth.Guard(http.HandlerFunc(handleDownloads)))
	mux.Handle("/api/pelicula/downloads/stats", auth.Guard(http.HandlerFunc(handleDownloadStats)))
	mux.Handle("/api/pelicula/downloads/pause", auth.Guard(http.HandlerFunc(handleDownloadPause)))
	mux.Handle("/api/pelicula/downloads/cancel", auth.Guard(http.HandlerFunc(handleDownloadCancel)))

	// Procula integration
	mux.Handle("/api/pelicula/hooks/import", auth.Guard(http.HandlerFunc(handleImportHook)))
	mux.Handle("/api/pelicula/processing", auth.Guard(http.HandlerFunc(handleProcessingProxy)))

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
	indexerCount := 0
	if services.ProwlarrKey != "" {
		data, err := services.ArrGet(prowlarrURL, services.ProwlarrKey, "/api/v1/indexer")
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
