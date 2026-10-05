package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// JobStatus is the lifecycle of one imported file through the pipeline.
type JobStatus string

const (
	JobQueued  JobStatus = "queued"
	JobRunning JobStatus = "running"
	JobPassed  JobStatus = "passed"
	JobFailed  JobStatus = "failed"
)

// Job is one imported file to validate. ArrType is "radarr" or "sonarr";
// ArrID is the movie or series id; EpisodeID is set for Sonarr imports.
// RuntimeMin is the expected runtime from *arr (0 when unknown) and drives
// the sample and duration checks.
type Job struct {
	ID         int64      `json:"id"`
	ArrType    string     `json:"arr_type"`
	ArrID      int        `json:"arr_id"`
	EpisodeID  int        `json:"episode_id,omitempty"`
	Title      string     `json:"title"`
	Path       string     `json:"path"`
	Size       int64      `json:"size"`
	DownloadID string     `json:"download_id,omitempty"`
	RuntimeMin int        `json:"runtime_min,omitempty"`
	Status     JobStatus  `json:"status"`
	Result     string     `json:"result,omitempty"` // JSON written by the pipeline
	Error      string     `json:"error,omitempty"`
	Attempts   int        `json:"attempts"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

const jobCols = `id, arr_type, arr_id, episode_id, title, path, size, download_id, runtime_min, status, result, error, attempts, created_at, started_at, finished_at`

func scanJob(sc interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	var status, created string
	var started, finished sql.NullString
	if err := sc.Scan(&j.ID, &j.ArrType, &j.ArrID, &j.EpisodeID, &j.Title, &j.Path, &j.Size, &j.DownloadID,
		&j.RuntimeMin, &status, &j.Result, &j.Error, &j.Attempts, &created, &started, &finished); err != nil {
		return nil, err
	}
	j.Status = JobStatus(status)
	j.CreatedAt = parseTS(created)
	j.StartedAt = nullTS(started)
	j.FinishedAt = nullTS(finished)
	return &j, nil
}

// EnqueueJob inserts j as queued and sets j.ID. If a queued or running job
// already exists for the same path, no row is inserted and j.ID is set to
// the existing job's id (dedupe for *arr webhook retries).
func (s *Store) EnqueueJob(ctx context.Context, j *Job) error {
	var existing int64
	err := s.db.QueryRowContext(ctx, `
		SELECT id FROM jobs WHERE path = ? AND status IN ('queued', 'running') ORDER BY id DESC LIMIT 1`, j.Path).Scan(&existing)
	if err == nil {
		j.ID = existing
		j.Status = JobQueued
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	j.Status = JobQueued
	j.CreatedAt = now()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO jobs (arr_type, arr_id, episode_id, title, path, size, download_id, runtime_min, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'queued', ?)`,
		j.ArrType, j.ArrID, j.EpisodeID, j.Title, j.Path, j.Size, j.DownloadID, j.RuntimeMin, ts(j.CreatedAt))
	if err != nil {
		return err
	}
	j.ID, err = res.LastInsertId()
	return err
}

// ClaimNextJob atomically moves the oldest queued job to running, increments
// its attempt count and returns it. It returns nil, nil when the queue is empty.
func (s *Store) ClaimNextJob(ctx context.Context) (*Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM jobs WHERE status = 'queued' ORDER BY id LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE jobs SET status = 'running', attempts = attempts + 1, started_at = ?, finished_at = NULL, error = ''
		WHERE id = ?`, ts(now()), id); err != nil {
		return nil, err
	}
	j, err := scanJob(tx.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	return j, tx.Commit()
}

// FinishJob records the outcome of a running job.
func (s *Store) FinishJob(ctx context.Context, id int64, status JobStatus, result, errMsg string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET status = ?, result = ?, error = ?, finished_at = ? WHERE id = ?`,
		string(status), result, errMsg, ts(now()), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RequeueJob puts a finished job back in the queue.
func (s *Store) RequeueJob(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET status = 'queued', error = '', started_at = NULL, finished_at = NULL
		WHERE id = ? AND status IN ('passed', 'failed')`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetJob returns one job or ErrNotFound.
func (s *Store) GetJob(ctx context.Context, id int64) (*Job, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return j, err
}

// ListJobs returns the most recent jobs, newest first.
func (s *Store) ListJobs(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobCols+` FROM jobs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// CountJobs returns how many jobs have the given status.
func (s *Store) CountJobs(ctx context.Context, status JobStatus) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE status = ?`, string(status)).Scan(&n)
	return n, err
}

// ResetRunningJobs requeues jobs left running by a previous process. Call
// once at startup before the worker begins.
func (s *Store) ResetRunningJobs(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE jobs SET status = 'queued', started_at = NULL WHERE status = 'running'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneJobs deletes finished jobs older than before, returning how many.
func (s *Store) PruneJobs(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM jobs WHERE status IN ('passed', 'failed') AND finished_at IS NOT NULL AND finished_at < ?`, ts(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
