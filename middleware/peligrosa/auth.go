// Peligrosa: trust boundary layer.
// Sessions, login rate limiter, CSRF origin guard, and role-based access
// guards (Guard/GuardManager/GuardAdmin). Changes here affect the core
// authentication surface — see ../../docs/PELIGROSA.md.
package peligrosa

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"pelicula-api/clients"
	"pelicula-api/httputil"
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
// mode "jellyfin" — credentials verified against Jellyfin; roles from rolesStore
//
// When db is non-nil, sessions and rate-limit data are persisted to SQLite so
// they survive process restarts. When db is nil (e.g. in tests created with
// newTestAuth), the in-memory maps are used exclusively.
type Auth struct {
	mode       string
	db         *sql.DB                // non-nil in production; nil in tests that don't need persistence
	rolesStore *RolesStore            // non-nil in "jellyfin" mode
	jellyfin   clients.JellyfinClient // used for Jellyfin auth calls
	sessions   map[string]session
	failures   map[string]*loginAttempts // IP → recent failure timestamps
	mu         sync.RWMutex
}

// AuthConfig holds parameters for NewAuth.
type AuthConfig struct {
	Mode     string
	DB       *sql.DB                // for session + rate-limit persistence (nil = in-memory only)
	Jellyfin clients.JellyfinClient // for "jellyfin" mode; must be non-nil when Mode == "jellyfin"
}

func NewAuth(cfg AuthConfig) *Auth {
	a := &Auth{
		mode:     cfg.Mode,
		db:       cfg.DB,
		sessions: make(map[string]session),
		failures: make(map[string]*loginAttempts),
		jellyfin: cfg.Jellyfin,
	}
	if cfg.Mode == "jellyfin" {
		if cfg.DB != nil {
			a.rolesStore = NewRolesStore(cfg.DB)
		}
		slog.Info("auth mode: jellyfin — credentials verified against Jellyfin", "component", "auth")
	}
	// Restore persisted sessions from DB into the in-memory map on startup.
	if cfg.DB != nil {
		a.loadSessionsFromDB()
	}
	go a.cleanupSessions()
	return a
}

// Roles returns the roles store, or nil when auth runs in off mode.
// Used by the main-package export/import backup codepath.
func (a *Auth) Roles() *RolesStore {
	if a == nil {
		return nil
	}
	return a.rolesStore
}

// loadSessionsFromDB reads non-expired sessions from SQLite into the in-memory map.
func (a *Auth) loadSessionsFromDB() {
	rows, err := a.db.Query(
		`SELECT token, username, role, expires_at FROM sessions WHERE expires_at > ?`,
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		slog.Warn("failed to load sessions from DB", "component", "auth", "error", err)
		return
	}
	defer rows.Close()
	a.mu.Lock()
	defer a.mu.Unlock()
	for rows.Next() {
		var token, username, role, expiresAt string
		if err := rows.Scan(&token, &username, &role, &expiresAt); err != nil {
			continue
		}
		t, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			continue
		}
		a.sessions[token] = session{username: username, role: UserRole(role), expiry: t}
	}
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

		// Purge expired session rows from SQLite as well.
		if a.db != nil {
			nowStr := now.UTC().Format(time.RFC3339)
			if _, err := a.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, nowStr); err != nil {
				slog.Warn("cleanup: failed to delete expired sessions", "component", "auth", "error", err)
			}
		}
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
// Rate limits are kept purely in-memory: the 5-minute window is shorter than
// any realistic restart time, so persistence provides no meaningful value.
func (a *Auth) recordFailure(ip string) {
	now := time.Now()
	a.mu.Lock()
	if a.failures[ip] == nil {
		a.failures[ip] = &loginAttempts{}
	}
	a.failures[ip].times = append(a.failures[ip].times, now)
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

// isRemoteRequest reports whether the request arrived via the remote (Peligrosa)
// nginx vhost. The remote vhost injects X-Pelicula-Remote: true; the LAN vhost
// strips the header to prevent spoofing. See nginx/remote.conf.template.
func isRemoteRequest(r *http.Request) bool {
	return r.Header.Get("X-Pelicula-Remote") == "true"
}

// effectiveRole returns the role to enforce for this request. Remote requests
// are capped to viewer regardless of the stored role — defense-in-depth so that
// a compromised admin credential cannot escalate via the remote vhost.
func effectiveRole(sess session, r *http.Request) UserRole {
	if isRemoteRequest(r) && sess.role.atLeast(RoleManager) {
		return RoleViewer
	}
	return sess.role
}

func (a *Auth) guardRole(next http.Handler, minRole UserRole) http.Handler {
	if a.mode == "off" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := a.getSession(r)
		if !ok {
			httputil.WriteError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !effectiveRole(sess, r).atLeast(minRole) {
			httputil.WriteError(w, "forbidden", http.StatusForbidden)
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
		httputil.WriteJSON(w, map[string]any{"auth": false, "message": "auth disabled"})
		return
	}

	ip := httputil.ClientIP(r)
	if a.isRateLimited(ip) {
		httputil.WriteError(w, "too many failed attempts — try again later", http.StatusTooManyRequests)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10) // 64 KB
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, "invalid request", http.StatusBadRequest)
		return
	}

	result, err := a.jellyfin.AuthenticateByName(req.Username, req.Password)
	if err != nil {
		var httpErr *clients.JellyfinHTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == http.StatusUnauthorized {
			a.recordFailure(ip)
			httputil.WriteError(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		slog.Error("Jellyfin auth call failed", "component", "auth", "error", err)
		httputil.WriteError(w, "authentication service unavailable", http.StatusServiceUnavailable)
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
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}
	expiry := time.Now().Add(24 * time.Hour)

	a.mu.Lock()
	a.sessions[token] = session{username: req.Username, role: role, expiry: expiry}
	a.mu.Unlock()

	if a.db != nil {
		_, err := a.db.Exec(
			`INSERT OR REPLACE INTO sessions (token, username, role, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
			token, req.Username, string(role),
			time.Now().UTC().Format(time.RFC3339),
			expiry.UTC().Format(time.RFC3339),
		)
		if err != nil {
			slog.Warn("failed to persist session", "component", "auth", "user", req.Username, "error", err)
		}
	}

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

	httputil.WriteJSON(w, map[string]any{"status": "ok", "role": string(role)})
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
		if a.db != nil {
			if _, dbErr := a.db.Exec(`DELETE FROM sessions WHERE token = ?`, cookie.Value); dbErr != nil {
				slog.Warn("failed to delete session from DB", "component", "auth", "error", dbErr)
			}
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "pelicula_session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	httputil.WriteJSON(w, map[string]string{"status": "ok"})
}

func (a *Auth) HandleCheck(w http.ResponseWriter, r *http.Request) {
	if a.mode == "off" {
		httputil.WriteJSON(w, map[string]any{"auth": false, "valid": true})
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
		httputil.WriteJSON(w, map[string]any{"auth": true, "valid": false, "mode": a.mode})
		return
	}

	httputil.WriteJSON(w, map[string]any{
		"auth":     true,
		"valid":    true,
		"mode":     a.mode,
		"username": sess.username,
		"role":     string(effectiveRole(sess, r)),
		"remote":   isRemoteRequest(r),
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
	return sess.username, effectiveRole(sess, r), true
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
