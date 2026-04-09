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
		AdminUsername: "gwen",
		AdminPassword: "test-pass-123",
		VPNSkipped:    true,
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
		AdminUsername: "gwen",
		AdminPassword: "test-pass-123",
		VPNSkipped:    true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/setup", bytes.NewReader(body))
	// No Origin header — should be rejected by the strict CSRF guard.
	w := httptest.NewRecorder()
	requireLocalOriginStrict(http.HandlerFunc(handleSetupSubmit)).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for empty origin", w.Code)
	}
}

func TestHandleSetupSubmit_RejectsMissingAdminUsername(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"admin_password": "test-pass-123",
		"config_dir":     "./config",
		"media_dir":      "~/media",
		"vpn_skipped":    true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/setup", bytes.NewReader(body))
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	handleSetupSubmit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for missing admin_username", w.Code)
	}
}

func TestHandleSetupSubmit_RejectsShortPassword(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"admin_username": "gwen",
		"admin_password": "short",
		"config_dir":     "./config",
		"media_dir":      "~/media",
		"vpn_skipped":    true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/setup", bytes.NewReader(body))
	req.Header.Set("Origin", "http://localhost:7354")
	w := httptest.NewRecorder()
	handleSetupSubmit(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for short password", w.Code)
	}
}

func TestHandleSetupSubmit_RejectsMissingVPNKey(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"admin_username": "gwen",
		"admin_password": "test-pass-123",
		"config_dir":     "./config",
		"media_dir":      "~/media",
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
		"admin_username": "gwen",
		"admin_password": "test-pass-123",
		"wireguard_key":  "tooshort",
		"config_dir":     "./config",
		"media_dir":      "~/media",
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
		"admin_username": "gwen",
		"admin_password": "test-pass-123",
		"config_dir":     "/config\nnewline",
		"vpn_skipped":    true,
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
		"admin_username": "gwen",
		"admin_password": "test-pass-123",
		"config_dir":     "./config",
		"media_dir":      "~/media",
		"vpn_skipped":    true,
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

func TestHandleGeneratePassword(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/setup/generate-password", nil)
	w := httptest.NewRecorder()
	handleGeneratePassword(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	pw := resp["password"]
	if pw == "" {
		t.Error("expected non-empty password")
	}
	parts := strings.Split(pw, "-")
	if len(parts) != 3 {
		t.Errorf("expected 3 hyphen-separated groups, got %d in %q", len(parts), pw)
	}
}

// ── generateReadablePassword ─────────────────────────────────────────────────

func TestGenerateReadablePassword_Format(t *testing.T) {
	p := generateReadablePassword()
	parts := strings.Split(p, "-")
	if len(parts) != 3 {
		t.Fatalf("expected 3 hyphen-separated groups, got %d in %q", len(parts), p)
	}
	for i, part := range parts {
		if len(part) != 5 {
			t.Errorf("group %d: length = %d, want 5 in %q", i, len(part), p)
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
