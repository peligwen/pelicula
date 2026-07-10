# Catalog + Action Bus Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Catalog dashboard tab listing library movies and TV shows with a dynamic per-item action menu backed by a central action bus in procula's job queue.

**Architecture:** Procula's `jobs` table gains an `action_type` discriminator plus JSON `params`/`result` columns; the worker loop branches between the legacy `pipeline` stage machine and a new action registry (`procula/actions.go`) that dispatches `validate`, `transcode`, and `subtitle_refresh` handlers. The middleware exposes thin proxies over live Radarr/Sonarr (catalog list, series detail, season episodes) and forwards action requests to procula. The frontend renders a Catalog tab with a client-side expand-on-demand TV tree and a context menu hydrated from `GET /actions/registry`; series/season-level fan-outs are client loops with a stop button. The Pipeline tab scopes its query with `?action_type=pipeline` so non-pipeline actions never appear there.

**Tech Stack:** Go (procula + middleware, modernc.org/sqlite), Vanilla JS/CSS (dashboard)

**Frontend safety note:** The dashboard renders dynamic rows by composing template strings and assigning to `.innerHTML`, matching the prevailing pattern in `nginx/dashboard.js`. Every value interpolated into those templates MUST go through the existing `esc()` helper (HTML-entity escaping). The context menu is also populated via template strings but the values come from the action registry (server-controlled). Do not accept arbitrary user HTML. If a new field needs to be rendered, add `esc()` around it.

---

## Task 1 — procula: schema migration for action_type/params/result

- [ ] Add failing test: create `procula/db_test.go` test (or add to existing) that opens a fresh DB and asserts columns `action_type`, `params`, `result` exist on the `jobs` table after migration, and that `PRAGMA user_version` is at least 3.

Add to `procula/db_test.go`:

```go
func TestMigrate3AddsActionColumns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	var ver int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if ver < 3 {
		t.Fatalf("user_version = %d, want >= 3", ver)
	}

	rows, err := db.Query(`PRAGMA table_info(jobs)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()

	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		cols[name] = true
	}
	for _, want := range []string{"action_type", "params", "result"} {
		if !cols[want] {
			t.Errorf("missing column %q", want)
		}
	}
}
```

You will need `"database/sql"`, `"path/filepath"`, `"testing"` imports.

- [ ] Run: `go test ./procula/ -run TestMigrate3AddsActionColumns -v` — verify it fails because migration 3 does not exist yet.
- [ ] Edit `procula/db.go`: append `{version: 3, up: migrate3}` to the `migrations` slice, and add the migration function below `migrate2`:

```go
// migrate3 adds action-bus discriminator columns to the jobs table.
// action_type defaults to 'pipeline' so legacy rows continue to route
// through the stage machine.
func migrate3(tx *sql.Tx) error {
	stmts := []string{
		`ALTER TABLE jobs ADD COLUMN action_type TEXT NOT NULL DEFAULT 'pipeline'`,
		`ALTER TABLE jobs ADD COLUMN params TEXT`,
		`ALTER TABLE jobs ADD COLUMN result TEXT`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] Run: `go test ./procula/ -run TestMigrate3AddsActionColumns -v` — verify it passes.
- [ ] Run full procula package tests: `go test ./procula/... -v` to confirm nothing regressed.
- [ ] Commit: `feat(procula): add migration 3 for action_type/params/result columns`

---

## Task 2 — procula: Job struct action fields + Create/Update/scanJob wiring

- [ ] Add failing test to `procula/queue_test.go`:

```go
func TestQueueCreateWithActionType(t *testing.T) {
	q := newTestQueue(t)

	job, err := q.Create(JobSource{Path: "/movies/Foo (2024)/foo.mkv", ArrType: "radarr", Type: "movie"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if job.ActionType != "pipeline" {
		t.Errorf("ActionType default = %q, want %q", job.ActionType, "pipeline")
	}

	err = q.Update(job.ID, func(j *Job) {
		j.ActionType = "validate"
		j.Params = map[string]any{"path": "/movies/Foo (2024)/foo.mkv"}
		j.Result = map[string]any{"passed": true}
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, ok := q.Get(job.ID)
	if !ok {
		t.Fatal("Get: not found")
	}
	if got.ActionType != "validate" {
		t.Errorf("ActionType = %q, want %q", got.ActionType, "validate")
	}
	if got.Params["path"] != "/movies/Foo (2024)/foo.mkv" {
		t.Errorf("Params[path] = %v", got.Params["path"])
	}
	if got.Result["passed"] != true {
		t.Errorf("Result[passed] = %v", got.Result["passed"])
	}
}
```

- [ ] Run: `go test ./procula/ -run TestQueueCreateWithActionType -v` — verify it fails (Job has no ActionType/Params/Result).
- [ ] Edit `procula/queue.go` — add fields to `Job` struct (after `TranscodeETA`):

```go
	// Action-bus discriminator and payload. ActionType="pipeline" runs the
	// legacy stage machine; anything else is dispatched via the action registry.
	ActionType string         `json:"action_type,omitempty"`
	Params     map[string]any `json:"params,omitempty"`
	Result     map[string]any `json:"result,omitempty"`
```

- [ ] Edit `procula/queue.go` `Create` — change the `INSERT` statement to include the new columns and default them:

```go
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
```

And set the struct default so callers see it:

```go
	job := &Job{
		ID:         id,
		CreatedAt:  now,
		UpdatedAt:  now,
		State:      StateQueued,
		Stage:      StageValidate,
		Source:     source,
		ActionType: "pipeline",
	}
```

- [ ] Edit `procula/queue.go` `Get` and `List` queries — append the three new columns to the SELECT lists (same order each time):

```go
`SELECT id, created_at, updated_at, state, stage, progress, source, validation, missing_subs,
        subs_acquired,
        error, retry_count, manual_profile, dualsub_outputs, dualsub_error,
        transcode_profile, transcode_decision, transcode_outputs, transcode_error, transcode_eta,
        action_type, params, result
 FROM jobs WHERE id=?`
```

Do the same for the `List()` query.

- [ ] Edit `procula/queue.go` `Update` — add params/result/action_type to the UPDATE statement:

```go
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
```

- [ ] Edit `procula/queue.go` `scanJob` — append the three columns to the Scan and decode JSON columns:

```go
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
	job.ActionType = actionType
	if job.ActionType == "" {
		job.ActionType = "pipeline"
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
	if paramsJSON != nil {
		json.Unmarshal([]byte(*paramsJSON), &job.Params) //nolint:errcheck
	}
	if resultJSON != nil {
		json.Unmarshal([]byte(*resultJSON), &job.Result) //nolint:errcheck
	}

	return &job, nil
}
```

- [ ] Run: `go test ./procula/ -run TestQueueCreateWithActionType -v` — verify it passes.
- [ ] Run: `go test ./procula/... -v` to confirm nothing regressed.
- [ ] Commit: `feat(procula): add ActionType/Params/Result to Job struct and queue I/O`

---

## Task 3 — procula: Queue.Wait(id, timeout) poll loop

- [ ] Add failing test to `procula/queue_test.go`:

```go
func TestQueueWaitReturnsOnTerminal(t *testing.T) {
	q := newTestQueue(t)
	job, err := q.Create(testSource("/movies/foo.mkv"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = q.Update(job.ID, func(j *Job) { j.State = StateCompleted })
	}()

	got, err := q.Wait(job.ID, 2*time.Second)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if got.State != StateCompleted {
		t.Errorf("State = %q, want %q", got.State, StateCompleted)
	}
}

func TestQueueWaitTimesOut(t *testing.T) {
	q := newTestQueue(t)
	job, err := q.Create(testSource("/movies/bar.mkv"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err = q.Wait(job.ID, 150*time.Millisecond)
	if err == nil {
		t.Fatal("Wait: expected timeout error, got nil")
	}
}

func TestQueueWaitNotFound(t *testing.T) {
	q := newTestQueue(t)
	_, err := q.Wait("nope", 100*time.Millisecond)
	if err == nil {
		t.Fatal("Wait: expected not-found error, got nil")
	}
}
```

- [ ] Run: `go test ./procula/ -run TestQueueWait -v` — verify it fails (no Wait method).
- [ ] Edit `procula/queue.go` — add the `Wait` method after `Cancel`:

```go
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
```

- [ ] Run: `go test ./procula/ -run TestQueueWait -v` — verify all three cases pass.
- [ ] Commit: `feat(procula): add Queue.Wait for synchronous action calls`

---

## Task 4 — procula: action registry + ActionDef types

- [ ] Create failing test `procula/actions_test.go`:

```go
package main

import (
	"context"
	"testing"
)

func TestRegisterAndLookupAction(t *testing.T) {
	actionRegistry = map[string]*ActionDef{}

	def := &ActionDef{
		Name:        "test_noop",
		Label:       "Test No-op",
		AppliesTo:   []string{"movie", "episode"},
		Sync:        true,
		Description: "unit test",
		Handler: func(ctx context.Context, q *Queue, job *Job) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		},
	}
	Register(def)

	got := Lookup("test_noop")
	if got == nil {
		t.Fatal("Lookup returned nil")
	}
	if got.Label != "Test No-op" {
		t.Errorf("Label = %q", got.Label)
	}

	all := List()
	if len(all) == 0 {
		t.Fatal("List returned empty")
	}
}

func TestLookupUnknownAction(t *testing.T) {
	actionRegistry = map[string]*ActionDef{}
	if Lookup("nope") != nil {
		t.Error("Lookup(nope) should be nil")
	}
}
```

- [ ] Run: `go test ./procula/ -run TestRegisterAndLookupAction -v` — verify it fails (no actions.go).
- [ ] Create `procula/actions.go`:

```go
// actions.go — central action registry for procula's action bus.
//
// Actions are discrete operations on library items (validate, transcode,
// subtitle_refresh). Each action is registered with a Handler that runs inside
// the worker loop when a job's ActionType != "pipeline".
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ActionTarget identifies a library item an action applies to.
// Not all fields are required by every action — validate only needs Path,
// subtitle_refresh needs ArrType+ArrID (+EpisodeID for TV).
type ActionTarget struct {
	Path      string `json:"path,omitempty"`
	ArrType   string `json:"arr_type,omitempty"`
	ArrID     int    `json:"arr_id,omitempty"`
	EpisodeID int    `json:"episode_id,omitempty"`
}

// ActionRequest is the shape POSTed to /api/procula/actions.
type ActionRequest struct {
	Action string         `json:"action"`
	Target ActionTarget   `json:"target"`
	Params map[string]any `json:"params,omitempty"`
}

// ActionResult is returned inline when ?wait=N is used. Otherwise the
// caller receives the Job envelope and polls.
type ActionResult struct {
	JobID  string         `json:"job_id"`
	State  string         `json:"state"`
	Error  string         `json:"error,omitempty"`
	Result map[string]any `json:"result,omitempty"`
}

// ActionHandler runs in the worker goroutine. It must be idempotent.
// The handler should update job state via q.Update when it needs to
// persist progress; the worker marks the job completed/failed afterward
// based on the returned error.
type ActionHandler func(ctx context.Context, q *Queue, job *Job) (map[string]any, error)

// ActionDef describes one registered action. Stable JSON shape — the
// dashboard hydrates its context menus from /api/procula/actions/registry.
type ActionDef struct {
	Name        string        `json:"name"`
	Label       string        `json:"label"`
	AppliesTo   []string      `json:"applies_to"`            // "movie", "episode", "season", "series"
	Sync        bool          `json:"sync"`                  // true if worker completes fast enough for inline ?wait
	Description string        `json:"description,omitempty"`
	Handler     ActionHandler `json:"-"`
}

var (
	actionRegistryMu sync.RWMutex
	actionRegistry   = map[string]*ActionDef{}
)

// Register adds an action to the registry. Safe to call from init().
func Register(def *ActionDef) {
	actionRegistryMu.Lock()
	defer actionRegistryMu.Unlock()
	actionRegistry[def.Name] = def
}

// Lookup returns the ActionDef for name, or nil if unknown.
func Lookup(name string) *ActionDef {
	actionRegistryMu.RLock()
	defer actionRegistryMu.RUnlock()
	return actionRegistry[name]
}

// List returns all registered actions sorted by name — stable for tests
// and for the registry JSON endpoint.
func List() []*ActionDef {
	actionRegistryMu.RLock()
	defer actionRegistryMu.RUnlock()
	out := make([]*ActionDef, 0, len(actionRegistry))
	for _, d := range actionRegistry {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Placeholder: Task 5 appends registerBuiltinActions and the handler
// implementations into this file. The imports above ("fmt", "os",
// "path/filepath", "strings") are included now so the file compiles after
// Task 5 — remove any that are still unused after Task 4 compiles.
var _ = fmt.Sprintf
var _ = os.Stat
var _ = filepath.Base
var _ = strings.HasPrefix
```

(The `var _ = ...` lines are temporary and Task 5 removes them when it adds the real code that uses those imports. Alternative: omit those imports in Task 4 and add them in Task 5. The executor may choose whichever keeps the file compiling.)

- [ ] Run: `go test ./procula/ -run TestRegisterAndLookupAction -v` — verify it passes.
- [ ] Commit: `feat(procula): add action registry and ActionDef types`

---

## Task 5 — procula: three v1 action handlers (validate, transcode, subtitle_refresh)

- [ ] Add failing test cases to `procula/actions_test.go`:

```go
func TestValidateActionHandler(t *testing.T) {
	actionRegistry = map[string]*ActionDef{}
	registerBuiltinActions()

	def := Lookup("validate")
	if def == nil {
		t.Fatal("validate not registered")
	}
	if !def.Sync {
		t.Error("validate should be sync")
	}
	wantMovie, wantEp := false, false
	for _, a := range def.AppliesTo {
		if a == "movie" {
			wantMovie = true
		}
		if a == "episode" {
			wantEp = true
		}
	}
	if !wantMovie || !wantEp {
		t.Errorf("AppliesTo = %v", def.AppliesTo)
	}
}

func TestTranscodeActionDetectsTVFromPath(t *testing.T) {
	if got := arrTypeFromPath("/tv/Show/S01/ep.mkv"); got != "sonarr" {
		t.Errorf("arrTypeFromPath(/tv/...) = %q, want sonarr", got)
	}
	if got := arrTypeFromPath("/movies/Foo/foo.mkv"); got != "radarr" {
		t.Errorf("arrTypeFromPath(/movies/...) = %q, want radarr", got)
	}
	if got := mediaTypeFromPath("/tv/Show/ep.mkv"); got != "episode" {
		t.Errorf("mediaTypeFromPath(/tv/...) = %q, want episode", got)
	}
	if got := mediaTypeFromPath("/movies/foo.mkv"); got != "movie" {
		t.Errorf("mediaTypeFromPath(/movies/...) = %q, want movie", got)
	}
}

func TestSubtitleRefreshRegistered(t *testing.T) {
	actionRegistry = map[string]*ActionDef{}
	registerBuiltinActions()
	if Lookup("subtitle_refresh") == nil {
		t.Fatal("subtitle_refresh not registered")
	}
}
```

- [ ] Run: `go test ./procula/ -run TestValidateActionHandler -v` — verify it fails (no registerBuiltinActions).
- [ ] Remove the temporary `var _ = ...` lines from `procula/actions.go` (if added in Task 4) and append the handler implementations:

```go
// registerBuiltinActions wires the v1 action handlers. Called from main().
func registerBuiltinActions() {
	Register(&ActionDef{
		Name:        "validate",
		Label:       "Re-verify file",
		AppliesTo:   []string{"movie", "episode"},
		Sync:        true,
		Description: "Run ffprobe integrity/duration/sample checks on the source file.",
		Handler:     runValidateAction,
	})
	Register(&ActionDef{
		Name:        "transcode",
		Label:       "Re-transcode",
		AppliesTo:   []string{"movie", "episode"},
		Sync:        false,
		Description: "Run the current transcoding profile against the file.",
		Handler:     runTranscodeAction,
	})
	Register(&ActionDef{
		Name:        "subtitle_refresh",
		Label:       "Refresh subtitles",
		AppliesTo:   []string{"movie", "episode"},
		Sync:        true,
		Description: "Ask Bazarr to re-search subtitles for this item.",
		Handler:     runSubtitleRefreshAction,
	})
}

func arrTypeFromPath(p string) string {
	if strings.HasPrefix(p, "/tv/") || p == "/tv" {
		return "sonarr"
	}
	return "radarr"
}

func mediaTypeFromPath(p string) string {
	if strings.HasPrefix(p, "/tv/") || p == "/tv" {
		return "episode"
	}
	return "movie"
}

// runValidateAction builds a synthetic Job and calls Validate. Validate()
// in validate.go only reads job.Source.Path and job.Source.ExpectedRuntimeMinutes
// so a minimal shell is sufficient — no helper extraction needed.
func runValidateAction(ctx context.Context, q *Queue, job *Job) (map[string]any, error) {
	path, _ := job.Params["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("validate: path required")
	}
	syntheticJob := &Job{Source: JobSource{Path: path}}
	result, failReason := Validate(syntheticJob)
	out := map[string]any{
		"passed":      result.Passed,
		"checks":      result.Checks,
		"fail_reason": failReason,
	}
	return out, nil
}

// runTranscodeAction runs the manual transcode pipeline against a library
// file using the profile from Params. Unlike runValidateAction this runs
// long — callers should not use ?wait.
//
// Fixes the latent bug from handleManualTranscode by detecting arr_type
// and media type from the path prefix.
func runTranscodeAction(ctx context.Context, q *Queue, job *Job) (map[string]any, error) {
	path, _ := job.Params["path"].(string)
	profile, _ := job.Params["profile"].(string)
	if path == "" || profile == "" {
		return nil, fmt.Errorf("transcode: path and profile required")
	}

	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}
	arrType := arrTypeFromPath(path)
	mediaType := mediaTypeFromPath(path)
	title := strings.TrimSuffix(fi.Name(), filepath.Ext(fi.Name()))
	if parent := filepath.Base(filepath.Dir(path)); parent != "movies" && parent != "tv" {
		title = parent
	}

	err = q.Update(job.ID, func(j *Job) {
		j.Source = JobSource{Path: path, Size: fi.Size(), Title: title, ArrType: arrType, Type: mediaType}
		j.ManualProfile = profile
	})
	if err != nil {
		return nil, err
	}
	runManualTranscode(ctx, q, job.ID, configDir, env("PELICULA_API_URL", "http://pelicula-api:8181"))

	fresh, _ := q.Get(job.ID)
	return map[string]any{
		"decision": fresh.TranscodeDecision,
		"outputs":  fresh.TranscodeOutputs,
		"profile":  fresh.TranscodeProfile,
		"error":    fresh.TranscodeError,
	}, nil
}

// runSubtitleRefreshAction calls Bazarr directly using the target's arr IDs.
func runSubtitleRefreshAction(ctx context.Context, q *Queue, job *Job) (map[string]any, error) {
	arrType, _ := job.Params["arr_type"].(string)
	arrIDf, _ := job.Params["arr_id"].(float64)
	epIDf, _ := job.Params["episode_id"].(float64)
	if arrType == "" || arrIDf == 0 {
		return nil, fmt.Errorf("subtitle_refresh: arr_type and arr_id required")
	}
	synthetic := &Job{
		ID: "action-" + job.ID,
		Source: JobSource{
			ArrType:   arrType,
			ArrID:     int(arrIDf),
			EpisodeID: int(epIDf),
		},
	}
	bazarrSearchSubtitles(ctx, configDir, synthetic)
	return map[string]any{"triggered": true}, nil
}
```

- [ ] Run: `go test ./procula/ -run 'TestValidateActionHandler|TestTranscodeActionDetects|TestSubtitleRefreshRegistered' -v` — verify all pass.
- [ ] Commit: `feat(procula): implement validate/transcode/subtitle_refresh action handlers`

---

## Task 6 — procula: worker loop dispatch for non-pipeline action_type

- [ ] Add failing test to `procula/actions_test.go`:

```go
func TestWorkerDispatchesRegisteredAction(t *testing.T) {
	actionRegistry = map[string]*ActionDef{}
	Register(&ActionDef{
		Name: "noop_for_test",
		Sync: true,
		Handler: func(ctx context.Context, q *Queue, job *Job) (map[string]any, error) {
			return map[string]any{"hello": "world"}, nil
		},
	})
	q := newTestQueue(t)
	job, err := q.Create(JobSource{Path: "/movies/noop.mkv"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_ = q.Update(job.ID, func(j *Job) {
		j.ActionType = "noop_for_test"
		j.Params = map[string]any{"k": "v"}
	})
	processJob(q, job.ID, t.TempDir(), "http://localhost:0")

	got, _ := q.Get(job.ID)
	if got.State != StateCompleted {
		t.Errorf("State = %q, want %q; err=%q", got.State, StateCompleted, got.Error)
	}
	if got.Result["hello"] != "world" {
		t.Errorf("Result = %v", got.Result)
	}
}
```

- [ ] Run: `go test ./procula/ -run TestWorkerDispatchesRegisteredAction -v` — verify it fails.
- [ ] Edit `procula/pipeline.go` `processJob` — add an action-bus branch near the top, immediately after the cancelled-state short-circuit:

```go
	// Action bus: non-pipeline jobs dispatch through the registry instead of
	// running the stage machine.
	if job.ActionType != "" && job.ActionType != "pipeline" {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		q.registerCancel(id, cancel)
		defer q.unregisterCancel(id)
		runActionJob(ctx, q, job)
		return
	}
```

- [ ] Add `runActionJob` helper to `procula/pipeline.go` (bottom of file):

```go
// runActionJob dispatches a non-pipeline job to a registered action handler,
// writes the result JSON back to the job row, and marks the job
// completed/failed based on the handler's return.
func runActionJob(ctx context.Context, q *Queue, job *Job) {
	def := Lookup(job.ActionType)
	if def == nil {
		_ = q.Update(job.ID, func(j *Job) {
			j.State = StateFailed
			j.Error = "unknown action: " + j.ActionType
		})
		return
	}
	_ = q.Update(job.ID, func(j *Job) {
		j.State = StateProcessing
	})
	result, err := def.Handler(ctx, q, job)
	_ = q.Update(job.ID, func(j *Job) {
		if err != nil {
			j.State = StateFailed
			j.Error = err.Error()
			return
		}
		j.State = StateCompleted
		j.Progress = 1.0
		j.Result = result
	})
}
```

- [ ] Run: `go test ./procula/ -run TestWorkerDispatchesRegisteredAction -v` — verify it passes.
- [ ] Run: `go test ./procula/... -v` and confirm no regressions.
- [ ] Commit: `feat(procula): dispatch non-pipeline jobs through action registry`

---

## Task 7 — procula: POST /api/procula/actions endpoint

- [ ] Add failing test to `procula/actions_test.go`:

```go
func TestHandleCreateActionSync(t *testing.T) {
	actionRegistry = map[string]*ActionDef{}
	registerBuiltinActions()

	q := newTestQueue(t)
	srv := &Server{queue: q, db: q.db, configDir: t.TempDir()}

	// Need a background worker so the action actually runs.
	go RunWorker(q, t.TempDir(), "http://localhost:0")

	body := `{"action":"subtitle_refresh","target":{"arr_type":"radarr","arr_id":1}}`
	req := httptest.NewRequest(http.MethodPost, "/api/procula/actions?wait=3", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleCreateAction(w, req)

	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var resp ActionResult
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, w.Body.String())
	}
	if resp.JobID == "" {
		t.Error("JobID empty")
	}
}

func TestHandleCreateActionUnknown(t *testing.T) {
	actionRegistry = map[string]*ActionDef{}
	q := newTestQueue(t)
	srv := &Server{queue: q, db: q.db, configDir: t.TempDir()}

	body := `{"action":"bogus","target":{}}`
	req := httptest.NewRequest(http.MethodPost, "/api/procula/actions", strings.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleCreateAction(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}
```

Add the imports `"encoding/json"`, `"net/http"`, `"net/http/httptest"`, `"strings"` to `actions_test.go` as needed.

- [ ] Run: `go test ./procula/ -run TestHandleCreateAction -v` — verify it fails (no handler).
- [ ] Add `handleCreateAction` and `handleListActionRegistry` to `procula/main.go`:

```go
// handleCreateAction creates an action-bus job from an ActionRequest.
// When ?wait=N is set (max 10 seconds) the handler blocks until the job
// reaches a terminal state and returns the result inline.
func (s *Server) handleCreateAction(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req ActionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	def := Lookup(req.Action)
	if def == nil {
		writeError(w, "unknown action: "+req.Action, http.StatusBadRequest)
		return
	}

	params := map[string]any{}
	for k, v := range req.Params {
		params[k] = v
	}
	if req.Target.Path != "" {
		params["path"] = req.Target.Path
	}
	if req.Target.ArrType != "" {
		params["arr_type"] = req.Target.ArrType
	}
	if req.Target.ArrID != 0 {
		params["arr_id"] = float64(req.Target.ArrID)
	}
	if req.Target.EpisodeID != 0 {
		params["episode_id"] = float64(req.Target.EpisodeID)
	}

	source := JobSource{
		Path:    req.Target.Path,
		ArrType: req.Target.ArrType,
		ArrID:   req.Target.ArrID,
		Type:    mediaTypeFromPath(req.Target.Path),
		Title:   filepath.Base(req.Target.Path),
	}

	job, err := s.queue.Create(source)
	if err != nil {
		writeError(w, "create job: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.queue.Update(job.ID, func(j *Job) {
		j.ActionType = req.Action
		j.Params = params
	}); err != nil {
		writeError(w, "update job: "+err.Error(), http.StatusInternalServerError)
		return
	}
	select {
	case s.queue.pending <- job.ID:
	default:
	}

	waitSecs := 0
	if v := r.URL.Query().Get("wait"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			waitSecs = n
		}
	}
	if waitSecs > 10 {
		waitSecs = 10
	}
	if waitSecs > 0 {
		final, err := s.queue.Wait(job.ID, time.Duration(waitSecs)*time.Second)
		if err != nil && final == nil {
			writeError(w, err.Error(), http.StatusGatewayTimeout)
			return
		}
		res := ActionResult{JobID: final.ID, State: string(final.State), Error: final.Error, Result: final.Result}
		writeJSON(w, res)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, ActionResult{JobID: job.ID, State: string(StateQueued)})
}

// handleListActionRegistry returns all registered actions as JSON.
func (s *Server) handleListActionRegistry(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, List())
}
```

Add `"strconv"` and `"time"` imports if not already present in `procula/main.go`.

- [ ] Register the routes in `main()` (alongside the existing `/api/procula/transcode` registration around line 111):

```go
	mux.HandleFunc("POST /api/procula/actions", requireAPIKey(srv.handleCreateAction))
	mux.HandleFunc("GET /api/procula/actions/registry", srv.handleListActionRegistry)
```

- [ ] Call `registerBuiltinActions()` in `main()` before `go RunWorker(...)`.

- [ ] Run: `go test ./procula/ -run TestHandleCreateAction -v` — verify both pass.
- [ ] Run: `go test ./procula/... -v` to check nothing regressed.
- [ ] Commit: `feat(procula): add POST /actions and GET /actions/registry endpoints`

---

## Task 8 — procula: GET /api/procula/jobs?action_type= filter

- [ ] Add failing test to `procula/queue_test.go`:

```go
func TestQueueListByActionType(t *testing.T) {
	q := newTestQueue(t)
	a, _ := q.Create(testSource("/movies/a.mkv"))
	b, _ := q.Create(testSource("/movies/b.mkv"))
	_ = q.Update(b.ID, func(j *Job) { j.ActionType = "validate" })

	pipe := q.ListByActionType("pipeline")
	if len(pipe) != 1 || pipe[0].ID != a.ID {
		t.Errorf("pipeline filter: got %d jobs", len(pipe))
	}
	val := q.ListByActionType("validate")
	if len(val) != 1 || val[0].ID != b.ID {
		t.Errorf("validate filter: got %d jobs", len(val))
	}
}
```

- [ ] Run: `go test ./procula/ -run TestQueueListByActionType -v` — verify it fails.
- [ ] Edit `procula/queue.go` — add `ListByActionType`:

```go
// ListByActionType returns jobs filtered by action_type (e.g. "pipeline"
// or "validate"). Empty string means all action types.
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
```

- [ ] Edit `procula/main.go` `handleListJobs` to honor the filter:

```go
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	if at := r.URL.Query().Get("action_type"); at != "" {
		writeJSON(w, s.queue.ListByActionType(at))
		return
	}
	writeJSON(w, s.queue.List())
}
```

- [ ] Run: `go test ./procula/ -run TestQueueListByActionType -v` — verify it passes.
- [ ] Run full procula suite: `go test ./procula/... -v`.
- [ ] Commit: `feat(procula): add ?action_type filter to jobs list endpoint`

---

## Task 9 — middleware: catalog list handler (GET /api/pelicula/catalog)

- [ ] Create failing test `middleware/catalog_test.go`:

```go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleCatalogListFansOut(t *testing.T) {
	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/movie" {
			t.Errorf("radarr path = %q", r.URL.Path)
		}
		w.Write([]byte(`[{"id":1,"title":"Foo","year":2024},{"id":2,"title":"Bar","year":2023}]`))
	}))
	defer radarr.Close()

	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":10,"title":"Show","year":2020}]`))
	}))
	defer sonarr.Close()

	origR, origS := radarrURL, sonarrURL
	radarrURL, sonarrURL = radarr.URL, sonarr.URL
	services = &ServiceClients{RadarrKey: "k", SonarrKey: "k"}
	services.client = &http.Client{}
	t.Cleanup(func() { radarrURL, sonarrURL = origR, origS })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/catalog", nil)
	w := httptest.NewRecorder()
	handleCatalogList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Movies []map[string]any `json:"movies"`
		Series []map[string]any `json:"series"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Movies) != 2 {
		t.Errorf("movies = %d, want 2", len(resp.Movies))
	}
	if len(resp.Series) != 1 {
		t.Errorf("series = %d, want 1", len(resp.Series))
	}
}
```

Note: `ServiceClients` has unexported fields including `client`. If the test fails to assign `services.client` because the field is unexported from another file in the same package, that's still fine — this test lives in `package main` (middleware is a single package) so it can access unexported fields. If `ServiceClients.RadarrKey`/`SonarrKey` are not exported under those names, grep `middleware/services.go` and use the real field names.

- [ ] Run: `go test ./middleware/ -run TestHandleCatalogList -v` — verify it fails.
- [ ] Create `middleware/catalog.go`:

```go
// catalog.go — thin Radarr/Sonarr proxies powering the dashboard Catalog tab.
//
// The catalog is always read live from the *arr services — middleware holds
// no cache of its own. Handlers fan out concurrently where possible.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type catalogResponse struct {
	Movies []json.RawMessage `json:"movies"`
	Series []json.RawMessage `json:"series"`
}

// handleCatalogList returns movies and series from Radarr+Sonarr in parallel.
// Supports optional ?q= substring filter (case-insensitive on title) and
// ?type=movie|series to restrict the response.
func handleCatalogList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	typ := r.URL.Query().Get("type")

	sonarrKey, radarrKey, _ := services.Keys()

	type arrFetch struct {
		data []byte
		err  error
	}
	radarrCh := make(chan arrFetch, 1)
	sonarrCh := make(chan arrFetch, 1)

	go func() {
		if typ == "series" || radarrKey == "" {
			radarrCh <- arrFetch{}
			return
		}
		body, err := services.ArrGet(radarrURL, radarrKey, "/api/v3/movie")
		radarrCh <- arrFetch{data: body, err: err}
	}()
	go func() {
		if typ == "movie" || sonarrKey == "" {
			sonarrCh <- arrFetch{}
			return
		}
		body, err := services.ArrGet(sonarrURL, sonarrKey, "/api/v3/series")
		sonarrCh <- arrFetch{data: body, err: err}
	}()

	resp := catalogResponse{Movies: []json.RawMessage{}, Series: []json.RawMessage{}}
	if rf := <-radarrCh; rf.err == nil && len(rf.data) > 0 {
		resp.Movies = filterByTitle(rf.data, q)
	}
	if sf := <-sonarrCh; sf.err == nil && len(sf.data) > 0 {
		resp.Series = filterByTitle(sf.data, q)
	}
	writeJSON(w, resp)
}

// filterByTitle applies a case-insensitive substring filter to the "title"
// field of a JSON array. Returns an empty slice (never nil) for empty input.
func filterByTitle(data []byte, q string) []json.RawMessage {
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return []json.RawMessage{}
	}
	if q == "" {
		return items
	}
	out := make([]json.RawMessage, 0, len(items))
	for _, raw := range items {
		var probe struct {
			Title string `json:"title"`
		}
		if json.Unmarshal(raw, &probe) == nil {
			if strings.Contains(strings.ToLower(probe.Title), q) {
				out = append(out, raw)
			}
		}
	}
	return out
}

// handleCatalogSeriesDetail proxies Sonarr /api/v3/series/{id}.
func handleCatalogSeriesDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, "series id required", http.StatusBadRequest)
		return
	}
	sonarrKey, _, _ := services.Keys()
	body, err := services.ArrGet(sonarrURL, sonarrKey, "/api/v3/series/"+url.PathEscape(id))
	if err != nil {
		writeError(w, "sonarr unavailable", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(body) //nolint:errcheck
}

// handleCatalogSeason merges Sonarr episode and episodefile lists by
// episodeFileId so the frontend gets a single flat array of episode records
// with their file paths attached.
func handleCatalogSeason(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	seriesID := r.PathValue("id")
	seasonNum := r.PathValue("n")
	if seriesID == "" || seasonNum == "" {
		writeError(w, "series id and season required", http.StatusBadRequest)
		return
	}
	sonarrKey, _, _ := services.Keys()

	epData, err := services.ArrGet(sonarrURL, sonarrKey,
		"/api/v3/episode?seriesId="+url.QueryEscape(seriesID)+"&seasonNumber="+url.QueryEscape(seasonNum))
	if err != nil {
		writeError(w, "sonarr episode fetch failed", http.StatusBadGateway)
		return
	}
	fileData, err := services.ArrGet(sonarrURL, sonarrKey,
		"/api/v3/episodefile?seriesId="+url.QueryEscape(seriesID))
	if err != nil {
		writeError(w, "sonarr episodefile fetch failed", http.StatusBadGateway)
		return
	}

	var files []map[string]any
	_ = json.Unmarshal(fileData, &files)
	byID := map[float64]map[string]any{}
	for _, f := range files {
		if idF, ok := f["id"].(float64); ok {
			byID[idF] = f
		}
	}
	var eps []map[string]any
	_ = json.Unmarshal(epData, &eps)
	for _, e := range eps {
		if fid, ok := e["episodeFileId"].(float64); ok && fid > 0 {
			if file, ok := byID[fid]; ok {
				e["file"] = file
			}
		}
	}
	writeJSON(w, eps)
}

// handleCatalogItemHistory forwards a path to procula's jobs list filtered
// by that path and returns the most recent N records.
func handleCatalogItemHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, "path required", http.StatusBadRequest)
		return
	}
	_ = r.URL.Query().Get("limit") // reserved for future use
	resp, err := services.client.Get(proculaURL + "/api/procula/jobs")
	if err != nil {
		writeError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var all []map[string]any
	_ = json.Unmarshal(body, &all)
	var matching []map[string]any
	for _, j := range all {
		src, _ := j["source"].(map[string]any)
		if src == nil {
			continue
		}
		if p, _ := src["path"].(string); p == path {
			matching = append(matching, j)
		}
	}
	writeJSON(w, matching)
}
```

- [ ] Run: `go test ./middleware/ -run TestHandleCatalogList -v` — verify it passes.
- [ ] Commit: `feat(middleware): add catalog list handler with Radarr/Sonarr fan-out`

---

## Task 10 — middleware: season merge handler test

- [ ] Add test to `middleware/catalog_test.go`:

```go
func TestHandleCatalogSeasonMergesFiles(t *testing.T) {
	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/episode":
			w.Write([]byte(`[{"id":1,"episodeFileId":100,"title":"Ep 1"},{"id":2,"episodeFileId":0,"title":"Ep 2"}]`))
		case r.URL.Path == "/api/v3/episodefile":
			w.Write([]byte(`[{"id":100,"path":"/tv/Show/S01/ep1.mkv"}]`))
		}
	}))
	defer sonarr.Close()

	origS := sonarrURL
	sonarrURL = sonarr.URL
	services = &ServiceClients{SonarrKey: "k"}
	services.client = &http.Client{}
	t.Cleanup(func() { sonarrURL = origS })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/catalog/series/5/season/1", nil)
	req.SetPathValue("id", "5")
	req.SetPathValue("n", "1")
	w := httptest.NewRecorder()
	handleCatalogSeason(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var eps []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &eps)
	if len(eps) != 2 {
		t.Fatalf("eps = %d, want 2", len(eps))
	}
	if eps[0]["file"] == nil {
		t.Errorf("ep1 missing file merge")
	}
	if eps[1]["file"] != nil {
		t.Errorf("ep2 should not have file")
	}
}
```

- [ ] Run: `go test ./middleware/ -run TestHandleCatalogSeason -v` — verify it passes (the handler was created in Task 9).
- [ ] Commit: `test(middleware): cover catalog season merge handler`

---

## Task 11 — middleware: actions proxy + registry cache

- [ ] Add failing test to `middleware/catalog_test.go`:

```go
func TestHandleActionsRegistryCached(t *testing.T) {
	hits := 0
	procula := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/procula/actions/registry" {
			hits++
			w.Write([]byte(`[{"name":"validate","label":"Re-verify file"}]`))
		}
	}))
	defer procula.Close()

	origP := proculaURL
	proculaURL = procula.URL
	services = &ServiceClients{}
	services.client = &http.Client{}
	registryCache = actionRegistryCache{}
	t.Cleanup(func() { proculaURL = origP })

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/pelicula/actions/registry", nil)
		w := httptest.NewRecorder()
		handleActionsRegistry(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
	}
	if hits != 1 {
		t.Errorf("procula hits = %d, want 1 (cache should serve the rest)", hits)
	}
}
```

- [ ] Run: `go test ./middleware/ -run TestHandleActionsRegistry -v` — verify it fails.
- [ ] Create `middleware/actions.go`:

```go
// actions.go — proxies for POST /api/pelicula/actions and the cached
// registry endpoint. The action registry rarely changes during a procula
// lifetime, so we cache the JSON blob for 60 seconds to avoid a round
// trip on every context-menu open.
package main

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type actionRegistryCache struct {
	mu        sync.Mutex
	lastFetch time.Time
	body      []byte
}

var registryCache actionRegistryCache

const registryTTL = 60 * time.Second

// handleActionsRegistry proxies GET /api/procula/actions/registry with a
// 60-second in-memory cache.
func handleActionsRegistry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	registryCache.mu.Lock()
	defer registryCache.mu.Unlock()
	if len(registryCache.body) > 0 && time.Since(registryCache.lastFetch) < registryTTL {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Cache", "hit")
		w.Write(registryCache.body) //nolint:errcheck
		return
	}
	resp, err := services.client.Get(proculaURL + "/api/procula/actions/registry")
	if err != nil {
		writeError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		registryCache.body = body
		registryCache.lastFetch = time.Now()
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body) //nolint:errcheck
}

// handleActionsCreate proxies POST /api/procula/actions, forwarding the body
// and any ?wait= query param unchanged. PROCULA_API_KEY is injected.
func handleActionsCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	target := proculaURL + "/api/procula/actions"
	if q := r.URL.RawQuery; q != "" {
		target += "?" + q
	}
	upstream, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		writeError(w, "build request", http.StatusInternalServerError)
		return
	}
	upstream.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(os.Getenv("PROCULA_API_KEY")); key != "" {
		upstream.Header.Set("X-API-Key", key)
	}
	resp, err := services.client.Do(upstream)
	if err != nil {
		writeError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}
```

- [ ] Run: `go test ./middleware/ -run TestHandleActionsRegistry -v` — verify it passes.
- [ ] Commit: `feat(middleware): add actions proxy and cached registry endpoint`

---

## Task 12 — middleware: register catalog + actions routes

- [ ] Edit `middleware/main.go` — add these lines in the route registration block (place them after the existing library retranscode/resub routes, before the VPN admin block):

```go
	// viewer+: catalog (live Radarr/Sonarr library view)
	mux.Handle("/api/pelicula/catalog", auth.Guard(http.HandlerFunc(handleCatalogList)))
	mux.Handle("/api/pelicula/catalog/series/{id}", auth.Guard(http.HandlerFunc(handleCatalogSeriesDetail)))
	mux.Handle("/api/pelicula/catalog/series/{id}/season/{n}", auth.Guard(http.HandlerFunc(handleCatalogSeason)))
	mux.Handle("/api/pelicula/catalog/item/history", auth.Guard(http.HandlerFunc(handleCatalogItemHistory)))

	// admin only: action bus (mutating) — proxy to procula
	mux.Handle("/api/pelicula/actions", auth.GuardAdmin(http.HandlerFunc(handleActionsCreate)))
	mux.Handle("/api/pelicula/actions/registry", auth.Guard(http.HandlerFunc(handleActionsRegistry)))
```

- [ ] Run: `go build ./middleware/...` to verify the file compiles.
- [ ] Run: `go test ./middleware/... -v` — confirm nothing broke.
- [ ] Commit: `feat(middleware): register catalog and actions routes`

---

## Task 13 — middleware: Pipeline tab filter action_type=pipeline

- [ ] Add failing test to `middleware/pipeline_test.go` (or create it if missing):

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPipelineGetRequestsPipelineActionType(t *testing.T) {
	called := ""
	procula := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/procula/jobs" {
			called = r.URL.RawQuery
			w.Write([]byte(`[]`))
		}
	}))
	defer procula.Close()

	origP := proculaURL
	proculaURL = procula.URL
	// Provide a minimal services client so QbtGet can fail gracefully.
	services = &ServiceClients{}
	services.client = &http.Client{}
	t.Cleanup(func() { proculaURL = origP })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/pipeline", nil)
	w := httptest.NewRecorder()
	handlePipelineGet(w, req)

	if !strings.Contains(called, "action_type=pipeline") {
		t.Errorf("procula called with %q, want action_type=pipeline", called)
	}
}
```

(If the test panics because `services.QbtGet` needs additional setup, initialise the minimum required fields after inspecting `middleware/services.go`. Do not add a heavy qbt mock — the goal is just to exercise the procula call.)

- [ ] Run: `go test ./middleware/ -run TestPipelineGetRequestsPipelineActionType -v` — verify it fails (no filter passed yet).
- [ ] Edit `middleware/pipeline.go` `handlePipelineGet` — update the procula fetch URL:

```go
	go func() {
		resp, err := services.client.Get(proculaURL + "/api/procula/jobs?action_type=pipeline")
		if err != nil {
			proculaCh <- proculaFetchResult{err: err}
			return
		}
```

- [ ] Run: `go test ./middleware/ -run TestPipelineGetRequestsPipelineActionType -v` — verify it passes.
- [ ] Commit: `fix(middleware): scope pipeline board query to action_type=pipeline`

---

## Task 14 — frontend: catalog tab + section skeleton in index.html

- [ ] Edit `nginx/index.html` — insert the catalog tab button between pipeline and storage on the `<nav class="tabbar">`. The final tabbar should read:

```html
        <button class="tab active" data-tab="search" onclick="switchTab('search')">search</button>
        <button class="tab" data-tab="coming" onclick="switchTab('coming')">pipeline</button>
        <button class="tab" data-tab="catalog" onclick="switchTab('catalog')">catalog</button>
        <button class="tab" data-tab="storage" onclick="switchTab('storage')">storage</button>
        <button class="tab" data-tab="users" onclick="switchTab('users')">users</button>
        <button class="tab" data-tab="settings" onclick="switchTab('settings')">settings</button>
```

- [ ] Insert the `#catalog-section` panel immediately after the closing `</div>` of `#pipeline-section` and before the `<!-- Storage Management -->` comment. The new block:

```html
            <!-- Catalog -->
            <div class="section hidden" id="catalog-section" data-testid="catalog-section">
                <div class="um-hero">
                    <div class="um-hero-title-row">
                        <span class="um-hero-icon">&#128218;</span>
                        <h2 class="um-hero-title">Catalog</h2>
                    </div>
                    <p class="um-hero-sub">Browse your library and run actions per item</p>
                </div>
                <div class="cat-controls">
                    <input type="text" id="cat-search" class="cat-search-input" placeholder="Filter by title...">
                    <div class="cat-filters">
                        <button class="filter-btn active" data-cat-type="">All</button>
                        <button class="filter-btn" data-cat-type="movie">Movies</button>
                        <button class="filter-btn" data-cat-type="series">TV</button>
                    </div>
                </div>
                <div class="cat-list" id="cat-list">
                    <div class="no-items">Loading catalog...</div>
                </div>
                <ul class="cat-ctx hidden" id="cat-ctx" role="menu"></ul>
                <div class="cat-fanout hidden" id="cat-fanout">
                    <span id="cat-fanout-label"></span>
                    <button class="import-btn" id="cat-fanout-stop">Stop</button>
                </div>
            </div>
```

- [ ] Remove the Re-transcode and Re-search Subs buttons from `#storage-explorer-section`. The block that currently reads:

```html
                    <div class="action-bar-right">
                        <button class="import-btn" id="btn-import" data-testid="btn-import" onclick="onImportClick()" disabled title="">&#8615; Import</button>
                        <button class="import-btn" id="btn-transcode" onclick="onTranscodeClick()" disabled title="">&#8635; Transcode</button>
                        <button class="import-btn" id="btn-resub" onclick="onResubClick()" disabled title="">CC Re-search Subs</button>
                    </div>
```

becomes:

```html
                    <div class="action-bar-right">
                        <button class="import-btn" id="btn-import" data-testid="btn-import" onclick="onImportClick()" disabled title="">&#8615; Import</button>
                    </div>
```

- [ ] Add `<script src="catalog.js"></script>` next to the existing script includes near the bottom of `index.html` (after `dashboard.js`). Add `<link rel="stylesheet" href="catalog.css">` alongside the existing stylesheet link in `<head>`.

- [ ] Commit: `feat(dashboard): add catalog tab and section skeleton`

---

## Task 15 — frontend: catalog.css styles

- [ ] Create `nginx/catalog.css`:

```css
/* catalog.css — styles for the Catalog tab */
.cat-controls {
    display: flex;
    gap: 0.75rem;
    align-items: center;
    margin-bottom: 1rem;
    flex-wrap: wrap;
}
.cat-search-input {
    flex: 1 1 260px;
    min-width: 0;
    padding: 0.55rem 0.8rem;
    border-radius: 8px;
    border: 1px solid var(--border);
    background: var(--surface-glass, var(--glass2));
    color: var(--text-primary, var(--ink));
    font-size: 0.85rem;
}
.cat-filters { display: flex; gap: 0.4rem; }

.cat-list {
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
}
.cat-row {
    display: flex;
    align-items: center;
    gap: 0.7rem;
    padding: 0.55rem 0.8rem;
    border-radius: 8px;
    background: var(--surface-glass, var(--glass2));
    border: 1px solid var(--border);
}
.cat-row-title {
    flex: 1;
    min-width: 0;
    font-weight: 600;
    color: var(--text-primary, var(--ink));
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
}
.cat-row-meta {
    color: var(--text-muted, var(--muted));
    font-size: 0.75rem;
}
.cat-row-more {
    background: none;
    border: none;
    color: var(--text-muted, var(--muted));
    font-size: 1.1rem;
    cursor: pointer;
    padding: 0 0.4rem;
    border-radius: 4px;
}
.cat-row-more:hover { color: var(--pink); background: var(--border); }

.cat-series { display: flex; flex-direction: column; gap: 0.3rem; }
.cat-series-header {
    cursor: pointer;
    user-select: none;
}
.cat-series-caret {
    display: inline-block;
    width: 0.7rem;
    transition: transform 0.15s;
}
.cat-series.expanded .cat-series-caret { transform: rotate(90deg); }
.cat-season, .cat-episode {
    padding-left: 1.4rem;
    font-size: 0.8rem;
    color: var(--text-muted, var(--muted));
    display: flex;
    align-items: center;
    gap: 0.5rem;
}
.cat-episode { padding-left: 2.4rem; }

/* Context menu */
.cat-ctx {
    position: absolute;
    list-style: none;
    margin: 0;
    padding: 0.3rem 0;
    background: var(--surface-glass, var(--glass2));
    border: 1px solid var(--border);
    border-radius: 8px;
    min-width: 180px;
    box-shadow: 0 8px 24px rgba(0,0,0,0.25);
    z-index: 1000;
}
.cat-ctx li {
    padding: 0.45rem 0.85rem;
    cursor: pointer;
    font-size: 0.82rem;
    color: var(--text-primary, var(--ink));
}
.cat-ctx li:hover { background: var(--pink); color: #000; }

/* Fan-out strip */
.cat-fanout {
    position: sticky;
    bottom: 0;
    display: flex;
    align-items: center;
    gap: 1rem;
    padding: 0.6rem 1rem;
    background: var(--surface-glass, var(--glass2));
    border: 1px solid var(--border);
    border-radius: 8px;
    margin-top: 0.8rem;
}
```

- [ ] Edit `nginx/styles.css` — extend the tab visibility rules to include catalog. Find the `body[data-tab]` hide block and add `#catalog-section` to the selector list:

```css
body[data-tab] #search-section,
body[data-tab] #activity-section,
body[data-tab] #pipeline-section,
body[data-tab] #catalog-section,
body[data-tab] #storage-section,
body[data-tab] #storage-explorer-section,
body[data-tab] #users-section,
body[data-tab] #requests-section,
body[data-tab] #settings-section { display: none !important; }
```

Then add a show rule for the new tab immediately after the existing `body[data-tab="coming"]` rule:

```css
/* catalog: library + action bus */
body[data-tab="catalog"] #catalog-section { display: block !important; }
```

- [ ] Commit: `feat(dashboard): add catalog.css and tab visibility rule`

---

## Task 16 — frontend: catalog.js logic, context menu, action runner, fan-out

- [ ] Create `nginx/catalog.js`. The dashboard uses template-string + innerHTML rendering with an `esc()` helper to HTML-entity-escape every interpolated value. Follow that pattern exactly — never interpolate a raw value without `esc()`.

```javascript
// catalog.js — Catalog tab logic. Loads movies/series from the middleware
// proxy, renders an expand-on-demand TV tree, and runs per-item actions
// through /api/pelicula/actions with a dynamic context menu hydrated from
// /api/pelicula/actions/registry.
//
// All interpolated values pass through esc() for HTML-entity escaping.

const catState = {
    movies: [],
    series: [],
    filter: '',
    type: '',
    registry: null,
    fanoutStop: false,
};

function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, c => ({
        '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    }[c]));
}

async function loadCatalog() {
    const list = document.getElementById('cat-list');
    if (!list) return;
    try {
        const qs = new URLSearchParams();
        if (catState.filter) qs.set('q', catState.filter);
        if (catState.type) qs.set('type', catState.type);
        const res = await tfetch('/api/pelicula/catalog?' + qs.toString());
        if (!res.ok) throw new Error('HTTP ' + res.status);
        const data = await res.json();
        catState.movies = data.movies || [];
        catState.series = data.series || [];
        renderCatalog();
    } catch (e) {
        list.textContent = '';
        const err = document.createElement('div');
        err.className = 'no-items';
        err.textContent = 'Error loading catalog: ' + e.message;
        list.appendChild(err);
    }
}

function renderCatalog() {
    const list = document.getElementById('cat-list');
    const parts = [];

    if (catState.type !== 'series') {
        for (const m of catState.movies) {
            const path = (m.movieFile && m.movieFile.path) || '';
            parts.push(
                '<div class="cat-row" data-kind="movie" data-arr-id="' + esc(m.id) +
                '" data-path="' + esc(path) + '" data-title="' + esc(m.title) + '">' +
                    '<span class="cat-row-title">' + esc(m.title) + '</span>' +
                    '<span class="cat-row-meta">' + esc(m.year || '') + '</span>' +
                    '<button class="cat-row-more" aria-label="Actions">&#8943;</button>' +
                '</div>'
            );
        }
    }
    if (catState.type !== 'movie') {
        for (const s of catState.series) {
            parts.push(
                '<div class="cat-series" data-series-id="' + esc(s.id) +
                '" data-title="' + esc(s.title) + '">' +
                    '<div class="cat-row cat-series-header">' +
                        '<span class="cat-series-caret">&#9654;</span>' +
                        '<span class="cat-row-title">' + esc(s.title) + '</span>' +
                        '<span class="cat-row-meta">' + esc(s.year || '') + '</span>' +
                        '<button class="cat-row-more" data-kind="series" aria-label="Actions">&#8943;</button>' +
                    '</div>' +
                    '<div class="cat-series-body hidden"></div>' +
                '</div>'
            );
        }
    }
    list.innerHTML = parts.join('') || '<div class="no-items">No items</div>';
}

async function loadActionRegistry() {
    if (catState.registry) return catState.registry;
    try {
        const res = await tfetch('/api/pelicula/actions/registry');
        if (!res.ok) return [];
        catState.registry = await res.json();
        return catState.registry;
    } catch (e) {
        return [];
    }
}

// runAction posts to /api/pelicula/actions and, for sync actions, waits up
// to 8 seconds for the inline result. Returns the ActionResult JSON.
async function runAction(name, target, opts) {
    opts = opts || {};
    const sync = opts.sync !== false;
    const url = '/api/pelicula/actions' + (sync ? '?wait=8' : '');
    const res = await tfetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ action: name, target: target }),
    });
    if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        throw new Error(err.error || ('HTTP ' + res.status));
    }
    return res.json();
}

// showContextMenu anchors the <ul id="cat-ctx"> near (x,y), populates it
// with actions filtered by `kind`, and binds click handlers. Labels come
// from the server-controlled registry but are still passed through esc().
async function showContextMenu(x, y, kind, target) {
    const menu = document.getElementById('cat-ctx');
    const registry = await loadActionRegistry();
    const actions = (registry || []).filter(a => a.applies_to && a.applies_to.indexOf(kind) !== -1);
    if (!actions.length) {
        menu.innerHTML = '<li style="opacity:0.6;cursor:default">No actions</li>';
    } else {
        menu.innerHTML = actions.map(a =>
            '<li data-action="' + esc(a.name) + '" data-sync="' + (a.sync ? 1 : 0) + '">' +
            esc(a.label || a.name) + '</li>'
        ).join('');
    }
    menu.style.left = x + 'px';
    menu.style.top = y + 'px';
    menu.classList.remove('hidden');

    menu.onclick = async function(ev) {
        const li = ev.target.closest('li[data-action]');
        if (!li) return;
        const action = li.dataset.action;
        const sync = li.dataset.sync === '1';
        hideContextMenu();
        try {
            li.textContent = 'Running...';
            const result = await runAction(action, target, { sync: sync });
            flashToast(action + ': ' + (result.state || 'queued'));
        } catch (e) {
            flashToast(action + ' failed: ' + e.message);
        }
    };
}

function hideContextMenu() {
    const menu = document.getElementById('cat-ctx');
    if (menu) menu.classList.add('hidden');
}

function flashToast(msg) {
    const el = document.getElementById('toast');
    if (!el) { console.log('[catalog]', msg); return; }
    const span = el.querySelector('span:last-child');
    if (span) span.textContent = msg;
    el.classList.add('visible');
    setTimeout(() => el.classList.remove('visible'), 2500);
}

// runFanout iterates targets, calling runAction for each. Updates a visible
// progress strip and honors the stop flag.
async function runFanout(targets, action) {
    catState.fanoutStop = false;
    const strip = document.getElementById('cat-fanout');
    const label = document.getElementById('cat-fanout-label');
    strip.classList.remove('hidden');
    let done = 0;
    for (const t of targets) {
        if (catState.fanoutStop) break;
        label.textContent = action + ': ' + done + '/' + targets.length;
        try {
            await runAction(action, t, { sync: false });
        } catch (e) {
            console.warn('[catalog] fanout failed:', e);
        }
        done++;
    }
    label.textContent = action + ': ' + done + '/' + targets.length + ' complete';
    setTimeout(() => strip.classList.add('hidden'), 3000);
}

async function expandSeries(seriesEl) {
    const seriesId = seriesEl.dataset.seriesId;
    const body = seriesEl.querySelector('.cat-series-body');
    if (seriesEl.classList.contains('expanded')) {
        seriesEl.classList.remove('expanded');
        body.classList.add('hidden');
        return;
    }
    seriesEl.classList.add('expanded');
    body.classList.remove('hidden');
    if (body.dataset.loaded) return;

    body.innerHTML = '<div class="cat-season">Loading...</div>';
    try {
        const res = await tfetch('/api/pelicula/catalog/series/' + encodeURIComponent(seriesId));
        if (!res.ok) throw new Error('HTTP ' + res.status);
        const detail = await res.json();
        const seasons = (detail.seasons || []).filter(s => s.seasonNumber > 0);
        body.innerHTML = seasons.map(s =>
            '<div class="cat-season" data-season="' + esc(s.seasonNumber) + '">' +
                '<span class="cat-series-caret">&#9654;</span>' +
                '<span>Season ' + esc(s.seasonNumber) + '</span>' +
                '<button class="cat-row-more" data-kind="season">&#8943;</button>' +
                '<div class="cat-season-body hidden"></div>' +
            '</div>'
        ).join('') || '<div class="cat-season">No seasons</div>';
        body.dataset.loaded = '1';
    } catch (e) {
        body.innerHTML = '<div class="cat-season">Error: ' + esc(e.message) + '</div>';
    }
}

async function expandSeason(seriesEl, seasonEl) {
    const seriesId = seriesEl.dataset.seriesId;
    const seasonNum = seasonEl.dataset.season;
    const body = seasonEl.querySelector('.cat-season-body');
    if (body.dataset.loaded) {
        body.classList.toggle('hidden');
        return;
    }
    body.innerHTML = '<div class="cat-episode">Loading...</div>';
    try {
        const res = await tfetch('/api/pelicula/catalog/series/' +
            encodeURIComponent(seriesId) + '/season/' + encodeURIComponent(seasonNum));
        if (!res.ok) throw new Error('HTTP ' + res.status);
        const eps = await res.json();
        body.innerHTML = eps.map(e => {
            const path = (e.file && e.file.path) || '';
            return '<div class="cat-episode" data-kind="episode" data-ep-id="' + esc(e.id) +
                '" data-path="' + esc(path) + '">' +
                    '<span>' + esc(e.episodeNumber) + '. ' + esc(e.title || '') + '</span>' +
                    '<button class="cat-row-more" data-kind="episode">&#8943;</button>' +
                '</div>';
        }).join('');
        body.dataset.loaded = '1';
        body.classList.remove('hidden');
    } catch (e) {
        body.innerHTML = '<div class="cat-episode">Error: ' + esc(e.message) + '</div>';
    }
}

function initCatalog() {
    const list = document.getElementById('cat-list');
    if (!list) return;

    list.addEventListener('click', async (ev) => {
        const more = ev.target.closest('.cat-row-more');
        if (more) {
            ev.stopPropagation();
            const row = more.closest('[data-kind], [data-series-id]');
            const kind = more.dataset.kind || (row && row.dataset.kind) || 'movie';
            const target = {};
            if (row) {
                if (row.dataset.arrId) target.arr_id = parseInt(row.dataset.arrId, 10);
                if (row.dataset.seriesId) target.arr_id = parseInt(row.dataset.seriesId, 10);
                if (row.dataset.path) target.path = row.dataset.path;
                if (kind === 'movie') target.arr_type = 'radarr';
                else target.arr_type = 'sonarr';
                if (row.dataset.epId) target.episode_id = parseInt(row.dataset.epId, 10);
            }
            const rect = more.getBoundingClientRect();
            await showContextMenu(rect.right + window.scrollX, rect.bottom + window.scrollY, kind, target);
            return;
        }
        const seriesHeader = ev.target.closest('.cat-series-header');
        if (seriesHeader) {
            expandSeries(seriesHeader.parentElement);
            return;
        }
        const season = ev.target.closest('.cat-season');
        if (season) {
            const series = season.closest('.cat-series');
            expandSeason(series, season);
            return;
        }
    });

    list.addEventListener('contextmenu', async (ev) => {
        const row = ev.target.closest('[data-kind]');
        if (!row) return;
        ev.preventDefault();
        const kind = row.dataset.kind;
        const target = {
            path: row.dataset.path || '',
            arr_id: parseInt(row.dataset.arrId || '0', 10),
            arr_type: kind === 'movie' ? 'radarr' : 'sonarr',
            episode_id: parseInt(row.dataset.epId || '0', 10),
        };
        await showContextMenu(ev.pageX, ev.pageY, kind, target);
    });

    document.addEventListener('click', (ev) => {
        if (!ev.target.closest('#cat-ctx')) hideContextMenu();
    });
    document.addEventListener('keydown', (ev) => {
        if (ev.key === 'Escape') hideContextMenu();
    });

    const search = document.getElementById('cat-search');
    let searchTimer = null;
    if (search) {
        search.addEventListener('input', () => {
            clearTimeout(searchTimer);
            searchTimer = setTimeout(() => {
                catState.filter = search.value.trim();
                loadCatalog();
            }, 250);
        });
    }

    document.querySelectorAll('#catalog-section .filter-btn[data-cat-type]').forEach(btn => {
        btn.addEventListener('click', () => {
            document.querySelectorAll('#catalog-section .filter-btn').forEach(b => b.classList.remove('active'));
            btn.classList.add('active');
            catState.type = btn.dataset.catType;
            loadCatalog();
        });
    });

    const stopBtn = document.getElementById('cat-fanout-stop');
    if (stopBtn) stopBtn.addEventListener('click', () => { catState.fanoutStop = true; });
}

if (typeof window !== 'undefined') {
    window.addEventListener('DOMContentLoaded', () => {
        initCatalog();
        if (document.body.dataset.tab === 'catalog') loadCatalog();
    });
    document.addEventListener('pelicula:tab-changed', (e) => {
        if (e.detail === 'catalog') loadCatalog();
    });
}
```

- [ ] Edit `nginx/dashboard.js` — find the `switchTab` function and, at the end of the function body, dispatch the event so catalog.js can react. The new line to append inside `switchTab`, just before the closing `}`:

```javascript
    document.dispatchEvent(new CustomEvent('pelicula:tab-changed', { detail: tab }));
```

(If `switchTab` already dispatches a similar event, skip this sub-step and adjust the `document.addEventListener('pelicula:tab-changed', ...)` in catalog.js to use the existing event name.)

- [ ] Commit: `feat(dashboard): add catalog.js with tree, context menu, and fan-out`

---

## Task 17 — frontend: remove retranscode/resub UI bindings from import.js

- [ ] Edit `nginx/import.js`:
  - Delete the `rtProfileSelected` function.
  - Delete the `doRetranscode` function.
  - Delete the `renderRTResult` function.
  - Delete the `onResubClick` function and its section comment header.
- [ ] Verify no stragglers with Grep:
  ```
  Grep pattern="doRetranscode|renderRTResult|rtProfileSelected|onResubClick|btn-transcode|btn-resub" path=nginx/
  ```
  should return no matches.
- [ ] Verify middleware still has the endpoints (library/retranscode and library/resub stay — the legacy code is preserved):
  ```
  Grep pattern="library/retranscode|library/resub" path=middleware/
  ```
  should still match (middleware retains the endpoints).
- [ ] Commit: `refactor(dashboard): remove retranscode/resub UI bindings from import.js`

---

## Task 18 — full test sweep and build

- [ ] Run: `go test ./procula/... -v`
- [ ] Run: `go test ./middleware/... -v`
- [ ] Run: `go build ./procula/... && go build ./middleware/...`
- [ ] Run: `make test` (project top-level unit test target)
- [ ] Fix any failures surfaced by the sweep before proceeding.
- [ ] Commit (only if fixes were required): `test: fix regressions from catalog + action bus changes`

---

## Task 19 — manual dashboard smoke check notes

- [ ] The executing agent should add a manual verification checklist to the PR description covering:
  1. Catalog tab appears in the tabbar between pipeline and storage.
  2. Switching to it loads movies and series via `/api/pelicula/catalog`.
  3. The `⋯` button on a movie opens a context menu containing `Re-verify file`, `Re-transcode`, `Refresh subtitles`.
  4. Triggering `Re-verify file` returns a synchronous ActionResult (visible in DevTools Network).
  5. Triggering `Re-transcode` on a `/tv/` path produces a procula job with `arr_type=sonarr`, `type=episode` (check `/api/procula/jobs`).
  6. Pipeline tab no longer shows rows created by validate/transcode/subtitle_refresh actions.
  7. Storage Explorer no longer shows the Re-transcode or Re-search Subs buttons.

---

**End of plan.**
