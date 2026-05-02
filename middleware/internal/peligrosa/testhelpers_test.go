package peligrosa

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"pelicula-api/clients"

	_ "modernc.org/sqlite"
)

// testDB creates a fresh SQLite database in t.TempDir() with the pelicula schema.
// Duplicated from the main package — peligrosa tests cannot import package main.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("testDB: open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		t.Fatalf("testDB: WAL mode: %v", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		db.Close()
		t.Fatalf("testDB: foreign_keys: %v", err)
	}
	if err := createTestSchema(db); err != nil {
		db.Close()
		t.Fatalf("testDB: schema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// createTestSchema creates the tables required by peligrosa tests.
// Mirrors the schema from db.go in the main package.
func createTestSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS roles (
			jellyfin_id TEXT PRIMARY KEY,
			username    TEXT NOT NULL,
			role        TEXT NOT NULL DEFAULT 'viewer'
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
	`)
	return err
}

// fakeJellyfinHTTPClient implements clients.JellyfinClient using an httptest.Server.
// Used by peligrosa tests to exercise invite redemption and open registration without
// importing package main.
type fakeJellyfinHTTPClient struct {
	srv *httptest.Server
}

func (c *fakeJellyfinHTTPClient) AuthenticateByName(ctx context.Context, username, password string) (*clients.JellyfinLoginResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.srv.URL+"/Users/AuthenticateByName", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.srv.Client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, &clients.JellyfinHTTPError{StatusCode: resp.StatusCode}
	}
	var raw struct {
		User struct {
			Id     string `json:"Id"`
			Name   string `json:"Name"`
			Policy struct {
				IsAdministrator bool `json:"IsAdministrator"`
			} `json:"Policy"`
		} `json:"User"`
		AccessToken string `json:"AccessToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return &clients.JellyfinLoginResult{
		UserID:          raw.User.Id,
		Username:        raw.User.Name,
		IsAdministrator: raw.User.Policy.IsAdministrator,
		AccessToken:     raw.AccessToken,
	}, nil
}

func (c *fakeJellyfinHTTPClient) CreateUser(ctx context.Context, username, password string) (string, error) {
	if password == "" {
		return "", clients.ErrPasswordRequired
	}
	payload := fmt.Sprintf(`{"Name":%q,"Password":%q}`, username, password)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.srv.URL+"/Users/New", strings.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.srv.Client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", &clients.JellyfinHTTPError{StatusCode: resp.StatusCode}
	}
	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	id, _ := result["Id"].(string)
	if id == "" {
		return "", fmt.Errorf("no user ID in create response")
	}
	return id, nil
}

// newFakeJellyfinClient starts a test Jellyfin server and returns a client and cleanup.
// setup configures extra handlers; a basic /Users/AuthenticateByName is pre-registered.
func newFakeJellyfinClient(t *testing.T, setup func(mux *http.ServeMux)) clients.JellyfinClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/Users/AuthenticateByName", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"User":{"Id":"jf-user-001","Name":"alice","Policy":{"IsAdministrator":false}},"AccessToken":"test-jf-token","ServerId":"srv1"}`))
	})
	if setup != nil {
		setup(mux)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &fakeJellyfinHTTPClient{srv: srv}
}

// newFakeJellyfinServer starts a test Jellyfin server and returns both the server
// and a client. Used by tests that need direct access to the server (e.g. to call
// srv.Client() with custom handlers).
func newFakeJellyfinServer(t *testing.T, setup func(mux *http.ServeMux)) (*httptest.Server, clients.JellyfinClient) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/Users/AuthenticateByName", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"User":{"Id":"jf-user-001","Name":"alice","Policy":{"IsAdministrator":false}},"AccessToken":"test-jf-token"}`))
	})
	if setup != nil {
		setup(mux)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &fakeJellyfinHTTPClient{srv: srv}
}
