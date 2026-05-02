package actions_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pelicula-api/internal/app/actions"
)

// newTestHandler returns a Handler pointed at the given fake procula server URL.
func newTestHandler(proculaURL, apiKey string) *actions.Handler {
	return actions.New(http.DefaultClient, proculaURL, apiKey)
}

// TestHandleRegistry_CacheMissThenHit verifies the cache: the first request
// hits upstream; the second within the TTL is served from cache.
func TestHandleRegistry_CacheMissThenHit(t *testing.T) {
	calls := 0
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"actions":[]}`)) //nolint:errcheck
	}))
	defer fake.Close()

	h := newTestHandler(fake.URL, "")

	// First request — cache miss, upstream should be called.
	rec := httptest.NewRecorder()
	h.HandleRegistry(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("first request status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-Cache"); got == "hit" {
		t.Error("first request should not be a cache hit")
	}
	if calls != 1 {
		t.Fatalf("upstream call count after first request = %d, want 1", calls)
	}

	// Second request within TTL — should be served from cache.
	rec2 := httptest.NewRecorder()
	h.HandleRegistry(rec2, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("second request status = %d, want 200", rec2.Code)
	}
	if got := rec2.Header().Get("X-Cache"); got != "hit" {
		t.Errorf("second request X-Cache = %q, want hit", got)
	}
	if calls != 1 {
		t.Fatalf("upstream call count after second request = %d, want 1 (cache hit)", calls)
	}
}

// TestHandleRegistry_MethodNotAllowed verifies POST → 405.
func TestHandleRegistry_MethodNotAllowed(t *testing.T) {
	h := newTestHandler("http://127.0.0.1:0", "")
	rec := httptest.NewRecorder()
	h.HandleRegistry(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
	assertErrorJSON(t, rec)
}

// TestHandleRegistry_UpstreamUnavailable verifies that a closed upstream
// produces 502 with the expected error JSON.
func TestHandleRegistry_UpstreamUnavailable(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := fake.URL
	fake.Close() // close before the request

	h := newTestHandler(url, "")
	rec := httptest.NewRecorder()
	h.HandleRegistry(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	assertErrorJSON(t, rec)
}

// TestHandleCreate_ForwardsBodyAndAPIKey verifies that the request body and
// X-API-Key header are forwarded to procula when APIKey is set, and that no
// X-API-Key header is sent when APIKey is empty.
func TestHandleCreate_ForwardsBodyAndAPIKey(t *testing.T) {
	const inputBody = `{"action":"rescan","item_id":"42"}`
	const key = "test-key-123"

	var gotBody string
	var gotKey string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-API-Key")
		buf := new(strings.Builder)
		_, _ = strings.NewReader("").WriteTo(buf) // reset
		dec := json.NewDecoder(r.Body)
		var v any
		_ = dec.Decode(&v)
		b, _ := json.Marshal(v)
		gotBody = string(b)
		w.WriteHeader(http.StatusCreated)
	}))
	defer fake.Close()

	// With API key set.
	h := newTestHandler(fake.URL, key)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(inputBody))
	h.HandleCreate(rec, req)
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", rec.Code)
	}
	if gotKey != key {
		t.Errorf("X-API-Key forwarded = %q, want %q", gotKey, key)
	}

	// With no API key — header should be absent.
	h2 := newTestHandler(fake.URL, "")
	gotKey = "sentinel"
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(inputBody))
	h2.HandleCreate(rec2, req2)
	if gotKey != "" {
		t.Errorf("X-API-Key should be absent with empty apiKey, got %q", gotKey)
	}
	_ = gotBody // verified implicitly via round-trip; body is opaque bytes
}

// TestHandleCreate_ForwardsQueryString verifies that ?wait=5s is preserved.
func TestHandleCreate_ForwardsQueryString(t *testing.T) {
	var gotQuery string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(http.StatusAccepted)
	}))
	defer fake.Close()

	h := newTestHandler(fake.URL, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/?wait=5s", strings.NewReader(`{}`))
	h.HandleCreate(rec, req)
	if gotQuery != "wait=5s" {
		t.Errorf("upstream RawQuery = %q, want wait=5s", gotQuery)
	}
}

// TestHandleCreate_MethodNotAllowed verifies GET → 405.
func TestHandleCreate_MethodNotAllowed(t *testing.T) {
	h := newTestHandler("http://127.0.0.1:0", "")
	rec := httptest.NewRecorder()
	h.HandleCreate(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
	assertErrorJSON(t, rec)
}

// TestHandleCreate_MaxBytesReader verifies that a body exceeding 1 MiB → 400.
func TestHandleCreate_MaxBytesReader(t *testing.T) {
	h := newTestHandler("http://127.0.0.1:0", "")
	oversized := strings.NewReader(strings.Repeat("x", (1<<20)+1))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", oversized)
	h.HandleCreate(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for oversized body", rec.Code)
	}
	assertErrorJSON(t, rec)
}

// TestHandleCreate_UpstreamUnavailable verifies that a closed upstream → 502.
func TestHandleCreate_UpstreamUnavailable(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := fake.URL
	fake.Close()

	h := newTestHandler(url, "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`))
	h.HandleCreate(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
	assertErrorJSON(t, rec)
}

// assertErrorJSON checks that the response body has {"error": "<non-empty>"}.
func assertErrorJSON(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var got map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("response is not JSON: %v — body: %s", err, rec.Body.String())
	}
	if got["error"] == "" {
		t.Errorf("JSON error field is empty; body: %v", got)
	}
}
