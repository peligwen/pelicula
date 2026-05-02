package peligrosa

import (
	"context"
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
	"pelicula-api/internal/repo/sessions"
)

const (
	rateLimitThreshold = 5
	rateLimitWindow    = 5 * time.Minute
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

// Auth handles authentication and authorization.
// The only auth mode is "jellyfin": credentials are verified against Jellyfin
// and roles are stored in rolesStore. Host-machine callers (nginx upstream +
// loopback X-Real-IP + loopback Host) get admin automatically via
// loopbackAutoSession — see loopback.go.
//
// Sessions and rate-limit data are persisted to SQLite so they survive process
// restarts. Unit tests that do not exercise HandleLogin construct Auth directly
// via newTestAuth() (which bypasses NewAuth) and omit db/rolesStore.
type Auth struct {
	db            *sql.DB                // always non-nil (NewAuth panics if DB is nil)
	rolesStore    *RolesStore            // always non-nil (initialised from db in NewAuth)
	sessionsStore *sessions.Store        // always non-nil (initialised from db in NewAuth)
	jellyfin      clients.JellyfinClient // used for Jellyfin auth calls
	sessions      map[string]session
	mu            sync.RWMutex
}

// AuthConfig holds parameters for NewAuth.
type AuthConfig struct {
	DB       *sql.DB // required — NewAuth panics if nil
	Jellyfin clients.JellyfinClient
}

func NewAuth(cfg AuthConfig) *Auth {
	if cfg.DB == nil {
		// DB is required in production. nil is only permitted in unit tests that
		// construct Auth directly via newTestAuth() and never exercise HandleLogin.
		// Any call to NewAuth without a DB in production is a programming error.
		panic("peligrosa.NewAuth: AuthConfig.DB must not be nil — rolesStore cannot be initialised")
	}
	a := &Auth{
		db:       cfg.DB,
		sessions: make(map[string]session),
		jellyfin: cfg.Jellyfin,
	}
	a.rolesStore = NewRolesStore(cfg.DB)
	a.sessionsStore = sessions.New(cfg.DB)
	slog.Info("auth: Jellyfin credentials required for login", "component", "auth")
	// Restore persisted sessions from DB into the in-memory map on startup.
	a.loadSessionsFromDB()
	go a.cleanupSessions()
	return a
}

// Roles returns the roles store. Returns nil when a is nil (e.g. tests using
// newTestAuth that never touch the DB).
// Used by the main-package export/import backup codepath.
func (a *Auth) Roles() *RolesStore {
	if a == nil {
		return nil
	}
	return a.rolesStore
}

// loadSessionsFromDB reads non-expired sessions from SQLite into the in-memory map.
func (a *Auth) loadSessionsFromDB() {
	active, err := a.sessionsStore.LookupActive(context.Background())
	if err != nil {
		slog.Warn("failed to load sessions from DB", "component", "auth", "error", err)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range active {
		a.sessions[s.Token] = session{username: s.Username, role: UserRole(s.Role), expiry: s.ExpiresAt}
	}
}

// cleanupSessions periodically removes expired sessions and stale rate-limit
// entries to prevent unbounded DB growth.
func (a *Auth) cleanupSessions() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		a.mu.Lock()
		for token, sess := range a.sessions {
			if now.After(sess.expiry) {
				delete(a.sessions, token)
			}
		}
		a.mu.Unlock()

		ctx := context.Background()
		if a.sessionsStore != nil {
			if err := a.sessionsStore.PruneExpired(ctx); err != nil {
				slog.Warn("cleanup: failed to delete expired sessions", "component", "auth", "error", err)
			}
			// Prune rate-limit rows whose window has long expired (2× the window).
			if err := a.sessionsStore.PruneRateLimit(ctx, now.Add(-2*rateLimitWindow)); err != nil {
				slog.Warn("cleanup: failed to prune rate-limit entries", "component", "auth", "error", err)
			}
		}
	}
}

// isRateLimited returns true if the IP has reached or exceeded the threshold of
// failed logins within the current rate-limit window. Uses a read-only DB query
// so it does not affect the count — only recordFailure increments it.
func (a *Auth) isRateLimited(ctx context.Context, ip string) bool {
	if a.sessionsStore == nil {
		return false
	}
	window := time.Now().Add(-rateLimitWindow)
	count, err := a.sessionsStore.RateLimitCount(ctx, ip, window)
	if err != nil {
		slog.Warn("rate-limit check failed — allowing request", "component", "auth", "ip", ip, "error", err)
		return false
	}
	return count >= rateLimitThreshold
}

// recordFailure increments the fail count for ip in the DB-backed rate_limits
// table. This persists across restarts so brute-force attempts cannot be reset
// by bouncing the process.
func (a *Auth) recordFailure(ctx context.Context, ip string) {
	if a.sessionsStore == nil {
		return
	}
	window := time.Now().Add(-rateLimitWindow)
	if _, err := a.sessionsStore.RateLimitUpsert(ctx, ip, window); err != nil {
		slog.Warn("failed to record login failure", "component", "auth", "ip", ip, "error", err)
	}
}

// Guard wraps a handler; requires a valid session regardless of role.
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := a.getSession(r)
		if !ok {
			// Loopback callers (host-machine via nginx) get a synthetic admin session.
			if loopbackAutoSession(r) {
				next.ServeHTTP(w, r)
				return
			}
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

	ip := httputil.ClientIP(r)
	if a.isRateLimited(r.Context(), ip) {
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
			a.recordFailure(r.Context(), ip)
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
	} else if stored, ok := a.rolesStore.Lookup(r.Context(), result.UserID); ok {
		role = stored
	} else {
		role = RoleViewer
	}
	if err := a.rolesStore.Upsert(r.Context(), result.UserID, result.Username, role); err != nil {
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

	if a.sessionsStore != nil {
		if err := a.sessionsStore.Create(r.Context(), token, req.Username, string(role), expiry); err != nil {
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
		SameSite: http.SameSiteStrictMode,
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
		if a.sessionsStore != nil {
			if dbErr := a.sessionsStore.Revoke(r.Context(), cookie.Value); dbErr != nil {
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
	sess, ok := a.getSession(r)
	if !ok {
		// No cookie — try host-machine auto-session before declining.
		if loopbackAutoSession(r) {
			httputil.WriteJSON(w, map[string]any{
				"auth":     true,
				"valid":    true,
				"username": "(loopback)",
				"role":     string(RoleAdmin),
				"remote":   false,
			})
			return
		}
		// nginx auth_request requires a non-2xx status to deny access.
		// The dashboard JS uses the JSON body, so we still send it.
		if r.URL.Query().Get("nginx") == "1" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"auth":true,"valid":false}`)
			return
		}
		httputil.WriteJSON(w, map[string]any{"auth": true, "valid": false})
		return
	}

	httputil.WriteJSON(w, map[string]any{
		"auth":     true,
		"valid":    true,
		"username": sess.username,
		"role":     string(effectiveRole(sess, r)),
		"remote":   isRemoteRequest(r),
	})
}

// SessionFor returns the authenticated username and role for the request.
// Order: (1) valid cookie → the session's identity; (2) loopback auto-session →
// ("(loopback)", RoleAdmin, true); (3) otherwise ("", "", false).
func (a *Auth) SessionFor(r *http.Request) (username string, role UserRole, ok bool) {
	if sess, sOk := a.getSession(r); sOk {
		return sess.username, effectiveRole(sess, r), true
	}
	if loopbackAutoSession(r) {
		return "(loopback)", RoleAdmin, true
	}
	return "", "", false
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
