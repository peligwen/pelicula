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

// TestRemoteModeEmpty verifies that REMOTE_MODE="" includes remote.yml if present.
func TestRemoteModeEmpty(t *testing.T) {
	dir := t.TempDir()

	// Create compose sub-directory and a remote.yml file to simulate existing install.
	composeDir := filepath.Join(dir, "compose")
	if err := os.MkdirAll(composeDir, 0755); err != nil {
		t.Fatal(err)
	}
	remoteYML := filepath.Join(composeDir, "docker-compose.remote.yml")
	if err := os.WriteFile(remoteYML, []byte("# stub"), 0644); err != nil {
		t.Fatal(err)
	}

	c := NewCompose(dir, false, false, "pelicula")
	c.remoteMode = "" // empty → portforward / existing behaviour
	args := c.buildArgs("up", "-d")

	if !hasArg(args, remoteYML) {
		t.Errorf("expected docker-compose.remote.yml in args when REMOTE_MODE=''; args=%v", args)
	}
	if hasArg(args, filepath.Join(composeDir, "docker-compose.cloudflared.yml")) {
		t.Errorf("did not expect cloudflared overlay when REMOTE_MODE=''")
	}
	if hasArg(args, filepath.Join(composeDir, "docker-compose.tailscale.yml")) {
		t.Errorf("did not expect tailscale overlay when REMOTE_MODE=''")
	}
}

// TestRemoteModePortforward verifies that REMOTE_MODE="portforward" behaves the same as "".
func TestRemoteModePortforward(t *testing.T) {
	dir := t.TempDir()

	composeDir := filepath.Join(dir, "compose")
	if err := os.MkdirAll(composeDir, 0755); err != nil {
		t.Fatal(err)
	}
	remoteYML := filepath.Join(composeDir, "docker-compose.remote.yml")
	if err := os.WriteFile(remoteYML, []byte("# stub"), 0644); err != nil {
		t.Fatal(err)
	}

	c := NewCompose(dir, false, false, "pelicula")
	c.remoteMode = "portforward"
	args := c.buildArgs("up", "-d")

	if !hasArg(args, remoteYML) {
		t.Errorf("expected docker-compose.remote.yml in args when REMOTE_MODE='portforward'; args=%v", args)
	}
	if hasArg(args, filepath.Join(composeDir, "docker-compose.cloudflared.yml")) {
		t.Errorf("did not expect cloudflared overlay when REMOTE_MODE='portforward'")
	}
}

// TestRemoteModeCloudflared verifies that REMOTE_MODE="cloudflared" selects the cloudflared overlay.
func TestRemoteModeCloudflared(t *testing.T) {
	dir := t.TempDir()

	composeDir := filepath.Join(dir, "compose")
	if err := os.MkdirAll(composeDir, 0755); err != nil {
		t.Fatal(err)
	}

	c := NewCompose(dir, false, false, "pelicula")
	c.remoteMode = "cloudflared"
	args := c.buildArgs("up", "-d")

	cloudflaredYML := filepath.Join(composeDir, "docker-compose.cloudflared.yml")
	if !hasArg(args, cloudflaredYML) {
		t.Errorf("expected docker-compose.cloudflared.yml in args when REMOTE_MODE='cloudflared'; args=%v", args)
	}
	if hasArg(args, filepath.Join(composeDir, "docker-compose.remote.yml")) {
		t.Errorf("did not expect docker-compose.remote.yml when REMOTE_MODE='cloudflared'")
	}
	if hasArg(args, filepath.Join(composeDir, "docker-compose.tailscale.yml")) {
		t.Errorf("did not expect tailscale overlay when REMOTE_MODE='cloudflared'")
	}
}

// TestRemoteModeTailscale verifies that REMOTE_MODE="tailscale" selects the tailscale overlay.
func TestRemoteModeTailscale(t *testing.T) {
	dir := t.TempDir()

	composeDir := filepath.Join(dir, "compose")
	if err := os.MkdirAll(composeDir, 0755); err != nil {
		t.Fatal(err)
	}

	c := NewCompose(dir, false, false, "pelicula")
	c.remoteMode = "tailscale"
	args := c.buildArgs("up", "-d")

	tailscaleYML := filepath.Join(composeDir, "docker-compose.tailscale.yml")
	if !hasArg(args, tailscaleYML) {
		t.Errorf("expected docker-compose.tailscale.yml in args when REMOTE_MODE='tailscale'; args=%v", args)
	}
	if hasArg(args, filepath.Join(composeDir, "docker-compose.remote.yml")) {
		t.Errorf("did not expect docker-compose.remote.yml when REMOTE_MODE='tailscale'")
	}
	if hasArg(args, filepath.Join(composeDir, "docker-compose.cloudflared.yml")) {
		t.Errorf("did not expect cloudflared overlay when REMOTE_MODE='tailscale'")
	}
}
