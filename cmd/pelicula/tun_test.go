package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckTUN(t *testing.T) {
	dir := t.TempDir()
	present := filepath.Join(dir, "tun")
	writeFileT(t, present, "")
	missing := filepath.Join(dir, "missing")

	if err := checkTUN("linux", present); err != nil {
		t.Errorf("present device: %v", err)
	}
	err := checkTUN("linux", missing)
	if err == nil || !strings.Contains(err.Error(), "mknod") {
		t.Errorf("missing device error = %v, want a mknod hint", err)
	}
	for _, goos := range []string{"darwin", "windows"} {
		if err := checkTUN(goos, missing); err != nil {
			t.Errorf("%s needs no TUN device: %v", goos, err)
		}
	}
}

func TestEnsureTUNWritesOverrideOnLinux(t *testing.T) {
	dir := t.TempDir()
	tun := filepath.Join(dir, "tun")
	writeFileT(t, tun, "")
	override := tunOverridePath(dir)

	if err := ensureTUN("linux", dir, tun, true); err != nil {
		t.Fatal(err)
	}
	got := readFileT(t, override)
	if got != tunOverrideYAML {
		t.Errorf("override = %q", got)
	}
	for _, want := range []string{"services:", "gluetun:", "/dev/net/tun:/dev/net/tun"} {
		if !strings.Contains(got, want) {
			t.Errorf("override lacks %q", want)
		}
	}

	// Idempotent: unchanged content is not rewritten.
	if err := os.Chtimes(override, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	if err := ensureTUN("linux", dir, tun, true); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(override); !fi.ModTime().Equal(fixedTime) {
		t.Error("override rewritten although unchanged")
	}

	// A stale or hand-edited override is restored.
	writeFileT(t, override, "services: {}\n")
	if err := ensureTUN("linux", dir, tun, true); err != nil {
		t.Fatal(err)
	}
	if readFileT(t, override) != tunOverrideYAML {
		t.Error("stale override not regenerated")
	}
}

func TestEnsureTUNErrorsOnlyWhenVPNNeedsIt(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-tun")

	if err := ensureTUN("linux", dir, missing, true); err == nil {
		t.Error("VPN enabled with no TUN device must fail")
	}
	if err := ensureTUN("linux", dir, missing, false); err != nil {
		t.Errorf("without a VPN a missing TUN device is fine: %v", err)
	}
}

func TestEnsureTUNSkipsOtherPlatforms(t *testing.T) {
	dir := t.TempDir()
	for _, goos := range []string{"darwin", "windows"} {
		if err := ensureTUN(goos, dir, filepath.Join(dir, "none"), true); err != nil {
			t.Errorf("%s: %v", goos, err)
		}
		if fileExists(tunOverridePath(dir)) {
			t.Errorf("%s must not get a TUN override", goos)
		}
	}
}

func TestEnsureTUNOverrideDirMissing(t *testing.T) {
	if err := ensureTUN("linux", filepath.Join(t.TempDir(), "no", "such", "dir"), "/dev/null", false); err == nil {
		t.Error("unwritable compose dir should surface an error")
	}
}
