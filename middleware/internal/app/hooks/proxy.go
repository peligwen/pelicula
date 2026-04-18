// proxy.go — outbound proxying to Procula and Jellyfin.
package hooks

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"pelicula-api/httputil"
)

// HandleProcessingProxy proxies Procula's status + jobs for the dashboard
// Processing section.
func (h *Handler) HandleProcessingProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	type rawResult struct {
		body []byte
		err  error
	}
	statusCh := make(chan rawResult, 1)
	jobsCh := make(chan rawResult, 1)

	go func() {
		b, err := h.Procula.GetStatus(ctx)
		statusCh <- rawResult{body: b, err: err}
	}()
	go func() {
		b, err := h.Procula.ListJobs(ctx)
		jobsCh <- rawResult{body: b, err: err}
	}()

	statusRes := <-statusCh
	jobsRes := <-jobsCh

	if statusRes.err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"error":     "processing service unavailable",
			"retryable": true,
		})
		return
	}

	var statusData, jobsData any
	json.Unmarshal(statusRes.body, &statusData) //nolint:errcheck
	if jobsRes.err == nil {
		json.Unmarshal(jobsRes.body, &jobsData) //nolint:errcheck
	}

	httputil.WriteJSON(w, map[string]any{
		"status": statusData,
		"jobs":   jobsData,
	})
}

// HandleJellyfinRefresh triggers a Jellyfin library scan. Called by Procula
// (internal only).
func (h *Handler) HandleJellyfinRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Verify Procula API key so only Procula can trigger refreshes.
	if key := strings.TrimSpace(os.Getenv("PROCULA_API_KEY")); key != "" {
		provided := r.Header.Get("X-API-Key")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(key)) == 0 {
			httputil.WriteError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	if h.TriggerJellyfinRefresh == nil {
		httputil.WriteError(w, "refresh not configured", http.StatusInternalServerError)
		return
	}
	if err := h.TriggerJellyfinRefresh(); err != nil {
		slog.Error("library refresh failed", "component", "jellyfin", "error", err)
		httputil.WriteError(w, "refresh failed", http.StatusInternalServerError)
		return
	}
	httputil.WriteJSON(w, map[string]string{"status": "ok"})
}

// HandleStorageProxy proxies Procula's storage report for the dashboard
// Storage section.
func (h *Handler) HandleStorageProxy(w http.ResponseWriter, r *http.Request) {
	h.proxyProcula("/api/procula/storage")(w, r)
}

// HandleUpdatesProxy proxies Procula's update check result for the dashboard
// footer.
func (h *Handler) HandleUpdatesProxy(w http.ResponseWriter, r *http.Request) {
	h.proxyProcula("/api/procula/updates")(w, r)
}

// HandleProculaSettingsProxy proxies GET/POST to Procula's settings endpoint.
func (h *Handler) HandleProculaSettingsProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.proxyProcula("/api/procula/settings")(w, r)
		return
	}
	h.proxyProculaMutate("/api/procula/settings")(w, r)
}

// HandleStorageScanProxy proxies a POST scan trigger to Procula.
func (h *Handler) HandleStorageScanProxy(w http.ResponseWriter, r *http.Request) {
	h.proxyProculaMutate("/api/procula/storage/scan")(w, r)
}

// proxyProcula returns an http.HandlerFunc that forwards a GET to the given
// Procula path and streams the JSON response back.
// When forwardQuery is true the incoming request's raw query string is appended.
func (h *Handler) proxyProcula(path string, forwardQuery ...bool) http.HandlerFunc {
	fwd := len(forwardQuery) > 0 && forwardQuery[0]
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		target := h.ProculaURL + path
		if fwd {
			if q := r.URL.RawQuery; q != "" {
				target += "?" + q
			}
		}
		resp, err := h.httpClient().Get(target)
		if err != nil {
			httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		if _, err := io.Copy(w, resp.Body); err != nil {
			slog.Warn("failed to stream proxy response", "component", "proxy", "path", path, "error", err)
		}
	}
}

// proxyProculaMutate returns an http.HandlerFunc that forwards the request
// (method, body, Content-Type) to the given Procula path, injecting
// X-API-Key if PROCULA_API_KEY is set.
func (h *Handler) proxyProculaMutate(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body io.Reader
		if r.Body != nil {
			body = r.Body
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, h.ProculaURL+path, body)
		if err != nil {
			httputil.WriteError(w, "proxy error", http.StatusInternalServerError)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		if key := strings.TrimSpace(os.Getenv("PROCULA_API_KEY")); key != "" {
			req.Header.Set("X-API-Key", key)
		}
		resp, err := h.httpClient().Do(req)
		if err != nil {
			httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		if _, err := io.Copy(w, resp.Body); err != nil {
			slog.Warn("failed to stream proxy response", "component", "proxy", "path", path, "error", err)
		}
	}
}

// proxyProculaWithContext is like proxyProcula but uses an explicit context.
// Used by callers that need to pass a specific context.
func (h *Handler) proxyProculaWithContext(ctx context.Context, path string) ([]byte, error) {
	target := h.ProculaURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}
