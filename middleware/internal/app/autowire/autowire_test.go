package autowire_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"pelicula-api/internal/app/autowire"
	arrclient "pelicula-api/internal/clients/arr"
	bazarrclient "pelicula-api/internal/clients/bazarr"
)

// stubSvc implements ArrSvc with controllable responses for testing.
// Typed clients (SonarrClient/RadarrClient/ProwlarrClient) are backed by a
// shared httptest.Server; all *arr HTTP calls are captured in-process.
type stubSvc struct {
	sonarrKey   string
	radarrKey   string
	prowlarrKey string
	configDir   string
	wired       bool
	bazarrKey   string
	bazarr      *bazarrclient.Client
	httpClient  *http.Client

	mu sync.Mutex
	// responses maps "METHOD /path" → body bytes served by the test server.
	// Only the path portion is keyed (no host), because all three typed clients
	// point at the same httptest.Server.
	responses map[string][]byte
	// captured records every non-GET call for assertion in drift tests.
	captured []capturedCall

	// typed clients — constructed by newStubSvc, never nil after that.
	sonarr   *arrclient.Client
	radarr   *arrclient.Client
	prowlarr *arrclient.Client
	// arrSrv is the backing test server shared by all three typed clients.
	arrSrv *httptest.Server
}

type capturedCall struct {
	method  string
	path    string
	payload any
}

// newStubSvc builds a stubSvc whose typed arr clients are backed by an
// httptest.Server. responses maps "METHOD /path" → JSON body. The caller is
// responsible for calling svc.arrSrv.Close() when done.
func newStubSvc(t *testing.T, httpCli *http.Client) *stubSvc {
	t.Helper()
	s := &stubSvc{
		httpClient: httpCli,
		configDir:  t.TempDir(),
		responses:  map[string][]byte{},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture non-GET calls.
		if r.Method != http.MethodGet {
			body, _ := io.ReadAll(r.Body)
			// Decode to map[string]any for generic inspection; callers that need
			// typed structs will re-decode from the map.
			var payload any
			if len(body) > 0 {
				if body[0] == '[' {
					var arr []map[string]any
					if json.Unmarshal(body, &arr) == nil {
						payload = arr
					}
				} else {
					var m map[string]any
					if json.Unmarshal(body, &m) == nil {
						payload = m
					}
				}
				if payload == nil {
					payload = body
				}
			}
			s.mu.Lock()
			s.captured = append(s.captured, capturedCall{r.Method, r.URL.Path, payload})
			s.mu.Unlock()
		}

		key := r.Method + " " + r.URL.Path
		s.mu.Lock()
		resp, ok := s.responses[key]
		s.mu.Unlock()
		if !ok {
			resp = []byte("[]")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(resp) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)

	s.arrSrv = srv
	s.sonarr = arrclient.New(srv.URL, "test-key")
	s.radarr = arrclient.New(srv.URL, "test-key")
	s.prowlarr = arrclient.New(srv.URL, "test-key")
	return s
}

// setResponse registers a canned response for a method+path combination.
func (s *stubSvc) setResponse(method, path string, body []byte) {
	s.mu.Lock()
	s.responses[method+" "+path] = body
	s.mu.Unlock()
}

func (s *stubSvc) ReloadKeys() {}
func (s *stubSvc) SonarrRadarrKeys() (string, string) {
	return s.sonarrKey, s.radarrKey
}
func (s *stubSvc) GetProwlarrKey() string             { return s.prowlarrKey }
func (s *stubSvc) SetWired(v bool)                    { s.wired = v }
func (s *stubSvc) SonarrClient() *arrclient.Client    { return s.sonarr }
func (s *stubSvc) RadarrClient() *arrclient.Client    { return s.radarr }
func (s *stubSvc) ProwlarrClient() *arrclient.Client  { return s.prowlarr }
func (s *stubSvc) HTTPClient() *http.Client           { return s.httpClient }
func (s *stubSvc) ConfigDir() string                  { return s.configDir }
func (s *stubSvc) SetBazarrAPIKey(apiKey string)      { s.bazarrKey = apiKey }
func (s *stubSvc) BazarrClient() *bazarrclient.Client { return s.bazarr }

// findPut finds the first captured PUT whose path ends with pathSuffix and
// returns the raw decoded payload (map[string]any or []map[string]any).
func (s *stubSvc) findPut(pathSuffix string) *capturedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.captured {
		c := &s.captured[i]
		if c.method == "PUT" && strings.HasSuffix(c.path, pathSuffix) {
			return c
		}
	}
	return nil
}

// findPost finds the first captured POST whose path ends with pathSuffix.
func (s *stubSvc) findPost(pathSuffix string) *capturedCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.captured {
		c := &s.captured[i]
		if c.method == "POST" && strings.HasSuffix(c.path, pathSuffix) {
			return c
		}
	}
	return nil
}

// TestAutowireStateDone verifies that AutowireState.Done() starts false
// and becomes true after a successful Run.
func TestAutowireStateDone(t *testing.T) {
	// Spin up a tiny HTTP server that answers all service-readiness pings.
	pingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("{}")) //nolint:errcheck
	}))
	defer pingSrv.Close()

	svc := newStubSvc(t, pingSrv.Client())
	svc.sonarrKey = "sonarr-key"
	svc.radarrKey = "radarr-key"

	// Empty lists — "nothing configured yet".
	empty, _ := json.Marshal([]any{})
	svc.setResponse("GET", "/api/v3/downloadclient", empty)
	svc.setResponse("GET", "/api/v3/rootfolder", empty)
	svc.setResponse("GET", "/api/v3/notification", empty)
	svc.setResponse("GET", "/api/v3/releaseprofile", empty)

	urls := autowire.URLs{
		Sonarr:      pingSrv.URL,
		Radarr:      pingSrv.URL,
		Prowlarr:    pingSrv.URL,
		Bazarr:      pingSrv.URL,
		Jellyfin:    pingSrv.URL,
		QBT:         pingSrv.URL,
		PeliculaAPI: "http://pelicula-api:8181",
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
	pingSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer pingSrv.Close()
	svc := newStubSvc(t, pingSrv.Client())
	svc.sonarrKey = "k"
	svc.radarrKey = "k"
	_, state := autowire.NewAutowirer(autowire.Config{
		Svc:          svc,
		URLs:         autowire.URLs{},
		GetLibraries: func() []autowire.Library { return nil },
	})
	if state.Done() {
		t.Error("AutowireState.Done() must be false before Run is called")
	}
}

// newDriftTestSrv creates a test HTTP server that answers readiness pings
// and returns 200 OK for everything else. The caller provides responses which
// are served by the stubSvc's internal arr server, not this ping server.
func newDriftTestSrv(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

// TestWireDownloadClientDrift verifies that when an existing QBittorrent client
// has a stale host or category, wireDownloadClient issues a PUT to correct it.
func TestWireDownloadClientDrift(t *testing.T) {
	pingSrv := newDriftTestSrv(t)
	defer pingSrv.Close()

	svc := newStubSvc(t, pingSrv.Client())
	svc.sonarrKey = "sonarr-key"
	svc.radarrKey = "radarr-key"
	svc.prowlarrKey = "prowlarr-key"

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

	empty := []byte("[]")
	svc.setResponse("GET", "/api/v3/downloadclient", existingJSON)
	svc.setResponse("GET", "/api/v3/notification", empty)
	svc.setResponse("GET", "/api/v3/rootfolder", empty)
	svc.setResponse("GET", "/api/v1/applications", empty)
	svc.setResponse("GET", "/api/v3/releaseprofile", empty)

	urls := autowire.URLs{
		Sonarr:      pingSrv.URL,
		Radarr:      pingSrv.URL,
		Prowlarr:    pingSrv.URL,
		Bazarr:      pingSrv.URL,
		Jellyfin:    pingSrv.URL,
		QBT:         pingSrv.URL,
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
		t.Fatalf("PUT payload is not map[string]any, got %T", put.payload)
	}
	// Extract fields array from the payload.
	fields, _ := payload["fields"].([]any)
	got := make(map[string]any)
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
	pingSrv := newDriftTestSrv(t)
	defer pingSrv.Close()

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

	svc := newStubSvc(t, pingSrv.Client())
	svc.sonarrKey = "sonarr-key"
	svc.radarrKey = "radarr-key"

	empty := []byte("[]")
	svc.setResponse("GET", "/api/v3/notification", existingJSON)
	svc.setResponse("GET", "/api/v3/downloadclient", empty)
	svc.setResponse("GET", "/api/v3/rootfolder", empty)
	svc.setResponse("GET", "/api/v3/releaseprofile", empty)

	urls := autowire.URLs{
		Sonarr:      pingSrv.URL,
		Radarr:      pingSrv.URL,
		Bazarr:      pingSrv.URL,
		Jellyfin:    pingSrv.URL,
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
		t.Fatalf("PUT payload is not map[string]any, got %T", put.payload)
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
	pingSrv := newDriftTestSrv(t)
	defer pingSrv.Close()

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

	svc := newStubSvc(t, pingSrv.Client())
	svc.sonarrKey = "sonarr-key"
	svc.radarrKey = "radarr-key"

	empty := []byte("[]")
	svc.setResponse("GET", "/api/v3/notification", existingJSON)
	svc.setResponse("GET", "/api/v3/downloadclient", empty)
	svc.setResponse("GET", "/api/v3/rootfolder", empty)
	svc.setResponse("GET", "/api/v3/releaseprofile", empty)

	urls := autowire.URLs{
		Sonarr:      pingSrv.URL,
		Radarr:      pingSrv.URL,
		Bazarr:      pingSrv.URL,
		Jellyfin:    pingSrv.URL,
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
		t.Fatalf("PUT payload is not map[string]any, got %T", put.payload)
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
		if hdrs, ok := f["value"].([]any); ok {
			for _, hRaw := range hdrs {
				h, ok := hRaw.(map[string]any)
				if !ok {
					continue
				}
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

// newReleaseProfileSvc builds a stubSvc wired for release-profile-only tests.
// It answers readiness pings via the test server and stubs out the other
// endpoints so Run() doesn't fail before reaching wireReleaseProfile.
func newReleaseProfileSvc(t *testing.T, pingSrv *httptest.Server, profileJSON []byte) *stubSvc {
	t.Helper()
	svc := newStubSvc(t, pingSrv.Client())
	svc.sonarrKey = "sonarr-key"
	svc.radarrKey = "radarr-key"

	empty := []byte("[]")
	svc.setResponse("GET", "/api/v3/releaseprofile", profileJSON)
	svc.setResponse("GET", "/api/v3/downloadclient", empty)
	svc.setResponse("GET", "/api/v3/rootfolder", empty)
	svc.setResponse("GET", "/api/v3/notification", empty)
	return svc
}

func runReleaseProfileTest(t *testing.T, svc *stubSvc, pingSrv *httptest.Server) {
	t.Helper()
	urls := autowire.URLs{
		Sonarr:      pingSrv.URL,
		Radarr:      pingSrv.URL,
		Bazarr:      pingSrv.URL,
		Jellyfin:    pingSrv.URL,
		PeliculaAPI: "http://pelicula-api:8181",
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
}

// TestWireReleaseProfileCreate verifies that when no release profile exists,
// wireReleaseProfile POSTs a new profile with the desired ignored list.
func TestWireReleaseProfileCreate(t *testing.T) {
	pingSrv := newDriftTestSrv(t)
	defer pingSrv.Close()

	empty, _ := json.Marshal([]any{})
	svc := newReleaseProfileSvc(t, pingSrv, empty)

	runReleaseProfileTest(t, svc, pingSrv)

	post := svc.findPost("/api/v3/releaseprofile")
	if post == nil {
		t.Fatal("expected POST to /api/v3/releaseprofile, none issued")
	}

	payload, ok := post.payload.(map[string]any)
	if !ok {
		t.Fatalf("POST payload is not map[string]any, got %T", post.payload)
	}
	if payload["name"] != "Pelicula" {
		t.Errorf("name = %q, want Pelicula", payload["name"])
	}
	if enabled, _ := payload["enabled"].(bool); !enabled {
		t.Error("expected enabled=true")
	}
	ignored, _ := payload["ignored"].([]any)
	wantSet := map[string]struct{}{
		"REMUX": {}, "BluRay-2160p": {}, "WEB-2160p": {}, "WEBDL-2160p": {}, "HDR10+": {}, "DV ": {},
	}
	gotSet := make(map[string]struct{}, len(ignored))
	for _, v := range ignored {
		if s, ok := v.(string); ok {
			gotSet[s] = struct{}{}
		}
	}
	for k := range wantSet {
		if _, ok := gotSet[k]; !ok {
			t.Errorf("ignored missing %q", k)
		}
	}
}

// TestWireReleaseProfileDriftCorrected verifies that when an existing profile
// has a stale ignored list, wireReleaseProfile PUTs with the corrected list,
// preserving id and enabled.
func TestWireReleaseProfileDriftCorrected(t *testing.T) {
	pingSrv := newDriftTestSrv(t)
	defer pingSrv.Close()

	existing := []map[string]any{
		{
			"id":        float64(42),
			"name":      "Pelicula",
			"enabled":   true,
			"required":  []any{},
			"ignored":   []any{"WEB-2160p"}, // stale — missing REMUX and others
			"indexerId": float64(0),
			"tags":      []any{},
		},
	}
	existingJSON, _ := json.Marshal(existing)
	svc := newReleaseProfileSvc(t, pingSrv, existingJSON)

	runReleaseProfileTest(t, svc, pingSrv)

	put := svc.findPut("/api/v3/releaseprofile/42")
	if put == nil {
		t.Fatal("expected PUT to /api/v3/releaseprofile/42, none issued")
	}

	payload, ok := put.payload.(map[string]any)
	if !ok {
		t.Fatalf("PUT payload is not map[string]any, got %T", put.payload)
	}
	if id := payload["id"]; id != float64(42) {
		t.Errorf("id not preserved: got %v, want 42", id)
	}
	if enabled, _ := payload["enabled"].(bool); !enabled {
		t.Error("enabled not preserved: want true")
	}
	ignored, _ := payload["ignored"].([]any)
	wantSet := map[string]struct{}{
		"REMUX": {}, "BluRay-2160p": {}, "WEB-2160p": {}, "WEBDL-2160p": {}, "HDR10+": {}, "DV ": {},
	}
	gotSet := make(map[string]struct{}, len(ignored))
	for _, v := range ignored {
		if s, ok := v.(string); ok {
			gotSet[s] = struct{}{}
		}
	}
	for k := range wantSet {
		if _, ok := gotSet[k]; !ok {
			t.Errorf("corrected ignored missing %q", k)
		}
	}
}

// TestWireReleaseProfileNoChange verifies that when the existing profile
// already matches, no PUT or POST is issued.
func TestWireReleaseProfileNoChange(t *testing.T) {
	pingSrv := newDriftTestSrv(t)
	defer pingSrv.Close()

	existing := []map[string]any{
		{
			"id":        float64(5),
			"name":      "Pelicula",
			"enabled":   true,
			"required":  []any{},
			"ignored":   []any{"REMUX", "BluRay-2160p", "WEB-2160p", "WEBDL-2160p", "HDR10+", "DV "},
			"indexerId": float64(0),
			"tags":      []any{},
		},
	}
	existingJSON, _ := json.Marshal(existing)
	svc := newReleaseProfileSvc(t, pingSrv, existingJSON)

	runReleaseProfileTest(t, svc, pingSrv)

	svc.mu.Lock()
	defer svc.mu.Unlock()
	for _, c := range svc.captured {
		if (c.method == "PUT" || c.method == "POST") && strings.HasSuffix(c.path, "/api/v3/releaseprofile") {
			t.Errorf("unexpected %s to %s when profile already correct", c.method, c.path)
		}
	}
}

// TestWireReleaseProfileOptOut verifies that when PELICULA_DEFAULT_RELEASE_PROFILE=false,
// no API calls to /releaseprofile are made.
func TestWireReleaseProfileOptOut(t *testing.T) {
	t.Setenv("PELICULA_DEFAULT_RELEASE_PROFILE", "false")

	pingSrv := newDriftTestSrv(t)
	defer pingSrv.Close()

	empty, _ := json.Marshal([]any{})
	svc := newReleaseProfileSvc(t, pingSrv, empty)

	runReleaseProfileTest(t, svc, pingSrv)

	svc.mu.Lock()
	defer svc.mu.Unlock()
	for _, c := range svc.captured {
		if strings.HasSuffix(c.path, "/api/v3/releaseprofile") {
			t.Errorf("unexpected %s to %s when PELICULA_DEFAULT_RELEASE_PROFILE=false", c.method, c.path)
		}
	}
}

// TestWireReleaseProfileUserEditedTagsPreserved verifies that when an existing
// profile has user-added tags and a stale ignored list, the PUT preserves the
// tags while correcting ignored.
func TestWireReleaseProfileUserEditedTagsPreserved(t *testing.T) {
	pingSrv := newDriftTestSrv(t)
	defer pingSrv.Close()

	existing := []map[string]any{
		{
			"id":        float64(7),
			"name":      "Pelicula",
			"enabled":   true,
			"required":  []any{},
			"ignored":   []any{"WEB-2160p"}, // stale
			"indexerId": float64(0),
			"tags":      []any{float64(3)},
		},
	}
	existingJSON, _ := json.Marshal(existing)
	svc := newReleaseProfileSvc(t, pingSrv, existingJSON)

	runReleaseProfileTest(t, svc, pingSrv)

	put := svc.findPut("/api/v3/releaseprofile/7")
	if put == nil {
		t.Fatal("expected PUT to /api/v3/releaseprofile/7, none issued")
	}

	payload, ok := put.payload.(map[string]any)
	if !ok {
		t.Fatalf("PUT payload is not map[string]any, got %T", put.payload)
	}
	tags, _ := payload["tags"].([]any)
	if len(tags) != 1 || tags[0] != float64(3) {
		t.Errorf("tags not preserved: got %v, want [3]", tags)
	}
}
