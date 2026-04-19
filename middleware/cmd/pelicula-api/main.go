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
	"pelicula-api/internal/app/actions"
	"pelicula-api/internal/app/autowire"
	"pelicula-api/internal/app/backup"
	"pelicula-api/internal/app/catalog"
	"pelicula-api/internal/app/downloads"
	"pelicula-api/internal/app/health"
	"pelicula-api/internal/app/hooks"
	jfapp "pelicula-api/internal/app/jellyfin"
	"pelicula-api/internal/app/library"
	"pelicula-api/internal/app/missingwatcher"
	"pelicula-api/internal/app/router"
	"pelicula-api/internal/app/search"
	appservices "pelicula-api/internal/app/services"
	"pelicula-api/internal/app/settings"
	appsetup "pelicula-api/internal/app/setup"
	"pelicula-api/internal/app/sse"
	"pelicula-api/internal/app/sysinfo"
	"pelicula-api/internal/app/vpnwatchdog"
	"pelicula-api/internal/clients/apprise"
	"pelicula-api/internal/clients/docker"
	jfclient "pelicula-api/internal/clients/jellyfin"
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
	if appsetup.NeedsSetup() {
		slog.Info("starting in setup mode", "component", "main")
		setupHandler := appsetup.New(envPath, generateAPIKey, generateReadablePassword)
		mux := http.NewServeMux()
		mux.HandleFunc("/api/pelicula/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"setup"}`)) //nolint:errcheck
		})
		mux.HandleFunc("/api/pelicula/setup/detect", setupHandler.HandleDetect)
		mux.Handle("/api/pelicula/setup", httputil.RequireLocalOriginStrict(http.HandlerFunc(setupHandler.HandleSubmit)))
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

	tmdbKey := ""
	searchMode := ""
	if vars, err := parseEnvFile(envPath); err == nil {
		tmdbKey = vars["TMDB_API_KEY"]
		searchMode = vars["SEARCH_MODE"]
	}

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

	jfWirer := jfapp.NewWirer(svc, jellyfinURL, envPath, generateAPIKey, parseEnvFile, writeEnvFile, &envMu)

	jellyfinClient := jfapp.NewJellyfinHTTPClient(
		&http.Client{Timeout: 10 * time.Second},
		jfWirer.Auth,
		jfWirer.CreateUser,
		jellyfinURL,
	)

	jfHandler := jfapp.NewHandler(
		jfclient.NewWithHTTPClient(jellyfinURL, svc.HTTPClient()),
		jfWirer.Auth,
		jfapp.ServiceUser,
	)

	searchHandler := search.New(svc, sonarrURL, radarrURL, prowlarrURL, libHandler, tmdbKey, searchMode)

	invites := peligrosa.NewInviteStore(db, jellyfinClient)
	requests := peligrosa.NewRequestStore(reporeqs.New(db), search.NewArrFulfiller(searchHandler))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	hub := sse.NewHub()
	poller := sse.NewPoller(hub, svc, urls.Procula, dockerCli.Logs)
	go poller.Run(ctx)
	go catalog.RunQueuePoller(ctx, cdb, svc, urls.Radarr, urls.Sonarr)

	// app is declared here so the autowire closure below can capture it.
	// The closure is only called after network I/O completes in the background
	// goroutine, well after app is assigned below.
	var app *App

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
		WireJellyfin:           func() { jfWirer.Wire(libHandler) },
		InvalidateIndexerCache: func() { app.idxCache.invalidate() },
	})

	// Auto-wire in background so the HTTP server starts immediately.
	go func() {
		if err := autowirer.Run(ctx); err != nil {
			slog.Error("autowire failed", "component", "main", "error", err)
		}
	}()

	go missingwatcher.New(svc, sonarrURL, radarrURL).Run(2 * time.Minute)

	var watchdog *vpnwatchdog.Watchdog
	if cfg.WireguardPrivateKey != "" {
		watchdog = vpnwatchdog.New(svc, dockerCli, gluetunClient)
		go watchdog.Run()
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
	app = &App{
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
			Services: svc,
			GetWatchdog: func() health.WatchdogState {
				if watchdog == nil {
					return watchdogStateAdapter(vpnwatchdog.State{})
				}
				return watchdogStateAdapter(watchdog.State())
			},
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
			TriggerJellyfinRefresh: func() error { return jfWirer.TriggerRefresh() },
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

	settingsHandler := settings.New(envPath, generateAPIKey)

	actionsHandler := actions.New(services.HTTPClient(), proculaURL, strings.TrimSpace(os.Getenv("PROCULA_API_KEY")))

	mux := http.NewServeMux()

	router.Register(mux, router.Config{
		Auth:          auth,
		Deps:          deps,
		Health:        app.healthHandler,
		SSE:           app.sseHub,
		Sysinfo:       app.sysinfoHandler,
		Downloads:     app.dlHandler,
		Hooks:         app.hooksHandler,
		Backup:        app.backupHandler,
		JF:            app.jfHandler,
		Library:       app.libHandler,
		Catalog:       app.catalogHandler,
		Search:        searchHandler,
		Settings:      settingsHandler,
		Actions:       actionsHandler,
		Docker:        dockerCli,
		StatusHandler: http.HandlerFunc(app.handleStatus),
		JobsHandler:   http.HandlerFunc(handleJobsList),
	})

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

// watchdogStateAdapter converts vpnwatchdog.State to the health package's WatchdogState.
func watchdogStateAdapter(ws vpnwatchdog.State) health.WatchdogState {
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
