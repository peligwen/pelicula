package autowire_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"pelicula-api/internal/app/autowire"
	bazarrclient "pelicula-api/internal/clients/bazarr"
)

// stubSvc implements ArrSvc with controllable responses for testing.
type stubSvc struct {
	sonarrKey   string
	radarrKey   string
	prowlarrKey string
	configDir   string
	wired       bool
	bazarrKey   string
	bazarr      *bazarrclient.Client
	httpClient  *http.Client
	// responses maps "METHOD baseURL+path" → (body, statusCode)
	responses map[string]stubResp
}

type stubResp struct {
	body   []byte
	status int
}

func (s *stubSvc) ReloadKeys() {}
func (s *stubSvc) SonarrRadarrKeys() (string, string) {
	return s.sonarrKey, s.radarrKey
}
func (s *stubSvc) GetProwlarrKey() string { return s.prowlarrKey }
func (s *stubSvc) SetWired(v bool)        { s.wired = v }
func (s *stubSvc) ArrGet(baseURL, apiKey, path string) ([]byte, error) {
	return s.lookup("GET", baseURL+path)
}
func (s *stubSvc) ArrPost(baseURL, apiKey, path string, payload any) ([]byte, error) {
	return s.lookup("POST", baseURL+path)
}
func (s *stubSvc) ArrPut(baseURL, apiKey, path string, payload any) ([]byte, error) {
	return s.lookup("PUT", baseURL+path)
}
func (s *stubSvc) HTTPClient() *http.Client { return s.httpClient }
func (s *stubSvc) ConfigDir() string        { return s.configDir }
func (s *stubSvc) SetBazarrClient(apiKey string, client *bazarrclient.Client) {
	s.bazarrKey = apiKey
	s.bazarr = client
}
func (s *stubSvc) BazarrClient() *bazarrclient.Client { return s.bazarr }

func (s *stubSvc) lookup(method, key string) ([]byte, error) {
	if r, ok := s.responses[method+" "+key]; ok {
		return r.body, nil
	}
	return []byte("[]"), nil
}

// TestAutowireStateDone verifies that AutowireState.Done() starts false
// and becomes true after a successful Run.
func TestAutowireStateDone(t *testing.T) {
	// Spin up a tiny HTTP server that answers all service-readiness pings.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ping", "/System/Info/Public", "/api/system/status", "/":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("{}")) //nolint:errcheck
		case "/api/v3/downloadclient", "/api/v3/rootfolder", "/api/v3/notification":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("[]")) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	svc := &stubSvc{
		sonarrKey:  "sonarr-key",
		radarrKey:  "radarr-key",
		configDir:  t.TempDir(), // no bazarr config.yaml → wireBazarr is a no-op
		httpClient: srv.Client(),
	}

	urls := autowire.URLs{
		Sonarr:      srv.URL,
		Radarr:      srv.URL,
		Prowlarr:    srv.URL,
		Bazarr:      srv.URL,
		Jellyfin:    srv.URL,
		QBT:         srv.URL,
		PeliculaAPI: "http://pelicula-api:8181",
	}

	// Stub arr responses: empty lists = "nothing configured yet"
	empty, _ := json.Marshal([]any{})
	svc.responses = map[string]stubResp{
		"GET " + srv.URL + "/api/v3/downloadclient": {body: empty},
		"GET " + srv.URL + "/api/v3/rootfolder":     {body: empty},
		"GET " + srv.URL + "/api/v3/notification":   {body: empty},
	}

	jellyfinCalled := false
	a, state := autowire.NewAutowirer(autowire.Config{
		Svc:           svc,
		URLs:          urls,
		VPNConfigured: false, // skip Prowlarr/qBT readiness checks
		WireJellyfin:  func() { jellyfinCalled = true },
		GetLibraries:  func() []autowire.Library { return nil },
	})
	_ = a

	if state.Done() {
		t.Fatal("AutowireState.Done() should be false before Run")
	}

	if err := a.Run(context.Background()); err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}

	if !state.Done() {
		t.Error("AutowireState.Done() should be true after successful Run")
	}
	if !jellyfinCalled {
		t.Error("WireJellyfin callback was not called")
	}
	if !svc.wired {
		t.Error("SetWired(true) was not called on the service")
	}
}

// TestAutowireStateDoneBeforeRun verifies the zero-value is false.
func TestAutowireStateDoneBeforeRun(t *testing.T) {
	_, state := autowire.NewAutowirer(autowire.Config{
		Svc: &stubSvc{
			sonarrKey:  "k",
			radarrKey:  "k",
			httpClient: http.DefaultClient,
		},
		URLs:         autowire.URLs{},
		GetLibraries: func() []autowire.Library { return nil },
	})
	if state.Done() {
		t.Error("AutowireState.Done() must be false before Run is called")
	}
}
