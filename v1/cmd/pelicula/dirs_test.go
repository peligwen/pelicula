package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSetupDirs(t *testing.T) {
	root := t.TempDir()
	cfg, lib, work := filepath.Join(root, "cfg"), filepath.Join(root, "lib"), filepath.Join(root, "work")

	if err := setupDirs(cfg, lib, work); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, s := range []string{"sonarr", "radarr", "prowlarr", "qbittorrent", "jellyfin", "pelicula", "gluetun"} {
		want = append(want, filepath.Join(cfg, s))
	}
	want = append(want,
		filepath.Join(lib, "movies"), filepath.Join(lib, "tv"),
		filepath.Join(work, "downloads", "radarr"), filepath.Join(work, "downloads", "tv-sonarr"),
	)
	for _, d := range want {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			t.Errorf("%s not created (err %v)", d, err)
		}
	}

	// Existing content is preserved and a rerun is fine.
	marker := filepath.Join(lib, "movies", "keep.mkv")
	writeFileT(t, marker, "x")
	if err := setupDirs(cfg, lib, work); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !fileExists(marker) {
		t.Error("existing media removed")
	}

	// The old CLI's extra directories are gone.
	for _, gone := range []string{
		filepath.Join(cfg, "bazarr"), filepath.Join(cfg, "procula"),
		filepath.Join(work, "processing"), filepath.Join(work, "downloads", "incomplete"),
	} {
		if fileExists(gone) {
			t.Errorf("%s should not be created", gone)
		}
	}
}

func TestSetupDirsReportsFailingPath(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "file")
	writeFileT(t, blocker, "not a directory")

	err := setupDirs(filepath.Join(blocker, "cfg"), filepath.Join(root, "lib"), filepath.Join(root, "work"))
	var dce *dirCreateError
	if !errors.As(err, &dce) {
		t.Fatalf("err = %v, want *dirCreateError", err)
	}
	if filepath.Dir(dce.path) != filepath.Join(blocker, "cfg") {
		t.Errorf("failing path = %s", dce.path)
	}
}

func TestFirstExistingAncestor(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(existing, 0755); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		existing:                                 existing,
		filepath.Join(existing, "c"):             existing,
		filepath.Join(existing, "c", "d"):        existing,
		filepath.Join(root, "x", "y", "z"):       root,
		filepath.Join(root, "a", "..", "q", "r"): root,
	}
	for in, want := range cases {
		if got := firstExistingAncestor(in); got != want {
			t.Errorf("firstExistingAncestor(%q) = %q, want %q", in, got, want)
		}
	}
}
