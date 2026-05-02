// Package network provides per-container bandwidth stats for the admin dashboard.
// It queries the Docker socket proxy for one-shot stats and aggregates rx/tx bytes
// per container.
package network

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"pelicula-api/httputil"
	"pelicula-api/internal/clients/docker"
)

// DefaultVPNContainers is the static set of service names whose traffic is
// routed through the VPN tunnel (gluetun network namespace).
var DefaultVPNContainers = map[string]bool{
	"gluetun":     true,
	"qbittorrent": true,
	"prowlarr":    true,
}

// statsSource is the narrow interface the handler requires from the docker client.
type statsSource interface {
	Stats(ctx context.Context, name string) (*docker.StatsResponse, error)
	AllowedNames() map[string]bool
}

// containerStats is the per-container bandwidth row in the response.
type containerStats struct {
	Name      string `json:"name"`
	BytesIn   uint64 `json:"bytes_in"`
	BytesOut  uint64 `json:"bytes_out"`
	VPNRouted bool   `json:"vpn_routed"`
}

// response is the JSON body returned by ServeStats.
type response struct {
	Containers []containerStats `json:"containers"`
	AsOf       time.Time        `json:"as_of"`
}

// cachedResponse is used for the short-lived TTL cache.
type cachedResponse struct {
	body      []byte
	fetchedAt time.Time
}

const cacheTTL = 10 * time.Second

// Handler serves GET /api/pelicula/network with per-container bandwidth stats.
type Handler struct {
	Docker        statsSource
	VPNContainers map[string]bool
	Now           func() time.Time // injectable for tests; defaults to time.Now

	mu    sync.Mutex
	cache *cachedResponse
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Handler) vpnSet() map[string]bool {
	if h.VPNContainers != nil {
		return h.VPNContainers
	}
	return DefaultVPNContainers
}

// ServeStats handles GET /api/pelicula/network.
// Returns JSON {containers:[{name,bytes_in,bytes_out,vpn_routed}...],as_of}.
func (h *Handler) ServeStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	now := h.now()

	// Serve from cache if fresh.
	h.mu.Lock()
	if h.cache != nil && now.Sub(h.cache.fetchedAt) < cacheTTL {
		body := h.cache.body
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write(body) //nolint:errcheck
		return
	}
	h.mu.Unlock()

	names := h.Docker.AllowedNames()
	vpn := h.vpnSet()

	type result struct {
		name  string
		stats *docker.StatsResponse
		err   error
	}

	results := make([]result, 0, len(names))
	var wg sync.WaitGroup
	ch := make(chan result, len(names))

	ctx := r.Context()
	for name := range names {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			s, err := h.Docker.Stats(ctx, n)
			ch <- result{name: n, stats: s, err: err}
		}(name)
	}

	wg.Wait()
	close(ch)
	for r := range ch {
		results = append(results, r)
	}

	containers := make([]containerStats, 0, len(results))
	for _, res := range results {
		var bytesIn, bytesOut uint64
		if res.err == nil && res.stats != nil && res.stats.Networks != nil {
			for _, iface := range res.stats.Networks {
				bytesIn += iface.RxBytes
				bytesOut += iface.TxBytes
			}
		}
		containers = append(containers, containerStats{
			Name:      res.name,
			BytesIn:   bytesIn,
			BytesOut:  bytesOut,
			VPNRouted: vpn[res.name],
		})
	}

	sort.Slice(containers, func(i, j int) bool {
		return containers[i].Name < containers[j].Name
	})

	resp := response{
		Containers: containers,
		AsOf:       now,
	}

	body, err := json.Marshal(resp)
	if err != nil {
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}

	h.mu.Lock()
	h.cache = &cachedResponse{body: body, fetchedAt: now}
	h.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Write(body) //nolint:errcheck
}
