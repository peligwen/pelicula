# Jellyfin Auto-Discovery URL Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Jellyfin LAN URL field to the setup wizard that gets written into `network.xml` as `<PublishedServerUrl>`, fixing Jellyfin client LAN auto-discovery (UDP 7359 broadcast currently advertises the container-internal IP instead of the host's LAN IP).

**Architecture:** Host CLI detects LAN IP and passes it as `HOST_LAN_URL` env var → middleware reads it in `handleSetupDetect` → wizard prefills it → `handleSetupSubmit` writes it as `JELLYFIN_PUBLISHED_URL` to `.env` → `seed.go` reads it when writing `network.xml`.

**Tech Stack:** Go (stdlib-only CLI), Go (middleware with modernc.org/sqlite), HTML/JS (wizard), Docker Compose env vars

---

## Task 1: CLI — `detectLANURL()` with unit test

Adds an RFC1918 LAN detection helper to the CLI. `platform.go` already owns host detection (TZ, UID, sudo, WSL); `detectLANURL()` belongs there as a sibling of `detectTZ()`. The CLI is stdlib-only so we use `net.InterfaceAddrs()`.

### Step 1.1: Write failing test for `detectLANURL`

- [ ] Create `/Users/gwen/workspace/pelicula/cmd/pelicula/platform_test.go` with the test below. If the file already exists, append the test instead.

```go
package main

import (
	"net"
	"strings"
	"testing"
)

func TestDetectLANURL_FormatAndRange(t *testing.T) {
	got := detectLANURL()
	if got == "" {
		// Acceptable on hosts with no RFC1918 address (CI sandboxes, etc.).
		// We don't fail the test — but we assert the empty-string contract
		// is honored rather than some partial/malformed string.
		return
	}

	// Must be http://<ip>:7354/jellyfin
	const prefix = "http://"
	const suffix = ":7354/jellyfin"
	if !strings.HasPrefix(got, prefix) || !strings.HasSuffix(got, suffix) {
		t.Fatalf("detectLANURL() = %q, want http://<ip>%s", got, suffix)
	}

	ipStr := strings.TrimSuffix(strings.TrimPrefix(got, prefix), suffix)
	ip := net.ParseIP(ipStr)
	if ip == nil || ip.To4() == nil {
		t.Fatalf("detectLANURL() ip = %q, not a valid IPv4", ipStr)
	}

	if !isRFC1918(ip) {
		t.Fatalf("detectLANURL() ip %s not in an RFC1918 range", ipStr)
	}
	if ip.IsLoopback() {
		t.Fatalf("detectLANURL() returned loopback address %s", ipStr)
	}
}

func TestIsRFC1918(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"172.15.0.1", false},
		{"172.32.0.1", false},
		{"192.168.1.1", true},
		{"192.168.255.255", true},
		{"8.8.8.8", false},
		{"127.0.0.1", false},
		{"169.254.1.1", false},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if got := isRFC1918(ip); got != c.want {
			t.Errorf("isRFC1918(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}
```

- [ ] Run: `go test ./cmd/pelicula/... -run 'TestDetectLANURL_FormatAndRange|TestIsRFC1918'`
- [ ] Verify the test fails with "undefined: detectLANURL" and "undefined: isRFC1918".

### Step 1.2: Implement `detectLANURL` and `isRFC1918`

- [ ] Add to `/Users/gwen/workspace/pelicula/cmd/pelicula/platform.go` — append after `detectSudo()`:

```go
// detectLANURL walks host interfaces and returns an http URL of the first
// non-loopback IPv4 address in an RFC1918 range, formatted for the nginx
// dashboard port. Returns empty string if no suitable interface is found
// (no network, all loopback, or all public/APIPA addresses).
//
// Used to populate HOST_LAN_URL so the setup wizard can prefill a Jellyfin
// PublishedServerUrl — what clients on the LAN should see when they discover
// the server over UDP 7359 broadcast.
func detectLANURL() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipnet.IP
		if ip == nil || ip.IsLoopback() {
			continue
		}
		ip4 := ip.To4()
		if ip4 == nil {
			continue
		}
		if isRFC1918(ip4) {
			return fmt.Sprintf("http://%s:7354/jellyfin", ip4.String())
		}
	}
	return ""
}

// isRFC1918 reports whether ip is in 10.0.0.0/8, 172.16.0.0/12, or
// 192.168.0.0/16.
func isRFC1918(ip net.IP) bool {
	if ip == nil {
		return false
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	switch {
	case ip4[0] == 10:
		return true
	case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
		return true
	case ip4[0] == 192 && ip4[1] == 168:
		return true
	}
	return false
}
```

- [ ] Update the import block at the top of `platform.go` — it currently has:

```go
import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)
```

Change it to:

```go
import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
)
```

- [ ] Run: `go test ./cmd/pelicula/... -run 'TestDetectLANURL_FormatAndRange|TestIsRFC1918'`
- [ ] Verify both tests pass.
- [ ] Run full CLI test suite: `go test ./cmd/pelicula/...`
- [ ] Commit: `feat(cli): detect RFC1918 LAN URL for Jellyfin auto-discovery`

---

## Task 2: CLI — export `HOST_LAN_URL` to the setup container

`cmd_up.go` already exports `HOST_CONFIG_DIR`, `HOST_LIBRARY_DIR`, etc. before launching `docker-compose.setup.yml`. We add `HOST_LAN_URL` alongside and wire it through the compose file.

### Step 2.1: Export `HOST_LAN_URL` in `cmdUp`

- [ ] Edit `/Users/gwen/workspace/pelicula/cmd/pelicula/cmd_up.go`. Find the `setupEnv = append(setupEnv, ...)` block (around line 37) and add `HOST_LAN_URL`:

Current:
```go
setupEnv = append(setupEnv,
    "HOST_PLATFORM="+plat.HostPlatformID(),
    "HOST_TZ="+plat.TZ,
    fmt.Sprintf("HOST_PUID=%d", plat.UID),
    fmt.Sprintf("HOST_PGID=%d", plat.GID),
    "HOST_HOME="+home,
    "HOST_CONFIG_DIR="+plat.DefaultConfigDir,
    "HOST_LIBRARY_DIR="+plat.DefaultLibraryDir,
    "HOST_WORK_DIR="+plat.DefaultWorkDir,
)
```

Replace with:
```go
setupEnv = append(setupEnv,
    "HOST_PLATFORM="+plat.HostPlatformID(),
    "HOST_TZ="+plat.TZ,
    fmt.Sprintf("HOST_PUID=%d", plat.UID),
    fmt.Sprintf("HOST_PGID=%d", plat.GID),
    "HOST_HOME="+home,
    "HOST_CONFIG_DIR="+plat.DefaultConfigDir,
    "HOST_LIBRARY_DIR="+plat.DefaultLibraryDir,
    "HOST_WORK_DIR="+plat.DefaultWorkDir,
    "HOST_LAN_URL="+detectLANURL(),
)
```

### Step 2.2: Forward `HOST_LAN_URL` in `docker-compose.setup.yml`

- [ ] Edit `/Users/gwen/workspace/pelicula/docker-compose.setup.yml`. Find the `environment:` block for `pelicula-api` and add `HOST_LAN_URL` after `HOST_WORK_DIR`:

Current:
```yaml
      - HOST_CONFIG_DIR=${HOST_CONFIG_DIR:-./config}
      - HOST_LIBRARY_DIR=${HOST_LIBRARY_DIR:-~/media}
      - HOST_WORK_DIR=${HOST_WORK_DIR:-~/media}
```

Replace with:
```yaml
      - HOST_CONFIG_DIR=${HOST_CONFIG_DIR:-./config}
      - HOST_LIBRARY_DIR=${HOST_LIBRARY_DIR:-~/media}
      - HOST_WORK_DIR=${HOST_WORK_DIR:-~/media}
      - HOST_LAN_URL=${HOST_LAN_URL:-}
```

- [ ] Run: `go build ./cmd/pelicula/...` to verify `cmd_up.go` still compiles.
- [ ] Run: `docker compose -f docker-compose.setup.yml config > /dev/null` to verify compose yaml is valid.
- [ ] Commit: `feat(cli): export HOST_LAN_URL to setup container`

---

## Task 3: Middleware — wire `LANUrl` through SetupDetect and SetupRequest

Adds `lan_url` to the detect response, adds `lan_url` to the submit request, and persists the value as `JELLYFIN_PUBLISHED_URL` in `.env`. Empty strings are allowed (backwards compatible — no new required fields).

### Step 3.1: Failing test — `handleSetupDetect` returns `lan_url` from env

- [ ] Edit `/Users/gwen/workspace/pelicula/middleware/setup_test.go`. Append the following test at the bottom:

```go
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
```

- [ ] Run: `go test ./middleware/... -run 'TestHandleSetupDetect_ReturnsLANURL|TestHandleSetupDetect_EmptyLANURLWhenUnset'`
- [ ] Verify the test fails with "resp.LANUrl undefined".

### Step 3.2: Add `LANUrl` to `SetupDetect` and populate in `handleSetupDetect`

- [ ] Edit `/Users/gwen/workspace/pelicula/middleware/setup.go`. Update the `SetupDetect` struct:

Current:
```go
type SetupDetect struct {
	Platform   string `json:"platform"`
	TZ         string `json:"tz"`
	PUID       string `json:"puid"`
	PGID       string `json:"pgid"`
	ConfigDir  string `json:"config_dir"`
	LibraryDir string `json:"library_dir"`
	WorkDir    string `json:"work_dir"`
}
```

Replace with:
```go
type SetupDetect struct {
	Platform   string `json:"platform"`
	TZ         string `json:"tz"`
	PUID       string `json:"puid"`
	PGID       string `json:"pgid"`
	ConfigDir  string `json:"config_dir"`
	LibraryDir string `json:"library_dir"`
	WorkDir    string `json:"work_dir"`
	LANUrl     string `json:"lan_url"`
}
```

- [ ] In the same file, update `handleSetupDetect` to populate the field:

Current:
```go
	resp := SetupDetect{
		Platform:   envOr("HOST_PLATFORM", "linux"),
		TZ:         envOr("HOST_TZ", "America/New_York"),
		PUID:       envOr("HOST_PUID", "1000"),
		PGID:       envOr("HOST_PGID", "1000"),
		ConfigDir:  envOr("HOST_CONFIG_DIR", "./config"),
		LibraryDir: envOr("HOST_LIBRARY_DIR", "~/media"),
		WorkDir:    envOr("HOST_WORK_DIR", "~/media"),
	}
```

Replace with:
```go
	resp := SetupDetect{
		Platform:   envOr("HOST_PLATFORM", "linux"),
		TZ:         envOr("HOST_TZ", "America/New_York"),
		PUID:       envOr("HOST_PUID", "1000"),
		PGID:       envOr("HOST_PGID", "1000"),
		ConfigDir:  envOr("HOST_CONFIG_DIR", "./config"),
		LibraryDir: envOr("HOST_LIBRARY_DIR", "~/media"),
		WorkDir:    envOr("HOST_WORK_DIR", "~/media"),
		LANUrl:     envOr("HOST_LAN_URL", ""),
	}
```

- [ ] Run: `go test ./middleware/... -run 'TestHandleSetupDetect_ReturnsLANURL|TestHandleSetupDetect_EmptyLANURLWhenUnset'`
- [ ] Verify both tests pass.

### Step 3.3: Failing test — `handleSetupSubmit` persists `lan_url` as `JELLYFIN_PUBLISHED_URL`

This test exercises the real write path, which means it needs a writable `envPath`. `envPath` is a package-level const (`/project/.env`), so we can't redirect it from a test. Instead, assert the new field is decoded and sanitized — we'll cover the env-write path via e2e plus a direct `writeEnvFile` test.

- [ ] Append to `/Users/gwen/workspace/pelicula/middleware/setup_test.go`:

```go
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
		"CONFIG_DIR":              "/config",
		"JELLYFIN_PUBLISHED_URL":  "http://192.168.1.42:7354/jellyfin",
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
	// Empty JELLYFIN_PUBLISHED_URL should not appear as an empty line;
	// if the caller omits the key, writeEnvFile should skip it. This keeps
	// existing installs (no wizard value) byte-identical in .env.
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
```

- [ ] Run: `go test ./middleware/... -run 'TestSetupRequest_DecodesLANURL|TestHandleSetupSubmit_RejectsInjectionInLANURL|TestWriteEnvFile_IncludesJellyfinPublishedURL|TestWriteEnvFile_OmitsEmptyJellyfinPublishedURL'`
- [ ] Verify all four tests fail.

### Step 3.4: Add `LANUrl` to `SetupRequest`, sanitize, and persist

- [ ] Edit `/Users/gwen/workspace/pelicula/middleware/setup.go`. Update `SetupRequest`:

Current:
```go
type SetupRequest struct {
	ConfigDir    string `json:"config_dir"`
	MediaDir     string `json:"media_dir"`
	LibraryDir   string `json:"library_dir"`
	WorkDir      string `json:"work_dir"`
	WireguardKey string `json:"wireguard_key"`
	VPNSkipped   bool   `json:"vpn_skipped"`
}
```

Replace with:
```go
type SetupRequest struct {
	ConfigDir    string `json:"config_dir"`
	MediaDir     string `json:"media_dir"`
	LibraryDir   string `json:"library_dir"`
	WorkDir      string `json:"work_dir"`
	WireguardKey string `json:"wireguard_key"`
	VPNSkipped   bool   `json:"vpn_skipped"`
	LANUrl       string `json:"lan_url"`
}
```

- [ ] In `handleSetupSubmit`, update the sanitization loop to include `lan_url`. Current:

```go
	for _, check := range []struct{ name, val string }{
		{"wireguard_key", req.WireguardKey},
		{"config_dir", req.ConfigDir},
		{"media_dir", req.MediaDir},
		{"library_dir", req.LibraryDir},
		{"work_dir", req.WorkDir},
	} {
```

Replace with:
```go
	for _, check := range []struct{ name, val string }{
		{"wireguard_key", req.WireguardKey},
		{"config_dir", req.ConfigDir},
		{"media_dir", req.MediaDir},
		{"library_dir", req.LibraryDir},
		{"work_dir", req.WorkDir},
		{"lan_url", req.LANUrl},
	} {
```

- [ ] In the same function, add `JELLYFIN_PUBLISHED_URL` to the `vars` map but only when non-empty (so existing-install `.env` files stay byte-identical if the wizard field is blank). Find:

```go
	vars := map[string]string{
		"CONFIG_DIR":            req.ConfigDir,
		"LIBRARY_DIR":           libraryDir,
		"WORK_DIR":              workDir,
		"PUID":                  puid,
		"PGID":                  pgid,
		"TZ":                    tz,
		"WIREGUARD_PRIVATE_KEY": wgKey,
		"SERVER_COUNTRIES":      "Netherlands",
		"PELICULA_PORT":         "7354",
		"PELICULA_AUTH":         "jellyfin",
		"PROCULA_API_KEY":       proculaKey,
		"WEBHOOK_SECRET":        webhookSecret,
		"TRANSCODING_ENABLED":   "false",
		"NOTIFICATIONS_ENABLED": "false",
		"NOTIFICATIONS_MODE":    "internal",
		"PELICULA_SUB_LANGS":    "en",
	}

	if err := writeEnvFile(envPath, vars); err != nil {
```

Replace with:
```go
	vars := map[string]string{
		"CONFIG_DIR":            req.ConfigDir,
		"LIBRARY_DIR":           libraryDir,
		"WORK_DIR":              workDir,
		"PUID":                  puid,
		"PGID":                  pgid,
		"TZ":                    tz,
		"WIREGUARD_PRIVATE_KEY": wgKey,
		"SERVER_COUNTRIES":      "Netherlands",
		"PELICULA_PORT":         "7354",
		"PELICULA_AUTH":         "jellyfin",
		"PROCULA_API_KEY":       proculaKey,
		"WEBHOOK_SECRET":        webhookSecret,
		"TRANSCODING_ENABLED":   "false",
		"NOTIFICATIONS_ENABLED": "false",
		"NOTIFICATIONS_MODE":    "internal",
		"PELICULA_SUB_LANGS":    "en",
	}

	// Only persist JELLYFIN_PUBLISHED_URL when the user provided a value.
	// writeEnvFile skips unknown/absent keys, so omitting it here keeps
	// existing installs byte-identical if the wizard field is blank.
	if lan := strings.TrimSpace(req.LANUrl); lan != "" {
		vars["JELLYFIN_PUBLISHED_URL"] = lan
	}

	if err := writeEnvFile(envPath, vars); err != nil {
```

### Step 3.5: Add `JELLYFIN_PUBLISHED_URL` to `writeEnvFile` canonical order

`writeEnvFile` walks `order[]` first, then writes any remaining keys in map iteration order. We want a stable location so repeat saves are deterministic.

- [ ] Edit `/Users/gwen/workspace/pelicula/middleware/settings.go`. Find the `order` slice in `writeEnvFile` (around line 92) and add `JELLYFIN_PUBLISHED_URL` after `JELLYFIN_API_KEY`:

Current:
```go
	order := []string{
		"CONFIG_DIR", "LIBRARY_DIR", "WORK_DIR",
		"PUID", "PGID", "TZ",
		"WIREGUARD_PRIVATE_KEY", "SERVER_COUNTRIES",
		"PELICULA_PORT", "PELICULA_AUTH",
		"PELICULA_OPEN_REGISTRATION",
		"JELLYFIN_ADMIN_USER", // legacy: kept for upgrade-path ordering
		"JELLYFIN_PASSWORD",   // legacy: kept for upgrade-path ordering
		"JELLYFIN_API_KEY",
		"PROCULA_API_KEY", "WEBHOOK_SECRET",
		"TRANSCODING_ENABLED",
		"NOTIFICATIONS_ENABLED", "NOTIFICATIONS_MODE",
		"PELICULA_SUB_LANGS",
		"REQUESTS_RADARR_PROFILE_ID", "REQUESTS_RADARR_ROOT",
		"REQUESTS_SONARR_PROFILE_ID", "REQUESTS_SONARR_ROOT",
		"REMOTE_ACCESS_ENABLED", "REMOTE_HOSTNAME",
		"REMOTE_HTTP_PORT", "REMOTE_HTTPS_PORT",
		"REMOTE_CERT_MODE", "REMOTE_LE_EMAIL", "REMOTE_LE_STAGING",
		"SEEDING_REMOVE_ON_COMPLETE",
	}
```

Replace with:
```go
	order := []string{
		"CONFIG_DIR", "LIBRARY_DIR", "WORK_DIR",
		"PUID", "PGID", "TZ",
		"WIREGUARD_PRIVATE_KEY", "SERVER_COUNTRIES",
		"PELICULA_PORT", "PELICULA_AUTH",
		"PELICULA_OPEN_REGISTRATION",
		"JELLYFIN_ADMIN_USER", // legacy: kept for upgrade-path ordering
		"JELLYFIN_PASSWORD",   // legacy: kept for upgrade-path ordering
		"JELLYFIN_API_KEY",
		"JELLYFIN_PUBLISHED_URL",
		"PROCULA_API_KEY", "WEBHOOK_SECRET",
		"TRANSCODING_ENABLED",
		"NOTIFICATIONS_ENABLED", "NOTIFICATIONS_MODE",
		"PELICULA_SUB_LANGS",
		"REQUESTS_RADARR_PROFILE_ID", "REQUESTS_RADARR_ROOT",
		"REQUESTS_SONARR_PROFILE_ID", "REQUESTS_SONARR_ROOT",
		"REMOTE_ACCESS_ENABLED", "REMOTE_HOSTNAME",
		"REMOTE_HTTP_PORT", "REMOTE_HTTPS_PORT",
		"REMOTE_CERT_MODE", "REMOTE_LE_EMAIL", "REMOTE_LE_STAGING",
		"SEEDING_REMOVE_ON_COMPLETE",
	}
```

Note: `writeEnvFile`'s existing loop already does `if _, ok := vars[k]; !ok { continue }` (line 121) — so keys that aren't in the input map are skipped. No extra code is needed for the empty-string case: we simply don't add the key to `vars` when `req.LANUrl` is empty.

- [ ] Run: `go test ./middleware/... -run 'TestSetupRequest_DecodesLANURL|TestHandleSetupSubmit_RejectsInjectionInLANURL|TestWriteEnvFile_IncludesJellyfinPublishedURL|TestWriteEnvFile_OmitsEmptyJellyfinPublishedURL'`
- [ ] Verify all four pass.
- [ ] Run full middleware test suite: `go test ./middleware/...`
- [ ] Commit: `feat(middleware): wire LAN URL through setup detect/submit to JELLYFIN_PUBLISHED_URL`

---

## Task 4: Seed — write `PublishedServerUrl` into `network.xml`

Update both `SeedAllConfigs` (first-run seed) and `resetJellyfin` (reset path) to include `<PublishedServerUrl>` when `JELLYFIN_PUBLISHED_URL` is set in the environment. Empty/unset omits the element entirely — Jellyfin falls back to its default advertising behavior, and existing installs stay byte-identical.

### Step 4.1: Failing test — seeded `network.xml` contains PublishedServerUrl

- [ ] Append to `/Users/gwen/workspace/pelicula/cmd/pelicula/seed_test.go`:

```go
func TestSeedAllConfigs_WritesPublishedServerURLWhenSet(t *testing.T) {
	t.Setenv("JELLYFIN_PUBLISHED_URL", "http://192.168.1.42:7354/jellyfin")

	dir := t.TempDir()
	if err := SeedAllConfigs(dir); err != nil {
		t.Fatalf("SeedAllConfigs: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "jellyfin", "network.xml"))
	got := string(data)

	if !strings.Contains(got, "<PublishedServerUrl>http://192.168.1.42:7354/jellyfin</PublishedServerUrl>") {
		t.Errorf("network.xml missing PublishedServerUrl\n got: %s", got)
	}
	if !strings.Contains(got, "<BaseUrl>/jellyfin</BaseUrl>") {
		t.Errorf("network.xml missing BaseUrl\n got: %s", got)
	}
}

func TestSeedAllConfigs_OmitsPublishedServerURLWhenUnset(t *testing.T) {
	t.Setenv("JELLYFIN_PUBLISHED_URL", "")

	dir := t.TempDir()
	if err := SeedAllConfigs(dir); err != nil {
		t.Fatalf("SeedAllConfigs: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "jellyfin", "network.xml"))
	got := string(data)

	if strings.Contains(got, "PublishedServerUrl") {
		t.Errorf("network.xml should not mention PublishedServerUrl when env unset\n got: %s", got)
	}
}

func TestSeedAllConfigs_EscapesPublishedServerURL(t *testing.T) {
	t.Setenv("JELLYFIN_PUBLISHED_URL", "http://h&t<s>:7354/jellyfin")

	dir := t.TempDir()
	if err := SeedAllConfigs(dir); err != nil {
		t.Fatalf("SeedAllConfigs: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "jellyfin", "network.xml"))
	got := string(data)

	// Raw unescaped characters must not appear inside the element
	if strings.Contains(got, "h&t<s>") {
		t.Errorf("network.xml should XML-escape special chars\n got: %s", got)
	}
	// Escaped form must appear
	if !strings.Contains(got, "http://h&amp;t&lt;s&gt;:7354/jellyfin") {
		t.Errorf("network.xml missing escaped PublishedServerUrl\n got: %s", got)
	}
}
```

- [ ] Run: `go test ./cmd/pelicula/... -run 'TestSeedAllConfigs_WritesPublishedServerURLWhenSet|TestSeedAllConfigs_OmitsPublishedServerURLWhenUnset|TestSeedAllConfigs_EscapesPublishedServerURL'`
- [ ] Verify all three fail (the existing `network.xml` has no PublishedServerUrl).

### Step 4.2: Extract `jellyfinNetworkXML` helper and use it in both seed paths

- [ ] Edit `/Users/gwen/workspace/pelicula/cmd/pelicula/seed.go`. Add this helper function near the top, below `xmlEscape`:

```go
// jellyfinNetworkXML returns the contents of Jellyfin's network.xml.
// When JELLYFIN_PUBLISHED_URL is set in the environment, it is included as a
// <PublishedServerUrl> element so LAN clients discovering the server via UDP
// 7359 broadcast see the correct host-reachable URL instead of the container's
// internal IP. When unset, the element is omitted — Jellyfin falls back to its
// default advertising behavior, and the file stays byte-identical to prior
// versions (backwards compatible for existing installs).
func jellyfinNetworkXML() string {
	const header = `<?xml version="1.0" encoding="utf-8"?><NetworkConfiguration xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema"><BaseUrl>/jellyfin</BaseUrl>`
	const footer = `</NetworkConfiguration>`
	if url := os.Getenv("JELLYFIN_PUBLISHED_URL"); url != "" {
		return header + "<PublishedServerUrl>" + xmlEscape(url) + "</PublishedServerUrl>" + footer
	}
	return header + footer
}
```

- [ ] In `SeedAllConfigs`, replace the inline Jellyfin network.xml string (around line 118) with a call to the helper. Current:

```go
	// Jellyfin network.xml
	if err := seedConfig(
		filepath.Join(configDir, "jellyfin", "network.xml"),
		`<?xml version="1.0" encoding="utf-8"?><NetworkConfiguration xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema"><BaseUrl>/jellyfin</BaseUrl></NetworkConfiguration>`,
	); err != nil {
		return fmt.Errorf("seed jellyfin network.xml: %w", err)
	}
```

Replace with:
```go
	// Jellyfin network.xml
	if err := seedConfig(
		filepath.Join(configDir, "jellyfin", "network.xml"),
		jellyfinNetworkXML(),
	); err != nil {
		return fmt.Errorf("seed jellyfin network.xml: %w", err)
	}
```

- [ ] In `resetJellyfin` (around line 240), replace the inline string. Current:

```go
	if err := os.WriteFile(
		filepath.Join(jellyfinDir, "network.xml"),
		[]byte(`<?xml version="1.0" encoding="utf-8"?><NetworkConfiguration xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema"><BaseUrl>/jellyfin</BaseUrl></NetworkConfiguration>`),
		0644,
	); err != nil {
		return err
	}
```

Replace with:
```go
	if err := os.WriteFile(
		filepath.Join(jellyfinDir, "network.xml"),
		[]byte(jellyfinNetworkXML()),
		0644,
	); err != nil {
		return err
	}
```

- [ ] Run: `go test ./cmd/pelicula/... -run 'TestSeedAllConfigs_WritesPublishedServerURLWhenSet|TestSeedAllConfigs_OmitsPublishedServerURLWhenUnset|TestSeedAllConfigs_EscapesPublishedServerURL'`
- [ ] Verify all three pass.
- [ ] Run full CLI test suite: `go test ./cmd/pelicula/...`
- [ ] Commit: `feat(cli): write JELLYFIN_PUBLISHED_URL into network.xml on seed/reset`

---

## Task 5: Wizard UI — add the input field

Adds a Jellyfin LAN URL field to Step 2 of `nginx/setup.html`, prefilled from `detected.lan_url`, included in the submit body, with light auto-`/jellyfin`-suffix validation.

### Step 5.1: Add the input to the Step 2 panel

- [ ] Edit `/Users/gwen/workspace/pelicula/nginx/setup.html`. Find the Step 2 panel's advanced paths `<details>` block (around line 78) and insert the new field after it, before the `<div class="step-nav">`. Current:

```html
            <details id="advanced-paths">
                <summary class="advanced-toggle">Advanced: separate library &amp; work paths</summary>
                <div class="advanced-content">
                    <p class="field-help">Split media across disks — e.g. finished media on a large HDD, downloads on a fast SSD.</p>
                    <label for="path-library">Finished Media</label>
                    <input type="text" id="path-library" placeholder="Same as media directory">
                    <label for="path-work">Downloads &amp; Processing</label>
                    <input type="text" id="path-work" placeholder="Same as media directory">
                </div>
            </details>

            <div class="step-nav">
```

Replace with:
```html
            <details id="advanced-paths">
                <summary class="advanced-toggle">Advanced: separate library &amp; work paths</summary>
                <div class="advanced-content">
                    <p class="field-help">Split media across disks — e.g. finished media on a large HDD, downloads on a fast SSD.</p>
                    <label for="path-library">Finished Media</label>
                    <input type="text" id="path-library" placeholder="Same as media directory">
                    <label for="path-work">Downloads &amp; Processing</label>
                    <input type="text" id="path-work" placeholder="Same as media directory">
                </div>
            </details>

            <label for="published-url">Jellyfin LAN URL</label>
            <p class="field-help">What Jellyfin clients on your network should connect to. Leave blank to skip.</p>
            <input id="published-url" type="url" placeholder="http://192.168.1.42:7354/jellyfin">

            <div class="step-nav">
```

### Step 5.2: Prefill from `detected.lan_url` in `init()`

- [ ] In the same file, update the `init()` IIFE. Current (around line 144):

```js
            const r = await fetch('/api/pelicula/setup/detect');
            detected = await r.json();
            document.getElementById('path-config').value = detected.config_dir || '';
            document.getElementById('path-media').value = detected.library_dir || '';
```

Replace with:
```js
            const r = await fetch('/api/pelicula/setup/detect');
            detected = await r.json();
            document.getElementById('path-config').value = detected.config_dir || '';
            document.getElementById('path-media').value = detected.library_dir || '';
            document.getElementById('published-url').value = detected.lan_url || '';
```

### Step 5.3: Include `published_url` in the submit body with `/jellyfin` suffix enforcement

- [ ] In the same file, update `submitSetup()`. Current (around line 302):

```js
    const body = {
        config_dir: document.getElementById('path-config').value.trim(),
        media_dir: document.getElementById('path-media').value.trim(),
        library_dir: document.getElementById('path-library').value.trim() || '',
        work_dir: document.getElementById('path-work').value.trim() || '',
        wireguard_key: vpnSkipped ? '' : document.getElementById('vpn-key').value.trim(),
        vpn_skipped: vpnSkipped,
    };
```

Replace with:
```js
    // Light validation: if published URL is non-empty and does not end with
    // /jellyfin, append it before submitting. Jellyfin's BaseUrl is /jellyfin,
    // so LAN discovery URLs must include that suffix.
    let publishedURL = document.getElementById('published-url').value.trim();
    if (publishedURL && !publishedURL.replace(/\/+$/, '').endsWith('/jellyfin')) {
        publishedURL = publishedURL.replace(/\/+$/, '') + '/jellyfin';
    }

    const body = {
        config_dir: document.getElementById('path-config').value.trim(),
        media_dir: document.getElementById('path-media').value.trim(),
        library_dir: document.getElementById('path-library').value.trim() || '',
        work_dir: document.getElementById('path-work').value.trim() || '',
        wireguard_key: vpnSkipped ? '' : document.getElementById('vpn-key').value.trim(),
        vpn_skipped: vpnSkipped,
        lan_url: publishedURL,
    };
```

- [ ] Smoke-test the page renders correctly by opening `nginx/setup.html` in a browser. The "Jellyfin LAN URL" field should appear in Step 2 below the advanced paths details. (No automated UI test — the e2e test in Task 6 will cover the full wire.)
- [ ] Commit: `feat(wizard): add Jellyfin LAN URL input and submit as lan_url`

---

## Task 6: E2E test fixture — update `network.xml` and pass `HOST_LAN_URL`

The e2e test writes its own `.env` directly (it doesn't go through the wizard) and calls `seed_config` from bash. Both sides need updating:
1. Pass `HOST_LAN_URL` to the test environment so `detect` returns it (even though the e2e never actually runs the wizard — this keeps the env surface honest).
2. Update the bash `seed_config` call to include `<PublishedServerUrl>` in the fixture XML so the test asserts against a realistic file.

### Step 6.1: Add `HOST_LAN_URL` + `JELLYFIN_PUBLISHED_URL` to the test env

- [ ] Edit `/Users/gwen/workspace/pelicula/tests/e2e.sh`. Find the `cat > "$test_env" <<EOF` heredoc (around line 179) and add `JELLYFIN_PUBLISHED_URL` after `NOTIFICATIONS_MODE`:

Current:
```bash
    cat > "$test_env" <<EOF
CONFIG_DIR="${test_config_dir}"
LIBRARY_DIR="${test_library_dir}"
WORK_DIR="${test_work_dir}"
PUID="$(id -u)"
PGID="$(id -g)"
TZ="${test_tz}"
WIREGUARD_PRIVATE_KEY="dGVzdGtleXRlc3RrZXl0ZXN0a2V5dGVzdGtleTE="
SERVER_COUNTRIES="Netherlands"
PELICULA_PORT="${test_port}"
PELICULA_AUTH="off"
JELLYFIN_ADMIN_USER="admin"
JELLYFIN_PASSWORD="test-jellyfin-pw"
PROCULA_API_KEY="${test_api_key}"
TRANSCODING_ENABLED=false
NOTIFICATIONS_ENABLED=false
NOTIFICATIONS_MODE=internal
EOF
```

Replace with:
```bash
    cat > "$test_env" <<EOF
CONFIG_DIR="${test_config_dir}"
LIBRARY_DIR="${test_library_dir}"
WORK_DIR="${test_work_dir}"
PUID="$(id -u)"
PGID="$(id -g)"
TZ="${test_tz}"
WIREGUARD_PRIVATE_KEY="dGVzdGtleXRlc3RrZXl0ZXN0a2V5dGVzdGtleTE="
SERVER_COUNTRIES="Netherlands"
PELICULA_PORT="${test_port}"
PELICULA_AUTH="off"
JELLYFIN_ADMIN_USER="admin"
JELLYFIN_PASSWORD="test-jellyfin-pw"
JELLYFIN_PUBLISHED_URL="http://127.0.0.1:${test_port}/jellyfin"
PROCULA_API_KEY="${test_api_key}"
TRANSCODING_ENABLED=false
NOTIFICATIONS_ENABLED=false
NOTIFICATIONS_MODE=internal
EOF
```

### Step 6.2: Update the Jellyfin `network.xml` seed fixture

- [ ] In the same file, find the `seed_config "$test_config_dir/jellyfin/network.xml"` call (around line 208) and add `<PublishedServerUrl>`. Current:

```bash
    seed_config "$test_config_dir/jellyfin/network.xml" \
        '<?xml version="1.0" encoding="utf-8"?><NetworkConfiguration xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema"><BaseUrl>/jellyfin</BaseUrl></NetworkConfiguration>'
```

Replace with:
```bash
    seed_config "$test_config_dir/jellyfin/network.xml" \
        "<?xml version=\"1.0\" encoding=\"utf-8\"?><NetworkConfiguration xmlns:xsi=\"http://www.w3.org/2001/XMLSchema-instance\" xmlns:xsd=\"http://www.w3.org/2001/XMLSchema\"><BaseUrl>/jellyfin</BaseUrl><PublishedServerUrl>http://127.0.0.1:${test_port}/jellyfin</PublishedServerUrl></NetworkConfiguration>"
```

Note the switch from single quotes to double quotes so `${test_port}` expands. XML attributes are now backslash-escaped.

### Step 6.3: Run the e2e

- [ ] Run: `./pelicula test`
- [ ] Verify the e2e passes end-to-end. If anything breaks, `docker exec` into the Jellyfin container and `cat /config/network.xml` to confirm the fixture was written correctly.
- [ ] Commit: `test(e2e): seed PublishedServerUrl in jellyfin network.xml fixture`

---

## Final verification

- [ ] Run full unit suite: `make test`
- [ ] Run e2e: `./pelicula test`
- [ ] Manual smoke test:
  1. `rm .env` in a scratch clone
  2. `./pelicula up`
  3. Open the wizard, verify Step 2 prefills "Jellyfin LAN URL" with your host's LAN IP
  4. Submit
  5. `grep JELLYFIN_PUBLISHED_URL .env` — should match
  6. `cat config/jellyfin/network.xml` — should contain `<PublishedServerUrl>`
  7. Open Jellyfin mobile app on a phone on the same LAN, confirm auto-discovery finds the server at the correct URL.

## Rollback plan

Each task commits independently. If a task breaks the build:
- Revert the most recent commit: `git revert HEAD`
- The middleware changes (Task 3) are the highest risk — if `writeEnvFile`'s canonical order breaks existing installs, revert Task 3 and Task 5 together.
- Tasks 1, 2, 4, 6 are additive and self-contained.
