package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"pelicula-api/internal/peligrosa"
)

// exportTestFulfiller is a no-op Fulfiller for export tests.
type exportTestFulfiller struct{}

func (f *exportTestFulfiller) AddMovie(tmdbID, profileID int, rootPath string) (int, error) {
	return 0, nil
}
func (f *exportTestFulfiller) AddSeries(tvdbID, profileID int, rootPath string) (int, error) {
	return 0, nil
}

func TestResolveProfileID(t *testing.T) {
	t.Run("name found in map", func(t *testing.T) {
		m := map[string]int{"HD-1080p": 3, "Any": 1}
		got := resolveProfileID("HD-1080p", m)
		if got != 3 {
			t.Errorf("got %d, want 3", got)
		}
	})

	t.Run("name not found returns first available", func(t *testing.T) {
		m := map[string]int{"Any": 5}
		got := resolveProfileID("Missing", m)
		if got != 5 {
			t.Errorf("got %d, want 5", got)
		}
	})

	t.Run("empty map returns 1", func(t *testing.T) {
		got := resolveProfileID("anything", map[string]int{})
		if got != 1 {
			t.Errorf("got %d, want 1", got)
		}
	})
}

func TestResolveTagIDs(t *testing.T) {
	labelMap := map[string]int{
		"4k":    10,
		"hevc":  20,
		"anime": 30,
	}

	t.Run("all labels present", func(t *testing.T) {
		ids := resolveTagIDs([]string{"4k", "hevc"}, labelMap)
		if len(ids) != 2 {
			t.Fatalf("expected 2 ids, got %v", ids)
		}
		// Order mirrors input order
		if ids[0] != 10 || ids[1] != 20 {
			t.Errorf("ids = %v, want [10 20]", ids)
		}
	})

	t.Run("missing labels skipped", func(t *testing.T) {
		ids := resolveTagIDs([]string{"4k", "unknown"}, labelMap)
		if len(ids) != 1 || ids[0] != 10 {
			t.Errorf("ids = %v, want [10]", ids)
		}
	})

	t.Run("empty labels returns empty", func(t *testing.T) {
		ids := resolveTagIDs(nil, labelMap)
		if len(ids) != 0 {
			t.Errorf("expected empty, got %v", ids)
		}
	})
}

func TestResolveTagLabels(t *testing.T) {
	tagMap := map[int]string{
		10: "4k",
		20: "hevc",
	}

	t.Run("tags as float64 IDs resolved", func(t *testing.T) {
		m := map[string]any{
			"tags": []any{float64(10), float64(20)},
		}
		labels := resolveTagLabels(m, tagMap)
		if len(labels) != 2 || labels[0] != "4k" || labels[1] != "hevc" {
			t.Errorf("labels = %v, want [4k hevc]", labels)
		}
	})

	t.Run("unknown tag IDs skipped", func(t *testing.T) {
		m := map[string]any{
			"tags": []any{float64(10), float64(99)},
		}
		labels := resolveTagLabels(m, tagMap)
		if len(labels) != 1 || labels[0] != "4k" {
			t.Errorf("labels = %v, want [4k]", labels)
		}
	})

	t.Run("missing tags key returns empty", func(t *testing.T) {
		m := map[string]any{}
		labels := resolveTagLabels(m, tagMap)
		if len(labels) != 0 {
			t.Errorf("expected empty, got %v", labels)
		}
	})
}

func TestUniqueStrings(t *testing.T) {
	t.Run("duplicates removed, order preserved", func(t *testing.T) {
		got := uniqueStrings(func(add func(string)) {
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
		got := uniqueStrings(func(add func(string)) {})
		if len(got) != 0 {
			t.Errorf("expected empty, got %v", got)
		}
	})
}

func TestExtractSeasons(t *testing.T) {
	t.Run("seasons extracted", func(t *testing.T) {
		s := map[string]any{
			"seasons": []any{
				map[string]any{"seasonNumber": float64(0), "monitored": false},
				map[string]any{"seasonNumber": float64(1), "monitored": true},
				map[string]any{"seasonNumber": float64(2), "monitored": true},
			},
		}
		seasons := extractSeasons(s)
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
		seasons := extractSeasons(map[string]any{})
		if len(seasons) != 0 {
			t.Errorf("expected empty, got %v", seasons)
		}
	})
}

// ── Backup version tests ───────────────────────────────────────────────────

func TestBackupVersionBoundaries(t *testing.T) {
	// Helper that POSTs a backup JSON to handleImportBackup and returns the response code.
	post := func(t *testing.T, payload BackupExport) int {
		t.Helper()
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/pelicula/import-backup", bytes.NewReader(body))
		w := httptest.NewRecorder()

		// handleImportBackup calls services.Keys() — stub them.
		origServices := services
		services = &ServiceClients{}
		t.Cleanup(func() { services = origServices })

		handleImportBackup(w, req)
		return w.Code
	}

	t.Run("version 0 rejected", func(t *testing.T) {
		code := post(t, BackupExport{Version: 0})
		if code != http.StatusBadRequest {
			t.Errorf("got %d, want 400", code)
		}
	})

	t.Run("version 99 rejected", func(t *testing.T) {
		code := post(t, BackupExport{Version: 99})
		if code != http.StatusBadRequest {
			t.Errorf("got %d, want 400", code)
		}
	})

	t.Run("version 1 accepted (keys missing → 503)", func(t *testing.T) {
		// Version is valid; missing API keys yield 503, not 400.
		code := post(t, BackupExport{Version: 1})
		if code != http.StatusServiceUnavailable {
			t.Errorf("got %d, want 503", code)
		}
	})

	t.Run("version 2 accepted (keys missing → 503)", func(t *testing.T) {
		code := post(t, BackupExport{Version: 2})
		if code != http.StatusServiceUnavailable {
			t.Errorf("got %d, want 503", code)
		}
	})
}

// ── InviteStore.InsertFull tests ───────────────────────────────────────────

func TestInviteStoreInsertFull(t *testing.T) {
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

	if err := store.InsertFull(inv); err != nil {
		t.Fatalf("InsertFull: %v", err)
	}

	// Verify it's in the store.
	list := store.ListInvites()
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
		// Second insert of same token must not error.
		if err := store.InsertFull(inv); err != nil {
			t.Errorf("second InsertFull: %v", err)
		}
		if len(store.ListInvites()) != 1 {
			t.Error("expected still 1 invite after duplicate insert")
		}
	})
}

// ── RequestStore.InsertFull tests ─────────────────────────────────────────

func TestRequestStoreInsertFull(t *testing.T) {
	db := testDB(t)
	store := peligrosa.NewRequestStore(db, &exportTestFulfiller{})

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

	if err := store.InsertFull(req); err != nil {
		t.Fatalf("InsertFull: %v", err)
	}

	// Verify it's in the store.
	all := store.All()
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
		if err := store.InsertFull(req); err != nil {
			t.Errorf("second InsertFull: %v", err)
		}
		if len(store.All()) != 1 {
			t.Error("expected still 1 request after duplicate insert")
		}
	})
}

// ── v1 backup import compatibility test ───────────────────────────────────

func TestImportV1BackupHasNoRolesInvitesRequests(t *testing.T) {
	// A v1 backup JSON: only movies/series, no roles/invites/requests.
	v1 := `{"version":1,"exported":"2025-01-01T00:00:00Z","movies":[],"series":[]}`
	var backup BackupExport
	if err := json.Unmarshal([]byte(v1), &backup); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if backup.Version != 1 {
		t.Errorf("version = %d, want 1", backup.Version)
	}
	if len(backup.Roles) != 0 || len(backup.Invites) != 0 || len(backup.Requests) != 0 {
		t.Error("v1 backup should have no roles/invites/requests")
	}
}

// ── v2 backup struct roundtrip test ───────────────────────────────────────

func TestBackupExportV2Roundtrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	maxUses := 1
	export := BackupExport{
		Version:  2,
		Exported: now.Format(time.RFC3339),
		Movies:   []MovieExport{},
		Series:   []SeriesExport{},
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
	var got BackupExport
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

// ── resolveProfileID warning tests ────────────────────────────────────────

func TestResolveProfileIDWithWarning(t *testing.T) {
	t.Run("known profile returns exact match silently", func(t *testing.T) {
		m := map[string]int{"HD-1080p": 3, "Any": 1}
		got := resolveProfileID("HD-1080p", m)
		if got != 3 {
			t.Errorf("got %d, want 3", got)
		}
	})

	t.Run("unknown profile falls back to first available", func(t *testing.T) {
		m := map[string]int{"Any": 5}
		got := resolveProfileID("MissingProfile", m)
		if got != 5 {
			t.Errorf("got %d, want 5 (first available)", got)
		}
	})

	t.Run("empty map returns 1", func(t *testing.T) {
		got := resolveProfileID("anything", map[string]int{})
		if got != 1 {
			t.Errorf("got %d, want 1", got)
		}
	})
}
