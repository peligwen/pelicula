package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
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
	StageAwaitSubs JobStage = "await_subs"  // NEW: inserted between catalog and dualsub
	StageDualSub   JobStage = "dualsub"
	StageProcess   JobStage = "process"
	StageDone      JobStage = "done"
)

type JobSource struct {
	Type                   string `json:"type"`      // "movie" or "episode"
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

type CodecInfo struct {
	Video     string   `json:"video"`
	Audio     string   `json:"audio"`
	Subtitles []string `json:"subtitles"`
	Width     int      `json:"width,omitempty"`
	Height    int      `json:"height,omitempty"`
}

type ValidationChecks struct {
	Integrity string     `json:"integrity"`        // "pass", "fail", "pending"
	Duration  string     `json:"duration"`
	Sample    string     `json:"sample"`
	Codecs    *CodecInfo `json:"codecs,omitempty"`
}

type ValidationResult struct {
	Passed bool             `json:"passed"`
	Checks ValidationChecks `json:"checks"`
}

type Job struct {
	ID          string            `json:"id"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	State       JobState          `json:"state"`
	Stage       JobStage          `json:"stage"`
	Progress    float64           `json:"progress"`
	Source      JobSource         `json:"source"`
	Validation  *ValidationResult `json:"validation,omitempty"`
	MissingSubs  []string          `json:"missing_subs,omitempty"`
	SubsAcquired []string          `json:"subs_acquired,omitempty"`  // NEW: langs Bazarr has delivered
	Error        string            `json:"error,omitempty"`
	RetryCount  int               `json:"retry_count"`

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
}

type Queue struct {
	db        *sql.DB
	pending   chan string
	cancelsMu sync.Mutex
	cancels   map[string]context.CancelFunc
}

func NewQueue(db *sql.DB) (*Queue, error) {
	q := &Queue{
		db:      db,
		pending: make(chan string, 256),
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

	for _, r := range toProcess {
		if r.state == StateProcessing {
			// Reset interrupted jobs back to queued
			_, err := q.db.Exec(
				`UPDATE jobs SET state=?, stage=?, progress=0, error=?, updated_at=? WHERE id=?`,
				string(StateQueued), string(StageValidate), "interrupted: restarted",
				time.Now().UTC().Format(time.RFC3339Nano), r.id,
			)
			if err != nil {
				slog.Warn("could not reset interrupted job", "component", "queue", "job_id", r.id, "error", err)
				continue
			}
		}
		select {
		case q.pending <- r.id:
		default:
			slog.Warn("pending channel full, job will not run until retried or restarted", "component", "queue", "job_id", r.id)
		}
	}
	return nil
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

	id := fmt.Sprintf("job_%d_%s", time.Now().UnixMilli(), randStr(6))
	now := time.Now().UTC()

	sourceJSON, err := json.Marshal(source)
	if err != nil {
		return nil, fmt.Errorf("marshal source: %w", err)
	}

	_, err = q.db.Exec(
		`INSERT INTO jobs (id, created_at, updated_at, state, stage, progress, source, error, retry_count,
		                   manual_profile, dualsub_error, transcode_profile, transcode_decision, transcode_error, transcode_eta,
		                   action_type, params, result)
		 VALUES (?, ?, ?, ?, ?, 0, ?, '', 0, '', '', '', '', '', 0, 'pipeline', NULL, NULL)`,
		id,
		now.Format(time.RFC3339Nano),
		now.Format(time.RFC3339Nano),
		string(StateQueued),
		string(StageValidate),
		string(sourceJSON),
	)
	if err != nil {
		return nil, fmt.Errorf("insert job: %w", err)
	}

	job := &Job{
		ID:         id,
		CreatedAt:  now,
		UpdatedAt:  now,
		State:      StateQueued,
		Stage:      StageValidate,
		Source:     source,
		ActionType: "pipeline",
	}

	select {
	case q.pending <- id:
	default:
		slog.Warn("pending channel full, job will not run until retried or restarted", "component", "queue", "job_id", id)
	}

	return job, nil
}

func (q *Queue) Get(id string) (*Job, bool) {
	row := q.db.QueryRow(
		`SELECT id, created_at, updated_at, state, stage, progress, source, validation, missing_subs,
		        subs_acquired,
		        error, retry_count, manual_profile, dualsub_outputs, dualsub_error,
		        transcode_profile, transcode_decision, transcode_outputs, transcode_error, transcode_eta,
		        action_type, params, result
		 FROM jobs WHERE id=?`, id,
	)
	job, err := scanJob(row)
	if err != nil {
		return nil, false
	}
	return job, true
}

func (q *Queue) List() []Job {
	rows, err := q.db.Query(
		`SELECT id, created_at, updated_at, state, stage, progress, source, validation, missing_subs,
		        subs_acquired,
		        error, retry_count, manual_profile, dualsub_outputs, dualsub_error,
		        transcode_profile, transcode_decision, transcode_outputs, transcode_error, transcode_eta,
		        action_type, params, result
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

	_, err := q.db.Exec(
		`UPDATE jobs SET
			updated_at=?, state=?, stage=?, progress=?, source=?, validation=?, missing_subs=?,
			subs_acquired=?,
			error=?, retry_count=?, manual_profile=?, dualsub_outputs=?, dualsub_error=?,
			transcode_profile=?, transcode_decision=?, transcode_outputs=?, transcode_error=?, transcode_eta=?,
			action_type=?, params=?, result=?
		 WHERE id=?`,
		job.UpdatedAt.Format(time.RFC3339Nano),
		string(job.State), string(job.Stage), job.Progress,
		string(sourceJSON), validationJSON, missingSubsJSON,
		subsAcquiredJSON,
		job.Error, job.RetryCount, job.ManualProfile, dualSubOutputsJSON, job.DualSubError,
		job.TranscodeProfile, job.TranscodeDecision, transcodeOutputsJSON, job.TranscodeError, job.TranscodeETA,
		job.ActionType, paramsJSON, resultJSON,
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
	case q.pending <- id:
	default:
		slog.Warn("pending channel full, retry will not run until restart", "component", "queue", "job_id", id)
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
		&actionType, &paramsJSON, &resultJSON,
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

	return &job, nil
}

const randChars = "abcdefghijklmnopqrstuvwxyz0123456789"

func randStr(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = randChars[rand.Intn(len(randChars))]
	}
	return string(b)
}
