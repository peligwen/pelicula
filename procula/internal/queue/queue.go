// Package queue provides the job queue backed by SQLite, the Job and related
// types, and the state machine constants used throughout the pipeline.
package queue

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ── State / Stage enums ──────────────────────────────────────────────────────

type JobState string

const (
	StateQueued     JobState = "queued"
	StateProcessing JobState = "processing"
	StateCompleted  JobState = "completed"
	StateFailed     JobState = "failed"
	StateCancelled  JobState = "cancelled"
)

type JobStage string

const (
	StageValidate  JobStage = "validate"
	StageCatalog   JobStage = "catalog"
	StageAwaitSubs JobStage = "await_subs"
	StageDualSub   JobStage = "dualsub"
	StageProcess   JobStage = "process"
	StageDone      JobStage = "done"
)

// ── Domain types ─────────────────────────────────────────────────────────────

type JobSource struct {
	Type                   string `json:"type"`
	Title                  string `json:"title"`
	Year                   int    `json:"year"`
	Path                   string `json:"path"`
	Size                   int64  `json:"size"`
	ArrID                  int    `json:"arr_id"`
	ArrType                string `json:"arr_type"`
	EpisodeID              int    `json:"episode_id,omitempty"`
	SeasonNumber           int    `json:"season_number,omitempty"`
	EpisodeNumber          int    `json:"episode_number,omitempty"`
	DownloadHash           string `json:"download_hash"`
	ExpectedRuntimeMinutes int    `json:"expected_runtime_minutes"`
}

type AudioTrack struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Language string `json:"language"`
	Channels int    `json:"channels,omitempty"`
}

type CodecInfo struct {
	Video       string       `json:"video"`
	Audio       string       `json:"audio"`
	AudioTracks []AudioTrack `json:"audio_tracks,omitempty"`
	Subtitles   []string     `json:"subtitles"`
	Width       int          `json:"width,omitempty"`
	Height      int          `json:"height,omitempty"`
}

type ValidationChecks struct {
	Integrity string     `json:"integrity"`
	Duration  string     `json:"duration"`
	Sample    string     `json:"sample"`
	Codecs    *CodecInfo `json:"codecs,omitempty"`
}

type ValidationResult struct {
	Passed bool             `json:"passed"`
	Checks ValidationChecks `json:"checks"`
}

// CatalogInfo records what happened during the catalog stage of a pipeline job.
type CatalogInfo struct {
	JellyfinSynced   bool `json:"jellyfin_synced"`
	NotificationSent bool `json:"notification_sent"`
}

// FlagSeverity orders flags by urgency.
type FlagSeverity string

const (
	FlagSeverityError FlagSeverity = "error"
	FlagSeverityWarn  FlagSeverity = "warn"
	FlagSeverityInfo  FlagSeverity = "info"
)

// Flag is a single derived issue on a media item.
type Flag struct {
	Code     string         `json:"code"`
	Severity FlagSeverity   `json:"severity"`
	Detail   string         `json:"detail,omitempty"`
	Fields   map[string]any `json:"fields,omitempty"`
}

// Job is a single unit of work managed by the Queue.
type Job struct {
	ID           string            `json:"id"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	State        JobState          `json:"state"`
	Stage        JobStage          `json:"stage"`
	Progress     float64           `json:"progress"`
	Source       JobSource         `json:"source"`
	Validation   *ValidationResult `json:"validation,omitempty"`
	MissingSubs  []string          `json:"missing_subs,omitempty"`
	SubsAcquired []string          `json:"subs_acquired,omitempty"`
	Error        string            `json:"error,omitempty"`
	RetryCount   int               `json:"retry_count"`

	ManualProfile string `json:"manual_profile,omitempty"`

	DualSubOutputs []string `json:"dualsub_outputs,omitempty"`
	DualSubError   string   `json:"dualsub_error,omitempty"`

	TranscodeProfile  string   `json:"transcode_profile,omitempty"`
	TranscodeDecision string   `json:"transcode_decision,omitempty"`
	TranscodeOutputs  []string `json:"transcode_outputs,omitempty"`
	TranscodeError    string   `json:"transcode_error,omitempty"`
	TranscodeETA      float64  `json:"transcode_eta,omitempty"`

	ActionType string         `json:"action_type,omitempty"`
	Params     map[string]any `json:"params,omitempty"`
	Result     map[string]any `json:"result,omitempty"`

	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`

	Catalog *CatalogInfo `json:"catalog,omitempty"`
	Flags   []Flag       `json:"flags,omitempty"`
}

// ── Queue ────────────────────────────────────────────────────────────────────

// BackoffDuration returns the retry delay given the current retry count.
// Exported so callers (pipeline, actions) can use the same backoff policy.
func BackoffDuration(retryCount int) time.Duration {
	switch retryCount {
	case 1:
		return 1 * time.Minute
	case 2:
		return 5 * time.Minute
	case 3:
		return 30 * time.Minute
	default:
		return 2 * time.Hour
	}
}

// IsTerminal reports whether s is a terminal job state.
func IsTerminal(s JobState) bool {
	return s == StateCompleted || s == StateFailed || s == StateCancelled
}

// Queue manages the job queue backed by SQLite.
type Queue struct {
	db        *sql.DB
	pending   chan struct{}
	cancelsMu sync.Mutex
	cancels   map[string]context.CancelFunc
}

// NewQueue creates a Queue over db, loading any existing queued/processing jobs.
func NewQueue(db *sql.DB) (*Queue, error) {
	q := &Queue{
		db:      db,
		pending: make(chan struct{}, 1),
		cancels: make(map[string]context.CancelFunc),
	}
	if err := q.loadExisting(); err != nil {
		return nil, err
	}
	return q, nil
}

// DB returns the underlying database handle.
func (q *Queue) DB() *sql.DB { return q.db }

// Pending returns the pending wake semaphore channel for the worker loop.
func (q *Queue) Pending() <-chan struct{} { return q.pending }

// SignalPending sends a non-blocking signal to the pending channel.
func (q *Queue) SignalPending() {
	select {
	case q.pending <- struct{}{}:
	default:
	}
}

func (q *Queue) loadExisting() error {
	rows, err := q.db.Query(`SELECT id, state FROM jobs WHERE state IN ('queued','processing')`)
	if err != nil {
		return fmt.Errorf("loadExisting query: %w", err)
	}
	defer rows.Close()

	type row struct {
		id    string
		state JobState
	}
	var toProcess []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.state); err != nil {
			slog.Warn("loadExisting scan error", "component", "queue", "error", err)
			continue
		}
		toProcess = append(toProcess, r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("loadExisting rows: %w", err)
	}

	for _, r := range toProcess {
		if r.state == StateProcessing {
			var retryCount int
			err := q.db.QueryRow(`SELECT retry_count FROM jobs WHERE id=?`, r.id).Scan(&retryCount)
			if err != nil {
				slog.Warn("could not read retry_count for interrupted job", "component", "queue", "job_id", r.id, "error", err)
				continue
			}
			retryCount++
			const interruptCap = 5
			if retryCount > interruptCap {
				_, err = q.db.Exec(
					`UPDATE jobs SET state=?, error=?, retry_count=?, next_attempt_at=NULL, updated_at=? WHERE id=?`,
					string(StateFailed), "interrupted repeatedly",
					retryCount, time.Now().UTC().Format(time.RFC3339Nano), r.id,
				)
				if err != nil {
					slog.Warn("could not mark interrupted job as failed", "component", "queue", "job_id", r.id, "error", err)
				} else {
					slog.Warn("job marked failed: interrupted too many times", "component", "queue", "job_id", r.id, "retry_count", retryCount)
				}
				continue
			}
			next := time.Now().UTC().Add(BackoffDuration(retryCount))
			_, err = q.db.Exec(
				`UPDATE jobs SET state=?, stage=?, progress=0, error=?, retry_count=?, next_attempt_at=?, updated_at=? WHERE id=?`,
				string(StateQueued), string(StageValidate), "interrupted: restarted",
				retryCount, next.Format(time.RFC3339Nano),
				time.Now().UTC().Format(time.RFC3339Nano), r.id,
			)
			if err != nil {
				slog.Warn("could not reset interrupted job", "component", "queue", "job_id", r.id, "error", err)
				continue
			}
			slog.Info("interrupted job re-queued with backoff", "component", "queue", "job_id", r.id,
				"retry_count", retryCount, "next_attempt_at", next.Format(time.RFC3339))
		}
		q.SignalPending()
	}
	return nil
}

func (q *Queue) insertJobRow(j *Job) error {
	sourceJSON, err := json.Marshal(j.Source)
	if err != nil {
		return fmt.Errorf("marshal source: %w", err)
	}
	var paramsJSON *string
	if j.Params != nil {
		b, _ := json.Marshal(j.Params)
		s := string(b)
		paramsJSON = &s
	}
	var nextAttemptAt *string
	if j.NextAttemptAt != nil {
		s := j.NextAttemptAt.UTC().Format(time.RFC3339Nano)
		nextAttemptAt = &s
	}
	_, err = q.db.Exec(
		`INSERT INTO jobs (id, created_at, updated_at, state, stage, progress, source, error, retry_count,
		                   manual_profile, dualsub_error, transcode_profile, transcode_decision, transcode_error, transcode_eta,
		                   action_type, params, result, flags, next_attempt_at)
		 VALUES (?, ?, ?, ?, ?, 0, ?, '', 0, '', '', '', '', '', 0, ?, ?, NULL, NULL, ?)`,
		j.ID,
		j.CreatedAt.Format(time.RFC3339Nano),
		j.UpdatedAt.Format(time.RFC3339Nano),
		string(j.State),
		string(j.Stage),
		string(sourceJSON),
		j.ActionType,
		paramsJSON,
		nextAttemptAt,
	)
	return err
}

// Create inserts a new pipeline job, de-duplicating by source path.
func (q *Queue) Create(source JobSource) (*Job, error) {
	var existingID string
	err := q.db.QueryRow(
		`SELECT id FROM jobs WHERE json_extract(source, '$.path') = ? AND state IN ('queued','processing') LIMIT 1`,
		source.Path,
	).Scan(&existingID)
	if err == nil {
		if job, ok := q.Get(existingID); ok {
			slog.Info("duplicate job, returning existing", "component", "queue", "path", source.Path, "existing_id", existingID)
			return job, nil
		}
	}

	now := time.Now().UTC()
	job := &Job{
		ID:         fmt.Sprintf("job_%d_%s", now.UnixMilli(), randStr(6)),
		CreatedAt:  now,
		UpdatedAt:  now,
		State:      StateQueued,
		Stage:      StageValidate,
		Source:     source,
		ActionType: "pipeline",
	}

	if err := q.insertJobRow(job); err != nil {
		return nil, fmt.Errorf("insert job: %w", err)
	}

	q.SignalPending()
	return job, nil
}

// CreateActionJob inserts a new action-bus job without path de-duplication.
func (q *Queue) CreateActionJob(source JobSource, actionType string, params map[string]any) (*Job, error) {
	now := time.Now().UTC()
	job := &Job{
		ID:         fmt.Sprintf("job_%d_%s", now.UnixMilli(), randStr(6)),
		CreatedAt:  now,
		UpdatedAt:  now,
		State:      StateQueued,
		Stage:      StageValidate,
		Source:     source,
		ActionType: actionType,
		Params:     params,
	}

	if err := q.insertJobRow(job); err != nil {
		return nil, fmt.Errorf("insert action job: %w", err)
	}

	q.SignalPending()
	return job, nil
}

// Get returns the job with the given id.
func (q *Queue) Get(id string) (*Job, bool) {
	row := q.db.QueryRow(
		`SELECT id, created_at, updated_at, state, stage, progress, source, validation, missing_subs,
		        subs_acquired,
		        error, retry_count, manual_profile, dualsub_outputs, dualsub_error,
		        transcode_profile, transcode_decision, transcode_outputs, transcode_error, transcode_eta,
		        action_type, params, result, catalog, flags, next_attempt_at
		 FROM jobs WHERE id=?`, id,
	)
	job, err := scanJob(row)
	if err != nil {
		return nil, false
	}
	return job, true
}

// List returns all jobs sorted by created_at ascending.
func (q *Queue) List() []Job {
	rows, err := q.db.Query(
		`SELECT id, created_at, updated_at, state, stage, progress, source, validation, missing_subs,
		        subs_acquired,
		        error, retry_count, manual_profile, dualsub_outputs, dualsub_error,
		        transcode_profile, transcode_decision, transcode_outputs, transcode_error, transcode_eta,
		        action_type, params, result, catalog, flags, next_attempt_at
		 FROM jobs ORDER BY created_at ASC`,
	)
	if err != nil {
		slog.Warn("List query failed", "component", "queue", "error", err)
		return nil
	}
	defer rows.Close()

	var out []Job
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			slog.Warn("List scan failed", "component", "queue", "error", err)
			continue
		}
		out = append(out, *job)
	}
	return out
}

// ListByActionType returns jobs filtered by action_type. Empty string returns all.
func (q *Queue) ListByActionType(actionType string) []Job {
	all := q.List()
	if actionType == "" {
		return all
	}
	var out []Job
	for _, j := range all {
		if j.ActionType == actionType {
			out = append(out, j)
		}
	}
	return out
}

// Update applies fn to the job and persists the result.
func (q *Queue) Update(id string, fn func(*Job)) error {
	job, ok := q.Get(id)
	if !ok {
		return fmt.Errorf("job %s not found", id)
	}

	fn(job)
	job.UpdatedAt = time.Now().UTC()

	sourceJSON, _ := json.Marshal(job.Source)

	var validationJSON *string
	if job.Validation != nil {
		b, _ := json.Marshal(job.Validation)
		s := string(b)
		validationJSON = &s
	}

	var missingSubsJSON *string
	if job.MissingSubs != nil {
		b, _ := json.Marshal(job.MissingSubs)
		s := string(b)
		missingSubsJSON = &s
	}

	var subsAcquiredJSON *string
	if job.SubsAcquired != nil {
		b, _ := json.Marshal(job.SubsAcquired)
		s := string(b)
		subsAcquiredJSON = &s
	}

	var dualSubOutputsJSON *string
	if job.DualSubOutputs != nil {
		b, _ := json.Marshal(job.DualSubOutputs)
		s := string(b)
		dualSubOutputsJSON = &s
	}

	var transcodeOutputsJSON *string
	if job.TranscodeOutputs != nil {
		b, _ := json.Marshal(job.TranscodeOutputs)
		s := string(b)
		transcodeOutputsJSON = &s
	}

	var paramsJSON *string
	if job.Params != nil {
		b, _ := json.Marshal(job.Params)
		s := string(b)
		paramsJSON = &s
	}
	var resultJSON *string
	if job.Result != nil {
		b, _ := json.Marshal(job.Result)
		s := string(b)
		resultJSON = &s
	}
	var catalogJSON *string
	if job.Catalog != nil {
		b, _ := json.Marshal(job.Catalog)
		s := string(b)
		catalogJSON = &s
	}

	var flagsJSON *string
	if job.Flags != nil {
		b, _ := json.Marshal(job.Flags)
		s := string(b)
		flagsJSON = &s
	}

	var nextAttemptAtStr *string
	if job.NextAttemptAt != nil {
		s := job.NextAttemptAt.UTC().Format(time.RFC3339Nano)
		nextAttemptAtStr = &s
	}

	_, err := q.db.Exec(
		`UPDATE jobs SET
			updated_at=?, state=?, stage=?, progress=?, source=?, validation=?, missing_subs=?,
			subs_acquired=?,
			error=?, retry_count=?, manual_profile=?, dualsub_outputs=?, dualsub_error=?,
			transcode_profile=?, transcode_decision=?, transcode_outputs=?, transcode_error=?, transcode_eta=?,
			action_type=?, params=?, result=?, catalog=?, flags=?, next_attempt_at=?
		 WHERE id=?`,
		job.UpdatedAt.Format(time.RFC3339Nano),
		string(job.State), string(job.Stage), job.Progress,
		string(sourceJSON), validationJSON, missingSubsJSON,
		subsAcquiredJSON,
		job.Error, job.RetryCount, job.ManualProfile, dualSubOutputsJSON, job.DualSubError,
		job.TranscodeProfile, job.TranscodeDecision, transcodeOutputsJSON, job.TranscodeError, job.TranscodeETA,
		job.ActionType, paramsJSON, resultJSON, catalogJSON, flagsJSON, nextAttemptAtStr,
		id,
	)
	if err != nil {
		slog.Warn("failed to persist job", "component", "queue", "job_id", id, "error", err)
		return err
	}
	return nil
}

// Retry re-queues a failed or cancelled job.
func (q *Queue) Retry(id string) error {
	found := false
	err := q.Update(id, func(j *Job) {
		if j.State == StateFailed || j.State == StateCancelled {
			j.State = StateQueued
			j.Stage = StageValidate
			j.Progress = 0
			j.Error = ""
			j.RetryCount++
			j.NextAttemptAt = nil
			found = true
		}
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("job %s is not in a retryable state", id)
	}
	q.SignalPending()
	return nil
}

// Cancel marks a queued or processing job as cancelled and kills its context.
func (q *Queue) Cancel(id string) error {
	err := q.Update(id, func(j *Job) {
		if j.State == StateQueued || j.State == StateProcessing {
			j.State = StateCancelled
		}
	})
	if err != nil {
		return err
	}
	q.cancelsMu.Lock()
	if fn, ok := q.cancels[id]; ok {
		fn()
		delete(q.cancels, id)
	}
	q.cancelsMu.Unlock()
	return nil
}

// Wait polls for a terminal state on id, returning the final snapshot.
func (q *Queue) Wait(id string, timeout time.Duration) (*Job, error) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	if job, ok := q.Get(id); ok {
		if IsTerminal(job.State) {
			return job, nil
		}
	} else {
		return nil, fmt.Errorf("job %s not found", id)
	}

	for {
		<-ticker.C
		job, ok := q.Get(id)
		if !ok {
			return nil, fmt.Errorf("job %s not found", id)
		}
		if IsTerminal(job.State) {
			return job, nil
		}
		if time.Now().After(deadline) {
			return job, fmt.Errorf("timeout after %s waiting for job %s", timeout, id)
		}
	}
}

// RegisterCancel stores a context cancel func for a running job.
func (q *Queue) RegisterCancel(id string, fn context.CancelFunc) {
	q.cancelsMu.Lock()
	q.cancels[id] = fn
	q.cancelsMu.Unlock()
}

// UnregisterCancel removes the cancel func for id.
func (q *Queue) UnregisterCancel(id string) {
	q.cancelsMu.Lock()
	delete(q.cancels, id)
	q.cancelsMu.Unlock()
}

// NextQueued returns up to 64 immediately-eligible job IDs, oldest first.
func (q *Queue) NextQueued() []string {
	rows, err := q.db.Query(
		`SELECT id FROM jobs
		  WHERE state='queued'
		    AND (next_attempt_at IS NULL OR datetime(next_attempt_at) <= datetime('now'))
		  ORDER BY created_at ASC LIMIT 64`,
	)
	if err != nil {
		slog.Warn("nextQueued query failed", "component", "queue", "error", err)
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// Status returns a count of jobs in each state.
func (q *Queue) Status() map[string]int {
	counts := map[string]int{
		"queued":     0,
		"processing": 0,
		"completed":  0,
		"failed":     0,
		"cancelled":  0,
	}

	rows, err := q.db.Query(`SELECT state, COUNT(*) FROM jobs GROUP BY state`)
	if err != nil {
		slog.Warn("Status query failed", "component", "queue", "error", err)
		return counts
	}
	defer rows.Close()

	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err == nil {
			counts[state] = count
		}
	}
	return counts
}

// ── Catalog flags ────────────────────────────────────────────────────────────

// CatalogFlagRow is what the HTTP handler serves.
type CatalogFlagRow struct {
	Path      string    `json:"path"`
	Flags     []Flag    `json:"flags"`
	Severity  string    `json:"severity"`
	JobID     string    `json:"job_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UpsertFlagsForPath persists flags in the catalog_flags index.
// Empty flags delete the row.
func UpsertFlagsForPath(db *sql.DB, path, jobID string, flags []Flag) error {
	if path == "" {
		return fmt.Errorf("UpsertFlagsForPath: empty path")
	}
	if len(flags) == 0 {
		_, err := db.Exec(`DELETE FROM catalog_flags WHERE path = ?`, path)
		return err
	}
	data, err := json.Marshal(flags)
	if err != nil {
		return fmt.Errorf("marshal flags: %w", err)
	}
	_, err = db.Exec(
		`INSERT INTO catalog_flags (path, flags, severity, job_id, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(path) DO UPDATE SET
		   flags=excluded.flags,
		   severity=excluded.severity,
		   job_id=excluded.job_id,
		   updated_at=excluded.updated_at`,
		path, string(data), string(topSeverity(flags)), jobID,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

// FlagsByPath returns the flag row for a single path, or (nil, nil) if absent.
func FlagsByPath(db *sql.DB, path string) (*CatalogFlagRow, error) {
	row := db.QueryRow(
		`SELECT path, flags, severity, job_id, updated_at FROM catalog_flags WHERE path = ?`,
		path,
	)
	var r CatalogFlagRow
	var flagsJSON, tsStr string
	err := row.Scan(&r.Path, &flagsJSON, &r.Severity, &r.JobID, &tsStr)
	if err != nil {
		if isNoRows(err) {
			return nil, nil
		}
		return nil, err
	}
	json.Unmarshal([]byte(flagsJSON), &r.Flags) //nolint:errcheck
	if t, err := time.Parse(time.RFC3339Nano, tsStr); err == nil {
		r.UpdatedAt = t
	}
	return &r, nil
}

// AllFlagged returns every catalog_flags row sorted with errors first.
func AllFlagged(db *sql.DB) ([]CatalogFlagRow, error) {
	rows, err := db.Query(
		`SELECT path, flags, severity, job_id, updated_at FROM catalog_flags
		 ORDER BY
		   CASE severity WHEN 'error' THEN 0 WHEN 'warn' THEN 1 ELSE 2 END,
		   updated_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CatalogFlagRow
	for rows.Next() {
		var r CatalogFlagRow
		var flagsJSON, tsStr string
		if err := rows.Scan(&r.Path, &flagsJSON, &r.Severity, &r.JobID, &tsStr); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(flagsJSON), &r.Flags) //nolint:errcheck
		if t, err := time.Parse(time.RFC3339Nano, tsStr); err == nil {
			r.UpdatedAt = t
		}
		out = append(out, r)
	}
	return out, nil
}

// topSeverity picks the most urgent severity from a flag list.
func topSeverity(flags []Flag) FlagSeverity {
	rank := func(s FlagSeverity) int {
		switch s {
		case FlagSeverityError:
			return 3
		case FlagSeverityWarn:
			return 2
		case FlagSeverityInfo:
			return 1
		}
		return 0
	}
	best := FlagSeverity("")
	for _, f := range flags {
		if rank(f.Severity) > rank(best) {
			best = f.Severity
		}
	}
	return best
}

// ── scanner helper ────────────────────────────────────────────────────────────

type scanner interface {
	Scan(dest ...any) error
}

func scanJob(s scanner) (*Job, error) {
	var (
		job                  Job
		createdAtStr         string
		updatedAtStr         string
		sourceJSON           string
		validationJSON       *string
		missingSubsJSON      *string
		subsAcquiredJSON     *string
		dualSubOutputsJSON   *string
		transcodeOutputsJSON *string
		actionType           string
		paramsJSON           *string
		resultJSON           *string
		catalogJSON          *string
		flagsJSON            *string
		nextAttemptAtStr     *string
	)

	err := s.Scan(
		&job.ID, &createdAtStr, &updatedAtStr,
		&job.State, &job.Stage, &job.Progress,
		&sourceJSON, &validationJSON, &missingSubsJSON,
		&subsAcquiredJSON,
		&job.Error, &job.RetryCount, &job.ManualProfile,
		&dualSubOutputsJSON, &job.DualSubError,
		&job.TranscodeProfile, &job.TranscodeDecision,
		&transcodeOutputsJSON, &job.TranscodeError, &job.TranscodeETA,
		&actionType, &paramsJSON, &resultJSON, &catalogJSON, &flagsJSON,
		&nextAttemptAtStr,
	)
	if err != nil {
		return nil, err
	}

	if t, err := time.Parse(time.RFC3339Nano, createdAtStr); err == nil {
		job.CreatedAt = t
	}
	if t, err := time.Parse(time.RFC3339Nano, updatedAtStr); err == nil {
		job.UpdatedAt = t
	}

	json.Unmarshal([]byte(sourceJSON), &job.Source) //nolint:errcheck

	if validationJSON != nil {
		var v ValidationResult
		if err := json.Unmarshal([]byte(*validationJSON), &v); err == nil {
			job.Validation = &v
		}
	}

	if missingSubsJSON != nil {
		json.Unmarshal([]byte(*missingSubsJSON), &job.MissingSubs) //nolint:errcheck
	}

	if subsAcquiredJSON != nil {
		json.Unmarshal([]byte(*subsAcquiredJSON), &job.SubsAcquired) //nolint:errcheck
	}

	if dualSubOutputsJSON != nil {
		json.Unmarshal([]byte(*dualSubOutputsJSON), &job.DualSubOutputs) //nolint:errcheck
	}

	if transcodeOutputsJSON != nil {
		json.Unmarshal([]byte(*transcodeOutputsJSON), &job.TranscodeOutputs) //nolint:errcheck
	}

	job.ActionType = actionType
	if job.ActionType == "" {
		job.ActionType = "pipeline"
	}

	if paramsJSON != nil {
		json.Unmarshal([]byte(*paramsJSON), &job.Params) //nolint:errcheck
	}
	if resultJSON != nil {
		json.Unmarshal([]byte(*resultJSON), &job.Result) //nolint:errcheck
	}
	if catalogJSON != nil {
		var c CatalogInfo
		if err := json.Unmarshal([]byte(*catalogJSON), &c); err == nil {
			job.Catalog = &c
		}
	}

	if flagsJSON != nil {
		json.Unmarshal([]byte(*flagsJSON), &job.Flags) //nolint:errcheck
	}

	if nextAttemptAtStr != nil {
		if t, err := time.Parse(time.RFC3339Nano, *nextAttemptAtStr); err == nil {
			job.NextAttemptAt = &t
		}
	}

	return &job, nil
}

// randStr returns a hex-encoded random string. n is number of bytes; minimum 8.
func randStr(n int) string {
	if n < 8 {
		n = 8
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// isNoRows reports whether err is a "no rows" error from database/sql.
func isNoRows(err error) bool {
	return err == sql.ErrNoRows
}
