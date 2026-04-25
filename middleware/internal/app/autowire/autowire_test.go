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
	// captured records every non-GET call for assertion in drift tests.
	captured []capturedCall
}

type stubResp struct {
	body   []byte
	status int
}

type capturedCall struct {
	method  string
	path    string
	payload any
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
	s.captured = append(s.captured, capturedCall{"POST", baseURL + path, payload})
	return s.lookup("POST", baseURL+path)
}
func (s *stubSvc) ArrPut(baseURL, apiKey, path string, payload any) ([]byte, error) {
	s.captured = append(s.captured, capturedCall{"PUT", baseURL + path, payload})
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

func (s *stubSvc) findPut(pathSuffix string) *capturedCall {
	for i := range s.captured {
		if s.captured[i].method == "PUT" && len(s.captured[i].path) >= len(pathSuffix) &&
			s.captured[i].path[len(s.captured[i].path)-len(pathSuffix):] == pathSuffix {
			return &s.captured[i]
		}
	}
	return nil
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

// newDriftTestSrv creates a test HTTP server that answers readiness pings
// and returns 200 OK for everything else. The caller provides arrResponses which
// are served by the stubSvc, not the HTTP server — this server only satisfies
// waitForServices polling.
func newDriftTestSrv(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

// TestWireDownloadClientDrift verifies that when an existing QBittorrent client
// has a stale host or category, wireDownloadClient issues a PUT to correct it.
func TestWireDownloadClientDrift(t *testing.T) {
	srv := newDriftTestSrv(t)
	defer srv.Close()

	// Existing client with wrong host and wrong category.
	existing := []map[string]any{
		{
			"id":             float64(7),
			"name":           "qBittorrent",
			"implementation": "QBittorrent",
			"configContract": "QBittorrentSettings",
			"enable":         true,
			"priority":       float64(1),
			"fields": []any{
				map[string]any{"name": "host", "value": "old-gluetun"},
				map[string]any{"name": "port", "value": float64(8080)},
				map[string]any{"name": "category", "value": "wrong-category"},
			},
		},
	}
	existingJSON, _ := json.Marshal(existing)

	svc := &stubSvc{
		sonarrKey:   "sonarr-key",
		radarrKey:   "radarr-key",
		prowlarrKey: "prowlarr-key",
		configDir:   t.TempDir(),
		httpClient:  srv.Client(),
		responses: map[string]stubResp{
			"GET " + srv.URL + "/api/v3/downloadclient": {body: existingJSON},
			"GET " + srv.URL + "/api/v3/notification":   {body: []byte("[]")},
			"GET " + srv.URL + "/api/v3/rootfolder":     {body: []byte("[]")},
			"GET " + srv.URL + "/api/v1/applications":   {body: []byte("[]")},
		},
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
	a, _ := autowire.NewAutowirer(autowire.Config{
		Svc:           svc,
		URLs:          urls,
		VPNConfigured: true,
		GetLibraries:  func() []autowire.Library { return nil },
		WireJellyfin:  func() {},
	})
	if err := a.Run(context.Background()); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	put := svc.findPut("/api/v3/downloadclient/7")
	if put == nil {
		t.Fatal("expected PUT to /api/v3/downloadclient/7 to correct drift, but none was issued")
	}

	payload, ok := put.payload.(map[string]any)
	if !ok {
		t.Fatal("PUT payload is not map[string]any")
	}
	fields, _ := payload["fields"].([]any)
	got := map[string]any{}
	for _, fRaw := range fields {
		f, ok := fRaw.(map[string]any)
		if !ok {
			continue
		}
		got[f["name"].(string)] = f["value"]
	}
	if got["host"] != "gluetun" {
		t.Errorf("host not corrected: got %v", got["host"])
	}
	if got["category"] != "tv-sonarr" {
		t.Errorf("category not corrected: got %v", got["category"])
	}
}

// TestWireImportWebhookURLDrift verifies that when the Procula webhook exists
// but has a stale URL, wireImportWebhook issues a PUT with the correct URL.
func TestWireImportWebhookURLDrift(t *testing.T) {
	srv := newDriftTestSrv(t)
	defer srv.Close()

	const correctAPI = "http://pelicula-api-new:8181"

	existing := []map[string]any{
		{
			"id":             float64(3),
			"name":           "Procula",
			"implementation": "Webhook",
			"configContract": "WebhookSettings",
			"onDownload":     true,
			"onUpgrade":      true,
			"fields": []any{
				map[string]any{"name": "url", "value": "http://old-api:8181/api/pelicula/hooks/import"},
				map[string]any{"name": "method", "value": float64(1)},
			},
		},
	}
	existingJSON, _ := json.Marshal(existing)

	svc := &stubSvc{
		sonarrKey:  "sonarr-key",
		radarrKey:  "radarr-key",
		configDir:  t.TempDir(),
		httpClient: srv.Client(),
		responses: map[string]stubResp{
			"GET " + srv.URL + "/api/v3/notification":   {body: existingJSON},
			"GET " + srv.URL + "/api/v3/downloadclient": {body: []byte("[]")},
			"GET " + srv.URL + "/api/v3/rootfolder":     {body: []byte("[]")},
		},
	}

	urls := autowire.URLs{
		Sonarr:      srv.URL,
		Radarr:      srv.URL,
		Bazarr:      srv.URL,
		Jellyfin:    srv.URL,
		PeliculaAPI: correctAPI,
	}
	a, _ := autowire.NewAutowirer(autowire.Config{
		Svc:           svc,
		URLs:          urls,
		VPNConfigured: false,
		GetLibraries:  func() []autowire.Library { return nil },
		WireJellyfin:  func() {},
	})
	if err := a.Run(context.Background()); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	put := svc.findPut("/api/v3/notification/3")
	if put == nil {
		t.Fatal("expected PUT to /api/v3/notification/3 to correct URL drift, but none was issued")
	}

	payload, ok := put.payload.(map[string]any)
	if !ok {
		t.Fatal("PUT payload is not map[string]any")
	}
	fields, _ := payload["fields"].([]any)
	var gotURL string
	for _, fRaw := range fields {
		f, ok := fRaw.(map[string]any)
		if !ok {
			continue
		}
		if f["name"] == "url" {
			gotURL, _ = f["value"].(string)
		}
	}
	wantURL := correctAPI + "/api/pelicula/hooks/import"
	if gotURL != wantURL {
		t.Errorf("url not corrected: got %q, want %q", gotURL, wantURL)
	}
}

// TestWireImportWebhookSecretDrift verifies that a rotated webhook secret
// triggers a PUT that updates the X-Webhook-Secret header value.
func TestWireImportWebhookSecretDrift(t *testing.T) {
	srv := newDriftTestSrv(t)
	defer srv.Close()

	const peliculaAPI = "http://pelicula-api:8181"
	const newSecret = "new-secret-456"

	existing := []map[string]any{
		{
			"id":             float64(5),
			"name":           "Procula",
			"implementation": "Webhook",
			"configContract": "WebhookSettings",
			"onDownload":     true,
			"onUpgrade":      true,
			"fields": []any{
				map[string]any{"name": "url", "value": peliculaAPI + "/api/pelicula/hooks/import"},
				map[string]any{"name": "method", "value": float64(1)},
				map[string]any{
					"name": "headers",
					"value": []any{
						map[string]any{"key": "X-Webhook-Secret", "value": "old-secret-123"},
					},
				},
			},
		},
	}
	existingJSON, _ := json.Marshal(existing)

	svc := &stubSvc{
		sonarrKey:  "sonarr-key",
		radarrKey:  "radarr-key",
		configDir:  t.TempDir(),
		httpClient: srv.Client(),
		responses: map[string]stubResp{
			"GET " + srv.URL + "/api/v3/notification":   {body: existingJSON},
			"GET " + srv.URL + "/api/v3/downloadclient": {body: []byte("[]")},
			"GET " + srv.URL + "/api/v3/rootfolder":     {body: []byte("[]")},
		},
	}

	urls := autowire.URLs{
		Sonarr:      srv.URL,
		Radarr:      srv.URL,
		Bazarr:      srv.URL,
		Jellyfin:    srv.URL,
		PeliculaAPI: peliculaAPI,
	}
	a, _ := autowire.NewAutowirer(autowire.Config{
		Svc:           svc,
		URLs:          urls,
		VPNConfigured: false,
		WebhookSecret: newSecret,
		GetLibraries:  func() []autowire.Library { return nil },
		WireJellyfin:  func() {},
	})
	if err := a.Run(context.Background()); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	put := svc.findPut("/api/v3/notification/5")
	if put == nil {
		t.Fatal("expected PUT to /api/v3/notification/5 to correct secret drift, but none was issued")
	}

	payload, ok := put.payload.(map[string]any)
	if !ok {
		t.Fatal("PUT payload is not map[string]any")
	}
	fields, _ := payload["fields"].([]any)
	var gotSecret string
	for _, fRaw := range fields {
		f, ok := fRaw.(map[string]any)
		if !ok {
			continue
		}
		if f["name"] != "headers" {
			continue
		}
		if vals, ok := f["value"].([]map[string]any); ok {
			for _, h := range vals {
				if h["key"] == "X-Webhook-Secret" {
					gotSecret, _ = h["value"].(string)
				}
			}
		}
	}
	if gotSecret != newSecret {
		t.Errorf("X-Webhook-Secret not corrected: got %q, want %q", gotSecret, newSecret)
	}
}
