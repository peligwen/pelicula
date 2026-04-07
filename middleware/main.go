package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

var services *ServiceClients

// authMiddleware is the package-level Auth instance, used by handlers that
// need to inspect auth state (e.g. handleUsers off-mode guard).
var authMiddleware *Auth

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Setup mode: only serve setup endpoints, skip autowire and all service logic
	if isSetupMode() {
		slog.Info("starting in setup mode", "component", "main")
		mux := http.NewServeMux()
		mux.HandleFunc("/api/pelicula/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"setup"}`))
		})
		mux.HandleFunc("/api/pelicula/setup/detect", handleSetupDetect)
		mux.HandleFunc("/api/pelicula/setup", handleSetupSubmit)
		slog.Info("listening (setup mode)", "component", "main", "addr", ":8181")
		if err := http.ListenAndServe(":8181", mux); err != nil {
			slog.Error("server exited", "component", "main", "error", err)
			os.Exit(1)
		}
		return
	}

	services = NewServiceClients("/config")
	inviteStore = NewInviteStore("/config/pelicula/invites.json")

	// Auto-wire in background so the HTTP server starts immediately
	go func() {
		if err := AutoWire(services); err != nil {
			slog.Error("autowire failed", "component", "main", "error", err)
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
	authMiddleware = NewAuth(authMode, os.Getenv("PELICULA_PASSWORD"), "/config/pelicula/users.json")
	auth := authMiddleware

	// Health check — no auth, called by bash check-vpn and optionally by the dashboard
	mux.HandleFunc("/api/pelicula/health", handleHealth)

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
	mux.Handle("/api/pelicula/storage", auth.Guard(http.HandlerFunc(handleStorageProxy)))
	mux.Handle("/api/pelicula/updates", auth.Guard(http.HandlerFunc(handleUpdatesProxy)))

	// manager+: search and add content, pause/resume downloads
	mux.Handle("/api/pelicula/search", auth.GuardManager(http.HandlerFunc(handleSearch)))
	mux.Handle("/api/pelicula/search/add", auth.GuardManager(http.HandlerFunc(handleSearchAdd)))
	mux.Handle("/api/pelicula/downloads/pause", auth.GuardManager(http.HandlerFunc(handleDownloadPause)))

	// admin only: destructive actions
	mux.Handle("/api/pelicula/downloads/cancel", auth.GuardAdmin(http.HandlerFunc(handleDownloadCancel)))

	// admin only: settings (read and update .env)
	mux.Handle("/api/pelicula/settings", auth.GuardAdmin(http.HandlerFunc(handleSettings)))
	mux.Handle("/api/pelicula/settings/reset", auth.GuardAdmin(http.HandlerFunc(handleSettingsReset)))

	// admin only: backup export / import
	mux.Handle("/api/pelicula/export", auth.GuardAdmin(http.HandlerFunc(handleExport)))
	mux.Handle("/api/pelicula/import-backup", auth.GuardAdmin(http.HandlerFunc(handleImportBackup)))

	// admin only: Jellyfin user management (list + create)
	mux.Handle("/api/pelicula/users", auth.GuardAdmin(http.HandlerFunc(handleUsers)))
	// admin only: per-user operations (delete + password reset)
	mux.Handle("/api/pelicula/users/", auth.GuardAdmin(http.HandlerFunc(handleUsersWithID)))

	// Invites: list+create are admin-only; check+redeem are public (auth checked inside handler).
	mux.Handle("/api/pelicula/invites", auth.GuardAdmin(http.HandlerFunc(handleInvites)))
	mux.HandleFunc("/api/pelicula/invites/", handleInviteOp)
	// read: active Jellyfin sessions for the now-playing card.
	// GuardAdmin is intentionally conservative — the dashboard is admin-only today.
	// Relax to GuardAuthenticated when viewer/manager roles land on the dashboard.
	mux.Handle("/api/pelicula/sessions", auth.GuardAdmin(http.HandlerFunc(handleSessions)))

	// admin only: library import scan + apply + browse
	mux.Handle("/api/pelicula/browse", auth.GuardAdmin(http.HandlerFunc(handleBrowse)))
	mux.Handle("/api/pelicula/library/scan", auth.GuardAdmin(http.HandlerFunc(handleLibraryScan)))
	mux.Handle("/api/pelicula/library/apply", auth.GuardAdmin(http.HandlerFunc(handleLibraryApply)))

	slog.Info("listening", "component", "main", "addr", ":8181")
	if err := http.ListenAndServe(":8181", mux); err != nil {
		slog.Error("server exited", "component", "main", "error", err)
		os.Exit(1)
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

	jsPort := os.Getenv("JELLYSEERR_PORT")
	if jsPort == "" {
		jsPort = "5055"
	}
	status := map[string]any{
		"status":             "ok",
		"services":           services.CheckHealth(),
		"wired":              services.IsWired(),
		"indexers":           indexerCount,
		"jellyseerr_enabled": os.Getenv("JELLYSEERR_ENABLED") == "true",
		"jellyseerr_port":    jsPort,
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

// clientIP extracts the real client IP from X-Real-IP (set by nginx) or falls
// back to the remote address. Used for rate limiting.
func clientIP(r *http.Request) string {
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return ip
	}
	// Strip port from RemoteAddr
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i != -1 {
		return addr[:i]
	}
	return addr
}
