package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
)

// Runtime settings an admin can change from the dashboard. Everything that
// needs a container restart lives in .env and is owned by the CLI instead.
const (
	// SettingValidationEnabled gates the ffprobe check on import. Default true.
	SettingValidationEnabled = "validation_enabled"
	// SettingAutoBlocklist controls whether a failed validation marks the
	// release failed in *arr (blocklist plus re-search) and deletes the file.
	// Default true.
	SettingAutoBlocklist = "auto_blocklist"
	// SettingAutoApprove makes viewer requests go straight to *arr without a
	// manager's approval. Default false.
	SettingAutoApprove = "auto_approve_requests"
)

// SettingDefaults is the canonical list of editable keys and their defaults.
// The API rejects unknown keys.
var SettingDefaults = map[string]string{
	SettingValidationEnabled: "true",
	SettingAutoBlocklist:     "true",
	SettingAutoApprove:       "false",
}

// GetSetting returns the stored value or def when unset.
func (s *Store) GetSetting(ctx context.Context, key, def string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return def, nil
	}
	return v, err
}

// SetSetting upserts a value.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// AllSettings returns every known setting, with defaults filled in.
func (s *Store) AllSettings(ctx context.Context) (map[string]string, error) {
	out := make(map[string]string, len(SettingDefaults))
	for k, v := range SettingDefaults {
		out[k] = v
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// BoolSetting returns a boolean setting, falling back to its default on any
// error or unparseable value.
func (s *Store) BoolSetting(ctx context.Context, key string) bool {
	def, _ := strconv.ParseBool(SettingDefaults[key])
	v, err := s.GetSetting(ctx, key, SettingDefaults[key])
	if err != nil {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
