// Package actions proxies action-bus requests to procula and caches the
// registry endpoint.
package actions

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"pelicula-api/httputil"
	"strconv"
	"sync"
	"time"
)

const registryTTL = 60 * time.Second

// actionWaitMargin is the headroom added to a client-supplied ?wait= value
// for HandleCreate's per-request deadline, so the middleware's own timeout
// never races Procula's honestly-reported worst case: Procula caps ?wait= at
// 10s and can legitimately respond right at that boundary (see MWA-13).
const actionWaitMargin = 5 * time.Second

// actionDefaultTimeout is HandleCreate's per-request deadline when the
// request carries no (or an unparseable) ?wait= value.
const actionDefaultTimeout = 15 * time.Second

// actionRequestTimeout returns the per-request deadline for a proxied
// POST /api/procula/actions call: the requested wait (whole seconds, same
// format Procula itself parses) plus actionWaitMargin, or
// actionDefaultTimeout when wait is absent or not a positive integer.
func actionRequestTimeout(waitParam string) time.Duration {
	if waitParam == "" {
		return actionDefaultTimeout
	}
	waitSecs, err := strconv.Atoi(waitParam)
	if err != nil || waitSecs <= 0 {
		return actionDefaultTimeout
	}
	return time.Duration(waitSecs)*time.Second + actionWaitMargin
}

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

	// now is injectable for tests; production code leaves it nil (falls back to time.Now).
	now func() time.Time

	// requestTimeoutFor computes HandleCreate's per-request deadline from the
	// request's ?wait= value. Injectable for tests (see actionRequestTimeout,
	// MWA-13); production code leaves it nil, which falls back to
	// actionRequestTimeout with the real wait+margin/default budgets.
	requestTimeoutFor func(waitParam string) time.Duration
}

// timeNow returns the current time using the injectable clock or time.Now in production.
func (h *Handler) timeNow() time.Time {
	if h.now != nil {
		return h.now()
	}
	return time.Now()
}

// requestTimeout returns the per-request deadline for a proxied action-bus
// call, using the injectable override if set (tests) or actionRequestTimeout
// (production).
func (h *Handler) requestTimeout(waitParam string) time.Duration {
	if h.requestTimeoutFor != nil {
		return h.requestTimeoutFor(waitParam)
	}
	return actionRequestTimeout(waitParam)
}

// actionsClient returns an *http.Client for one HandleCreate call that
// shares h.HTTPClient's Transport (connection pooling, User-Agent injection)
// but carries no fixed Timeout of its own — the per-request context deadline
// set in HandleCreate governs instead. This is deliberately not a new
// long-lived shared client: it's a cheap per-call wrapper around the
// existing Transport, so every other consumer of h.HTTPClient (registry
// fetch, health checks, Jellyfin/Procula typed clients, autowire polling)
// keeps its original fixed 10s Timeout unchanged (MWA-13).
func (h *Handler) actionsClient() *http.Client {
	if h.HTTPClient == nil {
		return &http.Client{}
	}
	return &http.Client{Transport: h.HTTPClient.Transport}
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
	if len(h.cache.body) > 0 && h.timeNow().Sub(h.cache.lastFetch) < registryTTL {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "hit")
		w.Write(h.cache.body) //nolint:errcheck
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, h.ProculaURL+"/api/procula/actions/registry", nil)
	if err != nil {
		httputil.WriteError(w, "build request", http.StatusInternalServerError)
		return
	}
	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		// Never cache or serve a truncated registry body.
		httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	if resp.StatusCode == http.StatusOK {
		h.cache.body = body
		h.cache.lastFetch = h.timeNow()
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

	// h.HTTPClient carries a fixed 10s Timeout shared with several other
	// consumers (registry fetch, health checks, typed clients) — exactly
	// equal to Procula's documented ?wait= cap, leaving zero margin for
	// network latency or scheduling at wait=10. Give this call its own
	// deadline, scaled to the wait value actually being forwarded, instead
	// of widening (or replacing) that shared client (MWA-13).
	ctx, cancel := context.WithTimeout(r.Context(), h.requestTimeout(r.URL.Query().Get("wait")))
	defer cancel()

	upstream, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		httputil.WriteError(w, "build request", http.StatusInternalServerError)
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	if h.APIKey != "" {
		upstream.Header.Set("X-API-Key", h.APIKey)
	}
	resp, err := h.actionsClient().Do(upstream)
	if err != nil {
		httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}
