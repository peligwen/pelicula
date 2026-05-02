// cmd/pelicula-api is the entry point for the pelicula-api middleware service.
// It loads config, runs bootstrap, launches goroutines via supervisor, registers
// routes, and serves. Business logic lives in internal/.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pelicula-api/httputil"
	"pelicula-api/internal/app/bootstrap"
	"pelicula-api/internal/app/router"
	appservices "pelicula-api/internal/app/services"
	appsetup "pelicula-api/internal/app/setup"
	"pelicula-api/internal/app/supervisor"
	"pelicula-api/internal/config"
	"pelicula-api/internal/cryptogen"
	"pelicula-api/internal/httpx"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.Info("starting pelicula-api", "version", appservices.Version, "component", "main")
	httpx.DefaultUserAgent = "Pelicula/" + appservices.Version + " (+https://github.com/peligwen/pelicula)"
	cfg := config.Load()

	// Setup mode: serves only the wizard endpoints. The CLI
	// (cmd/pelicula/cmd_up.go) polls for .env existence after the wizard
	// writes it, then runs docker compose down on the setup container —
	// this binary is killed by docker, not by a graceful in-process
	// shutdown.
	if appsetup.NeedsSetup() {
		slog.Info("starting in setup mode", "component", "main")
		setupH := appsetup.New("/project/.env", cryptogen.GenerateAPIKey, generateReadablePassword)
		mux := http.NewServeMux()
		mux.HandleFunc("/api/pelicula/health", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"status":"setup"}`)) //nolint:errcheck
		})
		mux.HandleFunc("/api/pelicula/setup/detect", setupH.HandleDetect)
		mux.Handle("/api/pelicula/setup", httputil.RequireLocalOriginStrict(http.HandlerFunc(setupH.HandleSubmit)))
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		slog.Info("listening (setup mode)", "component", "main", "addr", ":8181", "version", appservices.Version)
		serveWithShutdown(ctx, ":8181", httpx.RecoverMiddleware(mux))
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a, err := bootstrap.New(ctx, cfg, generateReadablePassword)
	if err != nil {
		slog.Error("bootstrap failed", "component", "main", "error", err)
		os.Exit(1)
	}

	// Wire the one remaining cmd-level global (used by handleJobsList in jobs.go).
	services = a.Svc

	wg := supervisor.Run(ctx, a)

	mux := http.NewServeMux()
	router.Register(mux, router.Config{
		Auth:          a.Auth,
		Deps:          a.Deps,
		Health:        a.HealthHandler,
		SSE:           a.SSEHub,
		Sysinfo:       a.SysinfoHandler,
		Downloads:     a.DLHandler,
		Hooks:         a.HooksHandler,
		Backup:        a.BackupHandler,
		JF:            a.JFHandler,
		JFInfo:        a.JFInfoHandler,
		Library:       a.LibHandler,
		Catalog:       a.CatalogHandler,
		Search:        a.SearchHandler,
		Settings:      a.SettingsHandler,
		Actions:       a.ActionsHandler,
		Admin:         a.AdminHandler,
		Network:       a.NetworkHandler,
		StatusHandler: http.HandlerFunc(a.HandleStatus),
		JobsHandler:   http.HandlerFunc(handleJobsList),
	})

	slog.Info("listening", "component", "main", "addr", ":8181", "version", appservices.Version)
	serveWithShutdown(ctx, ":8181", httpx.RecoverMiddleware(mux))

	// Drain background goroutines before closing resources. The 5s cap
	// prevents a misbehaving goroutine from blocking the process indefinitely.
	shutdownDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(shutdownDone)
	}()
	t := time.NewTimer(5 * time.Second)
	defer t.Stop()
	select {
	case <-shutdownDone:
	case <-t.C:
		slog.Warn("background goroutines did not exit within 5s", "component", "main")
	}

	if err := a.Close(); err != nil {
		slog.Error("app close", "err", err, "component", "main")
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
