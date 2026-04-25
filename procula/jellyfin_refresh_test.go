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
	t.Setenv("JELLYFIN_REFRESH_DEBOUNCE_MS", "150")
	t.Setenv("PROCULA_API_KEY", "")

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
// by setting JELLYFIN_REFRESH_DEBOUNCE_MS=0 (immediate, synchronous POST).
func TestScheduleJellyfinRefresh_ZeroBypassesDebounce(t *testing.T) {
	resetRefreshState(t)
	t.Setenv("JELLYFIN_REFRESH_DEBOUNCE_MS", "0")
	t.Setenv("PROCULA_API_KEY", "")

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
	t.Setenv("JELLYFIN_REFRESH_DEBOUNCE_MS", "60000") // long enough that the timer can't naturally fire during the test
	t.Setenv("PROCULA_API_KEY", "")

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
	t.Setenv("JELLYFIN_REFRESH_DEBOUNCE_MS", "5000")
	t.Setenv("PROCULA_API_KEY", "")

	apiURL, count := startCountingServer(t)
	_ = apiURL // server exists but no schedule call was made

	FlushJellyfinRefresh()

	if n := count.Load(); n != 0 {
		t.Errorf("got %d POSTs after no-op flush, want 0", n)
	}
}

// TestRefreshDebounceDelay_DefaultsTo5s verifies the default debounce window.
func TestRefreshDebounceDelay_DefaultsTo5s(t *testing.T) {
	t.Setenv("JELLYFIN_REFRESH_DEBOUNCE_MS", "")
	if got := refreshDebounceDelay(); got != 5*time.Second {
		t.Errorf("default debounce = %v, want 5s", got)
	}
}

// TestRefreshDebounceDelay_InvalidFallsBackToDefault verifies we don't panic
// or fire immediately on a malformed env value.
func TestRefreshDebounceDelay_InvalidFallsBackToDefault(t *testing.T) {
	t.Setenv("JELLYFIN_REFRESH_DEBOUNCE_MS", "not-a-number")
	if got := refreshDebounceDelay(); got != 5*time.Second {
		t.Errorf("invalid debounce = %v, want 5s default", got)
	}
}
