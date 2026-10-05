package api

import (
	"context"
	"net/http"
	"strings"

	"pelicula/internal/store"
)

type userRow struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	IsAdmin    bool       `json:"is_admin"`
	IsDisabled bool       `json:"is_disabled"`
	Role       store.Role `json:"role"`
	LastLogin  string     `json:"last_login"`
}

// jellyfinUsers lists Jellyfin's accounts using the cached admin token. On
// failure it writes the response and returns ok=false.
func (s *Server) jellyfinUsers(ctx context.Context, w http.ResponseWriter) (users []JellyfinUser, token string, ok bool) {
	if s.Jellyfin == nil || s.JFAdmin == nil {
		writeError(w, http.StatusServiceUnavailable, "Jellyfin is not configured")
		return nil, "", false
	}
	token, err := s.JFAdmin.Token(ctx)
	if err != nil {
		s.log().Warn("jellyfin admin token", "err", err)
		writeError(w, http.StatusBadGateway, "could not sign in to Jellyfin")
		return nil, "", false
	}
	users, err = s.Jellyfin.ListUsers(ctx, token)
	if err != nil {
		s.log().Warn("jellyfin list users", "err", err)
		writeError(w, http.StatusBadGateway, "Jellyfin is unreachable")
		return nil, "", false
	}
	return users, token, true
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, _, ok := s.jellyfinUsers(r.Context(), w)
	if !ok {
		return
	}
	roles, err := s.Store.ListRoles(r.Context())
	if err != nil {
		s.log().Error("list roles", "err", err)
		writeError(w, http.StatusInternalServerError, "could not read roles")
		return
	}
	rows := make([]userRow, 0, len(users))
	for _, u := range users {
		role, ok := roles[u.Name]
		if !ok {
			role = store.RoleViewer
			if u.IsAdmin {
				role = store.RoleAdmin
			}
		}
		rows = append(rows, userRow{
			ID: u.ID, Name: u.Name, IsAdmin: u.IsAdmin, IsDisabled: u.IsDisabled,
			Role: role, LastLogin: u.LastLogin,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": rows})
}

func (s *Server) handleSetUserRole(w http.ResponseWriter, r *http.Request) {
	sess := s.requireUser(w, r)
	if sess == nil {
		return
	}
	var body struct {
		Role string `json:"role"`
	}
	if !readJSON(w, r, &body, false) {
		return
	}
	role, valid := store.ParseRole(body.Role)
	if !valid {
		writeError(w, http.StatusBadRequest, "role must be viewer, manager or admin")
		return
	}
	name := r.PathValue("username")
	if strings.EqualFold(name, sess.Username) {
		writeError(w, http.StatusBadRequest, "you cannot change your own role")
		return
	}
	u, found, ok := s.findUser(r.Context(), w, name)
	if !ok {
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err := s.Store.SetRole(r.Context(), u.Name, role); err != nil {
		s.log().Error("set role", "user", u.Name, "err", err)
		writeError(w, http.StatusInternalServerError, "could not save role")
		return
	}
	noContent(w)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	sess := s.requireUser(w, r)
	if sess == nil {
		return
	}
	name := r.PathValue("username")
	if strings.EqualFold(name, sess.Username) {
		writeError(w, http.StatusBadRequest, "you cannot delete your own account")
		return
	}
	users, token, ok := s.jellyfinUsers(r.Context(), w)
	if !ok {
		return
	}
	u, found := matchUser(users, name)
	if !found {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err := s.Jellyfin.DeleteUser(r.Context(), token, u.ID); err != nil {
		s.log().Warn("jellyfin delete user", "user", u.Name, "err", err)
		writeError(w, http.StatusBadGateway, "Jellyfin could not delete the user")
		return
	}
	// Drops the role row and every live session for the account.
	if err := s.Store.DeleteRole(r.Context(), u.Name); err != nil {
		s.log().Error("delete role", "user", u.Name, "err", err)
		writeError(w, http.StatusInternalServerError, "user deleted in Jellyfin, but cleaning up its role failed")
		return
	}
	noContent(w)
}

// findUser looks name up in Jellyfin so roles are keyed by Jellyfin's own
// spelling of the username (Jellyfin treats names case-insensitively).
// ok=false means a response was already written.
func (s *Server) findUser(ctx context.Context, w http.ResponseWriter, name string) (u JellyfinUser, found, ok bool) {
	users, _, ok := s.jellyfinUsers(ctx, w)
	if !ok {
		return JellyfinUser{}, false, false
	}
	u, found = matchUser(users, name)
	return u, found, true
}

func matchUser(users []JellyfinUser, name string) (JellyfinUser, bool) {
	for _, u := range users {
		if strings.EqualFold(u.Name, name) {
			return u, true
		}
	}
	return JellyfinUser{}, false
}
