// Package auth is Pelicula's login and session layer.
//
// Jellyfin is the identity provider and the dashboard is for its
// administrator: passwords are only ever checked by Jellyfin, and a login
// succeeds only when Jellyfin reports the user as an administrator. Other
// Jellyfin users never sign in here; they use Jellyfin itself. Pelicula keeps
// a cookie session per login (package store) and this package wires that
// into HTTP: login/logout/me/check, the Guard middleware, and CSRF
// protection.
//
// # Talking to Jellyfin
//
// The package never imports the Jellyfin client. It declares the one small
// interface it needs (Identity) and the integrator passes anything that
// satisfies it. Identity needs a thin adapter because Go has no covariant
// return types; IdentityFunc keeps it to a few lines:
//
//	auth.IdentityFunc(func(ctx context.Context, user, pass string) (*auth.LoginResult, error) {
//		r, err := jf.AuthenticateByName(ctx, user, pass)
//		if err != nil {
//			return nil, err
//		}
//		return &auth.LoginResult{Username: r.Username, IsAdmin: r.IsAdmin}, nil
//	})
//
// # Error classification
//
// An error from Identity.AuthenticateByName is "bad credentials" (HTTP 401)
// when errors.Is(err, ErrBadCredentials), or when its message contains the
// standalone number 401 or the word "unauthorized" (so jellyfin.ErrUnauthorized
// and an HTTPError for status 401 both qualify without wrapping). Any other
// error, including timeouts and refused connections, is "Jellyfin unavailable"
// (HTTP 503).
//
// Login attempts are rate limited by nginx, not here.
package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"pelicula/internal/store"
)

// CookieName is the session cookie.
const CookieName = "pelicula_session"

const defaultSessionTTL = 30 * 24 * time.Hour

// ErrBadCredentials may be wrapped by an Identity to be classified as a bad
// login without relying on message matching. See the package comment.
var ErrBadCredentials = errors.New("auth: bad credentials")

var badCredsRE = regexp.MustCompile(`(?i)\b401\b|unauthorized`)

// LoginResult is what Jellyfin tells us about a user who just authenticated.
type LoginResult struct {
	Username string // canonical name as Jellyfin stores it
	IsAdmin  bool
}

// Identity authenticates a username and password against Jellyfin.
type Identity interface {
	AuthenticateByName(ctx context.Context, username, password string) (*LoginResult, error)
}

// IdentityFunc adapts a function to Identity.
type IdentityFunc func(ctx context.Context, username, password string) (*LoginResult, error)

// AuthenticateByName calls f.
func (f IdentityFunc) AuthenticateByName(ctx context.Context, username, password string) (*LoginResult, error) {
	return f(ctx, username, password)
}

// Deps are the Auth dependencies.
type Deps struct {
	Store      *store.Store
	Jellyfin   Identity
	SessionTTL time.Duration // default 30 days
	Log        *slog.Logger
}

// Auth serves the auth routes and guards other handlers.
type Auth struct {
	store *store.Store
	jf    Identity
	ttl   time.Duration
	log   *slog.Logger
}

// New builds an Auth. SessionTTL defaults to 30 days, Log to slog.Default().
func New(d Deps) *Auth {
	a := &Auth{store: d.Store, jf: d.Jellyfin, ttl: d.SessionTTL, log: d.Log}
	if a.ttl <= 0 {
		a.ttl = defaultSessionTTL
	}
	if a.log == nil {
		a.log = slog.Default()
	}
	return a
}

// Routes registers the auth routes on mux.
func (a *Auth) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/login", a.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", a.handleLogout)
	mux.HandleFunc("GET /api/auth/me", a.handleMe)
	mux.HandleFunc("GET /api/auth/check", a.handleCheck)
}

type sessionKey struct{}

// SessionFrom returns the session Guard attached to ctx, or nil when the
// request did not pass through Guard.
func SessionFrom(ctx context.Context) *store.Session {
	s, _ := ctx.Value(sessionKey{}).(*store.Session)
	return s
}

// Guard requires a valid session. It answers 401 {"error":"unauthorized"}
// without one and otherwise stores the session in the request context (see
// SessionFrom).
func (a *Auth) Guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, sess)))
	})
}

// GuardFunc is Guard for a HandlerFunc.
func (a *Auth) GuardFunc(h http.HandlerFunc) http.Handler {
	return a.Guard(h)
}

// sessionFor returns the live session named by the request cookie, or nil.
func (a *Auth) sessionFor(r *http.Request) (*store.Session, error) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	return a.store.GetSession(r.Context(), c.Value)
}

func isBadCredentials(err error) bool {
	return errors.Is(err, ErrBadCredentials) || badCredsRE.MatchString(err.Error())
}
