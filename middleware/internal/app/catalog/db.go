package catalog

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
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
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("migration %d: %w", m.version, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, m.version)); err != nil {
			tx.Rollback() //nolint:errcheck
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

func newCatalogID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return fmt.Sprintf("cat_%d_%x", time.Now().UnixNano(), b)
}

// catalogTierRank returns a numeric rank (higher = more advanced).
// Tier is never downgraded — use this to compare.
func catalogTierRank(tier string) int {
	switch tier {
	case "queue":
		return 0
	case "pipeline":
		return 1
	case "library":
		return 2
	default:
		return -1
	}
}

const selectCatalogItem = `
	SELECT id, type, parent_id, tmdb_id, tvdb_id, arr_id, arr_type,
	       jellyfin_id, episode_id, season_number, episode_number,
	       title, year, tier, artwork_url, synopsis,
	       metadata_synced_at, procula_job_id, file_path, created_at, updated_at
	FROM catalog_items
`

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanCatalogRow(s scanner) (*CatalogItem, error) {
	var it CatalogItem
	err := s.Scan(
		&it.ID, &it.Type, &it.ParentID, &it.TmdbID, &it.TvdbID,
		&it.ArrID, &it.ArrType, &it.JellyfinID, &it.EpisodeID,
		&it.SeasonNumber, &it.EpisodeNumber, &it.Title, &it.Year,
		&it.Tier, &it.ArtworkURL, &it.Synopsis, &it.MetadataSyncedAt,
		&it.ProculaJobID, &it.FilePath, &it.CreatedAt, &it.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &it, err
}

// findExistingCatalogItem looks up a catalog item by its natural key.
// Returns nil, nil if not found.
func findExistingCatalogItem(db *sql.DB, item CatalogItem) (*CatalogItem, error) {
	var row *sql.Row
	tryRow := func(q string, args ...any) (*CatalogItem, error) {
		return scanCatalogRow(db.QueryRow(q, args...))
	}

	switch item.Type {
	case "movie":
		if item.TmdbID != 0 {
			it, err := tryRow(selectCatalogItem+` WHERE type='movie' AND tmdb_id=?`, item.TmdbID)
			if err != nil || it != nil {
				return it, err
			}
		}
		if item.ArrID != 0 {
			row = db.QueryRow(selectCatalogItem+` WHERE type='movie' AND arr_id=? AND arr_type=?`, item.ArrID, item.ArrType)
		}
	case "series":
		if item.TvdbID != 0 {
			it, err := tryRow(selectCatalogItem+` WHERE type='series' AND tvdb_id=?`, item.TvdbID)
			if err != nil || it != nil {
				return it, err
			}
		}
		if item.TmdbID != 0 {
			it, err := tryRow(selectCatalogItem+` WHERE type='series' AND tmdb_id=?`, item.TmdbID)
			if err != nil || it != nil {
				return it, err
			}
		}
		if item.ArrID != 0 {
			row = db.QueryRow(selectCatalogItem+` WHERE type='series' AND arr_id=? AND arr_type=?`, item.ArrID, item.ArrType)
		}
	case "season":
		if item.ParentID != "" {
			row = db.QueryRow(selectCatalogItem+` WHERE type='season' AND parent_id=? AND season_number=?`, item.ParentID, item.SeasonNumber)
		}
	case "episode":
		if item.EpisodeID != 0 {
			row = db.QueryRow(selectCatalogItem+` WHERE type='episode' AND episode_id=?`, item.EpisodeID)
		} else if item.ParentID != "" {
			row = db.QueryRow(selectCatalogItem+` WHERE type='episode' AND parent_id=? AND episode_number=?`, item.ParentID, item.EpisodeNumber)
		}
	}
	if row == nil {
		return nil, nil
	}
	return scanCatalogRow(row)
}

// coalesce returns a if non-empty, otherwise b.
func coalesce(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// UpsertCatalogItem finds an existing item by natural key and updates it,
// or inserts a new record. Tier is never downgraded.
// Returns the ID of the upserted item.
func UpsertCatalogItem(db *sql.DB, item CatalogItem) (string, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	existing, err := findExistingCatalogItem(db, item)
	if err != nil {
		return "", fmt.Errorf("find catalog item: %w", err)
	}

	if existing != nil {
		tier := item.Tier
		if catalogTierRank(existing.Tier) > catalogTierRank(tier) {
			tier = existing.Tier
		}
		artworkURL := coalesce(item.ArtworkURL, existing.ArtworkURL)
		synopsis := coalesce(item.Synopsis, existing.Synopsis)
		metadataSyncedAt := coalesce(item.MetadataSyncedAt, existing.MetadataSyncedAt)
		jellyfinID := coalesce(item.JellyfinID, existing.JellyfinID)
		proculaJobID := coalesce(item.ProculaJobID, existing.ProculaJobID)
		filePath := coalesce(item.FilePath, existing.FilePath)
		arrID := existing.ArrID
		if item.ArrID != 0 {
			arrID = item.ArrID
		}
		arrType := coalesce(item.ArrType, existing.ArrType)
		episodeID := existing.EpisodeID
		if item.EpisodeID != 0 {
			episodeID = item.EpisodeID
		}
		tmdbID := existing.TmdbID
		if item.TmdbID != 0 {
			tmdbID = item.TmdbID
		}
		tvdbID := existing.TvdbID
		if item.TvdbID != 0 {
			tvdbID = item.TvdbID
		}
		title := item.Title
		if title == "" {
			title = existing.Title
		}
		year := item.Year
		if year == 0 {
			year = existing.Year
		}

		_, err = db.Exec(`
			UPDATE catalog_items SET
				title=?, year=?, tmdb_id=?, tvdb_id=?,
				tier=?, artwork_url=?, synopsis=?,
				metadata_synced_at=?, jellyfin_id=?,
				procula_job_id=?, file_path=?,
				arr_id=?, arr_type=?, episode_id=?,
				updated_at=?
			WHERE id=?
		`, title, year, tmdbID, tvdbID,
			tier, artworkURL, synopsis, metadataSyncedAt, jellyfinID,
			proculaJobID, filePath, arrID, arrType, episodeID,
			now, existing.ID)
		if err != nil {
			return "", fmt.Errorf("update catalog item %s: %w", existing.ID, err)
		}
		return existing.ID, nil
	}

	if item.ID == "" {
		item.ID = newCatalogID()
	}
	_, err = db.Exec(`
		INSERT INTO catalog_items (
			id, type, parent_id, tmdb_id, tvdb_id, arr_id, arr_type,
			jellyfin_id, episode_id, season_number, episode_number,
			title, year, tier, artwork_url, synopsis,
			metadata_synced_at, procula_job_id, file_path, created_at, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	`, item.ID, item.Type, item.ParentID, item.TmdbID, item.TvdbID,
		item.ArrID, item.ArrType, item.JellyfinID, item.EpisodeID,
		item.SeasonNumber, item.EpisodeNumber, item.Title, item.Year,
		item.Tier, item.ArtworkURL, item.Synopsis, item.MetadataSyncedAt,
		item.ProculaJobID, item.FilePath, now, now)
	if err != nil {
		return "", fmt.Errorf("insert catalog item: %w", err)
	}
	return item.ID, nil
}

// GetCatalogItemByID fetches a catalog item by its internal ID.
// Returns nil, nil if not found.
func GetCatalogItemByID(db *sql.DB, id string) (*CatalogItem, error) {
	row := db.QueryRow(selectCatalogItem+` WHERE id=?`, id)
	return scanCatalogRow(row)
}

// GetCatalogItemByFilePath fetches a catalog item by its file_path.
// Returns (nil, nil) if no item matches.
func GetCatalogItemByFilePath(db *sql.DB, filePath string) (*CatalogItem, error) {
	row := db.QueryRow(selectCatalogItem+` WHERE file_path=?`, filePath)
	return scanCatalogRow(row)
}

// CatalogFilter controls which items ListCatalogItems returns.
type CatalogFilter struct {
	Type  string // "" = all types
	Tier  string // "" = all tiers
	Query string // case-insensitive substring match on title
	Limit int    // 0 = default 100
}

// ListCatalogItems returns catalog items matching the filter, ordered by updated_at DESC.
func ListCatalogItems(db *sql.DB, f CatalogFilter) ([]CatalogItem, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	query := selectCatalogItem + ` WHERE 1=1`
	args := []any{}
	if f.Type != "" {
		query += ` AND type=?`
		args = append(args, f.Type)
	}
	if f.Tier != "" {
		query += ` AND tier=?`
		args = append(args, f.Tier)
	}
	if f.Query != "" {
		escaped := strings.NewReplacer(`%`, `\%`, `_`, `\_`).Replace(f.Query)
		args = append(args, "%"+strings.ToLower(escaped)+"%")
		query += ` AND lower(title) LIKE ? ESCAPE '\'`
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list catalog items: %w", err)
	}
	defer rows.Close()

	items := []CatalogItem{}
	for rows.Next() {
		it, err := scanCatalogRow(rows)
		if err != nil {
			return nil, err
		}
		if it == nil {
			continue
		}
		items = append(items, *it)
	}
	return items, rows.Err()
}

// UpdateCatalogMetadata sets Jellyfin-sourced fields on a catalog item.
func UpdateCatalogMetadata(db *sql.DB, id, jellyfinID, artworkURL, synopsis, syncedAt string) error {
	result, err := db.Exec(`
		UPDATE catalog_items
		SET jellyfin_id=?, artwork_url=?, synopsis=?, metadata_synced_at=?, updated_at=?
		WHERE id=?
	`, jellyfinID, artworkURL, synopsis, syncedAt,
		time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("catalog item %s not found", id)
	}
	return nil
}
