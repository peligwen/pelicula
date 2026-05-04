package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestBuildArgsIncludesProjectName(t *testing.T) {
	c := NewCompose("/tmp/pelicula", false, false, "pelicula")
	args := c.buildArgs("up", "-d")

	// --project-name and value must appear in args
	idx := slices.Index(args, "--project-name")
	if idx == -1 {
		t.Fatal("buildArgs did not include --project-name")
	}
	if idx+1 >= len(args) || args[idx+1] != "pelicula" {
		t.Errorf("expected --project-name pelicula, got args[%d+1]=%q", idx, args[idx+1])
	}
}

func TestBuildArgsCustomProjectName(t *testing.T) {
	c := NewCompose("/tmp/pelicula", false, false, "my-media-stack")
	args := c.buildArgs("ps")

	idx := slices.Index(args, "--project-name")
	if idx == -1 {
		t.Fatal("buildArgs did not include --project-name")
	}
	if idx+1 >= len(args) || args[idx+1] != "my-media-stack" {
		t.Errorf("expected --project-name my-media-stack, got %q", args[idx+1])
	}
}

func TestNewComposeDefaultsProjectName(t *testing.T) {
	c := NewCompose("/tmp/pelicula", false, false, "")
	if c.projectName != "pelicula" {
		t.Errorf("expected default projectName %q, got %q", "pelicula", c.projectName)
	}
}

// hasArg returns true if args contains the given string.
func hasArg(args []string, s string) bool {
	return slices.Contains(args, s)
}

// TestBuildArgsNoRemoteOverlay verifies that docker-compose.remote.yml is never
// included in buildArgs after the remote-vhost layer was retired.
func TestBuildArgsNoRemoteOverlay(t *testing.T) {
	dir := t.TempDir()

	composeDir := filepath.Join(dir, "compose")
	if err := os.MkdirAll(composeDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Even if the file exists (leftover from an old install), it must not be
	// included — the migration removes it, but buildArgs should not re-add it.
	remoteYML := filepath.Join(composeDir, "docker-compose.remote.yml")
	if err := os.WriteFile(remoteYML, []byte("# stub"), 0644); err != nil {
		t.Fatal(err)
	}

	c := NewCompose(dir, false, false, "pelicula")
	args := c.buildArgs("up", "-d")

	if hasArg(args, remoteYML) {
		t.Errorf("docker-compose.remote.yml must not appear in buildArgs after remote-vhost cut; args=%v", args)
	}
}
