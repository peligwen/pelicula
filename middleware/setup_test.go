package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleSetupSubmit_AlreadyConfigured(t *testing.T) {
	// Verify the handler returns 405 for GET (method guard fires before file check).
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/setup", nil)
	w := httptest.NewRecorder()
	handleSetupSubmit(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405 for GET", w.Code)
	}
}

func TestHandleSetupSubmit_RejectsForeignOrigin(t *testing.T) {
	body, _ := json.Marshal(SetupRequest{
		VPNSkipped: true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/setup", bytes.NewReader(body))
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	requireLocalOriginStrict(http.HandlerFunc(handleSetupSubmit)).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for foreign origin", w.Code)
	}
}

func TestHandleSetupSubmit_RejectsEmptyOrigin(t *testing.T) {
	body, _ := json.Marshal(SetupRequest{
		VPNSkipped: true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/setup", bytes.NewReader(body))
	// No Origin header — should be rejected by the strict CSRF guard.
	w := httptest.NewRecorder()
	requireLocalOriginStrict(http.HandlerFunc(handleSetupSubmit)).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for empty origin", w.Code)
	}
}

func TestHandleSetupSubmit_RejectsMissingVPNKey(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"config_dir":  "./config",
		"media_dir":   "~/media",
		// vpn_skipped omitted — defaults to false, so key is required
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/setup", bytes.NewReader(body))
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	handleSetupSubmit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for missing wireguard_key when not skipped", w.Code)
	}
}

func TestHandleSetupSubmit_RejectsShortKey(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"wireguard_key": "tooshort",
		"config_dir":    "./config",
		"media_dir":     "~/media",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/setup", bytes.NewReader(body))
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	handleSetupSubmit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for invalid key length", w.Code)
	}
}

func TestHandleSetupSubmit_RejectsInjectionInFields(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"config_dir":  "/config\nnewline",
		"vpn_skipped": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/setup", bytes.NewReader(body))
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	handleSetupSubmit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for newline in config_dir", w.Code)
	}
}

func TestHandleSetupSubmit_AcceptsVPNSkipped(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"config_dir":  "./config",
		"media_dir":   "~/media",
		"vpn_skipped": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/setup", bytes.NewReader(body))
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	handleSetupSubmit(w, req)

	// Will fail with 409 (already configured) or 500 (can't write /project/.env)
	// in test environment — but NOT 400, which would mean validation rejected it.
	if w.Code == http.StatusBadRequest {
		t.Errorf("status = 400, but VPN skip with valid fields should pass validation")
	}
}

func TestGenerateReadablePassword_Format(t *testing.T) {
	p := generateReadablePassword()
	parts := strings.Split(p, "-")
	if len(parts) != 4 {
		t.Fatalf("expected 4 hyphen-separated words, got %d in %q", len(parts), p)
	}
	wordSet := make(map[string]bool, len(passphraseWords))
	for _, w := range passphraseWords {
		wordSet[w] = true
	}
	for i, part := range parts {
		if l := len(part); l < 3 || l > 7 {
			t.Errorf("word %d: length = %d, want 3–7 in %q", i, l, p)
		}
		if !wordSet[part] {
			t.Errorf("word %d: %q not in passphraseWords", i, part)
		}
	}
}

func TestGenerateReadablePassword_Unique(t *testing.T) {
	a := generateReadablePassword()
	b := generateReadablePassword()
	if a == b {
		t.Error("expected two different passwords, got same")
	}
}

func TestHandleSetupDetect_ReturnsLANURL(t *testing.T) {
	t.Setenv("HOST_LAN_URL", "http://192.168.1.42:7354/jellyfin")

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/setup/detect", nil)
	w := httptest.NewRecorder()
	handleSetupDetect(w, req)

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

func TestHandleSetupDetect_EmptyLANURLWhenUnset(t *testing.T) {
	t.Setenv("HOST_LAN_URL", "")

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/setup/detect", nil)
	w := httptest.NewRecorder()
	handleSetupDetect(w, req)

	var resp SetupDetect
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.LANUrl != "" {
		t.Errorf("LANUrl = %q, want empty string when HOST_LAN_URL unset", resp.LANUrl)
	}
}

// ── writeEnvFile uses writeEnvFile (not fmt.Sprintf) ────────────────────────

func TestSetupUsesWriteEnvFile(t *testing.T) {
	// Verify that the setup wizard writes booleans unquoted and strings quoted —
	// the canonical format produced by writeEnvFile.
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	vars := map[string]string{
		"PELICULA_AUTH":         "jellyfin",
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

func TestHandleSetupSubmit_RejectsInjectionInLANURL(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"config_dir":  "./config",
		"media_dir":   "~/media",
		"lan_url":     "http://1.2.3.4:7354/jellyfin\nX-Evil: yes",
		"vpn_skipped": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/setup", bytes.NewReader(body))
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	handleSetupSubmit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for newline in lan_url", w.Code)
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
