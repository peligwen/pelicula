package procula

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestNewProculaClient_UserAgent verifies that newProculaClient injects the
// expected User-Agent header on outbound requests.
func TestNewProculaClient_UserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newProculaClient(5 * time.Second)
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	resp.Body.Close()

	want := "Pelicula/" + Version + " (+https://github.com/peligwen/pelicula)"
	if gotUA != want {
		t.Errorf("User-Agent = %q, want %q", gotUA, want)
	}
}

// TestNewProculaClient_Timeout verifies that newProculaClient respects the
// timeout by failing against a server that hangs past the deadline.
func TestNewProculaClient_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newProculaClient(50 * time.Millisecond)
	start := time.Now()
	_, err := client.Get(srv.URL)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 400*time.Millisecond {
		t.Errorf("client took %v to time out, want < 400ms", elapsed)
	}
}

// TestRetryHTTPPost_SuccessFirstAttempt verifies that retryHTTPPost returns nil
// when the server responds with 2xx on the first attempt.
func TestRetryHTTPPost_SuccessFirstAttempt(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	client := newProculaClient(5 * time.Second)
	body, _ := json.Marshal(map[string]string{"key": "value"})
	if err := retryHTTPPost(context.Background(), client, srv.URL, body, 3); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("expected 1 call, got %d", n)
	}
}

// TestRetryHTTPPost_RetriesOnFailure verifies that retryHTTPPost retries on
// 5xx responses and succeeds on the third attempt.
func TestRetryHTTPPost_RetriesOnFailure(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			http.Error(w, "temporary error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newProculaClient(5 * time.Second)
	body, _ := json.Marshal(map[string]string{"key": "value"})

	// Use a context with a generous timeout; retryHTTPPost waits attempt*2s between
	// attempts, so 3 attempts = up to 4s wait. Allow 30s.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := retryHTTPPost(ctx, client, srv.URL, body, 3); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if n := calls.Load(); n != 3 {
		t.Errorf("expected 3 calls, got %d", n)
	}
}

// TestRetryHTTPPost_ExhaustsAllAttempts verifies that retryHTTPPost returns an
// error after maxAttempts consecutive failures.
func TestRetryHTTPPost_ExhaustsAllAttempts(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "always failing", http.StatusBadGateway)
	}))
	defer srv.Close()

	client := newProculaClient(5 * time.Second)
	body := []byte(`{}`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := retryHTTPPost(ctx, client, srv.URL, body, 2)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("expected 2 calls, got %d", n)
	}
}

// TestRetryHTTPPost_ContextCancellation verifies that retryHTTPPost returns
// promptly when the context is cancelled during the retry backoff.
func TestRetryHTTPPost_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := newProculaClient(5 * time.Second)
	body := []byte(`{}`)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately after the first attempt would fail.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := retryHTTPPost(ctx, client, srv.URL, body, 5)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("retryHTTPPost took %v after ctx cancel, want < 2s", elapsed)
	}
}

// TestRetryHTTPPost_InvalidURL verifies that retryHTTPPost returns an error
// immediately when the URL is unreachable (no retry on dial error for 1 attempt).
func TestRetryHTTPPost_InvalidURL(t *testing.T) {
	client := newProculaClient(100 * time.Millisecond)
	err := retryHTTPPost(context.Background(), client, "http://127.0.0.1:1/bad", []byte(`{}`), 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	want := fmt.Sprintf("retryHTTPPost: failed after %d attempts", 1)
	if len(err.Error()) < len(want) || err.Error()[:len(want)] != want {
		t.Errorf("error = %q, want prefix %q", err.Error(), want)
	}
}

// TestRetryHTTPPost_NoRetryOn401 verifies that a 401 response causes an
// immediate return without any retry attempt.
func TestRetryHTTPPost_NoRetryOn401(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := newProculaClient(5 * time.Second)
	err := retryHTTPPost(context.Background(), client, srv.URL, []byte(`{}`), 3)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("expected exactly 1 request, got %d — 401 must not retry", n)
	}
}

// TestRetryHTTPPost_RetriesOn503 verifies that a 503 response is retried and
// that success on the second attempt returns nil.
func TestRetryHTTPPost_RetriesOn503(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := newProculaClient(5 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := retryHTTPPost(ctx, client, srv.URL, []byte(`{}`), 3); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("expected 2 calls (1 fail + 1 success), got %d", n)
	}
}

// countingReadCloser wraps an io.ReadCloser and counts total bytes read.
type countingReadCloser struct {
	rc io.ReadCloser
	n  *atomic.Int64
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func (c *countingReadCloser) Close() error { return c.rc.Close() }

// countingTransport wraps an http.RoundTripper and replaces resp.Body with a
// countingReadCloser so callers can assert that the body was fully drained.
type countingTransport struct {
	base http.RoundTripper
	n    *atomic.Int64
}

func (t *countingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if resp != nil {
		resp.Body = &countingReadCloser{rc: resp.Body, n: t.n}
	}
	return resp, err
}

// TestRetryHTTPPost_DrainsBody verifies that retryHTTPPost fully drains the
// response body on a 5xx response before closing it, preserving connection reuse.
func TestRetryHTTPPost_DrainsBody(t *testing.T) {
	const bodySize = 64 * 1024 // 64 KiB response body
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write(make([]byte, bodySize)) //nolint:errcheck
	}))
	defer srv.Close()

	var read atomic.Int64
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &countingTransport{
			base: http.DefaultTransport,
			n:    &read,
		},
	}

	// Single attempt so we get exactly one 502 and one drain.
	retryHTTPPost(context.Background(), client, srv.URL, []byte(`{}`), 1) //nolint:errcheck

	if got := read.Load(); got < bodySize {
		t.Errorf("body bytes read = %d, want ≥%d (body not fully drained)", got, bodySize)
	}
}
