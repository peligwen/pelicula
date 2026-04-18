package main

import (
	"encoding/json"
	"net/http"
	"pelicula-api/httputil"
	"pelicula-api/internal/peligrosa"
	"strings"
)

// handleOperators handles GET /api/pelicula/operators — returns all role entries.
func handleOperators(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if authMiddleware == nil {
		httputil.WriteJSON(w, []peligrosa.RolesEntry{})
		return
	}
	store := authMiddleware.Roles()
	if store == nil {
		httputil.WriteJSON(w, []peligrosa.RolesEntry{})
		return
	}
	httputil.WriteJSON(w, store.All())
}

// handleOperatorsWithID handles POST /api/pelicula/operators/{id} (set role)
// and DELETE /api/pelicula/operators/{id} (remove entry).
func handleOperatorsWithID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/pelicula/operators/")
	if !validJellyfinID(id) {
		httputil.WriteError(w, "invalid user ID", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req struct {
			Role     peligrosa.UserRole `json:"role"`
			Username string             `json:"username"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.WriteError(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		// Validate role before touching the store.
		switch req.Role {
		case peligrosa.RoleViewer, peligrosa.RoleManager, peligrosa.RoleAdmin:
			// valid
		default:
			httputil.WriteError(w, "role must be viewer, manager, or admin", http.StatusBadRequest)
			return
		}
		if authMiddleware == nil {
			httputil.WriteError(w, "roles store unavailable", http.StatusInternalServerError)
			return
		}
		store := authMiddleware.Roles()
		if store == nil {
			httputil.WriteError(w, "roles store unavailable", http.StatusInternalServerError)
			return
		}
		if err := store.Upsert(id, req.Username, req.Role); err != nil {
			httputil.WriteError(w, "could not update role", http.StatusInternalServerError)
			return
		}
		httputil.WriteJSON(w, map[string]string{"status": "ok"})
	case http.MethodDelete:
		if authMiddleware == nil {
			httputil.WriteError(w, "roles store unavailable", http.StatusInternalServerError)
			return
		}
		store := authMiddleware.Roles()
		if store == nil {
			httputil.WriteError(w, "roles store unavailable", http.StatusInternalServerError)
			return
		}
		if err := store.Delete(id); err != nil {
			httputil.WriteError(w, "could not remove role", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
