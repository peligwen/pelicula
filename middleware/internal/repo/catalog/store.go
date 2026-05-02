// Package catalog provides a typed data-access store for the catalog_items
// table. The store owns all SQL; domain logic lives in internal/app/catalog.
package catalog

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// newID generates a collision-resistant catalog item ID.
func newID() string {
	b := make([]byte, 6)
	rand.Read(b) //nolint:errcheck
	return fmt.Sprintf("cat_%d_%x", time.Now().UnixNano(), b)
}

// Item is a single row in catalog_items.
// Movies and series are root items (ParentID = "").
// Seasons reference a series. Episodes reference a season.
//
// Time columns (CreatedAt, UpdatedAt, MetadataSyncedAt) are stored and
// returned as RFC3339 strings, matching the existing schema convention.
type Item struct {
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

// Filter controls which items List returns.
type Filter struct {
	Type  string // "" = all types
	Tier  string // "" = all tiers
	Query string // case-insensitive substring match on title
	Limit int    // 0 = default 100
}

// Store wraps a *sql.DB and provides named methods for catalog_items access.
type Store struct {
	db *sql.DB
}

// New returns a Store backed by db.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// ── internal helpers ──────────────────────────────────────────────────────────

const selectItem = `
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

func scanItem(s scanner) (*Item, error) {
	var it Item
	err := s.Scan(
		&it.ID, &it.Type, &it.ParentID, &it.TmdbID, &it.TvdbID,
		&it.ArrID, &it.ArrType, &it.JellyfinID, &it.EpisodeID,
		&it.SeasonNumber, &it.EpisodeNumber, &it.Title, &it.Year,
		&it.Tier, &it.ArtworkURL, &it.Synopsis, &it.MetadataSyncedAt,
		&it.ProculaJobID, &it.FilePath, &it.CreatedAt, &it.UpdatedAt,
	)
	if err == nil {
		return &it, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return nil, err
}

// tierRank returns a numeric rank for a tier (higher = more advanced).
func tierRank(tier string) int {
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

// coalesce returns a if non-empty, otherwise b.
func coalesce(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// querier is satisfied by both *sql.DB and *sql.Tx.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// findExisting looks up an item by natural key using q. Returns nil, nil if not found.
func findExisting(ctx context.Context, q querier, item Item) (*Item, error) {
	tryRow := func(query string, args ...any) (*Item, error) {
		return scanItem(q.QueryRowContext(ctx, query, args...))
	}

	var row *sql.Row
	switch item.Type {
	case "movie":
		if item.TmdbID != 0 {
			it, err := tryRow(selectItem+` WHERE type='movie' AND tmdb_id=?`, item.TmdbID)
			if err != nil || it != nil {
				return it, err
			}
		}
		if item.ArrID != 0 {
			row = q.QueryRowContext(ctx, selectItem+` WHERE type='movie' AND arr_id=? AND arr_type=?`, item.ArrID, item.ArrType)
		}
	case "series":
		if item.TvdbID != 0 {
			it, err := tryRow(selectItem+` WHERE type='series' AND tvdb_id=?`, item.TvdbID)
			if err != nil || it != nil {
				return it, err
			}
		}
		if item.TmdbID != 0 {
			it, err := tryRow(selectItem+` WHERE type='series' AND tmdb_id=?`, item.TmdbID)
			if err != nil || it != nil {
				return it, err
			}
		}
		if item.ArrID != 0 {
			row = q.QueryRowContext(ctx, selectItem+` WHERE type='series' AND arr_id=? AND arr_type=?`, item.ArrID, item.ArrType)
		}
	case "season":
		if item.ParentID != "" {
			row = q.QueryRowContext(ctx, selectItem+` WHERE type='season' AND parent_id=? AND season_number=?`, item.ParentID, item.SeasonNumber)
		}
	case "episode":
		if item.EpisodeID != 0 {
			row = q.QueryRowContext(ctx, selectItem+` WHERE type='episode' AND episode_id=?`, item.EpisodeID)
		} else if item.ParentID != "" {
			row = q.QueryRowContext(ctx, selectItem+` WHERE type='episode' AND parent_id=? AND episode_number=?`, item.ParentID, item.EpisodeNumber)
		}
	}
	if row == nil {
		return nil, nil
	}
	return scanItem(row)
}

// ── Public methods ────────────────────────────────────────────────────────────

// Upsert finds an existing item by natural key and updates it, or inserts a
// new record. Tier is never downgraded. Returns the ID of the upserted item.
//
// Upsert is atomic against concurrent callers with the same natural key:
// the find + update/insert run inside a single BeginTx/Commit, so SQLite's
// writer-serialization prevents duplicate inserts.
func (s *Store) Upsert(ctx context.Context, item Item) (string, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin catalog upsert tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck — safe no-op after Commit

	existing, err := findExisting(ctx, tx, item)
	if err != nil {
		return "", fmt.Errorf("find catalog item: %w", err)
	}

	if existing != nil {
		tier := item.Tier
		if tierRank(existing.Tier) > tierRank(tier) {
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

		_, err = tx.ExecContext(ctx, `
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
		if err := tx.Commit(); err != nil {
			return "", fmt.Errorf("commit catalog update %s: %w", existing.ID, err)
		}
		return existing.ID, nil
	}

	if item.ID == "" {
		item.ID = newID()
	}
	_, err = tx.ExecContext(ctx, `
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
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit catalog insert: %w", err)
	}
	return item.ID, nil
}

// Get fetches a catalog item by its internal ID.
// Returns (nil, nil) if not found.
func (s *Store) Get(ctx context.Context, id string) (*Item, error) {
	return scanItem(s.db.QueryRowContext(ctx, selectItem+` WHERE id=?`, id))
}

// GetByFilePath fetches a catalog item by its file_path.
// Returns (nil, nil) if no item matches.
func (s *Store) GetByFilePath(ctx context.Context, filePath string) (*Item, error) {
	return scanItem(s.db.QueryRowContext(ctx, selectItem+` WHERE file_path=?`, filePath))
}

// List returns catalog items matching the filter, ordered by updated_at DESC.
func (s *Store) List(ctx context.Context, f Filter) ([]Item, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	query := selectItem + ` WHERE 1=1`
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

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list catalog items: %w", err)
	}
	defer rows.Close()

	items := []Item{}
	for rows.Next() {
		it, err := scanItem(rows)
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

// UpdateMetadata sets Jellyfin-sourced fields on a catalog item.
func (s *Store) UpdateMetadata(ctx context.Context, id, jellyfinID, artworkURL, synopsis, syncedAt string) error {
	result, err := s.db.ExecContext(ctx, `
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
