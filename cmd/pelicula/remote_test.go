package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupRemoteScriptDir creates a temp scriptDir with the nginx template files
// and compose subdirectory that RenderRemoteConfigs expects.
func setupRemoteScriptDir(t *testing.T) string {
	t.Helper()
	scriptDir := t.TempDir()

	// Create nginx subdirectory
	nginxDir := filepath.Join(scriptDir, "nginx")
	if err := os.MkdirAll(nginxDir, 0755); err != nil {
		t.Fatalf("create nginx dir: %v", err)
	}

	// Create compose subdirectory (RenderRemoteConfigs writes compose overlay here)
	composeDir := filepath.Join(scriptDir, "compose")
	if err := os.MkdirAll(composeDir, 0755); err != nil {
		t.Fatalf("create compose dir: %v", err)
	}

	// Create nginx/snippets subdirectory
	snippetsDir := filepath.Join(nginxDir, "snippets")
	if err := os.MkdirAll(snippetsDir, 0755); err != nil {
		t.Fatalf("create snippets dir: %v", err)
	}

	// Copy real template files from repo root
	repoRoot := "../../"

	simpleSrc := filepath.Join(repoRoot, "nginx", "remote-simple.conf.template")
	simpleBytes, err := os.ReadFile(simpleSrc)
	if err != nil {
		t.Fatalf("read remote-simple.conf.template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nginxDir, "remote-simple.conf.template"), simpleBytes, 0644); err != nil {
		t.Fatalf("write remote-simple.conf.template: %v", err)
	}

	fullSrc := filepath.Join(repoRoot, "nginx", "remote.conf.template")
	fullBytes, err := os.ReadFile(fullSrc)
	if err != nil {
		t.Fatalf("read remote.conf.template: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nginxDir, "remote.conf.template"), fullBytes, 0644); err != nil {
		t.Fatalf("write remote.conf.template: %v", err)
	}

	snippetSrc := filepath.Join(repoRoot, "nginx", "snippets", "jellyfin-proxy.conf")
	snippetBytes, err := os.ReadFile(snippetSrc)
	if err != nil {
		t.Fatalf("read nginx/snippets/jellyfin-proxy.conf: %v", err)
	}
	if err := os.WriteFile(filepath.Join(snippetsDir, "jellyfin-proxy.conf"), snippetBytes, 0644); err != nil {
		t.Fatalf("write jellyfin-proxy.conf snippet: %v", err)
	}

	return scriptDir
}

func TestRenderRemoteConfigs_SimpleMode(t *testing.T) {
	configDir := t.TempDir()
	scriptDir := setupRemoteScriptDir(t)

	env := EnvMap{
		"REMOTE_MODE":     "portforward",
		"REMOTE_HOSTNAME": "",
		"CONFIG_DIR":      configDir,
	}

	if err := RenderRemoteConfigs(scriptDir, env); err != nil {
		t.Fatalf("RenderRemoteConfigs error: %v", err)
	}

	// Read rendered remote.conf
	remoteConf := filepath.Join(configDir, "nginx", "remote.conf")
	data, err := os.ReadFile(remoteConf)
	if err != nil {
		t.Fatalf("read remote.conf: %v", err)
	}
	conf := string(data)

	// Assert simple-mode nginx config contents
	if !strings.Contains(conf, "server_name _;") {
		t.Error("remote.conf should contain 'server_name _;' in simple mode")
	}
	if !strings.Contains(conf, "include /etc/nginx/snippets/jellyfin-proxy.conf;") {
		t.Error("remote.conf should include jellyfin-proxy.conf snippet")
	}
	if strings.Contains(conf, "add_header Strict-Transport-Security") {
		t.Error("remote.conf must NOT contain Strict-Transport-Security add_header directive in simple mode")
	}
	if strings.Contains(conf, "listen 80") {
		t.Error("remote.conf must NOT contain 'listen 80' in simple mode")
	}
	if !strings.Contains(conf, "Content-Security-Policy") {
		t.Error("remote.conf should contain Content-Security-Policy header in simple mode")
	}

	// Assert self-signed certs were generated
	certDir := filepath.Join(configDir, "certs", "remote")
	if _, err := os.Stat(filepath.Join(certDir, "fullchain.pem")); err != nil {
		t.Error("fullchain.pem should exist after simple mode render")
	}
	if _, err := os.Stat(filepath.Join(certDir, "privkey.pem")); err != nil {
		t.Error("privkey.pem should exist after simple mode render")
	}

	// Read compose overlay and check simple-mode port bindings
	composePath := filepath.Join(scriptDir, "compose", "docker-compose.remote.yml")
	composeData, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read docker-compose.remote.yml: %v", err)
	}
	compose := string(composeData)

	if !strings.Contains(compose, "8443") {
		t.Error("compose overlay should contain HTTPS port mapping (8443)")
	}
	if strings.Contains(compose, `:80"`) {
		t.Error("compose overlay must NOT contain HTTP port mapping in simple mode")
	}
	if strings.Contains(compose, "certbot") {
		t.Error("compose overlay must NOT contain certbot service in simple mode")
	}
}

func TestRenderRemoteConfigs_FullMode(t *testing.T) {
	configDir := t.TempDir()
	scriptDir := setupRemoteScriptDir(t)

	env := EnvMap{
		"REMOTE_MODE":      "portforward",
		"REMOTE_HOSTNAME":  "test.example.com",
		"REMOTE_CERT_MODE": "self-signed",
		"CONFIG_DIR":       configDir,
	}

	if err := RenderRemoteConfigs(scriptDir, env); err != nil {
		t.Fatalf("RenderRemoteConfigs error: %v", err)
	}

	// Read rendered remote.conf
	remoteConf := filepath.Join(configDir, "nginx", "remote.conf")
	data, err := os.ReadFile(remoteConf)
	if err != nil {
		t.Fatalf("read remote.conf: %v", err)
	}
	conf := string(data)

	// Assert full-mode nginx config contents
	if !strings.Contains(conf, "test.example.com") {
		t.Error("remote.conf should contain the hostname in full mode")
	}
	if strings.Contains(conf, "server_name _;") {
		// Allow catch-all server blocks but the main vhost must have the real hostname
		// The full mode template has server_name _; only in catch-all reject blocks,
		// but the main Jellyfin vhost must have the real hostname.
		// Check that the main vhost block specifically has the hostname.
		if !strings.Contains(conf, "server_name test.example.com;") {
			t.Error("remote.conf main vhost should have server_name test.example.com; in full mode")
		}
	}
	if !strings.Contains(conf, "listen 80") {
		t.Error("remote.conf should contain 'listen 80' in full mode (HTTP redirect block)")
	}
	if !strings.Contains(conf, "add_header Strict-Transport-Security") {
		t.Error("remote.conf should contain HSTS add_header directive in full mode")
	}
	if !strings.Contains(conf, "includeSubDomains") {
		t.Error("remote.conf HSTS header should contain includeSubDomains in full mode")
	}
	if !strings.Contains(conf, "Content-Security-Policy") {
		t.Error("remote.conf should contain Content-Security-Policy header in full mode")
	}
	if !strings.Contains(conf, "include /etc/nginx/snippets/jellyfin-proxy.conf;") {
		t.Error("remote.conf should include jellyfin-proxy.conf snippet in full mode")
	}

	// Read compose overlay and check full-mode port bindings
	composePath := filepath.Join(scriptDir, "compose", "docker-compose.remote.yml")
	composeData, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read docker-compose.remote.yml: %v", err)
	}
	compose := string(composeData)

	if !strings.Contains(compose, `:80"`) {
		t.Error("compose overlay should contain HTTP port mapping in full mode")
	}
	if !strings.Contains(compose, "8443") {
		t.Error("compose overlay should contain HTTPS port mapping (8443)")
	}
}

// TestRenderRemoteConfigs_Disabled verifies that when REMOTE_MODE is "disabled"
// (or absent), RenderRemoteConfigs writes a placeholder comment to remote.conf
// and removes any existing docker-compose.remote.yml.
func TestRenderRemoteConfigs_Disabled(t *testing.T) {
	configDir := t.TempDir()
	scriptDir := setupRemoteScriptDir(t)

	env := EnvMap{
		"REMOTE_MODE": "disabled",
		"CONFIG_DIR":  configDir,
	}

	if err := RenderRemoteConfigs(scriptDir, env); err != nil {
		t.Fatalf("RenderRemoteConfigs error: %v", err)
	}

	remoteConf := filepath.Join(configDir, "nginx", "remote.conf")
	data, err := os.ReadFile(remoteConf)
	if err != nil {
		t.Fatalf("read remote.conf: %v", err)
	}
	conf := string(data)
	if !strings.Contains(conf, "disabled") && !strings.Contains(conf, "#") {
		t.Error("disabled mode should write a placeholder comment to remote.conf")
	}

	// docker-compose.remote.yml must not exist (or was removed).
	composePath := filepath.Join(scriptDir, "compose", "docker-compose.remote.yml")
	if _, err := os.Stat(composePath); err == nil {
		t.Error("docker-compose.remote.yml must not exist when remote access is disabled")
	}
}

// TestRenderRemoteConfigs_LetsEncrypt verifies full mode with Let's Encrypt:
// the rendered nginx config must include the ACME challenge location and the
// compose overlay must include certbot configuration.
func TestRenderRemoteConfigs_LetsEncrypt(t *testing.T) {
	configDir := t.TempDir()
	scriptDir := setupRemoteScriptDir(t)

	env := EnvMap{
		"REMOTE_MODE":      "portforward",
		"REMOTE_HOSTNAME":  "example.pelicula.io",
		"REMOTE_CERT_MODE": "letsencrypt",
		"REMOTE_LE_EMAIL":  "admin@example.com",
		"CONFIG_DIR":       configDir,
	}

	if err := RenderRemoteConfigs(scriptDir, env); err != nil {
		t.Fatalf("RenderRemoteConfigs error: %v", err)
	}

	// nginx config should contain the ACME challenge location.
	remoteConf := filepath.Join(configDir, "nginx", "remote.conf")
	data, err := os.ReadFile(remoteConf)
	if err != nil {
		t.Fatalf("read remote.conf: %v", err)
	}
	conf := string(data)
	if !strings.Contains(conf, "acme-challenge") && !strings.Contains(conf, ".well-known") {
		t.Error("letsencrypt mode: remote.conf should contain ACME challenge location")
	}
	if !strings.Contains(conf, "ssl_certificate") {
		t.Error("letsencrypt mode: remote.conf should reference ssl_certificate")
	}

	// Compose overlay should contain certbot service.
	composePath := filepath.Join(scriptDir, "compose", "docker-compose.remote.yml")
	composeData, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read docker-compose.remote.yml: %v", err)
	}
	compose := string(composeData)
	if !strings.Contains(compose, "certbot") {
		t.Error("letsencrypt mode: compose overlay should contain certbot service")
	}
	if !strings.Contains(compose, "admin@example.com") {
		t.Error("letsencrypt mode: compose overlay should contain the LE email address")
	}
}

// TestRenderRemoteConfigs_BYO verifies full mode with bring-your-own certificate:
// when fullchain.pem already exists in the cert dir, RenderRemoteConfigs must
// succeed and the rendered config must reference the cert path.
func TestRenderRemoteConfigs_BYO(t *testing.T) {
	configDir := t.TempDir()
	scriptDir := setupRemoteScriptDir(t)

	// Pre-create cert files to satisfy the BYO check.
	certDir := filepath.Join(configDir, "certs", "remote")
	if err := os.MkdirAll(certDir, 0755); err != nil {
		t.Fatalf("create certDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(certDir, "fullchain.pem"), []byte("FAKE CERT"), 0644); err != nil {
		t.Fatalf("write fullchain.pem: %v", err)
	}
	if err := os.WriteFile(filepath.Join(certDir, "privkey.pem"), []byte("FAKE KEY"), 0644); err != nil {
		t.Fatalf("write privkey.pem: %v", err)
	}

	env := EnvMap{
		"REMOTE_MODE":      "portforward",
		"REMOTE_HOSTNAME":  "byo.example.com",
		"REMOTE_CERT_MODE": "byo",
		"CONFIG_DIR":       configDir,
	}

	if err := RenderRemoteConfigs(scriptDir, env); err != nil {
		t.Fatalf("RenderRemoteConfigs error: %v", err)
	}

	// The nginx config must reference the cert path (same path used for all modes).
	remoteConf := filepath.Join(configDir, "nginx", "remote.conf")
	data, err := os.ReadFile(remoteConf)
	if err != nil {
		t.Fatalf("read remote.conf: %v", err)
	}
	if !strings.Contains(string(data), "ssl_certificate") {
		t.Error("BYO mode: remote.conf should reference ssl_certificate path")
	}
	if !strings.Contains(string(data), "byo.example.com") {
		t.Error("BYO mode: remote.conf should contain the hostname")
	}
}

// TestRenderRemoteConfigs_InvalidHostname verifies that a hostname containing
// spaces or special characters causes RenderRemoteConfigs to return an error
// rather than writing an invalid nginx config.
func TestRenderRemoteConfigs_InvalidHostname(t *testing.T) {
	configDir := t.TempDir()
	scriptDir := setupRemoteScriptDir(t)

	env := EnvMap{
		"REMOTE_MODE":      "portforward",
		"REMOTE_HOSTNAME":  "bad hostname with spaces",
		"REMOTE_CERT_MODE": "self-signed",
		"CONFIG_DIR":       configDir,
	}

	err := RenderRemoteConfigs(scriptDir, env)
	if err == nil {
		t.Fatal("expected an error for hostname with spaces, got nil")
	}
	if !strings.Contains(err.Error(), "valid hostname") && !strings.Contains(err.Error(), "REMOTE_HOSTNAME") {
		t.Errorf("error message should mention hostname validation, got: %v", err)
	}
}

// TestRenderRemoteConfigs_EmptyEmail verifies that Let's Encrypt mode with an
// empty REMOTE_LE_EMAIL returns a validation error before any cert provisioning.
func TestRenderRemoteConfigs_EmptyEmail(t *testing.T) {
	configDir := t.TempDir()
	scriptDir := setupRemoteScriptDir(t)

	env := EnvMap{
		"REMOTE_MODE":      "portforward",
		"REMOTE_HOSTNAME":  "example.pelicula.io",
		"REMOTE_CERT_MODE": "letsencrypt",
		"REMOTE_LE_EMAIL":  "", // intentionally empty
		"CONFIG_DIR":       configDir,
	}

	err := RenderRemoteConfigs(scriptDir, env)
	if err == nil {
		t.Fatal("expected an error for empty LE email, got nil")
	}
	if !strings.Contains(err.Error(), "REMOTE_LE_EMAIL") && !strings.Contains(err.Error(), "email") {
		t.Errorf("error message should mention email, got: %v", err)
	}
}
