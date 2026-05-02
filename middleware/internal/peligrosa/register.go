// Peligrosa: trust boundary layer.
// Open LAN registration: optional public account creation without invite tokens.
// LAN-only (httputil.RequireLocalOriginStrict in route table), viewer role only.
// See ../../docs/PELIGROSA.md.
package peligrosa

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"pelicula-api/clients"
	"pelicula-api/httputil"
	"sync"
	"time"
)

// OpenRegistration controls whether new accounts can be created without an invite.
// Set from main via the package-level accessor SetOpenRegistration.
var OpenRegistration bool

// initialSetupMu serialises the initial-setup registration so that only one
// request can observe IsEmpty()==true and claim the admin role.
var initialSetupMu sync.Mutex

// SetOpenRegistration sets the open-registration flag from main.
func SetOpenRegistration(v bool) {
	OpenRegistration = v
}

// HandleGeneratePassword returns a fresh passphrase suggestion.
// Public endpoint — no auth required; used by the registration UI.
// Rate-limited per IP via the auth limiter.
func (d *Deps) HandleGeneratePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ip := httputil.ClientIP(r)
	if d.Auth != nil && d.Auth.isRateLimited(r.Context(), ip) {
		httputil.WriteError(w, "too many requests — try again later", http.StatusTooManyRequests)
		return
	}
	httputil.WriteJSON(w, map[string]string{"password": d.genPassword()})
}

func (a *Auth) HandleOpenRegCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	initialSetup := false
	if a != nil && a.rolesStore != nil {
		// IsEmpty() has its own internal locking; no initialSetupMu needed here
		// because this endpoint is advisory (frontend uses it to show/hide the
		// "Create Admin Account" heading) — the actual gate is in HandleOpenRegister.
		// Note: the pelicula-internal Jellyfin service user is never in this table.
		initialSetup = a.rolesStore.IsEmpty(r.Context())
	}
	httputil.WriteJSON(w, map[string]any{
		"open_registration": OpenRegistration,
		"initial_setup":     initialSetup,
	})
}

func (a *Auth) HandleOpenRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Serialise the initial-setup check so only one request can claim admin.
	// Note: IsEmpty() checks Pelicula's own roles table — the pelicula-internal
	// Jellyfin service user is never inserted there, so it doesn't affect this.
	initialSetupMu.Lock()
	initialSetup := a != nil && a.rolesStore != nil && a.rolesStore.IsEmpty(r.Context())
	if !OpenRegistration && !initialSetup {
		initialSetupMu.Unlock()
		httputil.WriteError(w, "open registration is not enabled", http.StatusForbidden)
		return
	}

	// Rate-limit by IP — reuse the auth limiter.
	ip := httputil.ClientIP(r)
	if a != nil && a.isRateLimited(r.Context(), ip) {
		initialSetupMu.Unlock()
		httputil.WriteError(w, "too many requests — try again later", http.StatusTooManyRequests)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		initialSetupMu.Unlock()
		httputil.WriteError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if !clients.IsValidUsername(req.Username) {
		initialSetupMu.Unlock()
		if req.Username == "" {
			httputil.WriteError(w, "username is required", http.StatusBadRequest)
		} else {
			httputil.WriteError(w, "username is invalid (1–64 chars, no leading/trailing whitespace, no slashes)", http.StatusBadRequest)
		}
		return
	}
	if req.Password == "" {
		initialSetupMu.Unlock()
		httputil.WriteError(w, "password is required", http.StatusBadRequest)
		return
	}

	jellyfinID, err := a.jellyfin.CreateUser(r.Context(), req.Username, req.Password)
	if err != nil {
		// Detect username-already-taken (Jellyfin returns 400) before retrying.
		var jErr *clients.JellyfinHTTPError
		if errors.As(err, &jErr) && jErr.StatusCode == http.StatusBadRequest {
			initialSetupMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "that username is already taken",
				"code":  "username_taken",
			})
			return
		}
		// Jellyfin may still be initialising — retry once after a short delay.
		time.Sleep(2 * time.Second)
		jellyfinID, err = a.jellyfin.CreateUser(r.Context(), req.Username, req.Password)
	}
	if err != nil {
		initialSetupMu.Unlock()
		if a != nil {
			a.recordFailure(r.Context(), ip)
		}
		slog.Error("open registration failed", "component", "register", "username", req.Username, "error", err)
		httputil.WriteError(w, "Could not create account — Jellyfin may still be starting up. Wait a moment and try again.", http.StatusBadGateway)
		return
	}

	// First user gets admin role (initial setup); subsequent users get viewer.
	role := RoleViewer
	if initialSetup {
		role = RoleAdmin
		slog.Info("initial setup: first admin account created", "component", "register", "username", req.Username)
	}
	if a != nil && a.rolesStore != nil {
		if err := a.rolesStore.Upsert(r.Context(), jellyfinID, req.Username, role); err != nil {
			slog.Warn("failed to persist role for open-reg user", "component", "register", "username", req.Username, "error", err)
		}
	}
	initialSetupMu.Unlock()

	if !initialSetup {
		slog.Info("open registration: account created", "component", "register", "username", req.Username)
	}
	httputil.WriteJSON(w, map[string]string{"status": "ok"})
}
