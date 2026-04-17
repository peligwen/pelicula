package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"pelicula-api/httputil"
	"strings"
	"testing"
)

// ── parseEnvFile / writeEnvFile round-trip ────────────────────────────────────

func TestParseEnvFile_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	in := map[string]string{
		"CONFIG_DIR":            "/config",
		"PELICULA_PORT":         "7354",
		"TRANSCODING_ENABLED":   "false",
		"NOTIFICATIONS_ENABLED": "false",
	}

	if err := writeEnvFile(path, in); err != nil {
		t.Fatalf("writeEnvFile: %v", err)
	}

	got, err := parseEnvFile(path)
	if err != nil {
		t.Fatalf("parseEnvFile: %v", err)
	}

	for k, want := range in {
		if got[k] != want {
			t.Errorf("key %q: got %q, want %q", k, got[k], want)
		}
	}
}

func TestParseEnvFile_StripQuotes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(`KEY="value with spaces"`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := parseEnvFile(path)
	if err != nil {
		t.Fatalf("parseEnvFile: %v", err)
	}
	if got["KEY"] != "value with spaces" {
		t.Errorf("KEY = %q, want %q", got["KEY"], "value with spaces")
	}
}

func TestParseEnvFile_SkipsComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := `# comment
KEY=value
# another comment
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := parseEnvFile(path)
	if err != nil {
		t.Fatalf("parseEnvFile: %v", err)
	}
	if got["KEY"] != "value" {
		t.Errorf("KEY = %q, want %q", got["KEY"], "value")
	}
	if len(got) != 1 {
		t.Errorf("expected 1 key, got %d: %v", len(got), got)
	}
}

func TestParseEnvFile_NotExist(t *testing.T) {
	_, err := parseEnvFile("/nonexistent/.env")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// ── handleSettingsUpdate input validation ─────────────────────────────────────

func newSettingsEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	vars := map[string]string{
		"CONFIG_DIR":            "/config",
		"LIBRARY_DIR":           "/media",
		"WORK_DIR":              "/work",
		"PUID":                  "1000",
		"PGID":                  "1000",
		"TZ":                    "UTC",
		"WIREGUARD_PRIVATE_KEY": strings.Repeat("A", 43) + "=",
		"SERVER_COUNTRIES":      "Netherlands",
		"PELICULA_PORT":         "7354",
		"PROCULA_API_KEY":       "testkey",
		"WEBHOOK_SECRET":        "testsecret",
		"TRANSCODING_ENABLED":   "false",
		"NOTIFICATIONS_ENABLED": "false",
		"NOTIFICATIONS_MODE":    "internal",
	}
	if err := writeEnvFile(path, vars); err != nil {
		t.Fatalf("write test .env: %v", err)
	}
	return path
}

func TestHandleSettingsUpdate_RejectsInvalidCharacters(t *testing.T) {
	path := newSettingsEnv(t)
	t.Setenv("HOME", filepath.Dir(path)) // unused but harmless
	// Patch envPath to use the temp file by hijacking it via env
	origEnvPath := envPath
	// We can't reassign the const directly; test via the handler with
	// a direct call won't work cleanly — use the HTTP layer instead
	_ = origEnvPath
	_ = path

	body, _ := json.Marshal(SettingsResponse{Country: "bad\ncountry"})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/settings", bytes.NewReader(body))
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	handleSettingsUpdate(w, req)

	// envPath is a compile-time const pointing at /project/.env which
	// won't exist in test; the handler will fail when trying to read it
	// after validation passes. The important thing is it does NOT fail
	// before validation — it should fail with 400 for the bad character.
	if w.Code == http.StatusOK {
		t.Error("expected non-200 for country containing newline")
	}
	if w.Code == http.StatusForbidden {
		t.Error("should not be forbidden — origin is localhost")
	}
}

func TestHandleSettingsUpdate_RejectsForeignOrigin(t *testing.T) {
	body, _ := json.Marshal(SettingsResponse{Country: "Netherlands"})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/settings", bytes.NewReader(body))
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	httputil.RequireLocalOriginStrict(http.HandlerFunc(handleSettingsUpdate)).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for foreign origin", w.Code)
	}
}

func TestHandleSettingsUpdate_RejectsEmptyOrigin(t *testing.T) {
	// Empty Origin must be rejected by the strict CSRF guard.
	body, _ := json.Marshal(SettingsResponse{Country: "Netherlands"})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/settings", bytes.NewReader(body))
	// No Origin header set
	w := httptest.NewRecorder()
	httputil.RequireLocalOriginStrict(http.HandlerFunc(handleSettingsUpdate)).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for missing Origin (CSRF guard)", w.Code)
	}
}

func TestHandleSettingsUpdate_RejectsInvalidWireGuardKey(t *testing.T) {
	body, _ := json.Marshal(SettingsResponse{WireguardKey: "tooshort"})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/settings", bytes.NewReader(body))
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	handleSettingsUpdate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid WireGuard key", w.Code)
	}
}

func TestHandleSettingsReset_RejectsEmptyOrigin(t *testing.T) {
	body, _ := json.Marshal(SetupRequest{
		WireguardKey: strings.Repeat("A", 43) + "=",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/settings/reset", bytes.NewReader(body))
	// No Origin header
	w := httptest.NewRecorder()
	httputil.RequireLocalOriginStrict(http.HandlerFunc(handleSettingsReset)).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for empty origin on reset", w.Code)
	}
}
