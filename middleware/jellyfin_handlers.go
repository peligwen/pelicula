package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"pelicula-api/httputil"
	"strings"
)

// handleUsers handles GET /api/pelicula/users (list) and POST /api/pelicula/users (create).
func handleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if authMiddleware == nil {
			httputil.WriteError(w, "authentication not configured", http.StatusServiceUnavailable)
			return
		}
		users, err := ListJellyfinUsers(services)
		if err != nil {
			slog.Error("list jellyfin users failed", "component", "users", "error", err)
			httputil.WriteError(w, "could not list users", http.StatusBadGateway)
			return
		}
		httputil.WriteJSON(w, users)

	case http.MethodPost:
		if authMiddleware == nil {
			httputil.WriteError(w, "authentication not configured", http.StatusServiceUnavailable)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB cap
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.WriteError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if !validUsername(req.Username) {
			if req.Username == "" {
				httputil.WriteError(w, "username is required", http.StatusBadRequest)
			} else {
				httputil.WriteError(w, "username is invalid (1–64 chars, no leading/trailing whitespace, no control chars or slashes)", http.StatusBadRequest)
			}
			return
		}
		if _, err := CreateJellyfinUser(services, req.Username, req.Password); err != nil {
			slog.Error("create jellyfin user failed", "component", "users", "username", req.Username, "error", err)
			if errors.Is(err, ErrPasswordRequired) {
				httputil.WriteError(w, "password is required", http.StatusBadRequest)
				return
			}
			var jErr *jellyfinHTTPError
			if errors.As(err, &jErr) && jErr.StatusCode == http.StatusBadRequest {
				httputil.WriteError(w, "could not create user: name already taken or invalid", http.StatusBadRequest)
				return
			}
			httputil.WriteError(w, "could not create user", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusCreated)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleUsersWithID dispatches requests to /api/pelicula/users/{id} and
// /api/pelicula/users/{id}/password based on path suffix and HTTP method.
func handleUsersWithID(w http.ResponseWriter, r *http.Request) {
	// Strip the route prefix to get "{id}" or "{id}/password".
	tail := strings.TrimPrefix(r.URL.Path, "/api/pelicula/users/")

	if strings.HasSuffix(tail, "/password") {
		id := strings.TrimSuffix(tail, "/password")
		if !validJellyfinID(id) {
			httputil.WriteError(w, "invalid user ID", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleUserPassword(w, r, id)
		return
	}

	if strings.HasSuffix(tail, "/disable") {
		id := strings.TrimSuffix(tail, "/disable")
		if !validJellyfinID(id) {
			httputil.WriteError(w, "invalid user ID", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := SetJellyfinUserDisabled(services, id, true); err != nil {
			slog.Error("disable user failed", "component", "users", "userId", id, "error", err)
			httputil.WriteError(w, "could not disable user", http.StatusBadGateway)
			return
		}
		httputil.WriteJSON(w, map[string]string{"status": "ok"})
		return
	}

	if strings.HasSuffix(tail, "/enable") {
		id := strings.TrimSuffix(tail, "/enable")
		if !validJellyfinID(id) {
			httputil.WriteError(w, "invalid user ID", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := SetJellyfinUserDisabled(services, id, false); err != nil {
			slog.Error("enable user failed", "component", "users", "userId", id, "error", err)
			httputil.WriteError(w, "could not enable user", http.StatusBadGateway)
			return
		}
		httputil.WriteJSON(w, map[string]string{"status": "ok"})
		return
	}

	if strings.HasSuffix(tail, "/library") {
		id := strings.TrimSuffix(tail, "/library")
		if !validJellyfinID(id) {
			httputil.WriteError(w, "invalid user ID", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req struct {
			Movies bool `json:"movies"`
			TV     bool `json:"tv"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.WriteError(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		if err := SetJellyfinUserLibraryAccess(services, id, req.Movies, req.TV); err != nil {
			slog.Error("set library access failed", "component", "users", "userId", id, "error", err)
			httputil.WriteError(w, "could not update library access", http.StatusBadGateway)
			return
		}
		httputil.WriteJSON(w, map[string]string{"status": "ok"})
		return
	}

	if !validJellyfinID(tail) {
		httputil.WriteError(w, "invalid user ID", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		handleUserDelete(w, r, tail)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleUserDelete handles DELETE /api/pelicula/users/{id}.
// It prevents deletion of the last admin account.
func handleUserDelete(w http.ResponseWriter, r *http.Request, id string) {
	users, err := ListJellyfinUsers(services)
	if err != nil {
		slog.Error("list users for delete check failed", "component", "users", "error", err)
		httputil.WriteError(w, "could not verify user before deletion", http.StatusBadGateway)
		return
	}
	var target *JellyfinUser
	adminCount := 0
	for i := range users {
		if users[i].ID == id {
			target = &users[i]
		}
		if users[i].IsAdmin {
			adminCount++
		}
	}
	if target == nil {
		httputil.WriteError(w, "user not found", http.StatusNotFound)
		return
	}
	if target.Name == jellyfinServiceUser {
		httputil.WriteError(w, "cannot delete internal service account", http.StatusForbidden)
		return
	}
	if target.IsAdmin && adminCount <= 1 {
		httputil.WriteError(w, "cannot delete the only admin account", http.StatusConflict)
		return
	}
	if err := DeleteJellyfinUser(services, id); err != nil {
		slog.Error("delete jellyfin user failed", "component", "users", "userId", id, "error", err)
		httputil.WriteError(w, "could not delete user", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUserPassword handles POST /api/pelicula/users/{id}/password.
// Resets the user's password; no current password required (admin operation).
func handleUserPassword(w http.ResponseWriter, r *http.Request, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB cap
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := SetJellyfinUserPassword(services, id, req.Password); err != nil {
		slog.Error("reset password failed", "component", "users", "userId", id, "error", err)
		if errors.Is(err, ErrPasswordRequired) {
			httputil.WriteError(w, "password is required", http.StatusBadRequest)
			return
		}
		var jErr *jellyfinHTTPError
		if errors.As(err, &jErr) && jErr.StatusCode == http.StatusBadRequest {
			httputil.WriteError(w, "could not set password: invalid or rejected by Jellyfin", http.StatusBadRequest)
			return
		}
		httputil.WriteError(w, "could not set password", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSessions handles GET /api/pelicula/sessions.
// Returns active Jellyfin sessions for the now-playing dashboard card.
func handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sessions, err := GetJellyfinSessions(services)
	if err != nil {
		slog.Error("list sessions failed", "component", "sessions", "error", err)
		httputil.WriteError(w, "could not list sessions", http.StatusBadGateway)
		return
	}
	httputil.WriteJSON(w, sessions)
}
