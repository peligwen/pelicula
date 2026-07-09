package peligrosa

import (
	"context"
	"encoding/json"
	"log/slog"
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
	entries, err := store.All(r.Context())
	if err != nil {
		slog.Warn("operators: failed to load role entries", "component", "operators", "error", err)
		httputil.WriteError(w, "could not load operators", http.StatusInternalServerError)
		return
	}
	httputil.WriteJSON(w, entries)
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
		// Look up the pre-change entry so we know whether this is a
		// downgrade (and, since the roles table is the source of truth
		// for the live session's username, who to revoke).
		prev, hadPrev := lookupOperatorEntry(r.Context(), store, id)
		if err := store.Upsert(r.Context(), id, req.Username, req.Role); err != nil {
			httputil.WriteError(w, "could not update role", http.StatusInternalServerError)
			return
		}
		// Revoke live sessions on a downgrade only — a promotion should
		// not force the user to re-login (see MWD-1 remediation note).
		if hadPrev && roleRank(req.Role) < roleRank(prev.Role) {
			a.invalidateSessionsFor(r.Context(), prev.Username)
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
		// Look up the username before deleting the row — RolesStore.Delete
		// only takes the Jellyfin ID, but we need the username afterward
		// to revoke the removed operator's live sessions.
		prev, hadPrev := lookupOperatorEntry(r.Context(), store, id)
		if err := store.Delete(r.Context(), id); err != nil {
			httputil.WriteError(w, "could not remove role", http.StatusInternalServerError)
			return
		}
		if hadPrev {
			a.invalidateSessionsFor(r.Context(), prev.Username)
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// lookupOperatorEntry returns the current roles-table entry for jellyfinID,
// if any. RolesStore exposes no direct id→entry lookup (Lookup only returns
// the role), so this scans the full listing — fine at the tiny scale of a
// self-hosted operator list, and consistent with how HandleOperators already
// loads the whole table.
func lookupOperatorEntry(ctx context.Context, store *RolesStore, jellyfinID string) (RolesEntry, bool) {
	entries, err := store.All(ctx)
	if err != nil {
		return RolesEntry{}, false
	}
	for _, e := range entries {
		if e.JellyfinID == jellyfinID {
			return e, true
		}
	}
	return RolesEntry{}, false
}
