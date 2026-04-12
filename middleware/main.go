package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pelicula-api/httputil"
	"pelicula-api/peligrosa"
)

var (
	services       *ServiceClients
	authMiddleware *peligrosa.Auth
	inviteStore    *peligrosa.InviteStore
	requestStore   *peligrosa.RequestStore
	dismissedStore *DismissedStore
)

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
		// Peligrosa: httputil.RequireLocalOriginStrict — setup should only accept POSTs from a LAN browser.
		mux.Handle("/api/pelicula/setup", httputil.RequireLocalOriginStrict(http.HandlerFunc(handleSetupSubmit)))
		slog.Info("listening (setup mode)", "component", "main", "addr", ":8181")
		serveWithShutdown(":8181", mux)
		return
	}

	services = NewServiceClients("/config")

	db, err := OpenDB("/config/pelicula/pelicula.db")
	if err != nil {
		slog.Error("failed to open database", "component", "main", "error", err)
		os.Exit(1)
	}
	migrateAllJSON(db, "/config/pelicula")

	jellyfinClient := NewJellyfinHTTPClient(&http.Client{Timeout: 10 * time.Second}, services)

	inviteStore = peligrosa.NewInviteStore(db, jellyfinClient)
	dismissedStore = NewDismissedStore(db)
	requestStore = peligrosa.NewRequestStore(db, NewArrFulfiller())

	// Auto-wire in background so the HTTP server starts immediately
	go func() {
		if err := AutoWire(services); err != nil {
			slog.Error("autowire failed", "component", "main", "error", err)
		}
	}()

	// Watch for monitored content missing files and auto-search
	go StartMissingWatcher(services, 2*time.Minute)

	// Monitor VPN port forwarding; keep qBittorrent listen port in sync.
	// Only active when a WireGuard key is present (VPN profile enabled).
	if os.Getenv("WIREGUARD_PRIVATE_KEY") != "" {
		go StartVPNWatchdog(services)
	}

	mux := http.NewServeMux()

	peligrosa.SetOpenRegistration(os.Getenv("PELICULA_OPEN_REGISTRATION") == "true")

	authMiddleware = peligrosa.NewAuth(peligrosa.AuthConfig{
		DB:       db,
		Jellyfin: jellyfinClient,
	})
	auth := authMiddleware
	deps := peligrosa.NewDeps(db, authMiddleware, inviteStore, requestStore, jellyfinClient)
	deps.Notify = notifyApprise
	deps.GenPassword = generateReadablePassword

	// Health check — no auth, called by bash check-vpn and optionally by the dashboard
	mux.HandleFunc("/api/pelicula/health", handleHealth)

	// Peligrosa routes: auth, invites, requests, open registration.
	// Webhook routes stay below (handlers live in hooks.go).
	peligrosa.RegisterRoutes(mux, deps)

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

	// admin only: *arr metadata for settings dropdowns
	mux.Handle("/api/pelicula/arr-meta", auth.GuardAdmin(http.HandlerFunc(handleArrMeta)))

	// manager+: search and add content, pause/resume downloads
	mux.Handle("/api/pelicula/search", auth.GuardManager(http.HandlerFunc(handleSearch)))
	mux.Handle("/api/pelicula/search/add", auth.GuardManager(http.HandlerFunc(handleSearchAdd)))
	mux.Handle("/api/pelicula/downloads/pause", auth.GuardManager(http.HandlerFunc(handleDownloadPause)))

	// admin only: destructive actions
	mux.Handle("/api/pelicula/downloads/cancel", auth.GuardAdmin(http.HandlerFunc(handleDownloadCancel)))

	// admin only: settings (read and update .env)
	// Peligrosa: httputil.RequireLocalOriginStrict guards the POST paths against cross-origin mutations.
	mux.Handle("/api/pelicula/settings", auth.GuardAdmin(httputil.RequireLocalOriginStrict(http.HandlerFunc(handleSettings))))
	mux.Handle("/api/pelicula/settings/reset", auth.GuardAdmin(httputil.RequireLocalOriginStrict(http.HandlerFunc(handleSettingsReset))))

	// admin only: backup export / import
	mux.Handle("/api/pelicula/export", auth.GuardAdmin(http.HandlerFunc(handleExport)))
	mux.Handle("/api/pelicula/import-backup", auth.GuardAdmin(http.HandlerFunc(handleImportBackup)))

	// admin only: Jellyfin user management (list + create)
	// Peligrosa: httputil.RequireLocalOriginSoft allows API callers, blocks browser cross-origin.
	mux.Handle("/api/pelicula/users", auth.GuardAdmin(httputil.RequireLocalOriginSoft(http.HandlerFunc(handleUsers))))
	// admin only: per-user operations (delete + password reset)
	mux.Handle("/api/pelicula/users/", auth.GuardAdmin(httputil.RequireLocalOriginSoft(http.HandlerFunc(handleUsersWithID))))

	// read: active Jellyfin sessions for the now-playing card.
	// GuardAdmin is intentionally conservative — the dashboard is admin-only today.
	// Relax to GuardAuthenticated when viewer/manager roles land on the dashboard.
	mux.Handle("/api/pelicula/sessions", auth.GuardAdmin(http.HandlerFunc(handleSessions)))

	// admin only: library import scan + apply + browse
	mux.Handle("/api/pelicula/browse", auth.GuardAdmin(http.HandlerFunc(handleBrowse)))
	mux.Handle("/api/pelicula/library/scan", auth.GuardAdmin(http.HandlerFunc(handleLibraryScan)))
	mux.Handle("/api/pelicula/library/apply", auth.GuardAdmin(http.HandlerFunc(handleLibraryApply)))

	// admin only: manual transcoding — list/create/update profiles, delete profile, enqueue transcode jobs
	mux.Handle("/api/pelicula/transcode/profiles", auth.GuardAdmin(http.HandlerFunc(handleTranscodeProfiles)))
	mux.Handle("/api/pelicula/transcode/profiles/{name}", auth.GuardAdmin(http.HandlerFunc(handleDeleteTranscodeProfile)))
	mux.Handle("/api/pelicula/library/retranscode", auth.GuardAdmin(http.HandlerFunc(handleLibraryRetranscode)))

	// admin only: subtitle re-acquisition
	mux.Handle("/api/pelicula/library/resub", auth.GuardAdmin(http.HandlerFunc(handleLibraryResub)))
	mux.Handle("/api/pelicula/procula/jobs/{id}/resub", auth.GuardAdmin(http.HandlerFunc(handleJobResub)))

	// viewer+: catalog (live Radarr/Sonarr library view)
	mux.Handle("/api/pelicula/catalog", auth.Guard(http.HandlerFunc(handleCatalogList)))
	mux.Handle("/api/pelicula/catalog/series/{id}", auth.Guard(http.HandlerFunc(handleCatalogSeriesDetail)))
	mux.Handle("/api/pelicula/catalog/series/{id}/season/{n}", auth.Guard(http.HandlerFunc(handleCatalogSeason)))
	mux.Handle("/api/pelicula/catalog/item/history", auth.Guard(http.HandlerFunc(handleCatalogItemHistory)))
	mux.Handle("/api/pelicula/catalog/flags", auth.Guard(http.HandlerFunc(handleCatalogFlags)))
	mux.Handle("/api/pelicula/catalog/detail", auth.Guard(http.HandlerFunc(handleCatalogDetail)))
	mux.Handle("/api/pelicula/jobs", auth.Guard(http.HandlerFunc(handleJobsList)))

	// admin only: action bus (mutating) — proxy to procula
	mux.Handle("/api/pelicula/actions", auth.GuardAdmin(http.HandlerFunc(handleActionsCreate)))
	mux.Handle("/api/pelicula/actions/registry", auth.Guard(http.HandlerFunc(handleActionsRegistry)))

	// admin only: VPN speed test
	mux.Handle("/api/pelicula/speedtest", auth.GuardAdmin(http.HandlerFunc(handleSpeedTest)))

	// admin only: container control via docker-socket-proxy sidecar
	mux.Handle("/api/pelicula/admin/stack/restart", auth.GuardAdmin(http.HandlerFunc(handleStackRestart)))
	mux.Handle("/api/pelicula/admin/vpn/restart", auth.GuardAdmin(http.HandlerFunc(handleVPNRestart)))
	mux.Handle("/api/pelicula/admin/logs", auth.GuardAdmin(http.HandlerFunc(handleServiceLogs)))

	slog.Info("listening", "component", "main", "addr", ":8181")
	serveWithShutdown(":8181", mux)
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
		"status":         "ok",
		"services":       services.CheckHealth(),
		"wired":          services.IsWired(),
		"indexers":       indexerCount,
		"vpn_configured": os.Getenv("WIREGUARD_PRIVATE_KEY") != "",
	}
	httputil.WriteJSON(w, status)
}
