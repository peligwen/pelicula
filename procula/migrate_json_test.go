package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMigrateJobsJSON_HappyPath(t *testing.T) {
	db := testDB(t)
	configDir := t.TempDir()

	jobsDir := filepath.Join(configDir, "jobs")
	if err := os.MkdirAll(jobsDir, 0755); err != nil {
		t.Fatalf("mkdir jobs: %v", err)
	}

	// Write two job JSON files
	now := time.Now().UTC().Truncate(time.Second)
	jobs := []Job{
		{
			ID:        "job_001",
			CreatedAt: now,
			UpdatedAt: now,
			State:     StateCompleted,
			Stage:     StageDone,
			Progress:  1.0,
			Source:    testSource("/movies/movie1.mkv"),
		},
		{
			ID:         "job_002",
			CreatedAt:  now,
			UpdatedAt:  now,
			State:      StateFailed,
			Stage:      StageValidate,
			Progress:   0.33,
			Source:     testSource("/movies/movie2.mkv"),
			Error:      "corrupt file",
			RetryCount: 2,
			Validation: &ValidationResult{
				Passed: false,
				Checks: ValidationChecks{Integrity: "fail"},
			},
		},
	}

	for _, job := range jobs {
		data, _ := json.Marshal(job)
		os.WriteFile(filepath.Join(jobsDir, job.ID+".json"), data, 0644)
	}

	migrateJobsJSON(db, configDir)

	// Verify jobs were inserted
	for _, job := range jobs {
		loaded, ok := (&Queue{db: db}).Get(job.ID)
		if !ok {
			t.Errorf("job %s not found after migration", job.ID)
			continue
		}
		if loaded.State != job.State {
			t.Errorf("job %s: state = %q, want %q", job.ID, loaded.State, job.State)
		}
		if loaded.Source.Path != job.Source.Path {
			t.Errorf("job %s: path = %q, want %q", job.ID, loaded.Source.Path, job.Source.Path)
		}
	}

	// Verify job with validation
	loaded2, _ := (&Queue{db: db}).Get("job_002")
	if loaded2.Validation == nil {
		t.Error("job_002: expected validation to be non-nil")
	} else if loaded2.Validation.Checks.Integrity != "fail" {
		t.Errorf("job_002: integrity = %q, want fail", loaded2.Validation.Checks.Integrity)
	}

	// Verify jobs dir was renamed
	if _, err := os.Stat(jobsDir); !os.IsNotExist(err) {
		t.Error("jobs dir should have been renamed to jobs.migrated")
	}
	if _, err := os.Stat(jobsDir + ".migrated"); err != nil {
		t.Error("jobs.migrated dir should exist")
	}
}

func TestMigrateJobsJSON_NonexistentDir(t *testing.T) {
	db := testDB(t)
	configDir := t.TempDir()
	// No jobs dir — should not panic or error
	migrateJobsJSON(db, configDir)
}

func TestMigrateJobsJSON_CorruptFileSkipped(t *testing.T) {
	db := testDB(t)
	configDir := t.TempDir()

	jobsDir := filepath.Join(configDir, "jobs")
	os.MkdirAll(jobsDir, 0755)

	// Write a corrupt file
	os.WriteFile(filepath.Join(jobsDir, "corrupt.json"), []byte("not json{{"), 0644)

	// Write a valid file
	now := time.Now().UTC()
	valid := Job{
		ID:        "job_valid",
		CreatedAt: now,
		UpdatedAt: now,
		State:     StateQueued,
		Stage:     StageValidate,
		Source:    testSource("/movies/valid.mkv"),
	}
	data, _ := json.Marshal(valid)
	os.WriteFile(filepath.Join(jobsDir, "job_valid.json"), data, 0644)

	migrateJobsJSON(db, configDir)

	// Valid job should be inserted
	_, ok := (&Queue{db: db}).Get("job_valid")
	if !ok {
		t.Error("valid job should have been migrated")
	}
}

func TestMigrateJobsJSON_Idempotent(t *testing.T) {
	db := testDB(t)
	configDir := t.TempDir()

	jobsDir := filepath.Join(configDir, "jobs")
	os.MkdirAll(jobsDir, 0755)

	now := time.Now().UTC()
	job := Job{
		ID:        "job_dedup",
		CreatedAt: now,
		UpdatedAt: now,
		State:     StateCompleted,
		Stage:     StageDone,
		Progress:  1.0,
		Source:    testSource("/movies/film.mkv"),
	}
	data, _ := json.Marshal(job)
	os.WriteFile(filepath.Join(jobsDir, job.ID+".json"), data, 0644)

	// Run migration once
	migrateJobsJSON(db, configDir)

	// Recreate the jobs dir with the same file (simulate second run)
	os.MkdirAll(jobsDir, 0755)
	os.WriteFile(filepath.Join(jobsDir, job.ID+".json"), data, 0644)

	// Run again — ON CONFLICT DO NOTHING should prevent duplicates
	migrateJobsJSON(db, configDir)

	var count int
	db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE id=?`, job.ID).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row, got %d (ON CONFLICT failed)", count)
	}
}

func TestMigrateSettingsJSON_HappyPath(t *testing.T) {
	db := testDB(t)
	configDir := t.TempDir()

	procDir := filepath.Join(configDir, "procula")
	os.MkdirAll(procDir, 0755)

	s := PipelineSettings{
		ValidationEnabled:  true,
		TranscodingEnabled: true,
		CatalogEnabled:     false,
		NotifMode:          "apprise",
		StorageWarningPct:  80,
		StorageCriticalPct: 92,
	}
	data, _ := json.Marshal(s)
	os.WriteFile(filepath.Join(procDir, "settings.json"), data, 0644)

	migrateSettingsJSON(db, configDir)

	loaded := GetSettings(db)
	if !loaded.ValidationEnabled {
		t.Error("ValidationEnabled should be true")
	}
	if !loaded.TranscodingEnabled {
		t.Error("TranscodingEnabled should be true")
	}
	if loaded.CatalogEnabled {
		t.Error("CatalogEnabled should be false")
	}
	if loaded.NotifMode != "apprise" {
		t.Errorf("NotifMode = %q, want apprise", loaded.NotifMode)
	}
	if loaded.StorageWarningPct != 80 {
		t.Errorf("StorageWarningPct = %v, want 80", loaded.StorageWarningPct)
	}

	// Verify file was renamed
	settingsPath := filepath.Join(procDir, "settings.json")
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Error("settings.json should have been renamed")
	}
	if _, err := os.Stat(settingsPath + ".migrated"); err != nil {
		t.Error("settings.json.migrated should exist")
	}
}

func TestMigrateSettingsJSON_NonexistentFile(t *testing.T) {
	db := testDB(t)
	configDir := t.TempDir()
	// No settings file — should not panic or error
	migrateSettingsJSON(db, configDir)
}

func TestMigrateSettingsJSON_Idempotent(t *testing.T) {
	db := testDB(t)
	configDir := t.TempDir()

	procDir := filepath.Join(configDir, "procula")
	os.MkdirAll(procDir, 0755)

	s := PipelineSettings{ValidationEnabled: true, NotifMode: "internal", StorageWarningPct: 85, StorageCriticalPct: 95}
	data, _ := json.Marshal(s)
	os.WriteFile(filepath.Join(procDir, "settings.json"), data, 0644)

	migrateSettingsJSON(db, configDir)

	// Recreate file with different value
	s2 := PipelineSettings{ValidationEnabled: false, NotifMode: "direct", StorageWarningPct: 85, StorageCriticalPct: 95}
	data2, _ := json.Marshal(s2)
	os.WriteFile(filepath.Join(procDir, "settings.json"), data2, 0644)

	migrateSettingsJSON(db, configDir)

	// ON CONFLICT DO NOTHING — original value should remain
	loaded := GetSettings(db)
	if !loaded.ValidationEnabled {
		t.Error("ON CONFLICT: original value should be preserved (validation_enabled=true)")
	}
}
