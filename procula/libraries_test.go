package procula

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestLoadLibraries_RetryOnTransientFailure: server returns 500 three times then
// valid JSON. The caller should receive the correct libraries (not defaults).
func TestLoadLibraries_RetryOnTransientFailure(t *testing.T) {
	var calls atomic.Int32
	libs := []ProculaLibrary{
		{Name: "Movies", Slug: "movies", Type: "movies", Arr: "radarr", Processing: "full"},
		{Name: "Anime", Slug: "anime", Type: "tvshows", Arr: "sonarr", Processing: "full"},
	}
	libsJSON, _ := json.Marshal(libs)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= 3 {
			http.Error(w, "transient error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(libsJSON) //nolint:errcheck
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Use a very short retry delay to keep the test fast.
	result, ok := fetchLibrariesWithRetryDelay(ctx, srv.URL, 1*time.Millisecond, 10)
	if !ok {
		t.Fatal("expected success after transient failures, got failure")
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 libraries, got %d", len(result))
	}
	if result[1].Slug != "anime" {
		t.Errorf("result[1].Slug = %q, want %q", result[1].Slug, "anime")
	}
	if calls.Load() < 4 {
		t.Errorf("expected at least 4 calls (3 failures + 1 success), got %d", calls.Load())
	}
}

// TestLoadLibraries_FallsBackAfterMaxRetries: always-500 server; defaults returned.
func TestLoadLibraries_FallsBackAfterMaxRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "always failing", http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, ok := fetchLibrariesWithRetryDelay(ctx, srv.URL, 1*time.Millisecond, 3)
	if ok {
		t.Fatal("expected failure after max retries, got success")
	}
	if result != nil {
		t.Errorf("expected nil result on failure, got %v", result)
	}
}

// fetchLibrariesWithRetryDelay is the testable variant of fetchLibrariesWithRetry
// with injectable delay and max-attempts so tests run fast.
func fetchLibrariesWithRetryDelay(ctx context.Context, peliculaAPI string, delay time.Duration, maxAttempts int) ([]ProculaLibrary, bool) {
	client := &http.Client{Timeout: 5 * time.Second}
	apiURL := peliculaAPI + "/api/pelicula/libraries"

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		libs, err := fetchLibrariesOnce(client, apiURL)
		if err == nil && libs != nil {
			return libs, true
		}

		if attempt == maxAttempts {
			break
		}

		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(delay):
		}
	}
	return nil, false
}

// TestRefreshLibrariesGoroutine_StopsOnCtxCancel verifies that runLibraryRefresh
// exits when ctx is cancelled, without waiting for the next 5-minute tick.
func TestRefreshLibrariesGoroutine_StopsOnCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		runLibraryRefresh(ctx, "http://localhost:0")
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("runLibraryRefresh did not exit within 1s after ctx cancel")
	}
}
