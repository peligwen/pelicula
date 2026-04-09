// Peligrosa: trust boundary layer.
// Open LAN registration: optional public account creation without invite tokens.
// LAN-only (requireLocalOriginStrict in route table), viewer role only.
// See ../PELIGROSA.md.
package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// openRegistration is set in main.go from PELICULA_OPEN_REGISTRATION.
var openRegistration bool

// initialSetupMu serialises the initial-setup registration so that only one
// request can observe IsEmpty()==true and claim the admin role.
var initialSetupMu sync.Mutex

// handleGeneratePassword returns a fresh passphrase suggestion.
// Public endpoint — no auth required; used by the registration UI.
// Rate-limited per IP via the auth limiter.
func handleGeneratePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ip := clientIP(r)
	if authMiddleware != nil && authMiddleware.isRateLimited(ip) {
		writeError(w, "too many requests — try again later", http.StatusTooManyRequests)
		return
	}
	writeJSON(w, map[string]string{"password": generateReadablePassword()})
}

func handleOpenRegCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	initialSetup := false
	if authMiddleware != nil && authMiddleware.rolesStore != nil {
		// IsEmpty() has its own internal locking; no initialSetupMu needed here
		// because this endpoint is advisory (frontend uses it to show/hide the
		// "Create Admin Account" heading) — the actual gate is in handleOpenRegister.
		// Note: the pelicula-internal Jellyfin service user is never in this table.
		initialSetup = authMiddleware.rolesStore.IsEmpty()
	}
	writeJSON(w, map[string]any{
		"open_registration": openRegistration,
		"initial_setup":     initialSetup,
	})
}

func handleOpenRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Serialise the initial-setup check so only one request can claim admin.
	// Note: IsEmpty() checks Pelicula's own roles table — the pelicula-internal
	// Jellyfin service user is never inserted there, so it doesn't affect this.
	initialSetupMu.Lock()
	initialSetup := authMiddleware != nil && authMiddleware.rolesStore != nil && authMiddleware.rolesStore.IsEmpty()
	if !openRegistration && !initialSetup {
		initialSetupMu.Unlock()
		writeError(w, "open registration is not enabled", http.StatusForbidden)
		return
	}

	if authMiddleware != nil && authMiddleware.IsOffMode() {
		initialSetupMu.Unlock()
		writeError(w, "registration requires auth to be enabled (PELICULA_AUTH=jellyfin)", http.StatusForbidden)
		return
	}

	// Rate-limit by IP — reuse the auth limiter.
	ip := clientIP(r)
	if authMiddleware != nil && authMiddleware.isRateLimited(ip) {
		initialSetupMu.Unlock()
		writeError(w, "too many requests — try again later", http.StatusTooManyRequests)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		initialSetupMu.Unlock()
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if !validUsername(req.Username) {
		initialSetupMu.Unlock()
		if req.Username == "" {
			writeError(w, "username is required", http.StatusBadRequest)
		} else {
			writeError(w, "username is invalid (1–64 chars, no leading/trailing whitespace, no slashes)", http.StatusBadRequest)
		}
		return
	}
	if req.Password == "" {
		initialSetupMu.Unlock()
		writeError(w, "password is required", http.StatusBadRequest)
		return
	}

	jellyfinID, err := CreateJellyfinUser(services, req.Username, req.Password)
	if err != nil {
		// Detect username-already-taken (Jellyfin returns 400) before retrying.
		var jErr *jellyfinHTTPError
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
		jellyfinID, err = CreateJellyfinUser(services, req.Username, req.Password)
	}
	if err != nil {
		initialSetupMu.Unlock()
		if authMiddleware != nil {
			authMiddleware.recordFailure(ip)
		}
		slog.Error("open registration failed", "component", "register", "username", req.Username, "error", err)
		writeError(w, "Could not create account — Jellyfin may still be starting up. Wait a moment and try again.", http.StatusBadGateway)
		return
	}

	// First user gets admin role (initial setup); subsequent users get viewer.
	role := RoleViewer
	if initialSetup {
		role = RoleAdmin
		slog.Info("initial setup: first admin account created", "component", "register", "username", req.Username)
	}
	if authMiddleware != nil && authMiddleware.rolesStore != nil {
		if err := authMiddleware.rolesStore.Upsert(jellyfinID, req.Username, role); err != nil {
			slog.Warn("failed to persist role for open-reg user", "component", "register", "username", req.Username, "error", err)
		}
	}
	initialSetupMu.Unlock()

	if !initialSetup {
		slog.Info("open registration: account created", "component", "register", "username", req.Username)
	}
	writeJSON(w, map[string]string{"status": "ok"})
}
