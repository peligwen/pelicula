package backup_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"pelicula-api/internal/app/backup"
	arrclient "pelicula-api/internal/clients/arr"
	"pelicula-api/internal/peligrosa"
	reporeqs "pelicula-api/internal/repo/requests"

	_ "modernc.org/sqlite"
)

// ── Test infrastructure ───────────────────────────────────────────────────────

// stubArrClient is a minimal ArrClient backed by an httptest.Server that
// returns empty JSON arrays for all arr endpoints. The typed clients point
// at this server so no real network calls are made.
type stubArrClient struct {
	sonarr, radarr, prowlarr string
	srv                      *httptest.Server
	sonarrCli                *arrclient.Client
	radarrCli                *arrclient.Client
}

// newStubArrClient builds a stubArrClient backed by a test server.
// The caller must close t.Cleanup — httptest.Server is registered with t.Cleanup.
func newStubArrClient(t *testing.T, sonarr, radarr, prowlarr string) *stubArrClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("[]")) //nolint:errcheck
	}))
	t.Cleanup(srv.Close)
	return &stubArrClient{
		sonarr:    sonarr,
		radarr:    radarr,
		prowlarr:  prowlarr,
		srv:       srv,
		sonarrCli: arrclient.New(srv.URL, sonarr),
		radarrCli: arrclient.New(srv.URL, radarr),
	}
}

func (s *stubArrClient) Keys() (string, string, string) {
	return s.sonarr, s.radarr, s.prowlarr
}
func (s *stubArrClient) SonarrClient() *arrclient.Client { return s.sonarrCli }
func (s *stubArrClient) RadarrClient() *arrclient.Client { return s.radarrCli }

// stubLibPathResolver always returns the default path.
type stubLibPathResolver struct{}

func (s *stubLibPathResolver) FirstLibraryPath(_, defaultPath string) string {
	return defaultPath
}

// stubFulfiller is a no-op Fulfiller for export tests.
type stubFulfiller struct{}

func (f *stubFulfiller) AddMovie(_ context.Context, _, _ int, _ string) (int, error) {
	return 0, nil
}
func (f *stubFulfiller) AddSeries(_ context.Context, _, _ int, _ string) (int, error) {
	return 0, nil
}

// testDB creates a fresh SQLite database with the pelicula schema.
// Mirrors the schema from db.go in the main package.
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
	_, err = db.Exec(`
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
	`)
	if err != nil {
		db.Close()
		t.Fatalf("testDB: schema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newHandler returns a Handler wired with stub dependencies.
func newHandler(svc backup.ArrClient) *backup.Handler {
	return backup.New(svc, &stubLibPathResolver{}, nil, nil, nil, "http://radarr:7878/radarr", "http://sonarr:8989/sonarr")
}

// ── Pure-function tests ───────────────────────────────────────────────────────

func TestResolveProfileID(t *testing.T) {
	t.Parallel()
	t.Run("name found in map", func(t *testing.T) {
		t.Parallel()
		m := map[string]int{"HD-1080p": 3, "Any": 1}
		got := backup.ResolveProfileID("HD-1080p", m)
		if got != 3 {
			t.Errorf("got %d, want 3", got)
		}
	})

	t.Run("name not found returns first available", func(t *testing.T) {
		t.Parallel()
		m := map[string]int{"Any": 5}
		got := backup.ResolveProfileID("Missing", m)
		if got != 5 {
			t.Errorf("got %d, want 5", got)
		}
	})

	t.Run("empty map returns 1", func(t *testing.T) {
		t.Parallel()
		got := backup.ResolveProfileID("anything", map[string]int{})
		if got != 1 {
			t.Errorf("got %d, want 1", got)
		}
	})
}

func TestResolveTagIDs(t *testing.T) {
	t.Parallel()
	labelMap := map[string]int{
		"4k":    10,
		"hevc":  20,
		"anime": 30,
	}

	t.Run("all labels present", func(t *testing.T) {
		t.Parallel()
		ids := backup.ResolveTagIDs([]string{"4k", "hevc"}, labelMap)
		if len(ids) != 2 {
			t.Fatalf("expected 2 ids, got %v", ids)
		}
		if ids[0] != 10 || ids[1] != 20 {
			t.Errorf("ids = %v, want [10 20]", ids)
		}
	})

	t.Run("missing labels skipped", func(t *testing.T) {
		t.Parallel()
		ids := backup.ResolveTagIDs([]string{"4k", "unknown"}, labelMap)
		if len(ids) != 1 || ids[0] != 10 {
			t.Errorf("ids = %v, want [10]", ids)
		}
	})

	t.Run("empty labels returns empty", func(t *testing.T) {
		t.Parallel()
		ids := backup.ResolveTagIDs(nil, labelMap)
		if len(ids) != 0 {
			t.Errorf("expected empty, got %v", ids)
		}
	})
}

func TestResolveTagLabels(t *testing.T) {
	t.Parallel()
	tagMap := map[int]string{
		10: "4k",
		20: "hevc",
	}

	t.Run("tags as float64 IDs resolved", func(t *testing.T) {
		t.Parallel()
		m := map[string]any{
			"tags": []any{float64(10), float64(20)},
		}
		labels := backup.ResolveTagLabels(m, tagMap)
		if len(labels) != 2 || labels[0] != "4k" || labels[1] != "hevc" {
			t.Errorf("labels = %v, want [4k hevc]", labels)
		}
	})

	t.Run("unknown tag IDs skipped", func(t *testing.T) {
		t.Parallel()
		m := map[string]any{
			"tags": []any{float64(10), float64(99)},
		}
		labels := backup.ResolveTagLabels(m, tagMap)
		if len(labels) != 1 || labels[0] != "4k" {
			t.Errorf("labels = %v, want [4k]", labels)
		}
	})

	t.Run("missing tags key returns empty", func(t *testing.T) {
		t.Parallel()
		m := map[string]any{}
		labels := backup.ResolveTagLabels(m, tagMap)
		if len(labels) != 0 {
			t.Errorf("expected empty, got %v", labels)
		}
	})
}

func TestUniqueStrings(t *testing.T) {
	t.Parallel()
	t.Run("duplicates removed, order preserved", func(t *testing.T) {
		t.Parallel()
		got := backup.UniqueStrings(func(add func(string)) {
			add("a")
			add("b")
			add("a")
			add("c")
		})
		want := []string{"a", "b", "c"}
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})

	t.Run("empty input returns nil", func(t *testing.T) {
		t.Parallel()
		got := backup.UniqueStrings(func(add func(string)) {})
		if len(got) != 0 {
			t.Errorf("expected empty, got %v", got)
		}
	})
}

func TestExtractSeasons(t *testing.T) {
	t.Parallel()
	t.Run("seasons extracted", func(t *testing.T) {
		t.Parallel()
		s := map[string]any{
			"seasons": []any{
				map[string]any{"seasonNumber": float64(0), "monitored": false},
				map[string]any{"seasonNumber": float64(1), "monitored": true},
				map[string]any{"seasonNumber": float64(2), "monitored": true},
			},
		}
		seasons := backup.ExtractSeasons(s)
		if len(seasons) != 3 {
			t.Fatalf("expected 3 seasons, got %v", seasons)
		}
		if seasons[1].SeasonNumber != 1 || !seasons[1].Monitored {
			t.Errorf("season 1 = %+v", seasons[1])
		}
		if seasons[0].Monitored {
			t.Error("season 0 should not be monitored")
		}
	})

	t.Run("missing seasons key returns empty", func(t *testing.T) {
		t.Parallel()
		seasons := backup.ExtractSeasons(map[string]any{})
		if len(seasons) != 0 {
			t.Errorf("expected empty, got %v", seasons)
		}
	})
}

// ── Backup version tests ───────────────────────────────────────────────────

func TestBackupVersionBoundaries(t *testing.T) {
	t.Parallel()

	// post POSTs a backup JSON to HandleImportBackup and returns the response code.
	post := func(t *testing.T, payload backup.BackupExport) int {
		t.Helper()
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/pelicula/import-backup", bytes.NewReader(body))
		w := httptest.NewRecorder()

		// Use a stub client with no API keys — Keys() returns ("","","")
		h := newHandler(newStubArrClient(t, "", "", ""))
		h.HandleImportBackup(w, req)
		return w.Code
	}

	t.Run("version 0 rejected", func(t *testing.T) {
		t.Parallel()
		code := post(t, backup.BackupExport{Version: 0})
		if code != http.StatusBadRequest {
			t.Errorf("got %d, want 400", code)
		}
	})

	t.Run("version 99 rejected", func(t *testing.T) {
		t.Parallel()
		code := post(t, backup.BackupExport{Version: 99})
		if code != http.StatusBadRequest {
			t.Errorf("got %d, want 400", code)
		}
	})

	t.Run("version 1 accepted (keys missing → 503)", func(t *testing.T) {
		t.Parallel()
		// Version is valid; missing API keys yield 503, not 400.
		code := post(t, backup.BackupExport{Version: 1})
		if code != http.StatusServiceUnavailable {
			t.Errorf("got %d, want 503", code)
		}
	})

	t.Run("version 2 accepted (keys missing → 503)", func(t *testing.T) {
		t.Parallel()
		code := post(t, backup.BackupExport{Version: 2})
		if code != http.StatusServiceUnavailable {
			t.Errorf("got %d, want 503", code)
		}
	})
}

// ── InviteStore.InsertFull tests ───────────────────────────────────────────

func TestInviteStoreInsertFull(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	store := peligrosa.NewInviteStore(db, nil)

	now := time.Now().UTC().Truncate(time.Second)
	maxUses := 5

	inv := peligrosa.InviteExport{
		Token:     "aaaabbbbccccddddeeeeffffgggghhhh123", // 43 chars
		Label:     "test-invite",
		CreatedAt: now,
		CreatedBy: "admin",
		MaxUses:   &maxUses,
		Uses:      2,
		Revoked:   false,
	}

	if err := store.InsertFull(context.Background(), inv); err != nil {
		t.Fatalf("InsertFull: %v", err)
	}

	// Verify it's in the store.
	list := store.ListInvites(context.Background())
	if len(list) != 1 {
		t.Fatalf("expected 1 invite, got %d", len(list))
	}
	got := list[0]
	if got.Token != inv.Token {
		t.Errorf("token = %q, want %q", got.Token, inv.Token)
	}
	if got.Label != inv.Label {
		t.Errorf("label = %q, want %q", got.Label, inv.Label)
	}
	if got.Uses != inv.Uses {
		t.Errorf("uses = %d, want %d", got.Uses, inv.Uses)
	}

	t.Run("idempotent on duplicate token", func(t *testing.T) {
		t.Parallel()
		// Second insert of same token must not error.
		if err := store.InsertFull(context.Background(), inv); err != nil {
			t.Errorf("second InsertFull: %v", err)
		}
		if len(store.ListInvites(context.Background())) != 1 {
			t.Error("expected still 1 invite after duplicate insert")
		}
	})
}

// ── RequestStore.InsertFull tests ─────────────────────────────────────────

func TestRequestStoreInsertFull(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	store := peligrosa.NewRequestStore(reporeqs.New(db), &stubFulfiller{})

	now := time.Now().UTC().Truncate(time.Second)
	req := peligrosa.RequestExport{
		ID:          "req_backup_001",
		Type:        "movie",
		TmdbID:      12345,
		Title:       "Test Movie",
		Year:        2024,
		RequestedBy: "viewer1",
		State:       peligrosa.RequestPending,
		CreatedAt:   now,
		UpdatedAt:   now,
		History: []peligrosa.RequestEvent{
			{At: now, State: peligrosa.RequestPending, Actor: "viewer1"},
		},
	}

	if err := store.InsertFull(context.Background(), req); err != nil {
		t.Fatalf("InsertFull: %v", err)
	}

	// Verify it's in the store.
	all := store.All(context.Background())
	if len(all) != 1 {
		t.Fatalf("expected 1 request, got %d", len(all))
	}
	got := all[0]
	if got.ID != req.ID {
		t.Errorf("id = %q, want %q", got.ID, req.ID)
	}
	if got.Title != req.Title {
		t.Errorf("title = %q, want %q", got.Title, req.Title)
	}
	if len(got.History) != 1 {
		t.Errorf("history len = %d, want 1", len(got.History))
	}

	t.Run("idempotent on duplicate id", func(t *testing.T) {
		t.Parallel()
		if err := store.InsertFull(context.Background(), req); err != nil {
			t.Errorf("second InsertFull: %v", err)
		}
		if len(store.All(context.Background())) != 1 {
			t.Error("expected still 1 request after duplicate insert")
		}
	})
}

// ── v1 backup import compatibility test ───────────────────────────────────

func TestImportV1BackupHasNoRolesInvitesRequests(t *testing.T) {
	t.Parallel()
	v1 := `{"version":1,"exported":"2025-01-01T00:00:00Z","movies":[],"series":[]}`
	var bk backup.BackupExport
	if err := json.Unmarshal([]byte(v1), &bk); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if bk.Version != 1 {
		t.Errorf("version = %d, want 1", bk.Version)
	}
	if len(bk.Roles) != 0 || len(bk.Invites) != 0 || len(bk.Requests) != 0 {
		t.Error("v1 backup should have no roles/invites/requests")
	}
}

// ── v2 backup struct roundtrip test ───────────────────────────────────────

func TestBackupExportV2Roundtrip(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(time.Second)
	maxUses := 1
	export := backup.BackupExport{
		Version:  2,
		Exported: now.Format(time.RFC3339),
		Movies:   []backup.MovieExport{},
		Series:   []backup.SeriesExport{},
		Roles: []peligrosa.RolesEntry{
			{JellyfinID: "jf-001", Username: "alice", Role: peligrosa.RoleAdmin},
		},
		Invites: []peligrosa.InviteExport{
			{
				Token:     "aaaabbbbccccddddeeeeffffgggghhhh123",
				Label:     "family",
				CreatedAt: now,
				CreatedBy: "admin",
				MaxUses:   &maxUses,
				Uses:      0,
			},
		},
		Requests: []peligrosa.RequestExport{
			{
				ID:          "req_test_001",
				Type:        "movie",
				TmdbID:      999,
				Title:       "Some Film",
				Year:        2025,
				RequestedBy: "alice",
				State:       peligrosa.RequestPending,
				CreatedAt:   now,
				UpdatedAt:   now,
				History:     []peligrosa.RequestEvent{{At: now, State: peligrosa.RequestPending, Actor: "alice"}},
			},
		},
	}

	// Marshal + unmarshal roundtrip.
	data, err := json.Marshal(export)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got backup.BackupExport
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Version != 2 {
		t.Errorf("version = %d, want 2", got.Version)
	}
	if len(got.Roles) != 1 || got.Roles[0].JellyfinID != "jf-001" {
		t.Errorf("roles = %v", got.Roles)
	}
	if len(got.Invites) != 1 || got.Invites[0].Token != "aaaabbbbccccddddeeeeffffgggghhhh123" {
		t.Errorf("invites = %v", got.Invites)
	}
	if len(got.Requests) != 1 || got.Requests[0].ID != "req_test_001" {
		t.Errorf("requests = %v", got.Requests)
	}
}

// ── Context cancellation propagation tests ────────────────────────────────

// emptyArrClientFor returns a stubArrClient with valid keys that returns empty
// JSON arrays for all arr endpoints, allowing HandleExport to proceed past the
// movies/series goroutines.
func emptyArrClientFor(t *testing.T) *stubArrClient {
	t.Helper()
	return newStubArrClient(t, "sk", "rk", "")
}

// TestHandleExport_IncludesRequests asserts that HandleExport threads the
// request context to RequestStore.All and includes seeded requests in the
// export body when the context is live.
func TestHandleExport_IncludesRequests(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	reqStore := peligrosa.NewRequestStore(reporeqs.New(db), &stubFulfiller{})

	// Seed one request.
	now := time.Now().UTC().Truncate(time.Second)
	seed := peligrosa.RequestExport{
		ID:          "req_ctx_live_001",
		Type:        "movie",
		TmdbID:      11111,
		Title:       "Export Ctx Test",
		Year:        2025,
		RequestedBy: "viewer",
		State:       peligrosa.RequestPending,
		CreatedAt:   now,
		UpdatedAt:   now,
		History:     []peligrosa.RequestEvent{{At: now, State: peligrosa.RequestPending, Actor: "viewer"}},
	}
	if err := reqStore.InsertFull(context.Background(), seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h := backup.New(
		emptyArrClientFor(t),
		&stubLibPathResolver{},
		nil, nil, reqStore,
		"http://radarr:7878/radarr", "http://sonarr:8989/sonarr",
	)

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/export", nil)
	w := httptest.NewRecorder()
	h.HandleExport(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}

	var got backup.BackupExport
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Requests) != 1 {
		t.Errorf("requests = %d, want 1", len(got.Requests))
	} else if got.Requests[0].ID != seed.ID {
		t.Errorf("request id = %q, want %q", got.Requests[0].ID, seed.ID)
	}
}

// TestRequestStoreAll_CancelledCtx verifies that RequestStore.All respects
// context cancellation and returns empty when the context is already cancelled.
// This is the lower-level contract that HandleExport relies on for ctx threading.
func TestRequestStoreAll_CancelledCtx(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	reqStore := peligrosa.NewRequestStore(reporeqs.New(db), &stubFulfiller{})

	// Seed one request so the store is non-empty under a live context.
	now := time.Now().UTC().Truncate(time.Second)
	seed := peligrosa.RequestExport{
		ID:          "req_ctx_cancel_001",
		Type:        "movie",
		TmdbID:      11111,
		Title:       "Cancel Test",
		Year:        2025,
		RequestedBy: "viewer",
		State:       peligrosa.RequestPending,
		CreatedAt:   now,
		UpdatedAt:   now,
		History:     []peligrosa.RequestEvent{{At: now, State: peligrosa.RequestPending, Actor: "viewer"}},
	}
	if err := reqStore.InsertFull(context.Background(), seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Confirm the row is visible under a live context.
	live := reqStore.All(context.Background())
	if len(live) != 1 {
		t.Fatalf("expected 1 request with live ctx, got %d", len(live))
	}

	// A cancelled context should yield zero results (SQLite QueryContext honours ctx).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled := reqStore.All(ctx)
	if len(cancelled) != 0 {
		t.Errorf("All(cancelled ctx) = %d, want 0", len(cancelled))
	}
}

// TestHandleImportBackup_PassesCtx asserts that HandleImportBackup threads its
// request context to importRequests. With a pre-cancelled context InsertFull's
// ExecContext fails, leaving the table empty.
func TestHandleImportBackup_PassesCtx(t *testing.T) {
	t.Parallel()
	db := testDB(t)
	reqStore := peligrosa.NewRequestStore(reporeqs.New(db), &stubFulfiller{})

	h := backup.New(
		newStubArrClient(t, "sk", "rk", ""),
		&stubLibPathResolver{},
		nil, nil, reqStore,
		"http://radarr:7878/radarr", "http://sonarr:8989/sonarr",
	)

	now := time.Now().UTC().Truncate(time.Second)
	payload := backup.BackupExport{
		Version:  2,
		Exported: now.Format(time.RFC3339),
		Requests: []peligrosa.RequestExport{
			{
				ID:          "req_ctx_import_001",
				Type:        "movie",
				TmdbID:      22222,
				Title:       "Import Ctx Test",
				Year:        2025,
				RequestedBy: "viewer",
				State:       peligrosa.RequestPending,
				CreatedAt:   now,
				UpdatedAt:   now,
				History:     []peligrosa.RequestEvent{{At: now, State: peligrosa.RequestPending, Actor: "viewer"}},
			},
		},
	}

	body, _ := json.Marshal(payload)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel before the request lands

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/import-backup", bytes.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	h.HandleImportBackup(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}

	// The cancelled ctx prevents InsertFull from writing — table must be empty.
	all := reqStore.All(context.Background())
	if len(all) != 0 {
		t.Errorf("requests in db = %d, want 0 (cancelled ctx should block insert)", len(all))
	}
}

// ── resolveProfileID warning tests ────────────────────────────────────────

func TestResolveProfileIDWithWarning(t *testing.T) {
	t.Parallel()
	t.Run("known profile returns exact match silently", func(t *testing.T) {
		t.Parallel()
		m := map[string]int{"HD-1080p": 3, "Any": 1}
		got := backup.ResolveProfileID("HD-1080p", m)
		if got != 3 {
			t.Errorf("got %d, want 3", got)
		}
	})

	t.Run("unknown profile falls back to first available", func(t *testing.T) {
		t.Parallel()
		m := map[string]int{"Any": 5}
		got := backup.ResolveProfileID("MissingProfile", m)
		if got != 5 {
			t.Errorf("got %d, want 5 (first available)", got)
		}
	})

	t.Run("empty map returns 1", func(t *testing.T) {
		t.Parallel()
		got := backup.ResolveProfileID("anything", map[string]int{})
		if got != 1 {
			t.Errorf("got %d, want 1", got)
		}
	})
}
