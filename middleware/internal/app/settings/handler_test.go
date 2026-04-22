package settings

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

	appsetup "pelicula-api/internal/app/setup"
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

	if err := WriteEnvFile(path, in); err != nil {
		t.Fatalf("writeEnvFile: %v", err)
	}

	got, err := ParseEnvFile(path)
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
	got, err := ParseEnvFile(path)
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
	got, err := ParseEnvFile(path)
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
	_, err := ParseEnvFile("/nonexistent/.env")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// ── HandleSettings input validation ──────────────────────────────────────────

func newSettingsEnv(t *testing.T) (string, *Handler) {
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
	if err := WriteEnvFile(path, vars); err != nil {
		t.Fatalf("write test .env: %v", err)
	}
	h := New(path, func() string { return "testkey" })
	return path, h
}

func TestHandleSettingsUpdate_RejectsInvalidCharacters(t *testing.T) {
	_, h := newSettingsEnv(t)

	body, _ := json.Marshal(settingsResponse{Country: "bad\ncountry"})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/settings", bytes.NewReader(body))
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	h.handleSettingsUpdate(w, req)

	// The handler should reject the invalid character with 400.
	if w.Code == http.StatusOK {
		t.Error("expected non-200 for country containing newline")
	}
	if w.Code == http.StatusForbidden {
		t.Error("should not be forbidden — origin is localhost")
	}
}

func TestHandleSettingsUpdate_RejectsForeignOrigin(t *testing.T) {
	_, h := newSettingsEnv(t)

	body, _ := json.Marshal(settingsResponse{Country: "Netherlands"})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/settings", bytes.NewReader(body))
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	httputil.RequireLocalOriginStrict(http.HandlerFunc(h.handleSettingsUpdate)).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for foreign origin", w.Code)
	}
}

func TestHandleSettingsUpdate_RejectsEmptyOrigin(t *testing.T) {
	// Empty Origin must be rejected by the strict CSRF guard.
	_, h := newSettingsEnv(t)

	body, _ := json.Marshal(settingsResponse{Country: "Netherlands"})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/settings", bytes.NewReader(body))
	// No Origin header set
	w := httptest.NewRecorder()
	httputil.RequireLocalOriginStrict(http.HandlerFunc(h.handleSettingsUpdate)).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for missing Origin (CSRF guard)", w.Code)
	}
}

func TestHandleSettingsUpdate_RejectsInvalidWireGuardKey(t *testing.T) {
	_, h := newSettingsEnv(t)

	body, _ := json.Marshal(settingsResponse{WireguardKey: "tooshort"})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/settings", bytes.NewReader(body))
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	h.handleSettingsUpdate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid WireGuard key", w.Code)
	}
}

func TestHandleReset_RejectsEmptyOrigin(t *testing.T) {
	_, h := newSettingsEnv(t)

	body, _ := json.Marshal(appsetup.SetupRequest{
		WireguardKey: strings.Repeat("A", 43) + "=",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/settings/reset", bytes.NewReader(body))
	// No Origin header
	w := httptest.NewRecorder()
	httputil.RequireLocalOriginStrict(http.HandlerFunc(h.HandleReset)).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for empty origin on reset", w.Code)
	}
}

// TestHandleSettings_ErrorsAreJSON verifies that error responses from the
// settings handler use Content-Type: application/json and the {"error":"..."}
// body shape (not plain text from http.Error).
func TestHandleSettings_ErrorsAreJSON(t *testing.T) {
	_, h := newSettingsEnv(t)

	// Trigger a 400 by sending a body with an invalid WireGuard key.
	body, _ := json.Marshal(settingsResponse{WireguardKey: "short"})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/settings", bytes.NewReader(body))
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	h.handleSettingsUpdate(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json (not plain text)", ct)
	}

	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp["error"] == "" {
		t.Errorf("response body = %v, want {\"error\":\"...\"}", resp)
	}
}
