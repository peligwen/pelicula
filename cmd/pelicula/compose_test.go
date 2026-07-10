package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuildArgsIncludesProjectName(t *testing.T) {
	c := NewCompose("/tmp/pelicula", "", false, false, "pelicula")
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
	c := NewCompose("/tmp/pelicula", "", false, false, "my-media-stack")
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
	c := NewCompose("/tmp/pelicula", "", false, false, "")
	if c.projectName != "pelicula" {
		t.Errorf("expected default projectName %q, got %q", "pelicula", c.projectName)
	}
}

// TestNewComposeDefaultsEnvFileWhenEmpty verifies that passing "" for envFile
// (what ctx.newCompose() would never do in practice, since ctx.EnvFile is
// always populated, but what direct NewCompose callers — including these
// tests — may do) reproduces the pre-override default: scriptDir/.env.
func TestNewComposeDefaultsEnvFileWhenEmpty(t *testing.T) {
	c := NewCompose("/tmp/pelicula", "", false, false, "pelicula")
	want := filepath.Join("/tmp/pelicula", ".env")
	if c.envFile != want {
		t.Errorf("envFile = %q, want default %q", c.envFile, want)
	}
}

// TestNewComposeUsesGivenEnvFile is the CIT-1 regression test: Compose must
// use exactly the envFile it is given rather than re-deriving scriptDir/.env
// itself. Before this change, NewCompose always derived --env-file from
// scriptDir internally, so a PELICULA_ENV_FILE override on ctx.EnvFile never
// reached the actual `docker compose` invocation.
func TestNewComposeUsesGivenEnvFile(t *testing.T) {
	const custom = "/custom/test-instance/.env"
	c := NewCompose("/tmp/pelicula", custom, false, false, "pelicula")
	if c.envFile != custom {
		t.Fatalf("envFile = %q, want override %q", c.envFile, custom)
	}

	args := c.buildArgs("up", "-d")
	idx := slices.Index(args, "--env-file")
	if idx == -1 || idx+1 >= len(args) {
		t.Fatal("buildArgs did not include --env-file")
	}
	if args[idx+1] != custom {
		t.Errorf("--env-file value = %q, want override path %q (not a scriptDir-derived path)", args[idx+1], custom)
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

	c := NewCompose(dir, "", false, false, "pelicula")
	args := c.buildArgs("up", "-d")

	if hasArg(args, remoteYML) {
		t.Errorf("docker-compose.remote.yml must not appear in buildArgs after remote-vhost cut; args=%v", args)
	}
}

// TestBuildArgsComposeOverlay covers the PELICULA_COMPOSE_OVERLAY seam end to
// end: unset it never appears; set, it is appended after
// docker-compose.libraries.yml and before any --profile flags, so it wins
// merges against everything before it (including the libraries override) but
// profile activation is unaffected.
func TestBuildArgsComposeOverlay(t *testing.T) {
	dir := t.TempDir()
	composeDir := filepath.Join(dir, "compose")
	if err := os.MkdirAll(composeDir, 0755); err != nil {
		t.Fatal(err)
	}
	librariesYML := filepath.Join(composeDir, "docker-compose.libraries.yml")
	if err := os.WriteFile(librariesYML, []byte("# stub"), 0644); err != nil {
		t.Fatal(err)
	}

	overlayPath := filepath.Join(dir, "docker-compose.test.yml")
	if err := os.WriteFile(overlayPath, []byte("# stub"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Run("unset — overlay absent from buildArgs", func(t *testing.T) {
		t.Setenv("PELICULA_COMPOSE_OVERLAY", "")
		c := NewCompose(dir, "", false, false, "pelicula")
		args := c.buildArgs("up", "-d")
		if hasArg(args, overlayPath) {
			t.Errorf("overlay path leaked into buildArgs while PELICULA_COMPOSE_OVERLAY is unset: %v", args)
		}
	})

	t.Run("set — appended after libraries.yml and before --profile", func(t *testing.T) {
		t.Setenv("PELICULA_COMPOSE_OVERLAY", overlayPath)
		c := NewCompose(dir, "", false, false, "pelicula")
		c.profiles = []string{"vpn", "apprise"}
		args := c.buildArgs("up", "-d")

		libIdx := slices.Index(args, librariesYML)
		overlayIdx := slices.Index(args, overlayPath)
		profileIdx := slices.Index(args, "--profile")

		if libIdx == -1 {
			t.Fatalf("docker-compose.libraries.yml missing from buildArgs: %v", args)
		}
		if overlayIdx == -1 || args[overlayIdx-1] != "-f" {
			t.Fatalf("expected \"-f %s\" in buildArgs, got: %v", overlayPath, args)
		}
		if profileIdx == -1 {
			t.Fatalf("--profile missing from buildArgs: %v", args)
		}
		if !(libIdx < overlayIdx && overlayIdx < profileIdx) {
			t.Errorf("expected order libraries.yml(%d) < overlay(%d) < --profile(%d); args=%v",
				libIdx, overlayIdx, profileIdx, args)
		}
	})
}

// TestValidateComposeOverlay covers the fatal-fast validation path: unset is
// a no-op, an existing file validates cleanly, and a missing file produces
// an actionable error naming both the offending variable and the path.
func TestValidateComposeOverlay(t *testing.T) {
	if err := validateComposeOverlay(""); err != nil {
		t.Errorf("empty path (override unset) should validate as nil, got %v", err)
	}

	dir := t.TempDir()
	existing := filepath.Join(dir, "docker-compose.test.yml")
	if err := os.WriteFile(existing, []byte("# stub"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := validateComposeOverlay(existing); err != nil {
		t.Errorf("existing overlay file should validate cleanly, got %v", err)
	}

	missing := filepath.Join(dir, "does-not-exist.yml")
	err := validateComposeOverlay(missing)
	if err == nil {
		t.Fatal("expected an error for a missing overlay file, got nil")
	}
	if !strings.Contains(err.Error(), "PELICULA_COMPOSE_OVERLAY") {
		t.Errorf("error should name PELICULA_COMPOSE_OVERLAY for actionability, got: %v", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error should include the offending path, got: %v", err)
	}
}
