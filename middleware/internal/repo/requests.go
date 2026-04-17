package repo

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"pelicula-api/internal/repo/dbutil"
)

// RequestState is the lifecycle state of a media request.
type RequestState string

const (
	RequestPending   RequestState = "pending"
	RequestApproved  RequestState = "approved"
	RequestDenied    RequestState = "denied"
	RequestGrabbed   RequestState = "grabbed"
	RequestAvailable RequestState = "available"
)

// RequestEvent records a single state transition.
type RequestEvent struct {
	RequestID string
	At        time.Time
	State     RequestState
	Actor     string
	Note      string
}

// MediaRequest is a persisted viewer media request.
type MediaRequest struct {
	ID          string
	Type        string
	TmdbID      int
	TvdbID      int
	Title       string
	Year        int
	Poster      string
	RequestedBy string
	State       RequestState
	Reason      string
	ArrID       int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// RequestStore persists media requests in SQLite.
type RequestStore struct{ db *sql.DB }

// NewRequestStore creates a RequestStore backed by db.
func NewRequestStore(db *sql.DB) *RequestStore { return &RequestStore{db: db} }

// Insert creates a new request row.
func (s *RequestStore) Insert(ctx context.Context, req MediaRequest) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO requests (id, type, tmdb_id, tvdb_id, title, year, poster,
		                       requested_by, state, reason, arr_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ID, req.Type, req.TmdbID, req.TvdbID, req.Title, req.Year, req.Poster,
		req.RequestedBy, string(req.State), req.Reason, req.ArrID,
		dbutil.FormatTime(req.CreatedAt),
		dbutil.FormatTime(req.UpdatedAt),
	)
	return err
}

// InsertOrIgnore inserts a request, ignoring conflicts on id (restore path).
func (s *RequestStore) InsertOrIgnore(ctx context.Context, req MediaRequest) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO requests (id, type, tmdb_id, tvdb_id, title, year, poster,
		                                  requested_by, state, reason, arr_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ID, req.Type, req.TmdbID, req.TvdbID, req.Title, req.Year, req.Poster,
		req.RequestedBy, string(req.State), req.Reason, req.ArrID,
		dbutil.FormatTime(req.CreatedAt),
		dbutil.FormatTime(req.UpdatedAt),
	)
	return err
}

// Update persists mutable fields (state, reason, arr_id, updated_at) for a request.
func (s *RequestStore) Update(ctx context.Context, req MediaRequest) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE requests SET state=?, reason=?, arr_id=?, updated_at=? WHERE id=?`,
		string(req.State), req.Reason, req.ArrID,
		dbutil.FormatTime(req.UpdatedAt), req.ID,
	)
	return err
}

// Delete hard-deletes a request by id.
func (s *RequestStore) Delete(ctx context.Context, id string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM requests WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// Get returns the request for id, or (nil, nil) if not found.
func (s *RequestStore) Get(ctx context.Context, id string) (*MediaRequest, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, type, tmdb_id, tvdb_id, title, year, poster,
		        requested_by, state, reason, arr_id, created_at, updated_at
		 FROM requests WHERE id = ?`, id,
	)
	return scanRequest(row)
}

// All returns every request ordered by created_at ASC.
func (s *RequestStore) All(ctx context.Context) ([]*MediaRequest, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, tmdb_id, tvdb_id, title, year, poster,
		        requested_by, state, reason, arr_id, created_at, updated_at
		 FROM requests ORDER BY created_at`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*MediaRequest
	for rows.Next() {
		req, err := scanRequest(rows)
		if err != nil {
			continue
		}
		out = append(out, req)
	}
	return out, rows.Err()
}

// InsertEvent appends a state-transition event to request_events.
func (s *RequestStore) InsertEvent(ctx context.Context, ev RequestEvent) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO request_events (request_id, at, state, actor, note)
		 VALUES (?, ?, ?, ?, ?)`,
		ev.RequestID,
		dbutil.FormatTime(ev.At),
		string(ev.State),
		ev.Actor, ev.Note,
	)
	return err
}

// InsertEventOrIgnore inserts an event, ignoring duplicates (restore path).
func (s *RequestStore) InsertEventOrIgnore(ctx context.Context, ev RequestEvent) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO request_events (request_id, at, state, actor, note)
		 VALUES (?, ?, ?, ?, ?)`,
		ev.RequestID,
		dbutil.FormatTime(ev.At),
		string(ev.State),
		ev.Actor, ev.Note,
	)
	return err
}

// ListEvents returns all events for the given request IDs, ordered by at ASC.
// Pass a single id to fetch one request's history; pass multiple for a bulk load.
func (s *RequestStore) ListEvents(ctx context.Context, ids ...string) ([]RequestEvent, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx,
		"SELECT request_id, at, state, actor, note FROM request_events WHERE request_id IN ("+placeholders+") ORDER BY at",
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RequestEvent
	for rows.Next() {
		var ev RequestEvent
		var atStr string
		if err := rows.Scan(&ev.RequestID, &atStr, &ev.State, &ev.Actor, &ev.Note); err != nil {
			continue
		}
		if t, err := dbutil.ParseTime(atStr); err == nil {
			ev.At = t
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// ── helpers ───────────────────────────────────────────────────────────────────

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRequest(s rowScanner) (*MediaRequest, error) {
	var req MediaRequest
	var poster, reason sql.NullString
	var arrID sql.NullInt64
	var createdAtStr, updatedAtStr string

	err := s.Scan(
		&req.ID, &req.Type, &req.TmdbID, &req.TvdbID,
		&req.Title, &req.Year, &poster,
		&req.RequestedBy, &req.State, &reason, &arrID,
		&createdAtStr, &updatedAtStr,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
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
	if t, err := dbutil.ParseTime(createdAtStr); err == nil {
		req.CreatedAt = t
	}
	if t, err := dbutil.ParseTime(updatedAtStr); err == nil {
		req.UpdatedAt = t
	}
	return &req, nil
}
