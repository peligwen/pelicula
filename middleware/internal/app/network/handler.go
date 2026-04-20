// Package network provides a thin proxy handler for the netcap sidecar's
// /connections endpoint. It degrades gracefully when netcap is unavailable.
package network

import (
	"context"
	"io"
	"net/http"
	"time"
)

// upstreamTimeout is the maximum time to wait for netcap to respond.
const upstreamTimeout = 3 * time.Second

// maxResponseBytes caps the upstream response to avoid unbounded memory use.
const maxResponseBytes = 1 << 20 // 1 MiB

// fallbackJSON is returned when netcap is unreachable or returns non-2xx.
var fallbackJSON = []byte(`{"connections":[],"error":"netcap unavailable"}`)

// Handler proxies netcap's /connections endpoint and degrades gracefully
// when the sidecar is unavailable. All fields are set at construction time
// and never mutated.
type Handler struct {
	// NetcapURL is the base URL of the netcap sidecar, e.g. "http://host.docker.internal:9191".
	NetcapURL string
	// HTTP is the shared client used for upstream requests.
	HTTP *http.Client
}

// ServeConnections handles GET /api/pelicula/network.
// It proxies netcap's /connections response on success, or returns a safe
// fallback JSON body when netcap is unavailable or returns an error status.
func (h *Handler) ServeConnections(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), upstreamTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.NetcapURL+"/connections", nil)
	if err != nil {
		h.writeFallback(w)
		return
	}

	resp, err := h.HTTP.Do(req)
	if err != nil {
		h.writeFallback(w)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		h.writeFallback(w)
		return
	}

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(http.StatusOK)
	io.Copy(w, io.LimitReader(resp.Body, maxResponseBytes)) //nolint:errcheck
}

func (h *Handler) writeFallback(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(fallbackJSON) //nolint:errcheck
}
