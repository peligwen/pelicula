// cmd/pelicula-api is the entry point for the pelicula-api middleware service.
// It wires together all internal packages and starts the HTTP server.
//
// This file is wiring only: build App, register routes, listen.
// Business logic lives in the sibling handler files.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"pelicula-api/httputil"
	"pelicula-api/internal/app/adminops"
	"pelicula-api/internal/app/autowire"
	"pelicula-api/internal/app/backup"
	"pelicula-api/internal/app/catalog"
	"pelicula-api/internal/app/downloads"
	"pelicula-api/internal/app/health"
	"pelicula-api/internal/app/hooks"
	jfapp "pelicula-api/internal/app/jellyfin"
	"pelicula-api/internal/app/library"
	appservices "pelicula-api/internal/app/services"
	"pelicula-api/internal/app/sse"
	"pelicula-api/internal/app/sysinfo"
	"pelicula-api/internal/clients/apprise"
	"pelicula-api/internal/clients/docker"
	"pelicula-api/internal/config"
	"pelicula-api/internal/peligrosa"
	"pelicula-api/internal/repo/migratejson"
	reporeqs "pelicula-api/internal/repo/requests"

	_ "modernc.org/sqlite"
)

// App holds all wired-up application state. No package-level globals.
type App struct {
	svc            *appservices.Clients
	urls           config.URLs
	sseHub         *sse.Hub
	ssePoller      *sse.Poller
	catalogDB      *sql.DB
	mainDB         *sql.DB
	auth           *peligrosa.Auth
	invites        *peligrosa.InviteStore
	requests       *peligrosa.RequestStore
	idxCache       indexerCountCacheApp
	statusTTL      statusTTLCache
	backupHandler  *backup.Handler
	dlHandler      *downloads.Handler
	healthHandler  *health.Handler
	sysinfoHandler *sysinfo.Handler
	hooksHandler   *hooks.Handler
	libHandler     *library.Handler
	catalogHandler *catalog.Handler
	jfHandler      *jfapp.Handler
	vpnConfigured  bool
	autowireState  *autowire.AutowireState
}

// indexerCountCacheApp caches the Prowlarr indexer count.
type indexerCountCacheApp struct {
	mu        sync.Mutex
	count     *int
	fetchedAt time.Time
}

const indexerCountTTL = 5 * time.Minute

func (c *indexerCountCacheApp) get(svc *appservices.Clients) *int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.count != nil && time.Since(c.fetchedAt) < indexerCountTTL {
		return c.count
	}
	_, _, prowlarrKey := svc.Keys()
	if prowlarrKey == "" {
		return c.count
	}
	data, err := svc.ArrGet(prowlarrURL, prowlarrKey, "/api/v1/indexer")
	if err != nil {
		return c.count
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

func (c *indexerCountCacheApp) invalidate() {
	c.mu.Lock()
	c.fetchedAt = time.Time{}
	c.mu.Unlock()
}

// statusTTLCache is a simple single-value cache with a TTL for the status endpoint.
type statusTTLCache struct {
	mu        sync.Mutex
	value     map[string]string
	fetchedAt time.Time
	ttl       time.Duration
}

func (c *statusTTLCache) Get(fetch func() (map[string]string, error)) (map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.value != nil && time.Since(c.fetchedAt) < c.ttl {
		return c.value, nil
	}
	v, err := fetch()
	if err == nil {
		c.value = v
		c.fetchedAt = time.Now()
	}
	return v, err
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg := config.Load()
	urls := cfg.URLs

	dockerCli = docker.New(cfg.DockerHost, cfg.ProjectName)
	appriseCli = apprise.New(cfg.URLs.Apprise, cfg.ConfigDir)

	// Setup mode: only serve setup endpoints
	if isSetupMode() {
		slog.Info("starting in setup mode", "component", "main")
		mux := http.NewServeMux()
		mux.HandleFunc("/api/pelicula/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"setup"}`)) //nolint:errcheck
		})
		mux.HandleFunc("/api/pelicula/setup/detect", handleSetupDetect)
		mux.Handle("/api/pelicula/setup", httputil.RequireLocalOriginStrict(http.HandlerFunc(handleSetupSubmit)))
		slog.Info("listening (setup mode)", "component", "main", "addr", ":8181")
		setupCtx, setupStop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer setupStop()
		serveWithShutdown(setupCtx, ":8181", mux)
		return
	}

	jellyfinKey := cfg.JellyfinAPIKey
	if jellyfinKey == "" {
		if vars, err := parseEnvFile(envPath); err == nil {
			jellyfinKey = vars["JELLYFIN_API_KEY"]
		}
	}
	svc := appservices.New(cfg, jellyfinKey)
	initSearchMode()

	libCfg, err := library.LoadLibraries("/config/pelicula")
	if err != nil {
		slog.Warn("library registry", "component", "main", "error", err)
	}
	libHandler = &library.Handler{
		Svc:       svc,
		Procula:   procClient,
		RadarrURL: radarrURL,
		SonarrURL: sonarrURL,
		ConfigDir: "/config/pelicula",
		ForwardToProc: func(source library.ProculaJobSource) error {
			return forwardToProcula(context.Background(), ProculaJobSource{
				Type:    source.Type,
				Title:   source.Title,
				Year:    source.Year,
				Path:    source.Path,
				ArrType: source.ArrType,
			})
		},
	}
	libHandler.SetRegistry(libCfg)
	slog.Info("library registry loaded", "component", "main", "count", len(libCfg.Libraries))
	for _, w := range libHandler.CheckLibraryAccess() {
		slog.Warn("library access check", "component", "main", "warning", w)
	}

	db, err := OpenDB("/config/pelicula/pelicula.db")
	if err != nil {
		slog.Error("failed to open database", "component", "main", "error", err)
		os.Exit(1)
	}
	cdb, err := catalog.OpenCatalogDB("/config/pelicula/catalog.db")
	if err != nil {
		slog.Error("failed to open catalog database", "component", "main", "error", err)
		os.Exit(1)
	}
	migratejson.Run(db, "/config/pelicula")

	jellyfinClient := NewJellyfinHTTPClient(&http.Client{Timeout: 10 * time.Second}, svc)

	jfHandler := jfapp.NewHandler(
		jfClient(svc),
		func() (string, error) { return jellyfinAuth(svc) },
		jellyfinServiceUser,
	)

	invites := peligrosa.NewInviteStore(db, jellyfinClient)
	requests := peligrosa.NewRequestStore(reporeqs.New(db), NewArrFulfiller())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	hub := sse.NewHub()
	poller := sse.NewPoller(hub, svc, urls.Procula, dockerCli.Logs)
	go poller.Run(ctx)
	go catalog.RunQueuePoller(ctx, cdb, svc, urls.Radarr, urls.Sonarr)

	// Build the Autowirer. wireJellyfin is a callback so Jellyfin-specific
	// logic (envPath, envMu, wizard, API key persistence) stays in cmd/ until
	// a future phase extracts it.
	autowirer, autowireState := autowire.NewAutowirer(autowire.Config{
		Svc: svc,
		URLs: autowire.URLs{
			Sonarr:      urls.Sonarr,
			Radarr:      urls.Radarr,
			Prowlarr:    urls.Prowlarr,
			Bazarr:      envOr("BAZARR_URL", "http://bazarr:6767/bazarr"),
			Jellyfin:    envOr("JELLYFIN_URL", "http://jellyfin:8096/jellyfin"),
			QBT:         envOr("QBITTORRENT_URL", "http://gluetun:8080"),
			PeliculaAPI: envOr("PELICULA_API_URL", "http://pelicula-api:8181"),
		},
		VPNConfigured: cfg.WireguardPrivateKey != "",
		WebhookSecret: strings.TrimSpace(os.Getenv("WEBHOOK_SECRET")),
		SubLangs:      os.Getenv("PELICULA_SUB_LANGS"),
		AudioLang:     os.Getenv("PELICULA_AUDIO_LANG"),
		GetLibraries: func() []autowire.Library {
			libs := libHandler.GetLibraries()
			out := make([]autowire.Library, 0, len(libs))
			for _, l := range libs {
				out = append(out, autowire.Library{
					Name:          l.Name,
					ContainerPath: l.ContainerPath(),
					Arr:           l.Arr,
				})
			}
			return out
		},
		WireJellyfin:           func() { wireJellyfin(svc, libHandler) },
		InvalidateIndexerCache: func() { indexerCount.invalidate() },
	})

	// Auto-wire in background so the HTTP server starts immediately.
	go func() {
		if err := autowirer.Run(ctx); err != nil {
			slog.Error("autowire failed", "component", "main", "error", err)
		}
	}()

	go StartMissingWatcher(svc, 2*time.Minute)

	if cfg.WireguardPrivateKey != "" {
		go StartVPNWatchdog(svc)
	}

	peligrosa.SetOpenRegistration(cfg.OpenRegistration)

	auth := peligrosa.NewAuth(peligrosa.AuthConfig{
		DB:       db,
		Jellyfin: jellyfinClient,
	})

	peliculaNotify := func(title, body string) {
		appriseCli.Notify(title, body)
	}
	deps := peligrosa.NewDeps(db, auth, invites, requests, jellyfinClient)
	deps.Notify = peliculaNotify
	deps.GenPassword = generateReadablePassword

	// Build App struct — all new-style handler state lives here.
	app := &App{
		svc:           svc,
		urls:          urls,
		sseHub:        hub,
		ssePoller:     poller,
		catalogDB:     cdb,
		mainDB:        db,
		auth:          auth,
		invites:       invites,
		requests:      requests,
		statusTTL:     statusTTLCache{ttl: 5 * time.Second},
		vpnConfigured: cfg.WireguardPrivateKey != "",
		autowireState: autowireState,
		sysinfoHandler: &sysinfo.Handler{
			Svc:          svc,
			RadarrURL:    urls.Radarr,
			SonarrURL:    urls.Sonarr,
			DockerClient: dockerCli,
		},
		backupHandler: backup.New(svc, libHandler, auth, invites, requests, urls.Radarr, urls.Sonarr),
		dlHandler: &downloads.Handler{
			Svc:       svc,
			SonarrURL: urls.Sonarr,
			RadarrURL: urls.Radarr,
		},
		healthHandler: &health.Handler{
			Services:       svc,
			GetWatchdog:    func() health.WatchdogState { return watchdogStateAdapter(GetWatchdogState()) },
			GluetunBaseURL: urls.Gluetun,
		},
		hooksHandler: &hooks.Handler{
			Procula:                procClient,
			HTTPClient:             &http.Client{Timeout: 10 * time.Second},
			ProculaURL:             proculaURL,
			ProculaAPIKey:          cfg.ProculaAPIKey,
			SonarrURL:              sonarrURL,
			RadarrURL:              radarrURL,
			GetKeys:                func() (string, string, string) { return svc.Keys() },
			ArrGet:                 svc.ArrGet,
			CatalogDB:              cdb,
			RequestStore:           requests,
			Qbt:                    svc.Qbt,
			TriggerJellyfinRefresh: func() error { return TriggerLibraryRefresh(svc) },
			Notify:                 func(t, b string) error { appriseCli.Notify(t, b); return nil },
		},
		libHandler: libHandler,
		jfHandler:  jfHandler,
		catalogHandler: &catalog.Handler{
			DB:         cdb,
			Arr:        svc,
			Jf:         svc,
			Client:     &http.Client{Timeout: 10 * time.Second},
			ProculaURL: proculaURL,
			RadarrURL:  urls.Radarr,
			SonarrURL:  urls.Sonarr,
		},
	}
	// Wire package-level globals for the handler files that still use them.
	services = svc
	authMiddleware = auth
	inviteStore = invites
	requestStore = requests
	mainDB = db

	mux := http.NewServeMux()

	// Health check — no auth, called by bash check-vpn
	mux.Handle("/api/pelicula/health", app.healthHandler)

	// Peligrosa routes: auth, invites, requests, open registration
	peligrosa.RegisterRoutes(mux, deps)

	// Webhook receiver — no session auth needed (*arr services call this)
	mux.HandleFunc("/api/pelicula/hooks/import", app.hooksHandler.HandleImportHook)
	// Jellyfin refresh — called by Procula internally
	mux.HandleFunc("/api/pelicula/jellyfin/refresh", app.hooksHandler.HandleJellyfinRefresh)

	// viewer+: SSE stream
	mux.Handle("/api/pelicula/sse", auth.Guard(http.HandlerFunc(app.sseHub.HandleSSE)))

	// viewer+: read-only dashboard data
	mux.Handle("/api/pelicula/host", auth.Guard(http.HandlerFunc(app.sysinfoHandler.ServeHost)))
	mux.Handle("/api/pelicula/status", auth.Guard(http.HandlerFunc(app.handleStatus)))
	mux.Handle("/api/pelicula/downloads", auth.Guard(http.HandlerFunc(app.dlHandler.HandleDownloads)))
	mux.Handle("/api/pelicula/downloads/stats", auth.Guard(http.HandlerFunc(app.dlHandler.HandleDownloadStats)))
	mux.Handle("/api/pelicula/processing", auth.Guard(http.HandlerFunc(app.hooksHandler.HandleProcessingProxy)))
	mux.Handle("/api/pelicula/notifications", auth.Guard(http.HandlerFunc(app.hooksHandler.HandleNotificationsProxy)))
	mux.Handle("/api/pelicula/storage", auth.Guard(http.HandlerFunc(app.hooksHandler.HandleStorageProxy)))
	mux.Handle("/api/pelicula/procula-settings", auth.GuardAdmin(http.HandlerFunc(app.hooksHandler.HandleProculaSettingsProxy)))
	mux.Handle("/api/pelicula/storage/scan", auth.GuardAdmin(http.HandlerFunc(app.hooksHandler.HandleStorageScanProxy)))
	mux.Handle("/api/pelicula/updates", auth.Guard(http.HandlerFunc(app.hooksHandler.HandleUpdatesProxy)))

	// admin only: *arr metadata for settings dropdowns
	mux.Handle("/api/pelicula/arr-meta", auth.GuardAdmin(http.HandlerFunc(handleArrMeta)))

	// manager+: search and add content, pause/resume downloads
	mux.Handle("/api/pelicula/search", auth.GuardManager(http.HandlerFunc(handleSearch)))
	mux.Handle("/api/pelicula/search/add", auth.GuardManager(http.HandlerFunc(handleSearchAdd)))
	mux.Handle("/api/pelicula/downloads/pause", auth.GuardManager(http.HandlerFunc(app.dlHandler.HandleDownloadPause)))

	// admin only: destructive actions
	mux.Handle("/api/pelicula/downloads/cancel", auth.GuardAdmin(http.HandlerFunc(app.dlHandler.HandleDownloadCancel)))

	// admin only: settings
	mux.Handle("/api/pelicula/settings", auth.GuardAdmin(httputil.RequireLocalOriginStrict(http.HandlerFunc(handleSettings))))
	mux.Handle("/api/pelicula/settings/reset", auth.GuardAdmin(httputil.RequireLocalOriginStrict(http.HandlerFunc(handleSettingsReset))))

	// admin only: backup export / import
	mux.Handle("/api/pelicula/export", auth.GuardAdmin(http.HandlerFunc(app.backupHandler.HandleExport)))
	mux.Handle("/api/pelicula/import-backup", auth.GuardAdmin(http.HandlerFunc(app.backupHandler.HandleImportBackup)))

	// admin only: Jellyfin user management
	mux.Handle("/api/pelicula/users", auth.GuardAdmin(httputil.RequireLocalOriginSoft(http.HandlerFunc(app.jfHandler.HandleUsers))))
	mux.Handle("/api/pelicula/users/", auth.GuardAdmin(httputil.RequireLocalOriginSoft(http.HandlerFunc(app.jfHandler.HandleUsersWithID))))

	// admin only: role management
	mux.Handle("/api/pelicula/operators", auth.GuardAdmin(httputil.RequireLocalOriginSoft(http.HandlerFunc(handleOperators))))
	mux.Handle("/api/pelicula/operators/", auth.GuardAdmin(httputil.RequireLocalOriginSoft(http.HandlerFunc(handleOperatorsWithID))))

	// viewer+: active Jellyfin sessions
	mux.Handle("/api/pelicula/sessions", auth.Guard(http.HandlerFunc(app.jfHandler.HandleSessions)))

	// viewer+: library metadata
	mux.Handle("GET /api/pelicula/libraries", auth.Guard(http.HandlerFunc(app.libHandler.HandleListLibraries)))
	// admin only: library CRUD
	mux.Handle("POST /api/pelicula/libraries", auth.GuardAdmin(httputil.RequireLocalOriginStrict(http.HandlerFunc(app.libHandler.HandleAddLibrary))))
	mux.Handle("PUT /api/pelicula/libraries/{slug}", auth.GuardAdmin(httputil.RequireLocalOriginStrict(http.HandlerFunc(app.libHandler.HandleUpdateLibrary))))
	mux.Handle("DELETE /api/pelicula/libraries/{slug}", auth.GuardAdmin(httputil.RequireLocalOriginStrict(http.HandlerFunc(app.libHandler.HandleDeleteLibrary))))

	// admin only: library import scan + apply + browse
	mux.Handle("/api/pelicula/browse", auth.GuardAdmin(http.HandlerFunc(app.libHandler.HandleBrowse)))
	mux.Handle("/api/pelicula/library/scan", auth.GuardAdmin(http.HandlerFunc(app.libHandler.HandleLibraryScan)))
	mux.Handle("/api/pelicula/library/apply", auth.GuardAdmin(http.HandlerFunc(app.libHandler.HandleLibraryApply)))

	// admin only: transcoding
	mux.Handle("/api/pelicula/transcode/profiles", auth.GuardAdmin(http.HandlerFunc(app.libHandler.HandleTranscodeProfiles)))
	mux.Handle("/api/pelicula/transcode/profiles/{name}", auth.GuardAdmin(http.HandlerFunc(app.libHandler.HandleDeleteTranscodeProfile)))
	mux.Handle("/api/pelicula/library/retranscode", auth.GuardAdmin(http.HandlerFunc(app.libHandler.HandleLibraryRetranscode)))

	// admin only: subtitle re-acquisition
	mux.Handle("/api/pelicula/library/resub", auth.GuardAdmin(http.HandlerFunc(app.libHandler.HandleLibraryResub)))
	mux.Handle("/api/pelicula/procula/jobs/{id}/resub", auth.GuardAdmin(http.HandlerFunc(app.libHandler.HandleJobResub)))
	mux.Handle("/api/pelicula/procula/jobs/{id}/retry", auth.GuardAdmin(http.HandlerFunc(app.libHandler.HandleJobRetry)))

	// viewer+: catalog
	mux.Handle("/api/pelicula/catalog", auth.Guard(http.HandlerFunc(app.catalogHandler.HandleCatalogList)))
	mux.Handle("/api/pelicula/catalog/series/{id}", auth.Guard(http.HandlerFunc(app.catalogHandler.HandleCatalogSeriesDetail)))
	mux.Handle("/api/pelicula/catalog/series/{id}/season/{n}", auth.Guard(http.HandlerFunc(app.catalogHandler.HandleCatalogSeason)))
	mux.Handle("/api/pelicula/catalog/item/history", auth.Guard(http.HandlerFunc(app.catalogHandler.HandleCatalogItemHistory)))
	mux.Handle("/api/pelicula/catalog/flags", auth.Guard(http.HandlerFunc(app.catalogHandler.HandleCatalogFlags)))
	mux.Handle("/api/pelicula/catalog/detail", auth.Guard(http.HandlerFunc(app.catalogHandler.HandleCatalogDetail)))
	mux.Handle("/api/pelicula/catalog/items", auth.Guard(http.HandlerFunc(app.catalogHandler.HandleCatalogItems)))
	mux.Handle("/api/pelicula/catalog/items/{id}", auth.Guard(http.HandlerFunc(app.catalogHandler.HandleCatalogItemDetail)))
	mux.Handle("/api/pelicula/catalog/backfill", auth.GuardAdmin(http.HandlerFunc(app.catalogHandler.HandleCatalogBackfill)))
	mux.Handle("/api/pelicula/catalog/command", auth.GuardAdmin(http.HandlerFunc(app.catalogHandler.HandleCatalogCommand)))
	mux.Handle("/api/pelicula/catalog/replace", auth.GuardAdmin(http.HandlerFunc(app.catalogHandler.HandleCatalogReplace)))
	mux.Handle("/api/pelicula/catalog/blocklist/{id}", auth.GuardAdmin(http.HandlerFunc(app.catalogHandler.HandleCatalogUnblocklist)))
	mux.Handle("/api/pelicula/catalog/qualityprofiles", auth.Guard(http.HandlerFunc(app.catalogHandler.HandleCatalogQualityProfiles)))
	mux.Handle("/api/pelicula/jobs", auth.Guard(http.HandlerFunc(handleJobsList)))

	// admin only: action bus
	mux.Handle("/api/pelicula/actions", auth.GuardAdmin(http.HandlerFunc(handleActionsCreate)))
	mux.Handle("/api/pelicula/actions/registry", auth.Guard(http.HandlerFunc(handleActionsRegistry)))

	// admin only: VPN speed test
	mux.Handle("/api/pelicula/speedtest", auth.GuardAdmin(http.HandlerFunc(app.sysinfoHandler.ServeSpeedtest)))

	// admin only: container control
	adminHandler := adminops.New(dockerCli, func(r *http.Request) (string, bool) {
		if authMiddleware == nil {
			return "", false
		}
		username, _, ok := authMiddleware.SessionFor(r)
		return username, ok
	})
	mux.Handle("/api/pelicula/admin/stack/restart", auth.GuardAdmin(http.HandlerFunc(adminHandler.HandleStackRestart)))
	mux.Handle("/api/pelicula/admin/vpn/restart", auth.GuardAdmin(http.HandlerFunc(adminHandler.HandleVPNRestart)))
	mux.Handle("/api/pelicula/admin/logs", auth.GuardAdmin(http.HandlerFunc(adminHandler.HandleServiceLogs)))
	mux.Handle("/api/pelicula/logs/aggregate", auth.GuardAdmin(http.HandlerFunc(app.sysinfoHandler.ServeLogs)))

	slog.Info("listening", "component", "main", "addr", ":8181")
	serveWithShutdown(ctx, ":8181", mux)
}

// handleStatus is a method on App so it can access app.svc, app.idxCache, app.statusTTL
// without package-level globals.
func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	_, _, prowlarrKey := a.svc.Keys()
	var idxCount *int
	if prowlarrKey != "" {
		idxCount = a.idxCache.get(a.svc)
	}

	svcHealth, _ := a.statusTTL.Get(func() (map[string]string, error) {
		return a.svc.CheckHealth(), nil
	})
	status := map[string]any{
		"status":         "ok",
		"services":       svcHealth,
		"wired":          a.svc.IsWired(),
		"indexers":       idxCount,
		"vpn_configured": a.vpnConfigured,
		"warnings":       a.libHandler.CheckLibraryAccess(),
	}
	httputil.WriteJSON(w, status)
}

// watchdogStateAdapter converts VPNWatchdogState to the health package's WatchdogState.
func watchdogStateAdapter(ws VPNWatchdogState) health.WatchdogState {
	return health.WatchdogState{
		PortForwardStatus: ws.PortForwardStatus,
		ForwardedPort:     ws.ForwardedPort,
		LastSyncedAt:      ws.LastSyncedAt,
		RestartAttempts:   ws.RestartAttempts,
		ConsecutiveZero:   ws.ConsecutiveZero,
		GraceRemaining:    ws.GraceRemaining,
		CooldownRemaining: ws.CooldownRemaining,
		LastTransitionAt:  ws.LastTransitionAt,
		VPNTunnelStatus:   ws.VPNTunnelStatus,
	}
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
