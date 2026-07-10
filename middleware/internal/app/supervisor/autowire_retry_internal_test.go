package supervisor

// White-box tests for runAutowireWithRetry (MWA-2). This file is package
// supervisor (not supervisor_test) so it can override the unexported backoff
// vars — tests shrink them to avoid waiting out the real 30s/5m production
// values.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	pelapp "pelicula-api/internal/app/app"
	"pelicula-api/internal/app/autowire"
	appservices "pelicula-api/internal/app/services"
	"pelicula-api/internal/config"
)

// writeArrConfigXML writes a minimal *arr config.xml so appservices.Clients'
// ReloadKeys()/loadKeys() picks up apiKey for the given service directory.
func writeArrConfigXML(t *testing.T, configDir, service, apiKey string) {
	t.Helper()
	dir := filepath.Join(configDir, service)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	content := "<Config><ApiKey>" + apiKey + "</ApiKey></Config>"
	if err := os.WriteFile(filepath.Join(dir, "config.xml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write config.xml: %v", err)
	}
}

// TestRunAutowireWithRetry_RetriesUntilFullyWired verifies the MWA-2 fix: a
// partial-wiring failure on the first attempt (Radarr's download-client
// wiring succeeds but Sonarr's transiently fails) does not leave IsWired()
// permanently false — runAutowireWithRetry backs off and retries until every
// step succeeds.
func TestRunAutowireWithRetry_RetriesUntilFullyWired(t *testing.T) {
	// Shrink backoff so the test doesn't wait out the real 30s default.
	origInitial, origMax := autowireInitialBackoff, autowireMaxBackoff
	autowireInitialBackoff = 5 * time.Millisecond
	autowireMaxBackoff = 20 * time.Millisecond
	t.Cleanup(func() {
		autowireInitialBackoff = origInitial
		autowireMaxBackoff = origMax
	})

	var downloadClientGETs atomic.Int32

	arrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v3/downloadclient" {
			n := downloadClientGETs.Add(1)
			if n == 1 {
				// Fail only the very first ever call to this endpoint — that's
				// Sonarr's wireDownloadClient on attempt 1 (Sonarr is wired
				// before Radarr in autowire.Run). Every subsequent call
				// (Radarr's call on attempt 1, and both calls on attempt 2)
				// succeeds, so attempt 1 is a genuine partial failure and
				// attempt 2 completes cleanly.
				//
				// 400 (not 5xx) is deliberate: httpx.Client's own RetryPolicy
				// retries 5xx internally, which would absorb a single 500
				// before wireDownloadClient ever saw an error and this test
				// would (for the wrong reason) never exercise the outer
				// supervisor-level retry this test targets. 4xx never retries
				// at the httpx layer, so this failure surfaces immediately.
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.Write([]byte("[]")) //nolint:errcheck
			return
		}
		if r.Method == http.MethodGet {
			w.Write([]byte("[]")) //nolint:errcheck
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer arrSrv.Close()

	configDir := t.TempDir()
	writeArrConfigXML(t, configDir, "sonarr", "sonarr-key")
	writeArrConfigXML(t, configDir, "radarr", "radarr-key")
	writeArrConfigXML(t, configDir, "prowlarr", "prowlarr-key")

	svc := appservices.New(&config.Config{
		ConfigDir: configDir,
		URLs: config.URLs{
			Sonarr:   arrSrv.URL,
			Radarr:   arrSrv.URL,
			Prowlarr: arrSrv.URL,
			QBT:      arrSrv.URL,
			Bazarr:   arrSrv.URL,
			Jellyfin: arrSrv.URL,
			Procula:  arrSrv.URL,
		},
	}, "")

	autowirer, _ := autowire.NewAutowirer(autowire.Config{
		Svc: svc,
		URLs: autowire.URLs{
			Sonarr:      arrSrv.URL,
			Radarr:      arrSrv.URL,
			Prowlarr:    arrSrv.URL,
			Bazarr:      arrSrv.URL,
			Jellyfin:    arrSrv.URL,
			QBT:         arrSrv.URL,
			PeliculaAPI: "http://pelicula-api:8181",
		},
		VPNConfigured: true,
		GetLibraries:  func() []autowire.Library { return nil },
	})

	a := &pelapp.App{Svc: svc, Autowirer: autowirer}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		runAutowireWithRetry(ctx, a)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runAutowireWithRetry did not return within 3s")
	}

	if !svc.IsWired() {
		t.Error("expected svc.IsWired() to be true after retry recovered from the partial failure")
	}
	if n := downloadClientGETs.Load(); n < 3 {
		t.Errorf("expected at least 3 GET /api/v3/downloadclient calls (1 failed + at least 2 more across a retried attempt), got %d", n)
	}
}

// TestRunAutowireWithRetry_StopsOnCtxCancel verifies that a persistently
// failing Autowirer.Run does not retry forever once ctx is cancelled —
// runAutowireWithRetry must return promptly instead of waiting out the
// backoff timer.
func TestRunAutowireWithRetry_StopsOnCtxCancel(t *testing.T) {
	origInitial, origMax := autowireInitialBackoff, autowireMaxBackoff
	// Deliberately long backoff — the test asserts we do NOT wait this out.
	autowireInitialBackoff = 2 * time.Second
	autowireMaxBackoff = 2 * time.Second
	t.Cleanup(func() {
		autowireInitialBackoff = origInitial
		autowireMaxBackoff = origMax
	})

	// No config.xml written — SonarrRadarrKeys() always returns empty, so
	// every Autowirer.Run attempt fails fast with "missing API keys" right
	// after a trivially-satisfied readiness check.
	arrSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer arrSrv.Close()

	configDir := t.TempDir()
	svc := appservices.New(&config.Config{
		ConfigDir: configDir,
		URLs: config.URLs{
			Sonarr:   arrSrv.URL,
			Radarr:   arrSrv.URL,
			Prowlarr: arrSrv.URL,
			QBT:      arrSrv.URL,
			Bazarr:   arrSrv.URL,
			Jellyfin: arrSrv.URL,
			Procula:  arrSrv.URL,
		},
	}, "")

	autowirer, _ := autowire.NewAutowirer(autowire.Config{
		Svc: svc,
		URLs: autowire.URLs{
			Sonarr:      arrSrv.URL,
			Radarr:      arrSrv.URL,
			Prowlarr:    arrSrv.URL,
			Bazarr:      arrSrv.URL,
			Jellyfin:    arrSrv.URL,
			QBT:         arrSrv.URL,
			PeliculaAPI: "http://pelicula-api:8181",
		},
		VPNConfigured: false,
		GetLibraries:  func() []autowire.Library { return nil },
	})

	a := &pelapp.App{Svc: svc, Autowirer: autowirer}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		runAutowireWithRetry(ctx, a)
		close(done)
	}()

	// Let attempt 1 fail and enter its backoff wait, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("runAutowireWithRetry did not stop within 500ms of ctx cancellation (still waiting out the 2s backoff?)")
	}

	if svc.IsWired() {
		t.Error("svc.IsWired() should remain false — every attempt failed")
	}
}
