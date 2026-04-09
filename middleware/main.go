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
		// Peligrosa: requireLocalOriginStrict — setup should only accept POSTs from a LAN browser.
		mux.Handle("/api/pelicula/setup", requireLocalOriginStrict(http.HandlerFunc(handleSetupSubmit)))
		slog.Info("listening (setup mode)", "component", "main", "addr", ":8181")
		if err := http.ListenAndServe(":8181", mux); err != nil {
			slog.Error("server exited", "component", "main", "error", err)
			os.Exit(1)
		}
		return
	}

	services = NewServiceClients("/config")
	inviteStore = NewInviteStore("/config/pelicula/invites.json")
	dismissedStore = NewDismissedStore("/config/pelicula/dismissed.json")
	requestStore = NewRequestStore("/config/pelicula/requests.json")

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
	//   PELICULA_AUTH=jellyfin             — credentials verified against Jellyfin
	authEnv := os.Getenv("PELICULA_AUTH")
	var authMode string
	switch authEnv {
	case "users":
		authMode = "users"
	case "jellyfin":
		authMode = "jellyfin"
	case "true", "password":
		authMode = "password"
	default:
		authMode = "off"
	}
	peliculaPassword := os.Getenv("PELICULA_PASSWORD")
	if authMode == "password" && peliculaPassword == "" {
		slog.Error("PELICULA_AUTH=password requires PELICULA_PASSWORD to be set — run ./pelicula setup to configure authentication")
		os.Exit(1)
	}
	authMiddleware = NewAuth(AuthConfig{
		Mode:      authMode,
		Password:  peliculaPassword,
		UsersFile: "/config/pelicula/users.json",
		RolesFile: "/config/pelicula/roles.json",
	})
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

	// viewer+: pipeline board (unified downloads + processing view)
	mux.Handle("/api/pelicula/pipeline", auth.Guard(http.HandlerFunc(handlePipelineGet)))
	// admin only: dismiss a failed job from the needs-attention lane
	mux.Handle("/api/pelicula/pipeline/dismiss", auth.GuardAdmin(http.HandlerFunc(handlePipelineDismiss)))

	// viewer+: read-only dashboard data
	mux.Handle("/api/pelicula/host", auth.Guard(http.HandlerFunc(handleHost)))
	mux.Handle("/api/pelicula/status", auth.Guard(http.HandlerFunc(handleStatus)))
	mux.Handle("/api/pelicula/downloads", auth.Guard(http.HandlerFunc(handleDownloads)))
	mux.Handle("/api/pelicula/downloads/stats", auth.Guard(http.HandlerFunc(handleDownloadStats)))
	mux.Handle("/api/pelicula/processing", auth.Guard(http.HandlerFunc(handleProcessingProxy)))
	mux.Handle("/api/pelicula/notifications", auth.Guard(http.HandlerFunc(handleNotificationsProxy)))
	mux.Handle("/api/pelicula/storage", auth.Guard(http.HandlerFunc(handleStorageProxy)))
	mux.Handle("/api/pelicula/procula-settings", auth.GuardAdmin(handleProculaSettingsProxy))
	mux.Handle("/api/pelicula/storage/scan", auth.GuardAdmin(http.HandlerFunc(handleStorageScanProxy)))
	mux.Handle("/api/pelicula/updates", auth.Guard(http.HandlerFunc(handleUpdatesProxy)))
	mux.Handle("/api/pelicula/events", auth.Guard(http.HandlerFunc(handleEventsProxy)))

	// viewer+: request queue (list own requests + create)
	mux.Handle("/api/pelicula/requests", auth.Guard(http.HandlerFunc(handleRequests)))
	// admin only: per-request approve/deny/delete and *arr metadata for settings dropdowns
	mux.Handle("/api/pelicula/requests/", auth.GuardAdmin(http.HandlerFunc(handleRequestOp)))
	mux.Handle("/api/pelicula/arr-meta", auth.GuardAdmin(http.HandlerFunc(handleArrMeta)))

	// manager+: search and add content, pause/resume downloads
	mux.Handle("/api/pelicula/search", auth.GuardManager(http.HandlerFunc(handleSearch)))
	mux.Handle("/api/pelicula/search/add", auth.GuardManager(http.HandlerFunc(handleSearchAdd)))
	mux.Handle("/api/pelicula/downloads/pause", auth.GuardManager(http.HandlerFunc(handleDownloadPause)))

	// admin only: destructive actions
	mux.Handle("/api/pelicula/downloads/cancel", auth.GuardAdmin(http.HandlerFunc(handleDownloadCancel)))

	// admin only: settings (read and update .env)
	// Peligrosa: requireLocalOriginStrict guards the POST paths against cross-origin mutations.
	mux.Handle("/api/pelicula/settings", auth.GuardAdmin(requireLocalOriginStrict(http.HandlerFunc(handleSettings))))
	mux.Handle("/api/pelicula/settings/reset", auth.GuardAdmin(requireLocalOriginStrict(http.HandlerFunc(handleSettingsReset))))

	// admin only: backup export / import
	mux.Handle("/api/pelicula/export", auth.GuardAdmin(http.HandlerFunc(handleExport)))
	mux.Handle("/api/pelicula/import-backup", auth.GuardAdmin(http.HandlerFunc(handleImportBackup)))

	// admin only: Jellyfin user management (list + create)
	// Peligrosa: requireLocalOriginSoft allows API callers, blocks browser cross-origin.
	mux.Handle("/api/pelicula/users", auth.GuardAdmin(requireLocalOriginSoft(http.HandlerFunc(handleUsers))))
	// admin only: per-user operations (delete + password reset)
	mux.Handle("/api/pelicula/users/", auth.GuardAdmin(requireLocalOriginSoft(http.HandlerFunc(handleUsersWithID))))

	// Invites: list+create are admin-only; check+redeem are public (auth checked inside handler).
	// Peligrosa: requireLocalOriginSoft on both routes — redeem is public but invite-gated.
	mux.Handle("/api/pelicula/invites", auth.GuardAdmin(requireLocalOriginSoft(http.HandlerFunc(handleInvites))))
	mux.HandleFunc("/api/pelicula/invites/", requireLocalOriginSoft(http.HandlerFunc(handleInviteOp)).ServeHTTP)
	// read: active Jellyfin sessions for the now-playing card.
	// GuardAdmin is intentionally conservative — the dashboard is admin-only today.
	// Relax to GuardAuthenticated when viewer/manager roles land on the dashboard.
	mux.Handle("/api/pelicula/sessions", auth.GuardAdmin(http.HandlerFunc(handleSessions)))

	// admin only: library import scan + apply + browse
	mux.Handle("/api/pelicula/browse", auth.GuardAdmin(http.HandlerFunc(handleBrowse)))
	mux.Handle("/api/pelicula/library/scan", auth.GuardAdmin(http.HandlerFunc(handleLibraryScan)))
	mux.Handle("/api/pelicula/library/apply", auth.GuardAdmin(http.HandlerFunc(handleLibraryApply)))

	// admin only: manual transcoding — list profiles, enqueue transcode jobs
	mux.Handle("/api/pelicula/transcode/profiles", auth.GuardAdmin(http.HandlerFunc(handleTranscodeProfiles)))
	mux.Handle("/api/pelicula/library/retranscode", auth.GuardAdmin(http.HandlerFunc(handleLibraryRetranscode)))

	// admin only: container control via docker-socket-proxy sidecar
	mux.Handle("/api/pelicula/admin/stack/restart", auth.GuardAdmin(http.HandlerFunc(handleStackRestart)))
	mux.Handle("/api/pelicula/admin/logs", auth.GuardAdmin(http.HandlerFunc(handleServiceLogs)))

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
