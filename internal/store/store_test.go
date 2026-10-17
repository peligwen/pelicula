package store

import (
	"context"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenMemory()
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSessions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	sess := Session{Token: "tok", Username: "gwen", ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ctx, "tok")
	if err != nil || got == nil || got.Username != "gwen" || got.CreatedAt.IsZero() {
		t.Fatalf("GetSession: %+v %v", got, err)
	}

	// Expired sessions read as missing and are purged.
	_ = s.CreateSession(ctx, Session{Token: "old", Username: "gwen", ExpiresAt: time.Now().Add(-time.Minute)})
	if got, _ := s.GetSession(ctx, "old"); got != nil {
		t.Fatal("expired session returned")
	}
	if got, _ := s.GetSession(ctx, "missing"); got != nil {
		t.Fatal("missing session returned")
	}
	_ = s.CreateSession(ctx, Session{Token: "old2", Username: "gwen", ExpiresAt: time.Now().Add(-time.Minute)})
	if n, err := s.PurgeExpiredSessions(ctx); err != nil || n != 1 {
		t.Fatalf("PurgeExpiredSessions n=%d err=%v", n, err)
	}

	if err := s.DeleteSession(ctx, "tok"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSession(ctx, "tok"); got != nil {
		t.Fatal("DeleteSession left the row behind")
	}
}

func TestJobs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	j := &Job{ArrType: "radarr", ArrID: 7, Title: "Movie", Path: "/media/movies/m.mkv", Size: 10, DownloadID: "HASH"}
	if err := s.EnqueueJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	// Same path while queued dedupes to the existing row.
	dup := &Job{ArrType: "radarr", ArrID: 7, Title: "Movie", Path: "/media/movies/m.mkv"}
	if err := s.EnqueueJob(ctx, dup); err != nil {
		t.Fatal(err)
	}
	if dup.ID != j.ID {
		t.Fatalf("dedupe failed: %d vs %d", dup.ID, j.ID)
	}
	claimed, err := s.ClaimNextJob(ctx)
	if err != nil || claimed == nil || claimed.ID != j.ID || claimed.Status != JobRunning || claimed.Attempts != 1 {
		t.Fatalf("ClaimNextJob: %+v %v", claimed, err)
	}
	if next, _ := s.ClaimNextJob(ctx); next != nil {
		t.Fatal("second claim should be empty")
	}
	if err := s.FinishJob(ctx, j.ID, JobFailed, "", "boom"); err != nil {
		t.Fatal(err)
	}
	// Once finished, the same path enqueues a fresh row.
	again := &Job{ArrType: "radarr", ArrID: 7, Title: "Movie", Path: "/media/movies/m.mkv"}
	_ = s.EnqueueJob(ctx, again)
	if again.ID == j.ID {
		t.Fatal("finished job should not dedupe")
	}
	if err := s.RequeueJob(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountJobs(ctx, JobQueued); n != 2 {
		t.Fatalf("queued count=%d", n)
	}
	list, _ := s.ListJobs(ctx, 10)
	if len(list) != 2 || list[0].ID != again.ID {
		t.Fatalf("ListJobs: %+v", list)
	}
	c, _ := s.ClaimNextJob(ctx)
	if n, _ := s.ResetRunningJobs(ctx); n != 1 || c == nil {
		t.Fatalf("ResetRunningJobs n=%d", n)
	}
	_ = s.FinishJob(ctx, again.ID, JobPassed, `{"ok":true}`, "")
	if n, _ := s.PruneJobs(ctx, time.Now().Add(time.Minute)); n != 1 {
		t.Fatalf("PruneJobs n=%d", n)
	}
}

func TestSettings(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if !s.BoolSetting(ctx, SettingValidationEnabled) || !s.BoolSetting(ctx, SettingAutoBlocklist) {
		t.Fatal("defaults wrong")
	}
	if err := s.SetSetting(ctx, SettingValidationEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	if s.BoolSetting(ctx, SettingValidationEnabled) {
		t.Fatal("SetSetting not applied")
	}
	all, _ := s.AllSettings(ctx)
	if all[SettingValidationEnabled] != "false" || all[SettingAutoBlocklist] != "true" {
		t.Fatalf("AllSettings: %v", all)
	}
	_ = s.SetSetting(ctx, SettingAutoBlocklist, "garbage")
	if !s.BoolSetting(ctx, SettingAutoBlocklist) {
		t.Fatal("unparseable value should fall back to default")
	}
}

func TestOpenCreatesFile(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir + "/sub/pelicula.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var v int
	if err := s.DB().QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != schemaVersion {
		t.Fatalf("user_version=%d err=%v", v, err)
	}
	// Reopen is idempotent.
	s2, err := Open(dir + "/sub/pelicula.db")
	if err != nil {
		t.Fatal(err)
	}
	_ = s2.Close()
}

// A database from the multi-user rescope (schema 1) loses its roles, invites
// and requests tables and its sessions, and keeps jobs and settings.
func TestMigrateFromV1(t *testing.T) {
	path := t.TempDir() + "/pelicula.db"
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.DB().ExecContext(ctx, `
		DROP TABLE sessions;
		CREATE TABLE sessions (token TEXT PRIMARY KEY, username TEXT NOT NULL, role TEXT NOT NULL,
		  jellyfin_user_id TEXT NOT NULL DEFAULT '', jellyfin_token TEXT NOT NULL DEFAULT '',
		  expires_at TEXT NOT NULL, created_at TEXT NOT NULL);
		INSERT INTO sessions VALUES ('t', 'gwen', 'admin', '', '', '2999-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
		CREATE TABLE roles (username TEXT PRIMARY KEY, role TEXT NOT NULL, updated_at TEXT NOT NULL);
		CREATE TABLE invites (code TEXT PRIMARY KEY);
		CREATE TABLE requests (id INTEGER PRIMARY KEY);
		INSERT INTO settings VALUES ('auto_approve_requests', 'true'), ('validation_enabled', 'false');
		INSERT INTO jobs (arr_type, arr_id, title, path, status, created_at)
		  VALUES ('radarr', 1, 'Heat', '/media/movies/heat.mkv', 'passed', '2026-01-01T00:00:00Z');
		PRAGMA user_version = 1;`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s, err = Open(path)
	if err != nil {
		t.Fatalf("reopen v1 database: %v", err)
	}
	defer s.Close()
	for _, table := range []string{"roles", "invites", "requests"} {
		var n int
		if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&n); err != nil || n != 0 {
			t.Errorf("table %s still exists (n=%d err=%v)", table, n, err)
		}
	}
	if got, _ := s.GetSession(ctx, "t"); got != nil {
		t.Error("old session survived the migration")
	}
	if err := s.CreateSession(ctx, Session{Token: "n", Username: "gwen", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("insert into migrated sessions: %v", err)
	}
	all, _ := s.AllSettings(ctx)
	if _, ok := all["auto_approve_requests"]; ok || all[SettingValidationEnabled] != "false" {
		t.Errorf("settings after migration: %v", all)
	}
	if jobs, _ := s.ListJobs(ctx, 10); len(jobs) != 1 || jobs[0].Title != "Heat" {
		t.Errorf("jobs after migration: %+v", jobs)
	}
	var v int
	if err := s.DB().QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != schemaVersion {
		t.Fatalf("user_version=%d err=%v", v, err)
	}
}
