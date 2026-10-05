package auth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"time"

	"pelicula/internal/store"
)

const (
	defaultInviteHours = 72
	maxInviteHours     = 720
	minPasswordLen     = 8
	maxCodeLen         = 64
)

var usernameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{3,32}$`)

type inviteJSON struct {
	Code      string     `json:"code"`
	Role      store.Role `json:"role"`
	CreatedBy string     `json:"created_by"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	UsedBy    string     `json:"used_by"`
	UsedAt    *time.Time `json:"used_at"`
}

func (a *Auth) handleInviteList(w http.ResponseWriter, r *http.Request) {
	invs, err := a.store.ListInvites(r.Context())
	if err != nil {
		a.log.Error("auth: list invites", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]inviteJSON, 0, len(invs))
	for _, i := range invs {
		out = append(out, inviteJSON{i.Code, i.Role, i.CreatedBy, i.CreatedAt, i.ExpiresAt, i.UsedBy, i.UsedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": out})
}

// handleInviteCreate accepts {role, expires_hours}; both are optional. A role
// defaults to viewer, hours of 0 default to 72 and anything above 720 is
// capped at 720.
func (a *Auth) handleInviteCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role         string `json:"role"`
		ExpiresHours int    `json:"expires_hours"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	role := store.RoleViewer
	if req.Role != "" {
		var ok bool
		if role, ok = store.ParseRole(req.Role); !ok {
			writeError(w, http.StatusBadRequest, "role must be viewer, manager or admin")
			return
		}
	}
	hours := req.ExpiresHours
	switch {
	case hours < 0:
		writeError(w, http.StatusBadRequest, "expires_hours must not be negative")
		return
	case hours == 0:
		hours = defaultInviteHours
	case hours > maxInviteHours:
		hours = maxInviteHours
	}
	code, err := newInviteCode()
	if err != nil {
		a.log.Error("auth: invite code", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	now := time.Now()
	inv := store.Invite{
		Code:      code,
		Role:      role,
		CreatedBy: SessionFrom(r.Context()).Username,
		CreatedAt: now,
		ExpiresAt: now.Add(time.Duration(hours) * time.Hour),
	}
	if err := a.store.CreateInvite(r.Context(), inv); err != nil {
		a.log.Error("auth: create invite", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.log.Info("auth: invite created", "by", inv.CreatedBy, "role", role, "hours", hours)
	writeJSON(w, http.StatusCreated, map[string]any{
		"code":       code,
		"role":       role,
		"expires_at": inv.ExpiresAt,
		"path":       "/register?code=" + code,
	})
}

func (a *Auth) handleInviteDelete(w http.ResponseWriter, r *http.Request) {
	err := a.store.DeleteInvite(r.Context(), r.PathValue("code"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "invite not found")
	case err != nil:
		a.log.Error("auth: delete invite", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// liveInvite returns the invite for code when it exists, is unused and has not
// expired; otherwise nil.
func (a *Auth) liveInvite(r *http.Request, code string) (*store.Invite, error) {
	if code == "" || len(code) > maxCodeLen {
		return nil, nil
	}
	inv, err := a.store.GetInvite(r.Context(), code)
	if err != nil || inv == nil {
		return nil, err
	}
	if inv.Used() || inv.Expired(time.Now()) {
		return nil, nil
	}
	return inv, nil
}

func (a *Auth) handleRegisterCheck(w http.ResponseWriter, r *http.Request) {
	inv, err := a.liveInvite(r, r.PathValue("code"))
	if err != nil {
		a.log.Error("auth: invite lookup", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	resp := map[string]any{"valid": inv != nil, "role": ""}
	if inv != nil {
		resp["role"] = inv.Role
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *Auth) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code     string `json:"code"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &req) {
		return
	}

	a.regMu.Lock()
	defer a.regMu.Unlock()

	inv, err := a.liveInvite(r, req.Code)
	if err != nil {
		a.log.Error("auth: invite lookup", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if inv == nil {
		writeError(w, http.StatusGone, "invite is invalid or has expired")
		return
	}
	if !usernameRE.MatchString(req.Username) {
		writeError(w, http.StatusBadRequest, "username must be 3-32 characters: letters, digits, '.', '_' or '-'")
		return
	}
	if len(req.Password) < minPasswordLen {
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	ctx := r.Context()
	adminToken, err := a.admin.Token(ctx)
	if err != nil {
		a.log.Error("auth: jellyfin admin token", "error", err)
		writeError(w, http.StatusServiceUnavailable, "account service unavailable")
		return
	}
	if _, err := a.users.CreateUser(ctx, adminToken, req.Username, req.Password); err != nil {
		if isConflict(err) {
			writeError(w, http.StatusConflict, "that username is already taken")
			return
		}
		a.log.Error("auth: jellyfin create user", "username", req.Username, "error", err)
		writeError(w, http.StatusServiceUnavailable, "account service unavailable")
		return
	}
	if err := a.store.UseInvite(ctx, inv.Code, req.Username); err != nil {
		// Cannot happen while regMu is held, short of the invite being deleted
		// between the check above and here.
		a.log.Error("auth: use invite after creating user", "username", req.Username, "error", err)
		writeError(w, http.StatusGone, "invite is invalid or has expired")
		return
	}
	if err := a.store.SetRole(ctx, req.Username, inv.Role); err != nil {
		a.log.Error("auth: set role", "username", req.Username, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.log.Info("auth: registered", "username", req.Username, "role", inv.Role)

	// Log the new user in. The account exists at this point, so a failure here
	// still answers 201; the client lands on the login screen.
	res, err := a.jf.AuthenticateByName(ctx, req.Username, req.Password)
	if err != nil {
		a.log.Warn("auth: login after register failed", "username", req.Username, "error", err)
		writeJSON(w, http.StatusCreated, userRole{req.Username, inv.Role})
		return
	}
	sess, err := a.startSession(w, r, res, req.Username)
	if err != nil {
		a.log.Error("auth: start session after register", "username", req.Username, "error", err)
		writeJSON(w, http.StatusCreated, userRole{req.Username, inv.Role})
		return
	}
	writeJSON(w, http.StatusCreated, userRole{sess.Username, sess.Role})
}

// newInviteCode returns 16 random bytes, base64url without padding.
func newInviteCode() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
