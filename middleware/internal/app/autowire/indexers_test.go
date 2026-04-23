package autowire

// White-box tests for seedDefaultIndexers — same package so we can call the
// method directly without going through Run().

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	bazarrclient "pelicula-api/internal/clients/bazarr"
)

// idxSvc is a minimal ArrSvc implementation for indexer-seeding tests.
// It records POST paths and supports per-call error injection on ArrPost.
type idxSvc struct {
	// getResponses maps path → JSON body returned by ArrGet.
	getResponses map[string][]byte
	// postedPaths records every path passed to ArrPost, in order.
	postedPaths []string
}

func (s *idxSvc) ReloadKeys()                                      {}
func (s *idxSvc) SonarrRadarrKeys() (string, string)               { return "sonarr-key", "radarr-key" }
func (s *idxSvc) GetProwlarrKey() string                           { return "prowlarr-key" }
func (s *idxSvc) SetWired(_ bool)                                  {}
func (s *idxSvc) HTTPClient() *http.Client                         { return http.DefaultClient }
func (s *idxSvc) ConfigDir() string                                { return "/tmp" }
func (s *idxSvc) SetBazarrClient(_ string, _ *bazarrclient.Client) {}
func (s *idxSvc) BazarrClient() *bazarrclient.Client               { return nil }
func (s *idxSvc) ArrPut(_, _, _ string, _ any) ([]byte, error)     { return []byte("{}"), nil }

func (s *idxSvc) ArrGet(_, _, path string) ([]byte, error) {
	if b, ok := s.getResponses[path]; ok {
		return b, nil
	}
	return []byte("[]"), nil
}

func (s *idxSvc) ArrPost(_, _, path string, _ any) ([]byte, error) {
	s.postedPaths = append(s.postedPaths, path)
	return []byte("{}"), nil
}

// newIdxAutowirer builds a minimal Autowirer for indexer-seeding tests.
func newIdxAutowirer(svc ArrSvc, seed bool) *Autowirer {
	return &Autowirer{
		svc:           svc,
		urls:          URLs{Prowlarr: "http://prowlarr"},
		seedIndexers:  seed,
		invalidateIdx: func() {},
	}
}

// indexerListJSON encodes a list of indexer objects with only a "name" field.
func indexerListJSON(names ...string) []byte {
	items := make([]map[string]any, 0, len(names))
	for _, n := range names {
		items = append(items, map[string]any{"name": n})
	}
	b, _ := json.Marshal(items)
	return b
}

// schemaFor returns a trivial schema JSON for the given definition name.
func schemaFor(defName string) []byte {
	b, _ := json.Marshal(map[string]any{"name": defName, "definitionName": defName})
	return b
}

// schemaPath builds the GET path Prowlarr uses for a Cardigann schema.
func schemaPath(defName string) string {
	return "/api/v1/indexer/schema?type=Cardigann&definitionName=" + defName
}

// ── Tests ────────────────────────────────────────────────────────────────────

// TestSeedIndexers_AllMissing: empty existing list → all preset indexers are added.
func TestSeedIndexers_AllMissing(t *testing.T) {
	svc := &idxSvc{
		getResponses: map[string][]byte{
			"/api/v1/indexer": indexerListJSON(), // empty
		},
	}
	for _, name := range defaultIndexers {
		svc.getResponses[schemaPath(name)] = schemaFor(name)
	}

	a := newIdxAutowirer(svc, true)
	a.seedDefaultIndexers()

	if got := len(svc.postedPaths); got != len(defaultIndexers) {
		t.Errorf("expected %d POSTs, got %d", len(defaultIndexers), got)
	}
	for _, p := range svc.postedPaths {
		if p != "/api/v1/indexer" {
			t.Errorf("unexpected POST path: %q", p)
		}
	}
}

// TestSeedIndexers_AllPresent: all preset indexers already present → no POSTs.
func TestSeedIndexers_AllPresent(t *testing.T) {
	svc := &idxSvc{
		getResponses: map[string][]byte{
			"/api/v1/indexer": indexerListJSON(defaultIndexers...),
		},
	}

	a := newIdxAutowirer(svc, true)
	a.seedDefaultIndexers()

	if got := len(svc.postedPaths); got != 0 {
		t.Errorf("expected 0 POSTs, got %d: %v", got, svc.postedPaths)
	}
}

// TestSeedIndexers_PartialOverlap: only missing indexers are added.
func TestSeedIndexers_PartialOverlap(t *testing.T) {
	if len(defaultIndexers) < 2 {
		t.Skip("test requires at least 2 default indexers")
	}

	present := defaultIndexers[:1]
	missing := defaultIndexers[1:]

	svc := &idxSvc{
		getResponses: map[string][]byte{
			"/api/v1/indexer": indexerListJSON(present...),
		},
	}
	for _, name := range missing {
		svc.getResponses[schemaPath(name)] = schemaFor(name)
	}

	a := newIdxAutowirer(svc, true)
	a.seedDefaultIndexers()

	if got := len(svc.postedPaths); got != len(missing) {
		t.Errorf("expected %d POSTs, got %d", len(missing), got)
	}
}

// TestSeedIndexers_PostError: one POST fails → warning logged, others still added.
func TestSeedIndexers_PostError(t *testing.T) {
	if len(defaultIndexers) < 2 {
		t.Skip("test requires at least 2 default indexers")
	}

	base := &idxSvc{
		getResponses: map[string][]byte{
			"/api/v1/indexer": indexerListJSON(), // all missing
		},
	}
	for _, name := range defaultIndexers {
		base.getResponses[schemaPath(name)] = schemaFor(name)
	}

	errSvc := &errOnFirstPostSvc{idxSvc: base, failOnCall: 1}
	a := newIdxAutowirer(errSvc, true)
	a.seedDefaultIndexers()

	// One call should have returned an error; the loop must continue for the rest.
	if got := errSvc.postCallCount; got != len(defaultIndexers) {
		t.Errorf("expected %d POST attempts (including the failed one), got %d",
			len(defaultIndexers), got)
	}
}

// errOnFirstPostSvc wraps idxSvc and returns an error on the Nth POST call.
type errOnFirstPostSvc struct {
	*idxSvc
	failOnCall    int
	postCallCount int
}

func (s *errOnFirstPostSvc) ArrPost(baseURL, apiKey, path string, payload any) ([]byte, error) {
	s.postCallCount++
	if s.postCallCount == s.failOnCall {
		return nil, fmt.Errorf("injected POST error on call %d", s.postCallCount)
	}
	return s.idxSvc.ArrPost(baseURL, apiKey, path, payload)
}

// TestSeedIndexers_Disabled: SeedIndexers=false → no API calls at all.
func TestSeedIndexers_Disabled(t *testing.T) {
	getCalled := false
	svc := &trackGetSvc{
		idxSvc: &idxSvc{
			getResponses: map[string][]byte{
				"/api/v1/indexer": indexerListJSON(),
			},
		},
		onGet: func() { getCalled = true },
	}
	for _, name := range defaultIndexers {
		svc.idxSvc.getResponses[schemaPath(name)] = schemaFor(name)
	}

	a := newIdxAutowirer(svc, false)
	a.seedDefaultIndexers()

	if getCalled {
		t.Error("ArrGet should not have been called when SeedIndexers=false")
	}
	if got := len(svc.idxSvc.postedPaths); got != 0 {
		t.Errorf("ArrPost should not have been called when SeedIndexers=false, got %d calls", got)
	}
}

// trackGetSvc wraps idxSvc and calls onGet whenever ArrGet is invoked.
type trackGetSvc struct {
	*idxSvc
	onGet func()
}

func (s *trackGetSvc) ArrGet(baseURL, apiKey, path string) ([]byte, error) {
	s.onGet()
	return s.idxSvc.ArrGet(baseURL, apiKey, path)
}
