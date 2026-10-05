package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestWalkUpForMarker(t *testing.T) {
	root := t.TempDir()
	writeFileT(t, filepath.Join(root, "compose", "docker-compose.yml"), "services: {}\n")
	deep := filepath.Join(root, "bin", "nested")
	if err := os.MkdirAll(deep, 0755); err != nil {
		t.Fatal(err)
	}

	if got := walkUpForMarker(deep, composeMarker); got != root {
		t.Errorf("from bin/nested: %q, want %q", got, root)
	}
	if got := walkUpForMarker(root, composeMarker); got != root {
		t.Errorf("from the root: %q, want %q", got, root)
	}

	// Not found anywhere: the start directory comes back unchanged.
	other := t.TempDir()
	if got := walkUpForMarker(other, filepath.Join("no", "such", "marker")); got != other {
		t.Errorf("no marker: %q, want %q", got, other)
	}
}

func TestGenerateSecret(t *testing.T) {
	alnum := regexp.MustCompile(`^[A-Za-z0-9]+$`)
	seen := map[string]bool{}
	for _, n := range []int{1, 16, 32, 64} {
		s := generateSecret(n)
		if len(s) != n || !alnum.MatchString(s) {
			t.Errorf("generateSecret(%d) = %q", n, s)
		}
	}
	for i := 0; i < 50; i++ {
		s := generateSecret(32)
		if seen[s] {
			t.Fatalf("duplicate secret %q", s)
		}
		seen[s] = true
	}
}

func TestGitDescribeFallsBackToVersion(t *testing.T) {
	// A directory that is not a git repository (or no git binary at all).
	orig := version
	defer func() { version = orig }()
	version = "v0.0.0-test"
	dir := t.TempDir()
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(dir)) // never find an enclosing repo
	if got := gitDescribe(dir); got != "v0.0.0-test" {
		t.Errorf("gitDescribe outside a repo = %q, want the baked-in version", got)
	}
}

func TestRequireEnvAndFileExists(t *testing.T) {
	dir := t.TempDir()
	if fileExists(filepath.Join(dir, ".env")) {
		t.Error("fileExists on a missing file")
	}
	writeFileT(t, filepath.Join(dir, ".env"), "A=1\n")
	if !fileExists(filepath.Join(dir, ".env")) {
		t.Error("fileExists on an existing file")
	}
	env := loadEnvOrFatal(filepath.Join(dir, ".env"))
	if env["A"] != "1" {
		t.Errorf("env = %v", env)
	}
}
