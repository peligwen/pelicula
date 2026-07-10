# Pre-v1.0 Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Migrate all mutable state from JSON files to SQLite, add auto-migration framework, harden backup format, make service URLs configurable, rewrite the bash CLI in Go, and freeze API contracts before public release.

**Architecture:** Two SQLite databases (`pelicula.db` for middleware, `procula.db` for procula) replace 5+ JSON files and in-memory stores. A migration framework runs on startup to auto-upgrade schemas. The bash CLI is replaced with a Go binary that handles orchestration only (setup, up, down, restart, etc.) while configuration lives in the dashboard UI. Backups become always-forward-compatible via versioned import with migration chains.

**Tech Stack:** Go 1.23, `modernc.org/sqlite` (pure Go, no CGO), stdlib HTTP/JSON/os/exec

---

## Phase 1: SQLite Data Layer + Migration Framework

### Task 1: Add SQLite dependency and create migration framework for middleware

**Files:**
- Modify: `middleware/go.mod`
- Create: `middleware/db.go`
- Create: `middleware/db_test.go`

- [ ] **Step 1: Write failing test for migration framework**

```go
// middleware/db_test.go
package main

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenDB_CreatesNewDB(t *testing.T) {
	db := testDB(t)
	var version int
	err := db.QueryRow("PRAGMA user_version").Scan(&version)
	if err != nil {
		t.Fatal(err)
	}
	if version != latestVersion {
		t.Errorf("version = %d, want %d", version, latestVersion)
	}
}

func TestOpenDB_MigratesForward(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Create DB at version 0 (no tables)
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	raw.Exec("PRAGMA user_version = 0")
	raw.Close()

	// OpenDB should migrate to latest
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	var version int
	db.QueryRow("PRAGMA user_version").Scan(&version)
	if version != latestVersion {
		t.Errorf("version = %d, want %d", version, latestVersion)
	}

	// Check that tables exist
	for _, table := range []string{"roles", "invites", "redemptions", "requests", "request_events", "sessions", "dismissed_jobs", "rate_limits"} {
		var name string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %q missing: %v", table, err)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd middleware && go test -run TestOpenDB -v ./...`
Expected: FAIL — `OpenDB` and `latestVersion` not defined

- [ ] **Step 3: Add modernc.org/sqlite dependency**

Run: `cd middleware && go get modernc.org/sqlite`

This adds the pure-Go SQLite driver.

- [ ] **Step 4: Write the migration framework**

```go
// middleware/db.go
package main

import (
	"database/sql"
	"fmt"
	"log/slog"

	_ "modernc.org/sqlite"
)

// latestVersion is the highest schema version. Bump when adding migrations.
const latestVersion = 1

// migrations maps target version → SQL to run. Keyed by the version the DB
// will be AT after the migration runs.
var migrations = map[int]string{
	1: `
CREATE TABLE IF NOT EXISTS roles (
	jellyfin_id TEXT PRIMARY KEY,
	username    TEXT NOT NULL,
	role        TEXT NOT NULL CHECK(role IN ('viewer','manager','admin'))
);

CREATE TABLE IF NOT EXISTS invites (
	token      TEXT PRIMARY KEY,
	label      TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	created_by TEXT NOT NULL,
	expires_at TEXT,
	max_uses   INTEGER,
	uses       INTEGER NOT NULL DEFAULT 0,
	revoked    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS redemptions (
	invite_token TEXT NOT NULL REFERENCES invites(token) ON DELETE CASCADE,
	username     TEXT NOT NULL,
	jellyfin_id  TEXT NOT NULL,
	redeemed_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS requests (
	id           TEXT PRIMARY KEY,
	type         TEXT NOT NULL,
	tmdb_id      INTEGER NOT NULL DEFAULT 0,
	tvdb_id      INTEGER NOT NULL DEFAULT 0,
	title        TEXT NOT NULL,
	year         INTEGER NOT NULL DEFAULT 0,
	poster       TEXT NOT NULL DEFAULT '',
	requested_by TEXT NOT NULL,
	state        TEXT NOT NULL,
	reason       TEXT NOT NULL DEFAULT '',
	arr_id       INTEGER NOT NULL DEFAULT 0,
	created_at   TEXT NOT NULL,
	updated_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS request_events (
	request_id TEXT NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
	at         TEXT NOT NULL,
	state      TEXT NOT NULL,
	actor      TEXT NOT NULL DEFAULT '',
	note       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_request_events_request ON request_events(request_id);

CREATE TABLE IF NOT EXISTS sessions (
	token      TEXT PRIMARY KEY,
	username   TEXT NOT NULL,
	role       TEXT NOT NULL,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS dismissed_jobs (
	job_id TEXT PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS rate_limits (
	ip           TEXT PRIMARY KEY,
	fail_count   INTEGER NOT NULL DEFAULT 0,
	window_start TEXT NOT NULL
);
`,
}

// OpenDB opens (or creates) the SQLite database at path, runs any pending
// migrations, and returns the connection. WAL mode is enabled for concurrent
// read access.
func OpenDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	// WAL mode for concurrent reads
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL: %w", err)
	}
	// Foreign keys
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set FK: %w", err)
	}

	if err := runMigrations(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

func runMigrations(db *sql.DB) error {
	var current int
	if err := db.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read version: %w", err)
	}

	for v := current + 1; v <= latestVersion; v++ {
		ddl, ok := migrations[v]
		if !ok {
			return fmt.Errorf("missing migration for version %d", v)
		}
		slog.Info("running migration", "component", "db", "from", v-1, "to", v)
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin tx v%d: %w", v, err)
		}
		if _, err := tx.Exec(ddl); err != nil {
			tx.Rollback()
			return fmt.Errorf("migrate v%d: %w", v, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil {
			tx.Rollback()
			return fmt.Errorf("set version v%d: %w", v, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit v%d: %w", v, err)
		}
		slog.Info("migration complete", "component", "db", "version", v)
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd middleware && go test -run TestOpenDB -v ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/gwen/workspace/pelicula
git add middleware/go.mod middleware/go.sum middleware/db.go middleware/db_test.go
git commit -m "feat(middleware): add SQLite migration framework with modernc.org/sqlite"
```

---

### Task 2: Migrate RolesStore from JSON to SQLite

**Files:**
- Modify: `middleware/roles.go`
- Modify: `middleware/roles_test.go`
- Create: `middleware/migrate_json.go`
- Create: `middleware/migrate_json_test.go`

- [ ] **Step 1: Write test for JSON-to-SQLite migration**

```go
// middleware/migrate_json_test.go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateRolesJSON(t *testing.T) {
	dir := t.TempDir()

	// Write old-format roles.json
	oldData := rolesFile{
		Version: 1,
		Users: []RolesEntry{
			{JellyfinID: "jf-1", Username: "alice", Role: RoleAdmin},
			{JellyfinID: "jf-2", Username: "bob", Role: RoleViewer},
		},
	}
	data, _ := json.MarshalIndent(oldData, "", "  ")
	jsonPath := filepath.Join(dir, "roles.json")
	os.WriteFile(jsonPath, data, 0600)

	db := testDB(t)
	err := migrateRolesJSON(db, jsonPath)
	if err != nil {
		t.Fatalf("migrateRolesJSON: %v", err)
	}

	// Verify data in SQLite
	rs := NewRolesStore(db)
	role, ok := rs.Lookup("jf-1")
	if !ok || role != RoleAdmin {
		t.Errorf("alice: got %v/%v, want admin/true", role, ok)
	}
	role, ok = rs.Lookup("jf-2")
	if !ok || role != RoleViewer {
		t.Errorf("bob: got %v/%v, want viewer/true", role, ok)
	}

	// Verify JSON file was renamed
	if _, err := os.Stat(jsonPath); !os.IsNotExist(err) {
		t.Error("roles.json should have been renamed")
	}
	if _, err := os.Stat(jsonPath + ".migrated"); os.IsNotExist(err) {
		t.Error("roles.json.migrated should exist")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd middleware && go test -run TestMigrateRolesJSON -v ./...`
Expected: FAIL — `migrateRolesJSON` not defined, `NewRolesStore` signature changed

- [ ] **Step 3: Rewrite RolesStore to use SQLite**

Modify `middleware/roles.go`: Replace the file-backed store with SQLite queries. Keep the same public API (`NewRolesStore`, `IsEmpty`, `Lookup`, `Upsert`, `All`) but change the constructor to accept `*sql.DB` instead of a path. Remove the `path`, `mu`, and `data` fields — SQLite handles concurrency.

```go
// middleware/roles.go
package main

import (
	"database/sql"
	"fmt"
)

type RolesEntry struct {
	JellyfinID string   `json:"jellyfin_id"`
	Username   string   `json:"username"`
	Role       UserRole `json:"role"`
}

// rolesFile is the legacy JSON format, kept for migration.
type rolesFile struct {
	Version int          `json:"version"`
	Users   []RolesEntry `json:"users"`
}

type RolesStore struct {
	db *sql.DB
}

func NewRolesStore(db *sql.DB) *RolesStore {
	return &RolesStore{db: db}
}

func (rs *RolesStore) IsEmpty() bool {
	var count int
	rs.db.QueryRow("SELECT COUNT(*) FROM roles").Scan(&count)
	return count == 0
}

func (rs *RolesStore) Lookup(jellyfinID string) (UserRole, bool) {
	var role string
	err := rs.db.QueryRow("SELECT role FROM roles WHERE jellyfin_id = ?", jellyfinID).Scan(&role)
	if err != nil {
		return "", false
	}
	return UserRole(role), true
}

func (rs *RolesStore) Upsert(jellyfinID, username string, role UserRole) error {
	_, err := rs.db.Exec(`
		INSERT INTO roles (jellyfin_id, username, role) VALUES (?, ?, ?)
		ON CONFLICT(jellyfin_id) DO UPDATE SET username = excluded.username, role = excluded.role`,
		jellyfinID, username, string(role))
	if err != nil {
		return fmt.Errorf("upsert role: %w", err)
	}
	return nil
}

func (rs *RolesStore) All() []RolesEntry {
	rows, err := rs.db.Query("SELECT jellyfin_id, username, role FROM roles")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var result []RolesEntry
	for rows.Next() {
		var e RolesEntry
		if rows.Scan(&e.JellyfinID, &e.Username, (*string)(&e.Role)) == nil {
			result = append(result, e)
		}
	}
	if result == nil {
		result = []RolesEntry{}
	}
	return result
}
```

- [ ] **Step 4: Write the JSON migration function**

```go
// middleware/migrate_json.go
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
)

// migrateRolesJSON reads roles.json into the SQLite roles table, then renames
// the JSON file to roles.json.migrated.
func migrateRolesJSON(db *sql.DB, jsonPath string) error {
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to migrate
		}
		return fmt.Errorf("read roles.json: %w", err)
	}

	var f rolesFile
	if err := json.Unmarshal(data, &f); err != nil {
		slog.Warn("skipping corrupt roles.json", "error", err)
		return os.Rename(jsonPath, jsonPath+".migrated")
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for _, u := range f.Users {
		if _, err := tx.Exec(
			"INSERT OR IGNORE INTO roles (jellyfin_id, username, role) VALUES (?, ?, ?)",
			u.JellyfinID, u.Username, string(u.Role),
		); err != nil {
			tx.Rollback()
			return fmt.Errorf("insert role %s: %w", u.Username, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	slog.Info("migrated roles.json", "component", "db", "count", len(f.Users))
	return os.Rename(jsonPath, jsonPath+".migrated")
}
```

- [ ] **Step 5: Update roles_test.go for SQLite-backed store**

Update all existing tests in `middleware/roles_test.go` to use `testDB(t)` instead of temp file paths. The test helper `newTestRolesStore(t)` should create a test DB and return `NewRolesStore(db)`.

- [ ] **Step 6: Run all roles tests**

Run: `cd middleware && go test -run "TestRoles|TestMigrateRoles" -v ./...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add middleware/roles.go middleware/roles_test.go middleware/migrate_json.go middleware/migrate_json_test.go
git commit -m "feat(middleware): migrate RolesStore from JSON to SQLite"
```

---

### Task 3: Migrate InviteStore from JSON to SQLite

**Files:**
- Modify: `middleware/invites.go`
- Modify: `middleware/invites_test.go`
- Modify: `middleware/migrate_json.go`
- Modify: `middleware/migrate_json_test.go`

- [ ] **Step 1: Write test for JSON invite migration**

Add `TestMigrateInvitesJSON` to `migrate_json_test.go`. Create a JSON file with 2 invites (one with redemptions, one revoked). Verify they migrate correctly including the redemptions table.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd middleware && go test -run TestMigrateInvitesJSON -v ./...`

- [ ] **Step 3: Rewrite InviteStore to use SQLite**

Replace `middleware/invites.go`: Change constructor to accept `*sql.DB`. Replace all `json.Marshal/Unmarshal` + `os.ReadFile/WriteFile` with SQL queries. The `Redeem` method's 3-phase operation (validate → create Jellyfin user → audit) should use a transaction for the validate+audit phases, with the Jellyfin call happening outside the transaction.

Key changes:
- `CreateInvite` → `INSERT INTO invites`
- `ListInvites` → `SELECT` with state derived from columns (revoked, expired, exhausted, active)
- `CheckInvite` → `SELECT` with state check
- `Redeem` → `BEGIN` → validate → (unlock, call Jellyfin) → `BEGIN` → insert redemption + increment uses → `COMMIT`
- `Revoke` → `UPDATE invites SET revoked = 1`
- `Delete` → `DELETE FROM invites` (cascade deletes redemptions)
- Remove the `sync.Mutex` — SQLite serializes writes

- [ ] **Step 4: Write migrateInvitesJSON in migrate_json.go**

Read `invites.json`, insert each invite into `invites` table and each redemption into `redemptions` table, rename to `.migrated`.

- [ ] **Step 5: Update invites_test.go**

Change `newTestInviteStore(t)` to create a test DB. Update `setInviteStore` pattern. Ensure all existing tests pass with the SQLite backend.

- [ ] **Step 6: Run tests**

Run: `cd middleware && go test -run "TestInvite|TestRedeem|TestMigrateInvites" -v ./...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add middleware/invites.go middleware/invites_test.go middleware/migrate_json.go middleware/migrate_json_test.go
git commit -m "feat(middleware): migrate InviteStore from JSON to SQLite"
```

---

### Task 4: Migrate RequestStore from JSON to SQLite

**Files:**
- Modify: `middleware/requests.go`
- Modify: `middleware/requests_test.go`
- Modify: `middleware/migrate_json.go`
- Modify: `middleware/migrate_json_test.go`

- [ ] **Step 1: Write test for JSON request migration**

Add `TestMigrateRequestsJSON` to `migrate_json_test.go`. Create a JSON file with requests in different states (pending, approved, available) with history events. Verify data and events migrate correctly.

- [ ] **Step 2: Run test to verify it fails**

- [ ] **Step 3: Rewrite RequestStore to use SQLite**

Replace `middleware/requests.go`: Constructor accepts `*sql.DB`. Key changes:
- `all()` → `SELECT * FROM requests` + `SELECT * FROM request_events WHERE request_id = ?` for each
- `findActive()` → `SELECT ... WHERE type=? AND (tmdb_id=? OR tvdb_id=?) AND state NOT IN ('denied','available')`
- `get()` → `SELECT ... WHERE id = ?`
- State transitions append to `request_events` table
- `MarkRequestAvailable()` → `UPDATE requests SET state = 'available' WHERE ...`
- Remove `sync.Mutex` — SQLite handles concurrency

- [ ] **Step 4: Write migrateRequestsJSON**

- [ ] **Step 5: Update requests_test.go**

- [ ] **Step 6: Run tests**

Run: `cd middleware && go test -run "TestRequest|TestMigrateRequests" -v ./...`

- [ ] **Step 7: Commit**

```bash
git add middleware/requests.go middleware/requests_test.go middleware/migrate_json.go middleware/migrate_json_test.go
git commit -m "feat(middleware): migrate RequestStore from JSON to SQLite"
```

---

### Task 5: Migrate DismissedStore and sessions/rate-limiting to SQLite

**Files:**
- Modify: `middleware/pipeline.go` (DismissedStore portion)
- Modify: `middleware/auth.go` (sessions + rate limiting)
- Modify: `middleware/auth_test.go`
- Modify: `middleware/migrate_json.go`
- Modify: `middleware/migrate_json_test.go`

- [ ] **Step 1: Write tests for dismissed jobs migration and session persistence**

Test `TestMigrateDismissedJSON`: create dismissed.json with job IDs, verify they appear in SQLite.
Test `TestSessionPersistence`: create session via Auth, verify it survives creating a new Auth with the same DB.

- [ ] **Step 2: Run tests to verify they fail**

- [ ] **Step 3: Rewrite DismissedStore to use SQLite**

Change `DismissedStore` in `middleware/pipeline.go`:
- Constructor: `NewDismissedStore(db *sql.DB)`
- `IsDismissed(id)` → `SELECT 1 FROM dismissed_jobs WHERE job_id = ?`
- `Dismiss(id)` → `INSERT OR IGNORE INTO dismissed_jobs (job_id) VALUES (?)`
- Remove `mu`, `path`, `ids` fields

- [ ] **Step 4: Rewrite Auth to persist sessions and rate limits in SQLite**

Modify `middleware/auth.go`:
- Add `db *sql.DB` field to `Auth` struct
- Add `DB *sql.DB` field to `AuthConfig`
- Session operations become SQL queries:
  - `getSession()` → `SELECT ... FROM sessions WHERE token = ? AND expires_at > ?`
  - Login success → `INSERT INTO sessions`
  - Logout → `DELETE FROM sessions WHERE token = ?`
  - `cleanupSessions()` → `DELETE FROM sessions WHERE expires_at < ?` + `DELETE FROM rate_limits WHERE window_start < ?`
- Rate limiting:
  - `isRateLimited(ip)` → `SELECT fail_count FROM rate_limits WHERE ip = ? AND window_start > ?`
  - `recordFailure(ip)` → `INSERT INTO rate_limits ... ON CONFLICT(ip) DO UPDATE SET fail_count = fail_count + 1`
- Keep the `sync.RWMutex` for the in-memory session cache as a performance optimization — read from cache first, fall back to DB. Or simpler: just use DB for everything since SQLite with WAL is fast enough for this scale.

- [ ] **Step 5: Write migrateDismissedJSON**

- [ ] **Step 6: Update auth_test.go and pipeline_test.go**

All tests that create `Auth` must pass a `*sql.DB` in `AuthConfig`. Add session persistence test.

- [ ] **Step 7: Run tests**

Run: `cd middleware && go test -run "TestSession|TestRateLimit|TestDismiss|TestMigrateDismissed|TestGuard|TestLogin" -v ./...`

- [ ] **Step 8: Commit**

```bash
git add middleware/pipeline.go middleware/auth.go middleware/auth_test.go middleware/migrate_json.go middleware/migrate_json_test.go
git commit -m "feat(middleware): migrate sessions, rate limits, and dismissed jobs to SQLite"
```

---

### Task 6: Wire SQLite into middleware main.go

**Files:**
- Modify: `middleware/main.go`

- [ ] **Step 1: Update main.go initialization**

Replace the JSON store initialization with SQLite:

```go
// In main(), after setup mode check:

// Open database (creates + migrates if needed)
db, err := OpenDB("/config/pelicula/pelicula.db")
if err != nil {
	slog.Error("database init failed", "component", "main", "error", err)
	os.Exit(1)
}

// Migrate legacy JSON files (first run after upgrade)
migrateAllJSON(db, "/config/pelicula")

// Initialize stores
rolesStore := NewRolesStore(db)
inviteStore = NewInviteStore(db)
dismissedStore = NewDismissedStore(db)
requestStore = NewRequestStore(db)

// Auth now uses DB for sessions and rate limiting
authMiddleware = NewAuth(AuthConfig{
	Mode:       authMode,
	RolesStore: rolesStore,
	DB:         db,
})
```

- [ ] **Step 2: Create migrateAllJSON orchestrator in migrate_json.go**

```go
func migrateAllJSON(db *sql.DB, configDir string) {
	migrateRolesJSON(db, filepath.Join(configDir, "roles.json"))
	migrateInvitesJSON(db, filepath.Join(configDir, "invites.json"))
	migrateRequestsJSON(db, filepath.Join(configDir, "requests.json"))
	migrateDismissedJSON(db, filepath.Join(configDir, "dismissed.json"))
}
```

- [ ] **Step 3: Update AuthConfig to pass RolesStore directly**

Since `NewAuth` previously created its own `RolesStore` from a path, change it to accept the pre-created store:
- Add `RolesStore *RolesStore` to `AuthConfig`
- Remove `RolesFile string` from `AuthConfig`
- In `NewAuth`, use `cfg.RolesStore` directly instead of calling `NewRolesStore(cfg.RolesFile)`

- [ ] **Step 4: Run full middleware test suite**

Run: `cd middleware && go test -v ./...`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
git add middleware/main.go middleware/migrate_json.go
git commit -m "feat(middleware): wire SQLite database into main.go with JSON auto-migration"
```

---

### Task 7: Add SQLite dependency and migration framework for procula

**Files:**
- Modify: `procula/go.mod`
- Create: `procula/db.go`
- Create: `procula/db_test.go`

- [ ] **Step 1: Write failing test for procula migration framework**

Same pattern as middleware: `TestOpenDB_CreatesNewDB` verifying tables `jobs` and `settings` exist.

The procula schema needs these tables:
```sql
CREATE TABLE IF NOT EXISTS jobs (
    id               TEXT PRIMARY KEY,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    state            TEXT NOT NULL,
    stage            TEXT NOT NULL,
    progress         REAL NOT NULL DEFAULT 0,
    source           TEXT NOT NULL,  -- JSON blob
    validation       TEXT,           -- JSON blob, nullable
    missing_subs     TEXT,           -- JSON array, nullable
    error            TEXT NOT NULL DEFAULT '',
    retry_count      INTEGER NOT NULL DEFAULT 0,
    manual_profile   TEXT NOT NULL DEFAULT '',
    dualsub_outputs  TEXT,           -- JSON array, nullable
    dualsub_error    TEXT NOT NULL DEFAULT '',
    transcode_profile  TEXT NOT NULL DEFAULT '',
    transcode_decision TEXT NOT NULL DEFAULT '',
    transcode_outputs  TEXT,         -- JSON array, nullable
    transcode_error    TEXT NOT NULL DEFAULT '',
    transcode_eta      REAL NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd procula && go test -run TestOpenDB -v ./...`

- [ ] **Step 3: Add modernc.org/sqlite and write db.go**

Same pattern as middleware: `OpenDB`, `runMigrations`, `latestVersion = 1`.

Run: `cd procula && go get modernc.org/sqlite`

- [ ] **Step 4: Run tests**

Run: `cd procula && go test -run TestOpenDB -v ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add procula/go.mod procula/go.sum procula/db.go procula/db_test.go
git commit -m "feat(procula): add SQLite migration framework"
```

---

### Task 8: Migrate procula Queue from JSON files to SQLite

**Files:**
- Modify: `procula/queue.go`
- Modify: `procula/queue_test.go`
- Create: `procula/migrate_json.go`
- Create: `procula/migrate_json_test.go`

- [ ] **Step 1: Write test for JSON jobs migration**

Create temp dir with 2 job JSON files (one completed, one queued). Run migration. Verify both appear in SQLite with correct fields. Verify JSON files renamed to `.migrated`.

- [ ] **Step 2: Run test to verify it fails**

- [ ] **Step 3: Rewrite Queue to use SQLite**

Key changes to `procula/queue.go`:
- `Queue` struct: replace `jobs map[string]*Job` with `db *sql.DB`. Keep `pending chan string` and `cancels map[string]context.CancelFunc`.
- `NewQueue(db *sql.DB)` instead of `NewQueue(configDir string)`
- `loadExisting()` → `SELECT id FROM jobs WHERE state IN ('queued','processing')` → re-queue processing as queued, send to pending channel
- `Create()` → `INSERT INTO jobs` + send to channel
- `Get()` → `SELECT` + unmarshal JSON columns (source, validation, etc.)
- `List()` → `SELECT` ordered by created_at
- `Update()` → caller provides mutation func, then `UPDATE jobs SET ...`
- `persist()` → replaced by direct SQL in Create/Update
- `Status()` → `SELECT state, COUNT(*) FROM jobs GROUP BY state`

For nested JSON fields (`Source`, `Validation`, etc.), store as JSON text columns and marshal/unmarshal in Go. This preserves the exact same struct shape while using SQLite for storage.

- [ ] **Step 4: Write migrateJobsJSON**

Read all `*.json` files from `configDir/jobs/`, insert each into jobs table, then rename the `jobs/` directory to `jobs.migrated/`.

- [ ] **Step 5: Update queue_test.go**

Change `newTestQueue(t)` to create a test DB and pass to `NewQueue(db)`. Update all queue tests.

- [ ] **Step 6: Run tests**

Run: `cd procula && go test -run "TestQueue|TestJob|TestMigrateJobs" -v ./...`

- [ ] **Step 7: Commit**

```bash
git add procula/queue.go procula/queue_test.go procula/migrate_json.go procula/migrate_json_test.go
git commit -m "feat(procula): migrate job queue from JSON files to SQLite"
```

---

### Task 9: Migrate procula Settings from JSON to SQLite

**Files:**
- Modify: `procula/settings.go`
- Modify: `procula/settings_test.go` (if exists)
- Modify: `procula/migrate_json.go`

- [ ] **Step 1: Write test for settings migration**

Create `settings.json` with non-default values. Migrate. Verify `GetSettings()` returns the migrated values.

- [ ] **Step 2: Rewrite settings to use SQLite**

Store settings as individual key-value rows in the `settings` table, or as a single JSON blob under key `"pipeline"`. The single-blob approach is simpler and matches the current pattern:

```go
func GetSettings(db *sql.DB) PipelineSettings {
    var raw string
    err := db.QueryRow("SELECT value FROM settings WHERE key = 'pipeline'").Scan(&raw)
    if err != nil {
        return defaultSettings()
    }
    var s PipelineSettings
    if json.Unmarshal([]byte(raw), &s) != nil {
        return defaultSettings()
    }
    return s
}

func SaveSettings(db *sql.DB, s PipelineSettings) error {
    data, _ := json.Marshal(s)
    _, err := db.Exec(
        "INSERT INTO settings (key, value) VALUES ('pipeline', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value",
        string(data))
    return err
}
```

Remove the global `cachedSettings` and `settingsMu` — SQLite reads are fast enough.

- [ ] **Step 3: Write migrateSettingsJSON**

Read `settings.json`, insert into settings table, rename to `.migrated`.

- [ ] **Step 4: Update tests and run**

Run: `cd procula && go test -v ./...`

- [ ] **Step 5: Commit**

```bash
git add procula/settings.go procula/settings_test.go procula/migrate_json.go
git commit -m "feat(procula): migrate settings from JSON to SQLite"
```

---

### Task 10: Wire SQLite into procula main.go and run full test suite

**Files:**
- Modify: `procula/main.go`
- Modify: `procula/pipeline.go` (RunWorker signature may change)

- [ ] **Step 1: Update procula main.go**

```go
// In main():
db, err := OpenDB(filepath.Join(configDir, "procula.db"))
if err != nil {
    slog.Error("database init failed", "error", err)
    os.Exit(1)
}
migrateAllJSON(db, configDir)

queue, err := NewQueue(db)
// ... pass db to settings functions, etc.
```

Update all handlers that call `GetSettings()` or `SaveSettings()` to pass `db`.

- [ ] **Step 2: Run full procula test suite**

Run: `cd procula && go test -v ./...`
Expected: ALL PASS

- [ ] **Step 3: Run full middleware test suite**

Run: `cd middleware && go test -v ./...`
Expected: ALL PASS

- [ ] **Step 4: Run make test**

Run: `make test`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
git add procula/main.go procula/pipeline.go
git commit -m "feat(procula): wire SQLite database into main.go with JSON auto-migration"
```

---

### Task 11: Phase 1 review checkpoint

- [ ] **Step 1: Run full test suite with race detection**

Run: `make test-race`
Expected: ALL PASS, no race conditions

- [ ] **Step 2: Verify migration works end-to-end**

Create sample JSON files in a temp config dir, start the services, verify DBs are created and JSON files renamed. This is a manual/visual verification.

- [ ] **Step 3: Review all changes since Phase 1 start**

Use the code-reviewer agent to review all Phase 1 changes for correctness, security, and consistency.

---

## Phase 2: Backup Hardening + Service URL Configuration

### Task 12: Make service URLs configurable via environment variables

**Files:**
- Modify: `middleware/autowire.go` (lines 14-17: constants → env-backed vars)
- Modify: `middleware/services.go` (QbtGet/QbtPost URLs, CheckHealth map)
- Modify: `middleware/jellyfin.go` (jellyfinURL)
- Modify: `middleware/hooks.go` (proculaURL — already has env override, verify pattern)
- Modify: `middleware/health.go` (gluetun control API URL)
- Modify: `middleware/apprise.go` (apprise URL)
- Modify: `middleware/docker.go` (already has DOCKER_HOST env, verify)

- [ ] **Step 1: Write test for env-backed service URLs**

```go
func TestServiceURLDefaults(t *testing.T) {
    // Verify defaults match current hardcoded values
    if sonarrURL != "http://sonarr:8989/sonarr" {
        t.Errorf("sonarrURL default = %q", sonarrURL)
    }
    // ... same for all services
}

func TestServiceURLOverride(t *testing.T) {
    t.Setenv("SONARR_URL", "http://custom:1234/sonarr")
    // Re-init the var (need to call init function or use a getter)
    if getSonarrURL() != "http://custom:1234/sonarr" {
        t.Error("override not applied")
    }
}
```

- [ ] **Step 2: Replace constants with env-backed vars**

In `middleware/autowire.go`, change:
```go
// Before:
const (
    sonarrURL   = "http://sonarr:8989/sonarr"
    radarrURL   = "http://radarr:7878/radarr"
    prowlarrURL = "http://prowlarr:9696/prowlarr"
    bazarrURL   = "http://bazarr:6767/bazarr"
)

// After:
var (
    sonarrURL   = envOr("SONARR_URL", "http://sonarr:8989/sonarr")
    radarrURL   = envOr("RADARR_URL", "http://radarr:7878/radarr")
    prowlarrURL = envOr("PROWLARR_URL", "http://prowlarr:9696/prowlarr")
    bazarrURL   = envOr("BAZARR_URL", "http://bazarr:6767/bazarr")
)

func envOr(key, fallback string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return fallback
}
```

Apply same pattern to:
- `middleware/jellyfin.go`: `jellyfinURL` (already a var, just add envOr)
- `middleware/services.go`: `qbtBaseURL` for QbtGet/QbtPost, health check URLs
- `middleware/health.go`: gluetun control URL
- `middleware/apprise.go`: apprise URL
- `middleware/hooks.go`: proculaBaseURL (already has env override — keep its pattern)
- `middleware/autowire.go`: webhook callback URL (`http://pelicula-api:8181`)

- [ ] **Step 3: Run tests**

Run: `cd middleware && go test -v ./...`

- [ ] **Step 4: Commit**

```bash
git add middleware/autowire.go middleware/services.go middleware/jellyfin.go middleware/hooks.go middleware/health.go middleware/apprise.go middleware/docker.go
git commit -m "feat(middleware): make all service URLs configurable via environment variables"
```

---

### Task 13: Harden backup format for forward compatibility

**Files:**
- Modify: `middleware/export.go`
- Modify: `middleware/export_test.go`

- [ ] **Step 1: Write test for versioned backup import**

```go
func TestImportBackupV1(t *testing.T) {
    // Create a v1 backup (current format)
    // Import it — should succeed
}

func TestImportBackupV2(t *testing.T) {
    // Create a v2 backup (with roles, invites, requests)
    // Import it — should succeed and populate all stores
}

func TestImportV1IntoV2System(t *testing.T) {
    // Create a v1 backup
    // Import into a system that expects v2 format
    // Should auto-migrate: v1 fields preserved, v2 fields default
}
```

- [ ] **Step 2: Run tests to verify they fail**

- [ ] **Step 3: Update export format to v2**

Add to `BackupExport`:
```go
type BackupExport struct {
    Version         int             `json:"version"`
    PeliculaVersion string          `json:"pelicula_version,omitempty"`
    Exported        string          `json:"exported"`
    Movies          []MovieExport   `json:"movies"`
    Series          []SeriesExport  `json:"series"`
    Roles           []RolesEntry    `json:"roles,omitempty"`
    Invites         []InviteExport  `json:"invites,omitempty"`
    Requests        []RequestExport `json:"requests,omitempty"`
}
```

- [ ] **Step 4: Update handleExport to include roles, invites, requests**

Export now also dumps the SQLite tables for roles, invites, and requests.

- [ ] **Step 5: Update handleImportBackup with version migration**

```go
func handleImportBackup(w http.ResponseWriter, r *http.Request) {
    // ... read body ...
    if backup.Version < 1 || backup.Version > currentBackupVersion {
        writeError(w, "unsupported backup version", 400)
        return
    }
    // Migrate forward if needed
    if backup.Version < currentBackupVersion {
        backup = migrateBackup(backup)
    }
    // ... import ...
}
```

- [ ] **Step 6: Improve profile matching**

In `resolveProfileID`: instead of silently falling back to ID 1, log a warning and include it in the ImportResult.Errors slice. Store both profile name and ID in the export so import can try ID first, then name.

- [ ] **Step 7: Remove 10MB body limit or increase to 100MB**

Change `http.MaxBytesReader(w, r.Body, 10<<20)` to `100<<20` for large libraries.

- [ ] **Step 8: Run tests**

Run: `cd middleware && go test -run "TestExport|TestImport" -v ./...`

- [ ] **Step 9: Commit**

```bash
git add middleware/export.go middleware/export_test.go
git commit -m "feat(middleware): versioned backup format with forward compatibility and full data export"
```

---

### Task 14: Phase 2 review checkpoint

- [ ] **Step 1: Run full test suite**

Run: `make test`

- [ ] **Step 2: Code review**

Review Phase 2 changes for correctness, backward compatibility, and security.

---

## Phase 3: Go CLI Rewrite

### Task 15: Scaffold Go CLI with command dispatch

**Files:**
- Create: `cmd/pelicula/main.go`
- Create: `cmd/pelicula/platform.go`
- Create: `cmd/pelicula/compose.go`
- Create: `cmd/pelicula/env.go`

- [ ] **Step 1: Create cmd/pelicula/main.go with command dispatch**

```go
// cmd/pelicula/main.go
package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(0)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "setup":
		cmdSetup(args)
	case "up":
		cmdUp(args)
	case "down":
		cmdDown(args)
	case "restart":
		cmdRestart(args)
	case "restart-acquire":
		cmdRestartAcquire(args)
	case "rebuild":
		cmdRebuild(args)
	case "reset-config":
		cmdResetConfig(args)
	case "status":
		cmdStatus(args)
	case "logs":
		cmdLogs(args)
	case "check-vpn":
		cmdCheckVPN(args)
	case "update":
		cmdUpdate(args)
	case "export":
		cmdExport(args)
	case "import-backup":
		cmdImportBackup(args)
	case "import":
		cmdImport(args)
	case "-h", "--help", "help":
		usage()
	case "-v", "--version":
		fmt.Println("pelicula", version)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`Usage: pelicula <command>

Commands:
  setup               Interactive setup wizard (opens browser)
  up                  Start the stack
  down                Stop the stack
  restart [service]   Restart service(s)
  rebuild [service]   Rebuild and restart middleware/procula
  reset-config [svc]  Reset service configs
  status              Show service health
  logs [service]      Tail service logs
  check-vpn           Verify VPN connectivity
  update              Pull latest images and restart
  export [file]       Export library backup
  import-backup file  Import library backup
  import [dir]        Open import wizard`)
}
```

- [ ] **Step 2: Create platform.go with OS detection**

```go
// cmd/pelicula/platform.go
package main

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

type Platform struct {
	OS        string // "darwin", "linux", "windows"
	Synology  bool
	NeedsSudo bool
}

func detectPlatform() Platform {
	p := Platform{OS: runtime.GOOS}

	// Synology detection
	if p.OS == "linux" {
		if _, err := os.Stat("/proc/syno_platform"); err == nil {
			p.Synology = true
		} else if _, err := os.Stat("/volume1"); err == nil {
			p.Synology = true
		}
	}

	// Docker sudo detection
	if err := exec.Command("docker", "info").Run(); err != nil {
		if err := exec.Command("sudo", "docker", "info").Run(); err == nil {
			p.NeedsSudo = true
		}
	}

	return p
}

func (p Platform) defaultConfigDir() string {
	if p.Synology {
		return "/volume1/docker/media-stack/config"
	}
	home, _ := os.UserHomeDir()
	return home + "/pelicula/config"
}

func (p Platform) defaultLibraryDir() string {
	if p.Synology {
		return "/volume1/media"
	}
	home, _ := os.UserHomeDir()
	return home + "/media"
}

func (p Platform) timezone() string {
	// Try /etc/localtime symlink
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if idx := strings.Index(target, "zoneinfo/"); idx >= 0 {
			return target[idx+len("zoneinfo/"):]
		}
	}
	// Try /etc/timezone
	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		return strings.TrimSpace(string(data))
	}
	return "UTC"
}
```

- [ ] **Step 3: Create compose.go with Docker Compose wrapper**

```go
// cmd/pelicula/compose.go
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type Compose struct {
	projectDir string
	envFile    string
	platform   Platform
}

func NewCompose(projectDir string, platform Platform) *Compose {
	return &Compose{
		projectDir: projectDir,
		envFile:    filepath.Join(projectDir, ".env"),
		platform:   platform,
	}
}

func (c *Compose) Run(args ...string) error {
	cmdArgs := []string{"compose", "--env-file", c.envFile, "-f", filepath.Join(c.projectDir, "docker-compose.yml")}

	// Add override files if they exist
	for _, f := range []string{"docker-compose.override.yml", "docker-compose.remote.yml"} {
		path := filepath.Join(c.projectDir, f)
		if _, err := os.Stat(path); err == nil {
			cmdArgs = append(cmdArgs, "-f", path)
		}
	}
	cmdArgs = append(cmdArgs, args...)

	var cmd *exec.Cmd
	if c.platform.NeedsSudo {
		cmd = exec.Command("sudo", append([]string{"docker"}, cmdArgs...)...)
	} else {
		cmd = exec.Command("docker", cmdArgs...)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func (c *Compose) RunSilent(args ...string) ([]byte, error) {
	cmdArgs := []string{"compose", "--env-file", c.envFile, "-f", filepath.Join(c.projectDir, "docker-compose.yml")}
	for _, f := range []string{"docker-compose.override.yml", "docker-compose.remote.yml"} {
		path := filepath.Join(c.projectDir, f)
		if _, err := os.Stat(path); err == nil {
			cmdArgs = append(cmdArgs, "-f", path)
		}
	}
	cmdArgs = append(cmdArgs, args...)

	var cmd *exec.Cmd
	if c.platform.NeedsSudo {
		cmd = exec.Command("sudo", append([]string{"docker"}, cmdArgs...)...)
	} else {
		cmd = exec.Command("docker", cmdArgs...)
	}
	return cmd.CombinedOutput()
}
```

- [ ] **Step 4: Create env.go with .env parsing and migration**

```go
// cmd/pelicula/env.go
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// parseEnv reads a .env file into a key-value map.
func parseEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	result := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		result[key] = val
	}
	return result, scanner.Err()
}

// migrateEnv applies forward migrations to the .env file.
// Returns true if changes were made.
func migrateEnv(path string) (bool, error) {
	vars, err := parseEnv(path)
	if err != nil {
		return false, err
	}

	changed := false

	// Migration: MEDIA_DIR → LIBRARY_DIR + WORK_DIR
	if vars["MEDIA_DIR"] != "" && vars["LIBRARY_DIR"] == "" {
		vars["LIBRARY_DIR"] = vars["MEDIA_DIR"]
		if vars["WORK_DIR"] == "" {
			vars["WORK_DIR"] = vars["MEDIA_DIR"]
		}
		delete(vars, "MEDIA_DIR")
		changed = true
		fmt.Println("Migrated MEDIA_DIR → LIBRARY_DIR and WORK_DIR")
	}

	// Add missing keys with defaults
	defaults := map[string]string{
		"PELICULA_OPEN_REGISTRATION":  "false",
		"TRANSCODING_ENABLED":         "false",
		"NOTIFICATIONS_ENABLED":       "false",
		"NOTIFICATIONS_MODE":          "internal",
		"SEEDING_REMOVE_ON_COMPLETE":  "false",
	}
	for k, v := range defaults {
		if _, ok := vars[k]; !ok {
			vars[k] = v
			changed = true
			fmt.Printf("Added missing .env key: %s=%s\n", k, v)
		}
	}

	if changed {
		return true, writeEnv(path, vars)
	}
	return false, nil
}

func writeEnv(path string, vars map[string]string) error {
	order := []string{
		"CONFIG_DIR", "LIBRARY_DIR", "WORK_DIR",
		"PUID", "PGID", "TZ",
		"WIREGUARD_PRIVATE_KEY", "SERVER_COUNTRIES",
		"PELICULA_PORT", "PELICULA_AUTH",
		"PELICULA_OPEN_REGISTRATION",
		"JELLYFIN_PASSWORD",
		"PROCULA_API_KEY", "WEBHOOK_SECRET",
		"TRANSCODING_ENABLED",
		"NOTIFICATIONS_ENABLED", "NOTIFICATIONS_MODE",
		"PELICULA_SUB_LANGS",
		"REQUESTS_RADARR_PROFILE_ID", "REQUESTS_RADARR_ROOT",
		"REQUESTS_SONARR_PROFILE_ID", "REQUESTS_SONARR_ROOT",
		"REMOTE_ACCESS_ENABLED", "REMOTE_HOSTNAME",
		"REMOTE_HTTP_PORT", "REMOTE_HTTPS_PORT",
		"REMOTE_CERT_MODE", "REMOTE_LE_EMAIL", "REMOTE_LE_STAGING",
		"SEEDING_REMOVE_ON_COMPLETE",
	}
	inOrder := make(map[string]bool, len(order))
	for _, k := range order {
		inOrder[k] = true
	}

	var sb strings.Builder
	sb.WriteString("# Generated by Pelicula\n")
	for _, k := range order {
		if v, ok := vars[k]; ok {
			fmt.Fprintf(&sb, "%s=\"%s\"\n", k, v)
		}
	}
	for k, v := range vars {
		if !inOrder[k] {
			fmt.Fprintf(&sb, "%s=\"%s\"\n", k, v)
		}
	}
	return os.WriteFile(path, []byte(sb.String()), 0600)
}
```

- [ ] **Step 5: Verify it compiles**

Run: `cd cmd/pelicula && go build -o /dev/null .`
Expected: compiles without errors (commands are stubs that panic for now)

- [ ] **Step 6: Commit**

```bash
git add cmd/pelicula/
git commit -m "feat(cli): scaffold Go CLI with command dispatch, platform detection, compose wrapper"
```

---

### Task 16: Implement core lifecycle commands (up, down, restart, status, logs)

**Files:**
- Create: `cmd/pelicula/cmd_up.go`
- Create: `cmd/pelicula/cmd_down.go`
- Create: `cmd/pelicula/cmd_restart.go`
- Create: `cmd/pelicula/cmd_status.go`
- Create: `cmd/pelicula/cmd_logs.go`
- Create: `cmd/pelicula/config_seed.go`
- Create: `cmd/pelicula/output.go`

- [ ] **Step 1: Create output.go with terminal formatting helpers**

```go
// cmd/pelicula/output.go
package main

import "fmt"

func info(msg string)  { fmt.Printf("\033[1;34m→\033[0m %s\n", msg) }
func pass(msg string)  { fmt.Printf("\033[1;32m✓\033[0m %s\n", msg) }
func warn(msg string)  { fmt.Printf("\033[1;33m!\033[0m %s\n", msg) }
func fail(msg string)  { fmt.Printf("\033[1;31m✗\033[0m %s\n", msg) }
func fatal(msg string) { fail(msg); os.Exit(1) }
```

- [ ] **Step 2: Create config_seed.go**

Port the config seeding logic from the bash CLI:
- `seedConfig(path, content)` — write file if it doesn't exist
- `seedArrConfig(configDir, service, urlBase)` — seed config.xml with UrlBase and auth bypass
- `enforceArrAuth(configPath)` — patch auth settings in config.xml (sed → Go string replacement)
- `seedJellyfinConfig(configDir)` — network.xml + branding.xml
- `seedBazarrConfig(configDir)` — config.ini with base_url
- `seedQBittorrentConfig(configDir)` — auth whitelist + categories
- `seedAllConfigs(configDir)` — orchestrates all of the above

- [ ] **Step 3: Implement cmdUp**

```go
// cmd/pelicula/cmd_up.go
func cmdUp(args []string) {
    projectDir := findProjectDir()
    platform := detectPlatform()
    envFile := filepath.Join(projectDir, ".env")

    // If no .env, run setup
    if _, err := os.Stat(envFile); os.IsNotExist(err) {
        cmdSetup(args)
        return
    }

    // Load and migrate .env
    changed, err := migrateEnv(envFile)
    if err != nil {
        fatal("env migration: " + err.Error())
    }
    if changed {
        info("Applied .env migrations")
    }

    vars, _ := parseEnv(envFile)
    configDir := vars["CONFIG_DIR"]

    // Setup directories
    setupDirs(vars)

    // Setup TLS cert if missing
    setupCert(configDir)

    // Render remote configs if enabled
    if vars["REMOTE_ACCESS_ENABLED"] == "true" {
        renderRemoteConfigs(projectDir, vars)
    }

    // Seed service configs
    seedAllConfigs(configDir)

    // Start containers
    compose := NewCompose(projectDir, platform)
    composeArgs := []string{"up", "-d"}
    if vars["NOTIFICATIONS_MODE"] == "apprise" {
        composeArgs = append(composeArgs, "--profile", "apprise")
    }
    info("Starting stack...")
    if err := compose.Run(composeArgs...); err != nil {
        fatal("docker compose up: " + err.Error())
    }

    // Wait for VPN health
    waitForVPN(compose)

    // Print URLs
    port := vars["PELICULA_PORT"]
    if port == "" { port = "7354" }
    pass("Stack is running")
    fmt.Printf("\n  Dashboard: http://localhost:%s\n\n", port)

    // Print admin credentials if first run
    if pw := vars["JELLYFIN_PASSWORD"]; pw != "" {
        fmt.Printf("  Jellyfin admin:    admin / %s\n\n", pw)
    }
}
```

- [ ] **Step 4: Implement cmdDown, cmdRestart, cmdStatus, cmdLogs**

Each is a thin wrapper around compose commands:
- `cmdDown` → `compose.Run("down")`
- `cmdRestart` → `cmdDown` + `cmdUp` (or `compose.Run("restart", service)` for single service)
- `cmdStatus` → HTTP GET to `/api/pelicula/health` and format output
- `cmdLogs` → `compose.Run("logs", "-f", service)`

- [ ] **Step 5: Verify compilation**

Run: `cd cmd/pelicula && go build -o /dev/null .`

- [ ] **Step 6: Commit**

```bash
git add cmd/pelicula/
git commit -m "feat(cli): implement up, down, restart, status, logs commands"
```

---

### Task 17: Implement setup, update, rebuild, reset-config commands

**Files:**
- Create: `cmd/pelicula/cmd_setup.go`
- Create: `cmd/pelicula/cmd_update.go`
- Create: `cmd/pelicula/cmd_rebuild.go`
- Create: `cmd/pelicula/cmd_reset.go`
- Create: `cmd/pelicula/cert.go`
- Create: `cmd/pelicula/tun.go`

- [ ] **Step 1: Implement cmdSetup**

The setup wizard runs in the browser. The Go CLI:
1. Detects platform defaults (config dir, library dir, timezone, PUID/PGID)
2. Starts nginx + middleware in setup mode via `docker-compose.setup.yml` (or just the main compose with a SETUP_MODE env var)
3. Opens browser to `http://localhost:7354/setup`
4. Polls for `.env` creation (2-second intervals)
5. Cleans up setup containers
6. Prints success message

- [ ] **Step 2: Implement cmdUpdate**

```go
func cmdUpdate(args []string) {
    projectDir := findProjectDir()
    compose := NewCompose(projectDir, detectPlatform())
    
    info("Pulling latest images...")
    if err := compose.Run("pull"); err != nil {
        fatal("pull: " + err.Error())
    }
    
    vars, _ := parseEnv(filepath.Join(projectDir, ".env"))
    upArgs := []string{"up", "-d"}
    if vars["NOTIFICATIONS_MODE"] == "apprise" {
        upArgs = append(upArgs, "--profile", "apprise")
    }
    info("Recreating containers...")
    if err := compose.Run(upArgs...); err != nil {
        fatal("up: " + err.Error())
    }
    pass("Update complete")
}
```

- [ ] **Step 3: Implement cmdRebuild**

Port the rebuild logic: build specific service images and restart without taking down the whole stack.

- [ ] **Step 4: Implement cmdResetConfig**

Port the reset-config logic with its three modes:
- Soft reset (default): wipe service configs, preserve API keys/VPN/certs
- Per-service reset: reset specific service
- Full reset (all): wipe config dir + regenerate .env

- [ ] **Step 5: Implement cert.go and tun.go**

- `cert.go`: Generate self-signed TLS cert using Go's `crypto/x509` (no openssl dependency)
- `tun.go`: Check/create `/dev/net/tun` on Linux, generate `docker-compose.override.yml`

- [ ] **Step 6: Verify compilation**

Run: `cd cmd/pelicula && go build -o /dev/null .`

- [ ] **Step 7: Commit**

```bash
git add cmd/pelicula/
git commit -m "feat(cli): implement setup, update, rebuild, reset-config commands"
```

---

### Task 18: Implement export, import-backup, import, check-vpn commands

**Files:**
- Create: `cmd/pelicula/cmd_export.go`
- Create: `cmd/pelicula/cmd_import.go`
- Create: `cmd/pelicula/cmd_vpn.go`

- [ ] **Step 1: Implement cmdExport**

HTTP GET to middleware API, write JSON to file. No Python dependency — use Go's json package for pretty-printing.

- [ ] **Step 2: Implement cmdImportBackup**

HTTP POST to middleware API with JSON file body.

- [ ] **Step 3: Implement cmdImport**

Open browser to import wizard URL.

- [ ] **Step 4: Implement cmdCheckVPN**

HTTP GET to middleware health endpoint, parse VPN status.

- [ ] **Step 5: Add go.mod for cmd/pelicula**

```
module github.com/peligwen/pelicula/cmd/pelicula
go 1.23
```

- [ ] **Step 6: Verify full compilation and basic smoke test**

Run: `cd cmd/pelicula && go build -o pelicula . && ./pelicula --version`
Expected: prints version

- [ ] **Step 7: Commit**

```bash
git add cmd/pelicula/
git commit -m "feat(cli): implement export, import-backup, import, check-vpn commands"
```

---

### Task 19: Add CLI tests

**Files:**
- Create: `cmd/pelicula/env_test.go`
- Create: `cmd/pelicula/platform_test.go`
- Create: `cmd/pelicula/config_seed_test.go`

- [ ] **Step 1: Write env migration tests**

Test `parseEnv`, `migrateEnv` (MEDIA_DIR migration, missing key defaults), `writeEnv` (canonical ordering).

- [ ] **Step 2: Write config seeding tests**

Test `seedConfig` (creates if missing, skips if exists), `enforceArrAuth` (patches auth settings).

- [ ] **Step 3: Write platform detection test**

Test `detectPlatform()` returns a valid Platform struct.

- [ ] **Step 4: Run tests**

Run: `cd cmd/pelicula && go test -v ./...`

- [ ] **Step 5: Update Makefile**

Add `test-cli` target:
```makefile
test-cli:
	cd cmd/pelicula && go test -v ./...

test: test-procula test-middleware test-cli
```

- [ ] **Step 6: Commit**

```bash
git add cmd/pelicula/*_test.go Makefile
git commit -m "test(cli): add unit tests for env migration, config seeding, platform detection"
```

---

### Task 20: Phase 3 review checkpoint

- [ ] **Step 1: Run full test suite**

Run: `make test`

- [ ] **Step 2: Build for multiple platforms**

```bash
cd cmd/pelicula
GOOS=darwin GOARCH=arm64 go build -o /dev/null .
GOOS=linux GOARCH=amd64 go build -o /dev/null .
GOOS=windows GOARCH=amd64 go build -o /dev/null .
```

- [ ] **Step 3: Code review**

Review Phase 3 changes for correctness, security, and feature parity with bash CLI.

---

## Phase 4: API Contract Freeze + Documentation

### Task 21: Document stable API endpoints in API.md

**Files:**
- Modify: `API.md`

- [ ] **Step 1: Add stability markers to API.md**

For each endpoint, add a "Stable since v1.0" marker. Document the response shapes that are now frozen. Add a policy section explaining:
- Fields are additive only (never removed or renamed)
- New endpoints may be added
- Breaking changes only at major version bumps
- Frontend treats unknown fields as ignorable

- [ ] **Step 2: Document backup format versioning**

Add a section to API.md explaining the backup format version history:
- v1: movies + series
- v2: movies + series + roles + invites + requests

- [ ] **Step 3: Document new environment variables**

Add all new service URL environment variables to the README or a configuration reference:
- `SONARR_URL`, `RADARR_URL`, `PROWLARR_URL`, `BAZARR_URL`
- `JELLYFIN_URL`, `QBITTORRENT_URL`, `PROCULA_URL`, `APPRISE_URL`

- [ ] **Step 4: Update ROADMAP.md**

Mark the following as shipped:
- SQLite data layer
- Auto-migration framework
- Go CLI rewrite
- Backup forward compatibility
- Service URL configuration
- API contract freeze

Remove "Procula queue: JSON files vs SQLite" from Deferred (it's now done).
Update the "Pelicula for Windows" section to reflect the Go CLI is complete.

- [ ] **Step 5: Commit**

```bash
git add API.md ROADMAP.md
git commit -m "docs: freeze API contract, document new env vars, update roadmap"
```

---

### Task 22: Final verification

- [ ] **Step 1: Run full test suite with race detection**

Run: `make test-race`
Expected: ALL PASS

- [ ] **Step 2: Verify Go CLI compiles for all platforms**

```bash
cd cmd/pelicula
GOOS=darwin GOARCH=arm64 go build -o pelicula-darwin-arm64 .
GOOS=linux GOARCH=amd64 go build -o pelicula-linux-amd64 .
GOOS=windows GOARCH=amd64 go build -o pelicula-windows-amd64.exe .
rm pelicula-darwin-arm64 pelicula-linux-amd64 pelicula-windows-amd64.exe
```

- [ ] **Step 3: Verify migration works with sample data**

Create sample JSON files matching the old format, start the migration code, verify SQLite DBs are created correctly and JSON files are renamed.

- [ ] **Step 4: Final code review**

Use code-reviewer agent to review the entire hardening work across all phases.
