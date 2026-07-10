package actions

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestActionRequestTimeout verifies the wait+margin/default-fallback math
// HandleCreate uses to compute its per-request deadline (MWA-13). Procula
// caps ?wait= at 10s; the old shared HTTPClient's fixed 10s Timeout left
// zero margin for wait=10, causing intermittent false 502s.
func TestActionRequestTimeout(t *testing.T) {
	cases := []struct {
		name string
		wait string
		want time.Duration
	}{
		{"absent", "", actionDefaultTimeout},
		{"zero", "0", actionDefaultTimeout},
		{"negative", "-1", actionDefaultTimeout},
		{"non-numeric", "5s", actionDefaultTimeout}, // procula itself only accepts bare integers
		{"typical", "5", 10 * time.Second},
		{"at procula's documented max", "10", 15 * time.Second}, // the exact MWA-13 case
		{"above procula's cap", "30", 35 * time.Second},         // middleware doesn't need to know procula clamps this
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := actionRequestTimeout(c.wait); got != c.want {
				t.Errorf("actionRequestTimeout(%q) = %v, want %v", c.wait, got, c.want)
			}
		})
	}
}

// TestHandleCreate_SurvivesSharedClientTimeout is the MWA-13 regression test:
// it proves HandleCreate's per-request deadline — not h.HTTPClient's fixed
// Timeout — governs how long a proxied call is allowed to run. h.HTTPClient
// is given a deliberately tiny Timeout (standing in for the old shared 10s
// Timeout, scaled down so the test runs in milliseconds instead of real
// seconds); requestTimeoutFor is overridden the same way to stand in for the
// new wait+margin deadline. The fake Procula sleeps for a duration that
// exceeds the tiny client Timeout but is comfortably inside the tiny
// per-request deadline — if HandleCreate still used h.HTTPClient.Do directly
// (the pre-fix code path), this would time out and return 502.
func TestHandleCreate_SurvivesSharedClientTimeout(t *testing.T) {
	const (
		staleSharedTimeout  = 20 * time.Millisecond  // stands in for the old 10s Client.Timeout
		newPerRequestBudget = 300 * time.Millisecond // stands in for the new wait+margin deadline
		fakeProculaDelay    = 120 * time.Millisecond // between the two — must survive only with the fix
	)

	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(fakeProculaDelay)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer fake.Close()

	h := New(&http.Client{Timeout: staleSharedTimeout}, fake.URL, "")
	h.requestTimeoutFor = func(string) time.Duration { return newPerRequestBudget }

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/?wait=10", nil)
	h.HandleCreate(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("HandleCreate status = %d, want 202 — a per-request deadline wider than the shared "+
			"client's Timeout must let this call complete instead of racing the old 502 (MWA-13); body: %s",
			rec.Code, rec.Body.String())
	}
}

// TestHandleRegistry_CacheTTLExpires verifies that a stale cache entry triggers
// a fresh upstream fetch. The clock is injected to advance past registryTTL.
func TestHandleRegistry_CacheTTLExpires(t *testing.T) {
	calls := 0
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"actions":[]}`)) //nolint:errcheck
	}))
	defer fake.Close()

	now := time.Now()
	h := New(http.DefaultClient, fake.URL, "")
	h.now = func() time.Time { return now }

	// Prime the cache with a first request.
	rec := httptest.NewRecorder()
	h.HandleRegistry(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if calls != 1 {
		t.Fatalf("upstream calls after prime = %d, want 1", calls)
	}

	// Advance the clock past the TTL so the cache entry is stale.
	now = now.Add(registryTTL + time.Second)

	rec2 := httptest.NewRecorder()
	h.HandleRegistry(rec2, httptest.NewRequest(http.MethodGet, "/", nil))
	if calls != 2 {
		t.Fatalf("upstream calls after TTL expiry = %d, want 2 (stale cache should re-fetch)", calls)
	}
	if got := rec2.Header().Get("X-Cache"); got == "hit" {
		t.Error("stale cache should not return X-Cache: hit")
	}
}
