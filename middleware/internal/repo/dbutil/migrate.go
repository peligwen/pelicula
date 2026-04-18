// migrate.go — shared schema migration runner for pelicula-api databases.
package dbutil

import (
	"database/sql"
	"fmt"
	"log/slog"
)

// Migration is a single schema migration step.
type Migration struct {
	// Version is the schema version this migration brings the DB to.
	// Versions must be contiguous and start at 1.
	Version int
	// Up applies the migration inside tx.
	Up func(tx *sql.Tx) error
}

// Migrate applies any pending migrations to db.
//
// It reads the current schema version from PRAGMA user_version, skips
// migrations already applied, and applies each pending migration inside its
// own transaction. On success the transaction sets PRAGMA user_version to the
// migration's version number. A failed migration is rolled back and Migrate
// returns the error — the DB is left at the last successfully applied version.
//
// component is used in structured log output to distinguish which DB is being
// migrated (e.g. "db" or "catalog_db").
func Migrate(db *sql.DB, migrations []Migration, component string) error {
	var ver int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}

	for _, m := range migrations {
		if m.Version <= ver {
			continue
		}
		slog.Info("applying DB migration", "component", component, "version", m.Version)
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", m.Version, err)
		}
		if err := m.Up(tx); err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("migration %d: %w", m.Version, err)
		}
		// SQLite does not allow PRAGMA user_version inside a transaction via
		// the parameter syntax, so we use string formatting (the value is
		// an int literal from our own code — not user input).
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, m.Version)); err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("set user_version %d: %w", m.Version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", m.Version, err)
		}
		slog.Info("DB migration applied", "component", component, "version", m.Version)
	}
	return nil
}
