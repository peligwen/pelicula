package peligrosa

import (
	"encoding/json"
	"net/http"
	"strings"

	"pelicula-api/httputil"
	jfapp "pelicula-api/internal/app/jellyfin"
)

// HandleOperators handles GET /api/pelicula/operators — returns all role entries.
func (a *Auth) HandleOperators(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a == nil {
		httputil.WriteJSON(w, []RolesEntry{})
		return
	}
	store := a.Roles()
	if store == nil {
		httputil.WriteJSON(w, []RolesEntry{})
		return
	}
	httputil.WriteJSON(w, store.All())
}

// HandleOperatorsWithID handles POST /api/pelicula/operators/{id} (set role)
// and DELETE /api/pelicula/operators/{id} (remove entry).
func (a *Auth) HandleOperatorsWithID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/pelicula/operators/")
	if !jfapp.ValidJellyfinID(id) {
		httputil.WriteError(w, "invalid user ID", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var req struct {
			Role     UserRole `json:"role"`
			Username string   `json:"username"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.WriteError(w, "invalid JSON", http.StatusBadRequest)
			return
		}
		// Validate role before touching the store.
		switch req.Role {
		case RoleViewer, RoleManager, RoleAdmin:
			// valid
		default:
			httputil.WriteError(w, "role must be viewer, manager, or admin", http.StatusBadRequest)
			return
		}
		if a == nil {
			httputil.WriteError(w, "roles store unavailable", http.StatusInternalServerError)
			return
		}
		store := a.Roles()
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
		if a == nil {
			httputil.WriteError(w, "roles store unavailable", http.StatusInternalServerError)
			return
		}
		store := a.Roles()
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
