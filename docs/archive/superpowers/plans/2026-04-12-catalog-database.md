# Pelicula Catalog Database Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a pelicula-owned `catalog.db` that stitches together Radarr/Sonarr, Jellyfin, and procula into a single record per item, tracking items across queue/pipeline/library tiers.

**Architecture:** A new SQLite database (`catalog.db`) at `/config/pelicula/catalog.db`, owned exclusively by the middleware (pelicula-api). A self-referencing `catalog_items` table models the series→season→episode hierarchy. Three sync sources populate it: the existing import webhook (sets tier `pipeline`), a new background queue poller (sets tier `queue`), and an admin backfill endpoint (scans existing Radarr/Sonarr library). Jellyfin metadata (artwork, synopsis) is fetched on-demand with a 24-hour TTL and cached in the row. Procula interacts with catalog data through the existing HTTP API boundary — no direct DB access.

**Tech Stack:** Go stdlib, `modernc.org/sqlite` (already in go.mod), standard `database/sql`

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `middleware/catalog_db.go` | **Create** | `OpenCatalogDB`, schema migrations, `CatalogItem` struct, all CRUD |
| `middleware/catalog_db_test.go` | **Create** | Tests for schema creation, upsert, tier preservation, hierarchy |
| `middleware/catalog_sync.go` | **Create** | `UpsertFromHook`, `BackfillFromArr`, `SyncJellyfinMetadata` |
| `middleware/catalog_poller.go` | **Create** | `RunQueuePoller` background goroutine |
| `middleware/main.go` | **Modify** | Add `catalogDB` global, open DB, register routes, start poller |
| `middleware/hooks.go` | **Modify** | Call `UpsertFromHook` after forwarding job to procula |
| `middleware/catalog.go` | **Modify** | Add `handleCatalogItems`, `handleCatalogItemDetail` |

---

### Task 1: catalog_db.go — Schema, OpenCatalogDB, CatalogItem

**Files:**
- Create: `middleware/catalog_db.go`
- Create: `middleware/catalog_db_test.go`

- [ ] **Step 1: Write the failing test**

Create `middleware/catalog_db_test.go`:

```go
package main

import (
	"database/sql"
	"testing"
)

func testCatalogDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenCatalogDB(":memory:")
	if err != nil {
		t.Fatalf("OpenCatalogDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenCatalogDB_CreatesSchema(t *testing.T) {
	db := testCatalogDB(t)

	// Verify the table exists with expected columns
	rows, err := db.Query(`PRAGMA table_info(catalog_items)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info: %v", err)
	}
	defer rows.Close()

	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}

	required := []string{
		"id", "type", "parent_id", "tmdb_id", "tvdb_id",
		"arr_id", "arr_type", "jellyfin_id", "episode_id",
		"season_number", "episode_number", "title", "year",
		"tier", "artwork_url", "synopsis", "metadata_synced_at",
		"procula_job_id", "file_path", "created_at", "updated_at",
	}
	for _, col := range required {
		if !cols[col] {
			t.Errorf("missing column: %s", col)
		}
	}
}
```

- [ ] **Step 2: Run test to confirm it fails**

```
cd middleware && go test -run TestOpenCatalogDB_CreatesSchema -v ./...
```

Expected: `FAIL` — `OpenCatalogDB undefined`

- [ ] **Step 3: Write `middleware/catalog_db.go`**

```go
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
	EpisodeID        int    // Sonarr episode ID (0 for non-episodes)
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
```

- [ ] **Step 4: Run test to confirm it passes**

```
cd middleware && go test -run TestOpenCatalogDB_CreatesSchema -v ./...
```

Expected: `PASS`

- [ ] **Step 5: Commit**

```bash
cd middleware && git add catalog_db.go catalog_db_test.go
git commit -m "feat(catalog): add catalog.db schema and OpenCatalogDB"
```

---

### Task 2: catalog_db.go — CRUD Operations

**Files:**
- Modify: `middleware/catalog_db.go` (append)
- Modify: `middleware/catalog_db_test.go` (append)

- [ ] **Step 1: Write failing CRUD tests**

Append to `middleware/catalog_db_test.go`:

```go
func TestUpsertCatalogItem_Movie_InsertAndFind(t *testing.T) {
	db := testCatalogDB(t)

	item := CatalogItem{
		Type:    "movie",
		TmdbID:  123,
		ArrID:   42,
		ArrType: "radarr",
		Title:   "The Matrix",
		Year:    1999,
		Tier:    "pipeline",
	}
	id, err := UpsertCatalogItem(db, item)
	if err != nil {
		t.Fatalf("UpsertCatalogItem: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty ID")
	}

	got, err := GetCatalogItemByID(db, id)
	if err != nil {
		t.Fatalf("GetCatalogItemByID: %v", err)
	}
	if got.TmdbID != 123 || got.Title != "The Matrix" || got.Tier != "pipeline" {
		t.Errorf("unexpected item: %+v", got)
	}
}

func TestUpsertCatalogItem_TierNotDowngraded(t *testing.T) {
	db := testCatalogDB(t)

	// Insert at pipeline tier
	id, err := UpsertCatalogItem(db, CatalogItem{
		Type: "movie", TmdbID: 999, ArrType: "radarr",
		Title: "Dune", Year: 2021, Tier: "pipeline",
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Attempt to downgrade to queue
	_, err = UpsertCatalogItem(db, CatalogItem{
		Type: "movie", TmdbID: 999, ArrType: "radarr",
		Title: "Dune", Year: 2021, Tier: "queue",
	})
	if err != nil {
		t.Fatalf("upsert downgrade: %v", err)
	}

	got, err := GetCatalogItemByID(db, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Tier != "pipeline" {
		t.Errorf("tier was downgraded: got %q, want %q", got.Tier, "pipeline")
	}
}

func TestUpsertCatalogItem_EpisodeHierarchy(t *testing.T) {
	db := testCatalogDB(t)

	// Insert series
	seriesID, err := UpsertCatalogItem(db, CatalogItem{
		Type: "series", TvdbID: 81189, ArrType: "sonarr",
		Title: "Breaking Bad", Year: 2008, Tier: "library",
	})
	if err != nil {
		t.Fatalf("insert series: %v", err)
	}

	// Insert season
	seasonID, err := UpsertCatalogItem(db, CatalogItem{
		Type: "season", ParentID: seriesID,
		SeasonNumber: 1, Title: "Breaking Bad Season 1",
		Year: 2008, Tier: "library",
	})
	if err != nil {
		t.Fatalf("insert season: %v", err)
	}

	// Insert episode
	epID, err := UpsertCatalogItem(db, CatalogItem{
		Type: "episode", ParentID: seasonID,
		EpisodeID: 55, SeasonNumber: 1, EpisodeNumber: 1,
		ArrType: "sonarr", FilePath: "/media/bb/s01e01.mkv",
		Title: "Pilot", Year: 2008, Tier: "library",
	})
	if err != nil {
		t.Fatalf("insert episode: %v", err)
	}

	// Re-upsert same episode by EpisodeID — should update, not duplicate
	_, err = UpsertCatalogItem(db, CatalogItem{
		Type: "episode", ParentID: seasonID,
		EpisodeID: 55, SeasonNumber: 1, EpisodeNumber: 1,
		ArrType: "sonarr", FilePath: "/media/bb/s01e01.mkv",
		Title: "Pilot", Year: 2008, Tier: "library",
		ProculaJobID: "job_123",
	})
	if err != nil {
		t.Fatalf("re-upsert episode: %v", err)
	}

	got, err := GetCatalogItemByID(db, epID)
	if err != nil {
		t.Fatalf("get episode: %v", err)
	}
	if got.ProculaJobID != "job_123" {
		t.Errorf("expected ProculaJobID updated, got %q", got.ProculaJobID)
	}

	// Verify no duplicate was inserted
	items, err := ListCatalogItems(db, CatalogFilter{Type: "episode"})
	if err != nil {
		t.Fatalf("list episodes: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("expected 1 episode, got %d", len(items))
	}
}

func TestListCatalogItems_FilterByTier(t *testing.T) {
	db := testCatalogDB(t)

	for _, tc := range []struct {
		tmdb int
		tier string
	}{
		{1, "queue"},
		{2, "pipeline"},
		{3, "library"},
	} {
		_, err := UpsertCatalogItem(db, CatalogItem{
			Type: "movie", TmdbID: tc.tmdb, ArrType: "radarr",
			Title: "Movie", Year: 2020, Tier: tc.tier,
		})
		if err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	libs, err := ListCatalogItems(db, CatalogFilter{Tier: "library"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(libs) != 1 || libs[0].TmdbID != 3 {
		t.Errorf("expected 1 library item, got %d", len(libs))
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```
cd middleware && go test -run "TestUpsertCatalogItem|TestListCatalogItems" -v ./...
```

Expected: `FAIL` — `UpsertCatalogItem undefined`

- [ ] **Step 3: Append CRUD functions to `middleware/catalog_db.go`**

```go
// catalogTierRank returns a numeric rank for tier comparison (higher = more advanced).
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

func scanCatalogRow(rows interface {
	Scan(dest ...any) error
}) (*CatalogItem, error) {
	var it CatalogItem
	return &it, rows.Scan(
		&it.ID, &it.Type, &it.ParentID, &it.TmdbID, &it.TvdbID,
		&it.ArrID, &it.ArrType, &it.JellyfinID, &it.EpisodeID,
		&it.SeasonNumber, &it.EpisodeNumber, &it.Title, &it.Year,
		&it.Tier, &it.ArtworkURL, &it.Synopsis, &it.MetadataSyncedAt,
		&it.ProculaJobID, &it.FilePath, &it.CreatedAt, &it.UpdatedAt,
	)
}

// GetCatalogItemByID fetches a catalog item by its internal ID.
// Returns nil, nil if not found.
func GetCatalogItemByID(db *sql.DB, id string) (*CatalogItem, error) {
	row := db.QueryRow(selectCatalogItem+` WHERE id = ?`, id)
	it, err := scanCatalogRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return it, err
}

// findExistingCatalogItem looks up a catalog item by its natural key.
// Returns nil, nil if no match exists.
func findExistingCatalogItem(db *sql.DB, item CatalogItem) (*CatalogItem, error) {
	var row *sql.Row
	switch item.Type {
	case "movie":
		if item.TmdbID != 0 {
			row = db.QueryRow(selectCatalogItem+` WHERE type='movie' AND tmdb_id=?`, item.TmdbID)
		} else if item.ArrID != 0 {
			row = db.QueryRow(selectCatalogItem+` WHERE type='movie' AND arr_id=? AND arr_type=?`, item.ArrID, item.ArrType)
		}
	case "series":
		if item.TvdbID != 0 {
			row = db.QueryRow(selectCatalogItem+` WHERE type='series' AND tvdb_id=?`, item.TvdbID)
		} else if item.TmdbID != 0 {
			row = db.QueryRow(selectCatalogItem+` WHERE type='series' AND tmdb_id=?`, item.TmdbID)
		} else if item.ArrID != 0 {
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
	it, err := scanCatalogRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return it, err
}

// UpsertCatalogItem finds an existing item by natural key and updates it,
// or inserts a new record if none exists. Tier is never downgraded.
// Returns the ID of the upserted item.
func UpsertCatalogItem(db *sql.DB, item CatalogItem) (string, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	existing, err := findExistingCatalogItem(db, item)
	if err != nil {
		return "", fmt.Errorf("find catalog item: %w", err)
	}

	if existing != nil {
		// Never downgrade tier
		tier := item.Tier
		if catalogTierRank(existing.Tier) > catalogTierRank(tier) {
			tier = existing.Tier
		}
		// Preserve existing values when incoming fields are zero/empty
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

		_, err = db.Exec(`
			UPDATE catalog_items SET
				tier=?, artwork_url=?, synopsis=?,
				metadata_synced_at=?, jellyfin_id=?,
				procula_job_id=?, file_path=?,
				arr_id=?, arr_type=?, episode_id=?,
				updated_at=?
			WHERE id=?
		`, tier, artworkURL, synopsis, metadataSyncedAt, jellyfinID,
			proculaJobID, filePath, arrID, arrType, episodeID,
			now, existing.ID)
		if err != nil {
			return "", fmt.Errorf("update catalog item %s: %w", existing.ID, err)
		}
		return existing.ID, nil
	}

	// Insert new record
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

// CatalogFilter controls which items ListCatalogItems returns.
type CatalogFilter struct {
	Type  string // "" = all types
	Tier  string // "" = all tiers
	Query string // substring match on title (case-insensitive)
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
		query += ` AND lower(title) LIKE ?`
		args = append(args, "%"+strings.ToLower(f.Query)+"%")
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list catalog items: %w", err)
	}
	defer rows.Close()

	var items []CatalogItem
	for rows.Next() {
		it, err := scanCatalogRow(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *it)
	}
	return items, rows.Err()
}

// UpdateCatalogMetadata sets Jellyfin-sourced fields on a catalog item.
func UpdateCatalogMetadata(db *sql.DB, id, jellyfinID, artworkURL, synopsis, syncedAt string) error {
	_, err := db.Exec(`
		UPDATE catalog_items
		SET jellyfin_id=?, artwork_url=?, synopsis=?, metadata_synced_at=?, updated_at=?
		WHERE id=?
	`, jellyfinID, artworkURL, synopsis, syncedAt, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

// coalesce returns a if non-empty, otherwise b.
func coalesce(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
```

Note: add `"strings"` to the import block in `catalog_db.go`.

- [ ] **Step 4: Run tests to confirm they pass**

```
cd middleware && go test -run "TestUpsertCatalogItem|TestListCatalogItems|TestOpenCatalogDB" -v ./...
```

Expected: all `PASS`

- [ ] **Step 5: Commit**

```bash
cd middleware && git add catalog_db.go catalog_db_test.go
git commit -m "feat(catalog): add CatalogItem CRUD — upsert, get, list, tier preservation"
```

---

### Task 3: Wire catalog.db into main.go

**Files:**
- Modify: `middleware/main.go`

- [ ] **Step 1: Add `catalogDB` package-level variable**

In `middleware/main.go`, add `catalogDB` to the existing `var` block at the top of the file (lines 17–25):

```go
var (
	services       *ServiceClients
	authMiddleware *peligrosa.Auth
	inviteStore    *peligrosa.InviteStore
	requestStore   *peligrosa.RequestStore
	dismissedStore *DismissedStore
	sseHub         *SSEHub
	ssePoller      *SSEPoller
	catalogDB      *sql.DB // catalog.db — pelicula-owned item registry
)
```

Add `"database/sql"` to the import if not already present.

- [ ] **Step 2: Open catalog.db after pelicula.db**

In `middleware/main.go`, after the existing lines that open pelicula.db (lines 52–57):

```go
	db, err := OpenDB("/config/pelicula/pelicula.db")
	if err != nil {
		slog.Error("failed to open database", "component", "main", "error", err)
		os.Exit(1)
	}
```

Add immediately after:

```go
	catalogDB, err = OpenCatalogDB("/config/pelicula/catalog.db")
	if err != nil {
		slog.Error("failed to open catalog database", "component", "main", "error", err)
		os.Exit(1)
	}
```

- [ ] **Step 3: Register catalog item routes**

In `middleware/main.go`, after the existing catalog routes block (after line 186 where `/api/pelicula/catalog/detail` is registered):

```go
	// viewer+: pelicula-owned catalog item registry
	mux.Handle("/api/pelicula/catalog/items", auth.Guard(http.HandlerFunc(handleCatalogItems)))
	mux.Handle("/api/pelicula/catalog/items/{id}", auth.Guard(http.HandlerFunc(handleCatalogItemDetail)))
	// admin only: backfill catalog from existing Radarr/Sonarr library
	mux.Handle("/api/pelicula/catalog/backfill", auth.GuardAdmin(http.HandlerFunc(handleCatalogBackfill)))
```

- [ ] **Step 4: Add handler stubs to `middleware/catalog.go`**

Append to `middleware/catalog.go`:

```go
func handleCatalogItems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f := CatalogFilter{
		Type:  r.URL.Query().Get("type"),
		Tier:  r.URL.Query().Get("tier"),
		Query: r.URL.Query().Get("q"),
	}
	items, err := ListCatalogItems(catalogDB, f)
	if err != nil {
		slog.Error("list catalog items", "component", "catalog", "error", err)
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if items == nil {
		items = []CatalogItem{}
	}
	httputil.WriteJSON(w, items)
}

func handleCatalogItemDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		httputil.WriteError(w, "missing id", http.StatusBadRequest)
		return
	}
	item, err := GetCatalogItemByID(catalogDB, id)
	if err != nil {
		slog.Error("get catalog item", "component", "catalog", "id", id, "error", err)
		httputil.WriteError(w, "internal error", http.StatusInternalServerError)
		return
	}
	if item == nil {
		httputil.WriteError(w, "not found", http.StatusNotFound)
		return
	}
	// Trigger lazy metadata sync if stale (>24h or never synced)
	go maybeSyncJellyfinMetadata(item)
	httputil.WriteJSON(w, item)
}

func handleCatalogBackfill(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	go func() {
		if err := BackfillFromArr(catalogDB, services); err != nil {
			slog.Error("catalog backfill failed", "component", "catalog", "error", err)
		}
	}()
	httputil.WriteJSON(w, map[string]string{"status": "started"})
}
```

- [ ] **Step 5: Build to confirm compilation**

```
cd middleware && go build ./...
```

Expected: no errors

- [ ] **Step 6: Commit**

```bash
cd middleware && git add main.go catalog.go
git commit -m "feat(catalog): wire catalog.db into middleware, add route stubs"
```

---

### Task 4: catalog_sync.go — UpsertFromHook + hooks.go integration

This task implements the path where a completed download upserts a catalog record.

**Files:**
- Create: `middleware/catalog_sync.go`
- Modify: `middleware/hooks.go`

- [ ] **Step 1: Create `middleware/catalog_sync.go` with `UpsertFromHook`**

```go
package main

import (
	"database/sql"
	"fmt"
	"log/slog"
)

// UpsertFromHook creates or updates catalog records for a newly imported item.
// For episodes, it upserts the parent series and season records first,
// then upserts the episode, linking it into the hierarchy.
// All items are set to tier "pipeline" — they are on the filesystem but not yet
// confirmed in Jellyfin.
func UpsertFromHook(db *sql.DB, source ProculaJobSource) error {
	switch source.Type {
	case "movie":
		_, err := UpsertCatalogItem(db, CatalogItem{
			Type:    "movie",
			TmdbID:  source.TmdbID,
			ArrID:   source.ArrID,
			ArrType: source.ArrType,
			Title:   source.Title,
			Year:    source.Year,
			Tier:    "pipeline",
			FilePath: source.Path,
		})
		if err != nil {
			return fmt.Errorf("upsert movie: %w", err)
		}
		return nil

	case "episode":
		// 1. Upsert the parent series
		seriesID, err := UpsertCatalogItem(db, CatalogItem{
			Type:    "series",
			TvdbID:  source.TvdbID,
			TmdbID:  source.TmdbID,
			ArrID:   source.ArrID,
			ArrType: source.ArrType,
			Title:   source.Title,
			Year:    source.Year,
			Tier:    "pipeline",
		})
		if err != nil {
			return fmt.Errorf("upsert series: %w", err)
		}

		// 2. Upsert the season
		seasonTitle := fmt.Sprintf("%s Season %d", source.Title, source.SeasonNumber)
		seasonID, err := UpsertCatalogItem(db, CatalogItem{
			Type:         "season",
			ParentID:     seriesID,
			SeasonNumber: source.SeasonNumber,
			Title:        seasonTitle,
			Year:         source.Year,
			Tier:         "pipeline",
		})
		if err != nil {
			return fmt.Errorf("upsert season: %w", err)
		}

		// 3. Upsert the episode
		_, err = UpsertCatalogItem(db, CatalogItem{
			Type:          "episode",
			ParentID:      seasonID,
			EpisodeID:     source.EpisodeID,
			SeasonNumber:  source.SeasonNumber,
			EpisodeNumber: source.EpisodeNumber,
			ArrType:       source.ArrType,
			Title:         source.Title,
			Year:          source.Year,
			Tier:          "pipeline",
			FilePath:      source.Path,
		})
		if err != nil {
			return fmt.Errorf("upsert episode: %w", err)
		}
		return nil

	default:
		return fmt.Errorf("unknown source type %q", source.Type)
	}
}
```

- [ ] **Step 2: Call `UpsertFromHook` from `middleware/hooks.go`**

In `middleware/hooks.go`, the `handleImportHook` function calls `forwardToProcula` and then calls `requestStore.MarkAvailable`. After the `forwardToProcula` call and before `MarkAvailable`, add the catalog upsert:

Find this block (approximately lines 82–96 of hooks.go):
```go
	proculaURL := proculaURL + "/api/procula/jobs"
	if err := forwardToProcula(proculaURL, source); err != nil {
		slog.Error("failed to forward to Procula", "component", "hooks", "error", err)
		httputil.WriteJSON(w, map[string]string{"status": "queued", "warning": err.Error()})
		return
	}
```

After the `forwardToProcula` block (and before the `go requestStore.MarkAvailable(...)` call), add:

```go
	// Upsert catalog record — best-effort, non-blocking
	go func() {
		if err := UpsertFromHook(catalogDB, source); err != nil {
			slog.Error("catalog upsert from hook failed", "component", "hooks", "error", err)
		}
	}()
```

- [ ] **Step 3: Build to confirm compilation**

```
cd middleware && go build ./...
```

Expected: no errors

- [ ] **Step 4: Commit**

```bash
cd middleware && git add catalog_sync.go hooks.go
git commit -m "feat(catalog): upsert catalog records on import webhook"
```

---

### Task 5: catalog_poller.go — Background Download Queue Poller

This task adds a background goroutine that periodically queries Radarr/Sonarr's download queue and creates `queue`-tier catalog records for items not yet on the filesystem.

**Files:**
- Create: `middleware/catalog_poller.go`
- Modify: `middleware/main.go`

- [ ] **Step 1: Create `middleware/catalog_poller.go`**

```go
package main

import (
	"context"
	"database/sql"
	"log/slog"
	"time"
)

// RunQueuePoller polls Radarr and Sonarr's download queues every 60 seconds
// and upserts queue-tier catalog records for items actively downloading.
// Items already at pipeline or library tier are not downgraded.
func RunQueuePoller(ctx context.Context, db *sql.DB, svc *ServiceClients) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	// Run once immediately at startup
	pollDownloadQueue(db, svc)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pollDownloadQueue(db, svc)
		}
	}
}

func pollDownloadQueue(db *sql.DB, svc *ServiceClients) {
	sonarrKey, radarrKey, _ := svc.Keys()

	// Radarr queue
	if radarrKey != "" {
		records, err := svc.ArrGetAllQueueRecords(radarrURL, radarrKey, "/api/v3", "&includeUnknownMovieItems=false")
		if err != nil {
			slog.Error("catalog poller: radarr queue fetch", "component", "catalog_poller", "error", err)
		} else {
			for _, rec := range records {
				upsertQueueMovie(db, rec)
			}
		}
	}

	// Sonarr queue
	if sonarrKey != "" {
		records, err := svc.ArrGetAllQueueRecords(sonarrURL, sonarrKey, "/api/v3", "&includeUnknownSeriesItems=false")
		if err != nil {
			slog.Error("catalog poller: sonarr queue fetch", "component", "catalog_poller", "error", err)
		} else {
			for _, rec := range records {
				upsertQueueEpisode(db, rec)
			}
		}
	}
}

func upsertQueueMovie(db *sql.DB, rec map[string]any) {
	movie, ok := rec["movie"].(map[string]any)
	if !ok {
		return
	}
	tmdbID := int(floatVal(movie, "tmdbId"))
	arrID := int(floatVal(movie, "id"))
	title, _ := movie["title"].(string)
	year := int(floatVal(movie, "year"))
	if title == "" {
		return
	}
	if _, err := UpsertCatalogItem(db, CatalogItem{
		Type:    "movie",
		TmdbID:  tmdbID,
		ArrID:   arrID,
		ArrType: "radarr",
		Title:   title,
		Year:    year,
		Tier:    "queue",
	}); err != nil {
		slog.Error("catalog poller: upsert queue movie", "component", "catalog_poller", "title", title, "error", err)
	}
}

func upsertQueueEpisode(db *sql.DB, rec map[string]any) {
	series, ok := rec["series"].(map[string]any)
	if !ok {
		return
	}
	tvdbID := int(floatVal(series, "tvdbId"))
	arrID := int(floatVal(series, "id"))
	title, _ := series["title"].(string)
	year := int(floatVal(series, "year"))
	if title == "" {
		return
	}
	episodeID := int(floatVal(rec, "episodeId"))
	seasonNum := 0
	epNum := 0
	if episode, ok := rec["episode"].(map[string]any); ok {
		seasonNum = int(floatVal(episode, "seasonNumber"))
		epNum = int(floatVal(episode, "episodeNumber"))
	}

	// Upsert series
	seriesID, err := UpsertCatalogItem(db, CatalogItem{
		Type:    "series",
		TvdbID:  tvdbID,
		ArrID:   arrID,
		ArrType: "sonarr",
		Title:   title,
		Year:    year,
		Tier:    "queue",
	})
	if err != nil {
		slog.Error("catalog poller: upsert queue series", "component", "catalog_poller", "title", title, "error", err)
		return
	}

	// Upsert season
	seasonTitle := fmt.Sprintf("%s Season %d", title, seasonNum)
	seasonID, err := UpsertCatalogItem(db, CatalogItem{
		Type:         "season",
		ParentID:     seriesID,
		SeasonNumber: seasonNum,
		Title:        seasonTitle,
		Year:         year,
		Tier:         "queue",
	})
	if err != nil {
		slog.Error("catalog poller: upsert queue season", "component", "catalog_poller", "title", title, "error", err)
		return
	}

	// Upsert episode (no file path yet — still downloading)
	if _, err := UpsertCatalogItem(db, CatalogItem{
		Type:          "episode",
		ParentID:      seasonID,
		EpisodeID:     episodeID,
		SeasonNumber:  seasonNum,
		EpisodeNumber: epNum,
		ArrType:       "sonarr",
		Title:         title,
		Year:          year,
		Tier:          "queue",
	}); err != nil {
		slog.Error("catalog poller: upsert queue episode", "component", "catalog_poller", "title", title, "error", err)
	}
}
```

Add `"fmt"` to imports.

- [ ] **Step 2: Start the poller in `middleware/main.go`**

In `middleware/main.go`, after the existing `go ssePoller.Run(ctx)` line (approximately line 70), add:

```go
	go RunQueuePoller(ctx, catalogDB, services)
```

- [ ] **Step 3: Build to confirm compilation**

```
cd middleware && go build ./...
```

Expected: no errors

- [ ] **Step 4: Commit**

```bash
cd middleware && git add catalog_poller.go main.go
git commit -m "feat(catalog): add download queue poller for queue-tier catalog records"
```

---

### Task 6: catalog_sync.go — BackfillFromArr

This task implements the admin-triggered backfill that scans the existing Radarr/Sonarr library and upserts catalog records for everything already present, marking them `library` tier.

**Files:**
- Modify: `middleware/catalog_sync.go`

- [ ] **Step 1: Append `BackfillFromArr` to `middleware/catalog_sync.go`**

```go
// BackfillFromArr scans all movies in Radarr and all series in Sonarr,
// upserting library-tier catalog records for items that have files on disk.
// Items already in the catalog are updated; their tier is not downgraded.
func BackfillFromArr(db *sql.DB, svc *ServiceClients) error {
	sonarrKey, radarrKey, _ := svc.Keys()

	if radarrKey != "" {
		if err := backfillRadarr(db, svc, radarrKey); err != nil {
			slog.Error("backfill radarr failed", "component", "catalog_sync", "error", err)
			// Continue to Sonarr even if Radarr fails
		}
	}
	if sonarrKey != "" {
		if err := backfillSonarr(db, svc, sonarrKey); err != nil {
			slog.Error("backfill sonarr failed", "component", "catalog_sync", "error", err)
		}
	}
	slog.Info("catalog backfill complete", "component", "catalog_sync")
	return nil
}

func backfillRadarr(db *sql.DB, svc *ServiceClients, apiKey string) error {
	data, err := svc.ArrGet(radarrURL, apiKey, "/api/v3/movie")
	if err != nil {
		return fmt.Errorf("radarr list: %w", err)
	}
	var movies []map[string]any
	if err := json.Unmarshal(data, &movies); err != nil {
		return fmt.Errorf("radarr parse: %w", err)
	}

	for _, m := range movies {
		hasFile, _ := m["hasFile"].(bool)
		tier := "queue"
		if hasFile {
			tier = "library"
		}
		filePath := ""
		if mf, ok := m["movieFile"].(map[string]any); ok {
			filePath, _ = mf["path"].(string)
		}
		title, _ := m["title"].(string)
		if title == "" {
			continue
		}
		if _, err := UpsertCatalogItem(db, CatalogItem{
			Type:     "movie",
			TmdbID:   int(floatVal(m, "tmdbId")),
			ArrID:    int(floatVal(m, "id")),
			ArrType:  "radarr",
			Title:    title,
			Year:     int(floatVal(m, "year")),
			Tier:     tier,
			FilePath: filePath,
		}); err != nil {
			slog.Error("backfill: upsert movie", "component", "catalog_sync", "title", title, "error", err)
		}
	}
	slog.Info("backfill radarr complete", "component", "catalog_sync", "count", len(movies))
	return nil
}

func backfillSonarr(db *sql.DB, svc *ServiceClients, apiKey string) error {
	data, err := svc.ArrGet(sonarrURL, apiKey, "/api/v3/series")
	if err != nil {
		return fmt.Errorf("sonarr list: %w", err)
	}
	var seriesList []map[string]any
	if err := json.Unmarshal(data, &seriesList); err != nil {
		return fmt.Errorf("sonarr parse: %w", err)
	}

	for _, s := range seriesList {
		title, _ := s["title"].(string)
		if title == "" {
			continue
		}
		arrID := int(floatVal(s, "id"))
		tvdbID := int(floatVal(s, "tvdbId"))
		tmdbID := int(floatVal(s, "tmdbId"))
		year := int(floatVal(s, "year"))

		// Determine series tier from statistics
		tier := "queue"
		if stats, ok := s["statistics"].(map[string]any); ok {
			episodeFileCount := int(floatVal(stats, "episodeFileCount"))
			if episodeFileCount > 0 {
				tier = "library"
			}
		}

		seriesID, err := UpsertCatalogItem(db, CatalogItem{
			Type:    "series",
			TvdbID:  tvdbID,
			TmdbID:  tmdbID,
			ArrID:   arrID,
			ArrType: "sonarr",
			Title:   title,
			Year:    year,
			Tier:    tier,
		})
		if err != nil {
			slog.Error("backfill: upsert series", "component", "catalog_sync", "title", title, "error", err)
			continue
		}

		// Fetch episodes for this series
		epData, err := svc.ArrGet(sonarrURL, apiKey, fmt.Sprintf("/api/v3/episode?seriesId=%d", arrID))
		if err != nil {
			slog.Error("backfill: fetch episodes", "component", "catalog_sync", "series", title, "error", err)
			continue
		}
		var episodes []map[string]any
		if err := json.Unmarshal(epData, &episodes); err != nil {
			continue
		}

		// Collect season numbers seen
		seasonsSeen := map[int]bool{}
		for _, ep := range episodes {
			seasonNum := int(floatVal(ep, "seasonNumber"))
			if seasonNum == 0 {
				continue // skip specials season
			}
			if !seasonsSeen[seasonNum] {
				seasonsSeen[seasonNum] = true
				seasonTitle := fmt.Sprintf("%s Season %d", title, seasonNum)
				if _, err := UpsertCatalogItem(db, CatalogItem{
					Type:         "season",
					ParentID:     seriesID,
					SeasonNumber: seasonNum,
					Title:        seasonTitle,
					Year:         year,
					Tier:         tier,
				}); err != nil {
					slog.Error("backfill: upsert season", "component", "catalog_sync", "title", seasonTitle, "error", err)
				}
			}
		}
	}
	slog.Info("backfill sonarr complete", "component", "catalog_sync", "count", len(seriesList))
	return nil
}
```

Add `"encoding/json"` to imports in `catalog_sync.go`.

- [ ] **Step 2: Build to confirm compilation**

```
cd middleware && go build ./...
```

Expected: no errors

- [ ] **Step 3: Commit**

```bash
cd middleware && git add catalog_sync.go
git commit -m "feat(catalog): add BackfillFromArr to seed catalog from existing library"
```

---

### Task 7: catalog_sync.go — SyncJellyfinMetadata

This task implements on-demand Jellyfin metadata fetching (artwork URL + synopsis), called lazily from `handleCatalogItemDetail` when a record's metadata is stale or missing.

**Files:**
- Modify: `middleware/catalog_sync.go`

- [ ] **Step 1: Append `SyncJellyfinMetadata` and `maybeSyncJellyfinMetadata` to `middleware/catalog_sync.go`**

```go
// maybeSyncJellyfinMetadata syncs Jellyfin metadata for an item if it has
// never been synced or was last synced more than 24 hours ago.
// Safe to call in a goroutine — logs errors, never panics.
func maybeSyncJellyfinMetadata(item *CatalogItem) {
	if item == nil {
		return
	}
	// Only sync root-level items (movies and series carry the metadata)
	if item.Type != "movie" && item.Type != "series" {
		return
	}
	// Check staleness
	if item.MetadataSyncedAt != "" {
		t, err := time.Parse(time.RFC3339, item.MetadataSyncedAt)
		if err == nil && time.Since(t) < 24*time.Hour {
			return // fresh enough
		}
	}
	if err := SyncJellyfinMetadata(catalogDB, services, item); err != nil {
		slog.Error("jellyfin metadata sync", "component", "catalog_sync", "id", item.ID, "error", err)
	}
}

// SyncJellyfinMetadata fetches artwork and synopsis from Jellyfin for a catalog item
// and persists the result. Looks up by TMDB ID (movies) or TVDB ID (series).
func SyncJellyfinMetadata(db *sql.DB, svc *ServiceClients, item *CatalogItem) error {
	jellyfinID, artworkURL, synopsis, err := fetchJellyfinItemMeta(svc, item)
	if err != nil {
		return err
	}
	if jellyfinID == "" {
		// Item not yet in Jellyfin — record the attempt so we don't hammer it
		return UpdateCatalogMetadata(db, item.ID, "", "", "", time.Now().UTC().Format(time.RFC3339))
	}
	if err := UpdateCatalogMetadata(db, item.ID, jellyfinID, artworkURL, synopsis,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("persist metadata: %w", err)
	}
	slog.Info("jellyfin metadata synced", "component", "catalog_sync", "id", item.ID, "jellyfin_id", jellyfinID)
	return nil
}

// fetchJellyfinItemMeta queries Jellyfin for an item by provider ID and returns
// (jellyfinID, artworkURL, synopsis, error). Returns empty strings if not found.
func fetchJellyfinItemMeta(svc *ServiceClients, item *CatalogItem) (string, string, string, error) {
	var providerParam string
	switch item.Type {
	case "movie":
		if item.TmdbID != 0 {
			providerParam = fmt.Sprintf("Tmdb.%d", item.TmdbID)
		}
	case "series":
		if item.TvdbID != 0 {
			providerParam = fmt.Sprintf("Tvdb.%d", item.TvdbID)
		} else if item.TmdbID != 0 {
			providerParam = fmt.Sprintf("Tmdb.%d", item.TmdbID)
		}
	}
	if providerParam == "" {
		return "", "", "", nil // no provider ID to search by
	}

	path := fmt.Sprintf("/Items?AnyProviderIdEquals=%s&Fields=Overview,ImageTags&Limit=1", providerParam)
	body, err := jellyfinDo(svc, "GET", path, svc.JellyfinAPIKey, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("jellyfin items query: %w", err)
	}

	var resp struct {
		Items []struct {
			ID        string         `json:"Id"`
			Overview  string         `json:"Overview"`
			ImageTags map[string]string `json:"ImageTags"`
		} `json:"Items"`
		TotalRecordCount int `json:"TotalRecordCount"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", "", "", fmt.Errorf("jellyfin items parse: %w", err)
	}
	if len(resp.Items) == 0 {
		return "", "", "", nil
	}

	jf := resp.Items[0]
	artworkURL := ""
	if tag, ok := jf.ImageTags["Primary"]; ok && tag != "" {
		artworkURL = fmt.Sprintf("%s/Items/%s/Images/Primary", jellyfinURL, jf.ID)
	}
	return jf.ID, artworkURL, jf.Overview, nil
}
```

Add `"time"` to the imports in `catalog_sync.go` (it will be needed alongside existing `"fmt"`, `"database/sql"`, `"log/slog"`, `"encoding/json"`).

- [ ] **Step 2: Build to confirm compilation**

```
cd middleware && go build ./...
```

Expected: no errors

- [ ] **Step 3: Commit**

```bash
cd middleware && git add catalog_sync.go
git commit -m "feat(catalog): on-demand Jellyfin metadata sync with 24h TTL"
```

---

### Task 8: Verify end-to-end with `go build` and manual API smoke test

**Files:** none new — verify integration

- [ ] **Step 1: Full build**

```
cd middleware && go build ./...
```

Expected: no errors

- [ ] **Step 2: Full test suite**

```
cd middleware && go test -v ./...
```

Expected: all tests pass (including the catalog_db tests added in Tasks 1–2)

- [ ] **Step 3: Rebuild middleware container and smoke test the new endpoints**

```bash
# From repo root:
pelicula rebuild
```

Then test the endpoints:

```bash
# List all catalog items (should be empty before backfill)
curl -s http://localhost:7354/api/pelicula/catalog/items \
  -H "Cookie: $(cat /tmp/pelicula-session-cookie 2>/dev/null || echo '')" | jq length

# Trigger backfill (admin session required)
curl -s -X POST http://localhost:7354/api/pelicula/catalog/backfill \
  -H "Cookie: ..." | jq

# List items after backfill
curl -s "http://localhost:7354/api/pelicula/catalog/items?type=movie" \
  -H "Cookie: ..." | jq length

# Fetch a specific item by ID from the backfill results
curl -s "http://localhost:7354/api/pelicula/catalog/items/<id-from-above>" \
  -H "Cookie: ..." | jq .
```

Expected:
- `/catalog/items` returns `[]` before backfill
- `/catalog/backfill` returns `{"status":"started"}`
- `/catalog/items?type=movie` returns populated array after backfill
- `/catalog/items/{id}` returns full item with `artwork_url` and `synopsis` populated (after Jellyfin sync fires in background)

- [ ] **Step 4: Final commit**

```bash
git add -p  # stage any remaining unstaged changes
git commit -m "feat(catalog): Phase 1 complete — catalog.db foundation with sync, poller, and backfill"
```

---

## Summary of New API Endpoints

| Endpoint | Auth | Method | Description |
|----------|------|--------|-------------|
| `GET /api/pelicula/catalog/items` | viewer+ | GET | List catalog items; filter with `?type=`, `?tier=`, `?q=` |
| `GET /api/pelicula/catalog/items/{id}` | viewer+ | GET | Fetch single catalog item; triggers lazy Jellyfin metadata sync |
| `POST /api/pelicula/catalog/backfill` | admin | POST | Scan Radarr/Sonarr library and upsert all catalog records |
