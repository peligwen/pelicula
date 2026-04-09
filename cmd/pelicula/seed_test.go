package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedConfig(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.xml")
	content := "<Config><UrlBase>/sonarr</UrlBase></Config>"

	// First call should create the file
	if err := seedConfig(file, content); err != nil {
		t.Fatalf("seedConfig error: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Errorf("content mismatch: got %q", string(data))
	}

	// Second call should NOT overwrite
	newContent := "<Config><modified/></Config>"
	if err := seedConfig(file, newContent); err != nil {
		t.Fatal(err)
	}
	data2, _ := os.ReadFile(file)
	if string(data2) != content {
		t.Error("seedConfig overwrote existing file (should be idempotent)")
	}
}

func TestSeedConfigCreatesDir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "subdir", "nested", "config.xml")
	if err := seedConfig(file, "content"); err != nil {
		t.Fatalf("seedConfig with nested dir: %v", err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Error("file not created")
	}
}

func TestEnforceArrAuth_AuthEnabled(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.xml")

	// Config with auth Enabled (what *arr writes on first boot)
	content := `<Config>
  <UrlBase>/sonarr</UrlBase>
  <AuthenticationMethod>Forms</AuthenticationMethod>
  <AuthenticationRequired>Enabled</AuthenticationRequired>
  <Theme>light</Theme>
</Config>`
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := enforceArrAuth(file); err != nil {
		t.Fatalf("enforceArrAuth error: %v", err)
	}

	data, _ := os.ReadFile(file)
	patched := string(data)

	if !strings.Contains(patched, "<AuthenticationMethod>External</AuthenticationMethod>") {
		t.Error("expected AuthenticationMethod=External")
	}
	if !strings.Contains(patched, "<AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>") {
		t.Error("expected AuthenticationRequired=DisabledForLocalAddresses")
	}
	if !strings.Contains(patched, "<Theme>dark</Theme>") {
		t.Error("expected Theme=dark")
	}
}

func TestEnforceArrAuth_AlreadyPatched(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.xml")

	// Already in the desired state
	content := `<Config>
  <AuthenticationMethod>External</AuthenticationMethod>
  <AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>
  <Theme>dark</Theme>
</Config>`
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := enforceArrAuth(file); err != nil {
		t.Fatalf("enforceArrAuth error: %v", err)
	}

	data, _ := os.ReadFile(file)
	if string(data) != content {
		t.Error("enforceArrAuth modified already-correct config")
	}
}

func TestEnforceArrAuth_Missing(t *testing.T) {
	// Should not error on missing file
	if err := enforceArrAuth("/nonexistent/path/config.xml"); err != nil {
		t.Errorf("expected no error for missing file, got: %v", err)
	}
}

func TestExtractAPIKey(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.xml")

	content := `<Config><UrlBase>/sonarr</UrlBase><ApiKey>abc123xyz</ApiKey></Config>`
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	key := extractAPIKey(file)
	if key != "abc123xyz" {
		t.Errorf("got %q, want abc123xyz", key)
	}
}

func TestExtractAPIKeyMissing(t *testing.T) {
	key := extractAPIKey("/nonexistent/config.xml")
	if key != "" {
		t.Errorf("expected empty key for missing file, got %q", key)
	}
}

func TestSeedAllConfigs(t *testing.T) {
	dir := t.TempDir()

	if err := SeedAllConfigs(dir); err != nil {
		t.Fatalf("SeedAllConfigs error: %v", err)
	}

	// Check that each expected file was created
	checks := []string{
		"sonarr/config.xml",
		"radarr/config.xml",
		"prowlarr/config.xml",
		"jellyfin/network.xml",
		"jellyfin/config/branding.xml",
		"bazarr/config/config.ini",
		"qbittorrent/qBittorrent/qBittorrent.conf",
		"qbittorrent/qBittorrent/categories.json",
	}
	for _, rel := range checks {
		full := filepath.Join(dir, rel)
		if _, err := os.Stat(full); err != nil {
			t.Errorf("expected file %s not found: %v", rel, err)
		}
	}

	// Verify key content of arr configs
	data, _ := os.ReadFile(filepath.Join(dir, "sonarr", "config.xml"))
	if !strings.Contains(string(data), "/sonarr") {
		t.Error("sonarr config.xml missing /sonarr UrlBase")
	}

	// Verify Jellyfin branding has Cotton Candy accent color
	branding, _ := os.ReadFile(filepath.Join(dir, "jellyfin", "config", "branding.xml"))
	if !strings.Contains(string(branding), "#f060a8") {
		t.Error("branding.xml missing Cotton Candy accent color")
	}

	// Verify Bazarr base_url
	bazarr, _ := os.ReadFile(filepath.Join(dir, "bazarr", "config", "config.ini"))
	if !strings.Contains(string(bazarr), "base_url=/bazarr") {
		t.Error("bazarr config.ini missing base_url=/bazarr")
	}

	// Verify qBittorrent subnet whitelist
	qbt, _ := os.ReadFile(filepath.Join(dir, "qbittorrent", "qBittorrent", "qBittorrent.conf"))
	if !strings.Contains(string(qbt), "172.16.0.0/12") {
		t.Error("qBittorrent.conf missing subnet whitelist")
	}
}

func TestResetArrService(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "sonarr")
	if err := os.MkdirAll(svcDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Pre-existing file that should be wiped
	if err := os.WriteFile(filepath.Join(svcDir, "stale.db"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := ResetArrService("Sonarr", svcDir, "/sonarr", "myapikey"); err != nil {
		t.Fatalf("ResetArrService error: %v", err)
	}

	// Stale file should be gone
	if _, err := os.Stat(filepath.Join(svcDir, "stale.db")); err == nil {
		t.Error("stale.db should have been removed")
	}

	// config.xml should exist with preserved key
	data, err := os.ReadFile(filepath.Join(svcDir, "config.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "myapikey") {
		t.Error("config.xml missing preserved API key")
	}
	if !strings.Contains(string(data), "/sonarr") {
		t.Error("config.xml missing UrlBase")
	}
}
