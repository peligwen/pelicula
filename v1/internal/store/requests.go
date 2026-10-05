package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// RequestStatus is the lifecycle of a viewer's request.
type RequestStatus string

const (
	RequestPending   RequestStatus = "pending"   // waiting for a manager
	RequestApproved  RequestStatus = "approved"  // added to Radarr/Sonarr
	RequestDeclined  RequestStatus = "declined"  // manager said no
	RequestAvailable RequestStatus = "available" // a validated file landed
)

// Request is something a viewer asked for. MediaType is "movie" or "series".
type Request struct {
	ID          int64         `json:"id"`
	MediaType   string        `json:"media_type"`
	TmdbID      int           `json:"tmdb_id,omitempty"`
	TvdbID      int           `json:"tvdb_id,omitempty"`
	Title       string        `json:"title"`
	Year        int           `json:"year,omitempty"`
	Poster      string        `json:"poster,omitempty"`
	RequestedBy string        `json:"requested_by"`
	Status      RequestStatus `json:"status"`
	ArrID       int           `json:"arr_id,omitempty"`
	DecidedBy   string        `json:"decided_by,omitempty"`
	Note        string        `json:"note,omitempty"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

const requestCols = `id, media_type, tmdb_id, tvdb_id, title, year, poster, requested_by, status, arr_id, decided_by, note, created_at, updated_at`

func scanRequest(sc interface{ Scan(...any) error }) (*Request, error) {
	var r Request
	var status, created, updated string
	if err := sc.Scan(&r.ID, &r.MediaType, &r.TmdbID, &r.TvdbID, &r.Title, &r.Year, &r.Poster,
		&r.RequestedBy, &status, &r.ArrID, &r.DecidedBy, &r.Note, &created, &updated); err != nil {
		return nil, err
	}
	r.Status = RequestStatus(status)
	r.CreatedAt = parseTS(created)
	r.UpdatedAt = parseTS(updated)
	return &r, nil
}

// CreateRequest inserts r, setting ID and timestamps. Status defaults to pending.
func (s *Store) CreateRequest(ctx context.Context, r *Request) error {
	if r.Status == "" {
		r.Status = RequestPending
	}
	t := now()
	r.CreatedAt, r.UpdatedAt = t, t
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO requests (media_type, tmdb_id, tvdb_id, title, year, poster, requested_by, status, arr_id, decided_by, note, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.MediaType, r.TmdbID, r.TvdbID, r.Title, r.Year, r.Poster, r.RequestedBy, string(r.Status),
		r.ArrID, r.DecidedBy, r.Note, ts(t), ts(t))
	if err != nil {
		return err
	}
	r.ID, err = res.LastInsertId()
	return err
}

// GetRequest returns one request or ErrNotFound.
func (s *Store) GetRequest(ctx context.Context, id int64) (*Request, error) {
	r, err := scanRequest(s.db.QueryRowContext(ctx, `SELECT `+requestCols+` FROM requests WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// ListRequests returns requests newest first. An empty username returns all
// requests; otherwise only that user's.
func (s *Store) ListRequests(ctx context.Context, username string) ([]Request, error) {
	q := `SELECT ` + requestCols + ` FROM requests`
	var args []any
	if username != "" {
		q += ` WHERE requested_by = ?`
		args = append(args, username)
	}
	q += ` ORDER BY id DESC LIMIT 500`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// FindOpenRequest returns a pending or approved request for the same title
// (matched on media type plus TMDB or TVDB id), or nil.
func (s *Store) FindOpenRequest(ctx context.Context, mediaType string, tmdbID, tvdbID int) (*Request, error) {
	r, err := scanRequest(s.db.QueryRowContext(ctx, `
		SELECT `+requestCols+` FROM requests
		WHERE media_type = ? AND status IN ('pending', 'approved')
		  AND ((tmdb_id != 0 AND tmdb_id = ?) OR (tvdb_id != 0 AND tvdb_id = ?))
		ORDER BY id DESC LIMIT 1`, mediaType, tmdbID, tvdbID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

// UpdateRequestStatus sets status, the deciding user, an optional note and
// the *arr id the title was added under (0 leaves it unchanged).
func (s *Store) UpdateRequestStatus(ctx context.Context, id int64, status RequestStatus, decidedBy, note string, arrID int) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE requests SET status = ?, decided_by = ?, note = ?,
		  arr_id = CASE WHEN ? != 0 THEN ? ELSE arr_id END, updated_at = ?
		WHERE id = ?`,
		string(status), decidedBy, note, arrID, arrID, ts(now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkRequestsAvailable flips approved requests for the given *arr item to
// available. arrType is "radarr" (movies) or "sonarr" (series). Returns the
// number of requests updated.
func (s *Store) MarkRequestsAvailable(ctx context.Context, arrType string, arrID int) (int64, error) {
	mediaType := "movie"
	if arrType == "sonarr" {
		mediaType = "series"
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE requests SET status = 'available', updated_at = ?
		WHERE media_type = ? AND arr_id = ? AND status = 'approved'`,
		ts(now()), mediaType, arrID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountRequests returns how many requests have the given status.
func (s *Store) CountRequests(ctx context.Context, status RequestStatus) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM requests WHERE status = ?`, string(status)).Scan(&n)
	return n, err
}
