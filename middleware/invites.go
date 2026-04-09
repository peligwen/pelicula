// Peligrosa: trust boundary layer.
// Invite token lifecycle: creation, validation, redemption into Jellyfin user
// accounts. Public endpoints are invite-gated; admin endpoints are admin-only.
// See ../PELIGROSA.md.
package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// inviteStore is the package-level invite store, initialised in main.
var inviteStore *InviteStore

// Redemption records a single successful use of an invite.
type Redemption struct {
	Username    string    `json:"username"`
	JellyfinID  string    `json:"jellyfin_id"`
	RedeemedAt  time.Time `json:"redeemed_at"`
}

// Invite is a single invite record persisted to invites.json.
type Invite struct {
	Token      string      `json:"token"`
	Label      string      `json:"label,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
	CreatedBy  string      `json:"created_by"`
	ExpiresAt  *time.Time  `json:"expires_at,omitempty"`
	MaxUses    *int        `json:"max_uses,omitempty"` // nil = unlimited
	Uses       int         `json:"uses"`
	Revoked    bool        `json:"revoked"`
	RedeemedBy []Redemption `json:"redeemed_by,omitempty"`
}

// state returns the derived lifecycle state of the invite.
func (inv *Invite) state() string {
	if inv.Revoked {
		return "revoked"
	}
	if inv.ExpiresAt != nil && time.Now().After(*inv.ExpiresAt) {
		return "expired"
	}
	if inv.MaxUses != nil && inv.Uses >= *inv.MaxUses {
		return "exhausted"
	}
	return "active"
}

func (inv *Invite) isActive() bool { return inv.state() == "active" }

// InviteWithState is the wire type returned by GET /api/pelicula/invites.
type InviteWithState struct {
	Invite
	State string `json:"state"`
}

// InviteStore persists invites to a JSON file under a single mutex.
type InviteStore struct {
	path    string
	mu      sync.Mutex
	invites []Invite
}

func NewInviteStore(path string) *InviteStore {
	s := &InviteStore{path: path}
	if err := s.load(); err != nil {
		slog.Warn("could not load invites", "component", "invites", "path", path, "error", err)
	}
	return s
}

// load reads invites from disk. Caller must hold mu or be in init.
func (s *InviteStore) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // empty store is fine
		}
		return err
	}
	return json.Unmarshal(data, &s.invites)
}

// save writes the current invite slice to disk. Caller must hold mu.
func (s *InviteStore) save() error {
	data, err := json.MarshalIndent(s.invites, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0600)
}

// generateInviteToken returns a 32-byte URL-safe base64 token (43 chars, no padding).
func generateInviteToken() string {
	b := make([]byte, 32)
	rand.Read(b) //nolint:errcheck — always succeeds on Go 1.20+
	return base64.RawURLEncoding.EncodeToString(b)
}

// validInviteToken checks that a string looks like a raw base64url token (43 chars).
func validInviteToken(s string) bool {
	if len(s) != 43 {
		return false
	}
	for _, c := range s {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// validLabel checks that an invite label is safe for display (≤64 chars, no control chars).
func validLabel(s string) bool {
	if len(s) > 64 {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7F {
			return false
		}
	}
	return true
}

// CreateInvite adds a new invite and persists it.
func (s *InviteStore) CreateInvite(createdBy, label string, expiresAt *time.Time, maxUses *int) (Invite, error) {
	inv := Invite{
		Token:     generateInviteToken(),
		Label:     label,
		CreatedAt: time.Now().UTC(),
		CreatedBy: createdBy,
		ExpiresAt: expiresAt,
		MaxUses:   maxUses,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invites = append(s.invites, inv)
	if err := s.save(); err != nil {
		s.invites = s.invites[:len(s.invites)-1] // rollback
		return Invite{}, err
	}
	return inv, nil
}

// ListInvites returns all invites with their derived states.
func (s *InviteStore) ListInvites() []InviteWithState {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]InviteWithState, len(s.invites))
	for i, inv := range s.invites {
		result[i] = InviteWithState{Invite: inv, State: inv.state()}
	}
	return result
}

// CheckInvite looks up a token and returns its state without consuming it.
// Returns ("", found=false) if the token does not exist.
func (s *InviteStore) CheckInvite(token string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.invites {
		if s.invites[i].Token == token {
			return s.invites[i].state(), true
		}
	}
	return "", false
}

// ErrInviteNotFound is returned by Redeem/Revoke/Delete for unknown tokens.
var ErrInviteNotFound = errors.New("invite not found")

// ErrInviteNotActive is returned by Redeem when the invite is expired/exhausted/revoked.
var ErrInviteNotActive = errors.New("invite is not active")

// Redeem validates the token (under the mutex to prevent races), creates the
// Jellyfin account, then atomically increments uses and records the redemption.
// The Jellyfin creation happens before the mutex is held to avoid holding the
// lock during an HTTP call; a second validity check is done after re-acquiring.
func (s *InviteStore) Redeem(token, username, password string) error {
	// Phase 1: validate and reserve a slot by pre-incrementing uses (under lock).
	// This ensures a concurrent request sees the slot taken before we call Jellyfin,
	// giving a hard cap on the number of accounts created per invite.
	s.mu.Lock()
	idx := -1
	for i := range s.invites {
		if s.invites[i].Token == token {
			idx = i
			break
		}
	}
	if idx < 0 {
		s.mu.Unlock()
		return ErrInviteNotFound
	}
	if !s.invites[idx].isActive() {
		s.mu.Unlock()
		return ErrInviteNotActive
	}
	s.invites[idx].Uses++ // reserve the slot
	if err := s.save(); err != nil {
		s.invites[idx].Uses-- // undo reservation on save failure
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()

	// Phase 2: create Jellyfin user (outside mutex — can be slow).
	jellyfinID, err := CreateJellyfinUser(services, username, password)
	if err != nil {
		// Release the slot so the invite can be reused.
		s.mu.Lock()
		for i := range s.invites {
			if s.invites[i].Token == token {
				s.invites[i].Uses--
				s.save() //nolint:errcheck — best-effort rollback
				break
			}
		}
		s.mu.Unlock()
		return err
	}

	// Phase 3: append the audit record (under lock).
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.invites {
		if s.invites[i].Token == token {
			s.invites[i].RedeemedBy = append(s.invites[i].RedeemedBy, Redemption{
				Username:   username,
				JellyfinID: jellyfinID,
				RedeemedAt: time.Now().UTC(),
			})
			return s.save()
		}
	}
	// Invite was deleted after the slot was reserved — user account was created
	// but the audit record is lost. Log for admin visibility.
	slog.Warn("invite deleted after slot reserved; user account created but audit record lost",
		"component", "invites", "username", username)
	return nil
}

// Revoke marks an invite as revoked.
func (s *InviteStore) Revoke(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.invites {
		if s.invites[i].Token == token {
			s.invites[i].Revoked = true
			return s.save()
		}
	}
	return ErrInviteNotFound
}

// Delete hard-deletes an invite record.
func (s *InviteStore) Delete(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, inv := range s.invites {
		if inv.Token == token {
			s.invites = append(s.invites[:i], s.invites[i+1:]...)
			return s.save()
		}
	}
	return ErrInviteNotFound
}

// ── HTTP handlers ──────────────────────────────────────────────────────────────

// handleInvites serves GET /api/pelicula/invites (list) and
// POST /api/pelicula/invites (create). Both require admin.
func handleInvites(w http.ResponseWriter, r *http.Request) {
	// Block mutations in auth=off mode (same guard as handleUsers).
	if authMiddleware != nil && authMiddleware.IsOffMode() {
		writeError(w, "invite management requires PELICULA_AUTH to be enabled", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, inviteStore.ListInvites())

	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		var req struct {
			Label          string `json:"label"`
			ExpiresInHours *int   `json:"expires_in_hours"` // nil = never
			MaxUses        *int   `json:"max_uses"`          // nil = unlimited
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Label != "" && !validLabel(req.Label) {
			writeError(w, "label must be ≤64 chars with no control characters", http.StatusBadRequest)
			return
		}

		var expiresAt *time.Time
		if req.ExpiresInHours != nil && *req.ExpiresInHours > 0 {
			t := time.Now().UTC().Add(time.Duration(*req.ExpiresInHours) * time.Hour)
			expiresAt = &t
		}

		// Default: single-use if max_uses not specified
		maxUses := req.MaxUses
		if maxUses == nil {
			one := 1
			maxUses = &one
		}

		// Determine creator username from session (fallback: "admin").
		createdBy := "admin"
		if authMiddleware != nil {
			if sess, ok := authMiddleware.getSession(r); ok {
				createdBy = sess.username
			}
		}

		inv, err := inviteStore.CreateInvite(createdBy, req.Label, expiresAt, maxUses)
		if err != nil {
			slog.Error("create invite failed", "component", "invites", "error", err)
			writeError(w, "could not create invite", http.StatusInternalServerError)
			return
		}
		slog.Info("invite created", "component", "invites", "token", inv.Token[:8]+"…", "createdBy", createdBy)
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, inv)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleInviteOp dispatches requests to /api/pelicula/invites/{token}/...
// check and redeem are public; revoke and delete require admin.
func handleInviteOp(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/api/pelicula/invites/")
	parts := strings.SplitN(tail, "/", 2)
	token := parts[0]
	op := ""
	if len(parts) == 2 {
		op = parts[1]
	}

	if !validInviteToken(token) {
		writeError(w, "invalid invite token", http.StatusBadRequest)
		return
	}

	switch {
	case op == "check" && r.Method == http.MethodGet:
		// check and redeem are intentionally public — the token IS the credential.
		// Off-mode blocks admin endpoints only; viewers still need a way to register.
		handleInviteCheck(w, r, token)
	case op == "redeem" && r.Method == http.MethodPost:
		handleInviteRedeem(w, r, token)
	case op == "revoke" && r.Method == http.MethodPost:
		if !checkInviteAdmin(w, r) {
			return
		}
		handleInviteRevoke(w, r, token)
	case op == "" && r.Method == http.MethodDelete:
		if !checkInviteAdmin(w, r) {
			return
		}
		handleInviteDelete(w, r, token)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// checkInviteAdmin verifies admin auth for invite management operations.
// CSRF is enforced at the route level via requireLocalOriginSoft in main.go.
// Returns false and writes the error if the check fails.
func checkInviteAdmin(w http.ResponseWriter, r *http.Request) bool {
	if authMiddleware != nil && authMiddleware.IsOffMode() {
		writeError(w, "invite management requires PELICULA_AUTH to be enabled", http.StatusForbidden)
		return false
	}
	if authMiddleware == nil || authMiddleware.mode == "off" {
		return true
	}
	sess, ok := authMiddleware.getSession(r)
	if !ok {
		writeError(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if !sess.role.atLeast(RoleAdmin) {
		writeError(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func handleInviteCheck(w http.ResponseWriter, r *http.Request, token string) {
	state, found := inviteStore.CheckInvite(token)
	if !found {
		writeError(w, "invite not found", http.StatusNotFound)
		return
	}
	if state != "active" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		json.NewEncoder(w).Encode(map[string]string{"error": "invite " + state, "state": state})
		return
	}
	writeJSON(w, map[string]any{"valid": true})
}

func handleInviteRedeem(w http.ResponseWriter, r *http.Request, token string) {
	// Rate-limit by IP — reuse the auth limiter to prevent brute-force token abuse.
	ip := clientIP(r)
	if authMiddleware != nil && authMiddleware.isRateLimited(ip) {
		writeError(w, "too many requests — try again later", http.StatusTooManyRequests)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if !validUsername(req.Username) {
		if req.Username == "" {
			writeError(w, "username is required", http.StatusBadRequest)
		} else {
			writeError(w, "username is invalid (1–64 chars, no leading/trailing whitespace, no slashes)", http.StatusBadRequest)
		}
		return
	}
	if req.Password == "" {
		writeError(w, "password is required", http.StatusBadRequest)
		return
	}

	err := inviteStore.Redeem(token, req.Username, req.Password)
	if err != nil {
		if errors.Is(err, ErrInviteNotFound) {
			writeError(w, "invite not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, ErrInviteNotActive) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGone)
			json.NewEncoder(w).Encode(map[string]string{"error": "this invite is no longer active"})
			return
		}
		if errors.Is(err, ErrPasswordRequired) {
			writeError(w, "password is required", http.StatusBadRequest)
			return
		}
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
		slog.Error("invite redemption failed", "component", "invites", "username", req.Username, "error", err)
		writeError(w, "could not create account", http.StatusBadGateway)
		return
	}

	slog.Info("invite redeemed", "component", "invites", "username", req.Username)
	writeJSON(w, map[string]string{"status": "ok"})
}

func handleInviteRevoke(w http.ResponseWriter, r *http.Request, token string) {
	if err := inviteStore.Revoke(token); err != nil {
		if errors.Is(err, ErrInviteNotFound) {
			writeError(w, "invite not found", http.StatusNotFound)
			return
		}
		slog.Error("revoke invite failed", "component", "invites", "error", err)
		writeError(w, "could not revoke invite", http.StatusInternalServerError)
		return
	}
	slog.Info("invite revoked", "component", "invites", "token", token[:8]+"…")
	w.WriteHeader(http.StatusNoContent)
}

func handleInviteDelete(w http.ResponseWriter, r *http.Request, token string) {
	if err := inviteStore.Delete(token); err != nil {
		if errors.Is(err, ErrInviteNotFound) {
			writeError(w, "invite not found", http.StatusNotFound)
			return
		}
		slog.Error("delete invite failed", "component", "invites", "error", err)
		writeError(w, "could not delete invite", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
