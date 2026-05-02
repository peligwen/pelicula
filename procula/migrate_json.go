// migrate_json.go — one-time migration from legacy JSON flat files to SQLite.
// Each function is idempotent: if the source files don't exist they return nil.
// On success the JSON files/directory are renamed to *.migrated so they won't
// be re-processed on the next restart.
package procula

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// markMigrated is a package-level var so tests can override it to simulate
// rename failures without touching the filesystem.
var markMigrated = func(path string) error {
	return os.Rename(path, path+".migrated")
}

// migrateAllJSON orchestrates all JSON → SQLite migrations for procula.
func migrateAllJSON(db *sql.DB, configDir string) {
	ctx := context.Background()
	migrateJobsJSON(ctx, db, configDir)
	migrateSettingsJSON(ctx, db, configDir)
}

// migrateJobsJSON reads all *.json files from configDir/jobs/, inserts them
// into the jobs table within a single transaction, records the directory in
// migrated_json_files for idempotency, then renames the directory.
//
// Corrupt per-file JSON is skipped (renamed to .corrupt) before the
// transaction opens — parse failures are a per-file concern, not a batch
// rollback condition. Only DB-level errors roll back the transaction.
func migrateJobsJSON(ctx context.Context, db *sql.DB, configDir string) {
	jobsDir := filepath.Join(configDir, "jobs")
	if _, err := os.Stat(jobsDir); os.IsNotExist(err) {
		return
	}

	// If jobs.migrated already exists, the migration ran successfully on a prior
	// start. The jobs/ dir is leftover (e.g. recreated by a Docker volume mount).
	// Remove it and skip — re-processing is safe but wasteful, and the rename will
	// fail again producing the same warning.
	migratedDir := filepath.Join(configDir, "jobs.migrated")
	if _, err := os.Stat(migratedDir); err == nil {
		os.RemoveAll(jobsDir)
		return
	}

	entries, err := os.ReadDir(jobsDir)
	if err != nil {
		slog.Warn("jobs migration: cannot read jobs dir", "component", "migrate", "dir", jobsDir, "error", err)
		return
	}

	type parsedJob struct {
		job              Job
		sourceJSON       string
		validationJSON   *string
		missingSubsJSON  *string
		dualSubOutputs   *string
		transcodeOutputs *string
	}

	var batch []parsedJob
	corruptCount := 0

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(jobsDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			slog.Warn("jobs migration: cannot read file", "component", "migrate", "file", entry.Name(), "error", err)
			continue
		}

		var job Job
		if err := json.Unmarshal(data, &job); err != nil {
			slog.Warn("jobs migration: corrupt JSON, renaming to .corrupt", "component", "migrate", "file", entry.Name(), "error", err)
			corruptDest := path + ".corrupt"
			if renameErr := os.Rename(path, corruptDest); renameErr != nil {
				slog.Warn("jobs migration: could not rename corrupt file", "component", "migrate", "file", entry.Name(), "error", renameErr)
			}
			corruptCount++
			continue
		}

		pj := parsedJob{job: job}
		pj.sourceJSON = func() string { b, _ := json.Marshal(job.Source); return string(b) }()

		if job.Validation != nil {
			b, _ := json.Marshal(job.Validation)
			s := string(b)
			pj.validationJSON = &s
		}
		if job.MissingSubs != nil {
			b, _ := json.Marshal(job.MissingSubs)
			s := string(b)
			pj.missingSubsJSON = &s
		}
		if job.DualSubOutputs != nil {
			b, _ := json.Marshal(job.DualSubOutputs)
			s := string(b)
			pj.dualSubOutputs = &s
		}
		if job.TranscodeOutputs != nil {
			b, _ := json.Marshal(job.TranscodeOutputs)
			s := string(b)
			pj.transcodeOutputs = &s
		}

		batch = append(batch, pj)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		slog.Warn("jobs migration: begin tx failed", "component", "migrate", "error", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	var dummy int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM migrated_json_files WHERE path = ?`, jobsDir).Scan(&dummy)
	if err == nil {
		// Already migrated — nothing to do.
		_ = tx.Commit()
		os.RemoveAll(jobsDir)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("jobs migration: check migrated_json_files failed", "component", "migrate", "error", err)
		return
	}

	count := 0
	for _, pj := range batch {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO jobs (id, created_at, updated_at, state, stage, progress, source, validation,
			                   missing_subs, error, retry_count, manual_profile, dualsub_outputs, dualsub_error,
			                   transcode_profile, transcode_decision, transcode_outputs, transcode_error, transcode_eta)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(id) DO NOTHING`,
			pj.job.ID,
			pj.job.CreatedAt.UTC().Format(time.RFC3339Nano),
			pj.job.UpdatedAt.UTC().Format(time.RFC3339Nano),
			string(pj.job.State),
			string(pj.job.Stage),
			pj.job.Progress,
			pj.sourceJSON,
			pj.validationJSON,
			pj.missingSubsJSON,
			pj.job.Error,
			pj.job.RetryCount,
			pj.job.ManualProfile,
			pj.dualSubOutputs,
			pj.job.DualSubError,
			pj.job.TranscodeProfile,
			pj.job.TranscodeDecision,
			pj.transcodeOutputs,
			pj.job.TranscodeError,
			pj.job.TranscodeETA,
		)
		if err != nil {
			slog.Warn("jobs migration: failed to insert job, rolling back",
				"component", "migrate", "job_id", pj.job.ID, "error", err)
			return
		}
		count++
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO migrated_json_files (path, migrated_at) VALUES (?, ?)`,
		jobsDir, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		slog.Warn("jobs migration: record migrated_json_files failed", "component", "migrate", "error", err)
		return
	}

	if err := tx.Commit(); err != nil {
		slog.Warn("jobs migration: commit failed", "component", "migrate", "error", err)
		return
	}

	slog.Info("jobs migration complete", "component", "migrate", "count", count, "corrupt", corruptCount)

	// Rename the directory so it won't be re-processed.
	// If every JSON file was corrupt (nothing migrated), use .corrupt suffix.
	// Either way the migrated_json_files row prevents re-import on future starts
	// even if the rename fails — so this is cosmetic.
	jsonCount := count + corruptCount
	if jsonCount > 0 && count == 0 {
		if err := os.Rename(jobsDir, jobsDir+".corrupt"); err != nil {
			slog.Warn("jobs migration: could not rename jobs dir to .corrupt", "component", "migrate", "error", err)
		}
	} else {
		if err := markMigrated(jobsDir); err != nil {
			slog.Warn("jobs migration: could not rename jobs dir", "component", "migrate", "error", err)
		}
	}
}

// migrateSettingsJSON reads configDir/procula/settings.json, inserts it into
// the settings table within a single transaction, records the path in
// migrated_json_files for idempotency, then renames the file.
func migrateSettingsJSON(ctx context.Context, db *sql.DB, configDir string) {
	settingsPath := filepath.Join(configDir, "procula", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		slog.Warn("settings migration: cannot read file, will retry next startup", "component", "migrate", "path", settingsPath, "error", err)
		return
	}

	var s PipelineSettings
	if err := json.Unmarshal(data, &s); err != nil {
		slog.Warn("settings migration: corrupt JSON, renaming to .corrupt", "component", "migrate", "path", settingsPath, "error", err)
		corruptDest := settingsPath + ".corrupt"
		if renameErr := os.Rename(settingsPath, corruptDest); renameErr != nil {
			slog.Warn("settings migration: could not rename corrupt file", "component", "migrate", "path", settingsPath, "error", renameErr)
		}
		return
	}

	canonical, _ := json.Marshal(s)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		slog.Warn("settings migration: begin tx failed", "component", "migrate", "error", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	var dummy int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM migrated_json_files WHERE path = ?`, settingsPath).Scan(&dummy)
	if err == nil {
		// Already migrated — nothing to do.
		_ = tx.Commit()
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("settings migration: check migrated_json_files failed", "component", "migrate", "error", err)
		return
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES ('pipeline', ?)
		 ON CONFLICT(key) DO NOTHING`,
		string(canonical),
	); err != nil {
		slog.Warn("settings migration: failed to insert settings", "component", "migrate", "error", err)
		return
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO migrated_json_files (path, migrated_at) VALUES (?, ?)`,
		settingsPath, time.Now().UTC().Format(time.RFC3339),
	); err != nil {
		slog.Warn("settings migration: record migrated_json_files failed", "component", "migrate", "error", err)
		return
	}

	if err := tx.Commit(); err != nil {
		slog.Warn("settings migration: commit failed", "component", "migrate", "error", err)
		return
	}

	slog.Info("settings migration complete", "component", "migrate")
	if err := markMigrated(settingsPath); err != nil {
		slog.Warn("settings migration: could not rename migrated file (cosmetic)", "component", "migrate", "path", settingsPath, "error", err)
	}
}
