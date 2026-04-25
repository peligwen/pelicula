package procula

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
	StageAwaitSubs JobStage = "await_subs" // NEW: inserted between catalog and dualsub
	StageDualSub   JobStage = "dualsub"
	StageProcess   JobStage = "process"
	StageDone      JobStage = "done"
)

type JobSource struct {
	Type                   string `json:"type"` // "movie" or "episode"
	Title                  string `json:"title"`
	Year                   int    `json:"year"`
	Path                   string `json:"path"`
	Size                   int64  `json:"size"`
	ArrID                  int    `json:"arr_id"`
	ArrType                string `json:"arr_type"` // "radarr" or "sonarr"
	EpisodeID              int    `json:"episode_id,omitempty"`
	SeasonNumber           int    `json:"season_number,omitempty"`
	EpisodeNumber          int    `json:"episode_number,omitempty"`
	DownloadHash           string `json:"download_hash"`
	ExpectedRuntimeMinutes int    `json:"expected_runtime_minutes"`
}

type AudioTrack struct {
	Index    int    `json:"index"` // ffprobe absolute stream index
	Codec    string `json:"codec"`
	Language string `json:"language"`
	Channels int    `json:"channels,omitempty"`
}

type CodecInfo struct {
	Video       string       `json:"video"`
	Audio       string       `json:"audio"`                  // first track codec (backward compat)
	AudioTracks []AudioTrack `json:"audio_tracks,omitempty"` // all audio tracks
	Subtitles   []string     `json:"subtitles"`
	Width       int          `json:"width,omitempty"`
	Height      int          `json:"height,omitempty"`
}

type ValidationChecks struct {
	Integrity string     `json:"integrity"` // "pass", "fail", "pending"
	Duration  string     `json:"duration"`
	Sample    string     `json:"sample"`
	Codecs    *CodecInfo `json:"codecs,omitempty"`
}

type ValidationResult struct {
	Passed bool             `json:"passed"`
	Checks ValidationChecks `json:"checks"`
}

type Job struct {
	ID             string            `json:"id"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	State          JobState          `json:"state"`
	Stage          JobStage          `json:"stage"`
	Progress       float64           `json:"progress"`
	Source         JobSource         `json:"source"`
	Validation     *ValidationResult `json:"validation,omitempty"`
	MissingSubs    []string          `json:"missing_subs,omitempty"`
	SubsAcquired   []string          `json:"subs_acquired,omitempty"` // NEW: langs Bazarr has delivered
	Error          string            `json:"error,omitempty"`
	RetryCount     int               `json:"retry_count"`
	InterruptCount int               `json:"interrupt_count"`

	// ManualProfile, when non-empty, causes the pipeline to skip Validate and
	// CatalogEarly and run a targeted transcode using the named profile.
	// Set by the POST /api/procula/transcode endpoint.
	ManualProfile string `json:"manual_profile,omitempty"`

	// Dual-subtitle metadata (populated by GenerateDualSubs)
	DualSubOutputs []string `json:"dualsub_outputs,omitempty"`
	DualSubError   string   `json:"dualsub_error,omitempty"`

	// Transcoding metadata (populated by maybeTranscode / runManualTranscode)
	TranscodeProfile  string   `json:"transcode_profile,omitempty"`
	TranscodeDecision string   `json:"transcode_decision,omitempty"` // "transcoded", "passthrough", "failed"
	TranscodeOutputs  []string `json:"transcode_outputs,omitempty"`
	TranscodeError    string   `json:"transcode_error,omitempty"`
	TranscodeETA      float64  `json:"transcode_eta,omitempty"`

	// Action-bus discriminator and payload. ActionType="pipeline" runs the
	// legacy stage machine; anything else is dispatched via the action registry.
	ActionType string         `json:"action_type,omitempty"`
	Params     map[string]any `json:"params,omitempty"`
	Result     map[string]any `json:"result,omitempty"`

	// NextAttemptAt, when non-nil, defers re-execution until this UTC time.
	// NULL means the job is immediately eligible for pickup by the worker.
	NextAttemptAt *time.Time `json:"next_attempt_at,omitempty"`

	// Catalog tracks outcomes from the catalog stage.
	Catalog *CatalogInfo `json:"catalog,omitempty"`

	// Flags are derived issues (validation failures, missing subs, etc.)
	// computed by ComputeFlags after each pipeline stage. Empty means clean.
	Flags []Flag `json:"flags,omitempty"`
}

// CatalogInfo records what happened during the catalog stage of a pipeline job.
type CatalogInfo struct {
	JellyfinSynced   bool `json:"jellyfin_synced"`
	NotificationSent bool `json:"notification_sent"`
}

type Queue struct {
	db        *sql.DB
	pending   chan struct{}
	cancelsMu sync.Mutex
	cancels   map[string]context.CancelFunc
}

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

// loadExisting reads jobs from DB on startup. Jobs that were mid-processing
// when the process died are reset to queued and re-enqueued.
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

	// maxConsecutiveInterrupts is the number of consecutive process-kill
	// interruptions tolerated before the event is treated as a real failure
	// and promoted into retry_count.
	const maxConsecutiveInterrupts = 3
	// interruptBackoff is the short delay before re-queuing a job that was
	// killed mid-run (e.g. container restart). Much shorter than the transient
	// failure backoff because restarts are almost always transient infra events.
	const interruptBackoff = 30 * time.Second

	for _, r := range toProcess {
		if r.state == StateProcessing {
			// The job was mid-flight when the process died.
			var retryCount, interruptCount int
			err := q.db.QueryRow(`SELECT retry_count, interrupt_count FROM jobs WHERE id=?`, r.id).
				Scan(&retryCount, &interruptCount)
			if err != nil {
				slog.Warn("could not read counters for interrupted job", "component", "queue", "job_id", r.id, "error", err)
				continue
			}
			interruptCount++

			var next time.Time
			if interruptCount > maxConsecutiveInterrupts {
				// Consecutive-interrupt threshold exceeded: promote to a real retry.
				retryCount++
				interruptCount = 0
				if retryCount > maxTransientRetries {
					_, err = q.db.Exec(
						`UPDATE jobs SET state=?, error=?, retry_count=?, interrupt_count=0, next_attempt_at=NULL, updated_at=? WHERE id=?`,
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
				next = time.Now().UTC().Add(backoffDuration(retryCount))
				slog.Info("interrupt promoted to retry", "component", "queue", "job_id", r.id,
					"retry_count", retryCount, "next_attempt_at", next.Format(time.RFC3339))
			} else {
				next = time.Now().UTC().Add(interruptBackoff)
				slog.Info("interrupted job re-queued with short backoff", "component", "queue", "job_id", r.id,
					"interrupt_count", interruptCount, "next_attempt_at", next.Format(time.RFC3339))
			}

			_, err = q.db.Exec(
				`UPDATE jobs SET state=?, stage=?, progress=0, error=?, retry_count=?, interrupt_count=?, next_attempt_at=?, updated_at=? WHERE id=?`,
				string(StateQueued), string(StageValidate), "interrupted: restarted",
				retryCount, interruptCount, next.Format(time.RFC3339Nano),
				time.Now().UTC().Format(time.RFC3339Nano), r.id,
			)
			if err != nil {
				slog.Warn("could not reset interrupted job", "component", "queue", "job_id", r.id, "error", err)
				continue
			}
		}
		select {
		case q.pending <- struct{}{}:
		default:
		}
	}
	return nil
}

// insertJobRow persists a new Job to the database. The caller is responsible
// for populating all fields on j before calling. No signal is sent to q.pending.
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
		`INSERT INTO jobs (id, created_at, updated_at, state, stage, progress, source, error, retry_count, interrupt_count,
		                   manual_profile, dualsub_error, transcode_profile, transcode_decision, transcode_error, transcode_eta,
		                   action_type, params, result, flags, next_attempt_at)
		 VALUES (?, ?, ?, ?, ?, 0, ?, '', 0, 0, '', '', '', '', '', 0, ?, ?, NULL, NULL, ?)`,
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

func (q *Queue) Create(source JobSource) (*Job, error) {
	// Deduplicate: return existing job if the same path is already active.
	// json_extract avoids LIKE wildcards that break on paths containing % or _.
	var existingID string
	err := q.db.QueryRow(
		`SELECT id FROM jobs WHERE json_extract(source, '$.path') = ? AND state IN ('queued','processing') LIMIT 1`,
		source.Path,
	).Scan(&existingID)
	if err == nil {
		// Found existing job — load and return it
		job, ok := q.Get(existingID)
		if ok {
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

	select {
	case q.pending <- struct{}{}:
	default:
	}

	return job, nil
}

// createActionJob inserts a new action-bus job directly, bypassing the
// path-dedup logic that Create uses for pipeline jobs. Each action call
// always gets a fresh job row — dedup would corrupt in-flight pipeline jobs
// that happen to share the same source path.
func (q *Queue) createActionJob(source JobSource, actionType string, params map[string]any) (*Job, error) {
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

	select {
	case q.pending <- struct{}{}:
	default:
	}

	return job, nil
}

func (q *Queue) Get(id string) (*Job, bool) {
	row := q.db.QueryRow(
		`SELECT id, created_at, updated_at, state, stage, progress, source, validation, missing_subs,
		        subs_acquired,
		        error, retry_count, interrupt_count, manual_profile, dualsub_outputs, dualsub_error,
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

// ListFilter controls which jobs List returns.
// Zero values mean "no filter" (all states, no time bound, default limit).
type ListFilter struct {
	State      JobState  // empty = all states
	ActionType string    // empty = all action types
	Limit      int       // 0 = default 200; max 1000
	Since      time.Time // zero = no lower bound
}

// List returns jobs matching the filter, ordered by created_at DESC.
// Default limit is 200; maximum is 1000.
func (q *Queue) List(f ListFilter) []Job {
	limit := f.Limit
	switch {
	case limit <= 0:
		limit = 200
	case limit > 1000:
		limit = 1000
	}

	query := `SELECT id, created_at, updated_at, state, stage, progress, source, validation, missing_subs,
		        subs_acquired,
		        error, retry_count, interrupt_count, manual_profile, dualsub_outputs, dualsub_error,
		        transcode_profile, transcode_decision, transcode_outputs, transcode_error, transcode_eta,
		        action_type, params, result, catalog, flags, next_attempt_at
		 FROM jobs WHERE 1=1`
	var args []any

	if f.State != "" {
		query += ` AND state=?`
		args = append(args, string(f.State))
	}
	if f.ActionType != "" {
		query += ` AND action_type=?`
		args = append(args, f.ActionType)
	}
	if !f.Since.IsZero() {
		query += ` AND created_at >= ?`
		args = append(args, f.Since.UTC().Format(time.RFC3339Nano))
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := q.db.Query(query, args...)
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

// ArchiveOldJobs deletes terminal jobs (completed, failed, cancelled) older than
// olderThan. Returns the number of rows deleted.
func (q *Queue) ArchiveOldJobs(olderThan time.Duration) (int, error) {
	cutoff := time.Now().UTC().Add(-olderThan).Format(time.RFC3339Nano)
	res, err := q.db.Exec(
		`DELETE FROM jobs WHERE state IN ('completed','failed','cancelled') AND created_at < ?`,
		cutoff,
	)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

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
			error=?, retry_count=?, interrupt_count=?, manual_profile=?, dualsub_outputs=?, dualsub_error=?,
			transcode_profile=?, transcode_decision=?, transcode_outputs=?, transcode_error=?, transcode_eta=?,
			action_type=?, params=?, result=?, catalog=?, flags=?, next_attempt_at=?
		 WHERE id=?`,
		job.UpdatedAt.Format(time.RFC3339Nano),
		string(job.State), string(job.Stage), job.Progress,
		string(sourceJSON), validationJSON, missingSubsJSON,
		subsAcquiredJSON,
		job.Error, job.RetryCount, job.InterruptCount, job.ManualProfile, dualSubOutputsJSON, job.DualSubError,
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
	select {
	case q.pending <- struct{}{}:
	default:
	}
	return nil
}

func (q *Queue) Cancel(id string) error {
	err := q.Update(id, func(j *Job) {
		if j.State == StateQueued || j.State == StateProcessing {
			j.State = StateCancelled
		}
	})
	if err != nil {
		return err
	}
	// Kill any running FFmpeg process for this job
	q.cancelsMu.Lock()
	if fn, ok := q.cancels[id]; ok {
		fn()
		delete(q.cancels, id)
	}
	q.cancelsMu.Unlock()
	return nil
}

// Wait polls the queue for a terminal state on the given job ID.
// Returns the final job snapshot when state transitions to completed, failed,
// or cancelled. Returns an error on timeout or if the job ID is unknown.
// Caller should cap timeout at ~10 seconds; this is intended for synchronous
// action calls with ?wait=N.
func (q *Queue) Wait(id string, timeout time.Duration) (*Job, error) {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	if job, ok := q.Get(id); ok {
		if isTerminal(job.State) {
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
		if isTerminal(job.State) {
			return job, nil
		}
		if time.Now().After(deadline) {
			return job, fmt.Errorf("timeout after %s waiting for job %s", timeout, id)
		}
	}
}

func isTerminal(s JobState) bool {
	return s == StateCompleted || s == StateFailed || s == StateCancelled
}

// registerCancel stores a context cancel func for a running job.
// The caller must call unregisterCancel (or defer it) when the job finishes.
func (q *Queue) registerCancel(id string, fn context.CancelFunc) {
	q.cancelsMu.Lock()
	q.cancels[id] = fn
	q.cancelsMu.Unlock()
}

func (q *Queue) unregisterCancel(id string) {
	q.cancelsMu.Lock()
	delete(q.cancels, id)
	q.cancelsMu.Unlock()
}

// nextQueued returns up to 64 job IDs in queued state that are eligible for
// immediate execution (next_attempt_at is NULL or in the past), oldest first.
func (q *Queue) nextQueued() []string {
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

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// scanJob reads one job row from a scanner (either *sql.Row or *sql.Rows).
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
		&job.Error, &job.RetryCount, &job.InterruptCount, &job.ManualProfile,
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

// randStr returns a hex-encoded random string using crypto/rand.
// n is the number of random bytes (output length is 2*n hex chars).
// Uses at least 8 bytes (16 hex chars) regardless of the requested n.
func randStr(n int) string {
	if n < 8 {
		n = 8
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is extremely unlikely; fall back to a timestamp-derived suffix.
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
