// Command pelicula-server is the one long-running Pelicula process: it serves
// the dashboard API, runs the import pipeline worker, wires the *arr stack
// and Jellyfin on startup, and keeps qBittorrent's listen port in step with
// the VPN's forwarded port. The CLI (cmd/pelicula) owns .env, compose and
// config seeding; this binary only reads its environment.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"pelicula/internal/api"
	"pelicula/internal/auth"
	"pelicula/internal/autowire"
	"pelicula/internal/clients/arr"
	"pelicula/internal/clients/gluetun"
	"pelicula/internal/clients/jellyfin"
	"pelicula/internal/clients/qbt"
	"pelicula/internal/config"
	"pelicula/internal/pipeline"
	"pelicula/internal/portsync"
	"pelicula/internal/store"
)

// version is stamped by the build (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)
	if err := run(log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg := config.FromEnv(version)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if n, err := st.ResetRunningJobs(ctx); err != nil {
		log.Warn("reset running jobs", "error", err)
	} else if n > 0 {
		log.Info("requeued jobs interrupted by the last shutdown", "count", n)
	}

	// Clients. API keys for the *arr apps are filled in by WaitForAPIKeys
	// once the apps have written their config.xml.
	sonarr := arr.New(cfg.SonarrURL, "", "v3")
	radarr := arr.New(cfg.RadarrURL, "", "v3")
	jf := jellyfin.New(cfg.JellyfinURL)
	jfAdmin := jellyfin.NewAdmin(jf, cfg.JellyfinAdminUser, cfg.JellyfinAdminPassword)
	var (
		prowlarr *arr.Client
		qb       *qbt.Client
		gl       *gluetun.Client
	)
	if cfg.VPNEnabled {
		prowlarr = arr.New(cfg.ProwlarrURL, "", "v1")
		qb = qbt.New(cfg.QBTURL)
		gl = gluetun.New(cfg.GluetunURL, cfg.GluetunUser, cfg.GluetunPass)
	}

	// A missing ffprobe would fail every import and, with auto-blocklist on,
	// delete good files. Refuse to blocklist in that state.
	if _, err := exec.LookPath("ffprobe"); err != nil {
		log.Error("ffprobe not found; validation will fail every import, disabling auto-blocklist until it is installed")
		if err := st.SetSetting(ctx, store.SettingAutoBlocklist, "false"); err != nil {
			log.Warn("disable auto-blocklist", "error", err)
		}
	}

	authn := auth.New(auth.Deps{
		Store:    st,
		Jellyfin: identity(jf),
		Admin:    jfAdmin,
		Users:    jf,
		Log:      log,
	})

	worker := pipeline.New(pipeline.Deps{
		Store:    st,
		Sonarr:   sonarr,
		Radarr:   radarr,
		Jellyfin: jf,
		JFAdmin:  jfAdmin,
		FFprobe:  "ffprobe",
		Log:      log,
	})

	var wired atomic.Bool
	srv := &api.Server{
		Cfg:      cfg,
		Store:    st,
		Auth:     authn,
		Session:  auth.SessionFrom,
		Sonarr:   sonarr,
		Radarr:   radarr,
		Jellyfin: jfUsers{jf},
		JFAdmin:  jfAdmin,
		Wired:    wired.Load,
		Kick:     worker.Kick,
		Log:      log,
	}
	if cfg.VPNEnabled {
		// Only assign when non-nil: a typed nil inside an interface is not nil.
		srv.Prowlarr = prowlarr
		srv.QBT = qbtAdapter{qb}
		srv.Gluetun = gluetunAdapter{gl}
	}

	mux := http.NewServeMux()
	authn.Routes(mux)
	srv.Routes(mux)
	handler := recoverer(log, auth.CSRF(mux))

	// Background work.
	go wire(ctx, cfg, log, &wired, sonarr, radarr, prowlarr, qb, jf, jfAdmin)
	go worker.Run(ctx)
	if cfg.VPNEnabled {
		go portsync.Run(ctx, gl, qb, time.Minute, log)
	}
	go housekeeping(ctx, st, log)

	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		log.Info("pelicula-server listening", "addr", cfg.Listen, "version", version, "vpn", cfg.VPNEnabled)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

// wire waits for the *arr API keys, then runs autowire until it succeeds.
// The dashboard serves throughout; /api/health reports wired=false until
// every step has completed once.
func wire(ctx context.Context, cfg config.Config, log *slog.Logger, wired *atomic.Bool,
	sonarr, radarr, prowlarr *arr.Client, qb *qbt.Client, jf *jellyfin.Client, jfAdmin *jellyfin.Admin) {

	var prowlarrAC autowire.ArrClient
	if prowlarr != nil {
		prowlarrAC = prowlarr
	}
	var qbPinger autowire.Pinger
	if qb != nil {
		qbPinger = qb
	}

	const retry = 2 * time.Minute
	for {
		keyCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		sKey, rKey, err := autowire.WaitForAPIKeys(keyCtx, cfg, sonarr, radarr, prowlarrAC)
		cancel()
		if err == nil {
			deps := autowire.Deps{
				Cfg:          cfg,
				Sonarr:       sonarr,
				Radarr:       radarr,
				Prowlarr:     prowlarrAC,
				QBT:          qbPinger,
				Jellyfin:     jfLibraries{jf},
				JFAdmin:      jfAdmin,
				SonarrAPIKey: sKey,
				RadarrAPIKey: rKey,
				Log:          log,
			}
			runCtx, cancelRun := context.WithTimeout(ctx, 10*time.Minute)
			err = autowire.Run(runCtx, deps)
			cancelRun()
			if err == nil {
				wired.Store(true)
				log.Info("autowire complete")
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		log.Warn("autowire incomplete; retrying", "error", err, "in", retry)
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
	}
}

// housekeeping purges expired sessions hourly and prunes finished jobs
// older than 30 days once a day.
func housekeeping(ctx context.Context, st *store.Store, log *slog.Logger) {
	hourly := time.NewTicker(time.Hour)
	daily := time.NewTicker(24 * time.Hour)
	defer hourly.Stop()
	defer daily.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hourly.C:
			if n, err := st.PurgeExpiredSessions(ctx); err != nil {
				log.Warn("purge sessions", "error", err)
			} else if n > 0 {
				log.Info("purged expired sessions", "count", n)
			}
		case <-daily.C:
			if n, err := st.PruneJobs(ctx, time.Now().Add(-30*24*time.Hour)); err != nil {
				log.Warn("prune jobs", "error", err)
			} else if n > 0 {
				log.Info("pruned old jobs", "count", n)
			}
		}
	}
}

// recoverer turns a handler panic into a 500 instead of killing the server.
func recoverer(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("handler panic", "path", r.URL.Path, "panic", rec)
				http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
