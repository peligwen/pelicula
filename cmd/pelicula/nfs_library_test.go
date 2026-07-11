package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// findComposeFile returns the index within args of the -f value whose
// basename matches name, or -1.
func findComposeFile(args []string, name string) int {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-f" && filepath.Base(args[i+1]) == name {
			return i + 1
		}
	}
	return -1
}

// TestBuildArgsLibraryOverlayDefault pins the default library source: every
// compose invocation must include docker-compose.local-library.yml (the base
// file mounts no /media itself) and must NOT include the NFS overlay.
func TestBuildArgsLibraryOverlayDefault(t *testing.T) {
	c := NewCompose(t.TempDir(), "", false, false, "")
	args := c.buildArgs("up", "-d")

	if findComposeFile(args, "docker-compose.local-library.yml") == -1 {
		t.Errorf("local-library overlay missing from default buildArgs; args=%v", args)
	}
	if findComposeFile(args, "docker-compose.nfs.yml") != -1 {
		t.Errorf("nfs overlay must not appear without nfsLibrary; args=%v", args)
	}
}

// TestBuildArgsLibraryOverlayNFS pins the NFS selection: with nfsLibrary set
// the invocation swaps in docker-compose.nfs.yml and drops local-library.
// Exactly one of the pair must ever be present.
func TestBuildArgsLibraryOverlayNFS(t *testing.T) {
	c := NewCompose(t.TempDir(), "", false, false, "")
	c.nfsLibrary = true
	args := c.buildArgs("up", "-d")

	if findComposeFile(args, "docker-compose.nfs.yml") == -1 {
		t.Errorf("nfs overlay missing with nfsLibrary=true; args=%v", args)
	}
	if findComposeFile(args, "docker-compose.local-library.yml") != -1 {
		t.Errorf("local-library overlay must not appear with nfsLibrary=true; args=%v", args)
	}
}

// TestBuildArgsLibraryOverlayOrdering pins the merge precedence: the library
// overlay must come directly after the base file and before the
// PELICULA_COMPOSE_OVERLAY test seam, so test overlays keep winning merges.
func TestBuildArgsLibraryOverlayOrdering(t *testing.T) {
	dir := t.TempDir()
	overlay := filepath.Join(dir, "stub-overlay.yml")
	if err := os.WriteFile(overlay, []byte("services: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PELICULA_COMPOSE_OVERLAY", overlay)

	c := NewCompose(dir, "", false, false, "")
	args := c.buildArgs("up", "-d")

	base := findComposeFile(args, "docker-compose.yml")
	lib := findComposeFile(args, "docker-compose.local-library.yml")
	seam := findComposeFile(args, "stub-overlay.yml")
	if base == -1 || lib == -1 || seam == -1 {
		t.Fatalf("expected base, library overlay, and seam overlay all present; args=%v", args)
	}
	if !(base < lib && lib < seam) {
		t.Errorf("want base < library overlay < PELICULA_COMPOSE_OVERLAY; got positions %d, %d, %d in args=%v",
			base, lib, seam, args)
	}
}

// TestIsNFSLibrary pins the strict-"true" parsing convention shared with the
// stack's other boolean env vars.
func TestIsNFSLibrary(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{
		{"true", true},
		{"", false},
		{"false", false},
		{"TRUE", false},
		{"1", false},
	} {
		if got := isNFSLibrary(EnvMap{"LIBRARY_NFS": tc.val}); got != tc.want {
			t.Errorf("isNFSLibrary(LIBRARY_NFS=%q) = %v, want %v", tc.val, got, tc.want)
		}
	}
	if isNFSLibrary(EnvMap{}) {
		t.Error("isNFSLibrary must be false when LIBRARY_NFS is absent")
	}
}

// TestNewComposeReadsLibraryNFSFromEnv verifies ctx.newCompose threads the
// .env selection into the Compose — including the nil-Env (pre-setup) path,
// which must default to the local-library overlay.
func TestNewComposeReadsLibraryNFSFromEnv(t *testing.T) {
	ctx := &Context{ScriptDir: t.TempDir(), Env: EnvMap{"LIBRARY_NFS": "true"}}
	if c := ctx.newCompose(); !c.nfsLibrary {
		t.Error("newCompose did not pick up LIBRARY_NFS=true from ctx.Env")
	}

	ctx.Env = nil
	if c := ctx.newCompose(); c.nfsLibrary {
		t.Error("newCompose must default to local-library when ctx.Env is nil")
	}
}

// TestSetupDirsSkipsLibraryWhenNFS verifies the NFS-mode contract: with
// libraryDir == "" no slug directories are created (the export owns the
// layout), while config and work trees are still fully built.
func TestSetupDirsSkipsLibraryWhenNFS(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	workDir := filepath.Join(root, "work")

	if err := setupDirs(configDir, "", workDir, defaultLibraries().Libraries); err != nil {
		t.Fatalf("setupDirs: %v", err)
	}

	for _, want := range []string{
		filepath.Join(configDir, "pelicula"),
		filepath.Join(workDir, "downloads"),
		filepath.Join(workDir, "processing"),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("expected dir %s to exist: %v", want, err)
		}
	}

	// No slug dirs anywhere: the only entries under root must be config/ and work/.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	if want := []string{"config", "work"}; !slices.Equal(names, want) {
		t.Errorf("unexpected dirs created under root: got %v, want %v", names, want)
	}
}

// TestWriteEnvFilePreservesExtra verifies the hard-reset path keeps the NFS
// quartet: extra pairs land in the fresh .env, empty values are skipped, and
// explicit parameters are never overridden by extra.
func TestWriteEnvFilePreservesExtra(t *testing.T) {
	envPath := filepath.Join(t.TempDir(), ".env")

	if err := writeEnvFile(envPath, "/c", "/l", "/w",
		"1000", "1000", "UTC", "wg", "NL", "7354",
		"", "key", "",
		EnvMap{
			"LIBRARY_NFS": "true",
			"NFS_HOST":    "nas.local",
			"NFS_EXPORT":  "/volume1/media",
			"NFS_OPTIONS": "",          // empty — must be skipped
			"LIBRARY_DIR": "/override", // collides with explicit param — must lose
		}); err != nil {
		t.Fatalf("writeEnvFile: %v", err)
	}

	env, err := ParseEnv(envPath)
	if err != nil {
		t.Fatalf("ParseEnv: %v", err)
	}
	for k, want := range map[string]string{
		"LIBRARY_NFS": "true",
		"NFS_HOST":    "nas.local",
		"NFS_EXPORT":  "/volume1/media",
		"LIBRARY_DIR": "/l",
	} {
		if got := env[k]; got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if _, ok := env["NFS_OPTIONS"]; ok {
		t.Error("empty extra value NFS_OPTIONS must not be written")
	}

	data, _ := os.ReadFile(envPath)
	if !strings.Contains(string(data), "NFS_HOST=") {
		t.Error("NFS_HOST missing from written .env")
	}
}
