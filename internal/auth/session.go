package auth

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"pelicula/internal/store"
)

type userJSON struct {
	Username string `json:"username"`
}

func (a *Auth) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	res, err := a.jf.AuthenticateByName(r.Context(), req.Username, req.Password)
	if err != nil {
		if isBadCredentials(err) {
			a.log.Warn("auth: login rejected", "username", req.Username, "remote", r.RemoteAddr)
			writeError(w, http.StatusUnauthorized, "invalid username or password")
			return
		}
		a.log.Error("auth: jellyfin login failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "login service unavailable")
		return
	}
	name := res.Username
	if name == "" {
		name = req.Username
	}
	if !res.IsAdmin {
		a.log.Warn("auth: login refused for a non-administrator", "username", name, "remote", r.RemoteAddr)
		writeError(w, http.StatusForbidden, "the dashboard is for the Jellyfin administrator")
		return
	}
	sess, err := a.startSession(w, r, name)
	if err != nil {
		a.log.Error("auth: start session failed", "username", name, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.log.Info("auth: login", "username", sess.Username)
	writeJSON(w, http.StatusOK, userJSON{sess.Username})
}

// startSession creates a session for name and sets the cookie.
func (a *Auth) startSession(w http.ResponseWriter, r *http.Request, name string) (*store.Session, error) {
	ctx := r.Context()
	token, err := newToken()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	sess := store.Session{Token: token, Username: name, ExpiresAt: now.Add(a.ttl), CreatedAt: now}
	if err := a.store.CreateSession(ctx, sess); err != nil {
		return nil, err
	}
	// Sessions are only deleted when read after expiry; sweep the rest here.
	if _, err := a.store.PurgeExpiredSessions(ctx); err != nil {
		a.log.Warn("auth: purge expired sessions", "error", err)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(a.ttl / time.Second),
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})
	return &sess, nil
}

func (a *Auth) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
		if err := a.store.DeleteSession(r.Context(), c.Value); err != nil {
			a.log.Warn("auth: delete session", "error", err)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *Auth) handleMe(w http.ResponseWriter, r *http.Request) {
	sess, err := a.sessionFor(r)
	if err != nil {
		a.log.Error("auth: session lookup failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if sess == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, userJSON{sess.Username})
}

// handleCheck backs nginx's auth_request in front of the Sonarr, Radarr,
// Prowlarr and qBittorrent UIs, which run with their own auth off: status
// only, no body, one store lookup. 204 is a live session, 401 is none.
func (a *Auth) handleCheck(w http.ResponseWriter, r *http.Request) {
	sess, err := a.sessionFor(r)
	switch {
	case err != nil:
		a.log.Error("auth: session lookup failed", "error", err)
		w.WriteHeader(http.StatusInternalServerError)
	case sess == nil:
		w.WriteHeader(http.StatusUnauthorized)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// newToken returns 32 random bytes, hex encoded.
func newToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
