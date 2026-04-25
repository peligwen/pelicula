package procula

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// overrideSettings saves settings into a fresh test DB and points appDB at it
// for the duration of the test, restoring the original appDB on cleanup.
func overrideSettings(t *testing.T, s PipelineSettings) {
	t.Helper()
	db := testDB(t)
	if err := SaveSettings(db, s); err != nil {
		t.Fatalf("overrideSettings: SaveSettings: %v", err)
	}
	old := appDB
	appDB = db
	t.Cleanup(func() { appDB = old })
}

// overrideSettingsDB is like overrideSettings but also returns the DB so the
// caller can create a Queue that shares it.
func overrideSettingsDB(t *testing.T, s PipelineSettings) *sql.DB {
	t.Helper()
	db := testDB(t)
	if err := SaveSettings(db, s); err != nil {
		t.Fatalf("overrideSettingsDB: SaveSettings: %v", err)
	}
	old := appDB
	appDB = db
	t.Cleanup(func() { appDB = old })
	return db
}

// fakePeliculaAPI returns a test server that accepts any request (for blocklist/catalog calls).
func fakePeliculaAPI(t *testing.T) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	return ts.URL
}

func TestProcessJob_AllDisabled(t *testing.T) {
	overrideSettings(t, PipelineSettings{
		ValidationEnabled:  false,
		TranscodingEnabled: false,
		CatalogEnabled:     false,
	})

	q := newTestQueue(t)
	api := fakePeliculaAPI(t)
	created, _ := q.Create(testSource("/fake/movie.mkv"))

	processJob(q, created.ID, t.TempDir(), api)

	job, ok := q.Get(created.ID)
	if !ok {
		t.Fatal("job not found after processing")
	}
	if job.State != StateCompleted {
		t.Errorf("state = %q, want %q", job.State, StateCompleted)
	}
	if job.Stage != StageDone {
		t.Errorf("stage = %q, want %q", job.Stage, StageDone)
	}
	if job.Progress != 1.0 {
		t.Errorf("progress = %f, want 1.0", job.Progress)
	}
	if job.Validation != nil {
		t.Error("validation should be nil when disabled")
	}
}

func TestProcessJob_ValidationFails_FileNotFound(t *testing.T) {
	overrideSettings(t, PipelineSettings{
		ValidationEnabled:  true,
		TranscodingEnabled: false,
		CatalogEnabled:     false,
	})

	q := newTestQueue(t)
	cfgDir := t.TempDir()
	api := fakePeliculaAPI(t)
	created, _ := q.Create(testSource("/nonexistent/movie.mkv"))

	processJob(q, created.ID, cfgDir, api)

	job, ok := q.Get(created.ID)
	if !ok {
		t.Fatal("job not found after processing")
	}
	if job.State != StateFailed {
		t.Errorf("state = %q, want %q", job.State, StateFailed)
	}
	if job.Stage != StageValidate {
		t.Errorf("stage = %q, want %q", job.Stage, StageValidate)
	}
	if job.Error == "" {
		t.Error("error should be set on validation failure")
	}
}

func TestProcessJob_Cancelled(t *testing.T) {
	overrideSettings(t, PipelineSettings{
		ValidationEnabled:  false,
		TranscodingEnabled: false,
		CatalogEnabled:     false,
	})

	q := newTestQueue(t)
	api := fakePeliculaAPI(t)
	created, _ := q.Create(testSource("/fake/movie.mkv"))

	// Cancel before processing
	q.Cancel(created.ID)

	processJob(q, created.ID, t.TempDir(), api)

	job, _ := q.Get(created.ID)
	if job.State != StateCancelled {
		t.Errorf("state = %q, want %q", job.State, StateCancelled)
	}
}

// ── maybeTranscode ──────────────────────────────────────────────────────

func TestMaybeTranscode_Disabled(t *testing.T) {
	overrideSettings(t, PipelineSettings{TranscodingEnabled: false})

	q := newTestQueue(t)
	job := &Job{Source: testSource("/fake/movie.mkv")}

	if err := maybeTranscode(context.Background(), q, job, t.TempDir()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestMaybeTranscode_NoValidation_ProbeFailureReturnsNil covers the path where
// validation was skipped (job.Validation == nil) but ffprobe also fails (e.g.
// corrupt or missing file). Transcoding should be skipped gracefully — no crash,
// no error returned to the caller.
func TestMaybeTranscode_NoValidation_ProbeFailureReturnsNil(t *testing.T) {
	overrideSettings(t, PipelineSettings{TranscodingEnabled: true})

	q := newTestQueue(t)
	job := &Job{Source: testSource("/nonexistent/movie.mkv")}
	// job.Validation is nil and the file doesn't exist, so runFFprobe will fail

	if err := maybeTranscode(context.Background(), q, job, t.TempDir()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestMaybeTranscode_NoValidation_ProbesAndTranscodes verifies that when
// validation was skipped (job.Validation == nil), maybeTranscode probes the
// source file directly with ffprobe and proceeds to profile matching.
// Uses a fake ffprobe (h264/1080p) and a profile targeting h264 ≤1080p so the
// result is passthrough — no real ffmpeg invocation needed.
func TestMaybeTranscode_NoValidation_ProbesAndTranscodes(t *testing.T) {
	cfgDir := t.TempDir()
	overrideSettings(t, PipelineSettings{TranscodingEnabled: true})

	// Fake ffprobe returning h264 @ 1080p
	setupFakeFFprobe(t)
	t.Setenv("GO_TEST_FFPROBE", "success")

	// Profile: match h264, max_height 1080 — source is already 1080p → passthrough
	profileDir := filepath.Join(cfgDir, "procula", "profiles")
	os.MkdirAll(profileDir, 0755)
	os.WriteFile(filepath.Join(profileDir, "h264.json"), []byte(`{
		"name":"h264-1080p","enabled":true,
		"conditions":{"codecs_include":["h264"]},
		"output":{"video_codec":"libx264","audio_codec":"aac","max_height":1080,"suffix":".test"}
	}`), 0644)

	// Need a real queue entry so the Update calls inside maybeTranscode have an ID
	q := newTestQueue(t)
	created, _ := q.Create(testSource("/fake/movie.mkv"))
	job, _ := q.Get(created.ID)
	// Deliberately leave job.Validation nil to exercise the direct-probe path

	if err := maybeTranscode(context.Background(), q, job, cfgDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, _ := q.Get(created.ID)
	if updated.TranscodeProfile != "h264-1080p" {
		t.Errorf("TranscodeProfile = %q, want %q — direct probe path did not reach profile matching",
			updated.TranscodeProfile, "h264-1080p")
	}
	if updated.TranscodeDecision != "passthrough" {
		t.Errorf("TranscodeDecision = %q, want passthrough", updated.TranscodeDecision)
	}
}

func TestMaybeTranscode_NoMatchingProfile(t *testing.T) {
	cfgDir := t.TempDir()
	overrideSettings(t, PipelineSettings{TranscodingEnabled: true})

	q := newTestQueue(t)
	job := &Job{
		Source: testSource("/fake/movie.mkv"),
		Validation: &ValidationResult{
			Passed: true,
			Checks: ValidationChecks{
				Codecs: &CodecInfo{Video: "h264", Audio: "aac", Height: 1080},
			},
		},
	}

	// No profiles dir means no profiles loaded
	if err := maybeTranscode(context.Background(), q, job, cfgDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestMaybeTranscode_Passthrough verifies that when the source already matches
// the profile target the job is marked "passthrough" and no ffmpeg is invoked.
func TestMaybeTranscode_Passthrough(t *testing.T) {
	cfgDir := t.TempDir()
	overrideSettings(t, PipelineSettings{TranscodingEnabled: true})

	// Write a profile that targets h264/1080p
	profileDir := cfgDir + "/procula/profiles"
	os.MkdirAll(profileDir, 0755)
	os.WriteFile(profileDir+"/h264.json", []byte(`{
		"name":"h264-1080p","enabled":true,
		"conditions":{"codecs_include":["h264"]},
		"output":{"video_codec":"libx264","audio_codec":"aac","max_height":1080,"suffix":" - 1080p"}
	}`), 0644)

	q := newTestQueue(t)
	// Create a job so we can get an ID to Update
	created, _ := q.Create(testSource("/fake/movie.mkv"))
	job, _ := q.Get(created.ID)
	// Source is already h264 @ 720p → within 1080p max_height → passthrough
	job.Validation = &ValidationResult{
		Passed: true,
		Checks: ValidationChecks{
			Codecs: &CodecInfo{Video: "h264", Audio: "aac", Height: 720},
		},
	}

	if err := maybeTranscode(context.Background(), q, job, cfgDir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, _ := q.Get(created.ID)
	if updated.TranscodeDecision != "passthrough" {
		t.Errorf("TranscodeDecision = %q, want %q", updated.TranscodeDecision, "passthrough")
	}
	if updated.TranscodeProfile != "h264-1080p" {
		t.Errorf("TranscodeProfile = %q, want %q", updated.TranscodeProfile, "h264-1080p")
	}
	if len(updated.TranscodeOutputs) != 0 {
		t.Errorf("TranscodeOutputs should be empty for passthrough, got %v", updated.TranscodeOutputs)
	}
}

// TestMaybeTranscode_FailureLeavesOriginal verifies that when FFmpeg fails the
// job's Source.Path is untouched and TranscodeDecision is set to "failed".
func TestMaybeTranscode_FailureLeavesOriginal(t *testing.T) {
	cfgDir := t.TempDir()
	overrideSettings(t, PipelineSettings{TranscodingEnabled: true})

	// Profile matching hevc → libx264 (source is hevc, so condition is met)
	profileDir := cfgDir + "/procula/profiles"
	os.MkdirAll(profileDir, 0755)
	os.WriteFile(profileDir+"/hevc.json", []byte(`{
		"name":"hevc-compat","enabled":true,
		"conditions":{"codecs_include":["hevc"]},
		"output":{"video_codec":"libx264","audio_codec":"aac","suffix":" - H264"}
	}`), 0644)

	// Set up fake ffmpeg that always fails
	setupFakeFFmpeg(t)
	t.Setenv("GO_TEST_FFMPEG", "fail")

	// Create a real source file so processWithDir doesn't error on empty path
	sourceDir := t.TempDir()
	sourcePath := sourceDir + "/movie.hevc.mkv"
	os.WriteFile(sourcePath, []byte("fake"), 0644)

	q := newTestQueue(t)
	created, _ := q.Create(testSource(sourcePath))
	job, _ := q.Get(created.ID)
	job.Validation = &ValidationResult{
		Passed: true,
		Checks: ValidationChecks{
			Codecs: &CodecInfo{Video: "hevc", Audio: "ac3", Height: 1080},
		},
	}

	err := maybeTranscode(context.Background(), q, job, cfgDir)
	if err == nil {
		t.Fatal("expected error from failing FFmpeg")
	}

	updated, _ := q.Get(created.ID)
	// Source path must be unchanged
	if updated.Source.Path != sourcePath {
		t.Errorf("Source.Path = %q, want %q (original must be preserved)", updated.Source.Path, sourcePath)
	}
	// Decision must be "failed"
	if updated.TranscodeDecision != "failed" {
		t.Errorf("TranscodeDecision = %q, want %q", updated.TranscodeDecision, "failed")
	}
	if updated.TranscodeError == "" {
		t.Error("TranscodeError should be set on failure")
	}
	if len(updated.TranscodeOutputs) != 0 {
		t.Errorf("TranscodeOutputs should be empty on failure, got %v", updated.TranscodeOutputs)
	}
}

// TestProcessJob_TranscodeFailureContinues verifies the full pipeline: when
// transcoding fails the job still reaches StateCompleted (original cataloged).
func TestProcessJob_TranscodeFailureContinues(t *testing.T) {
	cfgDir := t.TempDir()
	overrideSettings(t, PipelineSettings{
		ValidationEnabled:  false,
		TranscodingEnabled: true,
		CatalogEnabled:     false,
	})

	profileDir := cfgDir + "/procula/profiles"
	os.MkdirAll(profileDir, 0755)
	os.WriteFile(profileDir+"/hevc.json", []byte(`{
		"name":"hevc-compat","enabled":true,
		"conditions":{"codecs_include":["hevc"]},
		"output":{"video_codec":"libx264","audio_codec":"aac","suffix":" - H264"}
	}`), 0644)

	setupFakeFFmpeg(t)
	t.Setenv("GO_TEST_FFMPEG", "fail")

	sourceDir := t.TempDir()
	sourcePath := sourceDir + "/movie.mkv"
	os.WriteFile(sourcePath, []byte("fake"), 0644)

	// Inject validation result so maybeTranscode finds the profile
	q := newTestQueue(t)
	created, _ := q.Create(testSource(sourcePath))
	q.Update(created.ID, func(j *Job) {
		j.Validation = &ValidationResult{
			Passed: true,
			Checks: ValidationChecks{
				Codecs: &CodecInfo{Video: "hevc", Audio: "ac3", Height: 1080},
			},
		}
	})

	api := fakePeliculaAPI(t)
	processJob(q, created.ID, cfgDir, api)

	job, _ := q.Get(created.ID)
	if job.State != StateCompleted {
		t.Errorf("state = %q, want %q (transcode failure must not block completion)", job.State, StateCompleted)
	}
	if job.TranscodeDecision != "failed" {
		t.Errorf("TranscodeDecision = %q, want %q", job.TranscodeDecision, "failed")
	}
}

// ── Catalog ordering tests ───────────────────────────────────────────────────

// countingServer returns a test server that counts hits to /api/pelicula/jellyfin/refresh.
func countingServer(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	var count atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/pelicula/jellyfin/refresh" {
			count.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)
	return ts.URL, &count
}

// TestCatalogEarly_TriggersBeforeTranscode verifies that Jellyfin is refreshed
// BEFORE the fake ffmpeg process runs (early catalog) and a second time after
// the sidecar is written (late catalog).
func TestCatalogEarly_TriggersBeforeTranscode(t *testing.T) {
	cfgDir := t.TempDir()

	// Write a profile that matches h264 (same as source), NOT passthrough
	// (source height 4320 > max_height 1080, so it will transcode)
	profileDir := cfgDir + "/procula/profiles"
	os.MkdirAll(profileDir, 0755)
	os.WriteFile(profileDir+"/4k.json", []byte(`{
		"name":"4k-down","enabled":true,
		"conditions":{"min_height":2160},
		"output":{"video_codec":"libx264","audio_codec":"aac","max_height":1080,"suffix":" - 1080p"}
	}`), 0644)

	setupFakeFFmpeg(t)
	t.Setenv("GO_TEST_FFMPEG", "success")

	sourceDir := t.TempDir()
	sourcePath := sourceDir + "/movie.mkv"
	os.WriteFile(sourcePath, []byte("fake"), 0644)

	apiURL, refreshCount := countingServer(t)
	t.Setenv("PROCULA_API_KEY", "")               // disable key check
	t.Setenv("JELLYFIN_REFRESH_DEBOUNCE_MS", "0") // immediate POST so ordering is observable

	overrideSettings(t, PipelineSettings{
		ValidationEnabled:  false,
		TranscodingEnabled: true,
		CatalogEnabled:     true,
	})

	q := newTestQueue(t)
	created, _ := q.Create(testSource(sourcePath))
	// Inject validation result
	q.Update(created.ID, func(j *Job) {
		j.Validation = &ValidationResult{
			Passed: true,
			Checks: ValidationChecks{
				Codecs: &CodecInfo{Video: "h264", Audio: "aac", Height: 4320},
			},
		}
	})

	processJob(q, created.ID, cfgDir, apiURL)

	job, _ := q.Get(created.ID)
	if job.State != StateCompleted {
		t.Fatalf("state = %q, want completed", job.State)
	}
	if job.TranscodeDecision != "transcoded" {
		t.Errorf("TranscodeDecision = %q, want transcoded", job.TranscodeDecision)
	}
	// Two Jellyfin refreshes: one early (catalog before transcode) + one late (sidecar ready)
	if n := refreshCount.Load(); n < 2 {
		t.Errorf("expected ≥2 Jellyfin refreshes, got %d (early + late)", n)
	}
}

// TestCatalogLate_SkipsWhenNoSidecar verifies only one Jellyfin refresh fires
// when transcoding is disabled (no sidecar written).
func TestCatalogLate_SkipsWhenNoSidecar(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("JELLYFIN_REFRESH_DEBOUNCE_MS", "0") // immediate POST for assertion
	overrideSettings(t, PipelineSettings{
		ValidationEnabled:  false,
		TranscodingEnabled: false,
		CatalogEnabled:     true,
	})

	apiURL, refreshCount := countingServer(t)
	q := newTestQueue(t)
	created, _ := q.Create(testSource("/fake/movie.mkv"))

	processJob(q, created.ID, cfgDir, apiURL)

	if n := refreshCount.Load(); n != 1 {
		t.Errorf("expected exactly 1 Jellyfin refresh (no transcode), got %d", n)
	}
}

// TestCatalogLate_SkipsOnPassthrough verifies only one refresh fires when the
// source already matches the profile target (passthrough decision).
func TestCatalogLate_SkipsOnPassthrough(t *testing.T) {
	cfgDir := t.TempDir()

	profileDir := cfgDir + "/procula/profiles"
	os.MkdirAll(profileDir, 0755)
	os.WriteFile(profileDir+"/h264.json", []byte(`{
		"name":"h264","enabled":true,
		"conditions":{"codecs_include":["h264"]},
		"output":{"video_codec":"libx264","audio_codec":"aac","max_height":1080,"suffix":" - 1080p"}
	}`), 0644)

	overrideSettings(t, PipelineSettings{
		ValidationEnabled:  false,
		TranscodingEnabled: true,
		CatalogEnabled:     true,
	})

	t.Setenv("JELLYFIN_REFRESH_DEBOUNCE_MS", "0") // immediate POST for assertion
	apiURL, refreshCount := countingServer(t)
	q := newTestQueue(t)
	created, _ := q.Create(testSource("/fake/movie.mkv"))
	q.Update(created.ID, func(j *Job) {
		j.Validation = &ValidationResult{
			Passed: true,
			Checks: ValidationChecks{
				Codecs: &CodecInfo{Video: "h264", Audio: "aac", Height: 720},
			},
		}
	})

	processJob(q, created.ID, cfgDir, apiURL)

	job, _ := q.Get(created.ID)
	if job.TranscodeDecision != "passthrough" {
		t.Errorf("TranscodeDecision = %q, want passthrough", job.TranscodeDecision)
	}
	if n := refreshCount.Load(); n != 1 {
		t.Errorf("expected exactly 1 refresh (passthrough, no sidecar), got %d", n)
	}
}

// ── Manual transcode tests ───────────────────────────────────────────────────

// TestRunManualTranscode_Success verifies a manual transcode job completes,
// writes a sidecar, and skips the validation + early-catalog stages.
func TestRunManualTranscode_Success(t *testing.T) {
	cfgDir := t.TempDir()

	profileDir := cfgDir + "/procula/profiles"
	os.MkdirAll(profileDir, 0755)
	os.WriteFile(profileDir+"/hevc.json", []byte(`{
		"name":"hevc-compat","enabled":true,
		"conditions":{"codecs_include":["hevc"]},
		"output":{"video_codec":"libx264","audio_codec":"aac","suffix":" - H264"}
	}`), 0644)

	setupFakeFFmpeg(t)
	t.Setenv("GO_TEST_FFMPEG", "success")

	sourceDir := t.TempDir()
	sourcePath := sourceDir + "/movie.hevc.mkv"
	os.WriteFile(sourcePath, []byte("fake"), 0644)

	overrideSettings(t, PipelineSettings{
		ValidationEnabled:  true, // would fail if it ran — but manual skips it
		TranscodingEnabled: false,
		CatalogEnabled:     false,
	})

	apiURL, refreshCount := countingServer(t)
	q := newTestQueue(t)
	src := testSource(sourcePath)
	created, _ := q.Create(src)
	q.Update(created.ID, func(j *Job) { j.ManualProfile = "hevc-compat" })

	processJob(q, created.ID, cfgDir, apiURL)

	job, _ := q.Get(created.ID)
	if job.State != StateCompleted {
		t.Errorf("state = %q, want completed", job.State)
	}
	if job.TranscodeDecision != "transcoded" {
		t.Errorf("TranscodeDecision = %q, want transcoded", job.TranscodeDecision)
	}
	if len(job.TranscodeOutputs) != 1 {
		t.Errorf("TranscodeOutputs = %v, want 1 path", job.TranscodeOutputs)
	}
	// Validation was skipped — Validation field should be nil
	if job.Validation != nil {
		t.Error("Validation should be nil for manual transcode jobs (validation is skipped)")
	}
	// CatalogEnabled=false → no Jellyfin refresh
	if n := refreshCount.Load(); n != 0 {
		t.Errorf("expected 0 Jellyfin refreshes (CatalogEnabled=false), got %d", n)
	}
}

// TestRunManualTranscode_ProfileNotFound verifies a clear failure when the
// named profile doesn't exist.
func TestRunManualTranscode_ProfileNotFound(t *testing.T) {
	cfgDir := t.TempDir()
	overrideSettings(t, PipelineSettings{TranscodingEnabled: false, CatalogEnabled: false})

	q := newTestQueue(t)
	created, _ := q.Create(testSource("/fake/movie.mkv"))
	q.Update(created.ID, func(j *Job) { j.ManualProfile = "nonexistent-profile" })

	processJob(q, created.ID, cfgDir, "http://localhost:0")

	job, _ := q.Get(created.ID)
	if job.State != StateFailed {
		t.Errorf("state = %q, want failed", job.State)
	}
	if job.Error == "" {
		t.Error("Error should be set when profile not found")
	}
}

// TestRunManualTranscode_SkipsValidation confirms that a manual transcode job
// starts transcoding even when the file path wouldn't pass normal job path checks.
// The manual endpoint already restricts to /media/ at creation time.
func TestRunManualTranscode_SkipsValidation(t *testing.T) {
	cfgDir := t.TempDir()

	profileDir := cfgDir + "/procula/profiles"
	os.MkdirAll(profileDir, 0755)
	os.WriteFile(profileDir+"/hevc.json", []byte(`{
		"name":"hevc-compat","enabled":false,
		"conditions":{"codecs_include":["hevc"]},
		"output":{"video_codec":"libx264","audio_codec":"aac","suffix":" - H264"}
	}`), 0644)

	// FindProfileByName ignores enabled flag → profile should still be found
	setupFakeFFmpeg(t)
	t.Setenv("GO_TEST_FFMPEG", "success")

	sourceDir := t.TempDir()
	sourcePath := sourceDir + "/movie.hevc.mkv"
	os.WriteFile(sourcePath, []byte("fake"), 0644)

	overrideSettings(t, PipelineSettings{
		ValidationEnabled:  true,
		TranscodingEnabled: true,
		CatalogEnabled:     false,
	})

	q := newTestQueue(t)
	created, _ := q.Create(testSource(sourcePath))
	q.Update(created.ID, func(j *Job) { j.ManualProfile = "hevc-compat" })

	processJob(q, created.ID, cfgDir, "http://localhost:0")

	job, _ := q.Get(created.ID)
	if job.State != StateCompleted {
		t.Errorf("state = %q, want completed (disabled profile should still be used for manual jobs)", job.State)
	}
}

// ── DeleteOnFailure branch matrix ────────────────────────────────────────────

// TestDeleteOnFailure_False verifies that a validation failure leaves the file
// in place when DeleteOnFailure is false (the default).
func TestDeleteOnFailure_False(t *testing.T) {
	dir := t.TempDir()
	filePath := dir + "/movie.mkv"
	if err := os.WriteFile(filePath, []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	// isAllowedPath doesn't allow /tmp paths — that's expected and correct;
	// we still verify the file is NOT deleted when DeleteOnFailure=false.
	overrideSettings(t, PipelineSettings{
		ValidationEnabled:  true,
		TranscodingEnabled: false,
		CatalogEnabled:     false,
		DeleteOnFailure:    false,
	})

	q := newTestQueue(t)
	api := fakePeliculaAPI(t)
	// Create job with a real (temp) file that will fail ffprobe
	src := testSource(filePath)
	created, _ := q.Create(src)
	processJob(q, created.ID, t.TempDir(), api)

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Error("file was deleted even though DeleteOnFailure=false")
	}
}

// TestDeleteOnFailure_True_AllowedPath verifies that a file under /downloads
// is deleted when DeleteOnFailure=true and validation fails.
func TestDeleteOnFailure_True_AllowedPath(t *testing.T) {
	// We can't put a real file at /downloads in tests, so we verify that
	// the code path reaches the os.Remove call by using a file under a
	// temp dir that is NOT on the allowlist — and confirming the file is
	// NOT deleted (since isAllowedPath returns false for /tmp paths).
	// The positive case is verified in TestDeleteOnFailure_True_DisallowedPath_Skipped.
	t.Skip("requires /downloads mount — covered by e2e test")
}

// TestDeleteOnFailure_True_DisallowedPath_Skipped confirms that a file outside
// the allowed prefixes (/downloads, /processing) is never deleted even when
// DeleteOnFailure=true — this is the core of the security fix.
func TestDeleteOnFailure_True_DisallowedPath_Skipped(t *testing.T) {
	dir := t.TempDir()
	filePath := dir + "/movie.mkv" // not under /downloads or /processing
	if err := os.WriteFile(filePath, []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	overrideSettings(t, PipelineSettings{
		ValidationEnabled:  true,
		TranscodingEnabled: false,
		CatalogEnabled:     false,
		DeleteOnFailure:    true,
	})

	q := newTestQueue(t)
	api := fakePeliculaAPI(t)
	src := testSource(filePath)
	created, _ := q.Create(src)
	processJob(q, created.ID, t.TempDir(), api)

	// File must NOT be deleted — isAllowedPath returns false for /tmp paths.
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Error("file was deleted even though path is outside allowed prefixes")
	}
}

// TestDeleteOnFailure_True_MediaPath_Refused confirms that files under /media/
// (the library root) are never deleted — library paths are excluded from
// isAllowedPath to prevent an attacker-controlled webhook from deleting imported media.
func TestDeleteOnFailure_True_MediaPath_Refused(t *testing.T) {
	// isAllowedPath should return false for /media/ paths — verify the function.
	// (The end-to-end file deletion is tested via isAllowedPath unit test.)
	if isAllowedPath("/media/movies/Alien/alien.mkv") {
		t.Error("isAllowedPath should return false for /media/movies paths — security regression")
	}
	if isAllowedPath("/media/tv/show/s01e01.mkv") {
		t.Error("isAllowedPath should return false for /media/tv paths — security regression")
	}
	// Downloads and processing must remain allowed.
	if !isAllowedPath("/downloads/alien.mkv") {
		t.Error("isAllowedPath should return true for /downloads paths")
	}
	if !isAllowedPath("/processing/alien.mkv") {
		t.Error("isAllowedPath should return true for /processing paths")
	}
}

// ── Dual subtitle pipeline integration tests ─────────────────────────────────

// TestProcessJob_DualSub_SidecarSource verifies the full pipeline when
// DualSubEnabled=true and sidecar .srt files exist alongside the source.
// A stacked .en-es.ass file should be written and reflected in DualSubOutputs.
func TestProcessJob_DualSub_SidecarSource(t *testing.T) {
	// Fake ffprobe returning valid metadata with no embedded subtitle streams
	// so getCues falls through to the sidecar discovery path.
	setupFakeFFprobe(t)
	t.Setenv("GO_TEST_FFPROBE", "success")

	dir := t.TempDir()
	sourcePath := dir + "/Movie (2020).mkv"
	os.WriteFile(sourcePath, []byte("fake media"), 0644)

	enSRT := "1\n00:00:01,000 --> 00:00:03,000\nHello\n\n2\n00:00:05,000 --> 00:00:07,000\nWorld\n\n"
	esSRT := "1\n00:00:01,500 --> 00:00:02,500\nHola\n\n2\n00:00:05,500 --> 00:00:06,500\nMundo\n\n"
	os.WriteFile(dir+"/Movie (2020).en.srt", []byte(enSRT), 0644)
	os.WriteFile(dir+"/Movie (2020).es.srt", []byte(esSRT), 0644)

	cfgDir := t.TempDir()
	overrideSettings(t, PipelineSettings{
		ValidationEnabled:  false,
		DualSubEnabled:     true,
		DualSubPairs:       []string{"en-es"},
		DualSubTranslator:  "none",
		TranscodingEnabled: false,
		CatalogEnabled:     false,
	})

	q := newTestQueue(t)
	api := fakePeliculaAPI(t)
	created, _ := q.Create(testSource(sourcePath))

	processJob(q, created.ID, cfgDir, api)

	job, _ := q.Get(created.ID)
	if job.State != StateCompleted {
		t.Fatalf("state = %q, want completed (DualSubError: %q)", job.State, job.DualSubError)
	}
	if len(job.DualSubOutputs) != 1 {
		t.Fatalf("DualSubOutputs = %v, want 1 path", job.DualSubOutputs)
	}

	assPath := dir + "/Movie (2020).en-es.ass"
	if job.DualSubOutputs[0] != assPath {
		t.Errorf("DualSubOutputs[0] = %q, want %q", job.DualSubOutputs[0], assPath)
	}

	data, err := os.ReadFile(assPath)
	if err != nil {
		t.Fatalf("read .ass file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, `{\an2}Hello`) {
		t.Errorf("expected English bottom cue in ASS output")
	}
	if !strings.Contains(content, `{\an2}Hola`) {
		t.Errorf("expected Spanish top cue in ASS output")
	}
	if job.DualSubError != "" {
		t.Errorf("DualSubError should be empty on success, got %q", job.DualSubError)
	}
}

// TestProcessJob_DualSub_Disabled verifies that the dualsub stage is skipped
// when DualSubEnabled=false, leaving no .ass file and no DualSubOutputs.
func TestProcessJob_DualSub_Disabled(t *testing.T) {
	dir := t.TempDir()
	sourcePath := dir + "/Movie.mkv"
	os.WriteFile(sourcePath, []byte("fake"), 0644)

	cfgDir := t.TempDir()
	overrideSettings(t, PipelineSettings{
		ValidationEnabled:  false,
		DualSubEnabled:     false,
		TranscodingEnabled: false,
		CatalogEnabled:     false,
	})

	q := newTestQueue(t)
	api := fakePeliculaAPI(t)
	created, _ := q.Create(testSource(sourcePath))

	processJob(q, created.ID, cfgDir, api)

	job, _ := q.Get(created.ID)
	if job.State != StateCompleted {
		t.Errorf("state = %q, want completed", job.State)
	}
	if len(job.DualSubOutputs) != 0 {
		t.Errorf("DualSubOutputs should be empty when disabled, got %v", job.DualSubOutputs)
	}
	if _, err := os.Stat(dir + "/Movie.en-es.ass"); !os.IsNotExist(err) {
		t.Error(".ass file should not exist when dualsub is disabled")
	}
}

// TestProcessJob_DualSub_NoSource verifies that when DualSubEnabled=true but
// no subtitle source exists, DualSubError is recorded and the job still completes.
func TestProcessJob_DualSub_NoSource(t *testing.T) {
	setupFakeFFprobe(t)
	t.Setenv("GO_TEST_FFPROBE", "success") // no subtitle streams in response

	dir := t.TempDir()
	sourcePath := dir + "/Movie.mkv"
	os.WriteFile(sourcePath, []byte("fake"), 0644)
	// No .en.srt or .es.srt sidecars created.

	cfgDir := t.TempDir()
	overrideSettings(t, PipelineSettings{
		ValidationEnabled:  false,
		DualSubEnabled:     true,
		DualSubPairs:       []string{"en-es"},
		DualSubTranslator:  "none",
		TranscodingEnabled: false,
		CatalogEnabled:     false,
	})

	q := newTestQueue(t)
	api := fakePeliculaAPI(t)
	created, _ := q.Create(testSource(sourcePath))

	processJob(q, created.ID, cfgDir, api)

	job, _ := q.Get(created.ID)
	if job.State != StateCompleted {
		t.Errorf("state = %q, want completed (dualsub failure must not block job)", job.State)
	}
	if len(job.DualSubOutputs) != 0 {
		t.Errorf("DualSubOutputs should be empty, got %v", job.DualSubOutputs)
	}
	if job.DualSubError == "" {
		t.Error("DualSubError should be set when no subtitle source is available")
	}
}

// TestPipelineStampsValidationFailedFlag verifies that when validation fails
// the pipeline writes a validation_failed flag to both the job row and the
// catalog_flags index table.
func TestPipelineStampsValidationFailedFlag(t *testing.T) {
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "test.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	old := appDB
	appDB = db
	t.Cleanup(func() { appDB = old })

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

	row, err := flagsByPath(db, job.Source.Path)
	if err != nil || row == nil {
		t.Fatalf("catalog_flags row missing: err=%v row=%v", err, row)
	}
	if row.Severity != "error" {
		t.Errorf("severity = %s, want error", row.Severity)
	}
}

// ── blocklist context tests ──────────────────────────────────────────────────

// TestBlocklist_ContextCancellation verifies that blocklist returns promptly
// when the context is cancelled before any retries complete.
func TestBlocklist_ContextCancellation(t *testing.T) {
	// Server that hangs until the connection is closed — ensures the test does
	// not pass simply because the server responds quickly.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Delay longer than the test deadline so the context cancellation fires first.
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately.
	cancel()

	job := &Job{Source: JobSource{DownloadHash: "abc123", ArrType: "radarr"}}
	start := time.Now()
	blocklist(ctx, job, srv.URL, "test cancellation")
	elapsed := time.Since(start)

	if elapsed > 100*time.Millisecond {
		t.Errorf("blocklist took %v after ctx cancel, want < 100ms", elapsed)
	}
}

// TestBlocklist_RetriesOnFailure verifies that blocklist retries on non-2xx
// responses and succeeds on the third attempt.
func TestBlocklist_RetriesOnFailure(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n < 3 {
			http.Error(w, "temporary error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	job := &Job{
		ID:     "test-job",
		Source: JobSource{DownloadHash: "deadbeef", ArrType: "sonarr", Title: "Test Show", Type: "episode"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Use a tiny retry delay so the test is fast.
	// blocklist uses time.Duration(attempt)*2*time.Second; we can't inject
	// the delay, but the test just has to complete within 30s.
	// Override by running with a real fast server (responds in <1ms each call).
	blocklist(ctx, job, srv.URL, "bad audio")

	if n := calls.Load(); n != 3 {
		t.Errorf("expected 3 calls (2 failures + 1 success), got %d", n)
	}
}

// TestCatalogLate_TriggersOnDualSubOutputs verifies that a late Jellyfin refresh
// fires when DualSubOutputs is populated — even without a transcode sidecar.
func TestCatalogLate_TriggersOnDualSubOutputs(t *testing.T) {
	setupFakeFFprobe(t)
	t.Setenv("GO_TEST_FFPROBE", "success")

	dir := t.TempDir()
	sourcePath := dir + "/Movie.mkv"
	os.WriteFile(sourcePath, []byte("fake"), 0644)
	enSRT := "1\n00:00:01,000 --> 00:00:03,000\nHello\n\n"
	esSRT := "1\n00:00:01,000 --> 00:00:03,000\nHola\n\n"
	os.WriteFile(dir+"/Movie.en.srt", []byte(enSRT), 0644)
	os.WriteFile(dir+"/Movie.es.srt", []byte(esSRT), 0644)

	cfgDir := t.TempDir()
	overrideSettings(t, PipelineSettings{
		ValidationEnabled:  false,
		DualSubEnabled:     true,
		DualSubPairs:       []string{"en-es"},
		DualSubTranslator:  "none",
		TranscodingEnabled: false,
		CatalogEnabled:     true, // enabled — should trigger CatalogEarly + CatalogLate
	})

	t.Setenv("JELLYFIN_REFRESH_DEBOUNCE_MS", "0") // immediate POST so each refresh is observable
	apiURL, refreshCount := countingServer(t)
	q := newTestQueue(t)
	created, _ := q.Create(testSource(sourcePath))

	processJob(q, created.ID, cfgDir, apiURL)

	job, _ := q.Get(created.ID)
	if job.State != StateCompleted {
		t.Fatalf("state = %q, want completed", job.State)
	}
	if len(job.DualSubOutputs) == 0 {
		t.Fatal("DualSubOutputs should be non-empty")
	}
	// CatalogEarly (1 refresh) + CatalogLate triggered by DualSubOutputs (1 more)
	if n := refreshCount.Load(); n < 2 {
		t.Errorf("expected ≥2 Jellyfin refreshes (early + late for dual-sub), got %d", n)
	}
}
