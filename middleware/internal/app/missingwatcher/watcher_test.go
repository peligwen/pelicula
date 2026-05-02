package missingwatcher

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	services "pelicula-api/internal/app/services"
	"pelicula-api/internal/config"
)

func TestSearchCooldown_FirstCallAllowed(t *testing.T) {
	c := newSearchCooldown()
	if !c.shouldSearch(1) {
		t.Fatal("expected first search to be allowed immediately")
	}
}

func TestSearchCooldown_SecondCallBlockedWithinCooldown(t *testing.T) {
	c := newSearchCooldown()
	c.shouldSearch(1)      // first call: allowed, records attempt=1
	if c.shouldSearch(1) { // second call: should be blocked (30min cooldown)
		t.Fatal("expected second search to be blocked within 30min cooldown")
	}
}

func TestSearchCooldown_CooldownTiersIncrease(t *testing.T) {
	c := newSearchCooldown()

	// Manually populate entries at various attempt counts to test tier lookup
	// without sleeping. Verify the tier durations are in the expected order.
	tiers := cooldownDurations
	if len(tiers) < 5 {
		t.Fatalf("expected at least 5 cooldown tiers, got %d", len(tiers))
	}
	if tiers[0] != 0 {
		t.Errorf("tier 0 should be 0 (immediate), got %v", tiers[0])
	}
	for i := 1; i < len(tiers); i++ {
		if tiers[i] <= tiers[i-1] {
			t.Errorf("tier %d (%v) should be greater than tier %d (%v)", i, tiers[i], i-1, tiers[i-1])
		}
	}
	_ = c
}

func TestSearchCooldown_ClearResetsEntry(t *testing.T) {
	c := newSearchCooldown()
	c.shouldSearch(1) // records attempt

	c.clear(1)

	if !c.shouldSearch(1) {
		t.Fatal("expected search to be allowed immediately after clear")
	}
}

func TestSearchCooldown_PastCooldownAllows(t *testing.T) {
	c := newSearchCooldown()
	c.shouldSearch(1) // attempt 1, sets lastSearched=now, next cooldown is 30min

	// Manually set lastSearched far in the past to simulate elapsed time
	c.mu.Lock()
	e := c.entries[1]
	e.lastSearched = time.Now().Add(-31 * time.Minute)
	c.entries[1] = e
	c.mu.Unlock()

	if !c.shouldSearch(1) {
		t.Fatal("expected search to be allowed after cooldown elapsed")
	}
}

func TestSearchCooldown_CapAtMaxTier(t *testing.T) {
	c := newSearchCooldown()
	maxTier := len(cooldownDurations) - 1

	// Drive attempts well past the last tier
	c.mu.Lock()
	c.entries[1] = cooldownEntry{
		lastSearched: time.Now().Add(-25 * time.Hour), // past even the 24h cap
		attempts:     maxTier + 5,
	}
	c.mu.Unlock()

	if !c.shouldSearch(1) {
		t.Fatal("expected search to be allowed after max-tier cooldown elapsed")
	}
}

// TestCooldown_ClearedWhenItemEntersQueue verifies that a cooldown entry in the
// blocked state becomes immediately searchable after clear() is called.
// Sequence: first call allowed → second call blocked (30min cooldown) → clear → allowed again.
func TestCooldown_ClearedWhenItemEntersQueue(t *testing.T) {
	c := newSearchCooldown()

	if !c.shouldSearch(1) {
		t.Fatal("first call: expected allowed")
	}
	if c.shouldSearch(1) {
		t.Fatal("second call: expected blocked within 30min cooldown")
	}
	c.clear(1)
	if !c.shouldSearch(1) {
		t.Fatal("after clear: expected allowed immediately")
	}
}

// newTestWatcher builds a Watcher pointed at the given httptest server URL,
// with a real *services.Clients that has the given API key set and is marked wired.
func newTestWatcher(serverURL, radarrKey, sonarrKey string) *Watcher {
	svc := services.New(&config.Config{}, "")
	svc.RadarrKey = radarrKey
	svc.SonarrKey = sonarrKey
	svc.SetWired(true)
	return &Watcher{
		Services:  svc,
		RadarrURL: serverURL,
		SonarrURL: serverURL,
		movie:     newSearchCooldown(),
		episode:   newSearchCooldown(),
	}
}

// TestSearchMissingMovies_ClearsAttemptsWhenQueued verifies that when a movie is
// present in the Radarr queue, its cooldown entry is cleared (attempts reset to 0).
func TestSearchMissingMovies_ClearsAttemptsWhenQueued(t *testing.T) {
	const movieID = 42

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/movie":
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": float64(movieID), "monitored": true, "hasFile": false, "isAvailable": true},
			})
		case "/api/v3/queue":
			json.NewEncoder(w).Encode(map[string]any{
				"totalRecords": 1,
				"records":      []map[string]any{{"movieId": float64(movieID)}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	w := newTestWatcher(srv.URL, "testkey", "")

	// Prime the cooldown so there's an entry to clear.
	w.movie.shouldSearch(movieID)

	w.searchMissingMovies(context.Background())

	w.movie.mu.Lock()
	_, exists := w.movie.entries[movieID]
	w.movie.mu.Unlock()

	if exists {
		t.Fatal("expected cooldown entry to be cleared when movie is queued")
	}
}

// TestSearchMissingMovies_ClearsAttemptsWhenHasFile verifies that when a movie
// has a file, its cooldown entry is cleared (the download settled).
func TestSearchMissingMovies_ClearsAttemptsWhenHasFile(t *testing.T) {
	const movieID = 99

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/movie":
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": float64(movieID), "monitored": true, "hasFile": true, "isAvailable": true},
			})
		case "/api/v3/queue":
			json.NewEncoder(w).Encode(map[string]any{
				"totalRecords": 0,
				"records":      []map[string]any{},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	w := newTestWatcher(srv.URL, "testkey", "")

	// Prime the cooldown so there's an entry to clear.
	w.movie.shouldSearch(movieID)

	w.searchMissingMovies(context.Background())

	w.movie.mu.Lock()
	_, exists := w.movie.entries[movieID]
	w.movie.mu.Unlock()

	if exists {
		t.Fatal("expected cooldown entry to be cleared when movie has a file")
	}
}

// TestRun_RespectsCtxCancel verifies that Run returns promptly when the context
// is cancelled, even when services are wired and the ticker would continue firing.
func TestRun_RespectsCtxCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve empty responses for any watcher fetches that fire before cancel.
		switch r.URL.Path {
		case "/api/v3/movie":
			json.NewEncoder(w).Encode([]map[string]any{})
		case "/api/v3/queue":
			json.NewEncoder(w).Encode(map[string]any{"totalRecords": 0, "records": []map[string]any{}})
		case "/api/v3/wanted/missing":
			json.NewEncoder(w).Encode(map[string]any{"records": []map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	w := newTestWatcher(srv.URL, "radarrkey", "sonarrkey")

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		w.Run(ctx, 50*time.Millisecond)
		close(done)
	}()

	// Give Run time to start the ticker loop, then cancel.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// expected: Run returned after ctx cancel
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Run did not return within 200ms after ctx cancel")
	}
}
