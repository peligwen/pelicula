package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
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
