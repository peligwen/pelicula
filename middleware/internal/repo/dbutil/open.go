package dbutil

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Open opens (or creates) the SQLite database at path, configures it for the
// single-writer model used throughout pelicula-api, then runs migrations.
//
// Single connection (SetMaxOpenConns(1)) enforces the single-writer SQLite
// contract and eliminates SQLITE_BUSY under normal operation. WAL mode allows
// concurrent reads alongside the writer. Foreign-key enforcement is off by
// default in SQLite and must be enabled per connection. busy_timeout is
// defense-in-depth for future sibling-process scenarios — harmless now.
// component is passed to Migrate's logger so log lines identify which DB is
// being migrated (e.g. "peliculadb", "catalog").
func Open(path string, migrations []Migration, component string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}

	db.SetMaxOpenConns(1)

	pragmas := []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA busy_timeout=5000`,
	}
	for _, p := range pragmas {
		if _, err := db.ExecContext(context.Background(), p); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}

	if err := Migrate(db, migrations, component); err != nil {
		db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return db, nil
}
