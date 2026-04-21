// Package bootstrap constructs the pelicula-api App from a resolved Config.
// All resource allocation (database opens, client construction, handler wiring)
// happens here. The App is ready to serve once New returns without error.
package bootstrap

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"pelicula-api/internal/app/actions"
	"pelicula-api/internal/app/adminops"
	pelapp "pelicula-api/internal/app/app"
	"pelicula-api/internal/app/autowire"
	"pelicula-api/internal/app/backup"
	"pelicula-api/internal/app/catalog"
	"pelicula-api/internal/app/downloads"
	"pelicula-api/internal/app/health"
	"pelicula-api/internal/app/hooks"
	jfapp "pelicula-api/internal/app/jellyfin"
	"pelicula-api/internal/app/library"
	"pelicula-api/internal/app/network"
	"pelicula-api/internal/app/search"
	appservices "pelicula-api/internal/app/services"
	"pelicula-api/internal/app/settings"
	"pelicula-api/internal/app/sse"
	"pelicula-api/internal/app/sysinfo"
	"pelicula-api/internal/app/vpnwatchdog"
	"pelicula-api/internal/clients/apprise"
	"pelicula-api/internal/clients/docker"
	gluetunclient "pelicula-api/internal/clients/gluetun"
	jfclient "pelicula-api/internal/clients/jellyfin"
	proculaclient "pelicula-api/internal/clients/procula"
	"pelicula-api/internal/config"
	"pelicula-api/internal/cryptogen"
	"pelicula-api/internal/peligrosa"
	"pelicula-api/internal/repo/migratejson"
	"pelicula-api/internal/repo/peliculadb"
	reporeqs "pelicula-api/internal/repo/requests"

	_ "modernc.org/sqlite"
)

// envPath is the canonical path to the .env file inside the pelicula container.
const envPath = "/project/.env"

// New constructs the full App from cfg. genPassword is injected by cmd/ because
// the passphrase wordlist lives in cmd/pelicula-api/wordlist.go. Returns an
// error if any required resource (DB, etc.) cannot be opened.
func New(cfg *config.Config, genPassword func() string) (*pelapp.App, error) {
	urls := cfg.URLs

	// Read jellyfinKey from env file if not already in cfg.
	jellyfinKey := cfg.JellyfinAPIKey
	if jellyfinKey == "" {
		if vars, err := settings.ParseEnvFile(envPath); err == nil {
			jellyfinKey = vars["JELLYFIN_API_KEY"]
		}
	}

	// Read TMDB key and search mode from env file.
	tmdbKey := ""
	searchMode := ""
	if vars, err := settings.ParseEnvFile(envPath); err == nil {
		tmdbKey = vars["TMDB_API_KEY"]
		searchMode = vars["SEARCH_MODE"]
	}

	svc := appservices.New(cfg, jellyfinKey)

	dockerCli := docker.New(cfg.DockerHost, cfg.ProjectName)
	appriseCli := apprise.New(cfg.URLs.Apprise, cfg.ConfigDir)

	libCfg, err := library.LoadLibraries("/config/pelicula")
	if err != nil {
		slog.Warn("library registry", "component", "bootstrap", "error", err)
	}

	var envMu sync.Mutex
	jfWirer := jfapp.NewWirer(
		svc,
		urls.Jellyfin,
		envPath,
		cryptogen.GenerateAPIKey,
		func(path string) (map[string]string, error) { return settings.ParseEnvFile(path) },
		func(path string, vars map[string]string) error { return settings.WriteEnvFile(path, vars) },
		&envMu,
	)

	jellyfinClient := jfapp.NewJellyfinHTTPClient(
		&http.Client{Timeout: 10 * time.Second},
		jfWirer.Auth,
		jfWirer.CreateUser,
		urls.Jellyfin,
	)

	jfHandler := jfapp.NewHandler(
		jfclient.NewWithHTTPClient(urls.Jellyfin, svc.HTTPClient()),
		jfWirer.Auth,
		jfapp.ServiceUser,
	)

	procClient := proculaclient.New(
		urls.Procula,
		strings.TrimSpace(cfg.ProculaAPIKey),
	)

	libHandler := &library.Handler{
		Svc:       svc,
		Procula:   procClient,
		RadarrURL: urls.Radarr,
		SonarrURL: urls.Sonarr,
		ConfigDir: "/config/pelicula",
		ForwardToProc: func(source library.ProculaJobSource) error {
			_, err := procClient.CreateJob(context.Background(), source)
			return err
		},
	}
	libHandler.SetRegistry(libCfg)
	slog.Info("library registry loaded", "component", "bootstrap", "count", len(libCfg.Libraries))
	for _, w := range libHandler.CheckLibraryAccess() {
		slog.Warn("library access check", "component", "bootstrap", "warning", w)
	}

	db, err := peliculadb.Open("/config/pelicula/pelicula.db")
	if err != nil {
		return nil, err
	}
	cdb, err := catalog.OpenCatalogDB("/config/pelicula/catalog.db")
	if err != nil {
		db.Close()
		return nil, err
	}
	migratejson.Run(db, "/config/pelicula")

	searchHandler := search.New(svc, urls.Sonarr, urls.Radarr, urls.Prowlarr, libHandler, tmdbKey, searchMode)

	invites := peligrosa.NewInviteStore(db, jellyfinClient)
	requests := peligrosa.NewRequestStore(reporeqs.New(db), search.NewArrFulfiller(searchHandler))

	// Construct the shared catalog cache once; both CatalogHandler and
	// missingwatcher draw from it to avoid redundant full-library fetches.
	catalogCacheSvc := svc // capture for closures below
	arrCatalogCache := catalog.NewCatalogCache(
		func(ctx context.Context) ([]byte, error) {
			_, radarrKey, _ := catalogCacheSvc.Keys()
			return catalogCacheSvc.ArrGet(urls.Radarr, radarrKey, "/api/v3/movie")
		},
		func(ctx context.Context) ([]byte, error) {
			sonarrKey, _, _ := catalogCacheSvc.Keys()
			return catalogCacheSvc.ArrGet(urls.Sonarr, sonarrKey, "/api/v3/series")
		},
	)

	hub := sse.NewHub()

	peligrosa.SetOpenRegistration(cfg.OpenRegistration)

	auth := peligrosa.NewAuth(peligrosa.AuthConfig{
		DB:       db,
		Jellyfin: jellyfinClient,
	})

	deps := peligrosa.NewDeps(db, auth, invites, requests, jellyfinClient)
	deps.Notify = func(title, body string) { appriseCli.Notify(title, body) }
	deps.GenPassword = genPassword

	gluetunCli := gluetunclient.New(cfg.URLs.Gluetun, cfg.GluetunHTTPUser, cfg.GluetunHTTPPass)

	// Construct App before the autowirer so the InvalidateIndexerCache callback
	// can capture it. Autowirer and Watchdog are assigned below.
	a := &pelapp.App{
		Svc:           svc,
		URLs:          urls,
		SSEHub:        hub,
		SSEPoller:     sse.NewPoller(hub, svc, urls.Procula, dockerCli.Logs),
		CatalogDB:     cdb,
		MainDB:        db,
		Auth:          auth,
		Deps:          deps,
		Invites:       invites,
		Requests:      requests,
		StatusTTL:     pelapp.NewStatusTTLCache(5 * time.Second),
		VPNConfigured: cfg.WireguardPrivateKey != "",
		IdxCache:      pelapp.IndexerCountCache{ProwlarrURL: urls.Prowlarr},
		SysinfoHandler: &sysinfo.Handler{
			Svc:          svc,
			RadarrURL:    urls.Radarr,
			SonarrURL:    urls.Sonarr,
			DockerClient: dockerCli,
		},
		BackupHandler: backup.New(svc, libHandler, auth, invites, requests, urls.Radarr, urls.Sonarr),
		DLHandler: &downloads.Handler{
			Svc:       svc,
			SonarrURL: urls.Sonarr,
			RadarrURL: urls.Radarr,
		},
		HooksHandler: &hooks.Handler{
			Procula:                procClient,
			HTTPClient:             &http.Client{Timeout: 10 * time.Second},
			ProculaURL:             urls.Procula,
			ProculaAPIKey:          cfg.ProculaAPIKey,
			SonarrURL:              urls.Sonarr,
			RadarrURL:              urls.Radarr,
			GetKeys:                func() (string, string, string) { return svc.Keys() },
			ArrGet:                 svc.ArrGet,
			CatalogDB:              cdb,
			RequestStore:           requests,
			Qbt:                    svc.Qbt,
			TriggerJellyfinRefresh: func() error { return jfWirer.TriggerRefresh() },
			Notify:                 func(t, b string) error { appriseCli.Notify(t, b); return nil },
		},
		LibHandler:      libHandler,
		JFHandler:       jfHandler,
		ArrCatalogCache: arrCatalogCache,
		CatalogHandler: &catalog.Handler{
			DB:         cdb,
			Arr:        svc,
			Jf:         svc,
			Client:     &http.Client{Timeout: 10 * time.Second},
			ProculaURL: urls.Procula,
			RadarrURL:  urls.Radarr,
			SonarrURL:  urls.Sonarr,
			Cache:      arrCatalogCache,
		},
		SearchHandler:   searchHandler,
		SettingsHandler: settings.New(envPath, cryptogen.GenerateAPIKey),
		ActionsHandler:  actions.New(svc.HTTPClient(), urls.Procula, strings.TrimSpace(cfg.ProculaAPIKey)),
		AdminHandler: adminops.New(dockerCli, func(r *http.Request) (string, bool) {
			username, _, ok := auth.SessionFor(r)
			return username, ok
		}),
		NetworkHandler: &network.Handler{
			Docker:        dockerCli,
			VPNContainers: network.DefaultVPNContainers,
		},
	}

	// HealthHandler closure captures a.Watchdog which is set below — the
	// closure runs on HTTP requests, well after construction completes.
	a.HealthHandler = &health.Handler{
		Services: svc,
		GetWatchdog: func() health.WatchdogState {
			if a.Watchdog == nil {
				return pelapp.WatchdogStateAdapter(vpnwatchdog.State{})
			}
			return pelapp.WatchdogStateAdapter(a.Watchdog.State())
		},
		GluetunBaseURL: urls.Gluetun,
	}

	if cfg.WireguardPrivateKey != "" {
		a.Watchdog = vpnwatchdog.New(svc, dockerCli, gluetunCli)
	}

	// Autowirer InvalidateIndexerCache captures a.IdxCache by pointer, which
	// is valid for the lifetime of the App.
	autowirer, autowireState := autowire.NewAutowirer(autowire.Config{
		Svc: svc,
		URLs: autowire.URLs{
			Sonarr:      urls.Sonarr,
			Radarr:      urls.Radarr,
			Prowlarr:    urls.Prowlarr,
			Bazarr:      urls.Bazarr,
			Jellyfin:    urls.Jellyfin,
			QBT:         urls.QBT,
			PeliculaAPI: urls.PeliculaAPI,
		},
		VPNConfigured: cfg.WireguardPrivateKey != "",
		WebhookSecret: strings.TrimSpace(os.Getenv("WEBHOOK_SECRET")),
		SubLangs:      cfg.SubLangs,
		AudioLang:     cfg.AudioLang,
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
		InvalidateIndexerCache: func() { a.IdxCache.Invalidate() },
	})

	a.Autowirer = autowirer
	a.AutowireState = autowireState

	// Wire the shared StatusTTLCache into the SSE poller so that fetchServices
	// reuses the same cached CheckHealth result as the status HTTP endpoint.
	a.SSEPoller.SetStatusCache(&a.StatusTTL)

	return a, nil
}
