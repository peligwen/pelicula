// Package auth is Pelicula's session and role layer.
//
// Jellyfin is the identity provider: passwords are only ever checked by
// Jellyfin. Pelicula stores a role per username, a session per login, and
// invites for registration (all in package store). This package wires those
// into HTTP: login/logout/me/check, invite-based registration, invite
// management, the Guard middleware, and CSRF protection.
//
// # Talking to Jellyfin
//
// The package never imports the Jellyfin client. It declares the three small
// interfaces it needs (Identity, UserCreator, UserAdmin) and the integrator
// passes anything that satisfies them. UserCreator and UserAdmin match
// (*jellyfin.Admin).Token and (*jellyfin.Client).CreateUser as written in the
// build contract. Identity needs a thin adapter because Go has no covariant
// return types; IdentityFunc keeps it to a few lines:
//
//	auth.IdentityFunc(func(ctx context.Context, user, pass string) (*auth.LoginResult, error) {
//		r, err := jf.AuthenticateByName(ctx, user, pass)
//		if err != nil {
//			return nil, err
//		}
//		return &auth.LoginResult{Token: r.Token, UserID: r.UserID, Username: r.Username, IsAdmin: r.IsAdmin}, nil
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
// An error from UserAdmin.CreateUser is "username taken" (HTTP 409) when
// errors.Is(err, ErrUserExists) or its message contains the standalone number
// 400 or 409 (Jellyfin answers 400 for a duplicate name). Any other error, and
// any error from UserCreator.Token, is HTTP 503.
//
// Login attempts are rate limited by nginx, not here.
package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"pelicula/internal/store"
)

// CookieName is the session cookie.
const CookieName = "pelicula_session"

const defaultSessionTTL = 30 * 24 * time.Hour

// Sentinel errors an Identity or UserAdmin may wrap to be classified without
// relying on message matching. See the package comment.
var (
	ErrBadCredentials = errors.New("auth: bad credentials")
	ErrUserExists     = errors.New("auth: user already exists")
)

var (
	badCredsRE = regexp.MustCompile(`(?i)\b401\b|unauthorized`)
	conflictRE = regexp.MustCompile(`\b(400|409)\b`)
)

// LoginResult is what Jellyfin tells us about a user who just authenticated.
type LoginResult struct {
	Token    string // Jellyfin access token for the user
	UserID   string
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

// UserCreator hands out a Jellyfin admin token (cached and refreshed by the
// implementation).
type UserCreator interface {
	Token(ctx context.Context) (string, error)
}

// UserAdmin creates Jellyfin users with an admin token and returns the new
// user's ID.
type UserAdmin interface {
	CreateUser(ctx context.Context, token, name, password string) (string, error)
}

// Deps are the Auth dependencies.
type Deps struct {
	Store      *store.Store
	Jellyfin   Identity      // login
	Admin      UserCreator   // admin token, used when redeeming an invite
	Users      UserAdmin     // user creation, used when redeeming an invite
	SessionTTL time.Duration // default 30 days
	Log        *slog.Logger
}

// Auth serves the auth routes and guards other handlers.
type Auth struct {
	store *store.Store
	jf    Identity
	admin UserCreator
	users UserAdmin
	ttl   time.Duration
	log   *slog.Logger

	// regMu serialises invite redemption so the check-invite, create-user,
	// use-invite sequence cannot create two Jellyfin users for one invite.
	regMu sync.Mutex
}

// New builds an Auth. SessionTTL defaults to 30 days, Log to slog.Default().
func New(d Deps) *Auth {
	a := &Auth{
		store: d.Store,
		jf:    d.Jellyfin,
		admin: d.Admin,
		users: d.Users,
		ttl:   d.SessionTTL,
		log:   d.Log,
	}
	if a.ttl <= 0 {
		a.ttl = defaultSessionTTL
	}
	if a.log == nil {
		a.log = slog.Default()
	}
	return a
}

// Routes registers the auth, registration and invite routes on mux.
func (a *Auth) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/login", a.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", a.handleLogout)
	mux.HandleFunc("GET /api/auth/me", a.handleMe)
	mux.HandleFunc("GET /api/auth/check", a.handleCheck)

	mux.HandleFunc("GET /api/register/{code}", a.handleRegisterCheck)
	mux.HandleFunc("POST /api/register", a.handleRegister)

	mux.Handle("GET /api/invites", a.GuardFunc(store.RoleAdmin, a.handleInviteList))
	mux.Handle("POST /api/invites", a.GuardFunc(store.RoleAdmin, a.handleInviteCreate))
	mux.Handle("DELETE /api/invites/{code}", a.GuardFunc(store.RoleAdmin, a.handleInviteDelete))
}

type sessionKey struct{}

// SessionFrom returns the session Guard attached to ctx, or nil when the
// request did not pass through Guard.
func SessionFrom(ctx context.Context) *store.Session {
	s, _ := ctx.Value(sessionKey{}).(*store.Session)
	return s
}

// Guard requires a valid session whose role is at least min. It answers 401
// {"error":"unauthorized"} without a session and 403 {"error":"forbidden"}
// with too low a role, and otherwise stores the session in the request context
// (see SessionFrom).
func (a *Auth) Guard(min store.Role, h http.Handler) http.Handler {
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
		if !sess.Role.AtLeast(min) {
			writeError(w, http.StatusForbidden, "forbidden")
			return
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, sess)))
	})
}

// GuardFunc is Guard for a HandlerFunc.
func (a *Auth) GuardFunc(min store.Role, h http.HandlerFunc) http.Handler {
	return a.Guard(min, h)
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

func isConflict(err error) bool {
	return errors.Is(err, ErrUserExists) || conflictRE.MatchString(strings.ToLower(err.Error()))
}
