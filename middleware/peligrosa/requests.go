// Peligrosa: trust boundary layer.
// Viewer-created media request queue. Viewers submit; admins approve or deny.
// The viewer/admin split is structural: Guard vs GuardAdmin in main.go.
// See ../../docs/PELIGROSA.md.
package peligrosa

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"pelicula-api/clients"
	"pelicula-api/httputil"
)

// RequestState represents the lifecycle state of a media request.
type RequestState string

const (
	RequestPending   RequestState = "pending"
	RequestApproved  RequestState = "approved"
	RequestDenied    RequestState = "denied"
	RequestGrabbed   RequestState = "grabbed"
	RequestAvailable RequestState = "available"
)

// RequestEvent records a single state transition for audit purposes.
type RequestEvent struct {
	At    time.Time    `json:"at"`
	State RequestState `json:"state"`
	Actor string       `json:"actor,omitempty"`
	Note  string       `json:"note,omitempty"`
}

// MediaRequest is a viewer's request for a movie or TV series.
type MediaRequest struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`    // "movie" | "series"
	TmdbID      int            `json:"tmdb_id"` // movies (primary); also present on many series
	TvdbID      int            `json:"tvdb_id"` // series (primary for Sonarr)
	Title       string         `json:"title"`
	Year        int            `json:"year"`
	Poster      string         `json:"poster,omitempty"`
	RequestedBy string         `json:"requested_by"`
	State       RequestState   `json:"state"`
	Reason      string         `json:"reason,omitempty"` // denial reason or status note
	ArrID       int            `json:"arr_id,omitempty"` // *arr internal id after approval
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	History     []RequestEvent `json:"history"`
}

// isTerminal returns true for states that are end-of-lifecycle.
func (r *MediaRequest) isTerminal() bool {
	return r.State == RequestDenied || r.State == RequestAvailable
}

// InviteExport captures the full state of an invite for backup/restore.
// Defined here so peligrosa's InsertFull method can use it, and export.go can
// reference it via peligrosa.InviteExport.
type InviteExport struct {
	Token     string     `json:"token"`
	Label     string     `json:"label,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	CreatedBy string     `json:"created_by"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	MaxUses   *int       `json:"max_uses,omitempty"`
	Uses      int        `json:"uses"`
	Revoked   bool       `json:"revoked"`
}

// RequestExport captures the full state of a media request for backup/restore.
type RequestExport struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	TmdbID      int            `json:"tmdb_id"`
	TvdbID      int            `json:"tvdb_id"`
	Title       string         `json:"title"`
	Year        int            `json:"year"`
	Poster      string         `json:"poster,omitempty"`
	RequestedBy string         `json:"requested_by"`
	State       RequestState   `json:"state"`
	Reason      string         `json:"reason,omitempty"`
	ArrID       int            `json:"arr_id,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	History     []RequestEvent `json:"history"`
}

// RequestStore persists media requests in SQLite.
// SQLite handles concurrency; no additional mutex is needed.
type RequestStore struct {
	db        *sql.DB
	fulfiller clients.Fulfiller
}

func NewRequestStore(db *sql.DB, fulfiller clients.Fulfiller) *RequestStore {
	return &RequestStore{db: db, fulfiller: fulfiller}
}

// loadHistory fetches the event history for a request from request_events.
func (s *RequestStore) loadHistory(id string) ([]RequestEvent, error) {
	rows, err := s.db.Query(
		`SELECT at, state, actor, note FROM request_events WHERE request_id = ? ORDER BY at`,
		id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []RequestEvent
	for rows.Next() {
		var ev RequestEvent
		var atStr string
		if err := rows.Scan(&atStr, &ev.State, &ev.Actor, &ev.Note); err != nil {
			continue
		}
		if t, parseErr := time.Parse(time.RFC3339Nano, atStr); parseErr == nil {
			ev.At = t
		} else if t, parseErr := time.Parse(time.RFC3339, atStr); parseErr == nil {
			ev.At = t
		}
		history = append(history, ev)
	}
	return history, nil
}

// scanRequest reads one row from requests and populates its History.
func (s *RequestStore) scanRequest(row *sql.Row) (*MediaRequest, error) {
	var req MediaRequest
	var createdAt, updatedAt string
	var poster, reason sql.NullString
	var arrID sql.NullInt64

	err := row.Scan(
		&req.ID, &req.Type, &req.TmdbID, &req.TvdbID,
		&req.Title, &req.Year, &poster,
		&req.RequestedBy, &req.State, &reason, &arrID,
		&createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	if poster.Valid {
		req.Poster = poster.String
	}
	if reason.Valid {
		req.Reason = reason.String
	}
	if arrID.Valid {
		req.ArrID = int(arrID.Int64)
	}
	if t, parseErr := time.Parse(time.RFC3339Nano, createdAt); parseErr == nil {
		req.CreatedAt = t
	} else if t, parseErr := time.Parse(time.RFC3339, createdAt); parseErr == nil {
		req.CreatedAt = t
	}
	if t, parseErr := time.Parse(time.RFC3339Nano, updatedAt); parseErr == nil {
		req.UpdatedAt = t
	} else if t, parseErr := time.Parse(time.RFC3339, updatedAt); parseErr == nil {
		req.UpdatedAt = t
	}

	history, _ := s.loadHistory(req.ID)
	if history == nil {
		history = []RequestEvent{}
	}
	req.History = history
	return &req, nil
}

func (s *RequestStore) All() []*MediaRequest {
	rows, err := s.db.Query(
		`SELECT id, type, tmdb_id, tvdb_id, title, year, poster,
		        requested_by, state, reason, arr_id, created_at, updated_at
		 FROM requests ORDER BY created_at`,
	)
	if err != nil {
		return []*MediaRequest{}
	}

	// Collect rows first, then close before making additional queries.
	// (SQLite MaxOpenConns=1: keeping rows open while issuing another query deadlocks.)
	var result []*MediaRequest
	var ids []string
	for rows.Next() {
		var req MediaRequest
		var createdAt, updatedAt string
		var poster, reason sql.NullString
		var arrID sql.NullInt64

		if err := rows.Scan(
			&req.ID, &req.Type, &req.TmdbID, &req.TvdbID,
			&req.Title, &req.Year, &poster,
			&req.RequestedBy, &req.State, &reason, &arrID,
			&createdAt, &updatedAt,
		); err != nil {
			continue
		}
		if poster.Valid {
			req.Poster = poster.String
		}
		if reason.Valid {
			req.Reason = reason.String
		}
		if arrID.Valid {
			req.ArrID = int(arrID.Int64)
		}
		if t, parseErr := time.Parse(time.RFC3339Nano, createdAt); parseErr == nil {
			req.CreatedAt = t
		} else if t, parseErr := time.Parse(time.RFC3339, createdAt); parseErr == nil {
			req.CreatedAt = t
		}
		if t, parseErr := time.Parse(time.RFC3339Nano, updatedAt); parseErr == nil {
			req.UpdatedAt = t
		} else if t, parseErr := time.Parse(time.RFC3339, updatedAt); parseErr == nil {
			req.UpdatedAt = t
		}
		result = append(result, &req)
		ids = append(ids, req.ID)
	}
	if err := rows.Err(); err != nil {
		slog.Warn("requests: all rows iteration error", "component", "requests", "error", err)
	}
	rows.Close() // must close before history query

	if len(result) == 0 {
		return []*MediaRequest{}
	}

	// Fetch all history events in one query and group by request_id in memory,
	// avoiding an N+1 pattern (one loadHistory call per request row).
	historyMap := make(map[string][]RequestEvent, len(ids))
	for _, id := range ids {
		historyMap[id] = []RequestEvent{} // ensure every request has a non-nil slice
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	query := "SELECT request_id, at, state, actor, note FROM request_events WHERE request_id IN (" +
		strings.Join(placeholders, ",") + ") ORDER BY at"
	evRows, err := s.db.Query(query, args...)
	if err == nil {
		defer evRows.Close()
		for evRows.Next() {
			var ev RequestEvent
			var requestID, atStr string
			if err := evRows.Scan(&requestID, &atStr, &ev.State, &ev.Actor, &ev.Note); err != nil {
				continue
			}
			if t, parseErr := time.Parse(time.RFC3339Nano, atStr); parseErr == nil {
				ev.At = t
			} else if t, parseErr := time.Parse(time.RFC3339, atStr); parseErr == nil {
				ev.At = t
			}
			historyMap[requestID] = append(historyMap[requestID], ev)
		}
	}

	for _, req := range result {
		req.History = historyMap[req.ID]
	}
	return result
}

// findActive returns the first non-terminal request matching type + tmdbID or tvdbID.
// Must be called without holding any lock.
func (s *RequestStore) findActive(reqType string, tmdbID, tvdbID int) *MediaRequest {
	all := s.All()
	for _, r := range all {
		if r.Type != reqType || r.isTerminal() {
			continue
		}
		if reqType == "movie" && tmdbID != 0 && r.TmdbID == tmdbID {
			return r
		}
		if reqType == "series" && tvdbID != 0 && r.TvdbID == tvdbID {
			return r
		}
	}
	return nil
}

func (s *RequestStore) get(id string) *MediaRequest {
	req, err := s.scanRequest(s.db.QueryRow(
		`SELECT id, type, tmdb_id, tvdb_id, title, year, poster,
		        requested_by, state, reason, arr_id, created_at, updated_at
		 FROM requests WHERE id = ?`, id,
	))
	if err != nil {
		return nil
	}
	return req
}

// insertEvent appends a state-transition event to request_events.
func (s *RequestStore) insertEvent(id string, ev RequestEvent) error {
	_, err := s.db.Exec(
		`INSERT INTO request_events (request_id, at, state, actor, note) VALUES (?, ?, ?, ?, ?)`,
		id, ev.At.UTC().Format(time.RFC3339Nano), string(ev.State), ev.Actor, ev.Note,
	)
	return err
}

// updateRequest updates mutable fields on an existing request row.
func (s *RequestStore) updateRequest(req *MediaRequest) error {
	_, err := s.db.Exec(
		`UPDATE requests SET state=?, reason=?, arr_id=?, updated_at=? WHERE id=?`,
		string(req.State), req.Reason, req.ArrID,
		req.UpdatedAt.UTC().Format(time.RFC3339Nano), req.ID,
	)
	return err
}

func generateRequestID() string {
	// Use a random token suffix; no dependency on main-package generateAPIKey.
	t, _ := generateToken()
	suffix := t
	if len(suffix) > 6 {
		suffix = suffix[:6]
	}
	return fmt.Sprintf("req_%d_%s", time.Now().UnixMilli(), suffix)
}

// InsertFull inserts a media request from a backup export, preserving all
// fields including the ID, timestamps, and event history. Silently succeeds
// if the ID already exists (idempotent restore).
func (s *RequestStore) InsertFull(req RequestExport) error {
	_, err := s.db.Exec(
		`INSERT OR IGNORE INTO requests (id, type, tmdb_id, tvdb_id, title, year, poster,
		                                 requested_by, state, reason, arr_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ID, req.Type, req.TmdbID, req.TvdbID, req.Title, req.Year, req.Poster,
		req.RequestedBy, string(req.State), req.Reason, req.ArrID,
		req.CreatedAt.UTC().Format(time.RFC3339Nano), req.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return err
	}
	// Insert history events, ignoring duplicates.
	for _, ev := range req.History {
		s.db.Exec( //nolint:errcheck — best-effort
			`INSERT OR IGNORE INTO request_events (request_id, at, state, actor, note) VALUES (?, ?, ?, ?, ?)`,
			req.ID, ev.At.UTC().Format(time.RFC3339Nano), string(ev.State), ev.Actor, ev.Note,
		)
	}
	return nil
}

// MarkAvailable transitions a request to "available" when its content has been imported.
// Matched by tmdbID (movies) or tvdbID (series). Non-fatal if no matching request exists.
// If notify is non-nil, it's invoked after the state transition to dispatch a notification.
func (s *RequestStore) MarkAvailable(reqType string, tmdbID, tvdbID int, title string, notify func(subject, body string)) {
	all := s.All()
	var matched *MediaRequest
	for _, req := range all {
		if req.isTerminal() || req.State == RequestAvailable {
			continue
		}
		if reqType == "movie" && tmdbID != 0 && req.TmdbID == tmdbID {
			matched = req
			break
		}
		if reqType == "series" && tvdbID != 0 && req.TvdbID == tvdbID {
			matched = req
			break
		}
	}
	if matched == nil {
		return
	}

	requester := matched.RequestedBy
	matched.State = RequestAvailable
	matched.UpdatedAt = time.Now().UTC()
	if err := s.updateRequest(matched); err != nil {
		slog.Error("failed to save request after availability update", "component", "requests", "error", err)
		return
	}
	ev := RequestEvent{
		At:    matched.UpdatedAt,
		State: RequestAvailable,
		Note:  "content imported",
	}
	if err := s.insertEvent(matched.ID, ev); err != nil {
		slog.Warn("failed to insert available event", "component", "requests", "error", err)
	}

	slog.Info("request marked available", "component", "requests", "id", matched.ID, "title", title)
	if notify != nil {
		notify(title+" is now available", fmt.Sprintf("Hey %s — %q has been imported and is ready to watch.", requester, title))
	}
}

// --- HTTP handlers ---

// HandleRequests dispatches GET (list) and POST (create) on /api/pelicula/requests.
func (p *Deps) HandleRequests(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		p.HandleRequestList(w, r)
	case http.MethodPost:
		p.HandleRequestCreate(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// HandleRequestList returns all requests. Admins see all; viewers see only their own.
func (p *Deps) HandleRequestList(w http.ResponseWriter, r *http.Request) {
	username, role, _ := p.Auth.SessionFor(r)

	all := p.Requests.All()
	var out []*MediaRequest
	for _, req := range all {
		if role.atLeast(RoleAdmin) || req.RequestedBy == username {
			out = append(out, req)
		}
	}
	if out == nil {
		out = []*MediaRequest{}
	}
	httputil.WriteJSON(w, out)
}

// HandleRequestCreate creates a new request from a viewer.
func (p *Deps) HandleRequestCreate(w http.ResponseWriter, r *http.Request) {
	username, _, ok := p.Auth.SessionFor(r)
	if !ok {
		httputil.WriteError(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var body struct {
		Type   string `json:"type"`
		TmdbID int    `json:"tmdb_id"`
		TvdbID int    `json:"tvdb_id"`
		Title  string `json:"title"`
		Year   int    `json:"year"`
		Poster string `json:"poster"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if body.Type != "movie" && body.Type != "series" {
		httputil.WriteError(w, "type must be 'movie' or 'series'", http.StatusBadRequest)
		return
	}
	if body.Title == "" {
		httputil.WriteError(w, "title is required", http.StatusBadRequest)
		return
	}
	if body.Type == "movie" && body.TmdbID == 0 {
		httputil.WriteError(w, "tmdb_id is required for movies", http.StatusBadRequest)
		return
	}
	if body.Type == "series" && body.TvdbID == 0 {
		httputil.WriteError(w, "tvdb_id is required for series", http.StatusBadRequest)
		return
	}

	// Deduplicate: return existing non-terminal request for the same content.
	existing := p.Requests.findActive(body.Type, body.TmdbID, body.TvdbID)
	if existing != nil {
		httputil.WriteJSON(w, existing)
		return
	}

	now := time.Now().UTC()
	req := &MediaRequest{
		ID:          generateRequestID(),
		Type:        body.Type,
		TmdbID:      body.TmdbID,
		TvdbID:      body.TvdbID,
		Title:       body.Title,
		Year:        body.Year,
		Poster:      body.Poster,
		RequestedBy: username,
		State:       RequestPending,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	_, err := p.Requests.db.Exec(
		`INSERT INTO requests (id, type, tmdb_id, tvdb_id, title, year, poster,
		                       requested_by, state, reason, arr_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', 0, ?, ?)`,
		req.ID, req.Type, req.TmdbID, req.TvdbID, req.Title, req.Year, req.Poster,
		req.RequestedBy, string(req.State),
		req.CreatedAt.Format(time.RFC3339Nano), req.UpdatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		slog.Error("failed to save request", "component", "requests", "error", err)
		httputil.WriteError(w, "failed to save request", http.StatusInternalServerError)
		return
	}

	ev := RequestEvent{At: now, State: RequestPending, Actor: username}
	if err := p.Requests.insertEvent(req.ID, ev); err != nil {
		slog.Warn("failed to insert initial request event", "component", "requests", "error", err)
	}
	req.History = []RequestEvent{ev}

	slog.Info("request created", "component", "requests", "id", req.ID, "title", req.Title, "user", username)
	w.WriteHeader(http.StatusCreated)
	httputil.WriteJSON(w, req)
}

// HandleRequestOp dispatches approve/deny/delete on /api/pelicula/requests/{id}[/action].
func (p *Deps) HandleRequestOp(w http.ResponseWriter, r *http.Request) {
	// Path: /api/pelicula/requests/{id} or /api/pelicula/requests/{id}/approve|deny
	path := strings.TrimPrefix(r.URL.Path, "/api/pelicula/requests/")
	path = strings.TrimSuffix(path, "/")

	parts := strings.SplitN(path, "/", 2)
	id := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	if id == "" {
		httputil.WriteError(w, "request id required", http.StatusBadRequest)
		return
	}

	switch action {
	case "approve":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		actorUsername, _, _ := p.Auth.SessionFor(r)
		p.Requests.handleRequestApprove(w, r, id, actorUsername, p.notify)
	case "deny":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p.HandleRequestDeny(w, r, id)
	case "":
		if r.Method != http.MethodDelete {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p.Requests.HandleRequestDelete(w, r, id)
	default:
		httputil.WriteError(w, "unknown action: "+action, http.StatusNotFound)
	}
}

func (rs *RequestStore) handleRequestApprove(w http.ResponseWriter, r *http.Request, id, actorUsername string, notify func(string, string)) {
	// Read profile/root from settings env vars (set at container start from .env)
	radarrProfileID := envIntOr("REQUESTS_RADARR_PROFILE_ID", 0)
	radarrRoot := os.Getenv("REQUESTS_RADARR_ROOT")
	sonarrProfileID := envIntOr("REQUESTS_SONARR_PROFILE_ID", 0)
	sonarrRoot := os.Getenv("REQUESTS_SONARR_ROOT")

	req := rs.get(id)
	if req == nil {
		httputil.WriteError(w, "request not found", http.StatusNotFound)
		return
	}
	if req.State != RequestPending {
		httputil.WriteError(w, fmt.Sprintf("request is %s, not pending", req.State), http.StatusConflict)
		return
	}

	// Snapshot what we need for the *arr call
	reqType := req.Type
	tmdbID := req.TmdbID
	tvdbID := req.TvdbID
	title := req.Title
	requester := req.RequestedBy

	// Add to *arr (outside lock — network call)
	var arrID int
	var addErr error
	switch reqType {
	case "movie":
		arrID, addErr = rs.fulfiller.AddMovie(tmdbID, radarrProfileID, radarrRoot)
	case "series":
		arrID, addErr = rs.fulfiller.AddSeries(tvdbID, sonarrProfileID, sonarrRoot)
	default:
		httputil.WriteError(w, "unknown request type", http.StatusInternalServerError)
		return
	}
	if addErr != nil {
		slog.Error("failed to add content to *arr", "component", "requests", "id", id, "error", addErr)
		httputil.WriteError(w, "failed to add to *arr: "+addErr.Error(), http.StatusBadGateway)
		return
	}

	// Re-fetch to ensure it still exists, then update state.
	req = rs.get(id)
	if req == nil {
		httputil.WriteError(w, "request not found", http.StatusNotFound)
		return
	}
	req.State = RequestGrabbed
	req.ArrID = arrID
	req.UpdatedAt = time.Now().UTC()
	if err := rs.updateRequest(req); err != nil {
		slog.Error("failed to save request after approve", "component", "requests", "error", err)
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}
	ev := RequestEvent{
		At:    req.UpdatedAt,
		State: RequestGrabbed,
		Actor: actorUsername,
		Note:  "approved and added to *arr",
	}
	if err := rs.insertEvent(req.ID, ev); err != nil {
		slog.Warn("failed to insert approve event", "component", "requests", "error", err)
	}
	req.History = append(req.History, ev)

	slog.Info("request approved", "component", "requests", "id", id, "title", title, "arr_id", arrID)
	if notify != nil {
		go notify("Request approved: "+title, fmt.Sprintf("%s requested %q — it's been added to the download queue.", requester, title))
	}

	httputil.WriteJSON(w, req)
}

// HandleRequestDeny denies a pending media request.
func (p *Deps) HandleRequestDeny(w http.ResponseWriter, r *http.Request, id string) {
	actorUsername, _, _ := p.Auth.SessionFor(r)

	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var body struct {
		Reason string `json:"reason"`
	}
	json.NewDecoder(r.Body).Decode(&body) // best-effort; reason is optional

	req := p.Requests.get(id)
	if req == nil {
		httputil.WriteError(w, "request not found", http.StatusNotFound)
		return
	}
	if req.isTerminal() {
		httputil.WriteError(w, fmt.Sprintf("request is already %s", req.State), http.StatusConflict)
		return
	}

	title := req.Title
	requester := req.RequestedBy

	req.State = RequestDenied
	req.Reason = body.Reason
	req.UpdatedAt = time.Now().UTC()
	if err := p.Requests.updateRequest(req); err != nil {
		slog.Error("failed to save request after deny", "component", "requests", "error", err)
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}
	ev := RequestEvent{
		At:    req.UpdatedAt,
		State: RequestDenied,
		Actor: actorUsername,
		Note:  body.Reason,
	}
	if err := p.Requests.insertEvent(req.ID, ev); err != nil {
		slog.Warn("failed to insert deny event", "component", "requests", "error", err)
	}
	req.History = append(req.History, ev)

	slog.Info("request denied", "component", "requests", "id", id, "title", title)
	if p.Notify != nil {
		msg := fmt.Sprintf("Your request for %q was not approved.", title)
		if body.Reason != "" {
			msg += " Reason: " + body.Reason
		}
		go p.Notify("Request denied: "+title, requester+" — "+msg)
	}

	httputil.WriteJSON(w, req)
}

// HandleRequestDelete hard-deletes a media request record.
func (rs *RequestStore) HandleRequestDelete(w http.ResponseWriter, r *http.Request, id string) {
	res, err := rs.db.Exec(`DELETE FROM requests WHERE id = ?`, id)
	if err != nil {
		slog.Error("failed to delete request", "component", "requests", "error", err)
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		httputil.WriteError(w, "request not found", http.StatusNotFound)
		return
	}
	httputil.WriteJSON(w, map[string]string{"status": "deleted"})
}

// envIntOr reads an env var as an integer, returning fallback on parse error or if unset.
func envIntOr(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return fallback
	}
	return n
}
