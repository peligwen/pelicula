package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
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
	Password string   `json:"password"` // hex(sha256(username + ":" + plaintext))
	Role     UserRole `json:"role"`
}

type session struct {
	username string
	role     UserRole
	expiry   time.Time
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
	mu        sync.RWMutex
}

func NewAuth(mode, password, usersFile string) *Auth {
	a := &Auth{
		mode:      mode,
		password:  password,
		usersFile: usersFile,
		sessions:  make(map[string]session),
	}
	if mode == "users" {
		if err := a.loadUsers(); err != nil {
			log.Printf("[auth] warning: could not load users from %s: %v", usersFile, err)
		}
	}
	return a
}

func (a *Auth) loadUsers() error {
	data, err := os.ReadFile(a.usersFile)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[auth] no users file at %s — all requests will be rejected until users are created", a.usersFile)
			return nil
		}
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return json.Unmarshal(data, &a.users)
}

// HashPassword returns the stored hash for a given username and plaintext password.
func HashPassword(username, plaintext string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s", username, plaintext)))
	return hex.EncodeToString(h[:])
}

func (a *Auth) lookupUser(username, plaintext string) (User, bool) {
	hash := HashPassword(username, plaintext)
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, u := range a.users {
		if u.Username == username && u.Password == hash {
			return u, true
		}
	}
	return User{}, false
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
		if req.Password != a.password {
			writeError(w, "invalid password", http.StatusUnauthorized)
			return
		}
		role = RoleAdmin // single-password mode is always admin
	case "users":
		u, ok := a.lookupUser(req.Username, req.Password)
		if !ok {
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
