// Package jellyfin provides Jellyfin HTTP handlers and business logic for
// the pelicula-api middleware.
//
// Handler wraps a typed *jellyfin.Client and exposes the route handlers for
// user management and session listing. No package-level globals — wire this
// from main() via NewHandler.
package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	jfclient "pelicula-api/internal/clients/jellyfin"

	"pelicula-api/httputil"
)

// Handler holds the dependencies for Jellyfin HTTP handlers.
type Handler struct {
	// Client is the underlying Jellyfin HTTP client.
	Client *jfclient.Client
	// Auth is a function that returns a valid Jellyfin token for the service
	// account. This is called before any authenticated Jellyfin request.
	Auth func(context.Context) (string, error)
	// ServiceUser is the name of the internal service account ("pelicula-internal").
	ServiceUser string
}

// NewHandler constructs a Handler.
// auth is a function that returns a Jellyfin API key or session token for
// the pelicula-internal service account; it is called before each
// authenticated request.
func NewHandler(client *jfclient.Client, auth func(context.Context) (string, error), serviceUser string) *Handler {
	return &Handler{
		Client:      client,
		Auth:        auth,
		ServiceUser: serviceUser,
	}
}

// HandleUsers handles GET /api/pelicula/users and POST /api/pelicula/users.
func (h *Handler) HandleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		users, err := h.ListUsers(r.Context())
		if err != nil {
			slog.Error("list jellyfin users failed", "component", "users", "error", err)
			httputil.WriteError(w, "could not list users", http.StatusBadGateway)
			return
		}
		httputil.WriteJSON(w, users)

	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB cap
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.WriteError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if !ValidUsername(req.Username) {
			if req.Username == "" {
				httputil.WriteError(w, "username is required", http.StatusBadRequest)
			} else {
				httputil.WriteError(w, "username is invalid (1–64 chars, no leading/trailing whitespace, no control chars or slashes)", http.StatusBadRequest)
			}
			return
		}
		if _, err := h.CreateUser(r.Context(), req.Username, req.Password); err != nil {
			slog.Error("create jellyfin user failed", "component", "users", "username", req.Username, "error", err)
			if errors.Is(err, ErrPasswordRequired) {
				httputil.WriteError(w, "password is required", http.StatusBadRequest)
				return
			}
			var jErr *jfclient.HTTPError
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

// HandleUsersWithID dispatches requests to /api/pelicula/users/{id} and
// sub-paths based on path suffix and HTTP method.
func (h *Handler) HandleUsersWithID(w http.ResponseWriter, r *http.Request) {
	// Strip the route prefix to get "{id}" or "{id}/password" etc.
	tail := strings.TrimPrefix(r.URL.Path, "/api/pelicula/users/")

	if strings.HasSuffix(tail, "/password") {
		id := strings.TrimSuffix(tail, "/password")
		if !ValidJellyfinID(id) {
			httputil.WriteError(w, "invalid user ID", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.handleUserPassword(w, r, id)
		return
	}

	if strings.HasSuffix(tail, "/disable") {
		id := strings.TrimSuffix(tail, "/disable")
		if !ValidJellyfinID(id) {
			httputil.WriteError(w, "invalid user ID", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := h.SetUserDisabled(r.Context(), id, true); err != nil {
			slog.Error("disable user failed", "component", "users", "userId", id, "error", err)
			httputil.WriteError(w, "could not disable user", http.StatusBadGateway)
			return
		}
		httputil.WriteJSON(w, map[string]string{"status": "ok"})
		return
	}

	if strings.HasSuffix(tail, "/enable") {
		id := strings.TrimSuffix(tail, "/enable")
		if !ValidJellyfinID(id) {
			httputil.WriteError(w, "invalid user ID", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := h.SetUserDisabled(r.Context(), id, false); err != nil {
			slog.Error("enable user failed", "component", "users", "userId", id, "error", err)
			httputil.WriteError(w, "could not enable user", http.StatusBadGateway)
			return
		}
		httputil.WriteJSON(w, map[string]string{"status": "ok"})
		return
	}

	if strings.HasSuffix(tail, "/library") {
		id := strings.TrimSuffix(tail, "/library")
		if !ValidJellyfinID(id) {
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
		if err := h.SetUserLibraryAccess(r.Context(), id, req.Movies, req.TV); err != nil {
			slog.Error("set library access failed", "component", "users", "userId", id, "error", err)
			httputil.WriteError(w, "could not update library access", http.StatusBadGateway)
			return
		}
		httputil.WriteJSON(w, map[string]string{"status": "ok"})
		return
	}

	if !ValidJellyfinID(tail) {
		httputil.WriteError(w, "invalid user ID", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		h.handleUserDelete(w, r, tail)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleUserDelete handles DELETE /api/pelicula/users/{id}.
// It prevents deletion of the last admin account.
func (h *Handler) handleUserDelete(w http.ResponseWriter, r *http.Request, id string) {
	users, err := h.ListUsers(r.Context())
	if err != nil {
		slog.Error("list users for delete check failed", "component", "users", "error", err)
		httputil.WriteError(w, "could not verify user before deletion", http.StatusBadGateway)
		return
	}
	var target *User
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
	if target.Name == h.ServiceUser {
		httputil.WriteError(w, "cannot delete internal service account", http.StatusForbidden)
		return
	}
	if target.IsAdmin && adminCount <= 1 {
		httputil.WriteError(w, "cannot delete the only admin account", http.StatusConflict)
		return
	}
	if err := h.DeleteUser(r.Context(), id); err != nil {
		slog.Error("delete jellyfin user failed", "component", "users", "userId", id, "error", err)
		httputil.WriteError(w, "could not delete user", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUserPassword handles POST /api/pelicula/users/{id}/password.
func (h *Handler) handleUserPassword(w http.ResponseWriter, r *http.Request, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB cap
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := h.SetUserPassword(r.Context(), id, req.Password); err != nil {
		slog.Error("reset password failed", "component", "users", "userId", id, "error", err)
		if errors.Is(err, ErrPasswordRequired) {
			httputil.WriteError(w, "password is required", http.StatusBadRequest)
			return
		}
		var jErr *jfclient.HTTPError
		if errors.As(err, &jErr) && jErr.StatusCode == http.StatusBadRequest {
			httputil.WriteError(w, "could not set password: invalid or rejected by Jellyfin", http.StatusBadRequest)
			return
		}
		httputil.WriteError(w, "could not set password", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleSessions handles GET /api/pelicula/sessions.
func (h *Handler) HandleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sessions, err := h.GetSessions(r.Context())
	if err != nil {
		slog.Error("list sessions failed", "component", "sessions", "error", err)
		httputil.WriteError(w, "could not list sessions", http.StatusBadGateway)
		return
	}
	httputil.WriteJSON(w, sessions)
}
