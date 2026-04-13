package main

import (
	"path/filepath"
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
	if !containsFlagCode(flags, "missing_subtitles") {
		t.Fatalf("missing missing_subtitles flag; got %+v", flags)
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

func TestFlagEngineEmitsDualSubFailed(t *testing.T) {
	job := &Job{
		State:        StateCompleted,
		DualSubError: "no subtitle tracks found",
	}
	flags := ComputeFlags(job)
	if !containsFlagCode(flags, "dualsub_failed") {
		t.Fatalf("missing dualsub_failed flag; got %+v", flags)
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

	got, err := flagsByPath(db, "/movies/Foo (2024)/Foo.mkv")
	if err != nil || got == nil {
		t.Fatalf("flagsByPath err=%v row=%v", err, got)
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
	got, _ = flagsByPath(db, "/movies/Foo (2024)/Foo.mkv")
	if got != nil {
		t.Errorf("expected row deleted, got %+v", got)
	}
}
