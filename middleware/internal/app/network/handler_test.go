package network_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"pelicula-api/internal/app/network"
	"pelicula-api/internal/clients/docker"
)

// fakeDocker is a test implementation of statsSource.
type fakeDocker struct {
	allowed map[string]bool
	stats   map[string]*docker.StatsResponse
	errs    map[string]error

	mu        sync.Mutex
	statCalls int // counts total Stats() invocations; guarded by mu
}

func (f *fakeDocker) AllowedNames() map[string]bool { return f.allowed }
func (f *fakeDocker) Stats(_ context.Context, name string) (*docker.StatsResponse, error) {
	f.mu.Lock()
	f.statCalls++
	f.mu.Unlock()
	if err, ok := f.errs[name]; ok && err != nil {
		return nil, err
	}
	return f.stats[name], nil
}

// statCallCount returns the total number of Stats invocations in a race-safe way.
func (f *fakeDocker) statCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statCalls
}

func fixedNow() time.Time {
	return time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
}

// TestServeStats_Success verifies the happy path: bytes are summed across
// interfaces and the response shape is correct.
func TestServeStats_Success(t *testing.T) {
	h := &network.Handler{
		Docker: &fakeDocker{
			allowed: map[string]bool{"sonarr": true, "radarr": true},
			stats: map[string]*docker.StatsResponse{
				"sonarr": {
					Read: fixedNow(),
					Networks: map[string]docker.NetIO{
						"eth0": {RxBytes: 1000, TxBytes: 500},
						"eth1": {RxBytes: 200, TxBytes: 100},
					},
				},
				"radarr": {
					Read: fixedNow(),
					Networks: map[string]docker.NetIO{
						"eth0": {RxBytes: 3000, TxBytes: 1500},
					},
				},
			},
			errs: map[string]error{},
		},
		VPNContainers: map[string]bool{},
		Now:           fixedNow,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/network", nil)
	h.ServeStats(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", ct)
	}

	var resp struct {
		Containers []struct {
			Name     string `json:"name"`
			BytesIn  uint64 `json:"bytes_in"`
			BytesOut uint64 `json:"bytes_out"`
		} `json:"containers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	byName := map[string]struct {
		BytesIn  uint64
		BytesOut uint64
	}{}
	for _, c := range resp.Containers {
		byName[c.Name] = struct {
			BytesIn  uint64
			BytesOut uint64
		}{c.BytesIn, c.BytesOut}
	}

	if s := byName["sonarr"]; s.BytesIn != 1200 || s.BytesOut != 600 {
		t.Errorf("sonarr: want in=1200 out=600, got in=%d out=%d", s.BytesIn, s.BytesOut)
	}
	if r := byName["radarr"]; r.BytesIn != 3000 || r.BytesOut != 1500 {
		t.Errorf("radarr: want in=3000 out=1500, got in=%d out=%d", r.BytesIn, r.BytesOut)
	}
}

// TestServeStats_VPNFlag verifies that containers in the VPN set get
// vpn_routed=true and others get false.
func TestServeStats_VPNFlag(t *testing.T) {
	h := &network.Handler{
		Docker: &fakeDocker{
			allowed: map[string]bool{"gluetun": true, "sonarr": true},
			stats: map[string]*docker.StatsResponse{
				"gluetun": {Read: fixedNow(), Networks: nil},
				"sonarr":  {Read: fixedNow(), Networks: nil},
			},
			errs: map[string]error{},
		},
		VPNContainers: map[string]bool{"gluetun": true},
		Now:           fixedNow,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/network", nil)
	h.ServeStats(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Containers []struct {
			Name      string `json:"name"`
			VPNRouted bool   `json:"vpn_routed"`
		} `json:"containers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	byName := map[string]bool{}
	for _, c := range resp.Containers {
		byName[c.Name] = c.VPNRouted
	}

	if !byName["gluetun"] {
		t.Error("expected gluetun vpn_routed=true")
	}
	if byName["sonarr"] {
		t.Error("expected sonarr vpn_routed=false")
	}
}

// TestServeStats_MethodNotAllowed verifies that non-GET methods return 405.
func TestServeStats_MethodNotAllowed(t *testing.T) {
	h := &network.Handler{
		Docker: &fakeDocker{
			allowed: map[string]bool{},
			stats:   map[string]*docker.StatsResponse{},
			errs:    map[string]error{},
		},
		Now: fixedNow,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/network", nil)
	h.ServeStats(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// TestServeStats_MethodNotAllowed_JSONShape verifies that the 405 error response
// uses Content-Type: application/json and the {"error":"..."} body shape (F16).
func TestServeStats_MethodNotAllowed_JSONShape(t *testing.T) {
	h := &network.Handler{
		Docker: &fakeDocker{
			allowed: map[string]bool{},
			stats:   map[string]*docker.StatsResponse{},
			errs:    map[string]error{},
		},
		Now: fixedNow,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/network", nil)
	h.ServeStats(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json (not plain text)", ct)
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["error"] == "" {
		t.Errorf("body = %v, want {\"error\":\"...\"}", resp)
	}
}

// TestServeStats_PerContainerError verifies that a Stats error for one service
// results in that service appearing with zero bytes, while other services are
// unaffected.
func TestServeStats_PerContainerError(t *testing.T) {
	h := &network.Handler{
		Docker: &fakeDocker{
			allowed: map[string]bool{"sonarr": true, "radarr": true},
			stats: map[string]*docker.StatsResponse{
				"radarr": {
					Read: fixedNow(),
					Networks: map[string]docker.NetIO{
						"eth0": {RxBytes: 9999, TxBytes: 4444},
					},
				},
			},
			errs: map[string]error{
				"sonarr": http.ErrServerClosed, // any non-nil error
			},
		},
		VPNContainers: map[string]bool{},
		Now:           fixedNow,
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/network", nil)
	h.ServeStats(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Containers []struct {
			Name     string `json:"name"`
			BytesIn  uint64 `json:"bytes_in"`
			BytesOut uint64 `json:"bytes_out"`
		} `json:"containers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	byName := map[string]struct {
		BytesIn  uint64
		BytesOut uint64
	}{}
	for _, c := range resp.Containers {
		byName[c.Name] = struct {
			BytesIn  uint64
			BytesOut uint64
		}{c.BytesIn, c.BytesOut}
	}

	if s := byName["sonarr"]; s.BytesIn != 0 || s.BytesOut != 0 {
		t.Errorf("sonarr should have 0/0 on error, got in=%d out=%d", s.BytesIn, s.BytesOut)
	}
	if r := byName["radarr"]; r.BytesIn != 9999 || r.BytesOut != 4444 {
		t.Errorf("radarr: want in=9999 out=4444, got in=%d out=%d", r.BytesIn, r.BytesOut)
	}
}

// TestServeStats_CacheHit verifies that a second request within the TTL window
// does not call Docker.Stats again — the cached body is served instead.
func TestServeStats_CacheHit(t *testing.T) {
	fd := &fakeDocker{
		allowed: map[string]bool{"sonarr": true},
		stats: map[string]*docker.StatsResponse{
			"sonarr": {Read: fixedNow(), Networks: map[string]docker.NetIO{
				"eth0": {RxBytes: 500, TxBytes: 250},
			}},
		},
		errs: map[string]error{},
	}
	h := &network.Handler{
		Docker:        fd,
		VPNContainers: map[string]bool{},
		Now:           fixedNow, // fixed time — every call returns the same "now"
	}

	// First request — populates the cache.
	rec1 := httptest.NewRecorder()
	h.ServeStats(rec1, httptest.NewRequest(http.MethodGet, "/api/pelicula/network", nil))
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request: expected 200, got %d", rec1.Code)
	}
	callsAfterFirst := fd.statCallCount()

	// Second request within the TTL window (same fixed Now).
	rec2 := httptest.NewRecorder()
	h.ServeStats(rec2, httptest.NewRequest(http.MethodGet, "/api/pelicula/network", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("second request: expected 200, got %d", rec2.Code)
	}

	if fd.statCallCount() != callsAfterFirst {
		t.Errorf("Docker.Stats called %d times across two requests (want %d — cache hit should skip re-fetch)",
			fd.statCallCount(), callsAfterFirst)
	}
}

// TestServeStats_ZeroContainers verifies that when Docker reports no allowed
// containers, the response is HTTP 200 with a containers array (empty or null).
func TestServeStats_ZeroContainers(t *testing.T) {
	h := &network.Handler{
		Docker: &fakeDocker{
			allowed: map[string]bool{},
			stats:   map[string]*docker.StatsResponse{},
			errs:    map[string]error{},
		},
		VPNContainers: map[string]bool{},
		Now:           fixedNow,
	}

	rec := httptest.NewRecorder()
	h.ServeStats(rec, httptest.NewRequest(http.MethodGet, "/api/pelicula/network", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Containers interface{} `json:"containers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// containers key must be present (value may be null or []).
	if !containsKey(rec.Body.Bytes(), "containers") {
		t.Error(`response body must contain "containers" key`)
	}
}

// containsKey is a minimal helper to assert a JSON object contains a given key.
func containsKey(data []byte, key string) bool {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}

// TestServeStats_AllContainersError verifies that when all containers return
// Stats errors, the response is still HTTP 200 with zero byte counts for each.
func TestServeStats_AllContainersError(t *testing.T) {
	h := &network.Handler{
		Docker: &fakeDocker{
			allowed: map[string]bool{"sonarr": true, "radarr": true},
			stats:   map[string]*docker.StatsResponse{},
			errs: map[string]error{
				"sonarr": http.ErrServerClosed,
				"radarr": http.ErrServerClosed,
			},
		},
		VPNContainers: map[string]bool{},
		Now:           fixedNow,
	}

	rec := httptest.NewRecorder()
	h.ServeStats(rec, httptest.NewRequest(http.MethodGet, "/api/pelicula/network", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 even when all containers error, got %d", rec.Code)
	}

	var resp struct {
		Containers []struct {
			Name     string `json:"name"`
			BytesIn  uint64 `json:"bytes_in"`
			BytesOut uint64 `json:"bytes_out"`
		} `json:"containers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, c := range resp.Containers {
		if c.BytesIn != 0 || c.BytesOut != 0 {
			t.Errorf("container %q: expected zero bytes on error, got in=%d out=%d",
				c.Name, c.BytesIn, c.BytesOut)
		}
	}
}
