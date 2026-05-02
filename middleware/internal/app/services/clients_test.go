package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pelicula-api/internal/config"
)

func TestVersion_DefaultIsDev(t *testing.T) {
	if Version != "dev" {
		t.Errorf("expected Version == %q, got %q", "dev", Version)
	}
}

func TestVersion_NotEmpty(t *testing.T) {
	if Version == "" {
		t.Error("Version must not be empty")
	}
}

// minConfig returns a *config.Config with the given ConfigDir and base URLs
// pointing to srv (or empty if srv is nil). All other fields are zero.
func minConfig(t *testing.T, configDir string, sonarrURL string) *config.Config {
	t.Helper()
	return &config.Config{
		ConfigDir: configDir,
		URLs: config.URLs{
			Sonarr:   sonarrURL,
			Radarr:   sonarrURL,
			Prowlarr: sonarrURL,
			Bazarr:   sonarrURL,
			QBT:      sonarrURL,
		},
	}
}

// TestClients_TypedClientsConstructedOnce verifies that the typed-client
// pointers returned by services.New() are identical before and after
// ReloadKeys(). Calling ReloadKeys() must mutate the key inside each
// existing client rather than replacing the pointer.
func TestClients_TypedClientsConstructedOnce(t *testing.T) {
	dir := t.TempDir() // no XML files → loadKeys reads empty keys
	cfg := minConfig(t, dir, "http://localhost:0")

	svc := New(cfg, "jf-key")

	// Capture pointers immediately after construction.
	sonarr := svc.Sonarr
	radarr := svc.Radarr
	prowlarr := svc.Prowlarr
	bazarr := svc.Bazarr
	qbt := svc.Qbt

	if sonarr == nil || radarr == nil || prowlarr == nil || bazarr == nil || qbt == nil {
		t.Fatal("one or more typed clients are nil after New()")
	}

	// ReloadKeys must not replace any pointer.
	svc.ReloadKeys()

	if svc.Sonarr != sonarr {
		t.Error("ReloadKeys() replaced svc.Sonarr pointer — invariant violated")
	}
	if svc.Radarr != radarr {
		t.Error("ReloadKeys() replaced svc.Radarr pointer — invariant violated")
	}
	if svc.Prowlarr != prowlarr {
		t.Error("ReloadKeys() replaced svc.Prowlarr pointer — invariant violated")
	}
	if svc.Bazarr != bazarr {
		t.Error("ReloadKeys() replaced svc.Bazarr pointer — invariant violated")
	}
	if svc.Qbt != qbt {
		t.Error("ReloadKeys() replaced svc.Qbt pointer — invariant violated")
	}
}

// TestClients_AccessorsReturnFieldPointers verifies that the typed-client
// accessor methods return the same pointer as the corresponding public field.
func TestClients_AccessorsReturnFieldPointers(t *testing.T) {
	dir := t.TempDir()
	cfg := minConfig(t, dir, "http://localhost:0")
	svc := New(cfg, "jf-key")

	if svc.SonarrClient() != svc.Sonarr {
		t.Error("SonarrClient() != svc.Sonarr")
	}
	if svc.RadarrClient() != svc.Radarr {
		t.Error("RadarrClient() != svc.Radarr")
	}
	if svc.ProwlarrClient() != svc.Prowlarr {
		t.Error("ProwlarrClient() != svc.Prowlarr")
	}
	if svc.BazarrClient() != svc.Bazarr {
		t.Error("BazarrClient() != svc.Bazarr")
	}
	if svc.QbtClient() != svc.Qbt {
		t.Error("QbtClient() != svc.Qbt")
	}
}

// TestClients_ReloadKeysIsRaceFree exercises concurrent ReloadKeys() calls
// alongside in-flight HTTP requests against a real test server. The test must
// pass under "go test -race ./...".
//
// To ensure setAuth's RLock path is covered, we write a valid config.xml so
// that loadKeys finds a non-empty key, making every request exercise the
// lock on the apiKey field.
func TestClients_ReloadKeysIsRaceFree(t *testing.T) {
	// Spin up a minimal HTTP server that accepts any request and returns [].
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]")) //nolint:errcheck
	}))
	defer srv.Close()

	// Write a fake Sonarr config.xml so loadKeys loads a non-empty key,
	// ensuring setAuth's RLock path is exercised on every request.
	dir := t.TempDir()
	sonarrCfgDir := filepath.Join(dir, "sonarr")
	if err := os.MkdirAll(sonarrCfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	xmlContent := `<?xml version="1.0"?>
<Config>
  <ApiKey>test-sonarr-key-12345</ApiKey>
</Config>`
	if err := os.WriteFile(filepath.Join(sonarrCfgDir, "config.xml"), []byte(xmlContent), 0o644); err != nil {
		t.Fatalf("write config.xml: %v", err)
	}

	cfg := minConfig(t, dir, srv.URL)
	svc := New(cfg, "jf-key")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{}, 2)

	// Goroutine A: repeatedly reloads keys.
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				svc.ReloadKeys()
			}
		}
	}()

	// Goroutine B: repeatedly fires GET requests through the Sonarr typed client.
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			select {
			case <-ctx.Done():
				return
			default:
				// Errors from context cancellation or server closure are expected; ignore them.
				svc.Sonarr.Get(ctx, "/api/v3/system/status") //nolint:errcheck
			}
		}
	}()

	<-done
	<-done
}
