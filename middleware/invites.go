// Peligrosa: trust boundary layer.
// Invite token lifecycle: creation, validation, redemption into Jellyfin user
// accounts. Public endpoints are invite-gated; admin endpoints are admin-only.
// See ../PELIGROSA.md.
package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// inviteStore is the package-level invite store, initialised in main.
var inviteStore *InviteStore

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

// InviteStore persists invites in SQLite.
// SQLite handles concurrency; no additional mutex is needed.
type InviteStore struct {
	db *sql.DB
}

// NewInviteStore creates an InviteStore backed by db.
func NewInviteStore(db *sql.DB) *InviteStore {
	return &InviteStore{db: db}
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

// scanInvite reads one row from the invites table plus its redemptions.
func (s *InviteStore) scanInvite(token string) (Invite, error) {
	var inv Invite
	var createdAt string
	var expiresAt sql.NullString
	var maxUses sql.NullInt64
	var revoked int

	err := s.db.QueryRow(
		`SELECT token, label, created_at, created_by, expires_at, max_uses, uses, revoked
		 FROM invites WHERE token = ?`, token,
	).Scan(&inv.Token, &inv.Label, &createdAt, &inv.CreatedBy,
		&expiresAt, &maxUses, &inv.Uses, &revoked)
	if err != nil {
		return Invite{}, err
	}
	inv.Revoked = revoked != 0
	if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
		inv.CreatedAt = t
	}
	if expiresAt.Valid {
		if t, err := time.Parse(time.RFC3339, expiresAt.String); err == nil {
			inv.ExpiresAt = &t
		}
	}
	if maxUses.Valid {
		n := int(maxUses.Int64)
		inv.MaxUses = &n
	}

	// Load redemptions.
	rows, err := s.db.Query(
		`SELECT username, jellyfin_id, redeemed_at FROM redemptions WHERE invite_token = ? ORDER BY redeemed_at`,
		token,
	)
	if err != nil {
		return inv, nil // non-fatal — invite data is valid
	}
	defer rows.Close()
	for rows.Next() {
		var r Redemption
		var redeemedAt string
		if err := rows.Scan(&r.Username, &r.JellyfinID, &redeemedAt); err != nil {
			continue
		}
		if t, err := time.Parse(time.RFC3339, redeemedAt); err == nil {
			r.RedeemedAt = t
		}
		inv.RedeemedBy = append(inv.RedeemedBy, r)
	}
	return inv, nil
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

	var expiresAtStr interface{}
	if expiresAt != nil {
		expiresAtStr = expiresAt.UTC().Format(time.RFC3339)
	}
	var maxUsesVal interface{}
	if maxUses != nil {
		maxUsesVal = *maxUses
	}

	_, err := s.db.Exec(
		`INSERT INTO invites (token, label, created_at, created_by, expires_at, max_uses, uses, revoked)
		 VALUES (?, ?, ?, ?, ?, ?, 0, 0)`,
		inv.Token, inv.Label, inv.CreatedAt.Format(time.RFC3339),
		inv.CreatedBy, expiresAtStr, maxUsesVal,
	)
	if err != nil {
		return Invite{}, err
	}
	return inv, nil
}

// InsertFull inserts an invite record from a backup export, preserving all
// fields including the token and timestamps. Silently succeeds if the token
// already exists (idempotent restore).
func (s *InviteStore) InsertFull(inv InviteExport) error {
	var expiresAtStr interface{}
	if inv.ExpiresAt != nil {
		expiresAtStr = inv.ExpiresAt.UTC().Format(time.RFC3339)
	}
	var maxUsesVal interface{}
	if inv.MaxUses != nil {
		maxUsesVal = *inv.MaxUses
	}
	revokedInt := 0
	if inv.Revoked {
		revokedInt = 1
	}
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO invites (token, label, created_at, created_by, expires_at, max_uses, uses, revoked)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		inv.Token, inv.Label, inv.CreatedAt.UTC().Format(time.RFC3339),
		inv.CreatedBy, expiresAtStr, maxUsesVal, inv.Uses, revokedInt,
	)
	return err
}

// ListInvites returns all invites with their derived states.
func (s *InviteStore) ListInvites() []InviteWithState {
	rows, err := s.db.Query(
		`SELECT token FROM invites ORDER BY created_at DESC`,
	)
	if err != nil {
		return []InviteWithState{}
	}

	// Collect tokens first, then close before making per-invite queries.
	// (SQLite MaxOpenConns=1: keeping rows open while issuing another query deadlocks.)
	var tokens []string
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err == nil {
			tokens = append(tokens, token)
		}
	}
	if err := rows.Err(); err != nil {
		slog.Warn("invites: ListInvites rows iteration error", "component", "invites", "error", err)
	}
	rows.Close() // must close before scanInvite queries

	result := make([]InviteWithState, 0, len(tokens))
	for _, token := range tokens {
		inv, err := s.scanInvite(token)
		if err != nil {
			continue
		}
		result = append(result, InviteWithState{Invite: inv, State: inv.state()})
	}
	return result
}

// CheckInvite looks up a token and returns its state without consuming it.
// Returns ("", found=false) if the token does not exist.
func (s *InviteStore) CheckInvite(token string) (string, bool) {
	inv, err := s.scanInvite(token)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		return "", false
	}
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
	// Phase 1: validate and atomically reserve a slot (BEGIN tx).
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	var inv Invite
	var createdAt string
	var expiresAt sql.NullString
	var maxUses sql.NullInt64
	var revoked int

	err = tx.QueryRow(
		`SELECT token, label, created_at, created_by, expires_at, max_uses, uses, revoked
		 FROM invites WHERE token = ?`, token,
	).Scan(&inv.Token, &inv.Label, &createdAt, &inv.CreatedBy,
		&expiresAt, &maxUses, &inv.Uses, &revoked)
	if err == sql.ErrNoRows {
		tx.Rollback() //nolint:errcheck
		return ErrInviteNotFound
	}
	if err != nil {
		tx.Rollback() //nolint:errcheck
		return err
	}
	inv.Revoked = revoked != 0
	if t, parseErr := time.Parse(time.RFC3339, createdAt); parseErr == nil {
		inv.CreatedAt = t
	}
	if expiresAt.Valid {
		if t, parseErr := time.Parse(time.RFC3339, expiresAt.String); parseErr == nil {
			inv.ExpiresAt = &t
		}
	}
	if maxUses.Valid {
		n := int(maxUses.Int64)
		inv.MaxUses = &n
	}

	if !inv.isActive() {
		tx.Rollback() //nolint:errcheck
		return ErrInviteNotActive
	}

	// Reserve the slot by pre-incrementing uses.
	if _, err := tx.Exec(`UPDATE invites SET uses = uses + 1 WHERE token = ?`, token); err != nil {
		tx.Rollback() //nolint:errcheck
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// Phase 2: create Jellyfin user (outside tx — can be slow).
	jellyfinID, err := CreateJellyfinUser(services, username, password)
	if err != nil {
		// Release the slot so the invite can be reused.
		if _, rollErr := s.db.Exec(`UPDATE invites SET uses = uses - 1 WHERE token = ?`, token); rollErr != nil {
			slog.Warn("failed to release invite slot after Jellyfin error",
				"component", "invites", "token", token[:8]+"…", "error", rollErr)
		}
		return err
	}

	// Phase 3: record the audit entry (BEGIN tx).
	tx2, err := s.db.Begin()
	if err != nil {
		slog.Warn("invite slot used but audit record may be lost — could not begin tx",
			"component", "invites", "username", username, "error", err)
		return nil
	}
	_, err = tx2.Exec(
		`INSERT INTO redemptions (invite_token, username, jellyfin_id, redeemed_at) VALUES (?, ?, ?, ?)`,
		token, username, jellyfinID, time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		tx2.Rollback() //nolint:errcheck
		slog.Warn("invite redeemed but audit record lost", "component", "invites",
			"username", username, "error", err)
		return nil
	}
	return tx2.Commit()
}

// Revoke marks an invite as revoked.
func (s *InviteStore) Revoke(token string) error {
	res, err := s.db.Exec(`UPDATE invites SET revoked = 1 WHERE token = ?`, token)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrInviteNotFound
	}
	return nil
}

// Delete hard-deletes an invite record.
func (s *InviteStore) Delete(token string) error {
	res, err := s.db.Exec(`DELETE FROM invites WHERE token = ?`, token)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrInviteNotFound
	}
	return nil
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
			MaxUses        *int   `json:"max_uses"`         // nil = unlimited
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
