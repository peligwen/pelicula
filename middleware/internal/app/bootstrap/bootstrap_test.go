package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pelicula-api/internal/app/settings"
)

// TestEnsureWebhookSecret_GeneratesWhenAbsent verifies that a non-empty hex
// secret is written to a tmp .env when WEBHOOK_SECRET is unset.
func TestEnsureWebhookSecret_GeneratesWhenAbsent(t *testing.T) {
	t.Setenv("WEBHOOK_SECRET", "")

	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")

	// Write a minimal .env without WEBHOOK_SECRET.
	if err := os.WriteFile(envFile, []byte("CONFIG_DIR=/config\n"), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := ensureWebhookSecret(envFile); err != nil {
		t.Fatalf("ensureWebhookSecret: %v", err)
	}

	// Process env must be set.
	secret := os.Getenv("WEBHOOK_SECRET")
	if secret == "" {
		t.Fatal("WEBHOOK_SECRET not set in process env after ensureWebhookSecret")
	}

	// Must be non-empty hex (64 chars for 32 bytes).
	if len(secret) != 64 {
		t.Errorf("WEBHOOK_SECRET length = %d, want 64 (32 hex-encoded bytes)", len(secret))
	}

	// Must be written back to the .env file.
	vars, err := settings.ParseEnvFile(envFile)
	if err != nil {
		t.Fatalf("ParseEnvFile: %v", err)
	}
	if got := strings.TrimSpace(vars["WEBHOOK_SECRET"]); got != secret {
		t.Errorf("file WEBHOOK_SECRET = %q, want %q", got, secret)
	}
}

// TestEnsureWebhookSecret_LeavesExistingUntouched verifies that a pre-set
// WEBHOOK_SECRET value in .env is not overwritten.
func TestEnsureWebhookSecret_LeavesExistingUntouched(t *testing.T) {
	const existing = "pre-existing-webhook-secret-value"
	t.Setenv("WEBHOOK_SECRET", "")

	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")

	if err := os.WriteFile(envFile, []byte("CONFIG_DIR=/config\nWEBHOOK_SECRET="+existing+"\n"), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := ensureWebhookSecret(envFile); err != nil {
		t.Fatalf("ensureWebhookSecret: %v", err)
	}

	// Process env should contain the original value.
	if got := os.Getenv("WEBHOOK_SECRET"); got != existing {
		t.Errorf("WEBHOOK_SECRET = %q, want %q", got, existing)
	}

	// File should still contain the original value.
	vars, err := settings.ParseEnvFile(envFile)
	if err != nil {
		t.Fatalf("ParseEnvFile: %v", err)
	}
	if got := vars["WEBHOOK_SECRET"]; got != existing {
		t.Errorf("file WEBHOOK_SECRET = %q, want %q", got, existing)
	}
}

// TestEnsureWebhookSecret_EnvVarAlreadySet verifies that when WEBHOOK_SECRET
// is already set in the process env, ensureWebhookSecret is a no-op.
func TestEnsureWebhookSecret_EnvVarAlreadySet(t *testing.T) {
	const preset = "already-set-secret"
	t.Setenv("WEBHOOK_SECRET", preset)

	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	// Write a .env without WEBHOOK_SECRET to confirm file is not touched.
	if err := os.WriteFile(envFile, []byte("CONFIG_DIR=/config\n"), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := ensureWebhookSecret(envFile); err != nil {
		t.Fatalf("ensureWebhookSecret: %v", err)
	}

	// Process env unchanged.
	if got := os.Getenv("WEBHOOK_SECRET"); got != preset {
		t.Errorf("WEBHOOK_SECRET changed to %q, want %q", got, preset)
	}

	// File should not have gained a WEBHOOK_SECRET entry.
	vars, err := settings.ParseEnvFile(envFile)
	if err != nil {
		t.Fatalf("ParseEnvFile: %v", err)
	}
	if val := vars["WEBHOOK_SECRET"]; val != "" {
		t.Errorf("file WEBHOOK_SECRET = %q, want empty (file should not be modified)", val)
	}
}
