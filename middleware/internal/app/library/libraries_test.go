package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetRegistry wipes the global libraryRegistry so each test starts clean.
func resetRegistry() {
	libraryRegistryMu.Lock()
	libraryRegistry = LibraryConfig{}
	libraryRegistryMu.Unlock()
}

// TestLoadLibraries_FileAbsent checks that when libraries.json does not exist
// LoadLibraries returns the default config and writes it to disk.
func TestLoadLibraries_FileAbsent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	resetRegistry()

	cfg, err := LoadLibraries(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	def := DefaultLibraryConfig()
	if len(cfg.Libraries) != len(def.Libraries) {
		t.Fatalf("got %d libraries, want %d", len(cfg.Libraries), len(def.Libraries))
	}
	for i, want := range def.Libraries {
		got := cfg.Libraries[i]
		if got.Slug != want.Slug || got.Name != want.Name {
			t.Errorf("library[%d]: got {%s %s}, want {%s %s}", i, got.Slug, got.Name, want.Slug, want.Name)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "libraries.json")); err != nil {
		t.Errorf("expected libraries.json to be written, stat error: %v", err)
	}
}

// TestLoadLibraries_FilePresent checks that an existing libraries.json is
// parsed correctly.
func TestLoadLibraries_FilePresent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	resetRegistry()

	raw := `{"libraries":[{"name":"Docs","slug":"docs","type":"other","arr":"none","processing":"off"}]}`
	if err := os.WriteFile(filepath.Join(dir, "libraries.json"), []byte(raw), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	cfg, err := LoadLibraries(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Libraries) != 1 {
		t.Fatalf("got %d libraries, want 1", len(cfg.Libraries))
	}
	if cfg.Libraries[0].Slug != "docs" {
		t.Errorf("got slug %q, want %q", cfg.Libraries[0].Slug, "docs")
	}
}

// TestContainerPath checks the ContainerPath helper.
func TestContainerPath(t *testing.T) {
	t.Parallel()
	lib := Library{Slug: "movies"}
	want := "/media/movies"
	if got := lib.ContainerPath(); got != want {
		t.Errorf("ContainerPath() = %q, want %q", got, want)
	}
}

// TestSaveLibrary_Upsert checks that SaveLibrary adds a new library then
// updates it on a second call with the same slug.
func TestSaveLibrary_Upsert(t *testing.T) {
	dir := t.TempDir()
	resetRegistry()

	cfg, err := LoadLibraries(dir)
	if err != nil {
		t.Fatalf("LoadLibraries: %v", err)
	}
	libraryRegistryMu.Lock()
	libraryRegistry = cfg
	libraryRegistryMu.Unlock()

	custom := Library{Name: "Anime", Slug: "anime", Type: "tvshows", Arr: "sonarr", Processing: "full"}

	if err := SaveLibrary(dir, custom); err != nil {
		t.Fatalf("SaveLibrary add: %v", err)
	}
	libs := GetLibraries()
	found := false
	for _, l := range libs {
		if l.Slug == "anime" {
			found = true
		}
	}
	if !found {
		t.Fatal("anime library not found after add")
	}

	custom.Name = "Anime (Updated)"
	if err := SaveLibrary(dir, custom); err != nil {
		t.Fatalf("SaveLibrary update: %v", err)
	}
	updated, err := GetLibraryBySlug("anime")
	if err != nil {
		t.Fatalf("GetLibraryBySlug: %v", err)
	}
	if updated.Name != "Anime (Updated)" {
		t.Errorf("got name %q, want %q", updated.Name, "Anime (Updated)")
	}

	count := 0
	for _, l := range GetLibraries() {
		if l.Slug == "anime" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 anime entry, got %d", count)
	}
}

// TestSaveLibrary_Validation checks that invalid Library values are rejected.
func TestSaveLibrary_Validation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	resetRegistry()

	cases := []struct {
		name string
		lib  Library
	}{
		{
			name: "empty slug",
			lib:  Library{Slug: "", Type: "movies", Arr: "radarr"},
		},
		{
			name: "slug with leading dash",
			lib:  Library{Slug: "-bad", Type: "movies", Arr: "radarr"},
		},
		{
			name: "slug with uppercase",
			lib:  Library{Slug: "Bad", Type: "movies", Arr: "radarr"},
		},
		{
			name: "invalid type",
			lib:  Library{Slug: "ok", Type: "documents", Arr: "radarr"},
		},
		{
			name: "invalid arr",
			lib:  Library{Slug: "ok", Type: "movies", Arr: "lidarr"},
		},
		{
			name: "invalid processing",
			lib:  Library{Slug: "ok", Type: "movies", Arr: "radarr", Processing: "banana"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := SaveLibrary(dir, tc.lib); err == nil {
				t.Errorf("expected error for %q, got nil", tc.name)
			}
		})
	}
}

// TestDeleteLibrary_BuiltIn checks that built-in libraries cannot be deleted.
func TestDeleteLibrary_BuiltIn(t *testing.T) {
	dir := t.TempDir()
	resetRegistry()

	cfg, err := LoadLibraries(dir)
	if err != nil {
		t.Fatalf("LoadLibraries: %v", err)
	}
	libraryRegistryMu.Lock()
	libraryRegistry = cfg
	libraryRegistryMu.Unlock()

	if err := DeleteLibrary(dir, "movies"); err == nil {
		t.Error("expected error deleting built-in library, got nil")
	}
}

// TestFirstLibraryPath checks the FirstLibraryPath helper.
func TestFirstLibraryPath(t *testing.T) {
	resetRegistry()
	libraryRegistryMu.Lock()
	libraryRegistry = LibraryConfig{
		Libraries: []Library{
			{Name: "Movies", Slug: "movies", Type: "movies", Arr: "radarr", Processing: "full", BuiltIn: true},
			{Name: "TV Shows", Slug: "tv", Type: "tvshows", Arr: "sonarr", Processing: "full", BuiltIn: true},
		},
	}
	libraryRegistryMu.Unlock()

	t.Run("match radarr", func(t *testing.T) {
		got := FirstLibraryPath("radarr", "/media/movies")
		if got != "/media/movies" {
			t.Errorf("got %q, want %q", got, "/media/movies")
		}
	})

	t.Run("match sonarr", func(t *testing.T) {
		got := FirstLibraryPath("sonarr", "/media/tv")
		if got != "/media/tv" {
			t.Errorf("got %q, want %q", got, "/media/tv")
		}
	})

	t.Run("no match returns default", func(t *testing.T) {
		got := FirstLibraryPath("lidarr", "/media/music")
		if got != "/media/music" {
			t.Errorf("got %q, want %q", got, "/media/music")
		}
	})

	t.Run("custom slug returned", func(t *testing.T) {
		resetRegistry()
		libraryRegistryMu.Lock()
		libraryRegistry = LibraryConfig{
			Libraries: []Library{
				{Name: "Films", Slug: "films", Type: "movies", Arr: "radarr", Processing: "full"},
			},
		}
		libraryRegistryMu.Unlock()
		got := FirstLibraryPath("radarr", "/media/movies")
		if got != "/media/films" {
			t.Errorf("got %q, want %q", got, "/media/films")
		}
	})
}

// TestCheckLibraryAccessPaths verifies that CheckLibraryAccessPaths warns on
// directories with no world-execute bit and passes on accessible directories.
func TestCheckLibraryAccessPaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	accessible := filepath.Join(dir, "accessible")
	inaccessible := filepath.Join(dir, "inaccessible")

	if err := os.Mkdir(accessible, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(inaccessible, 0000); err != nil {
		t.Fatal(err)
	}

	warns := CheckLibraryAccessPaths([]string{accessible, inaccessible})
	if len(warns) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(warns), warns)
	}
	if !strings.Contains(warns[0], inaccessible) {
		t.Errorf("warning %q should mention path %s", warns[0], inaccessible)
	}
}

// TestCheckLibraryAccessPaths_Missing verifies a warning is returned for a
// path that does not exist.
func TestCheckLibraryAccessPaths_Missing(t *testing.T) {
	t.Parallel()
	warns := CheckLibraryAccessPaths([]string{"/nonexistent/path/xyz123"})
	if len(warns) != 1 {
		t.Fatalf("expected 1 warning for missing path, got %d: %v", len(warns), warns)
	}
}

// TestDeleteLibrary_Custom checks that a custom library can be deleted.
func TestDeleteLibrary_Custom(t *testing.T) {
	dir := t.TempDir()
	resetRegistry()

	cfg, err := LoadLibraries(dir)
	if err != nil {
		t.Fatalf("LoadLibraries: %v", err)
	}
	libraryRegistryMu.Lock()
	libraryRegistry = cfg
	libraryRegistryMu.Unlock()

	custom := Library{Name: "Extras", Slug: "extras", Type: "other", Arr: "none", Processing: "off"}
	if err := SaveLibrary(dir, custom); err != nil {
		t.Fatalf("SaveLibrary: %v", err)
	}

	if err := DeleteLibrary(dir, "extras"); err != nil {
		t.Fatalf("DeleteLibrary: %v", err)
	}

	if _, err := GetLibraryBySlug("extras"); err == nil {
		t.Error("expected extras library to be gone, but found it")
	}

	resetRegistry()
	fileCfg, err := LoadLibraries(dir)
	if err != nil {
		t.Fatalf("LoadLibraries after delete: %v", err)
	}
	for _, l := range fileCfg.Libraries {
		if l.Slug == "extras" {
			t.Error("extras library still present in libraries.json after delete")
		}
	}
}
