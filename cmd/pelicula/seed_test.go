package main

import (
	"os"
	"path/filepath"
	"regexp"
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

func TestEnforceArrConfig_AuthEnabled(t *testing.T) {
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

	if err := enforceArrConfig(file); err != nil {
		t.Fatalf("enforceArrConfig error: %v", err)
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

func TestEnforceArrConfig_AlreadyPatched(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.xml")

	// Already in the desired state
	content := `<Config>
  <AuthenticationMethod>External</AuthenticationMethod>
  <AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>
  <Theme>dark</Theme>
  <AnalyticsEnabled>False</AnalyticsEnabled>
</Config>`
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := enforceArrConfig(file); err != nil {
		t.Fatalf("enforceArrConfig error: %v", err)
	}

	data, _ := os.ReadFile(file)
	if string(data) != content {
		t.Error("enforceArrConfig modified already-correct config")
	}
}

func TestEnforceArrConfig_Missing(t *testing.T) {
	// Should not error on missing file
	if err := enforceArrConfig("/nonexistent/path/config.xml"); err != nil {
		t.Errorf("expected no error for missing file, got: %v", err)
	}
}

func TestEnforceArrConfig_FreshInstall(t *testing.T) {
	// Fresh install: no AnalyticsEnabled, no Theme, no LogLevel
	// Should add AnalyticsEnabled, NOT add LogLevel (it doesn't exist), leave auth correct.
	dir := t.TempDir()
	file := filepath.Join(dir, "config.xml")
	content := `<Config>
  <UrlBase>/sonarr</UrlBase>
  <AuthenticationMethod>External</AuthenticationMethod>
  <AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>
</Config>`
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := enforceArrConfig(file); err != nil {
		t.Fatalf("enforceArrConfig error: %v", err)
	}

	data, _ := os.ReadFile(file)
	patched := string(data)

	if !strings.Contains(patched, "<AnalyticsEnabled>False</AnalyticsEnabled>") {
		t.Error("expected AnalyticsEnabled=False to be inserted")
	}
	if strings.Contains(patched, "<LogLevel>") {
		t.Error("LogLevel should not be added if it didn't exist")
	}
	if !strings.Contains(patched, "<AuthenticationMethod>External</AuthenticationMethod>") {
		t.Error("auth fields should be preserved")
	}
}

func TestEnforceArrConfig_DebugToInfo(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.xml")
	content := `<Config>
  <AuthenticationMethod>External</AuthenticationMethod>
  <AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>
  <LogLevel>debug</LogLevel>
</Config>`
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := enforceArrConfig(file); err != nil {
		t.Fatalf("enforceArrConfig error: %v", err)
	}

	data, _ := os.ReadFile(file)
	patched := string(data)

	if !strings.Contains(patched, "<LogLevel>info</LogLevel>") {
		t.Error("expected debug to be replaced with info")
	}
	if strings.Contains(patched, "<LogLevel>debug</LogLevel>") {
		t.Error("debug log level should have been replaced")
	}
}

func TestEnforceArrConfig_OtherLogLevelUnchanged(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.xml")
	content := `<Config>
  <AuthenticationMethod>External</AuthenticationMethod>
  <AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>
  <LogLevel>trace</LogLevel>
</Config>`
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := enforceArrConfig(file); err != nil {
		t.Fatalf("enforceArrConfig error: %v", err)
	}

	data, _ := os.ReadFile(file)
	patched := string(data)

	if !strings.Contains(patched, "<LogLevel>trace</LogLevel>") {
		t.Error("trace log level should be left unchanged")
	}
}

func TestEnforceArrConfig_AnalyticsFlippedToFalse(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.xml")
	content := `<Config>
  <AuthenticationMethod>External</AuthenticationMethod>
  <AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>
  <AnalyticsEnabled>True</AnalyticsEnabled>
</Config>`
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := enforceArrConfig(file); err != nil {
		t.Fatalf("enforceArrConfig error: %v", err)
	}

	data, _ := os.ReadFile(file)
	patched := string(data)

	if !strings.Contains(patched, "<AnalyticsEnabled>False</AnalyticsEnabled>") {
		t.Error("expected AnalyticsEnabled=True to be flipped to False")
	}
}

func TestEnforceArrConfig_OneLinerConfig(t *testing.T) {
	// The seeded config format is a one-liner; AnalyticsEnabled must be inserted correctly.
	dir := t.TempDir()
	file := filepath.Join(dir, "config.xml")
	content := `<Config><UrlBase>/sonarr</UrlBase><AuthenticationMethod>External</AuthenticationMethod><AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired></Config>`
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := enforceArrConfig(file); err != nil {
		t.Fatalf("enforceArrConfig error: %v", err)
	}

	data, _ := os.ReadFile(file)
	patched := string(data)

	if !strings.Contains(patched, "<AnalyticsEnabled>False</AnalyticsEnabled>") {
		t.Error("expected AnalyticsEnabled=False to be inserted in one-liner config")
	}
	if !strings.Contains(patched, "</Config>") {
		t.Error("</Config> closing tag must be present")
	}
	// The result must end with </Config> (no trailing junk)
	if !strings.HasSuffix(strings.TrimSpace(patched), "</Config>") {
		t.Errorf("result must end with </Config>, got: %q", patched)
	}
	// Basic structural check: AnalyticsEnabled must appear before </Config>
	aIdx := strings.Index(patched, "<AnalyticsEnabled>")
	cIdx := strings.Index(patched, "</Config>")
	if aIdx < 0 || cIdx < 0 || aIdx > cIdx {
		t.Error("AnalyticsEnabled must appear before </Config>")
	}
}

func TestEnforceJellyfinSystem_FlipsToFalse(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "jellyfin", "config")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := `<?xml version="1.0" encoding="utf-8"?>
<ServerConfiguration>
  <AllowClientLogUpload>true</AllowClientLogUpload>
</ServerConfiguration>`
	if err := os.WriteFile(filepath.Join(cfgDir, "system.xml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := enforceJellyfinSystem(dir); err != nil {
		t.Fatalf("enforceJellyfinSystem error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(cfgDir, "system.xml"))
	patched := string(data)

	if !strings.Contains(patched, "<AllowClientLogUpload>false</AllowClientLogUpload>") {
		t.Error("expected AllowClientLogUpload to be set to false")
	}
	if strings.Contains(patched, "<AllowClientLogUpload>true</AllowClientLogUpload>") {
		t.Error("true value should have been replaced")
	}
}

func TestEnforceJellyfinSystem_Idempotent(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "jellyfin", "config")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	hostname, _ := os.Hostname()
	content := `<?xml version="1.0" encoding="utf-8"?>
<ServerConfiguration>
  <AllowClientLogUpload>false</AllowClientLogUpload>
  <QuickConnectAvailable>false</QuickConnectAvailable>
  <ServerName>` + hostname + `</ServerName>
</ServerConfiguration>`
	if err := os.WriteFile(filepath.Join(cfgDir, "system.xml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := enforceJellyfinSystem(dir); err != nil {
		t.Fatalf("enforceJellyfinSystem error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(cfgDir, "system.xml"))
	if string(data) != content {
		t.Errorf("enforceJellyfinSystem modified already-correct system.xml\ngot: %q\nwant: %q", string(data), content)
	}
}

func TestEnforceJellyfinSystem_Insert(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "jellyfin", "config")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := `<?xml version="1.0" encoding="utf-8"?>
<ServerConfiguration>
  <SomeOtherField>true</SomeOtherField>
</ServerConfiguration>`
	if err := os.WriteFile(filepath.Join(cfgDir, "system.xml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := enforceJellyfinSystem(dir); err != nil {
		t.Fatalf("enforceJellyfinSystem error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(cfgDir, "system.xml"))
	patched := string(data)
	if !strings.Contains(patched, "<AllowClientLogUpload>false</AllowClientLogUpload>") {
		t.Error("expected AllowClientLogUpload to be inserted")
	}
	if !strings.Contains(patched, "</ServerConfiguration>") {
		t.Error("expected </ServerConfiguration> to still be present")
	}
}

func TestEnforceJellyfinSystem_Missing(t *testing.T) {
	// Should not error when system.xml doesn't exist (Jellyfin hasn't run yet)
	if err := enforceJellyfinSystem("/nonexistent/dir"); err != nil {
		t.Errorf("expected no error for missing system.xml, got: %v", err)
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
		"jellyfin/config/dlna.xml",
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

	// Verify network.xml includes KnownProxies with default subnet
	network, _ := os.ReadFile(filepath.Join(dir, "jellyfin", "network.xml"))
	if !strings.Contains(string(network), "172.16.0.0/12") {
		t.Error("network.xml missing default KnownProxies subnet")
	}
}

func TestSeedAllConfigs_WritesPublishedServerURLWhenSet(t *testing.T) {
	t.Setenv("JELLYFIN_PUBLISHED_URL", "http://192.168.1.42:7354/jellyfin")

	dir := t.TempDir()
	if err := SeedAllConfigs(dir); err != nil {
		t.Fatalf("SeedAllConfigs: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "jellyfin", "network.xml"))
	got := string(data)

	if !strings.Contains(got, "<PublishedServerUrl>http://192.168.1.42:7354/jellyfin</PublishedServerUrl>") {
		t.Errorf("network.xml missing PublishedServerUrl\n got: %s", got)
	}
	if !strings.Contains(got, "<BaseUrl>/jellyfin</BaseUrl>") {
		t.Errorf("network.xml missing BaseUrl\n got: %s", got)
	}
}

func TestSeedAllConfigs_OmitsPublishedServerURLWhenUnset(t *testing.T) {
	t.Setenv("JELLYFIN_PUBLISHED_URL", "")

	dir := t.TempDir()
	if err := SeedAllConfigs(dir); err != nil {
		t.Fatalf("SeedAllConfigs: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "jellyfin", "network.xml"))
	got := string(data)

	if strings.Contains(got, "PublishedServerUrl") {
		t.Errorf("network.xml should not mention PublishedServerUrl when env unset\n got: %s", got)
	}
}

func TestSeedAllConfigs_EscapesPublishedServerURL(t *testing.T) {
	t.Setenv("JELLYFIN_PUBLISHED_URL", "http://h&t<s>:7354/jellyfin")

	dir := t.TempDir()
	if err := SeedAllConfigs(dir); err != nil {
		t.Fatalf("SeedAllConfigs: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "jellyfin", "network.xml"))
	got := string(data)

	// Raw unescaped characters must not appear inside the element
	if strings.Contains(got, "h&t<s>") {
		t.Errorf("network.xml should XML-escape special chars\n got: %s", got)
	}
	// Escaped form must appear
	if !strings.Contains(got, "http://h&amp;t&lt;s&gt;:7354/jellyfin") {
		t.Errorf("network.xml missing escaped PublishedServerUrl\n got: %s", got)
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

// ── patchXMLFile unit tests ──────────────────────────────────────────────────

// TestPatchXMLFile_NoOp verifies that patchXMLFile returns nil without writing
// when no patch changes the content.
func TestPatchXMLFile_NoOp(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.xml")
	content := `<Config><Foo>bar</Foo></Config>`
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	// Record mtime before patch.
	info0, _ := os.Stat(file)

	patches := []xmlPatch{
		{
			matchRe:     regexp.MustCompile(`<Baz>[^<]*</Baz>`),
			replacement: "<Baz>replaced</Baz>",
			// No insertBeforeRe → only replaces, never inserts.
		},
	}
	if err := patchXMLFile(file, patches); err != nil {
		t.Fatalf("patchXMLFile error: %v", err)
	}

	info1, _ := os.Stat(file)
	if info1.ModTime() != info0.ModTime() {
		t.Error("patchXMLFile should not write when content is unchanged")
	}
	data, _ := os.ReadFile(file)
	if string(data) != content {
		t.Errorf("content changed unexpectedly: %q", string(data))
	}
}

// TestPatchXMLFile_MissingFile verifies that patchXMLFile returns nil for a
// non-existent file (treated as not-yet-created).
func TestPatchXMLFile_MissingFile(t *testing.T) {
	patches := []xmlPatch{
		{
			matchRe:     regexp.MustCompile(`<Foo>[^<]*</Foo>`),
			replacement: "<Foo>bar</Foo>",
		},
	}
	err := patchXMLFile("/nonexistent/path/config.xml", patches)
	if err != nil {
		t.Errorf("expected nil for missing file, got: %v", err)
	}
}

// TestPatchXMLFile_MultiplePatches verifies that multiple patches are applied
// in order and all take effect in a single write.
func TestPatchXMLFile_MultiplePatches(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.xml")
	content := `<Config><Alpha>old</Alpha><Beta>old</Beta></Config>`
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	patches := []xmlPatch{
		{
			matchRe:     regexp.MustCompile(`<Alpha>[^<]*</Alpha>`),
			replacement: "<Alpha>new</Alpha>",
		},
		{
			matchRe:     regexp.MustCompile(`<Beta>[^<]*</Beta>`),
			replacement: "<Beta>new</Beta>",
		},
	}
	if err := patchXMLFile(file, patches); err != nil {
		t.Fatalf("patchXMLFile error: %v", err)
	}

	data, _ := os.ReadFile(file)
	patched := string(data)
	if !strings.Contains(patched, "<Alpha>new</Alpha>") {
		t.Error("expected Alpha to be patched")
	}
	if !strings.Contains(patched, "<Beta>new</Beta>") {
		t.Error("expected Beta to be patched")
	}
}

// TestPatchXMLFile_InsertFallback verifies that when matchRe does not match
// and insertBeforeRe is set, the text is inserted before the anchor.
func TestPatchXMLFile_InsertFallback(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.xml")
	content := `<Config><Existing>yes</Existing></Config>`
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	patches := []xmlPatch{
		{
			matchRe:        regexp.MustCompile(`<NewElement>[^<]*</NewElement>`),
			replacement:    "<NewElement>value</NewElement>",
			insertBeforeRe: regexp.MustCompile(`</Config>`),
			insertText:     "<NewElement>value</NewElement></Config>",
		},
	}
	if err := patchXMLFile(file, patches); err != nil {
		t.Fatalf("patchXMLFile error: %v", err)
	}

	data, _ := os.ReadFile(file)
	patched := string(data)
	if !strings.Contains(patched, "<NewElement>value</NewElement>") {
		t.Errorf("expected NewElement to be inserted, got: %q", patched)
	}
	if !strings.Contains(patched, "</Config>") {
		t.Error("</Config> must remain present after insert")
	}
}

// ── enforceJellyfinSystem: new-feature tests ─────────────────────────────────

// TestEnforceJellyfinSystem_QuickConnectReasserted verifies that QuickConnect
// is set to false even when the input has true.
func TestEnforceJellyfinSystem_QuickConnectReasserted(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "jellyfin", "config")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := `<?xml version="1.0" encoding="utf-8"?>
<ServerConfiguration>
  <QuickConnectAvailable>true</QuickConnectAvailable>
  <AllowClientLogUpload>false</AllowClientLogUpload>
</ServerConfiguration>`
	if err := os.WriteFile(filepath.Join(cfgDir, "system.xml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	if err := enforceJellyfinSystem(dir); err != nil {
		t.Fatalf("enforceJellyfinSystem error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(cfgDir, "system.xml"))
	patched := string(data)

	if !strings.Contains(patched, "<QuickConnectAvailable>false</QuickConnectAvailable>") {
		t.Error("expected QuickConnectAvailable to be set to false")
	}
	if strings.Contains(patched, "<QuickConnectAvailable>true</QuickConnectAvailable>") {
		t.Error("QuickConnectAvailable true should have been replaced")
	}
}

// TestEnforceJellyfinSystem_ServerNamePopulatedWhenEmpty verifies that an empty
// ServerName is populated on first boot.
func TestEnforceJellyfinSystem_ServerNamePopulatedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "jellyfin", "config")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := `<?xml version="1.0" encoding="utf-8"?>
<ServerConfiguration>
  <ServerName />
  <AllowClientLogUpload>false</AllowClientLogUpload>
  <QuickConnectAvailable>false</QuickConnectAvailable>
</ServerConfiguration>`
	if err := os.WriteFile(filepath.Join(cfgDir, "system.xml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PELICULA_DASHBOARD_NAME", "MyPelicula")
	if err := enforceJellyfinSystem(dir); err != nil {
		t.Fatalf("enforceJellyfinSystem error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(cfgDir, "system.xml"))
	patched := string(data)

	if !strings.Contains(patched, "<ServerName>MyPelicula</ServerName>") {
		t.Errorf("expected ServerName to be populated with env value, got:\n%s", patched)
	}
}

// TestEnforceJellyfinSystem_ServerNameLeftAloneWhenSet verifies that a
// non-empty ServerName is never overwritten.
func TestEnforceJellyfinSystem_ServerNameLeftAloneWhenSet(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "jellyfin", "config")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := `<?xml version="1.0" encoding="utf-8"?>
<ServerConfiguration>
  <ServerName>UserChosenName</ServerName>
  <AllowClientLogUpload>false</AllowClientLogUpload>
  <QuickConnectAvailable>false</QuickConnectAvailable>
</ServerConfiguration>`
	if err := os.WriteFile(filepath.Join(cfgDir, "system.xml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PELICULA_DASHBOARD_NAME", "SomethingElse")
	if err := enforceJellyfinSystem(dir); err != nil {
		t.Fatalf("enforceJellyfinSystem error: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(cfgDir, "system.xml"))
	if string(data) != content {
		t.Errorf("enforceJellyfinSystem must not overwrite non-empty ServerName\ngot: %s", string(data))
	}
}

// ── DLNA tests ────────────────────────────────────────────────────────────────

// TestSeedAllConfigs_DlnaSeededWhenMissing verifies that dlna.xml is created on
// first boot with EnableServer and EnablePlayTo both false.
func TestSeedAllConfigs_DlnaSeededWhenMissing(t *testing.T) {
	dir := t.TempDir()

	if err := SeedAllConfigs(dir); err != nil {
		t.Fatalf("SeedAllConfigs error: %v", err)
	}

	dlnaPath := filepath.Join(dir, "jellyfin", "config", "dlna.xml")
	data, err := os.ReadFile(dlnaPath)
	if err != nil {
		t.Fatalf("dlna.xml not created: %v", err)
	}
	got := string(data)

	if !strings.Contains(got, "<EnableServer>false</EnableServer>") {
		t.Error("expected EnableServer=false in dlna.xml")
	}
	if !strings.Contains(got, "<EnablePlayTo>false</EnablePlayTo>") {
		t.Error("expected EnablePlayTo=false in dlna.xml")
	}
}

// TestSeedAllConfigs_DlnaNotOverwrittenWhenPresent verifies that seedConfig
// does not clobber a user-modified dlna.xml.
func TestSeedAllConfigs_DlnaNotOverwrittenWhenPresent(t *testing.T) {
	dir := t.TempDir()

	cfgDir := filepath.Join(dir, "jellyfin", "config")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	customContent := `<DlnaOptions><EnableServer>true</EnableServer></DlnaOptions>`
	dlnaPath := filepath.Join(cfgDir, "dlna.xml")
	if err := os.WriteFile(dlnaPath, []byte(customContent), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SeedAllConfigs(dir); err != nil {
		t.Fatalf("SeedAllConfigs error: %v", err)
	}

	data, _ := os.ReadFile(dlnaPath)
	if string(data) != customContent {
		t.Error("SeedAllConfigs must not overwrite existing dlna.xml")
	}
}

// ── network.xml / KnownProxies tests ─────────────────────────────────────────

// TestJellyfinNetworkXML_DefaultKnownProxies verifies that the seeded
// network.xml includes the default Docker bridge subnet when no env is set.
func TestJellyfinNetworkXML_DefaultKnownProxies(t *testing.T) {
	t.Setenv("PELICULA_KNOWN_PROXIES", "")
	t.Setenv("JELLYFIN_PUBLISHED_URL", "")

	got := jellyfinNetworkXML()

	if !strings.Contains(got, "<KnownProxies>") {
		t.Error("network.xml must include KnownProxies element")
	}
	if !strings.Contains(got, "172.16.0.0/12") {
		t.Errorf("expected default subnet 172.16.0.0/12 in KnownProxies, got: %s", got)
	}
}

// TestJellyfinNetworkXML_EnvKnownProxies verifies that PELICULA_KNOWN_PROXIES
// overrides the default.
func TestJellyfinNetworkXML_EnvKnownProxies(t *testing.T) {
	t.Setenv("PELICULA_KNOWN_PROXIES", "10.0.0.1,10.0.0.2")
	t.Setenv("JELLYFIN_PUBLISHED_URL", "")

	got := jellyfinNetworkXML()

	if !strings.Contains(got, "<string>10.0.0.1</string>") {
		t.Errorf("expected 10.0.0.1 in KnownProxies, got: %s", got)
	}
	if !strings.Contains(got, "<string>10.0.0.2</string>") {
		t.Errorf("expected 10.0.0.2 in KnownProxies, got: %s", got)
	}
	if strings.Contains(got, "172.16.0.0/12") {
		t.Error("default subnet should not appear when env override is set")
	}
}

// TestEnforceJellyfinNetwork_ReassertWhenEnvSet verifies that enforceJellyfinNetwork
// updates an existing network.xml when PELICULA_KNOWN_PROXIES is set.
func TestEnforceJellyfinNetwork_ReassertWhenEnvSet(t *testing.T) {
	dir := t.TempDir()
	jellyfinDir := filepath.Join(dir, "jellyfin")
	if err := os.MkdirAll(jellyfinDir, 0755); err != nil {
		t.Fatal(err)
	}
	initial := `<?xml version="1.0" encoding="utf-8"?><NetworkConfiguration xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema"><BaseUrl>/jellyfin</BaseUrl><KnownProxies><string>172.16.0.0/12</string></KnownProxies></NetworkConfiguration>`
	networkPath := filepath.Join(jellyfinDir, "network.xml")
	if err := os.WriteFile(networkPath, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PELICULA_KNOWN_PROXIES", "192.168.1.1")
	if err := enforceJellyfinNetwork(dir); err != nil {
		t.Fatalf("enforceJellyfinNetwork error: %v", err)
	}

	data, _ := os.ReadFile(networkPath)
	got := string(data)

	if !strings.Contains(got, "<string>192.168.1.1</string>") {
		t.Errorf("expected env-overridden IP in KnownProxies, got: %s", got)
	}
	if strings.Contains(got, "172.16.0.0/12") {
		t.Error("old default subnet should have been replaced")
	}
}

// TestEnforceJellyfinNetwork_NoOpWhenEnvUnset verifies that enforceJellyfinNetwork
// is a no-op when PELICULA_KNOWN_PROXIES is not set.
func TestEnforceJellyfinNetwork_NoOpWhenEnvUnset(t *testing.T) {
	dir := t.TempDir()
	jellyfinDir := filepath.Join(dir, "jellyfin")
	if err := os.MkdirAll(jellyfinDir, 0755); err != nil {
		t.Fatal(err)
	}
	initial := `<NetworkConfiguration><BaseUrl>/jellyfin</BaseUrl><KnownProxies><string>172.16.0.0/12</string></KnownProxies></NetworkConfiguration>`
	networkPath := filepath.Join(jellyfinDir, "network.xml")
	if err := os.WriteFile(networkPath, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PELICULA_KNOWN_PROXIES", "")
	if err := enforceJellyfinNetwork(dir); err != nil {
		t.Fatalf("enforceJellyfinNetwork error: %v", err)
	}

	data, _ := os.ReadFile(networkPath)
	if string(data) != initial {
		t.Error("enforceJellyfinNetwork must not modify network.xml when env is unset")
	}
}

// ── patchXMLFile unit tests ──────────────────────────────────────────────────

// TestPatchXMLFile_CondGuard verifies that a patch with condRe is skipped when
// the guard condition does not match.
func TestPatchXMLFile_CondGuard(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.xml")
	content := `<Config><Status>inactive</Status><Foo>old</Foo></Config>`
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	patches := []xmlPatch{
		{
			// Guard: only patch Foo if Status is "active" — it's not, so skip.
			condRe:      regexp.MustCompile(`<Status>active</Status>`),
			matchRe:     regexp.MustCompile(`<Foo>[^<]*</Foo>`),
			replacement: "<Foo>new</Foo>",
		},
	}
	if err := patchXMLFile(file, patches); err != nil {
		t.Fatalf("patchXMLFile error: %v", err)
	}

	data, _ := os.ReadFile(file)
	if strings.Contains(string(data), "<Foo>new</Foo>") {
		t.Error("Foo should NOT have been patched when guard condition does not match")
	}
	if !strings.Contains(string(data), "<Foo>old</Foo>") {
		t.Error("Foo should retain original value when guard condition does not match")
	}
}
