package main

import (
	"testing"
)

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
