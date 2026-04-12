package main

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	_ "modernc.org/sqlite"
)

// CatalogItem is a single row in catalog_items.
// Movies and series are root items (ParentID = "").
// Seasons reference a series. Episodes reference a season.
type CatalogItem struct {
	ID               string
	Type             string // "movie" | "series" | "season" | "episode"
	ParentID         string // "" for movies/series
	TmdbID           int
	TvdbID           int
	ArrID            int
	ArrType          string // "radarr" | "sonarr"
	JellyfinID       string
	EpisodeID        int // Sonarr episode ID (0 for non-episodes)
	SeasonNumber     int
	EpisodeNumber    int
	Title            string
	Year             int
	Tier             string // "queue" | "pipeline" | "library"
	ArtworkURL       string
	Synopsis         string
	MetadataSyncedAt string // RFC3339 timestamp; "" = never synced
	ProculaJobID     string // most recent procula job ID
	FilePath         string
	CreatedAt        string
	UpdatedAt        string
}

// OpenCatalogDB opens (or creates) the catalog SQLite database and runs migrations.
func OpenCatalogDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open catalog sqlite %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("catalog db WAL: %w", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		db.Close()
		return nil, fmt.Errorf("catalog db foreign keys: %w", err)
	}
	if err := runCatalogMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("catalog db migrations: %w", err)
	}
	return db, nil
}

type catalogMigration struct {
	version int
	up      func(tx *sql.Tx) error
}

var catalogMigrations = []catalogMigration{
	{version: 1, up: catalogMigrate1},
}

func runCatalogMigrations(db *sql.DB) error {
	var ver int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&ver); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	for _, m := range catalogMigrations {
		if m.version <= ver {
			continue
		}
		slog.Info("applying catalog DB migration", "component", "catalog_db", "version", m.version)
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", m.version, err)
		}
		if err := m.up(tx); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", m.version, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, m.version)); err != nil {
			tx.Rollback()
			return fmt.Errorf("set user_version %d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", m.version, err)
		}
		slog.Info("catalog DB migration applied", "component", "catalog_db", "version", m.version)
	}
	return nil
}

func catalogMigrate1(tx *sql.Tx) error {
	_, err := tx.Exec(`
		CREATE TABLE IF NOT EXISTS catalog_items (
			id                 TEXT PRIMARY KEY,
			type               TEXT NOT NULL,
			parent_id          TEXT NOT NULL DEFAULT '',
			tmdb_id            INTEGER NOT NULL DEFAULT 0,
			tvdb_id            INTEGER NOT NULL DEFAULT 0,
			arr_id             INTEGER NOT NULL DEFAULT 0,
			arr_type           TEXT NOT NULL DEFAULT '',
			jellyfin_id        TEXT NOT NULL DEFAULT '',
			episode_id         INTEGER NOT NULL DEFAULT 0,
			season_number      INTEGER NOT NULL DEFAULT 0,
			episode_number     INTEGER NOT NULL DEFAULT 0,
			title              TEXT NOT NULL,
			year               INTEGER NOT NULL DEFAULT 0,
			tier               TEXT NOT NULL,
			artwork_url        TEXT NOT NULL DEFAULT '',
			synopsis           TEXT NOT NULL DEFAULT '',
			metadata_synced_at TEXT NOT NULL DEFAULT '',
			procula_job_id     TEXT NOT NULL DEFAULT '',
			file_path          TEXT NOT NULL DEFAULT '',
			created_at         TEXT NOT NULL,
			updated_at         TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_catalog_tmdb
			ON catalog_items(tmdb_id) WHERE tmdb_id != 0;
		CREATE INDEX IF NOT EXISTS idx_catalog_tvdb
			ON catalog_items(tvdb_id) WHERE tvdb_id != 0;
		CREATE INDEX IF NOT EXISTS idx_catalog_arr
			ON catalog_items(arr_id, arr_type) WHERE arr_id != 0;
		CREATE INDEX IF NOT EXISTS idx_catalog_parent
			ON catalog_items(parent_id) WHERE parent_id != '';
		CREATE INDEX IF NOT EXISTS idx_catalog_tier
			ON catalog_items(tier);
		CREATE INDEX IF NOT EXISTS idx_catalog_path
			ON catalog_items(file_path) WHERE file_path != '';
	`)
	return err
}

func newCatalogID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return fmt.Sprintf("cat_%d_%x", time.Now().UnixNano(), b)
}
