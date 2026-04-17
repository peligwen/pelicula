// Package dbutil provides shared helpers for database access in pelicula-api.
package dbutil

import "time"

// ParseTime parses time strings stored in the DB.
// It tries RFC3339Nano first (the canonical write format) and falls back to
// RFC3339 for rows written by older code or the migration path.
func ParseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

// FormatTime formats a time for storage (RFC3339Nano, UTC).
func FormatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}
