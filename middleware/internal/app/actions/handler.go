// Package actions proxies action-bus requests to procula and caches the
// registry endpoint.
package actions

import (
	"bytes"
	"io"
	"net/http"
	"pelicula-api/httputil"
	"sync"
	"time"
)

const registryTTL = 60 * time.Second

// actionCache is an unexported in-memory cache for the registry response.
type actionCache struct {
	mu        sync.Mutex
	lastFetch time.Time
	body      []byte
}

// Handler proxies GET /api/procula/actions/registry and POST /api/procula/actions.
type Handler struct {
	HTTPClient *http.Client
	ProculaURL string
	APIKey     string // pre-resolved from env at construction; not re-read per request
	cache      actionCache
}

// New constructs a Handler. apiKey should be resolved from the environment at
// startup (e.g. strings.TrimSpace(os.Getenv("PROCULA_API_KEY"))).
func New(httpClient *http.Client, proculaURL, apiKey string) *Handler {
	return &Handler{
		HTTPClient: httpClient,
		ProculaURL: proculaURL,
		APIKey:     apiKey,
	}
}

// HandleRegistry proxies GET /api/procula/actions/registry with a 60-second
// in-memory cache.
func (h *Handler) HandleRegistry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.cache.mu.Lock()
	defer h.cache.mu.Unlock()
	if len(h.cache.body) > 0 && time.Since(h.cache.lastFetch) < registryTTL {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "hit")
		w.Write(h.cache.body) //nolint:errcheck
		return
	}
	resp, err := h.HTTPClient.Get(h.ProculaURL + "/api/procula/actions/registry")
	if err != nil {
		httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		h.cache.body = body
		h.cache.lastFetch = time.Now()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body) //nolint:errcheck
}

// HandleCreate proxies POST /api/procula/actions, forwarding the body and any
// ?wait= query param unchanged. The API key is injected from h.APIKey.
func (h *Handler) HandleCreate(w http.ResponseWriter, r *http.Request) {
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
	target := h.ProculaURL + "/api/procula/actions"
	if q := r.URL.RawQuery; q != "" {
		target += "?" + q
	}
	upstream, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		httputil.WriteError(w, "build request", http.StatusInternalServerError)
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	if h.APIKey != "" {
		upstream.Header.Set("X-API-Key", h.APIKey)
	}
	resp, err := h.HTTPClient.Do(upstream)
	if err != nil {
		httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}
