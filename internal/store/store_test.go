package store

import (
	"context"
	"errors"
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

func TestRole(t *testing.T) {
	if !RoleAdmin.AtLeast(RoleManager) || RoleViewer.AtLeast(RoleManager) {
		t.Fatal("AtLeast ordering wrong")
	}
	if _, ok := ParseRole("root"); ok {
		t.Fatal("ParseRole accepted bogus role")
	}
}

func TestRolesAndSessions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if _, ok, err := s.GetRole(ctx, "gwen"); err != nil || ok {
		t.Fatalf("expected no role, got ok=%v err=%v", ok, err)
	}
	if err := s.SetRole(ctx, "gwen", RoleViewer); err != nil {
		t.Fatal(err)
	}
	sess := Session{Token: "tok", Username: "gwen", Role: RoleViewer, ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	// Changing the role updates the live session.
	if err := s.SetRole(ctx, "gwen", RoleAdmin); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ctx, "tok")
	if err != nil || got == nil {
		t.Fatalf("GetSession: %v %v", got, err)
	}
	if got.Role != RoleAdmin {
		t.Fatalf("session role not updated: %s", got.Role)
	}
	roles, _ := s.ListRoles(ctx)
	if roles["gwen"] != RoleAdmin {
		t.Fatalf("ListRoles: %v", roles)
	}

	// Expired sessions read as missing and are purged.
	_ = s.CreateSession(ctx, Session{Token: "old", Username: "gwen", Role: RoleAdmin, ExpiresAt: time.Now().Add(-time.Minute)})
	if got, _ := s.GetSession(ctx, "old"); got != nil {
		t.Fatal("expired session returned")
	}
	if got, _ := s.GetSession(ctx, "missing"); got != nil {
		t.Fatal("missing session returned")
	}

	if err := s.DeleteRole(ctx, "gwen"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSession(ctx, "tok"); got != nil {
		t.Fatal("DeleteRole should drop sessions")
	}
}

func TestInvites(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	inv := Invite{Code: "abc", Role: RoleViewer, CreatedBy: "admin", ExpiresAt: time.Now().Add(time.Hour)}
	if err := s.CreateInvite(ctx, inv); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetInvite(ctx, "abc")
	if err != nil || got == nil || got.Used() || got.Expired(time.Now()) {
		t.Fatalf("GetInvite: %+v %v", got, err)
	}
	if err := s.UseInvite(ctx, "abc", "newbie"); err != nil {
		t.Fatal(err)
	}
	if err := s.UseInvite(ctx, "abc", "again"); !errors.Is(err, ErrInviteUnavailable) {
		t.Fatalf("second redemption should fail, got %v", err)
	}
	got, _ = s.GetInvite(ctx, "abc")
	if !got.Used() || got.UsedBy != "newbie" {
		t.Fatalf("invite not marked used: %+v", got)
	}
	// Expired invites cannot be redeemed.
	_ = s.CreateInvite(ctx, Invite{Code: "exp", Role: RoleViewer, CreatedBy: "admin", ExpiresAt: time.Now().Add(-time.Second)})
	if err := s.UseInvite(ctx, "exp", "x"); !errors.Is(err, ErrInviteUnavailable) {
		t.Fatalf("expired redemption should fail, got %v", err)
	}
	list, _ := s.ListInvites(ctx)
	if len(list) != 2 {
		t.Fatalf("ListInvites len=%d", len(list))
	}
	if err := s.DeleteInvite(ctx, "exp"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteInvite(ctx, "exp"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete: %v", err)
	}
}

func TestRequests(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r := &Request{MediaType: "movie", TmdbID: 603, Title: "The Matrix", Year: 1999, RequestedBy: "gwen"}
	if err := s.CreateRequest(ctx, r); err != nil {
		t.Fatal(err)
	}
	if r.ID == 0 || r.Status != RequestPending {
		t.Fatalf("CreateRequest: %+v", r)
	}
	dup, _ := s.FindOpenRequest(ctx, "movie", 603, 0)
	if dup == nil || dup.ID != r.ID {
		t.Fatalf("FindOpenRequest miss: %+v", dup)
	}
	if err := s.UpdateRequestStatus(ctx, r.ID, RequestApproved, "admin", "", 42); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetRequest(ctx, r.ID)
	if got.Status != RequestApproved || got.ArrID != 42 || got.DecidedBy != "admin" {
		t.Fatalf("after approve: %+v", got)
	}
	n, err := s.MarkRequestsAvailable(ctx, "radarr", 42)
	if err != nil || n != 1 {
		t.Fatalf("MarkRequestsAvailable n=%d err=%v", n, err)
	}
	if dup, _ := s.FindOpenRequest(ctx, "movie", 603, 0); dup != nil {
		t.Fatal("available request should not count as open")
	}
	mine, _ := s.ListRequests(ctx, "gwen")
	all, _ := s.ListRequests(ctx, "")
	none, _ := s.ListRequests(ctx, "someone")
	if len(mine) != 1 || len(all) != 1 || len(none) != 0 {
		t.Fatalf("ListRequests mine=%d all=%d none=%d", len(mine), len(all), len(none))
	}
	if _, err := s.GetRequest(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRequest missing: %v", err)
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
	if !s.BoolSetting(ctx, SettingValidationEnabled) || s.BoolSetting(ctx, SettingAutoApprove) {
		t.Fatal("defaults wrong")
	}
	if err := s.SetSetting(ctx, SettingAutoApprove, "true"); err != nil {
		t.Fatal(err)
	}
	if !s.BoolSetting(ctx, SettingAutoApprove) {
		t.Fatal("SetSetting not applied")
	}
	all, _ := s.AllSettings(ctx)
	if all[SettingAutoApprove] != "true" || all[SettingAutoBlocklist] != "true" {
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
