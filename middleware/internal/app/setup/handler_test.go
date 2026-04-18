package setup

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pelicula-api/httputil"
)

// stubGenerateAPIKey returns a fixed string for deterministic tests.
func stubGenerateAPIKey() string { return "testapikey1234567890abcdef123456" }

// stubGenPassword returns a fixed password for deterministic tests.
func stubGenPassword() string { return "calm-tiger-sobre-leaps" }

func newTestHandler(t *testing.T) (*Handler, string) {
	t.Helper()
	dir := t.TempDir()
	envPath := filepath.Join(dir, ".env")
	return New(envPath, stubGenerateAPIKey, stubGenPassword), envPath
}

func TestHandleSubmit_RejectsGET(t *testing.T) {
	h, _ := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/setup", nil)
	w := httptest.NewRecorder()
	h.HandleSubmit(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 for GET", w.Code)
	}
}

func TestHandleSubmit_RejectsForeignOrigin(t *testing.T) {
	h, _ := newTestHandler(t)
	body, _ := json.Marshal(SetupRequest{VPNSkipped: true})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/setup", bytes.NewReader(body))
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	httputil.RequireLocalOriginStrict(http.HandlerFunc(h.HandleSubmit)).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for foreign origin", w.Code)
	}
}

func TestHandleSubmit_RejectsEmptyOrigin(t *testing.T) {
	h, _ := newTestHandler(t)
	body, _ := json.Marshal(SetupRequest{VPNSkipped: true})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/setup", bytes.NewReader(body))
	// No Origin header — should be rejected by the strict CSRF guard.
	w := httptest.NewRecorder()
	httputil.RequireLocalOriginStrict(http.HandlerFunc(h.HandleSubmit)).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for empty origin", w.Code)
	}
}

func TestHandleSubmit_RejectsMissingVPNKey(t *testing.T) {
	h, _ := newTestHandler(t)
	body, _ := json.Marshal(map[string]any{
		"config_dir": "./config",
		"media_dir":  "~/media",
		// vpn_skipped omitted — defaults to false, so key is required
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/setup", bytes.NewReader(body))
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	h.HandleSubmit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for missing wireguard_key when not skipped", w.Code)
	}
}

func TestHandleSubmit_RejectsShortKey(t *testing.T) {
	h, _ := newTestHandler(t)
	body, _ := json.Marshal(map[string]any{
		"wireguard_key": "tooshort",
		"config_dir":    "./config",
		"media_dir":     "~/media",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/setup", bytes.NewReader(body))
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	h.HandleSubmit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid key length", w.Code)
	}
}

func TestHandleSubmit_RejectsInjectionInFields(t *testing.T) {
	h, _ := newTestHandler(t)
	body, _ := json.Marshal(map[string]any{
		"config_dir":  "/config\nnewline",
		"vpn_skipped": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/setup", bytes.NewReader(body))
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	h.HandleSubmit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for newline in config_dir", w.Code)
	}
}

func TestHandleSubmit_AcceptsVPNSkipped(t *testing.T) {
	h, _ := newTestHandler(t)
	body, _ := json.Marshal(map[string]any{
		"config_dir":  "./config",
		"media_dir":   "~/media",
		"vpn_skipped": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/setup", bytes.NewReader(body))
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	h.HandleSubmit(w, req)

	// Will succeed (200) since the temp dir .env doesn't exist yet and can be written.
	// Must NOT be 400 (validation rejection).
	if w.Code == http.StatusBadRequest {
		t.Errorf("status = 400, but VPN skip with valid fields should pass validation")
	}
}

func TestHandleSubmit_RejectsInjectionInLANURL(t *testing.T) {
	h, _ := newTestHandler(t)
	body, _ := json.Marshal(map[string]any{
		"config_dir":  "./config",
		"media_dir":   "~/media",
		"lan_url":     "http://1.2.3.4:7354/jellyfin\nX-Evil: yes",
		"vpn_skipped": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/setup", bytes.NewReader(body))
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	h.HandleSubmit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for newline in lan_url", w.Code)
	}
}

func TestHandleSubmit_AlreadyConfigured(t *testing.T) {
	h, envPath := newTestHandler(t)
	// Pre-create the .env so the handler sees it as already configured.
	if err := os.WriteFile(envPath, []byte("# existing\n"), 0600); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(SetupRequest{VPNSkipped: true})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/setup", bytes.NewReader(body))
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	h.HandleSubmit(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 when .env already exists", w.Code)
	}
}

func TestHandleDetect_ReturnsLANURL(t *testing.T) {
	h, _ := newTestHandler(t)
	t.Setenv("HOST_LAN_URL", "http://192.168.1.42:7354/jellyfin")

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/setup/detect", nil)
	w := httptest.NewRecorder()
	h.HandleDetect(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp SetupDetect
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.LANUrl != "http://192.168.1.42:7354/jellyfin" {
		t.Errorf("LANUrl = %q, want http://192.168.1.42:7354/jellyfin", resp.LANUrl)
	}
}

func TestHandleDetect_EmptyLANURLWhenUnset(t *testing.T) {
	h, _ := newTestHandler(t)
	t.Setenv("HOST_LAN_URL", "")

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/setup/detect", nil)
	w := httptest.NewRecorder()
	h.HandleDetect(w, req)

	var resp SetupDetect
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.LANUrl != "" {
		t.Errorf("LANUrl = %q, want empty string when HOST_LAN_URL unset", resp.LANUrl)
	}
}

func TestWriteEnvFile_BooleanAndStringFormatting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	vars := map[string]string{
		"TRANSCODING_ENABLED":   "false",
		"NOTIFICATIONS_ENABLED": "false",
		"CONFIG_DIR":            "/config",
	}
	if err := writeEnvFile(path, vars); err != nil {
		t.Fatalf("writeEnvFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)

	// Booleans must be unquoted
	if !strings.Contains(content, "TRANSCODING_ENABLED=false\n") {
		t.Error("expected TRANSCODING_ENABLED=false (unquoted)")
	}
	// Strings must be quoted
	if !strings.Contains(content, `CONFIG_DIR="/config"`) {
		t.Error(`expected CONFIG_DIR="/config" (quoted)`)
	}
}

func TestSetupRequest_DecodesLANURL(t *testing.T) {
	raw := `{"lan_url":"http://192.168.1.42:7354/jellyfin","vpn_skipped":true}`
	var req SetupRequest
	if err := json.NewDecoder(strings.NewReader(raw)).Decode(&req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if req.LANUrl != "http://192.168.1.42:7354/jellyfin" {
		t.Errorf("LANUrl = %q, want http://192.168.1.42:7354/jellyfin", req.LANUrl)
	}
}

func TestWriteEnvFile_IncludesJellyfinPublishedURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	vars := map[string]string{
		"CONFIG_DIR":             "/config",
		"JELLYFIN_PUBLISHED_URL": "http://192.168.1.42:7354/jellyfin",
	}
	if err := writeEnvFile(path, vars); err != nil {
		t.Fatalf("writeEnvFile: %v", err)
	}
	data, _ := os.ReadFile(path)
	want := `JELLYFIN_PUBLISHED_URL="http://192.168.1.42:7354/jellyfin"`
	if !strings.Contains(string(data), want) {
		t.Errorf("env file missing %s\n got: %s", want, string(data))
	}
}

func TestWriteEnvFile_OmitsEmptyJellyfinPublishedURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	vars := map[string]string{
		"CONFIG_DIR": "/config",
	}
	if err := writeEnvFile(path, vars); err != nil {
		t.Fatalf("writeEnvFile: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "JELLYFIN_PUBLISHED_URL") {
		t.Errorf("env file should not mention JELLYFIN_PUBLISHED_URL when absent\n got: %s", string(data))
	}
}

func TestNeedsSetup_True(t *testing.T) {
	t.Setenv("SETUP_MODE", "true")
	if !NeedsSetup() {
		t.Error("NeedsSetup() = false, want true when SETUP_MODE=true")
	}
}

func TestNeedsSetup_False(t *testing.T) {
	t.Setenv("SETUP_MODE", "")
	if NeedsSetup() {
		t.Error("NeedsSetup() = true, want false when SETUP_MODE unset")
	}
}
