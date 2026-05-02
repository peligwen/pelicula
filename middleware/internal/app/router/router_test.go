package router_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"pelicula-api/internal/app/actions"
	"pelicula-api/internal/app/adminops"
	"pelicula-api/internal/app/backup"
	"pelicula-api/internal/app/catalog"
	"pelicula-api/internal/app/downloads"
	"pelicula-api/internal/app/health"
	"pelicula-api/internal/app/hooks"
	jfapp "pelicula-api/internal/app/jellyfin"
	"pelicula-api/internal/app/library"
	"pelicula-api/internal/app/network"
	"pelicula-api/internal/app/router"
	"pelicula-api/internal/app/search"
	"pelicula-api/internal/app/settings"
	"pelicula-api/internal/app/sse"
	"pelicula-api/internal/app/sysinfo"
	"pelicula-api/internal/peligrosa"
	reporeqs "pelicula-api/internal/repo/requests"
	"pelicula-api/internal/repo/sessions"
)

// testDB creates a fresh SQLite DB with the tables that peligrosa.NewAuth needs.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("testDB: open: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		t.Fatalf("testDB: WAL: %v", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		db.Close()
		t.Fatalf("testDB: foreign_keys: %v", err)
	}
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS roles (
			jellyfin_id TEXT PRIMARY KEY,
			username    TEXT NOT NULL,
			role        TEXT NOT NULL DEFAULT 'viewer'
		);
		CREATE TABLE IF NOT EXISTS sessions (
			token      TEXT PRIMARY KEY,
			username   TEXT NOT NULL,
			role       TEXT NOT NULL,
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS rate_limits (
			ip           TEXT PRIMARY KEY,
			fail_count   INTEGER NOT NULL DEFAULT 0,
			window_start TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS invites (
			token      TEXT PRIMARY KEY,
			label      TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			created_by TEXT NOT NULL DEFAULT '',
			expires_at TEXT,
			max_uses   INTEGER,
			uses       INTEGER NOT NULL DEFAULT 0,
			revoked    INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS redemptions (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
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
			poster       TEXT,
			requested_by TEXT NOT NULL DEFAULT '',
			state        TEXT NOT NULL DEFAULT 'pending',
			reason       TEXT,
			arr_id       INTEGER,
			created_at   TEXT NOT NULL,
			updated_at   TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS request_events (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			request_id TEXT NOT NULL REFERENCES requests(id) ON DELETE CASCADE,
			at         TEXT NOT NULL,
			state      TEXT NOT NULL,
			actor      TEXT NOT NULL DEFAULT '',
			note       TEXT NOT NULL DEFAULT ''
		);
	`)
	if err != nil {
		db.Close()
		t.Fatalf("testDB: schema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// buildTestMux creates a ServeMux with all routes wired via router.Register.
// Returns the mux and a viewer-role session token. The inner handlers are
// zero-value stubs — auth guards short-circuit before reaching them for the
// 401/403/403-CSRF test cases, so the stubs are never invoked.
func buildTestMux(t *testing.T) (mux *http.ServeMux, viewerToken string) {
	t.Helper()
	db := testDB(t)

	// Seed a viewer session into SQLite before constructing Auth so that
	// NewAuth's loadSessionsFromDB pull it into the in-memory map.
	const token = "router-test-viewer-token"
	expiry := time.Now().Add(time.Hour)
	sess := sessions.New(db)
	if err := sess.Create(context.Background(), token, "alice", "viewer", expiry); err != nil {
		t.Fatalf("seed viewer session: %v", err)
	}

	auth := peligrosa.NewAuth(peligrosa.AuthConfig{DB: db})

	reqStore := peligrosa.NewRequestStore(reporeqs.New(db), nil)
	inviteStore := peligrosa.NewInviteStore(db, nil)
	deps := peligrosa.NewDeps(db, auth, inviteStore, reqStore, nil)

	cfg := router.Config{
		Auth:          auth,
		Deps:          deps,
		Health:        &health.Handler{},
		SSE:           sse.NewHub(),
		Sysinfo:       &sysinfo.Handler{},
		Downloads:     &downloads.Handler{},
		Hooks:         &hooks.Handler{},
		Backup:        &backup.Handler{},
		JF:            &jfapp.Handler{},
		JFInfo:        nil, // nil skips the optional route — guards the if-nil branch
		Library:       &library.Handler{},
		Catalog:       &catalog.Handler{},
		Search:        &search.Handler{},
		Settings:      &settings.Handler{},
		Actions:       &actions.Handler{},
		Admin:         &adminops.Handler{},
		Network:       &network.Handler{},
		StatusHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }),
		JobsHandler:   http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }),
	}

	mux = http.NewServeMux()
	router.Register(mux, cfg)
	return mux, token
}

// TestRouterAuthGates_PublicPaths asserts that public endpoints return non-401 with no cookie.
// /api/pelicula/hooks/import and /api/pelicula/jellyfin/refresh expect POST and return 405
// for GET — that is still non-401 and confirms the routes are public (no auth guard).
func TestRouterAuthGates_PublicPaths(t *testing.T) {
	mux, _ := buildTestMux(t)

	cases := []struct {
		method string
		path   string
	}{
		{"GET", "/api/pelicula/auth/check"},
		{"GET", "/api/pelicula/hooks/import"},
		{"GET", "/api/pelicula/jellyfin/refresh"},
		{"GET", "/api/pelicula/libraries"},
	}

	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			r := httptest.NewRequest(c.method, c.path, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code == http.StatusUnauthorized {
				t.Errorf("public path %s %s returned 401, want non-401", c.method, c.path)
			}
		})
	}
}

// TestRouterAuthGates_ViewerGuardedPaths asserts 401 with no cookie.
func TestRouterAuthGates_ViewerGuardedPaths(t *testing.T) {
	mux, _ := buildTestMux(t)

	paths := []string{
		"/api/pelicula/status",
		"/api/pelicula/downloads",
		"/api/pelicula/sse",
		"/api/pelicula/catalog",
		"/api/pelicula/sessions",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("viewer-guarded %s: want 401 with no cookie, got %d", path, w.Code)
			}
		})
	}
}

// TestRouterAuthGates_AdminPaths asserts 401 with no cookie, 403 with viewer cookie.
func TestRouterAuthGates_AdminPaths(t *testing.T) {
	mux, viewerToken := buildTestMux(t)

	paths := []string{
		"/api/pelicula/admin/stack/restart",
		"/api/pelicula/settings",
		"/api/pelicula/users",
		"/api/pelicula/arr-meta",
		"/api/pelicula/downloads/cancel",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			// No cookie → 401 (not authenticated).
			r := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("%s no-cookie: want 401, got %d", path, w.Code)
			}

			// Viewer cookie → 403 (authenticated but insufficient role).
			r2 := httptest.NewRequest(http.MethodGet, path, nil)
			r2.AddCookie(&http.Cookie{Name: "pelicula_session", Value: viewerToken})
			w2 := httptest.NewRecorder()
			mux.ServeHTTP(w2, r2)
			if w2.Code != http.StatusForbidden {
				t.Errorf("%s viewer-cookie: want 403, got %d", path, w2.Code)
			}
		})
	}
}

// TestRouterAuthGates_CSRFStrict asserts 403 on POST with a foreign Origin header.
// RequireLocalOriginStrict is wired around admin write endpoints — a non-LAN Origin
// must be rejected even when the caller is authenticated.
func TestRouterAuthGates_CSRFStrict(t *testing.T) {
	mux, _ := buildTestMux(t)

	// /api/pelicula/register is wrapped in RequireLocalOriginStrict with no auth
	// guard above it, so a POST with a foreign Origin exercises pure CSRF rejection
	// without needing a valid session.
	r := httptest.NewRequest(http.MethodPost, "/api/pelicula/register", nil)
	r.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("POST /api/pelicula/register with foreign Origin: want 403, got %d", w.Code)
	}
}
