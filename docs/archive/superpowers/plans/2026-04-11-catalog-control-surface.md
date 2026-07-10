# Catalog Control Surface Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the passive catalog into an operator control surface: flagged items rise to a "Needs Attention" section, a detail drawer surfaces encoding/subtitles/status pills, a Pipeline tab shows every job state across every phase, right-click actions can queue subtitle requests to Bazarr, and a Logs tab aggregates per-service output.

**Architecture:** Procula gains a `flags` JSON column on `jobs` and a derived index table `catalog_flags` keyed by media file path; a new `FlagEngine` runs after Validate/Transcode/Catalog and stamps flags on the owning job. Middleware exposes `/api/pelicula/catalog/flags`, `/api/pelicula/catalog/detail`, `/api/pelicula/jobs` (read-only aggregation over procula `GET /api/procula/jobs`), and `/api/pelicula/logs/aggregate` (fan-out over the existing docker-proxy logs endpoint). A new `subtitle_request` action in procula's action registry wraps the existing `bazarrSearchSubtitles` with an explicit per-language/HI/forced parameter surface and is routed via the existing `/api/pelicula/actions` proxy. The frontend gets a new collapsible "Needs Attention" section at the top of the catalog, a detail drawer with colorful pills, a dedicated Jobs tab, a Subtitle Request modal, and a Logs tab.

**Tech Stack:** Go (procula + middleware, modernc.org/sqlite pure-Go driver), vanilla JS/HTML/CSS (nginx static dashboard). No new runtime dependencies.

**Frontend safety note:** The dashboard composes rows by assigning to `.innerHTML` and relies on the existing `esc()` helper in `nginx/dashboard.js` (templated `html\`...\`` tag). Every interpolated value in new templates MUST be passed through `esc()`. Flag/pill data is server-controlled but still escaped defensively. The new Subtitle Request modal uses `createElement`/`textContent` for user-editable fields (matching the pattern in `nginx/catalog.js`) — no `innerHTML` with untrusted strings.

---

## Codebase Findings

### Q1: Procula Job Queue State

**File:** `procula/queue.go` — the queue is SQLite-backed (`procula/db.go`, table `jobs`, schema version 4) and mature. Key observations:

- **Job struct** (`procula/queue.go:70`) has `State` (`queued|processing|completed|failed|cancelled`), `Stage` (`validate|catalog|await_subs|dualsub|process|done`), `Progress` (0..1), rich validation/transcoding metadata, and an action-bus discriminator: `ActionType`, `Params`, `Result` (migration 3 in `db.go:111`). `CatalogInfo` (migration 4, `db.go:126`) records `jellyfin_synced`/`notification_sent`.
- **No parent/child relationship exists.** Jobs are flat. Each source file maps to one job row (deduped by `json_extract(source, '$.path')` in `Queue.Create`, `queue.go:185`). Action-bus jobs use `createActionJob` (`queue.go:249`) which bypasses dedup so each action request is its own row. Parent/child is **not needed for this plan** — we model flags per-job on the latest pipeline job for a given media path, and the Pipeline tab groups by lane.
- **Worker loop** (`procula/pipeline.go:18` `RunWorker` → `processJob`) pulls job IDs off `q.pending` and runs them serially. Action jobs dispatch via `runActionJob` (`pipeline.go:530`) to the registry in `procula/actions.go`.
- **Job lookup by path** is currently handled ad-hoc by the middleware `handleCatalogItemHistory` (`middleware/catalog.go:156`), which fetches `/api/procula/jobs` and filters. That approach stays — we add a derived `catalog_flags` table for O(1) flag lookup without walking all jobs.
- **Conclusion:** The queue does not need restructuring. We add one migration (version 5: `flags TEXT` on `jobs`, plus a new `catalog_flags` table), one engine (`procula/flags.go`) that produces flags from existing validation/codec/subtitle signals, and one action handler (`subtitle_request`).

### Q2: Bazarr Integration State

**Files:** `procula/bazarr.go`, `middleware/autowire.go:265-529`, `compose/docker-compose.yml:196-213`, `nginx/nginx.conf:304-308`.

- **Bazarr is a first-class compose service** (`bazarr:` in `compose/docker-compose.yml:196`, LinuxServer image pinned to `latest`, volumes `${CONFIG_DIR}/bazarr:/config`, `${LIBRARY_DIR}/movies:/movies`, `${LIBRARY_DIR}/tv:/tv`, healthcheck on `/bazarr/`). It is proxied at `/bazarr` in `nginx/nginx.conf:304`.
- **Autowiring** (`middleware/autowire.go:275 wireBazarr`): on startup the middleware reads `config/bazarr/config/config.yaml`, extracts `auth.apikey`, POSTs Sonarr+Radarr credentials via `bz/system/settings`, installs a single "Pelicula" language profile built from `PELICULA_SUB_LANGS`, and enables free provider set. Form-encoded POSTs because Bazarr uses Flask-RESTx `request.form`.
- **Procula subtitle calls** (`procula/bazarr.go`): `bazarrSearchSubtitles(ctx, configDir, job)` issues `PATCH /api/movies/subtitles` (radarr jobs, with `radarrid`) or `PATCH /api/episodes/subtitles` (sonarr jobs, with `seriesid`+`episodeid`), one PATCH per language, form-encoded, fire-and-forget. It currently pulls `language` from `job.MissingSubs` or falls back to `PELICULA_SUB_LANGS`. `hi` and `forced` are hard-coded to `False`.
- **Existing action:** `subtitle_refresh` in `procula/actions.go:103` is a thin wrapper that accepts `arr_type`/`arr_id`/`episode_id` and calls `bazarrSearchSubtitles`. It does not allow per-request language/HI/forced configuration.
- **Existing middleware entry points:** `/api/pelicula/library/resub` (`middleware/library.go:1342`) for library files, `/api/pelicula/procula/jobs/{id}/resub` (`library.go:1310`), and the `/api/pelicula/actions` proxy (`middleware/actions.go:59`) for registry-driven calls.
- **Conclusion:** Bazarr is fully wired. Phase 2 adds a new registered action `subtitle_request` that takes `languages` (`[]string`), `hi` (bool), `forced` (bool) params — extends the existing `bazarrSearchSubtitles` to accept these — and exposes it via the existing action registry without touching routing.

### Q3: Verification Flag Conditions

These are the signals currently produced by the pipeline that should materialize as flags. All come from code already in `procula/`:

1. **`validation_failed`** — `procula/pipeline.go:104` (`!result.Passed`). Sub-reasons exposed by `ValidationChecks`:
   - `integrity=fail` — ffprobe could not parse the file or file missing (`validate.go:52`, `validate.go:66`).
   - `sample=fail` — file under 50 MB absolute floor or under `3 MB × expected runtime` (`validate.go:162`).
   - `duration=fail` — ffprobe duration deviates > 50% from expected (`validate.go:194`).
   - `duration=warn` — deviation between 10% and 50% (`validate.go:196`) — does **not** currently fail validation but should become a flag (severity: warning).
2. **`missing_subtitles`** — `pipeline.go:161` populates `job.MissingSubs` when embedded codec tracks don't cover `PELICULA_SUB_LANGS`. Informational today; becomes a flag with the list of missing language codes.
3. **`sub_timeout`** — `procula/await_subs.go` emits `EventSubTimeout` when awaiting Bazarr times out with languages still missing. Flag severity: warning.
4. **`transcode_failed`** — `pipeline.go:375` sets `TranscodeDecision=failed` and `TranscodeError=...`. The pipeline continues with the original file, so this is a completed-with-flag state, not a failed job. Flag severity: warning.
5. **`dualsub_failed`** — `pipeline.go:242` sets `DualSubError` when no usable subtitle source produced an ASS pair. Severity: info.
6. **`catalog_not_synced`** — `pipeline.go:183-191` only sets `CatalogInfo.JellyfinSynced=true` when `CatalogEarly` actually ran; if the catalog stage was disabled or errored, the flag engine will emit this. Severity: info.
7. **`encoding_mismatch`** — computed by the flag engine: if `Validation.Checks.Codecs.Video` or `Height` does not match the active transcode profile (we compare against `FindMatchingProfile` in `procula/profiles.go`), and no passthrough decision exists, emit a warning flag. This is the only flag **not** already surfaced by pipeline code; the engine derives it from existing data.

Flag data model (`procula/flags.go`):

```go
type FlagSeverity string
const (
    FlagSeverityError FlagSeverity = "error"
    FlagSeverityWarn  FlagSeverity = "warn"
    FlagSeverityInfo  FlagSeverity = "info"
)

type Flag struct {
    Code     string       `json:"code"`      // e.g. "validation_failed"
    Severity FlagSeverity `json:"severity"`
    Detail   string       `json:"detail,omitempty"`
    Fields   map[string]any `json:"fields,omitempty"` // extra context (missing_langs, check, codec...)
}
```

### Q4: Current Catalog UI

**Files:** `nginx/index.html` (tabs, section shells), `nginx/catalog.js`, `nginx/catalog.css`, `nginx/dashboard.js` (shared toast + drawer).

- **Tabs** are driven by `body[data-tab]` CSS (`nginx/styles.css:178-203`); `window.switchTab(tab)` (`nginx/index.html:870`, wrapped in `nginx/dashboard.js:2357`) dispatches `pelicula:tab-changed` with `{tab}`. `catalog.js:506` listens for the catalog tab to lazy-init.
- **Catalog render pipeline:** `catalog.js` fetches `/api/pelicula/catalog?q=&type=` which returns `{movies:[], series:[]}` proxied from Radarr's `/api/v3/movie` and Sonarr's `/api/v3/series` (`middleware/catalog.go:18-61`). It renders a flat list of `.cat-row` elements. Clicking a series expands via `/api/pelicula/catalog/series/{id}` and `/api/pelicula/catalog/series/{id}/season/{n}` (`middleware/catalog.go:88-153`). Each row has a `⋯` context menu built from `/api/pelicula/actions/registry` and dispatches to `/api/pelicula/actions?wait=10` (`catalog.js:356`).
- **Detail view:** there is none for catalog rows. The pipeline tab has a drawer (`openJobDrawer` in `nginx/dashboard.js:2269`) keyed by Procula job ID — it shows validation check pills (`.proc-check`), a Transcoding k/v table, and an Error block. That drawer is the template we extend for per-catalog-item detail.
- **Needs Attention section:** lives only in the Pipeline tab today (`nginx/index.html:123`, rendered by `renderPipeline` → `pipeline-attention` container in `nginx/dashboard.js:1353`) and is driven by `/api/pelicula/pipeline` lane `needs_attention` (`middleware/pipeline.go:437`). The catalog has no equivalent — we add one.
- **Logs:** admin-only endpoint `/api/pelicula/admin/logs?svc=<name>&tail=<n>` (`middleware/admin_ops.go:150`) returns demuxed stdout+stderr for a single container. There is no aggregation and no frontend tab — we add both.

---

## File Map

**Procula — Go backend (modernc.org/sqlite, stdlib HTTP):**
- `procula/db.go` — add `migrate5` (creates `catalog_flags` table; adds `flags TEXT` to `jobs`). Modify `migrations` slice.
- `procula/flags.go` — **new**. `Flag`, `FlagSeverity`, `FlagEngine.Compute(job) []Flag`, persistence helpers `UpsertFlagsForPath`, `FlagsByPath`, `AllFlaggedPaths`.
- `procula/flags_test.go` — **new**. Unit tests for every condition in Q3 plus persistence.
- `procula/queue.go` — extend `Job` struct with `Flags []Flag`, `scanJob`/`Update`/`Create`/`createActionJob` to read/write `flags` column.
- `procula/queue_test.go` — **new test** `TestJobPersistsFlags` covering round-trip.
- `procula/pipeline.go` — call `FlagEngine.Compute` + `persistFlags` after Validate, Transcode, Catalog stages.
- `procula/pipeline_test.go` — extend existing test coverage with `TestPipelineStampsValidationFailedFlag`.
- `procula/actions.go` — register a new `subtitle_request` action with `Handler: runSubtitleRequestAction`, `Sync: true`.
- `procula/actions_test.go` — **new test** `TestSubtitleRequestActionPassesParams`.
- `procula/bazarr.go` — extend `bazarrSearchSubtitles` signature to `bazarrSearchSubtitlesWithOpts(ctx, configDir, job, opts)` where `opts` carries `hi`/`forced`; keep the old signature as a thin wrapper for existing callers.
- `procula/bazarr_test.go` — extend round-trip test to assert the form contains `hi=True` when passed.
- `procula/main.go` — add `GET /api/procula/catalog/flags` handler (serves `FlagsByPath`), add `GET /api/procula/jobs` is already there (no change).

**Middleware — pelicula-api:**
- `middleware/catalog.go` — add `handleCatalogFlags` (proxy to procula `/api/procula/catalog/flags`), `handleCatalogDetail` (merges Radarr/Sonarr metadata for a path with flags + latest job data).
- `middleware/catalog_test.go` — add tests for both new handlers.
- `middleware/jobs.go` — **new**. `handleJobsList` proxies `/api/procula/jobs` with optional `?lane=` filter and normalises job rows into a `JobCard` shape.
- `middleware/jobs_test.go` — **new**. Cover lane filtering and JSON shape.
- `middleware/logs_aggregate.go` — **new**. `handleLogsAggregate` fans out to `dockerLogs` for every allowlisted container and returns a merged JSONL stream tagged with service name + colour.
- `middleware/logs_aggregate_test.go` — **new**. Uses a test double for `dockerLogs`.
- `middleware/main.go` — register routes: `GET /api/pelicula/catalog/flags` (Guard), `GET /api/pelicula/catalog/detail` (Guard), `GET /api/pelicula/jobs` (Guard), `GET /api/pelicula/logs/aggregate` (GuardAdmin).

**Frontend — nginx static dashboard:**
- `nginx/index.html` — add `data-tab="jobs"` and `data-tab="logs"` tabs + matching sections (`#jobs-section`, `#logs-section`); add `<div id="cat-attention-wrap">` at the top of `#catalog-section`; add a new `<dialog id="cat-detail-drawer">` with pill container; add `<dialog id="sub-request-dialog">`.
- `nginx/styles.css` — selectors for the two new tabs.
- `nginx/catalog.css` — pill styles (`.cat-pill`, `.cat-pill-encoding`, `.cat-pill-subs`, `.cat-pill-status`, `.cat-pill-error`, `.cat-pill-warn`), attention banner, detail drawer, log colour-by-service scheme.
- `nginx/catalog.js` — render "Needs Attention" section, wire detail drawer, wire subtitle request dialog, add right-click context menu.
- `nginx/jobs.js` — **new**. Renders the Jobs tab (all procula jobs grouped by state/stage).
- `nginx/logs.js` — **new**. Renders the Logs tab, polls `/api/pelicula/logs/aggregate?tail=200`, colours lines by service.
- `nginx/index.html` — `<script src="/jobs.js" defer></script>` and `<script src="/logs.js" defer></script>`.

**Auth:** all new read endpoints use `auth.Guard` (viewer+); the logs aggregator uses `auth.GuardAdmin`; the subtitle_request action is already routed through `/api/pelicula/actions` which is `auth.GuardAdmin` (`middleware/main.go:173`). The catalog detail endpoint is viewer+ because the data is already visible to viewers via the existing catalog proxy.

---

## Phase 1 — Visibility

### Task 1.1 — procula: schema migration for `flags` column and `catalog_flags` table

- [ ] Add failing test `TestMigrate5AddsFlagSchema` to `procula/db_test.go`:

```go
func TestMigrate5AddsFlagSchema(t *testing.T) {
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
	if ver < 5 {
		t.Fatalf("user_version = %d, want >= 5", ver)
	}

	// jobs.flags column exists
	cols := map[string]bool{}
	rows, err := db.Query(`PRAGMA table_info(jobs)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
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
	rows.Close()
	if !cols["flags"] {
		t.Errorf("jobs.flags column missing")
	}

	// catalog_flags table exists with expected columns
	cols = map[string]bool{}
	rows, err = db.Query(`PRAGMA table_info(catalog_flags)`)
	if err != nil {
		t.Fatalf("table_info catalog_flags: %v", err)
	}
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
	rows.Close()
	for _, want := range []string{"path", "flags", "severity", "job_id", "updated_at"} {
		if !cols[want] {
			t.Errorf("catalog_flags.%s missing", want)
		}
	}
}
```

- [ ] Run: `cd procula && go test -run TestMigrate5AddsFlagSchema ./...`

  Expected output: failure — `user_version = 4, want >= 5`.

- [ ] Implement migration in `procula/db.go`. Add to the `migrations` slice:

```go
{version: 5, up: migrate5},
```

And add the function:

```go
// migrate5 adds the flags column to jobs and creates the catalog_flags
// index table (path → aggregated flag list + top severity) used by the
// catalog dashboard "Needs Attention" section.
func migrate5(tx *sql.Tx) error {
	stmts := []string{
		`ALTER TABLE jobs ADD COLUMN flags TEXT`,
		`CREATE TABLE IF NOT EXISTS catalog_flags (
			path       TEXT PRIMARY KEY,
			flags      TEXT NOT NULL,
			severity   TEXT NOT NULL,
			job_id     TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_catalog_flags_severity ON catalog_flags(severity)`,
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return nil
}
```

- [ ] Run: `cd procula && go test -run TestMigrate5AddsFlagSchema ./...`

  Expected output: `PASS`.

- [ ] Commit with message `feat(procula): migrate5 — flags column + catalog_flags table`.

### Task 1.2 — procula: Flag engine and `Flag` type

- [ ] Add failing test `TestFlagEngineEnumeratesConditions` to `procula/flags_test.go` (**new file**):

```go
package main

import (
	"testing"
)

func TestFlagEngineEmitsValidationFailed(t *testing.T) {
	job := &Job{
		State: StateFailed,
		Stage: StageValidate,
		Validation: &ValidationResult{
			Passed: false,
			Checks: ValidationChecks{Integrity: "fail", Duration: "skip", Sample: "skip"},
		},
		Error: "ffprobe failed: unknown format",
	}
	flags := ComputeFlags(job)
	if !containsFlagCode(flags, "validation_failed") {
		t.Fatalf("missing validation_failed flag; got %+v", flags)
	}
	if !containsFlagCode(flags, "integrity_fail") {
		t.Fatalf("missing integrity_fail flag; got %+v", flags)
	}
}

func TestFlagEngineEmitsDurationWarn(t *testing.T) {
	job := &Job{
		State: StateCompleted,
		Validation: &ValidationResult{
			Passed: true,
			Checks: ValidationChecks{Integrity: "pass", Duration: "warn", Sample: "pass"},
		},
	}
	flags := ComputeFlags(job)
	if !containsFlagCode(flags, "duration_warn") {
		t.Fatalf("missing duration_warn flag; got %+v", flags)
	}
}

func TestFlagEngineEmitsMissingSubtitles(t *testing.T) {
	job := &Job{
		State:       StateCompleted,
		MissingSubs: []string{"en", "es"},
	}
	flags := ComputeFlags(job)
	for _, want := range []string{"missing_subtitles"} {
		if !containsFlagCode(flags, want) {
			t.Fatalf("missing %q flag; got %+v", want, flags)
		}
	}
}

func TestFlagEngineEmitsTranscodeFailed(t *testing.T) {
	job := &Job{
		State:             StateCompleted,
		TranscodeDecision: "failed",
		TranscodeError:    "ffmpeg exit 1",
	}
	flags := ComputeFlags(job)
	if !containsFlagCode(flags, "transcode_failed") {
		t.Fatalf("missing transcode_failed flag")
	}
}

func TestFlagEngineEmitsCatalogNotSynced(t *testing.T) {
	job := &Job{
		State:   StateCompleted,
		Stage:   StageDone,
		Catalog: &CatalogInfo{JellyfinSynced: false},
	}
	flags := ComputeFlags(job)
	if !containsFlagCode(flags, "catalog_not_synced") {
		t.Fatalf("missing catalog_not_synced flag")
	}
}

func TestFlagEngineClean(t *testing.T) {
	job := &Job{
		State: StateCompleted,
		Stage: StageDone,
		Validation: &ValidationResult{
			Passed: true,
			Checks: ValidationChecks{Integrity: "pass", Duration: "pass", Sample: "pass"},
		},
		TranscodeDecision: "passthrough",
		Catalog:           &CatalogInfo{JellyfinSynced: true},
	}
	flags := ComputeFlags(job)
	if len(flags) != 0 {
		t.Fatalf("clean job produced flags: %+v", flags)
	}
}

func containsFlagCode(fs []Flag, code string) bool {
	for _, f := range fs {
		if f.Code == code {
			return true
		}
	}
	return false
}
```

- [ ] Run: `cd procula && go test -run TestFlagEngine ./...`

  Expected output: compile errors — `ComputeFlags` undefined, `Flag` undefined.

- [ ] Create `procula/flags.go`:

```go
// flags.go — derives catalog flags from pipeline job state and persists them
// in the catalog_flags index table. The engine is pure: ComputeFlags takes a
// Job and returns zero or more Flag records. Persistence is separate.
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// FlagSeverity orders flags by urgency. "error" floats a row into the
// catalog's Needs Attention section; "warn" and "info" are shown as pills
// on the item but do not promote it.
type FlagSeverity string

const (
	FlagSeverityError FlagSeverity = "error"
	FlagSeverityWarn  FlagSeverity = "warn"
	FlagSeverityInfo  FlagSeverity = "info"
)

// Flag is a single derived issue on a media item. Code is a stable
// identifier the frontend pattern-matches on; Detail is a short human
// string; Fields carries structured extras (missing_langs, profile name, ...).
type Flag struct {
	Code     string         `json:"code"`
	Severity FlagSeverity   `json:"severity"`
	Detail   string         `json:"detail,omitempty"`
	Fields   map[string]any `json:"fields,omitempty"`
}

// ComputeFlags is the flag engine. It is pure — no DB, no side effects —
// so it can be unit tested with synthetic jobs.
func ComputeFlags(j *Job) []Flag {
	var out []Flag

	// Validation failures (hard error).
	if j.Validation != nil && !j.Validation.Passed {
		out = append(out, Flag{
			Code:     "validation_failed",
			Severity: FlagSeverityError,
			Detail:   j.Error,
		})
		checks := j.Validation.Checks
		if checks.Integrity == "fail" {
			out = append(out, Flag{Code: "integrity_fail", Severity: FlagSeverityError})
		}
		if checks.Sample == "fail" {
			out = append(out, Flag{Code: "sample_fail", Severity: FlagSeverityError})
		}
		if checks.Duration == "fail" {
			out = append(out, Flag{Code: "duration_fail", Severity: FlagSeverityError})
		}
	}

	// Validation passed but duration drifted 10-50% (warn).
	if j.Validation != nil && j.Validation.Passed &&
		j.Validation.Checks.Duration == "warn" {
		out = append(out, Flag{Code: "duration_warn", Severity: FlagSeverityWarn})
	}

	// Missing subtitle languages (warn — user visible but not blocking).
	if len(j.MissingSubs) > 0 {
		out = append(out, Flag{
			Code:     "missing_subtitles",
			Severity: FlagSeverityWarn,
			Fields:   map[string]any{"langs": j.MissingSubs},
		})
	}

	// Sub timeout was emitted via event log but we also reflect it via
	// MissingSubs being non-empty after await_subs ran; the engine re-uses
	// the missing_subtitles flag there. No separate emission.

	// Transcode failed but pipeline continued with the original (warn).
	if j.TranscodeDecision == "failed" {
		out = append(out, Flag{
			Code:     "transcode_failed",
			Severity: FlagSeverityWarn,
			Detail:   j.TranscodeError,
		})
	}

	// Dual-sub generation failed (info — cosmetic).
	if j.DualSubError != "" {
		out = append(out, Flag{
			Code:     "dualsub_failed",
			Severity: FlagSeverityInfo,
			Detail:   j.DualSubError,
		})
	}

	// Catalog stage did not sync with Jellyfin (info — maybe disabled).
	if j.Stage == StageDone && (j.Catalog == nil || !j.Catalog.JellyfinSynced) {
		out = append(out, Flag{Code: "catalog_not_synced", Severity: FlagSeverityInfo})
	}

	return out
}

// topSeverity picks the most urgent severity in a flag list.
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

// UpsertFlagsForPath persists the given flag list in the catalog_flags
// index. Empty flag lists delete the row so clean items vanish from the
// Needs Attention query.
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

// CatalogFlagRow is what the HTTP handler serves.
type CatalogFlagRow struct {
	Path      string    `json:"path"`
	Flags     []Flag    `json:"flags"`
	Severity  string    `json:"severity"`
	JobID     string    `json:"job_id"`
	UpdatedAt time.Time `json:"updated_at"`
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
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
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
```

- [ ] Run: `cd procula && go test -run TestFlagEngine ./...`

  Expected output: `PASS` (5 test cases).

- [ ] Add a persistence test `TestFlagsPersistence` to `procula/flags_test.go`:

```go
func TestFlagsPersistence(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	flags := []Flag{
		{Code: "validation_failed", Severity: FlagSeverityError, Detail: "ffprobe"},
		{Code: "missing_subtitles", Severity: FlagSeverityWarn, Fields: map[string]any{"langs": []string{"en"}}},
	}
	if err := UpsertFlagsForPath(db, "/movies/Foo (2024)/Foo.mkv", "job_1", flags); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := FlagsByPath(db, "/movies/Foo (2024)/Foo.mkv")
	if err != nil || got == nil {
		t.Fatalf("FlagsByPath err=%v row=%v", err, got)
	}
	if got.Severity != "error" {
		t.Errorf("severity = %q, want error", got.Severity)
	}
	if len(got.Flags) != 2 {
		t.Errorf("len(flags) = %d, want 2", len(got.Flags))
	}

	// Clearing with empty slice removes the row.
	if err := UpsertFlagsForPath(db, "/movies/Foo (2024)/Foo.mkv", "job_1", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _ = FlagsByPath(db, "/movies/Foo (2024)/Foo.mkv")
	if got != nil {
		t.Errorf("expected row deleted, got %+v", got)
	}
}
```

Add `"path/filepath"` to the imports.

- [ ] Run: `cd procula && go test -run TestFlags ./...`

  Expected output: `PASS`.

- [ ] Commit: `feat(procula): flag engine with severity ladder and catalog_flags store`.

### Task 1.3 — procula: extend `Job` to carry `Flags` and persist through queue

- [ ] Add failing test `TestJobPersistsFlags` to `procula/queue_test.go`:

```go
func TestJobPersistsFlags(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	q, err := NewQueue(db)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}

	job, err := q.Create(JobSource{Path: "/movies/T/T.mkv", ArrType: "radarr", Title: "T"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := q.Update(job.ID, func(j *Job) {
		j.Flags = []Flag{{Code: "validation_failed", Severity: FlagSeverityError}}
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, ok := q.Get(job.ID)
	if !ok || len(got.Flags) != 1 || got.Flags[0].Code != "validation_failed" {
		t.Fatalf("flags round-trip failed: %+v", got)
	}
}
```

- [ ] Run: `cd procula && go test -run TestJobPersistsFlags ./...`

  Expected output: compile error — `Job` has no field `Flags`.

- [ ] Edit `procula/queue.go`. Add a field to `Job`:

```go
	// Flags are derived issues (validation failures, missing subs, etc.)
	// computed by ComputeFlags after each pipeline stage. Empty means clean.
	Flags []Flag `json:"flags,omitempty"`
```

- [ ] Edit the SELECT column lists in `Get`, `List`, `ListByActionType` → add `flags` (and bump the `scanJob` scan targets). Example replacement for `Get`:

```go
func (q *Queue) Get(id string) (*Job, bool) {
	row := q.db.QueryRow(
		`SELECT id, created_at, updated_at, state, stage, progress, source, validation, missing_subs,
		        subs_acquired,
		        error, retry_count, manual_profile, dualsub_outputs, dualsub_error,
		        transcode_profile, transcode_decision, transcode_outputs, transcode_error, transcode_eta,
		        action_type, params, result, catalog, flags
		 FROM jobs WHERE id=?`, id,
	)
	job, err := scanJob(row)
	if err != nil {
		return nil, false
	}
	return job, true
}
```

Apply the same `flags` suffix to the two `q.db.Query(...)` calls in `List` and via `List` in `ListByActionType`.

- [ ] Edit `scanJob` to add `flagsJSON *string` alongside `catalogJSON`, extend the `s.Scan(...)` targets, and after the existing catalog unmarshal add:

```go
	if flagsJSON != nil {
		json.Unmarshal([]byte(*flagsJSON), &job.Flags) //nolint:errcheck
	}
```

- [ ] Edit `Create` to insert `flags` as NULL:

```go
	_, err = q.db.Exec(
		`INSERT INTO jobs (id, created_at, updated_at, state, stage, progress, source, error, retry_count,
		                   manual_profile, dualsub_error, transcode_profile, transcode_decision, transcode_error, transcode_eta,
		                   action_type, params, result, flags)
		 VALUES (?, ?, ?, ?, ?, 0, ?, '', 0, '', '', '', '', '', 0, 'pipeline', NULL, NULL, NULL)`,
		id, ...
```

Apply the same two-column expansion to `createActionJob`.

- [ ] Edit `Update` to serialise `flags`:

```go
	var flagsJSON *string
	if job.Flags != nil {
		b, _ := json.Marshal(job.Flags)
		s := string(b)
		flagsJSON = &s
	}
```

Add `flags=?` at the end of the `SET` clause and append `flagsJSON` to the parameter list before `id`.

- [ ] Run: `cd procula && go test ./...`

  Expected output: all tests in `queue_test.go`, `flags_test.go`, `db_test.go` pass; `TestJobPersistsFlags` passes.

- [ ] Commit: `feat(procula): persist Job.Flags through the queue`.

### Task 1.4 — procula: pipeline stamps flags after each stage

- [ ] Add failing test `TestProcessJobStampsFlagsOnValidationFailure` to `procula/pipeline_test.go`. (Match the existing test package setup — e.g. fake ffprobe via `ffprobeCommand`.)

```go
func TestPipelineStampsValidationFailedFlag(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "test.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	appDB = db

	q, err := NewQueue(db)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}

	// Non-existent path → Validate fails with "file not found".
	job, err := q.Create(JobSource{
		Path:    filepath.Join(tmp, "missing.mkv"),
		ArrType: "radarr",
		Title:   "Missing",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	processJob(q, job.ID, tmp, "http://test")

	got, _ := q.Get(job.ID)
	if got.State != StateFailed {
		t.Fatalf("state = %s, want failed", got.State)
	}
	if !containsFlagCode(got.Flags, "validation_failed") {
		t.Fatalf("missing validation_failed flag; got %+v", got.Flags)
	}

	row, err := FlagsByPath(db, job.Source.Path)
	if err != nil || row == nil {
		t.Fatalf("catalog_flags row missing: err=%v row=%v", err, row)
	}
	if row.Severity != "error" {
		t.Errorf("severity = %s, want error", row.Severity)
	}
}
```

- [ ] Run: `cd procula && go test -run TestPipelineStampsValidationFailedFlag ./...`

  Expected output: failure — no flags are persisted yet.

- [ ] Edit `procula/pipeline.go`. Add a helper right below `processJob`:

```go
// persistFlags recomputes flags for the job and writes them to both the
// job row and the catalog_flags index. Called after each pipeline stage
// that can change flag state.
func persistFlags(q *Queue, id string) {
	job, ok := q.Get(id)
	if !ok {
		return
	}
	flags := ComputeFlags(job)
	_ = q.Update(id, func(j *Job) { j.Flags = flags })
	if job.Source.Path != "" {
		if err := UpsertFlagsForPath(appDB, job.Source.Path, id, flags); err != nil {
			slog.Warn("persist flags failed", "component", "pipeline", "job_id", id, "error", err)
		}
	}
}
```

Insert `persistFlags(q, id)` calls at these points in `processJob`:
1. Immediately after the validation-failed block that currently returns (`pipeline.go` after line 144, before the existing `return`).
2. At the end of the validate-passed branch, after `_ = q.Update(id, func(j *Job) { j.MissingSubs = missing })` (`pipeline.go:164`).
3. After the `maybeTranscode` return point (`pipeline.go:271`) — before the `job, _ = q.Get(id)` reload.
4. Inside the final "Done" block before returning success.

Example for site 1:

```go
			// Notify the dashboard
			WriteValidationFailedNotification(job, configDir, failReason)
			persistFlags(q, id)
			return
```

- [ ] Run: `cd procula && go test -run TestPipelineStampsValidationFailedFlag ./...`

  Expected output: `PASS`.

- [ ] Run: `cd procula && go test ./...`

  Expected output: full suite `PASS`.

- [ ] Commit: `feat(procula): pipeline stamps flags after each stage`.

### Task 1.5 — procula: HTTP endpoint `GET /api/procula/catalog/flags`

- [ ] Add failing test to `procula/main_test.go` (or a new `procula/catalog_flags_test.go`):

```go
func TestHandleCatalogFlagsReturnsAll(t *testing.T) {
	tmp := t.TempDir()
	db, err := OpenDB(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	flags := []Flag{{Code: "validation_failed", Severity: FlagSeverityError}}
	if err := UpsertFlagsForPath(db, "/movies/A/A.mkv", "job_a", flags); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	s := &Server{db: db, configDir: tmp}
	req := httptest.NewRequest(http.MethodGet, "/api/procula/catalog/flags", nil)
	w := httptest.NewRecorder()
	s.handleCatalogFlags(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Rows []CatalogFlagRow `json:"rows"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Rows) != 1 || resp.Rows[0].Path != "/movies/A/A.mkv" {
		t.Fatalf("rows = %+v", resp.Rows)
	}
}
```

Add `"encoding/json"`, `"net/http"`, `"net/http/httptest"`, `"path/filepath"`, `"testing"` to imports.

- [ ] Run: `cd procula && go test -run TestHandleCatalogFlags ./...`

  Expected output: compile error — `handleCatalogFlags` undefined.

- [ ] Edit `procula/main.go`. Add handler:

```go
// handleCatalogFlags returns every row in the catalog_flags table,
// sorted error > warn > info, newest first within each bucket.
func (s *Server) handleCatalogFlags(w http.ResponseWriter, r *http.Request) {
	rows, err := AllFlagged(s.db)
	if err != nil {
		writeError(w, "flags query failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []CatalogFlagRow{}
	}
	writeJSON(w, map[string]any{"rows": rows})
}
```

Register it in `main()` near the other catalog routes:

```go
	mux.HandleFunc("GET /api/procula/catalog/flags", srv.handleCatalogFlags)
```

- [ ] Run: `cd procula && go test ./...`

  Expected output: all pass.

- [ ] Commit: `feat(procula): GET /api/procula/catalog/flags`.

### Task 1.6 — middleware: `GET /api/pelicula/catalog/flags` proxy and `GET /api/pelicula/catalog/detail`

- [ ] Add failing test to `middleware/catalog_test.go`:

```go
func TestHandleCatalogFlagsProxies(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/procula/catalog/flags" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"rows":[{"path":"/movies/A/A.mkv","severity":"error","flags":[{"code":"validation_failed","severity":"error"}],"job_id":"job_a","updated_at":"2026-04-11T00:00:00Z"}]}`))
	}))
	defer upstream.Close()

	orig := proculaURL
	proculaURL = upstream.URL
	services = &ServiceClients{}
	services.client = &http.Client{}
	t.Cleanup(func() { proculaURL = orig })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/catalog/flags", nil)
	w := httptest.NewRecorder()
	handleCatalogFlags(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"validation_failed"`) {
		t.Fatalf("body missing flag code: %s", w.Body.String())
	}
}

func TestHandleCatalogDetailMergesFlagsAndJob(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/procula/catalog/flags":
			w.Write([]byte(`{"rows":[{"path":"/movies/A/A.mkv","severity":"warn","flags":[{"code":"missing_subtitles","severity":"warn","fields":{"langs":["es"]}}],"job_id":"job_a","updated_at":"2026-04-11T00:00:00Z"}]}`))
		case "/api/procula/jobs":
			w.Write([]byte(`[{"id":"job_a","state":"completed","stage":"done","source":{"path":"/movies/A/A.mkv","title":"A"},"validation":{"passed":true,"checks":{"integrity":"pass","duration":"pass","sample":"pass","codecs":{"video":"h264","audio":"aac","subtitles":["eng"],"width":1920,"height":1080}}}}]`))
		}
	}))
	defer upstream.Close()

	orig := proculaURL
	proculaURL = upstream.URL
	services = &ServiceClients{}
	services.client = &http.Client{}
	t.Cleanup(func() { proculaURL = orig })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/catalog/detail?path=%2Fmovies%2FA%2FA.mkv", nil)
	w := httptest.NewRecorder()
	handleCatalogDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Path  string                 `json:"path"`
		Flags []map[string]any       `json:"flags"`
		Job   map[string]any         `json:"job"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Flags) != 1 || resp.Flags[0]["code"] != "missing_subtitles" {
		t.Fatalf("flags = %+v", resp.Flags)
	}
	if resp.Job == nil || resp.Job["id"] != "job_a" {
		t.Fatalf("job = %+v", resp.Job)
	}
}
```

Add `"strings"` to imports if missing.

- [ ] Run: `cd middleware && go test -run "TestHandleCatalogFlagsProxies|TestHandleCatalogDetailMergesFlagsAndJob" ./...`

  Expected output: compile error — handlers undefined.

- [ ] Edit `middleware/catalog.go`. Add handlers:

```go
// handleCatalogFlags proxies GET /api/procula/catalog/flags unchanged.
func handleCatalogFlags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp, err := services.client.Get(proculaURL + "/api/procula/catalog/flags")
	if err != nil {
		httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body) //nolint:errcheck
}

// handleCatalogDetail returns {path, flags, job} for a specific media path.
// It fetches the flag row and the newest matching job from procula.
func handleCatalogDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		httputil.WriteError(w, "path required", http.StatusBadRequest)
		return
	}

	type flagsWrap struct {
		Rows []map[string]any `json:"rows"`
	}
	var fw flagsWrap
	if resp, err := services.client.Get(proculaURL + "/api/procula/catalog/flags"); err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		_ = json.Unmarshal(body, &fw)
	}

	var flags []map[string]any
	for _, row := range fw.Rows {
		if p, _ := row["path"].(string); p == path {
			if f, ok := row["flags"].([]any); ok {
				for _, item := range f {
					if m, ok := item.(map[string]any); ok {
						flags = append(flags, m)
					}
				}
			}
			break
		}
	}

	var matched map[string]any
	if resp, err := services.client.Get(proculaURL + "/api/procula/jobs"); err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var all []map[string]any
		_ = json.Unmarshal(body, &all)
		for _, j := range all {
			src, _ := j["source"].(map[string]any)
			if src == nil {
				continue
			}
			if p, _ := src["path"].(string); p == path {
				matched = j // latest by creation order (procula returns ASC)
			}
		}
	}

	httputil.WriteJSON(w, map[string]any{
		"path":  path,
		"flags": flags,
		"job":   matched,
	})
}
```

- [ ] Edit `middleware/main.go` route registrations — add after the existing catalog routes:

```go
	mux.Handle("/api/pelicula/catalog/flags", auth.Guard(http.HandlerFunc(handleCatalogFlags)))
	mux.Handle("/api/pelicula/catalog/detail", auth.Guard(http.HandlerFunc(handleCatalogDetail)))
```

- [ ] Run: `cd middleware && go test ./...`

  Expected output: full suite `PASS`.

- [ ] Commit: `feat(middleware): catalog flag + detail endpoints`.

### Task 1.7 — middleware: `GET /api/pelicula/jobs` aggregated jobs endpoint

- [ ] Add failing test `middleware/jobs_test.go` (**new file**):

```go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleJobsListGroupsByState(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/procula/jobs" {
			t.Errorf("upstream path = %q", r.URL.Path)
		}
		w.Write([]byte(`[
			{"id":"a","state":"queued","stage":"validate","progress":0,"source":{"title":"A","path":"/movies/A/A.mkv"},"action_type":"pipeline"},
			{"id":"b","state":"processing","stage":"process","progress":0.5,"source":{"title":"B","path":"/movies/B/B.mkv"},"action_type":"pipeline"},
			{"id":"c","state":"failed","stage":"validate","progress":0,"error":"boom","source":{"title":"C","path":"/movies/C/C.mkv"},"action_type":"pipeline"},
			{"id":"d","state":"completed","stage":"done","progress":1,"source":{"title":"D","path":"/movies/D/D.mkv"},"action_type":"subtitle_request"}
		]`))
	}))
	defer upstream.Close()

	orig := proculaURL
	proculaURL = upstream.URL
	services = &ServiceClients{}
	services.client = &http.Client{}
	t.Cleanup(func() { proculaURL = orig })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/jobs", nil)
	w := httptest.NewRecorder()
	handleJobsList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Groups map[string][]map[string]any `json:"groups"`
		Total  int                         `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Total != 4 {
		t.Errorf("total = %d, want 4", resp.Total)
	}
	if len(resp.Groups["queued"]) != 1 || len(resp.Groups["processing"]) != 1 ||
		len(resp.Groups["failed"]) != 1 || len(resp.Groups["completed"]) != 1 {
		t.Errorf("groups = %+v", resp.Groups)
	}
}
```

- [ ] Run: `cd middleware && go test -run TestHandleJobsList ./...`

  Expected output: compile error — `handleJobsList` undefined.

- [ ] Create `middleware/jobs.go`:

```go
// jobs.go — /api/pelicula/jobs: a flat, grouped view of every procula job
// across every action type. Used by the dashboard Jobs tab.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"pelicula-api/httputil"
)

// handleJobsList fetches /api/procula/jobs and groups rows by state.
// The response shape is {groups: {queued: [...], processing: [...], ...}, total}.
func handleJobsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp, err := services.client.Get(proculaURL + "/api/procula/jobs")
	if err != nil {
		httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var all []map[string]any
	if err := json.Unmarshal(body, &all); err != nil {
		httputil.WriteError(w, "invalid procula response: "+err.Error(), http.StatusBadGateway)
		return
	}
	groups := map[string][]map[string]any{
		"queued":     {},
		"processing": {},
		"completed":  {},
		"failed":     {},
		"cancelled":  {},
	}
	for _, j := range all {
		state, _ := j["state"].(string)
		if _, ok := groups[state]; !ok {
			continue
		}
		groups[state] = append(groups[state], j)
	}
	httputil.WriteJSON(w, map[string]any{
		"groups": groups,
		"total":  len(all),
	})
}
```

- [ ] Register in `middleware/main.go`:

```go
	mux.Handle("/api/pelicula/jobs", auth.Guard(http.HandlerFunc(handleJobsList)))
```

- [ ] Run: `cd middleware && go test ./...`

  Expected output: `PASS`.

- [ ] Commit: `feat(middleware): /api/pelicula/jobs grouped view`.

### Task 1.8 — frontend: catalog "Needs Attention" section

- [ ] Edit `nginx/index.html`. Inside `#catalog-section`, directly after the `<div class="cat-controls">` block, insert:

```html
                <!-- Needs Attention (flagged items) -->
                <details class="cat-attention" id="cat-attention" open>
                    <summary class="cat-attention-header">
                        <span class="cat-attention-title">Needs Attention</span>
                        <span class="cat-attention-count" id="cat-attention-count">0</span>
                    </summary>
                    <div id="cat-attention-list" class="cat-attention-list"></div>
                </details>
```

- [ ] Edit `nginx/catalog.css`. Append:

```css
.cat-attention {
    margin: 0.5rem 0 1rem 0;
    border: 1px solid rgba(240, 96, 168, 0.35);
    border-radius: 8px;
    background: rgba(240, 96, 168, 0.06);
}
.cat-attention-header {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.5rem 0.75rem;
    font-weight: 600;
    cursor: pointer;
    list-style: none;
}
.cat-attention-header::-webkit-details-marker { display: none; }
.cat-attention-count {
    display: inline-block;
    min-width: 1.25rem;
    padding: 0 0.4rem;
    border-radius: 999px;
    background: rgba(240, 96, 168, 0.7);
    color: #fff;
    font-size: 0.75rem;
    text-align: center;
}
.cat-attention-list { padding: 0 0.25rem 0.5rem; }
.cat-attention-row {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.4rem 0.5rem;
    border-top: 1px solid rgba(255, 255, 255, 0.05);
    cursor: pointer;
}
.cat-attention-row:hover { background: rgba(255, 255, 255, 0.04); }
.cat-pill {
    display: inline-block;
    padding: 0.15rem 0.5rem;
    border-radius: 999px;
    font-size: 0.72rem;
    margin-right: 0.25rem;
    background: rgba(255, 255, 255, 0.08);
    color: var(--text);
}
.cat-pill-error   { background: rgba(240, 96, 168, 0.8); color: #fff; }
.cat-pill-warn    { background: rgba(255, 178, 102, 0.8); color: #000; }
.cat-pill-info    { background: rgba(130, 180, 255, 0.6); color: #000; }
.cat-pill-encoding { background: rgba(120, 200, 160, 0.6); color: #000; }
.cat-pill-subs    { background: rgba(200, 160, 255, 0.6); color: #000; }
.cat-pill-status-pass { background: rgba(130, 220, 140, 0.7); color: #000; }
.cat-pill-status-fail { background: rgba(240, 96, 168, 0.85); color: #fff; }
```

- [ ] Edit `nginx/catalog.js`. Extend `catState` with flag storage:

```js
const catState = {
    items: [],
    query: '',
    type: '',
    loaded: false,
    loading: false,
    registry: null,
    registryExpires: 0,
    flagsByPath: {},
    flaggedRows: [],
};
```

Add below `loadCatalog`:

```js
async function loadFlags() {
    try {
        const res = await catFetch('/api/pelicula/catalog/flags');
        if (!res.ok) return;
        const data = await res.json();
        const rows = Array.isArray(data.rows) ? data.rows : [];
        const byPath = {};
        for (const r of rows) byPath[r.path] = r;
        catState.flagsByPath = byPath;
        catState.flaggedRows = rows;
        renderAttention();
    } catch (e) {
        console.warn('[catalog] flag fetch failed', e);
    }
}

function renderAttention() {
    const wrap = document.getElementById('cat-attention');
    const list = document.getElementById('cat-attention-list');
    const count = document.getElementById('cat-attention-count');
    if (!wrap || !list) return;
    // Only error-severity rows promote to the attention section.
    const rows = catState.flaggedRows.filter(r => r.severity === 'error');
    if (!rows.length) {
        wrap.style.display = 'none';
        return;
    }
    wrap.style.display = '';
    if (count) count.textContent = String(rows.length);
    const frag = document.createDocumentFragment();
    for (const row of rows) {
        frag.appendChild(renderAttentionRow(row));
    }
    list.replaceChildren(frag);
}

function renderAttentionRow(row) {
    const div = document.createElement('div');
    div.className = 'cat-attention-row';
    div.addEventListener('click', () => openDetail(row.path));

    const title = document.createElement('span');
    title.className = 'cat-row-title';
    title.textContent = row.path.split('/').slice(-1)[0] || row.path;
    title.title = row.path;

    const pills = document.createElement('span');
    for (const f of (row.flags || [])) {
        const pill = document.createElement('span');
        pill.className = 'cat-pill cat-pill-' + (f.severity || 'info');
        pill.textContent = f.code;
        if (f.detail) pill.title = f.detail;
        pills.appendChild(pill);
    }

    div.appendChild(title);
    div.appendChild(pills);
    return div;
}
```

Call `loadFlags()` inside `initCatalog()`:

```js
function initCatalog() {
    if (catState.loaded || catState.loading) return;
    loadCatalog();
    loadFlags();
    loadActionRegistry();
}
```

- [ ] Run the existing Go tests to confirm no regression: `cd middleware && go test ./...`.

  Expected output: `PASS` (unchanged).

- [ ] Commit: `feat(frontend): catalog Needs Attention section`.

### Task 1.9 — frontend: catalog detail drawer with pills

- [ ] Edit `nginx/index.html`. Right before `</body>` add:

```html
    <!-- Catalog detail drawer -->
    <div class="drawer-backdrop hidden" id="cat-drawer-backdrop" onclick="catCloseDetail()"></div>
    <div class="drawer hidden" id="cat-drawer">
        <div class="drawer-header">
            <div>
                <div class="drawer-title" id="cat-drawer-title">Details</div>
                <div class="drawer-sub" id="cat-drawer-sub"></div>
            </div>
            <button class="drawer-close" onclick="catCloseDetail()">&times;</button>
        </div>
        <div class="drawer-body" id="cat-drawer-body"></div>
    </div>
```

- [ ] Edit `nginx/catalog.js`. Append the detail drawer module:

```js
// ── Detail drawer ────────────────────────────────────────────────────────────
async function openDetail(path) {
    if (!path) return;
    const backdrop = document.getElementById('cat-drawer-backdrop');
    const drawer = document.getElementById('cat-drawer');
    const title = document.getElementById('cat-drawer-title');
    const sub = document.getElementById('cat-drawer-sub');
    const body = document.getElementById('cat-drawer-body');
    if (!drawer) return;
    backdrop.classList.remove('hidden');
    drawer.classList.remove('hidden');
    title.textContent = path.split('/').slice(-1)[0] || 'Details';
    sub.textContent = path;
    body.replaceChildren(makeTextNode('Loading\u2026', 'var(--muted)'));

    try {
        const res = await catFetch('/api/pelicula/catalog/detail?path=' + encodeURIComponent(path));
        if (!res.ok) throw new Error('HTTP ' + res.status);
        const data = await res.json();
        body.replaceChildren(renderDetail(data));
    } catch (e) {
        body.replaceChildren(makeTextNode('Failed to load details: ' + e.message, 'var(--danger)'));
    }
}

window.catCloseDetail = function () {
    document.getElementById('cat-drawer-backdrop').classList.add('hidden');
    document.getElementById('cat-drawer').classList.add('hidden');
};

function makeTextNode(text, color) {
    const span = document.createElement('div');
    span.style.color = color || 'var(--text)';
    span.style.padding = '1rem 0';
    span.textContent = text;
    return span;
}

function renderDetail(data) {
    const root = document.createElement('div');

    // Section: Flags
    if (Array.isArray(data.flags) && data.flags.length) {
        root.appendChild(sectionTitle('Flags'));
        const wrap = document.createElement('div');
        for (const f of data.flags) {
            const pill = document.createElement('span');
            pill.className = 'cat-pill cat-pill-' + (f.severity || 'info');
            pill.textContent = f.code;
            if (f.detail) pill.title = f.detail;
            wrap.appendChild(pill);
        }
        root.appendChild(wrap);
    }

    const job = data.job || {};
    const src = job.source || {};
    const val = job.validation || null;
    const codecs = val && val.checks && val.checks.codecs;

    // Section: Encoding
    root.appendChild(sectionTitle('Encoding'));
    const enc = document.createElement('div');
    if (codecs) {
        enc.appendChild(pill('video: ' + (codecs.video || '?'), 'cat-pill-encoding'));
        enc.appendChild(pill('audio: ' + (codecs.audio || '?'), 'cat-pill-encoding'));
        if (codecs.width && codecs.height) {
            enc.appendChild(pill(codecs.width + 'x' + codecs.height, 'cat-pill-encoding'));
        }
    } else {
        enc.appendChild(makeTextNode('No codec info yet.', 'var(--muted)'));
    }
    root.appendChild(enc);

    // Section: Subtitles
    root.appendChild(sectionTitle('Subtitles'));
    const subs = document.createElement('div');
    const embedded = codecs && Array.isArray(codecs.subtitles) ? codecs.subtitles : [];
    if (embedded.length) {
        for (const lang of embedded) subs.appendChild(pill(lang, 'cat-pill-subs'));
    } else {
        subs.appendChild(makeTextNode('No embedded subtitle tracks.', 'var(--muted)'));
    }
    const missing = Array.isArray(job.missing_subs) ? job.missing_subs : [];
    if (missing.length) {
        const label = document.createElement('div');
        label.style.marginTop = '0.5rem';
        label.style.color = 'var(--muted)';
        label.textContent = 'Missing:';
        subs.appendChild(label);
        for (const lang of missing) subs.appendChild(pill(lang, 'cat-pill-warn'));
    }
    root.appendChild(subs);

    // Section: Status
    root.appendChild(sectionTitle('Status'));
    const status = document.createElement('div');
    if (val && val.checks) {
        for (const k of ['integrity', 'duration', 'sample']) {
            const v = val.checks[k] || 'skip';
            const cls = v === 'pass' ? 'cat-pill-status-pass'
                : v === 'fail' ? 'cat-pill-status-fail'
                : 'cat-pill';
            status.appendChild(pill(k + ': ' + v, cls));
        }
    }
    if (job.transcode_decision) status.appendChild(pill('transcode: ' + job.transcode_decision, 'cat-pill'));
    if (job.catalog && job.catalog.jellyfin_synced) status.appendChild(pill('jellyfin synced', 'cat-pill-status-pass'));
    if (job.error) {
        const err = document.createElement('div');
        err.className = 'drawer-error';
        err.textContent = job.error;
        status.appendChild(err);
    }
    root.appendChild(status);

    return root;
}

function sectionTitle(text) {
    const div = document.createElement('div');
    div.className = 'drawer-section-title';
    div.textContent = text;
    return div;
}

function pill(text, cls) {
    const span = document.createElement('span');
    span.className = 'cat-pill ' + (cls || '');
    span.textContent = text;
    return span;
}
```

Wire the existing movie row to open the drawer. Update `renderMovieRow` so clicking outside the `⋯` button opens the detail drawer for the movie's file path:

```js
function renderMovieRow(item) {
    const div = document.createElement('div');
    div.className = 'cat-row cat-row-movie';
    div.dataset.id = item.id;
    div.addEventListener('click', (e) => {
        if (e.target.closest('.cat-ctx-btn')) return;
        const path = item.movieFile ? item.movieFile.path : '';
        if (path) openDetail(path);
    });
    // …existing title/meta/action code unchanged…
```

Keep the existing body of the function otherwise.

- [ ] Run the stack locally (`./pelicula up`) and smoke test by clicking a movie row → drawer opens and shows pills.

  Expected: the drawer appears with encoding/subtitles/status sections. Detail drawer closes on backdrop click.

- [ ] Commit: `feat(frontend): catalog item detail drawer with pills`.

### Task 1.10 — frontend: Jobs tab

- [ ] Edit `nginx/index.html`. Add the tab button:

```html
        <button class="tab" data-tab="jobs" onclick="switchTab('jobs')">jobs</button>
```

Insert directly after the `catalog` tab button, and add the section before `#storage-section`:

```html
            <!-- Jobs -->
            <div class="section hidden" id="jobs-section">
                <div class="um-hero">
                    <div class="um-hero-title-row">
                        <span class="um-hero-icon">&#128230;</span>
                        <h2 class="um-hero-title">Jobs</h2>
                        <button class="section-action" onclick="jobsRefresh()">&#8635;</button>
                    </div>
                    <p class="um-hero-sub">All pipeline and action jobs, every state</p>
                </div>
                <div id="jobs-groups"></div>
            </div>
```

- [ ] Edit `nginx/styles.css`. Add to the two `body[data-tab]` display rules:

```css
body[data-tab] #jobs-section,
```

goes into the hide list, and:

```css
body[data-tab="jobs"] #jobs-section,
```

goes into the show list (matching the existing pattern on lines 178-203).

- [ ] Create `nginx/jobs.js`:

```js
// jobs.js — Jobs tab: every procula job grouped by state
(function () {
'use strict';

const jobsState = { loaded: false, loading: false };

function jobsFetch(url) {
    return fetch(url, { credentials: 'same-origin' });
}

async function loadJobs() {
    if (jobsState.loading) return;
    jobsState.loading = true;
    const root = document.getElementById('jobs-groups');
    if (!root) { jobsState.loading = false; return; }
    root.replaceChildren(makeMsg('Loading\u2026'));
    try {
        const res = await jobsFetch('/api/pelicula/jobs');
        if (!res.ok) throw new Error('HTTP ' + res.status);
        const data = await res.json();
        renderJobs(root, data.groups || {});
        jobsState.loaded = true;
    } catch (e) {
        root.replaceChildren(makeMsg('Failed to load jobs: ' + e.message, true));
    } finally {
        jobsState.loading = false;
    }
}

function renderJobs(root, groups) {
    const order = ['processing', 'queued', 'failed', 'cancelled', 'completed'];
    const frag = document.createDocumentFragment();
    for (const state of order) {
        const jobs = groups[state] || [];
        if (!jobs.length) continue;
        frag.appendChild(renderGroup(state, jobs));
    }
    if (!frag.childNodes.length) {
        frag.appendChild(makeMsg('No jobs.'));
    }
    root.replaceChildren(frag);
}

function renderGroup(state, jobs) {
    const wrap = document.createElement('details');
    wrap.className = 'jobs-group jobs-group-' + state;
    wrap.open = (state === 'processing' || state === 'failed');

    const summary = document.createElement('summary');
    summary.className = 'jobs-group-header';
    summary.textContent = state + ' (' + jobs.length + ')';
    wrap.appendChild(summary);

    for (const j of jobs) {
        wrap.appendChild(renderJobRow(j));
    }
    return wrap;
}

function renderJobRow(j) {
    const row = document.createElement('div');
    row.className = 'jobs-row';
    row.dataset.id = j.id;

    const title = document.createElement('div');
    title.className = 'jobs-row-title';
    const src = j.source || {};
    title.textContent = src.title || src.path || j.id;

    const meta = document.createElement('div');
    meta.className = 'jobs-row-meta';
    const parts = [];
    if (j.stage) parts.push('stage: ' + j.stage);
    if (j.action_type && j.action_type !== 'pipeline') parts.push(j.action_type);
    if (typeof j.progress === 'number') parts.push(Math.round(j.progress * 100) + '%');
    if (j.error) parts.push('error: ' + j.error);
    meta.textContent = parts.join(' \u00b7 ');

    row.appendChild(title);
    row.appendChild(meta);
    return row;
}

function makeMsg(text, isError) {
    const div = document.createElement('div');
    div.className = 'no-items';
    div.style.color = isError ? 'var(--danger)' : 'var(--muted)';
    div.textContent = text;
    return div;
}

window.jobsRefresh = function () { jobsState.loaded = false; loadJobs(); };

document.addEventListener('pelicula:tab-changed', function (e) {
    if (e.detail && e.detail.tab === 'jobs' && !jobsState.loaded) loadJobs();
});

if (document.body && document.body.dataset.tab === 'jobs') loadJobs();

})();
```

- [ ] Edit `nginx/index.html`. Add the script tag near the existing `catalog.js`:

```html
    <script src="/jobs.js" defer></script>
```

- [ ] Append to `nginx/catalog.css`:

```css
.jobs-group { margin-bottom: 0.75rem; border: 1px solid rgba(255,255,255,0.06); border-radius: 8px; }
.jobs-group-header { padding: 0.5rem 0.75rem; cursor: pointer; font-weight: 600; }
.jobs-row { padding: 0.4rem 0.75rem; border-top: 1px solid rgba(255,255,255,0.04); }
.jobs-row-title { font-size: 0.85rem; }
.jobs-row-meta  { font-size: 0.72rem; color: var(--muted); }
```

- [ ] Smoke test by loading the dashboard and switching to Jobs tab.

  Expected: processing / queued / failed / completed groups, each expandable; rows show title, stage, progress.

- [ ] Commit: `feat(frontend): Jobs tab`.

---

## Phase 2 — Actions

### Task 2.1 — procula: extend `bazarrSearchSubtitles` with options

- [ ] Add failing test to `procula/bazarr_test.go`:

```go
func TestBazarrSearchSubtitlesWithOpts(t *testing.T) {
	var got url.Values
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s", r.Method)
		}
		r.ParseForm()
		got = r.PostForm
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	orig := bazarrURL
	bazarrURL = ts.URL
	t.Cleanup(func() { bazarrURL = orig })

	// Write a fake Bazarr config.yaml so readBazarrAPIKey succeeds.
	cfgDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cfgDir, "bazarr/config"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "bazarr/config/config.yaml"),
		[]byte("auth:\n  apikey: testkey\n"), 0644); err != nil {
		t.Fatal(err)
	}

	job := &Job{
		Source: JobSource{ArrType: "radarr", ArrID: 42, Title: "T"},
	}
	opts := BazarrSearchOpts{Languages: []string{"es"}, HI: true, Forced: false}
	bazarrSearchSubtitlesWithOpts(context.Background(), cfgDir, job, opts)

	if got.Get("language") != "es" {
		t.Errorf("language = %q, want es", got.Get("language"))
	}
	if got.Get("hi") != "True" {
		t.Errorf("hi = %q, want True", got.Get("hi"))
	}
	if got.Get("forced") != "False" {
		t.Errorf("forced = %q, want False", got.Get("forced"))
	}
}
```

Imports need `"context"`, `"net/http"`, `"net/http/httptest"`, `"net/url"`, `"os"`, `"path/filepath"`.

- [ ] Run: `cd procula && go test -run TestBazarrSearchSubtitlesWithOpts ./...`

  Expected output: compile error — `BazarrSearchOpts` and `bazarrSearchSubtitlesWithOpts` undefined.

- [ ] Edit `procula/bazarr.go`. Add the options type and the extended function, then rewrite the existing function to delegate:

```go
// BazarrSearchOpts parameterises subtitle search calls.
type BazarrSearchOpts struct {
	Languages []string // required; explicit per-request language list
	HI        bool     // true → request hearing-impaired variant
	Forced    bool     // true → request forced-only variant
}

// bazarrSearchSubtitlesWithOpts issues one PATCH per language with the given
// hi/forced flags. Empty Languages is a no-op.
func bazarrSearchSubtitlesWithOpts(ctx context.Context, configDir string, job *Job, opts BazarrSearchOpts) {
	apiKey, err := readBazarrAPIKey(configDir)
	if err != nil {
		slog.Warn("bazarr: cannot read API key, skipping subtitle search", "component", "bazarr", "error", err)
		return
	}
	if len(opts.Languages) == 0 {
		slog.Warn("bazarr: no languages to request, skipping", "component", "bazarr", "job_id", job.ID)
		return
	}

	var path string
	base := url.Values{}
	switch job.Source.ArrType {
	case "radarr":
		path = "/api/movies/subtitles"
		base.Set("radarrid", strconv.Itoa(job.Source.ArrID))
	case "sonarr":
		if job.Source.EpisodeID == 0 {
			slog.Warn("bazarr: episode ID not available, skipping subtitle search", "component", "bazarr", "job_id", job.ID)
			return
		}
		path = "/api/episodes/subtitles"
		base.Set("seriesid", strconv.Itoa(job.Source.ArrID))
		base.Set("episodeid", strconv.Itoa(job.Source.EpisodeID))
	default:
		slog.Warn("bazarr: unknown arr_type, skipping subtitle search", "component", "bazarr", "arr_type", job.Source.ArrType)
		return
	}

	hiStr := "False"
	if opts.HI {
		hiStr = "True"
	}
	forcedStr := "False"
	if opts.Forced {
		forcedStr = "True"
	}

	for _, code := range opts.Languages {
		form := url.Values{}
		for k, v := range base {
			form[k] = v
		}
		form.Set("language", code)
		form.Set("hi", hiStr)
		form.Set("forced", forcedStr)

		reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodPatch, bazarrURL+path, strings.NewReader(form.Encode()))
		if err != nil {
			cancel()
			slog.Warn("bazarr: build request failed", "component", "bazarr", "lang", code, "error", err)
			continue
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-API-KEY", apiKey)

		resp, err := http.DefaultClient.Do(req)
		cancel()
		if err != nil {
			slog.Warn("bazarr: request failed", "component", "bazarr", "lang", code, "error", err)
			continue
		}
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			slog.Warn("bazarr: search returned error", "component", "bazarr", "lang", code, "status", resp.StatusCode, "body", string(body))
			continue
		}
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
		slog.Info("bazarr: subtitle search triggered",
			"component", "bazarr", "arr_type", job.Source.ArrType, "job_id", job.ID,
			"lang", code, "hi", opts.HI, "forced", opts.Forced)
	}
}

// bazarrSearchSubtitles preserves the legacy signature: it falls back to
// job.MissingSubs or PELICULA_SUB_LANGS and uses hi=false, forced=false.
func bazarrSearchSubtitles(ctx context.Context, configDir string, job *Job) {
	langs := job.MissingSubs
	if len(langs) == 0 {
		for _, c := range strings.Split(os.Getenv("PELICULA_SUB_LANGS"), ",") {
			if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
				langs = append(langs, c)
			}
		}
	}
	bazarrSearchSubtitlesWithOpts(ctx, configDir, job, BazarrSearchOpts{Languages: langs})
}
```

- [ ] Run: `cd procula && go test -run TestBazarrSearchSubtitlesWithOpts ./...`

  Expected output: `PASS`.

- [ ] Run the full procula suite: `cd procula && go test ./...`

  Expected output: `PASS`.

- [ ] Commit: `refactor(procula): bazarrSearchSubtitles accepts HI/forced options`.

### Task 2.2 — procula: `subtitle_request` action

- [ ] Add failing test to `procula/actions_test.go` (**new file if not present**):

```go
package main

import (
	"context"
	"testing"
)

func TestSubtitleRequestActionValidatesParams(t *testing.T) {
	job := &Job{
		ID:     "job_test",
		Params: map[string]any{},
	}
	_, err := runSubtitleRequestAction(context.Background(), nil, job)
	if err == nil {
		t.Fatalf("expected error for missing params")
	}
}

func TestSubtitleRequestActionRegistered(t *testing.T) {
	registerBuiltinActions()
	def := Lookup("subtitle_request")
	if def == nil {
		t.Fatalf("subtitle_request not registered")
	}
	if !def.Sync {
		t.Errorf("subtitle_request should be sync")
	}
}
```

- [ ] Run: `cd procula && go test -run TestSubtitleRequest ./...`

  Expected output: compile error — `runSubtitleRequestAction` undefined.

- [ ] Edit `procula/actions.go`. Add to `registerBuiltinActions`:

```go
	Register(&ActionDef{
		Name:        "subtitle_request",
		Label:       "Request subtitles",
		AppliesTo:   []string{"movie", "episode"},
		Sync:        true,
		Description: "Queue a Bazarr subtitle search with explicit languages, HI, and forced flags.",
		Handler:     runSubtitleRequestAction,
	})
```

And add the handler:

```go
// runSubtitleRequestAction dispatches a targeted Bazarr search. Params:
//   languages: []string (required, at least one ISO 639-1 code)
//   hi:        bool (default false)
//   forced:    bool (default false)
//   arr_type:  "radarr" | "sonarr" (required)
//   arr_id:    int (required)
//   episode_id: int (required for sonarr)
func runSubtitleRequestAction(ctx context.Context, q *Queue, job *Job) (map[string]any, error) {
	arrType, _ := job.Params["arr_type"].(string)
	arrIDf, _ := job.Params["arr_id"].(float64)
	epIDf, _ := job.Params["episode_id"].(float64)
	if arrType == "" || arrIDf == 0 {
		return nil, fmt.Errorf("subtitle_request: arr_type and arr_id required")
	}

	var langs []string
	if raw, ok := job.Params["languages"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				langs = append(langs, s)
			}
		}
	}
	if len(langs) == 0 {
		return nil, fmt.Errorf("subtitle_request: languages required")
	}

	hi, _ := job.Params["hi"].(bool)
	forced, _ := job.Params["forced"].(bool)

	synthetic := &Job{
		ID: "action-" + job.ID,
		Source: JobSource{
			ArrType:   arrType,
			ArrID:     int(arrIDf),
			EpisodeID: int(epIDf),
		},
	}
	opts := BazarrSearchOpts{Languages: langs, HI: hi, Forced: forced}
	bazarrSearchSubtitlesWithOpts(ctx, configDir, synthetic, opts)
	return map[string]any{
		"triggered": true,
		"languages": langs,
		"hi":        hi,
		"forced":    forced,
	}, nil
}
```

- [ ] Run: `cd procula && go test -run TestSubtitleRequest ./...`

  Expected output: `PASS`.

- [ ] Run the full suite: `cd procula && go test ./...`

  Expected output: `PASS`.

- [ ] Commit: `feat(procula): subtitle_request action with explicit options`.

### Task 2.3 — frontend: Subtitle Request dialog + right-click context menu

- [ ] Edit `nginx/index.html`. Before `</body>`, add the dialog markup:

```html
    <!-- Subtitle request dialog -->
    <div class="drawer-backdrop hidden" id="sub-req-backdrop" onclick="subReqClose()"></div>
    <div class="drawer hidden" id="sub-req-dialog" role="dialog" aria-label="Request subtitles">
        <div class="drawer-header">
            <div>
                <div class="drawer-title">Request subtitles</div>
                <div class="drawer-sub" id="sub-req-sub"></div>
            </div>
            <button class="drawer-close" onclick="subReqClose()">&times;</button>
        </div>
        <div class="drawer-body">
            <div class="drawer-section-title">Languages</div>
            <div id="sub-req-langs" class="sub-req-langs"></div>
            <div class="drawer-section-title">Options</div>
            <label><input type="checkbox" id="sub-req-hi"> Hearing impaired</label>
            <label style="margin-left:1rem"><input type="checkbox" id="sub-req-forced"> Forced only</label>
            <div class="modal-buttons" style="margin-top:1rem">
                <button class="modal-btn-cancel" onclick="subReqClose()">Cancel</button>
                <button class="modal-btn-confirm" onclick="subReqSubmit()">Queue request</button>
            </div>
            <div id="sub-req-status" class="no-items"></div>
        </div>
    </div>
```

- [ ] Append to `nginx/catalog.css`:

```css
.sub-req-langs { display: flex; flex-wrap: wrap; gap: 0.35rem; margin-bottom: 0.75rem; }
.sub-req-lang {
    padding: 0.25rem 0.6rem;
    border-radius: 999px;
    background: rgba(255,255,255,0.06);
    cursor: pointer;
    user-select: none;
}
.sub-req-lang.active { background: rgba(200,160,255,0.75); color: #000; }
```

- [ ] Edit `nginx/catalog.js`. Add a right-click hook in `renderMovieRow` and `renderEpisodeRow` that opens the subtitle request dialog (right-click is the operator shortcut; the existing `⋯` menu still works). In `renderMovieRow`:

```js
    div.addEventListener('contextmenu', (e) => {
        e.preventDefault();
        openSubRequest({
            label: item.title || 'Movie',
            arrType: 'radarr',
            arrID: item.id,
            episodeID: 0,
        });
    });
```

In `renderEpisodeRow`:

```js
    div.addEventListener('contextmenu', (e) => {
        if (!hasFile) return;
        e.preventDefault();
        openSubRequest({
            label: series.title + ' ' + epNum,
            arrType: 'sonarr',
            arrID: series.id,
            episodeID: ep.id,
        });
    });
```

Append the dialog module:

```js
// ── Subtitle request dialog ──────────────────────────────────────────────────
const _subReqState = { target: null, selected: new Set() };
const SUB_REQ_DEFAULT_LANGS = ['en', 'es', 'fr', 'de', 'pt', 'it', 'ja', 'zh'];

function openSubRequest(target) {
    _subReqState.target = target;
    _subReqState.selected = new Set();

    // Pre-select from PELICULA_SUB_LANGS via the /settings endpoint if available.
    (async () => {
        try {
            const res = await catFetch('/api/pelicula/settings');
            if (res.ok) {
                const s = await res.json();
                const configured = (s.sub_langs || '').split(',').map(x => x.trim().toLowerCase()).filter(Boolean);
                for (const c of configured) _subReqState.selected.add(c);
            }
        } catch (e) { /* non-critical */ }
        renderSubReqLangs();
    })();

    document.getElementById('sub-req-sub').textContent = target.label;
    document.getElementById('sub-req-hi').checked = false;
    document.getElementById('sub-req-forced').checked = false;
    document.getElementById('sub-req-status').textContent = '';
    renderSubReqLangs();

    document.getElementById('sub-req-backdrop').classList.remove('hidden');
    document.getElementById('sub-req-dialog').classList.remove('hidden');
}

function renderSubReqLangs() {
    const wrap = document.getElementById('sub-req-langs');
    if (!wrap) return;
    const merged = new Set([...SUB_REQ_DEFAULT_LANGS, ..._subReqState.selected]);
    const frag = document.createDocumentFragment();
    for (const code of merged) {
        const el = document.createElement('span');
        el.className = 'sub-req-lang' + (_subReqState.selected.has(code) ? ' active' : '');
        el.textContent = code;
        el.addEventListener('click', () => {
            if (_subReqState.selected.has(code)) _subReqState.selected.delete(code);
            else _subReqState.selected.add(code);
            renderSubReqLangs();
        });
        frag.appendChild(el);
    }
    wrap.replaceChildren(frag);
}

window.subReqClose = function () {
    document.getElementById('sub-req-backdrop').classList.add('hidden');
    document.getElementById('sub-req-dialog').classList.add('hidden');
};

window.subReqSubmit = async function () {
    const t = _subReqState.target;
    if (!t) return;
    const langs = Array.from(_subReqState.selected);
    if (!langs.length) {
        document.getElementById('sub-req-status').textContent = 'Select at least one language.';
        return;
    }
    const body = JSON.stringify({
        action: 'subtitle_request',
        target: { arr_type: t.arrType, arr_id: t.arrID, episode_id: t.episodeID || 0 },
        params: {
            languages: langs,
            hi: document.getElementById('sub-req-hi').checked,
            forced: document.getElementById('sub-req-forced').checked,
            arr_type: t.arrType,
            arr_id: t.arrID,
            episode_id: t.episodeID || 0,
        },
    });
    document.getElementById('sub-req-status').textContent = 'Queuing\u2026';
    try {
        const res = await catFetch('/api/pelicula/actions?wait=10', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body,
        });
        const data = await res.json();
        if (!res.ok) {
            document.getElementById('sub-req-status').textContent = 'Failed: ' + (data.error || res.status);
            return;
        }
        if (data.state === 'completed') {
            document.getElementById('sub-req-status').textContent = 'Queued for ' + langs.join(', ');
            setTimeout(subReqClose, 1200);
        } else {
            document.getElementById('sub-req-status').textContent = 'State: ' + data.state;
        }
    } catch (e) {
        document.getElementById('sub-req-status').textContent = 'Error: ' + e.message;
    }
};
```

- [ ] Smoke test: right-click on a movie row → dialog opens with default language pre-selection → toggle HI → Queue request → status shows "Queued for …".

  Expected: POST to `/api/pelicula/actions` returns `{state: "completed", result: {triggered: true, ...}}` and the dialog dismisses after ~1.2s.

- [ ] Commit: `feat(frontend): subtitle request dialog and right-click trigger`.

### Task 2.4 — freeze automation on flagged items

The vision says "Automation is paused for flagged items — operator acts manually". The pipeline already stops on validation failure (`pipeline.go:108`) and does not auto-retry. The places that currently run without asking are:

1. `maybeTranscode` — runs unconditionally after validate passes.
2. `bazarrSearchSubtitles` called from `awaitSubtitles` — triggered for every missing lang on every import.

Phase 1's flag model gives us a handle to pause (2) on items that already have an error-severity flag.

- [ ] Add failing test `TestAwaitSubsSkipsFlaggedJob` in `procula/await_subs_test.go`:

```go
func TestAwaitSubsSkipsFlaggedJob(t *testing.T) {
	called := false
	origTrigger := bazarrTrigger
	bazarrTrigger = func(ctx context.Context, cfgDir string, j *Job) { called = true }
	t.Cleanup(func() { bazarrTrigger = origTrigger })

	job := &Job{
		Source:      JobSource{Path: "/movies/X/X.mkv", ArrType: "radarr", ArrID: 1},
		MissingSubs: []string{"es"},
		Flags:       []Flag{{Code: "validation_failed", Severity: FlagSeverityError}},
	}
	awaitSubtitles(context.Background(), nil, job, Settings{AwaitSubsSeconds: 0}, t.TempDir())
	if called {
		t.Fatalf("bazarr trigger should be skipped when job is flagged error")
	}
}
```

This test names a package-level function pointer `bazarrTrigger` that doesn't exist yet. We introduce it in the implementation step.

- [ ] Run: `cd procula && go test -run TestAwaitSubsSkipsFlaggedJob ./...`

  Expected output: compile error — `bazarrTrigger` undefined.

- [ ] Edit `procula/await_subs.go`. At the top of the file add:

```go
// bazarrTrigger is the seam for tests to swap out the real Bazarr PATCH loop.
// Production code leaves this bound to bazarrSearchSubtitles.
var bazarrTrigger = bazarrSearchSubtitles
```

Replace the in-function call `bazarrSearchSubtitles(ctx, configDir, job)` with `bazarrTrigger(ctx, configDir, job)`.

Add the short-circuit at the top of `awaitSubtitles`:

```go
	// Automation is paused for jobs that already carry an error-severity flag.
	// The operator must clear the flag (or dismiss it) before Bazarr fires.
	for _, f := range job.Flags {
		if f.Severity == FlagSeverityError {
			slog.Info("await_subs: skipping flagged job", "component", "await_subs", "job_id", job.ID)
			return
		}
	}
```

- [ ] Run: `cd procula && go test -run TestAwaitSubsSkipsFlaggedJob ./...`

  Expected output: `PASS`.

- [ ] Commit: `feat(procula): pause auto Bazarr search on flagged jobs`.

---

## Phase 3 — Observability

### Task 3.1 — middleware: log aggregator endpoint

- [ ] Add failing test `middleware/logs_aggregate_test.go` (**new file**):

```go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleLogsAggregateFansOut(t *testing.T) {
	origFetch := dockerLogsFunc
	dockerLogsFunc = func(name string, tail int) ([]byte, error) {
		switch name {
		case "sonarr":
			return []byte("sonarr line 1\nsonarr line 2\n"), nil
		case "radarr":
			return []byte("radarr line 1\n"), nil
		}
		return []byte(""), nil
	}
	t.Cleanup(func() { dockerLogsFunc = origFetch })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/logs/aggregate?tail=50&services=sonarr,radarr", nil)
	w := httptest.NewRecorder()
	handleLogsAggregate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Entries []struct {
			Service string `json:"service"`
			Line    string `json:"line"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var services []string
	for _, e := range resp.Entries {
		services = append(services, e.Service)
	}
	joined := strings.Join(services, ",")
	if !strings.Contains(joined, "sonarr") || !strings.Contains(joined, "radarr") {
		t.Fatalf("entries missing services: %v", services)
	}
	if len(resp.Entries) != 3 {
		t.Errorf("entries = %d, want 3", len(resp.Entries))
	}
}
```

- [ ] Run: `cd middleware && go test -run TestHandleLogsAggregate ./...`

  Expected output: compile error — `handleLogsAggregate` and `dockerLogsFunc` undefined.

- [ ] Edit `middleware/docker.go`. Introduce a function variable so tests can swap the underlying call:

```go
// dockerLogsFunc is the seam used by handleLogsAggregate; tests replace it.
// Production leaves it bound to dockerLogs.
var dockerLogsFunc = dockerLogs
```

- [ ] Create `middleware/logs_aggregate.go`:

```go
// logs_aggregate.go — fan-out over docker-proxy container logs, returning a
// unified entry list the dashboard Logs tab can colour by service.
package main

import (
	"bufio"
	"bytes"
	"net/http"
	"pelicula-api/httputil"
	"strconv"
	"strings"
	"sync"
)

// LogEntry is one line tagged with its source service.
type LogEntry struct {
	Service string `json:"service"`
	Line    string `json:"line"`
}

const (
	logAggDefaultTail = 100
	logAggMaxTail     = 500
)

// handleLogsAggregate fetches logs from each requested service in parallel
// and returns {entries: [...], services: [...]}.
// Query params: ?tail=N (default 100, max 500), ?services=a,b,c (default: all allowed).
func handleLogsAggregate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	tail := logAggDefaultTail
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > logAggMaxTail {
				n = logAggMaxTail
			}
			tail = n
		}
	}

	var services []string
	if sv := r.URL.Query().Get("services"); sv != "" {
		for _, s := range strings.Split(sv, ",") {
			s = strings.TrimSpace(s)
			if isAllowedContainer(s) {
				services = append(services, s)
			}
		}
	} else {
		for name := range allowedContainers {
			services = append(services, name)
		}
	}

	type fetchResult struct {
		svc string
		raw []byte
		err error
	}
	resCh := make(chan fetchResult, len(services))
	var wg sync.WaitGroup
	for _, svc := range services {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			raw, err := dockerLogsFunc(name, tail)
			resCh <- fetchResult{svc: name, raw: raw, err: err}
		}(svc)
	}
	wg.Wait()
	close(resCh)

	var entries []LogEntry
	for r := range resCh {
		if r.err != nil {
			entries = append(entries, LogEntry{Service: r.svc, Line: "(logs unavailable: " + r.err.Error() + ")"})
			continue
		}
		sc := bufio.NewScanner(bytes.NewReader(r.raw))
		sc.Buffer(make([]byte, 256*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r\n")
			if line == "" {
				continue
			}
			entries = append(entries, LogEntry{Service: r.svc, Line: line})
		}
	}

	httputil.WriteJSON(w, map[string]any{
		"entries":  entries,
		"services": services,
	})
}
```

- [ ] Register route in `middleware/main.go`:

```go
	mux.Handle("/api/pelicula/logs/aggregate", auth.GuardAdmin(http.HandlerFunc(handleLogsAggregate)))
```

- [ ] Run: `cd middleware && go test ./...`

  Expected output: `PASS`.

- [ ] Commit: `feat(middleware): log aggregator endpoint`.

### Task 3.2 — frontend: Logs tab

- [ ] Edit `nginx/index.html`. Add the tab button after the Jobs tab:

```html
        <button class="tab admin-only" data-tab="logs" onclick="switchTab('logs')">logs</button>
```

Add the section before `#storage-section`:

```html
            <!-- Logs -->
            <div class="section hidden admin-only" id="logs-section">
                <div class="um-hero">
                    <div class="um-hero-title-row">
                        <span class="um-hero-icon">&#128220;</span>
                        <h2 class="um-hero-title">Logs</h2>
                        <button class="section-action" onclick="logsRefresh()">&#8635;</button>
                    </div>
                    <p class="um-hero-sub">Live log lines aggregated from every service</p>
                </div>
                <div class="logs-filters" id="logs-filters"></div>
                <pre id="logs-stream" class="logs-stream"></pre>
            </div>
```

Add the script tag:

```html
    <script src="/logs.js" defer></script>
```

- [ ] Update `nginx/styles.css` display rules — add `body[data-tab] #logs-section,` to the hide list and `body[data-tab="logs"] #logs-section,` to the show list.

- [ ] Append to `nginx/catalog.css`:

```css
.logs-filters { display: flex; flex-wrap: wrap; gap: 0.3rem; margin-bottom: 0.5rem; }
.logs-filter-chip {
    padding: 0.2rem 0.6rem; border-radius: 999px;
    background: rgba(255,255,255,0.06); cursor: pointer; font-size: 0.75rem;
}
.logs-filter-chip.active { background: rgba(130,180,255,0.75); color: #000; }
.logs-stream {
    max-height: 60vh; overflow: auto;
    background: #0c0e13; padding: 0.5rem; border-radius: 8px;
    font-family: ui-monospace, Menlo, monospace; font-size: 0.72rem;
    white-space: pre-wrap; word-break: break-word;
}
.logs-line { display: block; }
.logs-line-svc {
    display: inline-block; width: 10ch; margin-right: 0.6rem;
    color: var(--muted); text-align: right;
}
/* colour-by-service: deterministic mapping */
.logs-svc-sonarr      { color: #8ec7ff; }
.logs-svc-radarr      { color: #ffb27a; }
.logs-svc-prowlarr    { color: #c498ff; }
.logs-svc-qbittorrent { color: #7adf97; }
.logs-svc-jellyfin    { color: #ff8ac6; }
.logs-svc-bazarr      { color: #ffd96e; }
.logs-svc-gluetun     { color: #8affd9; }
.logs-svc-procula     { color: #ffa0a0; }
.logs-svc-nginx       { color: #ccc; }
.logs-svc-pelicula-api { color: #9ef0c6; }
```

- [ ] Create `nginx/logs.js`:

```js
// logs.js — Logs tab: aggregated container log stream, coloured by service.
(function () {
'use strict';

const ALL_SERVICES = [
    'pelicula-api', 'procula', 'nginx',
    'sonarr', 'radarr', 'prowlarr',
    'qbittorrent', 'jellyfin', 'bazarr', 'gluetun',
];

const logsState = {
    loaded: false,
    loading: false,
    enabled: new Set(ALL_SERVICES),
};

function lfetch(url) { return fetch(url, { credentials: 'same-origin' }); }

async function loadLogs() {
    if (logsState.loading) return;
    logsState.loading = true;
    const out = document.getElementById('logs-stream');
    if (!out) { logsState.loading = false; return; }
    out.textContent = 'Loading\u2026';
    const enabled = Array.from(logsState.enabled).join(',');
    try {
        const res = await lfetch('/api/pelicula/logs/aggregate?tail=200&services=' + encodeURIComponent(enabled));
        if (!res.ok) throw new Error('HTTP ' + res.status);
        const data = await res.json();
        renderLogs(out, data.entries || []);
        logsState.loaded = true;
    } catch (e) {
        out.textContent = 'Failed to load logs: ' + e.message;
    } finally {
        logsState.loading = false;
    }
}

function renderLogs(out, entries) {
    const frag = document.createDocumentFragment();
    for (const e of entries) {
        const row = document.createElement('span');
        row.className = 'logs-line logs-svc-' + e.service;
        const svc = document.createElement('span');
        svc.className = 'logs-line-svc';
        svc.textContent = e.service;
        row.appendChild(svc);
        row.appendChild(document.createTextNode(e.line + '\n'));
        frag.appendChild(row);
    }
    out.replaceChildren(frag);
    out.scrollTop = out.scrollHeight;
}

function renderFilters() {
    const wrap = document.getElementById('logs-filters');
    if (!wrap) return;
    const frag = document.createDocumentFragment();
    for (const svc of ALL_SERVICES) {
        const chip = document.createElement('span');
        chip.className = 'logs-filter-chip' + (logsState.enabled.has(svc) ? ' active' : '');
        chip.textContent = svc;
        chip.addEventListener('click', () => {
            if (logsState.enabled.has(svc)) logsState.enabled.delete(svc);
            else logsState.enabled.add(svc);
            renderFilters();
            loadLogs();
        });
        frag.appendChild(chip);
    }
    wrap.replaceChildren(frag);
}

window.logsRefresh = function () { loadLogs(); };

document.addEventListener('pelicula:tab-changed', function (e) {
    if (e.detail && e.detail.tab === 'logs') {
        renderFilters();
        loadLogs();
    }
});

})();
```

- [ ] Smoke test: open the dashboard, switch to Logs tab. Confirm all services render with distinct colours.

  Expected: each allowed service shows a filter chip; clicking a chip toggles the service; the `<pre>` shows interleaved log lines tagged with the service name.

- [ ] Commit: `feat(frontend): Logs tab with per-service filters and colours`.

### Task 3.3 — full test run and final commit

- [ ] Run both suites end to end:

```sh
cd procula && go test ./... && cd ../middleware && go test ./...
```

Expected output: two `ok` lines with no failures.

- [ ] Run the end-to-end integration test that actually stands up the stack:

```sh
./pelicula test
```

Expected output: green exit from `tests/e2e.sh`.

- [ ] If all passes, commit any remaining doc or config touch-ups as `chore: Catalog Control Surface — final cleanup`.

---

## Self-Review

**Spec coverage (every goal in the vision maps to a task):**

| Goal | Tasks |
| --- | --- |
| Flagged items rise to the top of the catalog ("Needs Attention") | 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.8 |
| Detail view (overview, encoding, subtitles pills) | 1.6, 1.9 |
| Pipeline tab shows all jobs across every phase | 1.7, 1.10 |
| Right-click context menu with actions | 2.3 |
| Subtitle request dialog → Bazarr queue | 2.1, 2.2, 2.3 |
| Central log tab, colour-coded by source | 3.1, 3.2 |
| Automation paused for flagged items | 2.4 |
| Admin/manager only | uses existing `auth.Guard` / `auth.GuardAdmin` (File Map) |

**Placeholder scan:** no `TBD`, `TODO`, `similar to above`, `XXX`, `...` (other than literal Go variadic) in task bodies. The only ellipses are the U+2026 characters in visible UI strings (`Loading…`, `Queuing…`) which are intentional.

**Type consistency across tasks:**
- `Flag` / `FlagSeverity` / `FlagSeverityError|Warn|Info` defined in Task 1.2, used in 1.3, 1.4, 1.5, 2.4.
- `CatalogFlagRow` defined in Task 1.2, used by `AllFlagged`/`FlagsByPath` (1.2) and served in 1.5, consumed in 1.6.
- `ComputeFlags(*Job) []Flag` is the single engine entry point — referenced in 1.2 (defined), 1.4 (called from `persistFlags`), 1.3 (round-trip test).
- `Job.Flags []Flag` is the only new field on `Job`; added in 1.3, scanned in 1.3, populated in 1.4, serialised by extended scan lists in 1.3.
- `BazarrSearchOpts` / `bazarrSearchSubtitlesWithOpts` defined in 2.1, used in 2.2 and 2.4 (via the seam `bazarrTrigger`).
- `subtitle_request` is the registered action name used both in procula (2.2) and the frontend POST body (2.3); `arr_type` / `arr_id` / `episode_id` / `languages` / `hi` / `forced` param keys are consistent between them.
- `handleCatalogFlags` / `handleCatalogDetail` / `handleJobsList` / `handleLogsAggregate` — each Go handler has exactly one route registration in `middleware/main.go`.
- `LogEntry { Service, Line }` shape matches the JS renderer in `nginx/logs.js` (Task 3.2 consumes `e.service` and `e.line`).
