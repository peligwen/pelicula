package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
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
	StageValidate JobStage = "validate"
	StageProcess  JobStage = "process"
	StageCatalog  JobStage = "catalog"
	StageDone     JobStage = "done"
)

type JobSource struct {
	Type                   string `json:"type"`      // "movie" or "episode"
	Title                  string `json:"title"`
	Year                   int    `json:"year"`
	Path                   string `json:"path"`
	Size                   int64  `json:"size"`
	ArrID                  int    `json:"arr_id"`
	ArrType                string `json:"arr_type"` // "radarr" or "sonarr"
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
	ID         string            `json:"id"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
	State      JobState          `json:"state"`
	Stage      JobStage          `json:"stage"`
	Progress   float64           `json:"progress"`
	Source     JobSource         `json:"source"`
	Validation *ValidationResult `json:"validation,omitempty"`
	Error      string            `json:"error,omitempty"`
	RetryCount int               `json:"retry_count"`
}

type Queue struct {
	configDir string
	mu        sync.RWMutex
	jobs      map[string]*Job
	pending   chan string
	cancelsMu sync.Mutex
	cancels   map[string]context.CancelFunc
}

func NewQueue(configDir string) (*Queue, error) {
	jobsDir := filepath.Join(configDir, "jobs")
	if err := os.MkdirAll(jobsDir, 0755); err != nil {
		return nil, fmt.Errorf("create jobs dir: %w", err)
	}

	q := &Queue{
		configDir: configDir,
		jobs:      make(map[string]*Job),
		pending:   make(chan string, 256),
		cancels:   make(map[string]context.CancelFunc),
	}

	if err := q.loadExisting(); err != nil {
		return nil, err
	}

	return q, nil
}

// loadExisting reads job files from disk on startup. Jobs that were
// mid-processing when the process died are re-queued.
func (q *Queue) loadExisting() error {
	jobsDir := filepath.Join(q.configDir, "jobs")
	entries, err := os.ReadDir(jobsDir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(jobsDir, entry.Name()))
		if err != nil {
			continue
		}
		var job Job
		if err := json.Unmarshal(data, &job); err != nil {
			continue
		}
		// Re-queue jobs that were processing when we died
		if job.State == StateProcessing {
			job.State = StateQueued
			job.Stage = StageValidate
			job.Progress = 0
			job.Error = "interrupted: restarted"
		}
		q.jobs[job.ID] = &job
		if job.State == StateQueued {
			select {
			case q.pending <- job.ID:
			default:
				log.Printf("[queue] warning: pending channel full, job %s will not run until retried or restarted", job.ID)
			}
		}
	}
	return nil
}

func (q *Queue) Create(source JobSource) (*Job, error) {
	// Deduplicate: return existing job if the same path is already active
	q.mu.RLock()
	for _, j := range q.jobs {
		if j.Source.Path == source.Path && (j.State == StateQueued || j.State == StateProcessing) {
			cp := *j
			q.mu.RUnlock()
			log.Printf("[queue] duplicate job for %s, returning existing %s", source.Path, cp.ID)
			return &cp, nil
		}
	}
	q.mu.RUnlock()

	id := fmt.Sprintf("job_%d_%s", time.Now().UnixMilli(), randStr(6))
	now := time.Now().UTC()
	job := &Job{
		ID:        id,
		CreatedAt: now,
		UpdatedAt: now,
		State:     StateQueued,
		Stage:     StageValidate,
		Source:    source,
	}

	q.mu.Lock()
	q.jobs[id] = job
	q.mu.Unlock()

	if err := q.persist(job); err != nil {
		return nil, err
	}

	select {
	case q.pending <- id:
	default:
		log.Printf("[queue] warning: pending channel full, job %s will not run until retried or restarted", id)
	}

	return job, nil
}

func (q *Queue) Get(id string) (*Job, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	j, ok := q.jobs[id]
	if !ok {
		return nil, false
	}
	cp := *j
	return &cp, true
}

func (q *Queue) List() []Job {
	q.mu.RLock()
	defer q.mu.RUnlock()
	out := make([]Job, 0, len(q.jobs))
	for _, j := range q.jobs {
		out = append(out, *j)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func (q *Queue) Update(id string, fn func(*Job)) error {
	q.mu.Lock()
	job, ok := q.jobs[id]
	if !ok {
		q.mu.Unlock()
		return fmt.Errorf("job %s not found", id)
	}
	fn(job)
	job.UpdatedAt = time.Now().UTC()
	cp := *job
	q.mu.Unlock()
	if err := q.persist(&cp); err != nil {
		log.Printf("[queue] warning: failed to persist job %s: %v", id, err)
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
		log.Printf("[queue] warning: pending channel full, retry of job %s will not run until restart", id)
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
	q.mu.RLock()
	defer q.mu.RUnlock()
	counts := map[string]int{
		"queued":     0,
		"processing": 0,
		"completed":  0,
		"failed":     0,
		"cancelled":  0,
	}
	for _, j := range q.jobs {
		counts[string(j.State)]++
	}
	return counts
}

func (q *Queue) persist(job *Job) error {
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	final := filepath.Join(q.configDir, "jobs", job.ID+".json")
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

const randChars = "abcdefghijklmnopqrstuvwxyz0123456789"

func randStr(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = randChars[rand.Intn(len(randChars))]
	}
	return string(b)
}
