// actions.go — proxies for POST /api/pelicula/actions and the cached
// registry endpoint.
package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"pelicula-api/httputil"
	"strings"
	"sync"
	"time"
)

type actionRegistryCache struct {
	mu        sync.Mutex
	lastFetch time.Time
	body      []byte
}

var registryCache actionRegistryCache

const registryTTL = 60 * time.Second

// handleActionsRegistry proxies GET /api/procula/actions/registry with a
// 60-second in-memory cache.
func handleActionsRegistry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	registryCache.mu.Lock()
	defer registryCache.mu.Unlock()
	if len(registryCache.body) > 0 && time.Since(registryCache.lastFetch) < registryTTL {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "hit")
		w.Write(registryCache.body) //nolint:errcheck
		return
	}
	resp, err := services.HTTPClient().Get(proculaURL + "/api/procula/actions/registry")
	if err != nil {
		httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		registryCache.body = body
		registryCache.lastFetch = time.Now()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body) //nolint:errcheck
}

// handleActionsCreate proxies POST /api/procula/actions, forwarding the body
// and any ?wait= query param unchanged. PROCULA_API_KEY is injected.
func handleActionsCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httputil.WriteError(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	target := proculaURL + "/api/procula/actions"
	if q := r.URL.RawQuery; q != "" {
		target += "?" + q
	}
	upstream, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		httputil.WriteError(w, "build request", http.StatusInternalServerError)
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(os.Getenv("PROCULA_API_KEY")); key != "" {
		upstream.Header.Set("X-API-Key", key)
	}
	resp, err := services.HTTPClient().Do(upstream)
	if err != nil {
		httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}
