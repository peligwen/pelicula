// Package requests provides a typed data-access store for the requests and
// request_events tables. Business logic (fulfillment, HTTP handlers) lives in
// internal/peligrosa; this layer owns all SQL.
package requests

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"pelicula-api/internal/repo/dbutil"
)

// State represents the lifecycle state of a media request.
type State string

const (
	StatePending   State = "pending"
	StateApproved  State = "approved"
	StateDenied    State = "denied"
	StateGrabbed   State = "grabbed"
	StateAvailable State = "available"
)

// Event records a single state transition for audit purposes.
type Event struct {
	RequestID string
	At        time.Time
	State     State
	Actor     string
	Note      string
}

// Request holds the DB columns for a single row in the requests table plus its
// associated event history. It is distinct from peligrosa.MediaRequest (which
// adds HTTP JSON tags and business methods) to avoid an import cycle.
type Request struct {
	ID          string
	Type        string // "movie" | "series"
	TmdbID      int
	TvdbID      int
	Title       string
	Year        int
	Poster      string
	RequestedBy string
	State       State
	Reason      string
	ArrID       int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	History     []Event
	// Seasons is the season-level scope for a series request; nil means
	// unspecified/all (also the historical behavior before season support
	// existed). See seasonsToText/seasonsFromText for the DB encoding.
	Seasons []int
}

// seasonsToText serializes a season-number slice to the requests.seasons
// column's text encoding: nil (unspecified/all) becomes "" — never the
// literal "null" or "[]" — and a non-nil slice (including an explicit empty
// one, though "all seasons" should be represented as nil, not []) becomes a
// JSON int array, e.g. "[1,2]".
func seasonsToText(seasons []int) string {
	if seasons == nil {
		return ""
	}
	b, err := json.Marshal(seasons)
	if err != nil {
		return ""
	}
	return string(b)
}

// seasonsFromText is seasonsToText's inverse: "" (including every row
// written before migrate4 added the column) decodes to nil; anything else is
// JSON-decoded, falling back to nil on malformed data (defensive — the
// column is only ever written by seasonsToText).
func seasonsFromText(s string) []int {
	if s == "" {
		return nil
	}
	var out []int
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// ErrNotFound is returned when a request ID does not exist in the table.
var ErrNotFound = errors.New("request not found")

// Store wraps a *sql.DB and provides named methods for requests/request_events
// table access. SQLite handles concurrency; no additional mutex is needed.
type Store struct {
	db *sql.DB
}

// New returns a Store backed by db.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// DB returns the underlying *sql.DB. Callers that need direct DB access for
// test setup (e.g. seeding rows) may use this.
func (s *Store) DB() *sql.DB {
	return s.db
}

// All returns all requests ordered by created_at with their full event history.
// History is fetched in a single bulk query — not per-request — to avoid N+1
// (SQLite MaxOpenConns=1: keeping a cursor open while issuing another query
// would deadlock).
func (s *Store) All(ctx context.Context) ([]*Request, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, tmdb_id, tvdb_id, title, year, poster,
		        requested_by, state, reason, arr_id, created_at, updated_at, seasons
		 FROM requests ORDER BY created_at`,
	)
	if err != nil {
		return []*Request{}, err
	}

	// Collect all rows first, then close before issuing the history query.
	// (SQLite MaxOpenConns=1: holding a cursor open blocks subsequent queries.)
	var result []*Request
	var ids []string
	for rows.Next() {
		req, err := scanRequestRow(rows)
		if err != nil {
			continue
		}
		result = append(result, req)
		ids = append(ids, req.ID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, err
	}
	rows.Close() // must close before history query

	if len(result) == 0 {
		return []*Request{}, nil
	}

	// Fetch all history events in one query and group by request_id in memory,
	// avoiding an N+1 pattern (one query per request row).
	historyMap := make(map[string][]Event, len(ids))
	for _, id := range ids {
		historyMap[id] = []Event{} // ensure every request has a non-nil slice
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	evQuery := "SELECT request_id, at, state, actor, note FROM request_events WHERE request_id IN (" +
		strings.Join(placeholders, ",") + ") ORDER BY at"
	evRows, err := s.db.QueryContext(ctx, evQuery, args...)
	if err == nil {
		defer evRows.Close()
		for evRows.Next() {
			var ev Event
			var atStr string
			if scanErr := evRows.Scan(&ev.RequestID, &atStr, &ev.State, &ev.Actor, &ev.Note); scanErr != nil {
				continue
			}
			ev.At, _ = dbutil.ParseTime(atStr)
			historyMap[ev.RequestID] = append(historyMap[ev.RequestID], ev)
		}
	}

	for _, req := range result {
		req.History = historyMap[req.ID]
	}
	return result, nil
}

// Get returns the request row for id, including its event history.
// Returns ErrNotFound if the id does not exist.
func (s *Store) Get(ctx context.Context, id string) (*Request, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, type, tmdb_id, tvdb_id, title, year, poster,
		        requested_by, state, reason, arr_id, created_at, updated_at, seasons
		 FROM requests WHERE id = ?`, id,
	)
	req, err := scanRequestRowSingle(row)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	history, _ := s.loadHistory(ctx, id)
	if history == nil {
		history = []Event{}
	}
	req.History = history
	return req, nil
}

// loadHistory fetches all events for a single request, ordered by at.
func (s *Store) loadHistory(ctx context.Context, id string) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT request_id, at, state, actor, note FROM request_events WHERE request_id = ? ORDER BY at`,
		id,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []Event
	for rows.Next() {
		var ev Event
		var atStr string
		if err := rows.Scan(&ev.RequestID, &atStr, &ev.State, &ev.Actor, &ev.Note); err != nil {
			continue
		}
		ev.At, _ = dbutil.ParseTime(atStr)
		history = append(history, ev)
	}
	return history, rows.Err()
}

// Insert inserts a new request row. The caller is responsible for generating
// the ID and setting all fields; no defaults are applied here.
func (s *Store) Insert(ctx context.Context, req *Request) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO requests (id, type, tmdb_id, tvdb_id, title, year, poster,
		                       requested_by, state, reason, arr_id, created_at, updated_at, seasons)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ID, req.Type, req.TmdbID, req.TvdbID, req.Title, req.Year, req.Poster,
		req.RequestedBy, string(req.State), req.Reason, req.ArrID,
		dbutil.FormatTime(req.CreatedAt), dbutil.FormatTime(req.UpdatedAt), seasonsToText(req.Seasons),
	)
	return err
}

// InsertFull inserts a request from a backup export, preserving all fields
// including ID, timestamps, and event history. Silently succeeds if the ID
// already exists (idempotent restore via INSERT OR IGNORE).
func (s *Store) InsertFull(ctx context.Context, req *Request) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO requests (id, type, tmdb_id, tvdb_id, title, year, poster,
		                                  requested_by, state, reason, arr_id, created_at, updated_at, seasons)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ID, req.Type, req.TmdbID, req.TvdbID, req.Title, req.Year, req.Poster,
		req.RequestedBy, string(req.State), req.Reason, req.ArrID,
		dbutil.FormatTime(req.CreatedAt), dbutil.FormatTime(req.UpdatedAt), seasonsToText(req.Seasons),
	)
	if err != nil {
		return err
	}
	// Insert history events, ignoring duplicates.
	for _, ev := range req.History {
		s.db.ExecContext(ctx, //nolint:errcheck — best-effort
			`INSERT OR IGNORE INTO request_events (request_id, at, state, actor, note) VALUES (?, ?, ?, ?, ?)`,
			req.ID, dbutil.FormatTime(ev.At), string(ev.State), ev.Actor, ev.Note,
		)
	}
	return nil
}

// InsertEvent appends a state-transition event to request_events.
func (s *Store) InsertEvent(ctx context.Context, ev Event) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO request_events (request_id, at, state, actor, note) VALUES (?, ?, ?, ?, ?)`,
		ev.RequestID, dbutil.FormatTime(ev.At), string(ev.State), ev.Actor, ev.Note,
	)
	return err
}

// UpdateAndInsertEvent atomically updates the mutable fields of a request and
// records a state-transition event in a single SQL transaction. Either both
// writes land or neither does. Returns ErrNotFound if the request does not
// exist (matches Update's contract).
func (s *Store) UpdateAndInsertEvent(ctx context.Context, req *Request, ev Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck — no-op if Commit succeeds

	res, err := tx.ExecContext(ctx,
		`UPDATE requests SET state=?, reason=?, arr_id=?, updated_at=? WHERE id=?`,
		string(req.State), req.Reason, req.ArrID,
		dbutil.FormatTime(req.UpdatedAt), req.ID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO request_events (request_id, at, state, actor, note) VALUES (?, ?, ?, ?, ?)`,
		ev.RequestID, dbutil.FormatTime(ev.At), string(ev.State), ev.Actor, ev.Note,
	); err != nil {
		return err
	}

	return tx.Commit()
}

// Update updates the mutable fields (state, reason, arr_id, updated_at) on an
// existing request row.
func (s *Store) Update(ctx context.Context, req *Request) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE requests SET state=?, reason=?, arr_id=?, updated_at=? WHERE id=?`,
		string(req.State), req.Reason, req.ArrID,
		dbutil.FormatTime(req.UpdatedAt), req.ID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkGrabbedIfPending atomically transitions a request from pending → grabbed,
// also persisting the final season-level selection used for the *arr add
// (seasons == nil clears the column back to ” — "all seasons" — same as an
// admin's approve-time [] override; see peligrosa.handleRequestApprove).
// Returns (true, nil) if the row was updated, (false, nil) if no row matched
// (the request was already approved/denied/deleted by another caller),
// (false, err) on SQL error.
func (s *Store) MarkGrabbedIfPending(ctx context.Context, id string, arrID int, seasons []int, updatedAt time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE requests SET state=?, arr_id=?, seasons=?, updated_at=? WHERE id=? AND state=?`,
		string(StateGrabbed), arrID, seasonsToText(seasons), dbutil.FormatTime(updatedAt), id, string(StatePending),
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Delete hard-deletes a request row and cascades to request_events.
// Returns ErrNotFound if the id does not exist.
func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM requests WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListByState returns all requests whose state matches one of the given states,
// ordered by created_at. History is NOT loaded (callers that need history should
// use All or Get).
func (s *Store) ListByState(ctx context.Context, states ...State) ([]*Request, error) {
	if len(states) == 0 {
		return []*Request{}, nil
	}
	placeholders := make([]string, len(states))
	args := make([]any, len(states))
	for i, st := range states {
		placeholders[i] = "?"
		args[i] = string(st)
	}
	query := "SELECT id, type, tmdb_id, tvdb_id, title, year, poster, " +
		"requested_by, state, reason, arr_id, created_at, updated_at, seasons " +
		"FROM requests WHERE state IN (" + strings.Join(placeholders, ",") + ") ORDER BY created_at"

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return []*Request{}, err
	}
	defer rows.Close()

	var result []*Request
	for rows.Next() {
		req, err := scanRequestRow(rows)
		if err != nil {
			continue
		}
		req.History = []Event{}
		result = append(result, req)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if result == nil {
		return []*Request{}, nil
	}
	return result, nil
}

// ListActiveByKey returns non-terminal (not denied/available) requests of the
// given type ("movie" | "series") matching key — tmdb_id for movies, tvdb_id
// for series. History is NOT loaded (matches ListByState's contract).
//
// This is the targeted counterpart to All(): the request-creation dedup
// check (findActive in internal/peligrosa) used to fetch every row in the
// table via All() and filter in Go on every single POST /api/pelicula/requests.
// A WHERE-clause lookup avoids that full scan + bulk history join, which grows
// unbounded as the requests table accumulates history over the life of a
// long-running instance (MWD-8).
func (s *Store) ListActiveByKey(ctx context.Context, reqType string, key int) ([]*Request, error) {
	if key == 0 {
		return []*Request{}, nil
	}
	keyCol := "tmdb_id"
	if reqType == "series" {
		keyCol = "tvdb_id"
	}
	query := "SELECT id, type, tmdb_id, tvdb_id, title, year, poster, " +
		"requested_by, state, reason, arr_id, created_at, updated_at, seasons " +
		"FROM requests WHERE type = ? AND " + keyCol + " = ? AND state NOT IN (?, ?) " +
		"ORDER BY created_at"

	rows, err := s.db.QueryContext(ctx, query, reqType, key, string(StateDenied), string(StateAvailable))
	if err != nil {
		return []*Request{}, err
	}
	defer rows.Close()

	var result []*Request
	for rows.Next() {
		req, err := scanRequestRow(rows)
		if err != nil {
			continue
		}
		req.History = []Event{}
		result = append(result, req)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if result == nil {
		return []*Request{}, nil
	}
	return result, nil
}

// LatestByKey returns the single newest request row (ANY state — unlike
// ListActiveByKey, terminal states are included) of the given type
// ("movie" | "series") matching key — tmdb_id for movies, tvdb_id for
// series — with its full event history loaded. Returns (nil, nil) if no row
// matches (not an error — callers treat "no request for this title" as a
// normal, common case, e.g. journey.Handler's aggregation).
//
// Used by the per-title journey endpoint, which needs the request's whole
// lifecycle (including denied/available) to render the requested/approved
// stages — ListActiveByKey's active-only filter would hide exactly the
// terminal states journey most needs to distinguish.
func (s *Store) LatestByKey(ctx context.Context, reqType string, key int) (*Request, error) {
	if key == 0 {
		return nil, nil
	}
	keyCol := "tmdb_id"
	if reqType == "series" {
		keyCol = "tvdb_id"
	}
	query := "SELECT id, type, tmdb_id, tvdb_id, title, year, poster, " +
		"requested_by, state, reason, arr_id, created_at, updated_at, seasons " +
		"FROM requests WHERE type = ? AND " + keyCol + " = ? " +
		"ORDER BY created_at DESC LIMIT 1"

	row := s.db.QueryRowContext(ctx, query, reqType, key)
	req, err := scanRequestRowSingle(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	// row is a *sql.Row — Scan already closed its underlying cursor, so this
	// second query is safe under the MaxOpenConns=1 single-connection
	// discipline (matches Get's history-after-row-close pattern above).
	history, err := s.loadHistory(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	if history == nil {
		history = []Event{}
	}
	req.History = history
	return req, nil
}

// ── scan helpers ─────────────────────────────────────────────────────────────

// rowScanner is satisfied by both *sql.Rows and *sql.Row.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRequestRow(r rowScanner) (*Request, error) {
	var req Request
	var createdAt, updatedAt string
	var poster, reason sql.NullString
	var arrID sql.NullInt64
	var seasonsText sql.NullString

	err := r.Scan(
		&req.ID, &req.Type, &req.TmdbID, &req.TvdbID,
		&req.Title, &req.Year, &poster,
		&req.RequestedBy, &req.State, &reason, &arrID,
		&createdAt, &updatedAt, &seasonsText,
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
	req.CreatedAt, _ = dbutil.ParseTime(createdAt)
	req.UpdatedAt, _ = dbutil.ParseTime(updatedAt)
	req.Seasons = seasonsFromText(seasonsText.String)
	return &req, nil
}

// scanRequestRowSingle wraps *sql.Row (which exposes Scan differently from
// *sql.Rows in that it defers the ErrNoRows check to Scan time).
func scanRequestRowSingle(r *sql.Row) (*Request, error) {
	var req Request
	var createdAt, updatedAt string
	var poster, reason sql.NullString
	var arrID sql.NullInt64
	var seasonsText sql.NullString

	err := r.Scan(
		&req.ID, &req.Type, &req.TmdbID, &req.TvdbID,
		&req.Title, &req.Year, &poster,
		&req.RequestedBy, &req.State, &reason, &arrID,
		&createdAt, &updatedAt, &seasonsText,
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
	req.CreatedAt, _ = dbutil.ParseTime(createdAt)
	req.UpdatedAt, _ = dbutil.ParseTime(updatedAt)
	req.Seasons = seasonsFromText(seasonsText.String)
	return &req, nil
}
