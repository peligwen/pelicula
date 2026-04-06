package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// overrideSettings sets cachedSettings for the duration of a test.
func overrideSettings(t *testing.T, s PipelineSettings) {
	t.Helper()
	settingsMu.Lock()
	old := cachedSettings
	cachedSettings = &s
	settingsMu.Unlock()
	t.Cleanup(func() {
		settingsMu.Lock()
		cachedSettings = old
		settingsMu.Unlock()
	})
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

	out, err := maybeTranscode(nil, q, job, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "" {
		t.Errorf("output = %q, want empty (transcoding disabled)", out)
	}
}

func TestMaybeTranscode_NoValidation(t *testing.T) {
	overrideSettings(t, PipelineSettings{TranscodingEnabled: true})

	q := newTestQueue(t)
	job := &Job{Source: testSource("/fake/movie.mkv")}
	// job.Validation is nil

	out, err := maybeTranscode(nil, q, job, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "" {
		t.Errorf("output = %q, want empty (no validation data)", out)
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
	out, err := maybeTranscode(nil, q, job, cfgDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "" {
		t.Errorf("output = %q, want empty (no matching profile)", out)
	}
}
