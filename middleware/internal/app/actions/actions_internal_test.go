package actions

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

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
