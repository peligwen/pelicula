// Peligrosa: trust boundary layer.
// Sessions, login rate limiter, CSRF origin guard, and role-based access
// guards (Guard/GuardManager/GuardAdmin). Changes here affect the core
// authentication surface — see ../PELIGROSA.md.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type UserRole string

const (
	RoleViewer  UserRole = "viewer"
	RoleManager UserRole = "manager"
	RoleAdmin   UserRole = "admin"
)

func (r UserRole) atLeast(min UserRole) bool {
	order := map[UserRole]int{RoleViewer: 1, RoleManager: 2, RoleAdmin: 3}
	return order[r] >= order[min]
}

type session struct {
	username string
	role     UserRole
	expiry   time.Time
}

// loginAttempts tracks recent failed login timestamps per IP for rate limiting.
type loginAttempts struct {
	times []time.Time
}

// Auth handles authentication and authorization.
// mode "off"      — all requests pass through
// mode "jellyfin" — credentials verified against Jellyfin; roles from rolesFile
type Auth struct {
	mode       string
	rolesStore *RolesStore  // non-nil in "jellyfin" mode
	httpClient *http.Client // used for Jellyfin auth calls
	sessions   map[string]session
	failures   map[string]*loginAttempts // IP → recent failure timestamps
	mu         sync.RWMutex
}

// AuthConfig holds parameters for NewAuth.
type AuthConfig struct {
	Mode       string
	RolesFile  string       // for "jellyfin" mode
	HTTPClient *http.Client // for "jellyfin" mode; nil → 10-second default client
}

func NewAuth(cfg AuthConfig) *Auth {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	a := &Auth{
		mode:       cfg.Mode,
		sessions:   make(map[string]session),
		failures:   make(map[string]*loginAttempts),
		httpClient: hc,
	}
	if cfg.Mode == "jellyfin" {
		a.rolesStore = NewRolesStore(cfg.RolesFile)
		slog.Info("auth mode: jellyfin — credentials verified against Jellyfin", "component", "auth")
	}
	go a.cleanupSessions()
	return a
}

// cleanupSessions periodically removes expired sessions and stale rate-limit
// entries to prevent unbounded memory growth.
func (a *Auth) cleanupSessions() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		window := now.Add(-5 * time.Minute)
		a.mu.Lock()
		for token, sess := range a.sessions {
			if now.After(sess.expiry) {
				delete(a.sessions, token)
			}
		}
		for ip, fa := range a.failures {
			var recent []time.Time
			for _, t := range fa.times {
				if t.After(window) {
					recent = append(recent, t)
				}
			}
			if len(recent) == 0 {
				delete(a.failures, ip)
			} else {
				fa.times = recent
			}
		}
		a.mu.Unlock()
	}
}

// isRateLimited returns true if the IP has exceeded 5 failed logins in 5 minutes.
func (a *Auth) isRateLimited(ip string) bool {
	window := time.Now().Add(-5 * time.Minute)
	a.mu.RLock()
	fa, ok := a.failures[ip]
	if !ok {
		a.mu.RUnlock()
		return false
	}
	count := 0
	for _, t := range fa.times {
		if t.After(window) {
			count++
		}
	}
	a.mu.RUnlock()
	return count >= 5
}

// recordFailure records a failed login attempt for rate limiting.
func (a *Auth) recordFailure(ip string) {
	a.mu.Lock()
	if a.failures[ip] == nil {
		a.failures[ip] = &loginAttempts{}
	}
	a.failures[ip].times = append(a.failures[ip].times, time.Now())
	a.mu.Unlock()
}

// IsOffMode reports whether auth is disabled (PELICULA_AUTH=off).
// Used by endpoints that must block state-mutating requests even in off mode.
func (a *Auth) IsOffMode() bool {
	return a.mode == "off"
}

// Guard wraps a handler; if auth is off it passes through, otherwise
// it requires a valid session regardless of role.
func (a *Auth) Guard(next http.Handler) http.Handler {
	return a.guardRole(next, RoleViewer)
}

// GuardManager requires at least manager role.
func (a *Auth) GuardManager(next http.Handler) http.Handler {
	return a.guardRole(next, RoleManager)
}

// GuardAdmin requires admin role.
func (a *Auth) GuardAdmin(next http.Handler) http.Handler {
	return a.guardRole(next, RoleAdmin)
}

func (a *Auth) guardRole(next http.Handler, minRole UserRole) http.Handler {
	if a.mode == "off" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := a.getSession(r)
		if !ok {
			writeError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !sess.role.atLeast(minRole) {
			writeError(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Auth) getSession(r *http.Request) (session, bool) {
	cookie, err := r.Cookie("pelicula_session")
	if err != nil {
		return session{}, false
	}
	a.mu.RLock()
	sess, ok := a.sessions[cookie.Value]
	a.mu.RUnlock()
	if !ok || time.Now().After(sess.expiry) {
		return session{}, false
	}
	return sess, true
}

func (a *Auth) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if a.mode == "off" {
		writeJSON(w, map[string]any{"auth": false, "message": "auth disabled"})
		return
	}

	ip := clientIP(r)
	if a.isRateLimited(ip) {
		writeError(w, "too many failed attempts — try again later", http.StatusTooManyRequests)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10) // 64 KB
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request", http.StatusBadRequest)
		return
	}

	result, err := jellyfinAuthenticateByName(a.httpClient, req.Username, req.Password)
	if err != nil {
		var httpErr *jellyfinHTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized {
			a.recordFailure(ip)
			writeError(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		slog.Error("Jellyfin auth call failed", "component", "auth", "error", err)
		writeError(w, "authentication service unavailable", http.StatusServiceUnavailable)
		return
	}
	// Jellyfin admins always get the admin role in Pelicula.
	// Other users keep their stored role, defaulting to viewer on first login.
	var role UserRole
	if result.IsAdministrator {
		role = RoleAdmin
	} else if stored, ok := a.rolesStore.Lookup(result.UserID); ok {
		role = stored
	} else {
		role = RoleViewer
	}
	if err := a.rolesStore.Upsert(result.UserID, result.Username, role); err != nil {
		slog.Warn("failed to persist role", "component", "auth", "user", result.Username, "error", err)
	}
	// Override username to the one Jellyfin returned (canonical casing).
	req.Username = result.Username

	token, err := generateToken()
	if err != nil {
		writeError(w, "internal error", http.StatusInternalServerError)
		return
	}
	expiry := time.Now().Add(24 * time.Hour)

	a.mu.Lock()
	a.sessions[token] = session{username: req.Username, role: role, expiry: expiry}
	a.mu.Unlock()

	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     "pelicula_session",
		Value:    token,
		Path:     "/",
		Expires:  expiry,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})

	writeJSON(w, map[string]any{"status": "ok", "role": string(role)})
}

func (a *Auth) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	cookie, err := r.Cookie("pelicula_session")
	if err == nil {
		a.mu.Lock()
		delete(a.sessions, cookie.Value)
		a.mu.Unlock()
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "pelicula_session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	writeJSON(w, map[string]string{"status": "ok"})
}

func (a *Auth) HandleCheck(w http.ResponseWriter, r *http.Request) {
	if a.mode == "off" {
		writeJSON(w, map[string]any{"auth": false, "valid": true})
		return
	}

	sess, ok := a.getSession(r)
	if !ok {
		// nginx auth_request requires a non-2xx status to deny access.
		// The dashboard JS uses the JSON body, so we still send it.
		if r.URL.Query().Get("nginx") == "1" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"auth":true,"valid":false,"mode":%q}`, a.mode)
			return
		}
		writeJSON(w, map[string]any{"auth": true, "valid": false, "mode": a.mode})
		return
	}

	writeJSON(w, map[string]any{
		"auth":     true,
		"valid":    true,
		"mode":     a.mode,
		"username": sess.username,
		"role":     string(sess.role),
	})
}

// SessionFor returns the authenticated username and role for the request.
// Returns ("", "", false) if not authenticated. In off mode, returns ("", RoleAdmin, true).
func (a *Auth) SessionFor(r *http.Request) (username string, role UserRole, ok bool) {
	if a.mode == "off" {
		return "", RoleAdmin, true
	}
	sess, sOk := a.getSession(r)
	if !sOk {
		return "", "", false
	}
	return sess.username, sess.role, true
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// isStateMutating reports whether the HTTP method changes server state.
// CSRF guards only apply to mutating methods; safe methods pass through.
func isStateMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// requireLocalOriginStrict is a Peligrosa CSRF middleware. For state-mutating
// requests it rejects Origins that are missing or not a LAN/localhost address.
// Safe methods (GET/HEAD/OPTIONS) pass through.
// Use for admin-only endpoints where only a LAN browser should send POSTs.
func requireLocalOriginStrict(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isStateMutating(r.Method) && !isLocalOrigin(r.Header.Get("Origin")) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireLocalOriginSoft is a Peligrosa CSRF middleware. For state-mutating
// requests it allows empty Origin (API/curl callers) but rejects non-empty
// Origins that are not LAN/localhost (browser cross-origin).
// Safe methods pass through.
// Use for endpoints where programmatic callers without an Origin are valid.
func requireLocalOriginSoft(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isStateMutating(r.Method) {
			if origin := r.Header.Get("Origin"); origin != "" && !isLocalOrigin(origin) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// isLocalOrigin returns true if the request Origin is a localhost or
// private-network address. Parses the origin as a URL and checks the hostname
// to prevent substring-match bypasses. Returns false for empty Origin so that
// unauthenticated curl requests (no Origin header) cannot bypass strict checks.
//
// Peligrosa: Use the middleware wrappers (requireLocalOriginStrict /
// requireLocalOriginSoft) rather than calling this directly from handlers.
func isLocalOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, cidr := range []string{
		"192.168.0.0/16",
		"10.0.0.0/8",
		"172.16.0.0/12",
	} {
		_, network, _ := net.ParseCIDR(cidr)
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
