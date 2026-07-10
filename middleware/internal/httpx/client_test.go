package httpx

import (
	"context"
	"errors"
	"io"
	"net"
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

// ── MWA-15: POST retry policy ─────────────────────────────────────────────────

// countingTransport is a RoundTripper that returns queued errors before
// delegating to a success response. It lets tests simulate specific
// transport-error classes (dial failure vs connection reset) that are hard
// to produce deterministically with a real httptest server.
type countingTransport struct {
	calls  int
	errs   []error // errs[i] returned on call i; calls beyond len(errs) succeed
	status int     // status for successful calls (default 200)
}

func (t *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	i := t.calls
	t.calls++
	if i < len(t.errs) && t.errs[i] != nil {
		return nil, t.errs[i]
	}
	status := t.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    r,
	}, nil
}

func newTransportClient(rt http.RoundTripper) *Client {
	return &Client{
		BaseURL:    "http://arr.test",
		HTTPClient: &http.Client{Transport: rt},
		Retry:      RetryPolicy{MaxAttempts: 3, Delay: 1 * time.Millisecond},
	}
}

func dialErr() error {
	return &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connect: connection refused")}
}

func resetErr() error {
	return &net.OpError{Op: "read", Net: "tcp", Err: errors.New("read: connection reset by peer")}
}

// TestPostRetriesOnDialError verifies that a dial-phase failure — where the
// request provably never reached the server — IS retried for POSTs.
func TestPostRetriesOnDialError(t *testing.T) {
	rt := &countingTransport{errs: []error{dialErr()}}
	c := newTransportClient(rt)

	if err := c.PostJSON(context.Background(), "/api/v3/rootfolder", map[string]any{"path": "/movies"}, nil); err != nil {
		t.Fatalf("expected success after dial-error retry, got: %v", err)
	}
	if rt.calls != 2 {
		t.Errorf("calls = %d, want 2 (1 dial failure + 1 retry)", rt.calls)
	}
}

// TestPostNoRetryOnConnectionReset verifies that a post-send transport error
// (connection reset while reading the response) is NOT retried for POSTs —
// the server may already have committed the create, and a retry would
// duplicate it (MWA-15 regression).
func TestPostNoRetryOnConnectionReset(t *testing.T) {
	rt := &countingTransport{errs: []error{resetErr()}}
	c := newTransportClient(rt)

	err := c.PostJSON(context.Background(), "/api/v3/rootfolder", map[string]any{"path": "/movies"}, nil)
	if err == nil {
		t.Fatal("expected error on connection reset, got nil")
	}
	if rt.calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry after the request may have reached the server)", rt.calls)
	}
}

// TestPostStillRetriesOn5xx pins that a 5xx response keeps retrying for
// POSTs: it proves the request was processed and rejected by the backend
// (no intermediary proxy in this stack), so a retry cannot duplicate a
// committed create. This is the deliberate asymmetry with post-send
// transport errors, where the outcome is unknowable.
func TestPostStillRetriesOn5xx(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := New(srv.URL, "", "", 5*time.Second)
	c.Retry = RetryPolicy{MaxAttempts: 3, Delay: 1 * time.Millisecond}

	if err := c.PostJSON(context.Background(), "/", map[string]any{}, nil); err != nil {
		t.Fatalf("expected success after 5xx retries, got: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3 (POST retries 5xx)", attempts)
	}
}

// TestRawPostRetryPolicyMatchesPostJSON verifies RawPost applies the same
// POST policy: dial errors and 5xx retried, post-send resets not.
func TestRawPostRetryPolicyMatchesPostJSON(t *testing.T) {
	t.Run("dial error retried", func(t *testing.T) {
		rt := &countingTransport{errs: []error{dialErr()}}
		c := newTransportClient(rt)
		if _, err := c.RawPost(context.Background(), "/api/v1/applications", map[string]any{}); err != nil {
			t.Fatalf("expected success after dial-error retry, got: %v", err)
		}
		if rt.calls != 2 {
			t.Errorf("calls = %d, want 2", rt.calls)
		}
	})

	t.Run("reset not retried", func(t *testing.T) {
		rt := &countingTransport{errs: []error{resetErr()}}
		c := newTransportClient(rt)
		if _, err := c.RawPost(context.Background(), "/api/v1/applications", map[string]any{}); err == nil {
			t.Fatal("expected error on connection reset, got nil")
		}
		if rt.calls != 1 {
			t.Errorf("calls = %d, want 1", rt.calls)
		}
	})

	t.Run("5xx retried until exhausted", func(t *testing.T) {
		rt := &countingTransport{status: http.StatusInternalServerError}
		c := newTransportClient(rt)
		if _, err := c.RawPost(context.Background(), "/api/v1/applications", map[string]any{}); err == nil {
			t.Fatal("expected error after 5xx retry exhaustion, got nil")
		}
		if rt.calls != 3 {
			t.Errorf("calls = %d, want 3 (MaxAttempts)", rt.calls)
		}
	})
}

// TestGetStillRetriesOnTransportError pins the idempotent-request behavior:
// GETs keep retrying on any transport error class, including post-send resets.
func TestGetStillRetriesOnTransportError(t *testing.T) {
	rt := &countingTransport{errs: []error{resetErr()}}
	c := newTransportClient(rt)

	var out map[string]any
	if err := c.GetJSON(context.Background(), "/api/v3/movie", &out); err != nil {
		t.Fatalf("expected success after reset retry on GET, got: %v", err)
	}
	if rt.calls != 2 {
		t.Errorf("calls = %d, want 2 (GET retries resets)", rt.calls)
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
