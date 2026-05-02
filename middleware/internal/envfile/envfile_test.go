package envfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvfile_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	in := map[string]string{
		"CONFIG_DIR":            "/config",
		"PELICULA_PORT":         "7354",
		"TRANSCODING_ENABLED":   "false",
		"NOTIFICATIONS_ENABLED": "false",
	}

	if err := Write(path, in); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	for k, want := range in {
		if got[k] != want {
			t.Errorf("key %q: got %q, want %q", k, got[k], want)
		}
	}
}

func TestEnvfile_StripQuotes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	// Test both double and single quoted values as written to the file
	content := "KEY_DQ=\"value\"\nKEY_SQ='value'\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Double quotes are stripped
	if got["KEY_DQ"] != "value" {
		t.Errorf("KEY_DQ = %q, want %q", got["KEY_DQ"], "value")
	}
	// Single quotes are NOT stripped (Parse only strips double quotes, matching original behavior)
	if got["KEY_SQ"] != "'value'" {
		t.Errorf("KEY_SQ = %q, want %q (single quotes not stripped)", got["KEY_SQ"], "'value'")
	}
}

func TestEnvfile_SkipsComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	content := "# comment\nKEY=value\n# another comment\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	got, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got["KEY"] != "value" {
		t.Errorf("KEY = %q, want %q", got["KEY"], "value")
	}
	if len(got) != 1 {
		t.Errorf("expected 1 key, got %d: %v", len(got), got)
	}
}

func TestEnvfile_CanonicalOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	vars := map[string]string{
		"LIBRARY_DIR":   "/media",
		"CONFIG_DIR":    "/config",
		"WORK_DIR":      "/work",
		"PELICULA_PORT": "7354",
	}

	if err := Write(path, vars); err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)

	configIdx := strings.Index(content, "CONFIG_DIR=")
	libraryIdx := strings.Index(content, "LIBRARY_DIR=")
	workIdx := strings.Index(content, "WORK_DIR=")

	if configIdx < 0 || libraryIdx < 0 || workIdx < 0 {
		t.Fatalf("one or more expected keys missing from output:\n%s", content)
	}
	if configIdx > libraryIdx {
		t.Errorf("CONFIG_DIR (pos %d) should appear before LIBRARY_DIR (pos %d)", configIdx, libraryIdx)
	}
	if libraryIdx > workIdx {
		t.Errorf("LIBRARY_DIR (pos %d) should appear before WORK_DIR (pos %d)", libraryIdx, workIdx)
	}
}

func TestEnvfile_BooleansUnquoted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	vars := map[string]string{
		"TRANSCODING_ENABLED":   "false",
		"NOTIFICATIONS_ENABLED": "true",
		"CONFIG_DIR":            "/config",
	}

	if err := Write(path, vars); err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "TRANSCODING_ENABLED=false\n") {
		t.Error("expected TRANSCODING_ENABLED=false (unquoted)")
	}
	if !strings.Contains(content, "NOTIFICATIONS_ENABLED=true\n") {
		t.Error("expected NOTIFICATIONS_ENABLED=true (unquoted)")
	}
	// Non-boolean must be quoted
	if !strings.Contains(content, `CONFIG_DIR="/config"`) {
		t.Error(`expected CONFIG_DIR="/config" (double-quoted)`)
	}
}

func TestEnvfile_PreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	// Parse a file with a key not in the canonical order list
	content := "CONFIG_DIR=\"/config\"\nMY_CUSTOM_KEY=\"custom_value\"\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	vars, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Write back the parsed vars (round-trip)
	if err := Write(path, vars); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Re-parse and assert the unknown key survived
	got, err := Parse(path)
	if err != nil {
		t.Fatalf("Parse after Write: %v", err)
	}

	if got["MY_CUSTOM_KEY"] != "custom_value" {
		t.Errorf("MY_CUSTOM_KEY = %q, want %q — unknown keys must survive a round-trip", got["MY_CUSTOM_KEY"], "custom_value")
	}
	if got["CONFIG_DIR"] != "/config" {
		t.Errorf("CONFIG_DIR = %q, want /config", got["CONFIG_DIR"])
	}
}
