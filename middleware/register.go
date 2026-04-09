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
)

// openRegistration is set in main.go from PELICULA_OPEN_REGISTRATION.
var openRegistration bool

// initialSetupMu serialises the initial-setup registration so that only one
// request can observe IsEmpty()==true and claim the admin role.
var initialSetupMu sync.Mutex

func handleOpenRegCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	initialSetup := false
	if authMiddleware != nil && authMiddleware.rolesStore != nil {
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
		initialSetupMu.Unlock()
		// Detect username-already-taken (Jellyfin returns 400)
		var jErr *jellyfinHTTPError
		if errors.As(err, &jErr) && jErr.StatusCode == http.StatusBadRequest {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "that username is already taken",
				"code":  "username_taken",
			})
			return
		}
		if authMiddleware != nil {
			authMiddleware.recordFailure(ip)
		}
		slog.Error("open registration failed", "component", "register", "username", req.Username, "error", err)
		writeError(w, "could not create account", http.StatusBadGateway)
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
