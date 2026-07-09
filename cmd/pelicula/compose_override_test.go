package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateLibraryPath is a table-driven regression test for CIT-6: a
// library Path is interpolated verbatim into a double-quoted YAML scalar
// (libraries.yml.tmpl: `"{{.Path}}:/media/{{.Slug}}"`), so any character
// that breaks or reinterprets a double-quoted YAML scalar must be rejected.
func TestValidateLibraryPath(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		wantErr bool
	}{
		// ── rejected: characters that corrupt or reinterpret the YAML scalar ──
		{
			name:    "embedded double quote ends the scalar early",
			path:    `/mnt/media"; rm -rf /: garbage`,
			wantErr: true,
		},
		{
			name:    "windows-style backslash path is a malformed YAML escape",
			path:    `C:\Users\media\Movies`,
			wantErr: true,
		},
		{
			name:    "backslash-n would silently become a real newline",
			path:    `/mnt/media\nMovies`,
			wantErr: true,
		},
		{
			name:    "literal newline",
			path:    "/mnt/media\nMovies",
			wantErr: true,
		},
		{
			name:    "literal carriage return",
			path:    "/mnt/media\rMovies",
			wantErr: true,
		},
		{
			name:    "literal tab",
			path:    "/mnt/media\tMovies",
			wantErr: true,
		},
		{
			name:    "NUL byte",
			path:    "/mnt/media\x00Movies",
			wantErr: true,
		},
		{
			name:    "DEL control character",
			path:    "/mnt/media\x7fMovies",
			wantErr: true,
		},

		// ── accepted: ordinary path punctuation, safe inside double quotes ──
		{
			name:    "normal absolute unix path",
			path:    "/mnt/media/movies",
			wantErr: false,
		},
		{
			name:    "path containing spaces",
			path:    "/mnt/My Media/Movies",
			wantErr: false,
		},
		{
			name:    "path with forward-slash windows drive style",
			path:    "C:/Users/media/Movies",
			wantErr: false,
		},
		{
			name:    "path with hyphens, underscores, and dots",
			path:    "/mnt/media-2_final.v2/Movies",
			wantErr: false,
		},
		{
			name:    "path with a colon (e.g. a labeled volume)",
			path:    "/mnt/media:extra",
			wantErr: false,
		},
		{
			name:    "path with unicode characters",
			path:    "/mnt/médîà/Filmés",
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateLibraryPath(tc.path)
			if tc.wantErr && err == nil {
				t.Errorf("validateLibraryPath(%q) = nil, want error", tc.path)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("validateLibraryPath(%q) = %v, want nil", tc.path, err)
			}
		})
	}
}

// TestGenerateLibrariesOverride_SkipsUnsafePathButKeepsGoodOnes verifies that
// a single library with an unsafe Path does not corrupt or abort generation
// of the override file for the other, valid libraries — it is skipped (with
// a warning identifying it), while safe libraries still get a mount entry.
func TestGenerateLibrariesOverride_SkipsUnsafePathButKeepsGoodOnes(t *testing.T) {
	dir := t.TempDir()
	configPeliculaDir := filepath.Join(dir, "pelicula")
	if err := os.MkdirAll(configPeliculaDir, 0755); err != nil {
		t.Fatal(err)
	}

	librariesJSON := `{
		"libraries": [
			{"name": "Movies", "slug": "movies", "path": "/mnt/media/movies", "type": "movies", "arr": "radarr", "processing": "full"},
			{"name": "Bad", "slug": "bad", "path": "/mnt/media\"; evil: yaml", "type": "movies", "arr": "radarr", "processing": "full"}
		]
	}`
	if err := os.WriteFile(filepath.Join(configPeliculaDir, "libraries.json"), []byte(librariesJSON), 0644); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(dir, "docker-compose.libraries.yml")
	if err := generateLibrariesOverride(configPeliculaDir, outputPath); err != nil {
		t.Fatalf("generateLibrariesOverride: %v", err)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("reading generated override: %v", err)
	}
	out := string(data)

	if !strings.Contains(out, `"/mnt/media/movies:/media/movies"`) {
		t.Errorf("expected safe library mount line in output, got:\n%s", out)
	}
	if strings.Contains(out, "evil: yaml") {
		t.Errorf("unsafe library path leaked into generated YAML:\n%s", out)
	}
	// The unsafe library's slug must not appear as a mount target either.
	if strings.Contains(out, "/media/bad") {
		t.Errorf("unsafe library was mounted despite invalid path:\n%s", out)
	}
}

// TestGenerateLibrariesOverride_AllUnsafeRemovesOverrideFile verifies that if
// every external library has an unsafe path, the override file is removed
// (mirrors the existing "no external libraries" behavior) rather than being
// left stale or written with zero libraries in a way that could confuse
// docker compose.
func TestGenerateLibrariesOverride_AllUnsafeRemovesOverrideFile(t *testing.T) {
	dir := t.TempDir()
	configPeliculaDir := filepath.Join(dir, "pelicula")
	if err := os.MkdirAll(configPeliculaDir, 0755); err != nil {
		t.Fatal(err)
	}

	librariesJSON := `{
		"libraries": [
			{"name": "Bad", "slug": "bad", "path": "/mnt/media\"; evil: yaml", "type": "movies", "arr": "radarr", "processing": "full"}
		]
	}`
	if err := os.WriteFile(filepath.Join(configPeliculaDir, "libraries.json"), []byte(librariesJSON), 0644); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(dir, "docker-compose.libraries.yml")
	// Pre-seed a stale override file to confirm it gets cleaned up.
	if err := os.WriteFile(outputPath, []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := generateLibrariesOverride(configPeliculaDir, outputPath); err != nil {
		t.Fatalf("generateLibrariesOverride: %v", err)
	}

	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Errorf("expected override file to be removed when all libraries are unsafe, err=%v", err)
	}
}
