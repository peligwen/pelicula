package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWalkUpForMarker covers the walkUpForMarker helper extracted from getScriptDir.
func TestWalkUpForMarker(t *testing.T) {
	// Build a small directory tree:
	//   root/
	//     marker/target          ← the marker we're looking for
	//     level1/
	//       level2/              ← walk starting point for the "found after N hops" case
	root := t.TempDir()
	markerDir := filepath.Join(root, "marker")
	if err := os.MkdirAll(markerDir, 0755); err != nil {
		t.Fatal(err)
	}
	markerFile := filepath.Join(markerDir, "target")
	if err := os.WriteFile(markerFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	level2 := filepath.Join(root, "level1", "level2")
	if err := os.MkdirAll(level2, 0755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		start  string
		marker string
		want   string // expected return value; "" means "same as start" (not-found fallback)
	}{
		{
			name:   "found at current directory",
			start:  root,
			marker: filepath.Join("marker", "target"),
			want:   root,
		},
		{
			name:   "found after walking up two levels",
			start:  level2,
			marker: filepath.Join("marker", "target"),
			want:   root,
		},
		{
			name:   "not found — returns start",
			start:  level2,
			marker: filepath.Join("nonexistent", "file"),
			want:   level2, // fallback to start
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := walkUpForMarker(tc.start, tc.marker)
			if got != tc.want {
				t.Errorf("walkUpForMarker(%q, %q) = %q, want %q", tc.start, tc.marker, got, tc.want)
			}
		})
	}
}

// TestGetScriptDir_ReturnsNonEmpty is a smoke test: the binary is built from
// the cmd/pelicula directory which lives under the repo root that contains
// compose/docker-compose.yml, so getScriptDir() must return a non-empty string.
func TestGetScriptDir_ReturnsNonEmpty(t *testing.T) {
	got := getScriptDir()
	if got == "" {
		t.Error("getScriptDir() returned empty string")
	}
}
