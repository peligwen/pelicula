package procula

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// resetRefreshState clears the package-level debouncer between tests so they
// don't leak timers/state into each other.
func resetRefreshState(t *testing.T) {
	t.Helper()
	refreshMu.Lock()
	if refreshTimer != nil {
		refreshTimer.Stop()
		refreshTimer = nil
	}
	refreshTarget = ""
	refreshMu.Unlock()
}

func startCountingServer(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	var count atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/pelicula/jellyfin/refresh" {
			count.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	return ts.URL, &count
}

// TestScheduleJellyfinRefresh_DebouncesBurst verifies that a rapid burst of
// schedule calls collapses into a single POST after the debounce window.
func TestScheduleJellyfinRefresh_DebouncesBurst(t *testing.T) {
	resetRefreshState(t)
	old := refreshDebounceMs
	SetRefreshDebounceForTest(150 * time.Millisecond)
	t.Cleanup(func() { SetRefreshDebounceForTest(old) })
	oldKey := proculaAPIKey
	proculaAPIKey = ""
	t.Cleanup(func() { proculaAPIKey = oldKey })

	apiURL, count := startCountingServer(t)

	for i := 0; i < 10; i++ {
		if err := scheduleJellyfinRefresh(apiURL); err != nil {
			t.Fatalf("scheduleJellyfinRefresh: %v", err)
		}
	}

	// During the debounce window, no POST should have fired yet.
	if n := count.Load(); n != 0 {
		t.Errorf("got %d POSTs during debounce window, want 0", n)
	}

	// Wait past the debounce window; exactly one POST should fire.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if count.Load() == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := count.Load(); n != 1 {
		t.Errorf("got %d POSTs after debounce, want exactly 1", n)
	}

	// Allow extra grace; still must remain at 1 (no rogue extra fires).
	time.Sleep(300 * time.Millisecond)
	if n := count.Load(); n != 1 {
		t.Errorf("got %d POSTs after grace period, want still 1", n)
	}
}

// TestScheduleJellyfinRefresh_ZeroBypassesDebounce verifies tests can opt out
// by setting the debounce package var to 0 (immediate, synchronous POST).
func TestScheduleJellyfinRefresh_ZeroBypassesDebounce(t *testing.T) {
	resetRefreshState(t)
	old := refreshDebounceMs
	SetRefreshDebounceForTest(0)
	t.Cleanup(func() { SetRefreshDebounceForTest(old) })
	oldKey := proculaAPIKey
	proculaAPIKey = ""
	t.Cleanup(func() { proculaAPIKey = oldKey })

	apiURL, count := startCountingServer(t)

	for i := 0; i < 3; i++ {
		if err := scheduleJellyfinRefresh(apiURL); err != nil {
			t.Fatalf("scheduleJellyfinRefresh: %v", err)
		}
	}
	if n := count.Load(); n != 3 {
		t.Errorf("got %d POSTs with debounce=0, want 3 (one per call)", n)
	}
}

// TestFlushJellyfinRefresh_FiresPending verifies a pending debounced refresh
// is dispatched synchronously when Flush is called (e.g. on shutdown).
func TestFlushJellyfinRefresh_FiresPending(t *testing.T) {
	resetRefreshState(t)
	old := refreshDebounceMs
	SetRefreshDebounceForTest(60000 * time.Millisecond) // long enough that the timer can't naturally fire during the test
	t.Cleanup(func() { SetRefreshDebounceForTest(old) })
	oldKey := proculaAPIKey
	proculaAPIKey = ""
	t.Cleanup(func() { proculaAPIKey = oldKey })

	apiURL, count := startCountingServer(t)

	if err := scheduleJellyfinRefresh(apiURL); err != nil {
		t.Fatalf("scheduleJellyfinRefresh: %v", err)
	}
	if n := count.Load(); n != 0 {
		t.Fatalf("got %d POSTs before flush, want 0", n)
	}

	FlushJellyfinRefresh()

	if n := count.Load(); n != 1 {
		t.Errorf("got %d POSTs after flush, want 1", n)
	}
}

// TestFlushJellyfinRefresh_NoPendingIsNoop verifies Flush is safe when nothing
// is queued.
func TestFlushJellyfinRefresh_NoPendingIsNoop(t *testing.T) {
	resetRefreshState(t)
	old := refreshDebounceMs
	SetRefreshDebounceForTest(5000 * time.Millisecond)
	t.Cleanup(func() { SetRefreshDebounceForTest(old) })
	oldKey := proculaAPIKey
	proculaAPIKey = ""
	t.Cleanup(func() { proculaAPIKey = oldKey })

	apiURL, count := startCountingServer(t)
	_ = apiURL // server exists but no schedule call was made

	FlushJellyfinRefresh()

	if n := count.Load(); n != 0 {
		t.Errorf("got %d POSTs after no-op flush, want 0", n)
	}
}

// TestParseRefreshDebounceMs_DefaultsTo5s verifies the default debounce window.
func TestParseRefreshDebounceMs_DefaultsTo5s(t *testing.T) {
	if got := parseRefreshDebounceMs(""); got != 5*time.Second {
		t.Errorf("default debounce = %v, want 5s", got)
	}
}

// TestParseRefreshDebounceMs_InvalidFallsBackToDefault verifies we don't panic
// or fire immediately on a malformed env value.
func TestParseRefreshDebounceMs_InvalidFallsBackToDefault(t *testing.T) {
	if got := parseRefreshDebounceMs("not-a-number"); got != 5*time.Second {
		t.Errorf("invalid debounce = %v, want 5s default", got)
	}
}

// TestScheduleJellyfinRefresh_NoEnvOnHotPath verifies that doJellyfinRefresh
// uses the proculaAPIKey package var and ignores PROCULA_API_KEY in the environment.
// Setting PROCULA_API_KEY=wrong while proculaAPIKey="" (disabled) must result in
// no X-API-Key header — confirming the env is never re-read on the hot path.
func TestScheduleJellyfinRefresh_NoEnvOnHotPath(t *testing.T) {
	resetRefreshState(t)
	// Set the env to a sentinel that would cause auth failures if read.
	t.Setenv("PROCULA_API_KEY", "env-sentinel-should-not-be-read")

	// The package var is empty (auth disabled) — the server must see no key header.
	oldKey := proculaAPIKey
	proculaAPIKey = ""
	t.Cleanup(func() { proculaAPIKey = oldKey })

	old := refreshDebounceMs
	SetRefreshDebounceForTest(0) // synchronous: no timer goroutines
	t.Cleanup(func() { SetRefreshDebounceForTest(old) })

	var sawKey string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/pelicula/jellyfin/refresh" {
			sawKey = r.Header.Get("X-API-Key")
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)

	if err := scheduleJellyfinRefresh(ts.URL); err != nil {
		t.Fatalf("scheduleJellyfinRefresh: %v", err)
	}
	if sawKey != "" {
		t.Errorf("X-API-Key header = %q, want empty (env value must not be read on hot path)", sawKey)
	}
}
