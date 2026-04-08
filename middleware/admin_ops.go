package main

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── Per-user admin rate limiter ──────────────────────────────────────────────
// Caps admin ops at 10 per minute per key (username when auth on, IP when off).

type adminRateLimiter struct {
	mu      sync.Mutex
	buckets map[string][]time.Time
}

var adminLimiter = &adminRateLimiter{buckets: make(map[string][]time.Time)}

func (rl *adminRateLimiter) allow(key string) bool {
	const limit = 10
	window := time.Now().Add(-time.Minute)
	rl.mu.Lock()
	defer rl.mu.Unlock()
	times := rl.buckets[key]
	// trim expired
	valid := times[:0]
	for _, t := range times {
		if t.After(window) {
			valid = append(valid, t)
		}
	}
	rl.buckets[key] = valid
	if len(valid) >= limit {
		return false
	}
	rl.buckets[key] = append(rl.buckets[key], time.Now())
	return true
}

// adminRateLimitKey extracts a per-user key for rate limiting.
// Uses the session username when auth is on, else client IP.
func adminRateLimitKey(r *http.Request) string {
	if authMiddleware != nil && !authMiddleware.IsOffMode() {
		if sess, ok := authMiddleware.getSession(r); ok && sess.username != "" {
			return "user:" + sess.username
		}
	}
	return "ip:" + clientIP(r)
}

// checkAdminRate returns false and writes a 429 if the caller is rate-limited.
func checkAdminRate(w http.ResponseWriter, r *http.Request) bool {
	key := adminRateLimitKey(r)
	if !adminLimiter.allow(key) {
		writeError(w, "rate limited — try again in a moment", http.StatusTooManyRequests)
		return false
	}
	return true
}

// auditLog writes an slog entry for every admin action.
func auditLog(r *http.Request, action, target, result string) {
	key := adminRateLimitKey(r)
	slog.Info("admin action", "component", "admin_ops",
		"actor", key, "action", action, "target", target, "result", result)
}

// ── Off-mode guard ───────────────────────────────────────────────────────────

// requireAuthOrLocalOrigin enforces that when PELICULA_AUTH=off the request
// carries a same-origin / localhost / RFC1918 Origin header. In auth modes
// the caller is already authenticated via GuardAdmin; this guard is a no-op.
// Returns false and writes 403 if the check fails.
//
// Pattern mirrors invites.go, jellyfin.go, and settings.go.
func requireAuthOrLocalOrigin(w http.ResponseWriter, r *http.Request) bool {
	if authMiddleware == nil || !authMiddleware.IsOffMode() {
		return true
	}
	if origin := r.Header.Get("Origin"); origin == "" || !isLocalOrigin(origin) {
		writeError(w, "forbidden: enable PELICULA_AUTH or access from a local origin", http.StatusForbidden)
		return false
	}
	return true
}

// ── Handlers ─────────────────────────────────────────────────────────────────

// handleServiceRestart restarts a single container by name.
// POST /api/pelicula/admin/restart?svc=<name>
func handleServiceRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireAuthOrLocalOrigin(w, r) {
		return
	}
	if !checkAdminRate(w, r) {
		return
	}
	svc := r.URL.Query().Get("svc")
	if !isAllowedContainer(svc) {
		writeError(w, "unknown service", http.StatusBadRequest)
		return
	}
	if err := dockerRestart(svc); err != nil {
		slog.Error("restart failed", "component", "admin_ops", "svc", svc, "error", err)
		auditLog(r, "restart", svc, "error: "+err.Error())
		writeError(w, "restart failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	auditLog(r, "restart", svc, "ok")
	writeJSON(w, map[string]any{"ok": true, "svc": svc})
}

// handleStackRestart restarts all whitelisted containers.
// pelicula-api is restarted last (in a goroutine) so the response can flush.
// POST /api/pelicula/admin/stack/restart
func handleStackRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireAuthOrLocalOrigin(w, r) {
		return
	}
	if !checkAdminRate(w, r) {
		return
	}
	// Restart everything except ourselves first.
	// qbittorrent is omitted: it shares gluetun's network namespace and
	// comes back automatically when gluetun restarts.
	order := []string{"nginx", "procula", "sonarr", "radarr", "prowlarr",
		"qbittorrent", "jellyfin", "bazarr", "gluetun", "jellyseerr"}
	var errs []string
	for _, svc := range order {
		if !isAllowedContainer(svc) {
			continue // defense-in-depth: skip any future typo in order
		}
		if err := dockerRestart(svc); err != nil {
			slog.Warn("stack restart: skipping", "component", "admin_ops", "svc", svc, "error", err)
			errs = append(errs, svc+": "+err.Error())
		}
	}
	result := "ok"
	if len(errs) > 0 {
		result = "partial: " + strings.Join(errs, "; ")
	}
	auditLog(r, "stack_restart", "all", result)
	writeJSON(w, map[string]any{"ok": true, "errors": errs})
	// Restart ourselves last — response has already been sent above (flush happens
	// after writeJSON returns). Give it 500ms.
	go func() {
		time.Sleep(500 * time.Millisecond)
		dockerRestart("pelicula-api") //nolint:errcheck — we won't be here to log it
	}()
}

// handleStackRebuild restarts the two Go services (pelicula-api + procula).
// Named "rebuild" for historical/dashboard compatibility; a true image rebuild
// requires ./pelicula rebuild from a host shell.
// POST /api/pelicula/admin/stack/rebuild
func handleStackRebuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireAuthOrLocalOrigin(w, r) {
		return
	}
	if !checkAdminRate(w, r) {
		return
	}
	rebuildResult := "ok"
	if err := dockerRestart("procula"); err != nil {
		slog.Warn("restart_go_services: procula restart failed", "component", "admin_ops", "error", err)
		rebuildResult = "procula: " + err.Error()
	}
	auditLog(r, "restart_go_services", "pelicula-api+procula", rebuildResult)
	writeJSON(w, map[string]any{"ok": true})
	go func() {
		time.Sleep(500 * time.Millisecond)
		dockerRestart("pelicula-api") //nolint:errcheck
	}()
}

// handleServiceLogs returns recent log lines for a named container.
// GET /api/pelicula/admin/logs?svc=<name>&tail=<n>  (default 200, max 500)
func handleServiceLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireAuthOrLocalOrigin(w, r) {
		return
	}
	if !checkAdminRate(w, r) {
		return
	}
	svc := r.URL.Query().Get("svc")
	if !isAllowedContainer(svc) {
		writeError(w, "unknown service", http.StatusBadRequest)
		return
	}
	tail := 200
	if s := r.URL.Query().Get("tail"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			tail = n
		}
	}
	logs, err := dockerLogs(svc, tail)
	if err != nil {
		slog.Warn("logs failed", "component", "admin_ops", "svc", svc, "error", err)
		writeError(w, "logs unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}
	auditLog(r, "logs", svc, "ok")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(logs)
}
