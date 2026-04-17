// TODO: remove this file once legacy JSON installs are confirmed gone
// migrate_json.go — one-time migration from legacy JSON flat files to SQLite.
// Each function is idempotent: if the source files don't exist they return nil.
// On success the JSON files/directory are renamed to *.migrated so they won't
// be re-processed on the next restart.
package procula

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// migrateAllJSON orchestrates all JSON → SQLite migrations for procula.
func migrateAllJSON(db *sql.DB, configDir string) {
	migrateJobsJSON(db, configDir)
	migrateSettingsJSON(db, configDir)
}

// migrateJobsJSON reads all *.json files from configDir/jobs/, inserts them
// into the jobs table, then renames the directory to jobs.migrated.
func migrateJobsJSON(db *sql.DB, configDir string) {
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

	count := 0
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

		_, err = db.Exec(
			`INSERT INTO jobs (id, created_at, updated_at, state, stage, progress, source, validation,
			                   missing_subs, error, retry_count, manual_profile, dualsub_outputs, dualsub_error,
			                   transcode_profile, transcode_decision, transcode_outputs, transcode_error, transcode_eta)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(id) DO NOTHING`,
			job.ID,
			job.CreatedAt.UTC().Format(time.RFC3339Nano),
			job.UpdatedAt.UTC().Format(time.RFC3339Nano),
			string(job.State),
			string(job.Stage),
			job.Progress,
			string(sourceJSON),
			validationJSON,
			missingSubsJSON,
			job.Error,
			job.RetryCount,
			job.ManualProfile,
			dualSubOutputsJSON,
			job.DualSubError,
			job.TranscodeProfile,
			job.TranscodeDecision,
			transcodeOutputsJSON,
			job.TranscodeError,
			job.TranscodeETA,
		)
		if err != nil {
			slog.Warn("jobs migration: failed to insert job",
				"component", "migrate", "job_id", job.ID, "error", err)
			continue
		}
		count++
	}

	slog.Info("jobs migration complete", "component", "migrate", "count", count, "corrupt", corruptCount)

	// Rename the directory so it won't be re-processed.
	// If every JSON file was corrupt (nothing migrated), use .corrupt suffix.
	jsonCount := count + corruptCount
	dest := jobsDir + ".migrated"
	if jsonCount > 0 && count == 0 {
		dest = jobsDir + ".corrupt"
	}
	if err := os.Rename(jobsDir, dest); err != nil {
		slog.Warn("jobs migration: could not rename jobs dir", "component", "migrate", "error", err)
	}
}

// migrateSettingsJSON reads configDir/procula/settings.json, inserts it into
// the settings table, then renames the file to settings.json.migrated.
func migrateSettingsJSON(db *sql.DB, configDir string) {
	settingsPath := filepath.Join(configDir, "procula", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		// Non-NotExist error: log and retry next startup.
		slog.Warn("settings migration: cannot read file, will retry next startup", "component", "migrate", "path", settingsPath, "error", err)
		return
	}

	// Validate it parses as PipelineSettings
	var s PipelineSettings
	if err := json.Unmarshal(data, &s); err != nil {
		slog.Warn("settings migration: corrupt JSON, renaming to .corrupt", "component", "migrate", "path", settingsPath, "error", err)
		corruptDest := settingsPath + ".corrupt"
		if renameErr := os.Rename(settingsPath, corruptDest); renameErr != nil {
			slog.Warn("settings migration: could not rename corrupt file", "component", "migrate", "path", settingsPath, "error", renameErr)
		}
		return
	}

	// Re-marshal to ensure we store a clean canonical form
	canonical, _ := json.Marshal(s)
	_, err = db.Exec(
		`INSERT INTO settings (key, value) VALUES ('pipeline', ?)
		 ON CONFLICT(key) DO NOTHING`,
		string(canonical),
	)
	if err != nil {
		slog.Warn("settings migration: failed to insert settings", "component", "migrate", "error", err)
	}

	slog.Info("settings migration complete", "component", "migrate")
	markProculaMigrated(settingsPath)
}

// markProculaMigrated renames path → path.migrated.
func markProculaMigrated(path string) {
	if err := os.Rename(path, path+".migrated"); err != nil {
		slog.Warn("could not rename migrated JSON file", "component", "migrate", "path", path, "error", err)
	}
}
