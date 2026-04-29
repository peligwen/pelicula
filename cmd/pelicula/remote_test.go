package main

import (
	"os"
	"path/filepath"
	"regexp"
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

// assertSystemInfoRegexAllowsPublic checks that the rendered remote vhost denies
// /System/Info but allows /System/Info/Public — the unauthenticated endpoint
// Jellyfin clients (web + native) GET to verify a server URL during discovery
// or "Connect to Server". A too-broad regex here breaks Safari/native-client
// access on the remote vhost with "no servers available".
func assertSystemInfoRegexAllowsPublic(t *testing.T, conf string) {
	t.Helper()
	pattern := extractDenyRegex(t, conf, "Info")

	// Must deny: bare /System/Info (and /jellyfin/ prefixed variant).
	mustDeny := []string{
		"/System/Info",
		"/System/Info/",
		"/jellyfin/System/Info",
		"/jellyfin/System/Info/",
		"/system/info", // case-insensitive
	}
	mustAllow := []string{
		"/System/Info/Public",
		"/jellyfin/System/Info/Public",
		"/system/info/public",
	}
	assertRegexDenyAllow(t, pattern, mustDeny, mustAllow)
}

// assertSystemLogsRegexAnchored asserts the /System/Logs deny regex catches
// only the bare path and its trailing-slash form. An unanchored regex has
// nothing concrete to break in current Jellyfin, but it would silently swallow
// any future legitimate /System/Logs/<sub> endpoint Jellyfin adds.
func assertSystemLogsRegexAnchored(t *testing.T, conf string) {
	t.Helper()
	pattern := extractDenyRegex(t, conf, "Logs")

	mustDeny := []string{
		"/System/Logs",
		"/System/Logs/",
		"/jellyfin/System/Logs",
		"/jellyfin/System/Logs/",
		"/system/logs",
	}
	mustAllow := []string{
		"/System/Logs/Anything",
		"/jellyfin/System/Logs/Foo",
	}
	assertRegexDenyAllow(t, pattern, mustDeny, mustAllow)
}

// extractDenyRegex pulls a /System/<token> location pattern out of a rendered
// nginx config. Returns the inner regex string (without the ~* modifier).
// token is the canonical-case Jellyfin path component (e.g. "Info", "Logs");
// the matching nginx regex uses [Aa]-style case-permissive groups, so we
// build a search regex that matches that exact form.
func extractDenyRegex(t *testing.T, conf, token string) string {
	t.Helper()
	upper, lower := strings.ToUpper(token[:1]), strings.ToLower(token[:1])
	caseGroup := regexp.QuoteMeta("[" + upper + lower + "]")
	loc := regexp.MustCompile(`location\s+~\*\s+(\^[^\s{]+` + caseGroup + token[1:] + `[^\s{]*)`)
	m := loc.FindStringSubmatch(conf)
	if m == nil {
		t.Fatalf("could not find /System/%s location regex in rendered conf:\n%s", token, conf)
	}
	return m[1]
}

func assertRegexDenyAllow(t *testing.T, pattern string, mustDeny, mustAllow []string) {
	t.Helper()
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		t.Fatalf("regex %q failed to compile: %v", pattern, err)
	}
	for _, p := range mustDeny {
		if !re.MatchString(p) {
			t.Errorf("regex %q failed to match %q (should be denied)", pattern, p)
		}
	}
	for _, p := range mustAllow {
		if re.MatchString(p) {
			t.Errorf("regex %q must NOT match %q (should fall through to Jellyfin's auth)", pattern, p)
		}
	}
}

// TestRenderRemoteConfigs_HTTPSPortSubstitution checks that the rendered
// remote.conf actually substitutes the configured REMOTE_HTTPS_PORT into the
// HTTP→HTTPS redirect target, instead of leaving the literal placeholder
// behind. Regression value: an envsubst-style template with the wrong
// quoting can pass through as text and produce a redirect to
// `https://host:${REMOTE_HTTPS_PORT}/...` that browsers cannot resolve.
func TestRenderRemoteConfigs_HTTPSPortSubstitution(t *testing.T) {
	configDir := t.TempDir()
	scriptDir := setupRemoteScriptDir(t)
	env := EnvMap{
		"REMOTE_MODE":       "portforward",
		"REMOTE_HOSTNAME":   "test.example.com",
		"REMOTE_CERT_MODE":  "self-signed",
		"REMOTE_HTTPS_PORT": "9443",
		"CONFIG_DIR":        configDir,
	}
	if err := RenderRemoteConfigs(scriptDir, env); err != nil {
		t.Fatalf("RenderRemoteConfigs: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(configDir, "nginx", "remote.conf"))
	if err != nil {
		t.Fatalf("read remote.conf: %v", err)
	}
	conf := string(data)
	if strings.Contains(conf, "${REMOTE_HTTPS_PORT}") {
		t.Errorf("remote.conf still contains literal ${REMOTE_HTTPS_PORT} placeholder:\n%s", conf)
	}
	if !strings.Contains(conf, "https://$host:9443") {
		t.Errorf("remote.conf should redirect to https://$host:9443; got:\n%s", conf)
	}
}

// TestRenderRemoteConfigs_NoAdminPaths verifies the load-bearing Peligrosa
// invariant: neither remote vhost template proxies any admin route (sonarr,
// radarr, prowlarr, qbt, bazarr, /api/pelicula, /api/procula, /api/vpn). The
// remote vhost is Jellyfin-only by design — adding any of these would expose
// admin endpoints to the internet (docs/PELIGROSA.md).
func TestRenderRemoteConfigs_NoAdminPaths(t *testing.T) {
	cases := []struct {
		mode string
		env  EnvMap
	}{
		{"simple", EnvMap{
			"REMOTE_MODE":     "portforward",
			"REMOTE_HOSTNAME": "",
		}},
		{"full", EnvMap{
			"REMOTE_MODE":      "portforward",
			"REMOTE_HOSTNAME":  "test.example.com",
			"REMOTE_CERT_MODE": "self-signed",
		}},
	}
	forbidden := []string{
		"/sonarr",
		"/radarr",
		"/prowlarr",
		"/qbt",
		"/bazarr",
		"/api/pelicula",
		"/api/procula",
		"/api/vpn",
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			configDir := t.TempDir()
			scriptDir := setupRemoteScriptDir(t)
			tc.env["CONFIG_DIR"] = configDir
			if err := RenderRemoteConfigs(scriptDir, tc.env); err != nil {
				t.Fatalf("RenderRemoteConfigs: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(configDir, "nginx", "remote.conf"))
			if err != nil {
				t.Fatalf("read remote.conf: %v", err)
			}
			conf := string(data)
			for _, p := range forbidden {
				if strings.Contains(conf, "location "+p) {
					t.Errorf("remote vhost (%s) must not expose admin path %q", tc.mode, p)
				}
				if strings.Contains(conf, "proxy_pass http://"+strings.TrimPrefix(p, "/")) {
					t.Errorf("remote vhost (%s) must not proxy admin host %q", tc.mode, p)
				}
			}
		})
	}
}

// TestRenderRemoteConfigs_AuthRateLimited verifies that the bundled jellyfin
// proxy snippet attaches the `jf_auth` rate-limit zone to the AuthenticateByName
// endpoint. Without the snippet inclusion, password brute-forcing through the
// remote vhost has no rate cap.
func TestRenderRemoteConfigs_AuthRateLimited(t *testing.T) {
	cases := []struct {
		mode string
		env  EnvMap
	}{
		{"simple", EnvMap{
			"REMOTE_MODE":     "portforward",
			"REMOTE_HOSTNAME": "",
		}},
		{"full", EnvMap{
			"REMOTE_MODE":      "portforward",
			"REMOTE_HOSTNAME":  "test.example.com",
			"REMOTE_CERT_MODE": "self-signed",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			configDir := t.TempDir()
			scriptDir := setupRemoteScriptDir(t)
			tc.env["CONFIG_DIR"] = configDir
			if err := RenderRemoteConfigs(scriptDir, tc.env); err != nil {
				t.Fatalf("RenderRemoteConfigs: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(configDir, "nginx", "remote.conf"))
			if err != nil {
				t.Fatalf("read remote.conf: %v", err)
			}
			conf := string(data)
			if !strings.Contains(conf, "include /etc/nginx/snippets/jellyfin-proxy.conf;") {
				t.Errorf("remote.conf (%s) must include jellyfin-proxy snippet:\n%s", tc.mode, conf)
			}
		})
	}
	// Belt-and-suspenders: the snippet itself must reference the zone. Run
	// once on the bundled file rather than per case (the snippet is shared).
	snippet, err := os.ReadFile("../../nginx/snippets/jellyfin-proxy.conf")
	if err != nil {
		t.Fatalf("read jellyfin-proxy.conf: %v", err)
	}
	if !strings.Contains(string(snippet), "limit_req zone=jf_auth") {
		t.Errorf("jellyfin-proxy.conf must apply limit_req zone=jf_auth to AuthenticateByName")
	}
}

// TestRenderRemoteConfigs_DenyRegexAnchoring verifies that the rendered
// remote vhost (both simple and full modes) anchors its /System/Info and
// /System/Logs deny rules with /?$. Two regression vectors:
//
//   - /System/Info anchoring keeps /System/Info/Public reachable for client
//     discovery (the "Safari: no servers available" bug — see commit 1112dbf).
//   - /System/Logs anchoring keeps any future Jellyfin sub-paths reachable
//     instead of silently 403'ing every nested log endpoint.
func TestRenderRemoteConfigs_DenyRegexAnchoring(t *testing.T) {
	cases := []struct {
		mode string
		env  EnvMap
	}{
		{"simple", EnvMap{
			"REMOTE_MODE":     "portforward",
			"REMOTE_HOSTNAME": "",
		}},
		{"full", EnvMap{
			"REMOTE_MODE":      "portforward",
			"REMOTE_HOSTNAME":  "test.example.com",
			"REMOTE_CERT_MODE": "self-signed",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			configDir := t.TempDir()
			scriptDir := setupRemoteScriptDir(t)
			tc.env["CONFIG_DIR"] = configDir
			if err := RenderRemoteConfigs(scriptDir, tc.env); err != nil {
				t.Fatalf("RenderRemoteConfigs: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(configDir, "nginx", "remote.conf"))
			if err != nil {
				t.Fatalf("read remote.conf: %v", err)
			}
			conf := string(data)
			assertSystemInfoRegexAllowsPublic(t, conf)
			assertSystemLogsRegexAnchored(t, conf)
		})
	}
}

// TestRenderRemoteConfigs_PortCollision verifies that port shape and collision
// errors surface before cert/template work, so users get an actionable error
// instead of an opaque compose-time bind failure.
func TestRenderRemoteConfigs_PortCollision(t *testing.T) {
	cases := []struct {
		name    string
		env     EnvMap
		wantSub string
	}{
		{
			name: "remote https collides with dashboard port",
			env: EnvMap{
				"REMOTE_MODE":       "portforward",
				"REMOTE_HOSTNAME":   "",
				"REMOTE_HTTPS_PORT": "7354",
				"PELICULA_PORT":     "7354",
			},
			wantSub: "REMOTE_HTTPS_PORT must differ from PELICULA_PORT",
		},
		{
			name: "remote http collides with dashboard port (full mode)",
			env: EnvMap{
				"REMOTE_MODE":       "portforward",
				"REMOTE_HOSTNAME":   "host.example.com",
				"REMOTE_CERT_MODE":  "self-signed",
				"REMOTE_HTTP_PORT":  "7354",
				"REMOTE_HTTPS_PORT": "8920",
				"PELICULA_PORT":     "7354",
			},
			wantSub: "REMOTE_HTTP_PORT must differ from PELICULA_PORT",
		},
		{
			name: "out-of-range remote https port",
			env: EnvMap{
				"REMOTE_MODE":       "portforward",
				"REMOTE_HOSTNAME":   "",
				"REMOTE_HTTPS_PORT": "70000",
			},
			wantSub: "REMOTE_HTTPS_PORT",
		},
		{
			name: "non-numeric remote https port",
			env: EnvMap{
				"REMOTE_MODE":       "portforward",
				"REMOTE_HOSTNAME":   "",
				"REMOTE_HTTPS_PORT": "abc",
			},
			wantSub: "REMOTE_HTTPS_PORT",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configDir := t.TempDir()
			scriptDir := setupRemoteScriptDir(t)
			tc.env["CONFIG_DIR"] = configDir
			err := RenderRemoteConfigs(scriptDir, tc.env)
			if err == nil {
				t.Fatalf("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q should contain %q", err.Error(), tc.wantSub)
			}
		})
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
