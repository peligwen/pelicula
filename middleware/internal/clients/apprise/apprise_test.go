package apprise

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pelicula-api/internal/httpx"
)

func writeNotifConfig(t *testing.T, dir string, cfg notifConfig) {
	t.Helper()
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	subdir := filepath.Join(dir, "procula")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "notifications.json"), b, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func newTestClient(t *testing.T, endpoint, configDir string) *Client {
	t.Helper()
	c := New(endpoint, configDir)
	c.base.Retry.Delay = 1 * time.Millisecond
	return c
}

// TestNotify_PartialFailureContinues: the apprise container returns 503 for the
// first POST (url1 payload) and 200 for the second (url2 payload). Both POSTs
// go to the same server (the single apprise container); the urls field in the
// JSON body identifies the destination channel. The test asserts that url2 still
// receives its POST even though url1's POST exhausted its retries with 503.
func TestNotify_PartialFailureContinues(t *testing.T) {
	var calls []string
	var callCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		json.NewDecoder(r.Body).Decode(&payload) //nolint:errcheck
		u, _ := payload["urls"].(string)
		calls = append(calls, u)
		callCount++
		// Reject all attempts for url1 with 503; accept url2.
		if strings.Contains(u, "chan1") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeNotifConfig(t, dir, notifConfig{
		Mode:        "apprise",
		AppriseURLs: []string{"apprise://chan1", "apprise://chan2"},
	})

	c := newTestClient(t, srv.URL, dir)
	c.Notify("test title", "test body")

	found2 := false
	for _, u := range calls {
		if u == "apprise://chan2" {
			found2 = true
		}
	}
	if !found2 {
		t.Errorf("second URL never received a POST despite first failing; calls: %v", calls)
	}
}

func TestNotify_DisabledMode(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeNotifConfig(t, dir, notifConfig{
		Mode:        "internal",
		AppriseURLs: []string{"apprise://chan"},
	})

	c := newTestClient(t, srv.URL, dir)
	c.Notify("hello", "world")

	if n := callCount.Load(); n != 0 {
		t.Errorf("expected 0 HTTP calls for disabled mode, got %d", n)
	}
}

func TestNotify_NoURLs(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeNotifConfig(t, dir, notifConfig{
		Mode:        "apprise",
		AppriseURLs: []string{},
	})

	c := newTestClient(t, srv.URL, dir)
	c.Notify("hello", "world")

	if n := callCount.Load(); n != 0 {
		t.Errorf("expected 0 HTTP calls with no URLs configured, got %d", n)
	}
}

func TestNotify_UserAgentSet(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Must set DefaultUserAgent before constructing the client because uaTransport
	// captures the value by copy at New() time.
	prev := httpx.DefaultUserAgent
	t.Cleanup(func() { httpx.DefaultUserAgent = prev })
	httpx.DefaultUserAgent = "Pelicula/test"

	dir := t.TempDir()
	writeNotifConfig(t, dir, notifConfig{
		Mode:        "apprise",
		AppriseURLs: []string{"apprise://test"},
	})

	c := newTestClient(t, srv.URL, dir)
	c.Notify("title", "body")

	if !strings.HasPrefix(gotUA, "Pelicula/") {
		t.Errorf("expected User-Agent to start with Pelicula/, got %q", gotUA)
	}
}

func TestNotify_Retries5xx(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeNotifConfig(t, dir, notifConfig{
		Mode:        "apprise",
		AppriseURLs: []string{"apprise://chan"},
	})

	c := newTestClient(t, srv.URL, dir)
	c.Notify("retry test", "body")

	if n := attempts.Load(); n < 3 {
		t.Errorf("expected at least 3 attempts (retry on 5xx), got %d", n)
	}
}
