package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
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

type User struct {
	Username string   `json:"username"`
	Password string   `json:"password"` // "sha256v2:HEXSALT:HEXHASH" (new) or legacy hex
	Role     UserRole `json:"role"`
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
// mode "password" — single shared password (legacy, PELICULA_AUTH=true)
// mode "users"    — user model from usersFile
type Auth struct {
	mode      string
	password  string
	usersFile string
	users     []User
	sessions  map[string]session
	failures  map[string]*loginAttempts // IP → recent failure timestamps
	mu        sync.RWMutex
}

func NewAuth(mode, password, usersFile string) *Auth {
	a := &Auth{
		mode:      mode,
		password:  password,
		usersFile: usersFile,
		sessions:  make(map[string]session),
		failures:  make(map[string]*loginAttempts),
	}
	if mode == "users" {
		if err := a.loadUsers(); err != nil {
			slog.Warn("could not load users", "component", "auth", "path", usersFile, "error", err)
		}
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

func (a *Auth) loadUsers() error {
	data, err := os.ReadFile(a.usersFile)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Warn("no users file found — all requests will be rejected until users are created", "component", "auth", "path", a.usersFile)
			return nil
		}
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return json.Unmarshal(data, &a.users)
}

// HashPassword generates a salted SHA-256 hash in "sha256v2:SALT:HASH" format.
// HASH = sha256(SALT + ":" + username + ":" + plaintext)
func HashPassword(username, plaintext string) string {
	salt := make([]byte, 16)
	rand.Read(salt) //nolint:errcheck — always succeeds on Go 1.20+
	saltHex := hex.EncodeToString(salt)
	h := sha256.Sum256([]byte(saltHex + ":" + username + ":" + plaintext))
	return "sha256v2:" + saltHex + ":" + hex.EncodeToString(h[:])
}

// verifyPassword checks plaintext against a stored hash.
// Supports both "sha256v2:SALT:HASH" (salted) and the legacy unsalted format.
func verifyPassword(username, plaintext, stored string) bool {
	parts := strings.SplitN(stored, ":", 3)
	if len(parts) == 3 && parts[0] == "sha256v2" {
		saltHex, expectedHash := parts[1], parts[2]
		h := sha256.Sum256([]byte(saltHex + ":" + username + ":" + plaintext))
		computed := hex.EncodeToString(h[:])
		return subtle.ConstantTimeCompare([]byte(computed), []byte(expectedHash)) == 1
	}
	// Legacy: unsalted sha256(username:password)
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s", username, plaintext)))
	computed := hex.EncodeToString(h[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(stored)) == 1
}

func (a *Auth) lookupUser(username, plaintext string) (User, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, u := range a.users {
		if u.Username == username && verifyPassword(username, plaintext, u.Password) {
			return u, true
		}
	}
	return User{}, false
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

	var role UserRole
	switch a.mode {
	case "password":
		if subtle.ConstantTimeCompare([]byte(req.Password), []byte(a.password)) == 0 {
			a.recordFailure(ip)
			writeError(w, "invalid password", http.StatusUnauthorized)
			return
		}
		role = RoleAdmin // single-password mode is always admin
	case "users":
		u, ok := a.lookupUser(req.Username, req.Password)
		if !ok {
			a.recordFailure(ip)
			writeError(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		role = u.Role
	default:
		writeError(w, "auth misconfigured", http.StatusInternalServerError)
		return
	}

	token := generateToken()
	expiry := time.Now().Add(24 * time.Hour)

	a.mu.Lock()
	a.sessions[token] = session{username: req.Username, role: role, expiry: expiry}
	a.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     "pelicula_session",
		Value:    token,
		Path:     "/",
		Expires:  expiry,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
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

func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}
