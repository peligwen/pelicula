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
