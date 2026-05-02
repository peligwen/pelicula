package catalog

import (
	"context"
	"database/sql"
	"fmt"

	repocatalog "pelicula-api/internal/repo/catalog"
	"pelicula-api/internal/repo/dbutil"
)

// CatalogItem is an alias for the repo type so all in-package callers and
// existing tests continue to compile without modification.
type CatalogItem = repocatalog.Item

// CatalogFilter is an alias for the repo filter type.
type CatalogFilter = repocatalog.Filter

// OpenCatalogDB opens (or creates) the catalog SQLite database and runs migrations.
func OpenCatalogDB(path string) (*sql.DB, error) {
	return dbutil.Open(path, catalogMigrations, "catalog")
}

// catalogMigrations is the ordered list of all schema migrations for catalog.db.
var catalogMigrations = []dbutil.Migration{
	{Version: 1, Up: catalogMigrate1},
}

func catalogMigrate1(tx *sql.Tx) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS catalog_items (
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
		)`,
		`CREATE INDEX IF NOT EXISTS idx_catalog_tmdb
			ON catalog_items(tmdb_id) WHERE tmdb_id != 0`,
		`CREATE INDEX IF NOT EXISTS idx_catalog_tvdb
			ON catalog_items(tvdb_id) WHERE tvdb_id != 0`,
		`CREATE INDEX IF NOT EXISTS idx_catalog_arr
			ON catalog_items(arr_id, arr_type) WHERE arr_id != 0`,
		`CREATE INDEX IF NOT EXISTS idx_catalog_parent
			ON catalog_items(parent_id) WHERE parent_id != ''`,
		`CREATE INDEX IF NOT EXISTS idx_catalog_tier
			ON catalog_items(tier)`,
		`CREATE INDEX IF NOT EXISTS idx_catalog_path
			ON catalog_items(file_path) WHERE file_path != ''`,
	}

	for _, stmt := range stmts {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt[:min(40, len(stmt))], err)
		}
	}
	return nil
}

// ── Package-level helpers delegating to repocatalog.Store ─────────────────────

func storeFor(db *sql.DB) *repocatalog.Store {
	return repocatalog.New(db)
}

// UpsertCatalogItem finds an existing item by natural key and updates it,
// or inserts a new record. Tier is never downgraded.
// Returns the ID of the upserted item.
func UpsertCatalogItem(ctx context.Context, db *sql.DB, item CatalogItem) (string, error) {
	return storeFor(db).Upsert(ctx, item)
}

// GetCatalogItemByID fetches a catalog item by its internal ID.
// Returns nil, nil if not found.
func GetCatalogItemByID(ctx context.Context, db *sql.DB, id string) (*CatalogItem, error) {
	return storeFor(db).Get(ctx, id)
}

// GetCatalogItemByFilePath fetches a catalog item by its file_path.
// Returns (nil, nil) if no item matches.
func GetCatalogItemByFilePath(ctx context.Context, db *sql.DB, filePath string) (*CatalogItem, error) {
	return storeFor(db).GetByFilePath(ctx, filePath)
}

// ListCatalogItems returns catalog items matching the filter, ordered by updated_at DESC.
func ListCatalogItems(ctx context.Context, db *sql.DB, f CatalogFilter) ([]CatalogItem, error) {
	return storeFor(db).List(ctx, f)
}

// UpdateCatalogMetadata sets Jellyfin-sourced fields on a catalog item.
func UpdateCatalogMetadata(ctx context.Context, db *sql.DB, id, jellyfinID, artworkURL, synopsis, syncedAt string) error {
	return storeFor(db).UpdateMetadata(ctx, id, jellyfinID, artworkURL, synopsis, syncedAt)
}
