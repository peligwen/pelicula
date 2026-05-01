// Peligrosa: trust boundary layer.
// Invite token lifecycle: creation, validation, redemption into Jellyfin user
// accounts. Public endpoints are invite-gated; admin endpoints are admin-only.
// See ../../docs/PELIGROSA.md.
package peligrosa

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"pelicula-api/clients"
	"pelicula-api/httputil"
	repoinvites "pelicula-api/internal/repo/invites"
	"strings"
	"time"
)

// Redemption records a single successful use of an invite.
type Redemption struct {
	Username   string    `json:"username"`
	JellyfinID string    `json:"jellyfin_id"`
	RedeemedAt time.Time `json:"redeemed_at"`
}

// Invite is a single invite record.
type Invite struct {
	Token      string       `json:"token"`
	Label      string       `json:"label,omitempty"`
	CreatedAt  time.Time    `json:"created_at"`
	CreatedBy  string       `json:"created_by"`
	ExpiresAt  *time.Time   `json:"expires_at,omitempty"`
	MaxUses    *int         `json:"max_uses,omitempty"` // nil = unlimited
	Uses       int          `json:"uses"`
	Revoked    bool         `json:"revoked"`
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

// InviteStore manages invite lifecycle, delegating all SQL to the repo layer.
// SQLite handles concurrency; no additional mutex is needed.
type InviteStore struct {
	repo     *repoinvites.Store
	jellyfin clients.JellyfinClient
}

// NewInviteStore creates an InviteStore backed by db.
// jc may be nil when the store is used in contexts that never call Redeem
// (e.g. export/import tests).
func NewInviteStore(db *sql.DB, jc clients.JellyfinClient) *InviteStore {
	return &InviteStore{repo: repoinvites.New(db), jellyfin: jc}
}

// db returns the underlying *sql.DB for in-package test use (e.g. seeding rows).
func (s *InviteStore) db() *sql.DB {
	return s.repo.DB()
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

// repoInviteToInvite converts a repo InviteWithRedemptions to the peligrosa Invite type.
func repoInviteToInvite(r repoinvites.InviteWithRedemptions) Invite {
	inv := Invite{
		Token:     r.Token,
		Label:     r.Label,
		CreatedAt: r.CreatedAt,
		CreatedBy: r.CreatedBy,
		ExpiresAt: r.ExpiresAt,
		MaxUses:   r.MaxUses,
		Uses:      r.Uses,
		Revoked:   r.Revoked,
	}
	for _, red := range r.RedeemedBy {
		inv.RedeemedBy = append(inv.RedeemedBy, Redemption{
			Username:   red.Username,
			JellyfinID: red.JellyfinID,
			RedeemedAt: red.RedeemedAt,
		})
	}
	return inv
}

// repoInviteRowToInvite converts a bare repo Invite (no redemptions) to a peligrosa Invite.
func repoInviteRowToInvite(r repoinvites.Invite) Invite {
	return Invite{
		Token:     r.Token,
		Label:     r.Label,
		CreatedAt: r.CreatedAt,
		CreatedBy: r.CreatedBy,
		ExpiresAt: r.ExpiresAt,
		MaxUses:   r.MaxUses,
		Uses:      r.Uses,
		Revoked:   r.Revoked,
	}
}

// CreateInvite adds a new invite and persists it.
func (s *InviteStore) CreateInvite(createdBy, label string, expiresAt *time.Time, maxUses *int) (Invite, error) {
	inv := repoinvites.Invite{
		Token:     generateInviteToken(),
		Label:     label,
		CreatedAt: time.Now().UTC(),
		CreatedBy: createdBy,
		ExpiresAt: expiresAt,
		MaxUses:   maxUses,
		Uses:      0,
		Revoked:   false,
	}
	if err := s.repo.Create(context.Background(), inv); err != nil {
		return Invite{}, err
	}
	return repoInviteRowToInvite(inv), nil
}

// InsertFull inserts an invite record from a backup export, preserving all
// fields including the token and timestamps. Silently succeeds if the token
// already exists (idempotent restore).
func (s *InviteStore) InsertFull(inv InviteExport) error {
	return s.repo.InsertFull(context.Background(), repoinvites.Invite{
		Token:     inv.Token,
		Label:     inv.Label,
		CreatedAt: inv.CreatedAt,
		CreatedBy: inv.CreatedBy,
		ExpiresAt: inv.ExpiresAt,
		MaxUses:   inv.MaxUses,
		Uses:      inv.Uses,
		Revoked:   inv.Revoked,
	})
}

// ListInvites returns all invites with their derived states.
func (s *InviteStore) ListInvites() []InviteWithState {
	ctx := context.Background()
	tokens, err := s.repo.ListTokens(ctx)
	if err != nil {
		slog.Warn("invites: ListInvites failed to load tokens", "component", "invites", "error", err)
		return []InviteWithState{}
	}

	result := make([]InviteWithState, 0, len(tokens))
	for _, token := range tokens {
		r, err := s.repo.Get(ctx, token)
		if err != nil {
			continue
		}
		inv := repoInviteToInvite(r)
		result = append(result, InviteWithState{Invite: inv, State: inv.state()})
	}
	return result
}

// CheckInvite looks up a token and returns its state without consuming it.
// Returns ("", found=false) if the token does not exist.
func (s *InviteStore) CheckInvite(token string) (string, bool) {
	r, err := s.repo.Get(context.Background(), token)
	if errors.Is(err, repoinvites.ErrNotFound) {
		return "", false
	}
	if err != nil {
		return "", false
	}
	inv := repoInviteToInvite(r)
	return inv.state(), true
}

// ErrInviteNotFound is returned by Redeem/Revoke/Delete for unknown tokens.
var ErrInviteNotFound = errors.New("invite not found")

// ErrInviteNotActive is returned by Redeem when the invite is expired/exhausted/revoked.
var ErrInviteNotActive = errors.New("invite is not active")

// Redeem validates the token, creates the Jellyfin account, then records the
// redemption. A slot is reserved under a transaction before the Jellyfin call to
// prevent concurrent over-use; the slot is released (decremented) on failure.
func (s *InviteStore) Redeem(token, username, password string) error {
	ctx := context.Background()

	// Phase 1: validate and atomically reserve a slot (BEGIN tx).
	repoInv, tx, err := s.repo.ReserveSlot(ctx, token)
	if errors.Is(err, repoinvites.ErrNotFound) {
		return ErrInviteNotFound
	}
	if err != nil {
		return err
	}

	inv := repoInviteRowToInvite(repoInv)
	if !inv.isActive() {
		tx.Rollback() //nolint:errcheck
		return ErrInviteNotActive
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	// Phase 2: create Jellyfin user (outside tx — can be slow).
	jellyfinID, err := s.jellyfin.CreateUser(username, password)
	if err != nil {
		// Release the slot so the invite can be reused.
		if rollErr := s.repo.ReleaseSlot(ctx, token); rollErr != nil {
			slog.Warn("failed to release invite slot after Jellyfin error",
				"component", "invites", "username", username, "error", rollErr)
		}
		return err
	}

	// Phase 3: record the audit entry.
	if err := s.repo.InsertRedemption(ctx, token, username, jellyfinID, time.Now().UTC()); err != nil {
		slog.Error("invite redeemed but audit record lost", "component", "invites",
			"username", username, "error", err)
		return fmt.Errorf("audit record failed: %w", err)
	}
	return nil
}

// Revoke marks an invite as revoked.
func (s *InviteStore) Revoke(token string) error {
	err := s.repo.Revoke(context.Background(), token)
	if errors.Is(err, repoinvites.ErrNotFound) {
		return ErrInviteNotFound
	}
	return err
}

// Delete hard-deletes an invite record.
func (s *InviteStore) Delete(token string) error {
	err := s.repo.Delete(context.Background(), token)
	if errors.Is(err, repoinvites.ErrNotFound) {
		return ErrInviteNotFound
	}
	return err
}

// ── HTTP handlers ──────────────────────────────────────────────────────────────

// HandleInvites serves GET /api/pelicula/invites (list) and
// POST /api/pelicula/invites (create). Both require admin.
func (p *Deps) HandleInvites(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		httputil.WriteJSON(w, p.Invites.ListInvites())

	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		var req struct {
			Label          string `json:"label"`
			ExpiresInHours *int   `json:"expires_in_hours"` // nil = never
			MaxUses        *int   `json:"max_uses"`         // nil = unlimited
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.WriteError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Label != "" && !validLabel(req.Label) {
			httputil.WriteError(w, "label must be ≤64 chars with no control characters", http.StatusBadRequest)
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
		if p.Auth != nil {
			if username, _, ok := p.Auth.SessionFor(r); ok && username != "" {
				createdBy = username
			}
		}

		inv, err := p.Invites.CreateInvite(createdBy, req.Label, expiresAt, maxUses)
		if err != nil {
			slog.Error("create invite failed", "component", "invites", "error", err)
			httputil.WriteError(w, "could not create invite", http.StatusInternalServerError)
			return
		}
		if inv.Label != "" {
			slog.Info("invite created", "component", "invites", "label", inv.Label, "createdBy", createdBy)
		} else {
			slog.Info("invite created", "component", "invites", "createdAt", inv.CreatedAt.Unix(), "createdBy", createdBy)
		}
		w.WriteHeader(http.StatusCreated)
		httputil.WriteJSON(w, inv)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleInviteOp dispatches requests to /api/pelicula/invites/{token}/...
// check and redeem are public; revoke and delete require admin.
func (p *Deps) HandleInviteOp(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/api/pelicula/invites/")
	parts := strings.SplitN(tail, "/", 2)
	token := parts[0]
	op := ""
	if len(parts) == 2 {
		op = parts[1]
	}

	if !validInviteToken(token) {
		httputil.WriteError(w, "invalid invite token", http.StatusBadRequest)
		return
	}

	switch {
	case op == "check" && r.Method == http.MethodGet:
		// check and redeem are intentionally public — the token IS the credential.
		p.HandleInviteCheck(w, r, token)
	case op == "redeem" && r.Method == http.MethodPost:
		p.HandleInviteRedeem(w, r, token)
	case op == "revoke" && r.Method == http.MethodPost:
		if !p.checkInviteAdmin(w, r) {
			return
		}
		p.Invites.HandleInviteRevoke(w, r, token)
	case op == "" && r.Method == http.MethodDelete:
		if !p.checkInviteAdmin(w, r) {
			return
		}
		p.Invites.HandleInviteDelete(w, r, token)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// checkInviteAdmin verifies admin auth for invite management operations.
// CSRF is enforced at the route level via httputil.RequireLocalOriginSoft in main.go.
// Returns false and writes the error if the check fails.
func (p *Deps) checkInviteAdmin(w http.ResponseWriter, r *http.Request) bool {
	if p.Auth == nil {
		httputil.WriteError(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	_, role, ok := p.Auth.SessionFor(r)
	if !ok {
		httputil.WriteError(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if !role.atLeast(RoleAdmin) {
		httputil.WriteError(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// HandleInviteCheck checks the state of an invite token without consuming it.
// It is rate-limited per IP: a token probe is brute-force evidence when the
// token is not found, so recordFailure is called on the not-found path only.
// A token that exists but is revoked/expired is a legitimate single lookup —
// it does NOT count as a failure.
func (p *Deps) HandleInviteCheck(w http.ResponseWriter, r *http.Request, token string) {
	ip := httputil.ClientIP(r)
	if p.Auth != nil && p.Auth.isRateLimited(r.Context(), ip) {
		httputil.WriteError(w, "too many requests — try again later", http.StatusTooManyRequests)
		return
	}

	state, found := p.Invites.CheckInvite(token)
	if !found {
		if p.Auth != nil {
			p.Auth.recordFailure(r.Context(), ip)
		}
		httputil.WriteError(w, "invite not found", http.StatusNotFound)
		return
	}
	if state != "active" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		json.NewEncoder(w).Encode(map[string]string{"error": "invite " + state, "state": state})
		return
	}
	httputil.WriteJSON(w, map[string]any{"valid": true})
}

// HandleInviteRedeem redeems an invite token to create a new Jellyfin account.
func (p *Deps) HandleInviteRedeem(w http.ResponseWriter, r *http.Request, token string) {
	// Rate-limit by IP — reuse the auth limiter to prevent brute-force token abuse.
	ip := httputil.ClientIP(r)
	if p.Auth != nil && p.Auth.isRateLimited(r.Context(), ip) {
		httputil.WriteError(w, "too many requests — try again later", http.StatusTooManyRequests)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if !clients.IsValidUsername(req.Username) {
		if req.Username == "" {
			httputil.WriteError(w, "username is required", http.StatusBadRequest)
		} else {
			httputil.WriteError(w, "username is invalid (1–64 chars, no leading/trailing whitespace, no slashes)", http.StatusBadRequest)
		}
		return
	}
	if req.Password == "" {
		httputil.WriteError(w, "password is required", http.StatusBadRequest)
		return
	}

	err := p.Invites.Redeem(token, req.Username, req.Password)
	if err != nil {
		if errors.Is(err, ErrInviteNotFound) {
			// A probe against a non-existent token is brute-force evidence.
			if p.Auth != nil {
				p.Auth.recordFailure(r.Context(), ip)
			}
			httputil.WriteError(w, "invite not found", http.StatusNotFound)
			return
		}
		if errors.Is(err, ErrInviteNotActive) {
			// Redeeming a revoked/exhausted token is also brute-force-revealing.
			if p.Auth != nil {
				p.Auth.recordFailure(r.Context(), ip)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGone)
			json.NewEncoder(w).Encode(map[string]string{"error": "this invite is no longer active"})
			return
		}
		if errors.Is(err, clients.ErrPasswordRequired) {
			httputil.WriteError(w, "password is required", http.StatusBadRequest)
			return
		}
		// Detect username-already-taken (Jellyfin returns 400)
		var jErr *clients.JellyfinHTTPError
		if errors.As(err, &jErr) && jErr.StatusCode == http.StatusBadRequest {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "that username is already taken",
				"code":  "username_taken",
			})
			return
		}
		// Infrastructure / Jellyfin error — do not penalise the limiter.
		// A flapping backend must not lock out legitimate viewers.
		slog.Error("invite redemption failed", "component", "invites", "username", req.Username, "error", err)
		httputil.WriteError(w, "could not create account", http.StatusBadGateway)
		return
	}

	slog.Info("invite redeemed", "component", "invites", "username", req.Username)
	httputil.WriteJSON(w, map[string]string{"status": "ok"})
}

// HandleInviteRevoke marks an invite as revoked.
func (s *InviteStore) HandleInviteRevoke(w http.ResponseWriter, r *http.Request, token string) {
	if err := s.Revoke(token); err != nil {
		if errors.Is(err, ErrInviteNotFound) {
			httputil.WriteError(w, "invite not found", http.StatusNotFound)
			return
		}
		slog.Error("revoke invite failed", "component", "invites", "error", err)
		httputil.WriteError(w, "could not revoke invite", http.StatusInternalServerError)
		return
	}
	slog.Info("invite revoked", "component", "invites")
	w.WriteHeader(http.StatusNoContent)
}

// HandleInviteDelete hard-deletes an invite record.
func (s *InviteStore) HandleInviteDelete(w http.ResponseWriter, r *http.Request, token string) {
	if err := s.Delete(token); err != nil {
		if errors.Is(err, ErrInviteNotFound) {
			httputil.WriteError(w, "invite not found", http.StatusNotFound)
			return
		}
		slog.Error("delete invite failed", "component", "invites", "error", err)
		httputil.WriteError(w, "could not delete invite", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
