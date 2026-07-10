# First-Time Experience Redesign — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the two-command setup flow (`pelicula setup` + `pelicula up`) with a single `pelicula up` that opens a browser wizard where the user registers an admin account, configures storage, optionally configures VPN, then lands on the dashboard auto-logged-in.

**Architecture:** The setup wizard gains a new Step 1 (admin registration) and makes VPN optional. The CLI absorbs the setup flow into `cmdUp`. Docker Compose uses profiles to exclude VPN services when skipped. Auth is always `jellyfin` for new installs.

**Tech Stack:** Go (stdlib + modernc.org/sqlite), HTML/JS (vanilla), Docker Compose profiles

**Spec:** `docs/archive/superpowers/specs/2026-04-09-first-time-experience-design.md`

**Important:** `nginx/setup.html` doubles as the Settings page (accessed via `/settings`). The rewrite in Task 7 focuses on the setup mode flow. Settings mode (`mode = 'settings'`) preserves existing prefill logic but the old fields (auth toggle, remote access config, reset flow) are removed from the HTML panels. These settings-mode-only features must be re-added to the new wizard structure or moved to a separate settings page. The plan keeps a `prefillSettings` stub and `applySettingsMode` stub to avoid breaking settings mode entirely — but a full settings-mode update is deferred to a follow-up task. The setup flow is the priority.

---

### Task 1: Add `JELLYFIN_ADMIN_USER` to CLI env handling

**Files:**
- Modify: `cmd/pelicula/env.go:14-30` (envKeyOrder)
- Modify: `cmd/pelicula/cmd_setup.go:44-71` (writeEnvFile signature)
- Test: `cmd/pelicula/env_test.go`

- [ ] **Step 1: Write the failing test**

Add a test to `cmd/pelicula/env_test.go` that verifies `JELLYFIN_ADMIN_USER` round-trips through WriteEnv/ParseEnv:

```go
func TestWriteEnvAdminUser(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	m := EnvMap{
		"CONFIG_DIR":          "/config",
		"JELLYFIN_ADMIN_USER": "gwen",
		"JELLYFIN_PASSWORD":   "test-pass-123",
	}
	if err := WriteEnv(path, m); err != nil {
		t.Fatalf("WriteEnv error: %v", err)
	}
	m2, err := ParseEnv(path)
	if err != nil {
		t.Fatalf("ParseEnv error: %v", err)
	}
	if got := m2["JELLYFIN_ADMIN_USER"]; got != "gwen" {
		t.Errorf("JELLYFIN_ADMIN_USER: got %q, want %q", got, "gwen")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd cmd/pelicula && go test -run TestWriteEnvAdminUser -v`
Expected: PASS (WriteEnv already writes arbitrary keys — this test should pass immediately since extra keys are appended). The real point is to verify the key is in canonical order after we add it.

- [ ] **Step 3: Add JELLYFIN_ADMIN_USER to envKeyOrder**

In `cmd/pelicula/env.go`, add `"JELLYFIN_ADMIN_USER"` to the `envKeyOrder` slice, right before `"JELLYFIN_PASSWORD"`:

```go
var envKeyOrder = []string{
	"CONFIG_DIR",
	"LIBRARY_DIR",
	"WORK_DIR",
	"PUID",
	"PGID",
	"TZ",
	"WIREGUARD_PRIVATE_KEY",
	"SERVER_COUNTRIES",
	"PELICULA_PORT",
	"PELICULA_AUTH",
	"JELLYFIN_ADMIN_USER",
	"JELLYFIN_PASSWORD",
	"PROCULA_API_KEY",
	"TRANSCODING_ENABLED",
	"NOTIFICATIONS_ENABLED",
	"NOTIFICATIONS_MODE",
}
```

- [ ] **Step 4: Add JELLYFIN_ADMIN_USER migration**

In `cmd/pelicula/env.go`, add a migration in `MigrateEnv` for existing installs that lack the key. Add it after the existing `defaults` block (after line 167):

```go
// Migration 3: default JELLYFIN_ADMIN_USER for pre-existing installs
if _, ok := m["JELLYFIN_ADMIN_USER"]; !ok {
	m["JELLYFIN_ADMIN_USER"] = "admin"
	changed = true
}
```

- [ ] **Step 5: Write migration test**

Add to `cmd/pelicula/env_test.go`:

```go
func TestMigrateEnvAddsAdminUser(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	content := `CONFIG_DIR="/config"
LIBRARY_DIR="/media"
WORK_DIR="/media"
PUID="1000"
PGID="1000"
TZ="UTC"
PELICULA_PORT="7354"
PELICULA_AUTH="jellyfin"
JELLYFIN_PASSWORD="old-pass-123"
TRANSCODING_ENABLED=false
NOTIFICATIONS_ENABLED=false
NOTIFICATIONS_MODE=internal
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := MigrateEnv(path)
	if err != nil {
		t.Fatalf("MigrateEnv error: %v", err)
	}
	if !changed {
		t.Error("expected changed=true")
	}
	m, err := ParseEnv(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := m["JELLYFIN_ADMIN_USER"]; got != "admin" {
		t.Errorf("JELLYFIN_ADMIN_USER: got %q, want %q", got, "admin")
	}
}
```

- [ ] **Step 6: Run all CLI tests**

Run: `cd cmd/pelicula && go test -v ./...`
Expected: All PASS

- [ ] **Step 7: Commit**

```bash
git add cmd/pelicula/env.go cmd/pelicula/env_test.go
git commit -m "feat(cli): add JELLYFIN_ADMIN_USER to env schema and migration"
```

---

### Task 2: Add VPN profiles to Docker Compose

**Files:**
- Modify: `docker-compose.yml:1-320`

- [ ] **Step 1: Add `profiles: [vpn]` to gluetun**

In `docker-compose.yml`, add `profiles: [vpn]` to the gluetun service (after `restart: unless-stopped`, around line 28):

```yaml
  gluetun:
    image: qmcgaw/gluetun:v3.41.0
    container_name: gluetun
    profiles: [vpn]
    cap_add:
```

- [ ] **Step 2: Add `profiles: [vpn]` to qbittorrent**

Same pattern for qbittorrent (after `container_name`, around line 34):

```yaml
  qbittorrent:
    image: lscr.io/linuxserver/qbittorrent:latest
    container_name: qbittorrent
    profiles: [vpn]
    network_mode: "service:gluetun"
```

- [ ] **Step 3: Add `profiles: [vpn]` to prowlarr**

Same pattern for prowlarr (after `container_name`, around line 61):

```yaml
  prowlarr:
    image: lscr.io/linuxserver/prowlarr:latest
    container_name: prowlarr
    profiles: [vpn]
    environment:
```

- [ ] **Step 4: Remove gluetun and prowlarr from pelicula-api depends_on**

In the `pelicula-api` service (around line 251-255), remove `gluetun` and `prowlarr` from `depends_on` since they may not be running. Keep `sonarr`, `radarr`, and `docker-proxy`:

```yaml
    depends_on:
      - sonarr
      - radarr
      - docker-proxy
```

- [ ] **Step 5: Remove gluetun and prowlarr from nginx depends_on**

In the `nginx` service (around line 304-312), remove `gluetun` and `prowlarr` from `depends_on`:

```yaml
    depends_on:
      - sonarr
      - radarr
      - jellyfin
      - pelicula-api
      - procula
      - bazarr
```

- [ ] **Step 6: Commit**

```bash
git add docker-compose.yml
git commit -m "feat(compose): move gluetun/qbittorrent/prowlarr to vpn profile"
```

---

### Task 3: CLI `up` command — conditional VPN profile and absorb setup

**Files:**
- Modify: `cmd/pelicula/cmd_up.go:1-147`
- Modify: `cmd/pelicula/main.go:27-69` (remove setup command)
- Modify: `cmd/pelicula/main.go:71-102` (update usage text)
- Modify: `cmd/pelicula/helpers.go:86-91` (update requireEnv message)

- [ ] **Step 1: Add `--profile vpn` when WireGuard key is present**

In `cmd/pelicula/cmd_up.go`, replace the compose up args section (lines 88-93) with VPN-aware logic:

```go
	// Build compose up args with optional profiles
	upArgs := []string{"up", "-d"}

	// VPN profile: only when WireGuard key is configured
	wgKey := env["WIREGUARD_PRIVATE_KEY"]
	if wgKey != "" {
		upArgs = append(upArgs, "--profile", "vpn")
	}

	// Apprise profile: when notifications use apprise
	notifMode := envDefault(env, "NOTIFICATIONS_MODE", "internal")
	if notifMode == "apprise" {
		upArgs = append(upArgs, "--profile", "apprise")
	}
```

- [ ] **Step 2: Make VPN health check conditional**

Replace the gluetun health wait block (lines 99-115) with:

```go
	if wgKey != "" {
		// Wait for gluetun health
		info("Connecting to VPN...")
		const maxAttempts = 30
		vpnConnected := false
		for i := 0; i < maxAttempts; i++ {
			health, err := c.DockerInspect("{{.State.Health.Status}}", "gluetun")
			if err == nil && health == "healthy" {
				vpnConnected = true
				break
			}
			time.Sleep(2 * time.Second)
		}
		if vpnConnected {
			pass("VPN connected")
		} else {
			warn("VPN not ready — check: pelicula logs gluetun")
		}
	} else {
		info("VPN not configured — download services skipped")
	}
```

- [ ] **Step 3: Update admin credentials print to use JELLYFIN_ADMIN_USER**

Replace the admin credentials block (lines 120-124):

```go
	// Print admin credentials if auth is enabled
	if jfPass := env["JELLYFIN_PASSWORD"]; jfPass != "" {
		adminUser := envDefault(env, "JELLYFIN_ADMIN_USER", "admin")
		fmt.Println()
		fmt.Printf("  %sAdmin login:%s  %s / %s\n", colorBold, colorReset, adminUser, jfPass)
	}
```

- [ ] **Step 4: Conditionally print VPN service URLs**

Replace the service URLs block (lines 127-138). Only print VPN-dependent services when VPN is configured:

```go
	fmt.Println()
	fmt.Println("  Service         URL")
	fmt.Println("  ─────────────── ──────────────────────────")
	fmt.Printf("  Dashboard       http://localhost:%s/\n", port)
	fmt.Printf("  Sonarr          http://localhost:%s/sonarr/\n", port)
	fmt.Printf("  Radarr          http://localhost:%s/radarr/\n", port)
	if wgKey != "" {
		fmt.Printf("  Prowlarr        http://localhost:%s/prowlarr/\n", port)
		fmt.Printf("  qBittorrent     http://localhost:%s/qbt/\n", port)
	}
	fmt.Printf("  Jellyfin        http://localhost:%s/jellyfin/\n", port)
	fmt.Printf("  Bazarr          http://localhost:%s/bazarr/\n", port)
	fmt.Printf("  Procula API     http://localhost:%s/api/procula/status\n", port)
```

- [ ] **Step 5: Absorb setup into cmdUp first-run path**

Replace the first-run block in `cmd/pelicula/cmd_up.go` (lines 15-28) to absorb `cmdSetup` inline:

```go
	// If no .env, run setup wizard, then continue with up
	if _, err := os.Stat(envFile); err != nil {
		fmt.Println()
		fmt.Printf("%sNo configuration found — starting setup wizard.%s\n", colorBold, colorReset)
		fmt.Println()

		plat := Detect(scriptDir)
		c := NewCompose(scriptDir, plat.NeedsSudo)

		setupCompose := filepath.Join(scriptDir, "docker-compose.setup.yml")
		if _, err := os.Stat(setupCompose); err != nil {
			fatal("docker-compose.setup.yml not found — make sure you're running from the pelicula directory")
		}

		home, _ := os.UserHomeDir()
		setupEnv := os.Environ()
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

		setupCmd := c.buildSetupCmd(setupCompose, setupEnv)
		if err := setupCmd.Run(); err != nil {
			fatal("Failed to start setup containers: " + err.Error())
		}

		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		defer func() {
			_ = c.runSetupDown(setupCompose)
		}()

		fmt.Printf("  Open %s in your browser to continue setup\n", bold("http://localhost:7354/"))
		fmt.Println()
		openBrowser("http://localhost:7354/")

		info("Waiting for setup to complete (Ctrl+C to abort)...")
		const maxWait = 150
		completed := false
		for i := 0; i < maxWait; i++ {
			select {
			case <-ctx.Done():
				fmt.Println()
				warn("Setup cancelled.")
				return
			default:
			}
			time.Sleep(2 * time.Second)
			if _, err := os.Stat(envFile); err == nil {
				pass("Configuration saved")
				completed = true
				break
			}
			if i == maxWait-1 {
				warn("Setup timed out after 5 minutes")
				return
			}
		}
		if !completed {
			return
		}
		fmt.Println()
	}
```

Add the required imports at the top of `cmd_up.go`:

```go
import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)
```

- [ ] **Step 6: Remove `setup` command from main.go**

In `cmd/pelicula/main.go`, remove the `case "setup"` line (line 28-29):

Delete:
```go
	case "setup":
		cmdSetup(args[1:])
```

Update `usage()` to remove the setup line and update the description for `up`:

```go
func usage() {
	fmt.Print(`Pelicula — clone-and-run media stack

Usage: pelicula <command> [options]

Lifecycle:
  up                  Start the stack (runs setup wizard on first run)
  down                Stop the stack
  restart [service]   Restart service(s)
  rebuild [service]   Rebuild and restart middleware/procula/nginx
  update              Pull latest images and restart
  status              Show service health
  logs [service]      Tail service logs

Configuration:
  reset-config [svc]  Reset service configs (soft/per-service/all)

Data:
  export [file]       Export library backup
  import-backup file  Restore from backup
  import [dir]        Open media import wizard

Network:
  check-vpn           Verify VPN connectivity

Options:
  -v, --verbose       Verbose output
  -h, --help          Show this help
  --version           Show version
`)
}
```

- [ ] **Step 7: Update requireEnv message**

In `cmd/pelicula/helpers.go`, update the `requireEnv` function (line 87-90) to point to `pelicula up` instead of `pelicula setup`:

```go
func requireEnv(envFile string) {
	if _, err := os.Stat(envFile); err != nil {
		fatal("No .env file found. Run " + bold("pelicula up") + " first.")
	}
}
```

- [ ] **Step 8: Delete cmd_setup.go (move setupDirs to cmd_up.go)**

The `setupDirs` function (lines 14-41) is still needed by `cmdUp`. Move it to `cmd_up.go` (or a new `dirs.go`). The `writeEnvFile` function (lines 44-71) and `cmdSetup` function (lines 73-163) are no longer needed by the CLI — the middleware handles `.env` writing.

Create `cmd/pelicula/dirs.go` with just the `setupDirs` function:

```go
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// setupDirs creates the required directory tree for configDir, libraryDir, and workDir.
func setupDirs(configDir, libraryDir, workDir string) error {
	dirs := []string{
		filepath.Join(configDir, "gluetun"),
		filepath.Join(configDir, "qbittorrent"),
		filepath.Join(configDir, "prowlarr"),
		filepath.Join(configDir, "sonarr"),
		filepath.Join(configDir, "radarr"),
		filepath.Join(configDir, "jellyfin"),
		filepath.Join(configDir, "bazarr"),
		filepath.Join(configDir, "procula", "jobs"),
		filepath.Join(configDir, "procula", "profiles"),
		filepath.Join(configDir, "pelicula"),
		filepath.Join(configDir, "certs"),
		filepath.Join(libraryDir, "movies"),
		filepath.Join(libraryDir, "tv"),
		filepath.Join(workDir, "downloads"),
		filepath.Join(workDir, "downloads", "incomplete"),
		filepath.Join(workDir, "downloads", "radarr"),
		filepath.Join(workDir, "downloads", "tv-sonarr"),
		filepath.Join(workDir, "processing"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return nil
}
```

Then delete `cmd/pelicula/cmd_setup.go`.

- [ ] **Step 9: Verify CLI compiles**

Run: `cd cmd/pelicula && go build -o /dev/null .`
Expected: Compiles successfully

- [ ] **Step 10: Run CLI tests**

Run: `cd cmd/pelicula && go test -v ./...`
Expected: All PASS

- [ ] **Step 11: Commit**

```bash
git add cmd/pelicula/cmd_up.go cmd/pelicula/main.go cmd/pelicula/helpers.go cmd/pelicula/dirs.go
git rm cmd/pelicula/cmd_setup.go
git commit -m "feat(cli): absorb setup into up, remove standalone setup command"
```

---

### Task 4: Middleware setup — admin registration and optional VPN

**Files:**
- Modify: `middleware/setup.go:1-207`
- Test: `middleware/setup_test.go`

- [ ] **Step 1: Write tests for new setup request shape**

Add to `middleware/setup_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd middleware && go test -run "TestHandleSetupSubmit_RejectsMissingAdmin|TestHandleSetupSubmit_RejectsShort|TestHandleSetupSubmit_AcceptsVPN|TestHandleGeneratePassword" -v`
Expected: FAIL — functions don't exist yet

- [ ] **Step 3: Update SetupRequest struct and add generate-password endpoint**

Rewrite `middleware/setup.go`. Replace the `SetupRequest` struct (lines 15-23):

```go
// SetupRequest is the JSON body submitted by the browser wizard.
type SetupRequest struct {
	AdminUsername string `json:"admin_username"`
	AdminPassword string `json:"admin_password"`
	ConfigDir     string `json:"config_dir"`
	MediaDir      string `json:"media_dir"`
	LibraryDir    string `json:"library_dir"`
	WorkDir       string `json:"work_dir"`
	WireguardKey  string `json:"wireguard_key"`
	VPNSkipped    bool   `json:"vpn_skipped"`
}
```

Add the generate-password handler after `handleSetupDetect`:

```go
func handleGeneratePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"password": generateReadablePassword(),
	})
}
```

- [ ] **Step 4: Rewrite handleSetupSubmit validation**

Replace the `handleSetupSubmit` function body (lines 63-175). The new validation:

```go
func handleSetupSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if _, err := os.Stat(envPath); err == nil {
		http.Error(w, "already configured", http.StatusConflict)
		return
	}

	var req SetupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate admin credentials
	if req.AdminUsername == "" {
		http.Error(w, "admin_username is required", http.StatusBadRequest)
		return
	}
	if !validUsername(req.AdminUsername) {
		http.Error(w, "admin_username is invalid (1-64 chars, no control chars or slashes)", http.StatusBadRequest)
		return
	}
	if len(req.AdminPassword) < 8 {
		http.Error(w, "admin_password must be at least 8 characters", http.StatusBadRequest)
		return
	}

	// Sanitize all string fields
	for _, check := range []struct{ name, val string }{
		{"admin_username", req.AdminUsername},
		{"admin_password", req.AdminPassword},
		{"wireguard_key", req.WireguardKey},
		{"config_dir", req.ConfigDir},
		{"media_dir", req.MediaDir},
		{"library_dir", req.LibraryDir},
		{"work_dir", req.WorkDir},
	} {
		if strings.ContainsAny(check.val, "\"\n\r") {
			http.Error(w, check.name+" contains invalid characters", http.StatusBadRequest)
			return
		}
	}

	// VPN: validate key if provided, or require vpn_skipped
	wgKey := strings.TrimSpace(req.WireguardKey)
	if !req.VPNSkipped {
		if wgKey == "" {
			http.Error(w, "wireguard_key is required (or set vpn_skipped)", http.StatusBadRequest)
			return
		}
		if len(wgKey) != 44 || wgKey[43] != '=' {
			http.Error(w, "wireguard_key must be a 44-character base64 WireGuard private key", http.StatusBadRequest)
			return
		}
	} else {
		wgKey = "" // ensure empty when skipped
	}

	// Paths: media_dir is the single field; library_dir/work_dir override it
	if req.ConfigDir == "" {
		req.ConfigDir = envOr("HOST_CONFIG_DIR", "./config")
	}
	libraryDir := req.LibraryDir
	workDir := req.WorkDir
	if req.MediaDir != "" {
		if libraryDir == "" {
			libraryDir = req.MediaDir
		}
		if workDir == "" {
			workDir = req.MediaDir
		}
	}
	if libraryDir == "" {
		libraryDir = envOr("HOST_LIBRARY_DIR", "~/media")
	}
	if workDir == "" {
		workDir = envOr("HOST_WORK_DIR", "~/media")
	}

	puid := envOr("HOST_PUID", "1000")
	pgid := envOr("HOST_PGID", "1000")
	tz := envOr("HOST_TZ", "America/New_York")
	proculaKey := generateAPIKey()
	webhookSecret := generateAPIKey()

	envMu.Lock()
	defer envMu.Unlock()

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
		"JELLYFIN_ADMIN_USER":  req.AdminUsername,
		"JELLYFIN_PASSWORD":     req.AdminPassword,
		"PROCULA_API_KEY":       proculaKey,
		"WEBHOOK_SECRET":        webhookSecret,
		"TRANSCODING_ENABLED":   "false",
		"NOTIFICATIONS_ENABLED": "false",
		"NOTIFICATIONS_MODE":    "internal",
		"PELICULA_SUB_LANGS":    "en",
	}

	if err := writeEnvFile(envPath, vars); err != nil {
		slog.Error("failed to write .env", "error", err)
		http.Error(w, "failed to write config", http.StatusInternalServerError)
		return
	}

	slog.Info("setup wizard completed", "component", "setup", "admin", req.AdminUsername, "vpn", !req.VPNSkipped)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
```

- [ ] **Step 5: Register generate-password endpoint in setup mode mux**

In `middleware/main.go`, add to the setup mode mux (after line 32):

```go
mux.HandleFunc("/api/pelicula/setup/generate-password", handleGeneratePassword)
```

- [ ] **Step 6: Add `JELLYFIN_ADMIN_USER` to middleware writeEnvFile order**

In `middleware/settings.go`, add `"JELLYFIN_ADMIN_USER"` to the `order` slice in `writeEnvFile` (line 98), right before `"JELLYFIN_PASSWORD"`:

```go
	order := []string{
		"CONFIG_DIR", "LIBRARY_DIR", "WORK_DIR",
		"PUID", "PGID", "TZ",
		"WIREGUARD_PRIVATE_KEY", "SERVER_COUNTRIES",
		"PELICULA_PORT", "PELICULA_AUTH",
		"PELICULA_OPEN_REGISTRATION",
		"JELLYFIN_ADMIN_USER",
		"JELLYFIN_PASSWORD",
		// ...rest unchanged
```

- [ ] **Step 7: Run middleware tests**

Run: `cd middleware && go test -v ./...`
Expected: All PASS

- [ ] **Step 8: Commit**

```bash
git add middleware/setup.go middleware/setup_test.go middleware/main.go middleware/settings.go
git commit -m "feat(middleware): admin registration, optional VPN, generate-password endpoint"
```

---

### Task 5: Middleware — use JELLYFIN_ADMIN_USER in auto-wiring

**Files:**
- Modify: `middleware/jellyfin.go:184-249`
- Modify: `middleware/autowire.go:21-65`
- Test: `middleware/jellyfin_test.go`

- [ ] **Step 1: Update completeJellyfinWizard to use env var for admin name**

In `middleware/jellyfin.go`, update `completeJellyfinWizard` (around line 210). Replace:

```go
	_, err = jellyfinPost(s, "/Startup/User", "", map[string]any{
		"Name":     "admin",
		"Password": pass,
	})
```

With:

```go
	adminUser := envOr("JELLYFIN_ADMIN_USER", "admin")
	_, err = jellyfinPost(s, "/Startup/User", "", map[string]any{
		"Name":     adminUser,
		"Password": pass,
	})
```

Also update the log messages (lines 202-205):

```go
	if pass == "" {
		slog.Info("creating Jellyfin admin user with no password", "component", "autowire", "username", adminUser)
	} else {
		slog.Info("creating Jellyfin admin user with configured password", "component", "autowire", "username", adminUser)
	}
```

- [ ] **Step 2: Update jellyfinAuth to use env var for admin name**

In `middleware/jellyfin.go`, update `jellyfinAuth` (around line 229). Replace:

```go
	data, err := jellyfinPost(s, "/Users/AuthenticateByName", "", map[string]any{
		"Username": "admin",
		"Pw":       os.Getenv("JELLYFIN_PASSWORD"),
	})
```

With:

```go
	adminUser := envOr("JELLYFIN_ADMIN_USER", "admin")
	data, err := jellyfinPost(s, "/Users/AuthenticateByName", "", map[string]any{
		"Username": adminUser,
		"Pw":       os.Getenv("JELLYFIN_PASSWORD"),
	})
```

Update the log message (line 247):

```go
	slog.Info("Jellyfin authenticated as admin", "component", "autowire", "username", adminUser)
```

- [ ] **Step 3: Make AutoWire skip VPN-dependent wiring when no key**

In `middleware/autowire.go`, update `AutoWire` (lines 21-65). The function currently requires all API keys including Prowlarr. Make it conditional:

Replace lines 31-33 (the API key check):

```go
	if s.SonarrKey == "" || s.RadarrKey == "" {
		return fmt.Errorf("missing API keys (sonarr=%v radarr=%v)",
			s.SonarrKey != "", s.RadarrKey != "")
	}
```

Replace lines 38-59 (the wiring block):

```go
	vpnConfigured := os.Getenv("WIREGUARD_PRIVATE_KEY") != ""

	sonarrWired := true
	radarrWired := true
	prowlarrWired := true

	if vpnConfigured {
		if s.ProwlarrKey == "" {
			slog.Warn("Prowlarr API key not found — skipping download client and indexer wiring", "component", "autowire")
		} else {
			sonarrWired = wireDownloadClient(s, "Sonarr", sonarrURL, s.SonarrKey, "/api/v3", "tv-sonarr")
			radarrWired = wireDownloadClient(s, "Radarr", radarrURL, s.RadarrKey, "/api/v3", "radarr")
			prowlarrWired = wireProwlarrApp(s, "Sonarr", sonarrURL, s.SonarrKey) &&
				wireProwlarrApp(s, "Radarr", radarrURL, s.RadarrKey)
		}
	} else {
		slog.Info("VPN not configured — skipping download client and indexer wiring", "component", "autowire")
	}

	// Root folders are needed regardless of VPN (for library management + import)
	wireRootFolder(s, "Sonarr", sonarrURL, s.SonarrKey, "/api/v3", "/tv")
	wireRootFolder(s, "Radarr", radarrURL, s.RadarrKey, "/api/v3", "/movies")
```

- [ ] **Step 4: Update waitForServices to skip VPN-dependent services**

In `middleware/autowire.go`, update `waitForServices` (lines 67-99). Make qBittorrent and Prowlarr conditional:

```go
func waitForServices(s *ServiceClients) error {
	endpoints := map[string]string{
		"sonarr":   sonarrURL + "/ping",
		"radarr":   radarrURL + "/ping",
		"jellyfin": jellyfinURL + "/System/Info/Public",
	}
	endpoints["bazarr"] = bazarrURL + "/api/system/status"

	vpnConfigured := os.Getenv("WIREGUARD_PRIVATE_KEY") != ""
	if vpnConfigured {
		endpoints["prowlarr"] = prowlarrURL + "/ping"
		endpoints["qbittorrent"] = qbtBaseURL + "/"
	}

	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		allReady := true
		for _, url := range endpoints {
			resp, err := s.client.Get(url)
			if err != nil {
				allReady = false
				break
			}
			notReady := resp.StatusCode >= 500
			resp.Body.Close()
			if notReady {
				allReady = false
				break
			}
		}
		if allReady {
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("timeout waiting for services")
}
```

- [ ] **Step 5: Update import webhook wiring to be conditional on VPN**

In `middleware/autowire.go`, in the `AutoWire` function, make webhook wiring conditional. The import webhooks are useful even without VPN (for manual imports), so keep them. But move them after the conditional VPN block. The existing lines:

```go
	wireImportWebhook(s, "Sonarr", sonarrURL, s.SonarrKey, "/api/v3")
	wireImportWebhook(s, "Radarr", radarrURL, s.RadarrKey, "/api/v3")
```

Keep these as-is (they stay outside the VPN conditional).

- [ ] **Step 6: Run middleware tests**

Run: `cd middleware && go test -v ./...`
Expected: All PASS

- [ ] **Step 7: Commit**

```bash
git add middleware/jellyfin.go middleware/autowire.go
git commit -m "feat(middleware): use JELLYFIN_ADMIN_USER, conditional VPN wiring"
```

---

### Task 6: Middleware — always-on auth for new installs

**Files:**
- Modify: `middleware/main.go:68-81`

- [ ] **Step 1: Simplify auth mode initialization**

In `middleware/main.go`, the auth mode switch (lines 68-81) currently defaults to `"off"`. For new installs, auth is always `"jellyfin"` (set by the new setup wizard). But we must preserve backward compatibility for existing installs that have `PELICULA_AUTH=off`.

The current code already handles this correctly — it reads the env var and acts accordingly. No change is needed here; the always-on behavior is enforced by the setup wizard always writing `PELICULA_AUTH=jellyfin`. Existing installs keep their setting.

Mark this as "no code change needed — enforced at setup wizard level."

- [ ] **Step 2: Commit (skip if no changes)**

No commit needed for this task.

---

### Task 7: Rewrite setup wizard HTML

**Files:**
- Modify: `nginx/setup.html:1-689`

This is the largest task — a full rewrite of the wizard UI with the new 4-step flow.

- [ ] **Step 1: Rewrite the step indicators**

Replace the steps div (lines 20-28):

```html
        <div class="steps">
            <div class="step active" data-step="1"><span class="step-num">1</span> Account</div>
            <div class="step-line"></div>
            <div class="step" data-step="2"><span class="step-num">2</span> Storage</div>
            <div class="step-line"></div>
            <div class="step" data-step="3"><span class="step-num">3</span> VPN</div>
            <div class="step-line"></div>
            <div class="step" data-step="4"><span class="step-num">4</span> <span id="step4-label">Confirm</span></div>
        </div>
```

- [ ] **Step 2: Write Step 1 — Admin Registration**

Replace `panel-1` (lines 33-67) with the registration panel:

```html
        <!-- Step 1: Admin Account -->
        <div class="step-panel active" id="panel-1">
            <h2>Create Admin Account</h2>
            <p class="step-desc">This is your account. You'll use it to log into the dashboard and manage everything.</p>

            <label for="admin-username">Username</label>
            <input type="text" id="admin-username" placeholder="Username" spellcheck="false" autocomplete="off">

            <div class="password-label-row">
                <label for="admin-password">Password</label>
                <a href="#" onclick="generatePassword(); return false;" class="generate-link" id="generate-link">Generate one for me</a>
            </div>

            <div id="password-manual">
                <input type="password" id="admin-password" placeholder="Minimum 8 characters" spellcheck="false" autocomplete="new-password">

                <label for="admin-password-confirm">Confirm Password</label>
                <input type="password" id="admin-password-confirm" placeholder="Confirm password" spellcheck="false" autocomplete="new-password">
            </div>

            <div id="password-generated" class="hidden">
                <div class="generated-password-box">
                    <span id="generated-password-display"></span>
                    <button type="button" class="copy-btn" onclick="copyPassword()" title="Copy password">
                        <span id="copy-icon">📋</span>
                    </button>
                </div>
                <div class="password-warning">⚠️ Save this password — it won't be shown again after setup.</div>
            </div>

            <div class="step-nav">
                <span></span>
                <button onclick="goStep(2)" class="btn-primary">Next</button>
            </div>
        </div>
```

- [ ] **Step 3: Write Step 2 — Storage Paths**

Replace `panel-2` (lines 69-91) with the simplified storage panel:

```html
        <!-- Step 2: Storage -->
        <div class="step-panel" id="panel-2">
            <h2>Storage</h2>
            <p class="step-desc">Where Pelicula stores data. Defaults are auto-detected for your platform.</p>

            <label for="path-config">Config Directory</label>
            <p class="field-help">Settings, databases, service configs</p>
            <input type="text" id="path-config" placeholder="./config">

            <label for="path-media">Media Directory</label>
            <p class="field-help">Movies, TV shows, downloads, processing</p>
            <input type="text" id="path-media" placeholder="~/media">

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
                <button onclick="goStep(1)" class="btn-secondary">Back</button>
                <button onclick="goStep(3)" class="btn-primary">Next</button>
            </div>
        </div>
```

- [ ] **Step 4: Write Step 3 — VPN (optional)**

Replace `panel-3` (lines 93-173) with the optional VPN panel:

```html
        <!-- Step 3: VPN (optional) -->
        <div class="step-panel" id="panel-3">
            <h2>ProtonVPN Setup</h2>
            <p class="step-desc">Required for downloading media. You can skip this and set it up later.</p>

            <label for="vpn-key">WireGuard Private Key</label>
            <p class="field-help">
                Go to <strong>protonvpn.com/account</strong> &rarr; WireGuard &rarr; Create key for Linux.
                <br>Do <strong>not</strong> enable "Moderate NAT" — it breaks port forwarding.
                <br>Requires a paid plan (Plus or higher) with P2P and port forwarding.
            </p>
            <input type="password" id="vpn-key" placeholder="WireGuard private key" spellcheck="false" autocomplete="off">
            <button class="toggle-vis" onclick="toggleVis('vpn-key')" type="button">Show</button>

            <div class="vpn-country-display">
                Server: <strong>Netherlands</strong> <span class="field-help-inline">(best for P2P availability)</span>
            </div>

            <details class="why-protonvpn">
                <summary>Why ProtonVPN? Do you work for them?</summary>
                <div class="why-protonvpn-content">
                    Nope. Pelicula uses <strong>Gluetun</strong> for VPN, which supports 30+ providers.
                    We default to ProtonVPN because it's the only provider that reliably offers
                    <strong>WireGuard + port forwarding + P2P</strong> on all paid plans — the three
                    things a download client needs. You can switch providers later by editing
                    <code>.env</code> and the Gluetun config.
                </div>
            </details>

            <div class="step-nav">
                <button onclick="goStep(2)" class="btn-secondary">Back</button>
                <button onclick="goStep(4)" class="btn-primary">Next</button>
            </div>
            <div class="skip-vpn">
                <a href="#" onclick="skipVPN(); return false;">Skip — I'll set up VPN later</a>
            </div>
        </div>
```

- [ ] **Step 5: Write Step 4 — Confirm and Done panels**

Replace `panel-4` and `panel-done` (lines 176-207):

```html
        <!-- Step 4: Confirm -->
        <div class="step-panel" id="panel-4">
            <h2 id="confirm-heading">Ready to Go</h2>
            <p class="step-desc" id="confirm-desc">Review your settings.</p>

            <div class="summary" id="summary"></div>

            <div class="vpn-skipped-notice hidden" id="vpn-skipped-notice">
                Downloading disabled. Import, processing, and streaming still work. Add VPN later in Settings.
            </div>

            <div class="error-msg hidden" id="setup-error"></div>

            <div class="step-nav">
                <button onclick="goStep(3)" class="btn-secondary">Back</button>
                <button onclick="submitSetup()" class="btn-primary" id="btn-submit">Launch Pelicula</button>
            </div>
        </div>

        <!-- Done screen -->
        <div class="step-panel" id="panel-done">
            <h2 id="done-heading">Starting Up</h2>
            <p class="step-desc" id="done-desc">Configuration saved. Pelicula is now starting all services — this takes about 30-60 seconds.</p>
            <div id="done-spinner" class="spinner"></div>
            <p class="step-desc" id="done-status">Waiting for services...</p>
            <div id="done-restart" class="hidden">
                <div class="restart-notice">
                    Run <code>./pelicula restart</code> for changes to take effect.
                </div>
                <a href="/" class="btn-primary" style="display:inline-block; margin-top:1rem">Back to Dashboard</a>
            </div>
        </div>
```

- [ ] **Step 6: Write the new JavaScript**

Replace the entire `<script>` block (lines 220-687) with the new wizard logic:

```html
<script>
let mode = 'setup';
let detected = {};
let currentSettings = {};
let vpnSkipped = false;
let generatedPassword = '';

(async function init() {
    try {
        const h = await fetch('/api/pelicula/health', { cache: 'no-store' });
        const health = await h.json();

        if (health.status === 'setup') {
            mode = 'setup';
            const r = await fetch('/api/pelicula/setup/detect');
            detected = await r.json();
            document.getElementById('path-config').value = detected.config_dir || '';
            document.getElementById('path-media').value = detected.library_dir || '';

            const badge = document.getElementById('platform-badge');
            const labels = { synology: 'Synology NAS', macos: 'macOS', wsl: 'WSL', linux: 'Linux' };
            badge.textContent = labels[detected.platform] || detected.platform;
        } else {
            mode = 'settings';
            applySettingsMode();
            const r = await fetch('/api/pelicula/settings');
            if (r.ok) {
                currentSettings = await r.json();
                prefillSettings(currentSettings);
            }
        }
    } catch(e) {
        console.error('init failed', e);
    }
})();

function applySettingsMode() {
    document.getElementById('page-subtitle').textContent = 'Settings';
    document.getElementById('step4-label').textContent = 'Review';
    document.getElementById('confirm-heading').textContent = 'Review Changes';
    document.getElementById('confirm-desc').textContent = 'Review your configuration changes. A restart is required for changes to take effect.';
    document.getElementById('btn-submit').textContent = 'Save Settings';
    document.getElementById('platform-badge').style.display = 'none';
    // In settings mode, hide registration step and start at step 2 (storage)
    // TODO: settings mode will be addressed separately — for now, this is setup-only
}

function prefillSettings(s) {
    // Settings mode pre-fill — preserves existing behavior for settings page
    document.getElementById('path-config').value = s.config_dir || '';
    document.getElementById('path-media').value = s.library_dir || '';
    if (s.library_dir !== s.work_dir) {
        document.getElementById('path-library').value = s.library_dir || '';
        document.getElementById('path-work').value = s.work_dir || '';
        document.getElementById('advanced-paths').open = true;
    }
}

async function generatePassword() {
    try {
        const r = await fetch('/api/pelicula/setup/generate-password');
        const data = await r.json();
        generatedPassword = data.password;

        document.getElementById('password-manual').classList.add('hidden');
        document.getElementById('password-generated').classList.remove('hidden');
        document.getElementById('generated-password-display').textContent = generatedPassword;
        document.getElementById('generate-link').textContent = 'Generate another';
    } catch(e) {
        console.error('generate password failed', e);
    }
}

function copyPassword() {
    navigator.clipboard.writeText(generatedPassword).then(() => {
        const icon = document.getElementById('copy-icon');
        icon.textContent = '✓';
        icon.style.color = '#4CAF50';
        setTimeout(() => { icon.textContent = '📋'; icon.style.color = ''; }, 1500);
    });
}

function skipVPN() {
    vpnSkipped = true;
    goStep(4);
}

function toggleVis(id) {
    const el = document.getElementById(id);
    const btn = el.nextElementSibling;
    if (el.type === 'password') {
        el.type = 'text';
        btn.textContent = 'Hide';
    } else {
        el.type = 'password';
        btn.textContent = 'Show';
    }
}

function getPassword() {
    if (generatedPassword) return generatedPassword;
    return document.getElementById('admin-password').value;
}

function goStep(n) {
    // Validate current step before advancing
    if (n === 2) {
        // Validate step 1: admin account
        const username = document.getElementById('admin-username').value.trim();
        if (!username) {
            const el = document.getElementById('admin-username');
            el.focus();
            el.classList.add('input-error');
            return;
        }
        document.getElementById('admin-username').classList.remove('input-error');

        const pw = getPassword();
        if (!pw || pw.length < 8) {
            const el = document.getElementById('admin-password');
            el.focus();
            el.classList.add('input-error');
            el.placeholder = 'Minimum 8 characters';
            return;
        }
        document.getElementById('admin-password').classList.remove('input-error');

        if (!generatedPassword) {
            const confirm = document.getElementById('admin-password-confirm').value;
            if (pw !== confirm) {
                const el = document.getElementById('admin-password-confirm');
                el.focus();
                el.classList.add('input-error');
                el.placeholder = 'Passwords do not match';
                return;
            }
            document.getElementById('admin-password-confirm').classList.remove('input-error');
        }
    }

    if (n === 3) {
        // Validate step 2: storage paths
        for (const id of ['path-config', 'path-media']) {
            const el = document.getElementById(id);
            if (!el.value.trim()) {
                el.focus();
                el.classList.add('input-error');
                return;
            }
            el.classList.remove('input-error');
        }
        vpnSkipped = false; // reset skip when navigating normally
    }

    if (n === 4) {
        // Validate step 3: VPN (only if not skipping)
        if (!vpnSkipped) {
            const key = document.getElementById('vpn-key').value.trim();
            if (mode === 'setup' && key) {
                if (key.length !== 44 || !key.endsWith('=')) {
                    const el = document.getElementById('vpn-key');
                    el.focus();
                    el.classList.add('input-error');
                    el.placeholder = 'Must be 44-character base64 key ending in =';
                    return;
                }
            }
            if (mode === 'setup' && !key) {
                // No key and not explicitly skipping — treat as skip
                vpnSkipped = true;
            }
            document.getElementById('vpn-key').classList.remove('input-error');
        }
        buildSummary();
    }

    document.querySelectorAll('.step-panel').forEach(p => p.classList.remove('active'));
    document.querySelectorAll('.step').forEach(s => {
        const sn = parseInt(s.dataset.step);
        s.classList.toggle('active', sn === n);
        s.classList.toggle('done', sn < n);
    });
    const panel = document.getElementById('panel-' + n);
    if (panel) panel.classList.add('active');
}

function buildSummary() {
    const username = document.getElementById('admin-username').value.trim();
    const config = document.getElementById('path-config').value;
    const media = document.getElementById('path-media').value;
    const key = document.getElementById('vpn-key').value.trim();

    let html = '<table>';
    html += row('Admin', username);
    html += row('Config', config);
    html += row('Media', media);

    // Show advanced paths if expanded
    const libEl = document.getElementById('path-library');
    const workEl = document.getElementById('path-work');
    if (libEl.value.trim() && libEl.value.trim() !== media) {
        html += row('Library', libEl.value.trim());
    }
    if (workEl.value.trim() && workEl.value.trim() !== media) {
        html += row('Work', workEl.value.trim());
    }

    if (vpnSkipped) {
        html += row('VPN', '<span style="color:#FF9800">Skipped</span>');
        document.getElementById('vpn-skipped-notice').classList.remove('hidden');
    } else {
        html += row('VPN', '<span style="color:#4CAF50">Netherlands ✓</span>');
        document.getElementById('vpn-skipped-notice').classList.add('hidden');
    }
    html += '</table>';
    document.getElementById('summary').innerHTML = html;
}

function row(label, value) {
    return '<tr><td class="sum-label">' + esc(label) + '</td><td>' + value + '</td></tr>';
}

function esc(s) {
    const d = document.createElement('div');
    d.textContent = s;
    return d.innerHTML;
}

async function submitSetup() {
    const btn = document.getElementById('btn-submit');
    btn.disabled = true;
    btn.textContent = 'Saving...';

    const errEl = document.getElementById('setup-error');
    errEl.classList.add('hidden');

    const body = {
        admin_username: document.getElementById('admin-username').value.trim(),
        admin_password: getPassword(),
        config_dir: document.getElementById('path-config').value.trim(),
        media_dir: document.getElementById('path-media').value.trim(),
        library_dir: document.getElementById('path-library').value.trim() || '',
        work_dir: document.getElementById('path-work').value.trim() || '',
        wireguard_key: vpnSkipped ? '' : document.getElementById('vpn-key').value.trim(),
        vpn_skipped: vpnSkipped,
    };

    const endpoint = mode === 'settings' ? '/api/pelicula/settings' : '/api/pelicula/setup';

    try {
        const r = await fetch(endpoint, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(body),
        });
        if (!r.ok) {
            const text = await r.text();
            throw new Error(text || r.statusText);
        }

        document.querySelectorAll('.step-panel').forEach(p => p.classList.remove('active'));
        document.querySelectorAll('.step').forEach(s => s.classList.add('done'));
        document.getElementById('panel-done').classList.add('active');

        if (mode === 'settings') {
            showSettingsDone();
        } else {
            // Store credentials for auto-login after stack starts
            sessionStorage.setItem('pelicula_setup_user', body.admin_username);
            sessionStorage.setItem('pelicula_setup_pass', body.admin_password);
            pollForDashboard();
        }
    } catch(e) {
        errEl.textContent = 'Setup failed: ' + e.message;
        errEl.classList.remove('hidden');
        btn.disabled = false;
        btn.textContent = mode === 'settings' ? 'Save Settings' : 'Launch Pelicula';
    }
}

function showSettingsDone() {
    document.getElementById('done-heading').textContent = 'Settings Saved';
    document.getElementById('done-desc').textContent = 'Your configuration has been updated.';
    document.getElementById('done-spinner').style.display = 'none';
    document.getElementById('done-status').style.display = 'none';
    document.getElementById('done-restart').classList.remove('hidden');
}

async function pollForDashboard() {
    const status = document.getElementById('done-status');
    for (let i = 0; i < 90; i++) {
        await new Promise(r => setTimeout(r, 2000));
        try {
            const r = await fetch('/api/pelicula/health', { cache: 'no-store' });
            const data = await r.json();
            if (data.status && data.status !== 'setup') {
                status.textContent = 'Services are up! Logging you in...';
                await autoLogin();
                return;
            }
        } catch(e) {
            // Expected: containers restarting
        }
        status.textContent = 'Waiting for services... (' + ((i+1)*2) + 's)';
    }
    document.querySelector('.spinner').style.animationPlayState = 'paused';
    document.querySelector('.spinner').style.borderTopColor = '#555';
    status.textContent = 'Taking longer than expected. Check the terminal for details, or reload this page.';
}

async function autoLogin() {
    const username = sessionStorage.getItem('pelicula_setup_user');
    const password = sessionStorage.getItem('pelicula_setup_pass');
    sessionStorage.removeItem('pelicula_setup_user');
    sessionStorage.removeItem('pelicula_setup_pass');

    if (!username || !password) {
        window.location.href = '/';
        return;
    }

    try {
        const r = await fetch('/api/pelicula/auth/login', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ username, password }),
        });
        if (r.ok) {
            window.location.href = '/';
            return;
        }
    } catch(e) {
        console.error('auto-login failed', e);
    }
    // Fallback: redirect anyway, user will see login screen
    window.location.href = '/';
}
</script>
```

- [ ] **Step 7: Add CSS for new elements**

Add to `nginx/setup.css` the styles for new elements:

```css
.password-label-row {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
}
.generate-link {
    font-size: 0.75rem;
    color: var(--accent, #a78bfa);
    text-decoration: none;
}
.generate-link:hover {
    text-decoration: underline;
}
.generated-password-box {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.6rem 0.8rem;
    background: rgba(167, 139, 250, 0.06);
    border: 1px solid rgba(167, 139, 250, 0.2);
    border-radius: 0.4rem;
    font-family: monospace;
    font-size: 1rem;
    letter-spacing: 0.03em;
    color: var(--text, #e0e0e0);
    margin-bottom: 0.5rem;
}
.generated-password-box .copy-btn {
    background: none;
    border: none;
    cursor: pointer;
    font-size: 1rem;
    padding: 0.2rem;
    margin-left: auto;
}
.password-warning {
    font-size: 0.75rem;
    color: #FF9800;
    padding: 0.5rem 0.6rem;
    background: rgba(255, 152, 0, 0.06);
    border-radius: 0.3rem;
    border: 1px solid rgba(255, 152, 0, 0.15);
}
.advanced-toggle {
    font-size: 0.8rem;
    color: #888;
    cursor: pointer;
    user-select: none;
    margin-top: 0.5rem;
}
.advanced-content {
    margin-top: 0.75rem;
    padding: 0.75rem;
    background: rgba(255,255,255,0.02);
    border-radius: 0.4rem;
    border: 1px solid rgba(255,255,255,0.06);
}
.vpn-country-display {
    margin: 0.75rem 0;
    padding: 0.5rem 0.75rem;
    background: rgba(255,255,255,0.02);
    border-radius: 0.3rem;
    font-size: 0.85rem;
    color: #ccc;
}
.field-help-inline {
    font-size: 0.75rem;
    color: #666;
}
.why-protonvpn {
    margin: 0.5rem 0;
}
.why-protonvpn summary {
    font-size: 0.8rem;
    color: #888;
    cursor: pointer;
}
.why-protonvpn-content {
    font-size: 0.8rem;
    color: #aaa;
    margin-top: 0.5rem;
    padding: 0.6rem;
    background: rgba(255,255,255,0.03);
    border-radius: 0.4rem;
    line-height: 1.6;
}
.skip-vpn {
    text-align: center;
    margin-top: 0.75rem;
}
.skip-vpn a {
    font-size: 0.8rem;
    color: #888;
    text-decoration: none;
    border-bottom: 1px dashed #555;
}
.skip-vpn a:hover {
    color: #aaa;
}
.vpn-skipped-notice {
    padding: 0.6rem 0.8rem;
    background: rgba(255, 152, 0, 0.06);
    border: 1px solid rgba(255, 152, 0, 0.15);
    border-radius: 0.4rem;
    font-size: 0.8rem;
    color: #FF9800;
    line-height: 1.5;
    margin-top: 0.75rem;
}
.hidden { display: none !important; }
```

- [ ] **Step 8: Commit**

```bash
git add nginx/setup.html nginx/setup.css
git commit -m "feat(ui): rewrite setup wizard — admin registration, optional VPN, auto-login"
```

---

### Task 8: Dashboard VPN status banner

**Files:**
- Modify: `middleware/main.go:185-214` (handleStatus)
- Modify: `nginx/dashboard.js:12-31` (checkAuth area)

- [ ] **Step 1: Add vpn_configured to status response**

In `middleware/main.go`, in `handleStatus` (around line 207), add the VPN field:

```go
	status := map[string]any{
		"status":         "ok",
		"services":       services.CheckHealth(),
		"wired":          services.IsWired(),
		"indexers":       indexerCount,
		"vpn_configured": os.Getenv("WIREGUARD_PRIVATE_KEY") != "",
	}
```

- [ ] **Step 2: Add VPN banner to dashboard**

In `nginx/dashboard.js`, after the `checkAuth` function (after line 31), add a function that checks VPN status and shows a banner:

```javascript
async function checkVPNStatus() {
    try {
        const res = await tfetch('/api/pelicula/status');
        if (!res.ok) return;
        const data = await res.json();
        if (data.vpn_configured === false) {
            const banner = document.createElement('div');
            banner.className = 'vpn-banner';
            banner.innerHTML = '⚡ VPN not configured — downloading is disabled. <a href="/settings">Set up VPN →</a>';
            const main = document.querySelector('.main-content') || document.body;
            main.prepend(banner);
        }
    } catch(e) { /* non-critical */ }
}
```

Call it after `checkAuth()` at the bottom of the file (around line 1439):

```javascript
checkAuth();
checkVPNStatus();
```

- [ ] **Step 3: Add VPN banner CSS**

Add to `nginx/styles.css`:

```css
.vpn-banner {
    padding: 0.6rem 1rem;
    background: rgba(255, 152, 0, 0.08);
    border: 1px solid rgba(255, 152, 0, 0.2);
    border-radius: 0.4rem;
    font-size: 0.85rem;
    color: #FF9800;
    margin-bottom: 1rem;
}
.vpn-banner a {
    color: #FF9800;
    font-weight: 600;
}
```

- [ ] **Step 4: Commit**

```bash
git add middleware/main.go nginx/dashboard.js nginx/styles.css
git commit -m "feat(dashboard): VPN status banner when download services are disabled"
```

---

### Task 9: Integration testing and final verification

**Files:**
- No new files — run existing tests and verify manually

- [ ] **Step 1: Run all unit tests**

Run: `make test`
Expected: All tests pass across procula, middleware, and CLI

- [ ] **Step 2: Verify Docker Compose validates**

Run: `docker compose -f docker-compose.yml config --quiet`
Expected: No errors

Run: `docker compose -f docker-compose.yml --profile vpn config --quiet`
Expected: No errors

- [ ] **Step 3: Verify CLI compiles and help is correct**

Run: `cd cmd/pelicula && go build -o /tmp/pelicula . && /tmp/pelicula --help`
Expected: No `setup` command in output; `up` description says "(runs setup wizard on first run)"

- [ ] **Step 4: Commit any fixups**

If any test fixes were needed, commit them:
```bash
git add -A
git commit -m "fix: address test failures from first-time experience changes"
```

---

## Verification Checklist

After all tasks are complete:

1. **Fresh install with VPN**: Delete `.env`, run `./pelicula up` → wizard opens → register admin → set paths → enter WireGuard key → confirm → auto-login → dashboard with all services running
2. **Fresh install without VPN**: Same flow but skip VPN → dashboard shows VPN banner → only import/process/play available → gluetun/qbt/prowlarr containers not running
3. **Existing install upgrade**: `.env` with `JELLYFIN_ADMIN_USER` missing → `MigrateEnv` adds `JELLYFIN_ADMIN_USER=admin` → no behavior change
4. **Generated password**: Click "Generate one for me" → password shown → copy button works → can log in after setup
5. **Advanced paths**: Toggle advanced → set different library/work dirs → verify both written to `.env`
6. **Auth always on**: New `.env` always has `PELICULA_AUTH=jellyfin`
7. **`pelicula setup` removed**: `./pelicula setup` → "unknown command: setup"
