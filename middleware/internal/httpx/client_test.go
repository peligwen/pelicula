package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRetryOn5xx(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "", "", 5*time.Second)
	c.Retry = RetryPolicy{MaxAttempts: 3, Delay: 1 * time.Millisecond}

	var out map[string]any
	if err := c.GetJSON(context.Background(), "/", &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestNoRetryOn4xx(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	c := New(srv.URL, "", "", 5*time.Second)
	c.Retry = RetryPolicy{MaxAttempts: 3, Delay: 1 * time.Millisecond}

	var out map[string]any
	err := c.GetJSON(context.Background(), "/", &out)
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if attempts != 1 {
		t.Errorf("expected 1 attempt (no retry on 4xx), got %d", attempts)
	}
}

func TestRetryExhausted(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	c := New(srv.URL, "", "", 5*time.Second)
	c.Retry = RetryPolicy{MaxAttempts: 2, Delay: 1 * time.Millisecond}

	var out map[string]any
	err := c.GetJSON(context.Background(), "/", &out)
	if err == nil {
		t.Fatal("expected error after retry exhaustion")
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
}

func TestUserAgentSetByDefault(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := New(srv.URL, "", "", time.Second)
	var out map[string]any
	if err := c.GetJSON(context.Background(), "/", &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(gotUA, "Pelicula/") {
		t.Errorf("expected User-Agent to start with Pelicula/, got %q", gotUA)
	}
}

func TestUserAgentRespectsCustomHTTPClient(t *testing.T) {
	type roundTripperFunc func(*http.Request) (*http.Response, error)
	var capturedUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := New(srv.URL, "", "", time.Second)
	// Override with a bare http.Client that has no transport; Go will use
	// http.DefaultTransport, which sets no User-Agent, so the header is absent.
	c.HTTPClient = &http.Client{}

	var out map[string]any
	if err := c.GetJSON(context.Background(), "/", &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.HasPrefix(capturedUA, "Pelicula/") {
		t.Errorf("custom HTTPClient should suppress uaTransport, but got Pelicula/ UA: %q", capturedUA)
	}
}

func TestUserAgentOverridable(t *testing.T) {
	prev := DefaultUserAgent
	t.Cleanup(func() { DefaultUserAgent = prev })
	DefaultUserAgent = "test/1.2.3"

	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := New(srv.URL, "", "", time.Second)
	var out map[string]any
	if err := c.GetJSON(context.Background(), "/", &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotUA != "test/1.2.3" {
		t.Errorf("expected User-Agent %q, got %q", "test/1.2.3", gotUA)
	}
}

func TestRedactPathSensitiveParams(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/search?apikey=secret123", "/search?apikey=REDACTED"},
		{"/search?ApiKey=secret123", "/search?ApiKey=REDACTED"},
		{"/search?api_key=secret", "/search?api_key=REDACTED"},
		{"/search?token=abc&q=foo", "/search?q=foo&token=REDACTED"},
		{"/search?q=foo", "/search?q=foo"},
		{"/no-query", "/no-query"},
	}
	for _, tt := range tests {
		got := redactPath(tt.path)
		if got != tt.want {
			t.Errorf("redactPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
