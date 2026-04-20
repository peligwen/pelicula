package network_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pelicula-api/internal/app/network"
)

// newHandler returns a Handler pointed at the given server URL.
func newHandler(serverURL string) *network.Handler {
	return &network.Handler{
		NetcapURL: serverURL,
		HTTP:      &http.Client{},
	}
}

// TestServeConnections_ProxySuccess verifies that a 2xx response from netcap
// is streamed through with the original Content-Type preserved.
func TestServeConnections_ProxySuccess(t *testing.T) {
	const payload = `{"connections":[{"pid":42,"comm":"curl"}]}`

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/connections" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, payload)
	}))
	defer upstream.Close()

	h := newHandler(upstream.URL)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/network", nil)
	h.ServeConnections(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", got)
	}
	if got := rec.Body.String(); got != payload {
		t.Errorf("expected body %q, got %q", payload, got)
	}
}

// TestServeConnections_Unreachable verifies graceful degradation when netcap
// is not reachable at all.
func TestServeConnections_Unreachable(t *testing.T) {
	// Point at a server that is immediately closed — guaranteed connection refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // close before the request

	h := newHandler(url)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/network", nil)
	h.ServeConnections(rec, req)

	assertFallback(t, rec)
}

// TestServeConnections_NonTwoXX verifies graceful degradation when netcap
// returns a non-2xx status code.
func TestServeConnections_NonTwoXX(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, "boom")
	}))
	defer upstream.Close()

	h := newHandler(upstream.URL)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/network", nil)
	h.ServeConnections(rec, req)

	assertFallback(t, rec)
}

// TestServeConnections_MethodNotAllowed verifies that non-GET methods return 405.
func TestServeConnections_MethodNotAllowed(t *testing.T) {
	h := newHandler("http://unused")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/network", nil)
	h.ServeConnections(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

// TestServeConnections_BodyCapped verifies that responses larger than 1 MiB
// are truncated to exactly 1 MiB by the LimitReader.
func TestServeConnections_BodyCapped(t *testing.T) {
	const oneMiB = 1 << 20
	bigBody := bytes.Repeat([]byte("x"), oneMiB+1024) // 1 MiB + 1 KiB

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		w.Write(bigBody)
	}))
	defer upstream.Close()

	h := newHandler(upstream.URL)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/network", nil)
	h.ServeConnections(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	got := rec.Body.Len()
	if got > oneMiB {
		t.Errorf("response body %d bytes exceeds 1 MiB cap", got)
	}
	if got != oneMiB {
		t.Errorf("expected exactly 1 MiB (%d bytes), got %d bytes", oneMiB, got)
	}
}

// assertFallback checks that the recorder contains the graceful degradation response.
func assertFallback(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for fallback, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"connections":[]`) {
		t.Errorf("expected fallback body with empty connections, got %q", body)
	}
	if !strings.Contains(body, `"error":"netcap unavailable"`) {
		t.Errorf("expected fallback error message, got %q", body)
	}
}
