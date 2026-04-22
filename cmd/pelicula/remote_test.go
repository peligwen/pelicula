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
		"REMOTE_ACCESS_ENABLED": "true",
		"REMOTE_HOSTNAME":       "",
		"CONFIG_DIR":            configDir,
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
		"REMOTE_ACCESS_ENABLED": "true",
		"REMOTE_HOSTNAME":       "test.example.com",
		"REMOTE_CERT_MODE":      "self-signed",
		"CONFIG_DIR":            configDir,
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
