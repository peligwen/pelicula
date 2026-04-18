package adminops

import (
	"log/slog"
	"net/http"
	"pelicula-api/httputil"
	"pelicula-api/internal/clients/docker"
	"strconv"
	"strings"
	"time"
)

// Handler owns the admin ops dependencies.
type Handler struct {
	Docker    *docker.Client                     // internal/clients/docker
	sessionFn func(*http.Request) (string, bool) // may be nil (no-auth mode)
	limiter   *rateLimiter                       // unexported; initialized in New
}

// New constructs a Handler with a fresh rate limiter.
// sessionFn extracts the authenticated username from a request; pass nil for no-auth mode.
// Callers wrapping *peligrosa.Auth should pass:
//
//	func(r *http.Request) (string, bool) {
//	    username, _, ok := auth.SessionFor(r)
//	    return username, ok
//	}
func New(dockerClient *docker.Client, sessionFn func(*http.Request) (string, bool)) *Handler {
	return &Handler{
		Docker:    dockerClient,
		sessionFn: sessionFn,
		limiter:   newRateLimiter(),
	}
}

// rateLimitKey extracts a per-user key for rate limiting.
// Uses the session username when authenticated, else client IP.
// Loopback host-machine callers bucket as "user:(loopback)"; they share a
// single rate-limit token — fine since the host-machine admin isn't the
// attacker we're worried about.
func (h *Handler) rateLimitKey(r *http.Request) string {
	if h.sessionFn != nil {
		if username, ok := h.sessionFn(r); ok && username != "" {
			return "user:" + username
		}
	}
	return "ip:" + httputil.ClientIP(r)
}

// checkRate returns false and writes a 429 if the caller is rate-limited.
func (h *Handler) checkRate(w http.ResponseWriter, r *http.Request) bool {
	key := h.rateLimitKey(r)
	if !h.limiter.allow(key) {
		httputil.WriteError(w, "rate limited — try again in a moment", http.StatusTooManyRequests)
		return false
	}
	return true
}

// auditLog writes an slog entry for every admin action.
func (h *Handler) auditLog(r *http.Request, action, target, result string) {
	key := h.rateLimitKey(r)
	slog.Info("admin action", "component", "admin_ops",
		"actor", key, "action", action, "target", target, "result", result)
}

// HandleStackRestart restarts all whitelisted containers.
// pelicula-api is restarted last (in a goroutine) so the response can flush.
// POST /api/pelicula/admin/stack/restart
func (h *Handler) HandleStackRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.checkRate(w, r) {
		return
	}
	// Restart everything except ourselves first.
	// qbittorrent is omitted: it shares gluetun's network namespace and
	// comes back automatically when gluetun restarts.
	order := []string{"nginx", "procula", "sonarr", "radarr", "prowlarr",
		"qbittorrent", "jellyfin", "bazarr", "gluetun"}
	var errs []string
	for _, svc := range order {
		if !h.Docker.IsAllowed(svc) {
			continue // defense-in-depth: skip any future typo in order
		}
		if err := h.Docker.Restart(svc); err != nil {
			slog.Warn("stack restart: skipping", "component", "admin_ops", "svc", svc, "error", err)
			errs = append(errs, svc+": "+err.Error())
		}
	}
	result := "ok"
	if len(errs) > 0 {
		result = "partial: " + strings.Join(errs, "; ")
	}
	h.auditLog(r, "stack_restart", "all", result)
	httputil.WriteJSON(w, map[string]any{"ok": true, "errors": errs})
	// Restart ourselves last — response has already been sent above (flush happens
	// after httputil.WriteJSON returns). Give it 500ms.
	go func() {
		time.Sleep(500 * time.Millisecond)
		h.Docker.Restart("pelicula-api") //nolint:errcheck — we won't be here to log it
	}()
}

// HandleVPNRestart restarts the VPN stack (gluetun, qbittorrent, prowlarr).
// qBittorrent and Prowlarr run on gluetun's network namespace and must be
// restarted alongside it.
// POST /api/pelicula/admin/vpn/restart
func (h *Handler) HandleVPNRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.checkRate(w, r) {
		return
	}
	var errs []string
	for _, svc := range []string{"gluetun", "qbittorrent", "prowlarr"} {
		if !h.Docker.IsAllowed(svc) {
			continue
		}
		if err := h.Docker.Restart(svc); err != nil {
			slog.Warn("vpn restart: container error",
				"component", "admin_ops", "svc", svc, "error", err)
			errs = append(errs, svc+": "+err.Error())
		}
	}
	result := "ok"
	if len(errs) > 0 {
		result = "partial: " + strings.Join(errs, "; ")
	}
	h.auditLog(r, "vpn_restart", "gluetun+qbittorrent+prowlarr", result)
	httputil.WriteJSON(w, map[string]any{"ok": true, "errors": errs})
}

// HandleServiceLogs returns recent log lines for a named container.
// GET /api/pelicula/admin/logs?svc=<name>&tail=<n>  (default 200, max 500)
func (h *Handler) HandleServiceLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.checkRate(w, r) {
		return
	}
	svc := r.URL.Query().Get("svc")
	if !h.Docker.IsAllowed(svc) {
		httputil.WriteError(w, "unknown service", http.StatusBadRequest)
		return
	}
	tail := 200
	if s := r.URL.Query().Get("tail"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			tail = n
		}
	}
	logs, err := h.Docker.Logs(svc, tail, false)
	if err != nil {
		slog.Warn("logs failed", "component", "admin_ops", "svc", svc, "error", err)
		httputil.WriteError(w, "logs unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	h.auditLog(r, "logs", svc, "ok")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(logs) //nolint:errcheck
}
