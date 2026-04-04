package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

type Auth struct {
	enabled  bool
	password string
	sessions map[string]time.Time
	mu       sync.RWMutex
}

func NewAuth(enabled bool, password string) *Auth {
	return &Auth{
		enabled:  enabled,
		password: password,
		sessions: make(map[string]time.Time),
	}
}

func (a *Auth) Guard(next http.Handler) http.Handler {
	if !a.enabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("pelicula_session")
		if err != nil {
			writeError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		a.mu.RLock()
		expiry, ok := a.sessions[cookie.Value]
		a.mu.RUnlock()

		if !ok || time.Now().After(expiry) {
			writeError(w, "session expired", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Auth) HandleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !a.enabled {
		writeJSON(w, map[string]any{"auth": false, "message": "auth disabled"})
		return
	}

	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.Password != a.password {
		writeError(w, "invalid password", http.StatusUnauthorized)
		return
	}

	token := generateToken()
	expiry := time.Now().Add(24 * time.Hour)

	a.mu.Lock()
	a.sessions[token] = expiry
	a.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     "pelicula_session",
		Value:    token,
		Path:     "/",
		Expires:  expiry,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	writeJSON(w, map[string]string{"status": "ok"})
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
	if !a.enabled {
		writeJSON(w, map[string]any{"auth": false, "valid": true})
		return
	}

	cookie, err := r.Cookie("pelicula_session")
	if err != nil {
		writeJSON(w, map[string]any{"auth": true, "valid": false})
		return
	}

	a.mu.RLock()
	expiry, ok := a.sessions[cookie.Value]
	a.mu.RUnlock()

	valid := ok && time.Now().Before(expiry)
	writeJSON(w, map[string]any{"auth": true, "valid": valid})
}

func generateToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}
