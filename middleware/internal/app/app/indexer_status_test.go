package app

import (
	"encoding/json"
	"testing"
	"time"
)

// mustParseList unmarshals a JSON array literal the way the arr client does,
// so fixtures carry the same dynamic types (float64 ids, string timestamps)
// as live Prowlarr responses.
func mustParseList(t *testing.T, raw string) []map[string]any {
	t.Helper()
	var out []map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	return out
}

func TestMergePausedIndexers(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

	indexers := mustParseList(t, `[
		{"id": 5, "name": "Nyaa.si", "enable": true},
		{"id": 2, "name": "The Pirate Bay", "enable": true},
		{"id": 3, "name": "Internet Archive", "enable": false}
	]`)

	// Real /api/v1/indexerstatus shape: records persist after recovery
	// (expired disabledTill), may have null disabledTill, and may reference
	// an indexer that has since been deleted.
	statuses := mustParseList(t, `[
		{"id": 1, "indexerId": 5, "disabledTill": "2026-07-16T12:46:46Z"},
		{"id": 2, "indexerId": 2, "disabledTill": "2026-07-16T11:00:00Z"},
		{"id": 3, "indexerId": 3, "disabledTill": null},
		{"id": 4, "indexerId": 9, "disabledTill": "2026-07-16T13:00:00.123Z"}
	]`)

	got := mergePausedIndexers(indexers, statuses, now)

	if len(got) != 2 {
		t.Fatalf("expected 2 paused indexers, got %d: %+v", len(got), got)
	}
	// Sorted by name: "Nyaa.si" < "indexer 9".
	if got[0].Name != "Nyaa.si" || got[0].ID != 5 {
		t.Errorf("got[0] = %+v, want Nyaa.si/5", got[0])
	}
	if want := time.Date(2026, 7, 16, 12, 46, 46, 0, time.UTC); !got[0].DisabledTill.Equal(want) {
		t.Errorf("got[0].DisabledTill = %v, want %v", got[0].DisabledTill, want)
	}
	// Deleted indexer falls back to a synthetic name; fractional-second
	// timestamps must parse.
	if got[1].Name != "indexer 9" || got[1].ID != 9 {
		t.Errorf("got[1] = %+v, want indexer 9/9", got[1])
	}
}

func TestMergePausedIndexers_EmptyStatuses(t *testing.T) {
	indexers := mustParseList(t, `[{"id": 1, "name": "TPB"}]`)
	got := mergePausedIndexers(indexers, nil, time.Now())
	if got == nil {
		t.Fatal("expected non-nil empty slice so the field serializes as [], got nil")
	}
	if len(got) != 0 {
		t.Fatalf("expected no paused indexers, got %+v", got)
	}
}

func TestMergePausedIndexers_BadTimestampSkipped(t *testing.T) {
	statuses := mustParseList(t, `[{"indexerId": 1, "disabledTill": "not-a-time"}]`)
	if got := mergePausedIndexers(nil, statuses, time.Now()); len(got) != 0 {
		t.Fatalf("unparseable disabledTill must be skipped, got %+v", got)
	}
}

func TestActivePauses_FiltersExpiredWithoutMutating(t *testing.T) {
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	cached := []PausedIndexer{
		{ID: 1, Name: "A", DisabledTill: now.Add(-time.Minute)},
		{ID: 2, Name: "B", DisabledTill: now.Add(time.Minute)},
	}

	got := activePauses(cached, now)
	if len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("expected only the open window, got %+v", got)
	}
	if len(cached) != 2 {
		t.Fatalf("cached slice was mutated: %+v", cached)
	}

	if got := activePauses(nil, now); got == nil {
		t.Fatal("expected non-nil empty slice for nil input")
	}
}
