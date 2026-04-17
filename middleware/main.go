package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
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
	sseHub         *SSEHub
	ssePoller      *SSEPoller
	catalogDB      *sql.DB
	indexerCount   indexerCountCache
)

// statusCache caches the result of services.CheckHealth() with a 5-second TTL
// so that the 15-second dashboard polling loop doesn't hammer every service.
var statusCache = ttlCache[map[string]string]{ttl: 5 * time.Second}

// indexerCountCache caches the Prowlarr indexer count so handleStatus doesn't
// hit /api/v1/indexer on every 15-second dashboard poll.
type indexerCountCache struct {
	mu        sync.Mutex
	count     *int
	fetchedAt time.Time
}

const indexerCountTTL = 5 * time.Minute

func (c *indexerCountCache) get(prowlarrURL, prowlarrKey string) *int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.count != nil && time.Since(c.fetchedAt) < indexerCountTTL {
		return c.count
	}
	data, err := services.ArrGet(prowlarrURL, prowlarrKey, "/api/v1/indexer")
	if err != nil {
		return c.count // serve stale value (or nil) on error
	}
	var indexers []map[string]any
	if json.Unmarshal(data, &indexers) != nil {
		return c.count
	}
	n := len(indexers)
	c.count = &n
	c.fetchedAt = time.Now()
	return c.count
}

func (c *indexerCountCache) invalidate() {
	c.mu.Lock()
	c.fetchedAt = time.Time{}
	c.mu.Unlock()
}

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
		setupCtx, setupStop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer setupStop()
		serveWithShutdown(setupCtx, ":8181", mux)
		return
	}

	services = NewServiceClients("/config")
	initSearchMode()

	cfg, err := loadLibraries("/config/pelicula")
	if err != nil {
		slog.Warn("library registry", "component", "main", "error", err)
	}
	libraryRegistryMu.Lock()
	libraryRegistry = cfg
	libraryRegistryMu.Unlock()
	slog.Info("library registry loaded", "component", "main", "count", len(cfg.Libraries))
	for _, w := range CheckLibraryAccess() {
		slog.Warn("library access check", "component", "main", "warning", w)
	}

	db, err := OpenDB("/config/pelicula/pelicula.db")
	if err != nil {
		slog.Error("failed to open database", "component", "main", "error", err)
		os.Exit(1)
	}
	catalogDB, err = OpenCatalogDB("/config/pelicula/catalog.db")
	if err != nil {
		slog.Error("failed to open catalog database", "component", "main", "error", err)
		os.Exit(1)
	}
	migrateAllJSON(db, "/config/pelicula")

	jellyfinClient := NewJellyfinHTTPClient(&http.Client{Timeout: 10 * time.Second}, services)

	inviteStore = peligrosa.NewInviteStore(db, jellyfinClient)
	requestStore = peligrosa.NewRequestStore(db, NewArrFulfiller())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sseHub = NewSSEHub()
	ssePoller = NewSSEPoller(sseHub, services)
	go ssePoller.Run(ctx)
	go RunQueuePoller(ctx, catalogDB, services)

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

	// viewer+: SSE stream for real-time dashboard updates
	mux.Handle("/api/pelicula/sse", auth.Guard(http.HandlerFunc(sseHub.HandleSSE)))

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

	// admin only: role management (list + set + delete)
	mux.Handle("/api/pelicula/operators", auth.GuardAdmin(httputil.RequireLocalOriginSoft(http.HandlerFunc(handleOperators))))
	mux.Handle("/api/pelicula/operators/", auth.GuardAdmin(httputil.RequireLocalOriginSoft(http.HandlerFunc(handleOperatorsWithID))))

	// viewer+: active Jellyfin sessions for the now-playing card.
	mux.Handle("/api/pelicula/sessions", auth.Guard(http.HandlerFunc(handleSessions)))

	// GET /api/pelicula/libraries — viewer+ (library metadata should not be public)
	mux.Handle("GET /api/pelicula/libraries", auth.Guard(http.HandlerFunc(handleListLibraries)))
	// POST /api/pelicula/libraries — admin only (add library)
	mux.Handle("POST /api/pelicula/libraries", auth.GuardAdmin(httputil.RequireLocalOriginStrict(http.HandlerFunc(handleAddLibrary))))
	// PUT /api/pelicula/libraries/{slug} — admin only (update library)
	mux.Handle("PUT /api/pelicula/libraries/{slug}", auth.GuardAdmin(httputil.RequireLocalOriginStrict(http.HandlerFunc(handleUpdateLibrary))))
	// DELETE /api/pelicula/libraries/{slug} — admin only (remove custom library)
	mux.Handle("DELETE /api/pelicula/libraries/{slug}", auth.GuardAdmin(httputil.RequireLocalOriginStrict(http.HandlerFunc(handleDeleteLibrary))))

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
	mux.Handle("/api/pelicula/procula/jobs/{id}/retry", auth.GuardAdmin(http.HandlerFunc(handleJobRetry)))

	// viewer+: catalog (live Radarr/Sonarr library view)
	mux.Handle("/api/pelicula/catalog", auth.Guard(http.HandlerFunc(handleCatalogList)))
	mux.Handle("/api/pelicula/catalog/series/{id}", auth.Guard(http.HandlerFunc(handleCatalogSeriesDetail)))
	mux.Handle("/api/pelicula/catalog/series/{id}/season/{n}", auth.Guard(http.HandlerFunc(handleCatalogSeason)))
	mux.Handle("/api/pelicula/catalog/item/history", auth.Guard(http.HandlerFunc(handleCatalogItemHistory)))
	mux.Handle("/api/pelicula/catalog/flags", auth.Guard(http.HandlerFunc(handleCatalogFlags)))
	mux.Handle("/api/pelicula/catalog/detail", auth.Guard(http.HandlerFunc(handleCatalogDetail)))
	// viewer+: pelicula catalog item registry
	mux.Handle("/api/pelicula/catalog/items", auth.Guard(http.HandlerFunc(handleCatalogItems)))
	mux.Handle("/api/pelicula/catalog/items/{id}", auth.Guard(http.HandlerFunc(handleCatalogItemDetail)))
	// admin only: backfill catalog from existing Radarr/Sonarr library
	mux.Handle("/api/pelicula/catalog/backfill", auth.GuardAdmin(http.HandlerFunc(handleCatalogBackfill)))
	mux.Handle("/api/pelicula/catalog/command", auth.GuardAdmin(http.HandlerFunc(handleCatalogCommand)))
	mux.Handle("/api/pelicula/catalog/replace", auth.GuardAdmin(http.HandlerFunc(handleCatalogReplace)))
	mux.Handle("/api/pelicula/catalog/blocklist/{id}", auth.GuardAdmin(http.HandlerFunc(handleCatalogUnblocklist)))
	mux.Handle("/api/pelicula/catalog/qualityprofiles", auth.Guard(http.HandlerFunc(handleCatalogQualityProfiles)))
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
	mux.Handle("/api/pelicula/logs/aggregate", auth.GuardAdmin(http.HandlerFunc(handleLogsAggregate)))

	slog.Info("listening", "component", "main", "addr", ":8181")
	serveWithShutdown(ctx, ":8181", mux)
}

func serveWithShutdown(ctx context.Context, addr string, handler http.Handler) {
	srv := &http.Server{Addr: addr, Handler: handler}

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

	// Check indexer count from Prowlarr (cached; TTL = 5 min)
	services.mu.RLock()
	prowlarrKey := services.ProwlarrKey
	services.mu.RUnlock()

	var idxCount *int
	if prowlarrKey != "" {
		idxCount = indexerCount.get(prowlarrURL, prowlarrKey)
	}

	svcHealth, _ := statusCache.Get(func() (map[string]string, error) {
		return services.CheckHealth(), nil
	})
	status := map[string]any{
		"status":         "ok",
		"services":       svcHealth,
		"wired":          services.IsWired(),
		"indexers":       idxCount,
		"vpn_configured": os.Getenv("WIREGUARD_PRIVATE_KEY") != "",
		"warnings":       CheckLibraryAccess(),
	}
	httputil.WriteJSON(w, status)
}
