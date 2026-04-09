package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"strings"
)

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

// SetupDetect is returned by GET /api/pelicula/setup/detect.
// All values are passed from the host via env vars set by the CLI.
type SetupDetect struct {
	Platform   string `json:"platform"`
	TZ         string `json:"tz"`
	PUID       string `json:"puid"`
	PGID       string `json:"pgid"`
	ConfigDir  string `json:"config_dir"`
	LibraryDir string `json:"library_dir"`
	WorkDir    string `json:"work_dir"`
}

func isSetupMode() bool {
	return os.Getenv("SETUP_MODE") == "true"
}

func handleSetupDetect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// All detection is done on the host and passed via env vars.
	// The CLI sets these when starting docker-compose.setup.yml.
	resp := SetupDetect{
		Platform:   envOr("HOST_PLATFORM", "linux"),
		TZ:         envOr("HOST_TZ", "America/New_York"),
		PUID:       envOr("HOST_PUID", "1000"),
		PGID:       envOr("HOST_PGID", "1000"),
		ConfigDir:  envOr("HOST_CONFIG_DIR", "./config"),
		LibraryDir: envOr("HOST_LIBRARY_DIR", "~/media"),
		WorkDir:    envOr("HOST_WORK_DIR", "~/media"),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

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
		"JELLYFIN_ADMIN_USER":   req.AdminUsername,
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// generateReadablePassword creates a 15-char password in 3 groups of 5,
// using only unambiguous characters. Uses rejection sampling to avoid
// modulo bias.
func generateReadablePassword() string {
	const charset = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	max := big.NewInt(int64(len(charset)))
	b := make([]byte, 15)
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			b[i] = charset[0]
			continue
		}
		b[i] = charset[n.Int64()]
	}
	return string(b[:5]) + "-" + string(b[5:10]) + "-" + string(b[10:15])
}

func generateAPIKey() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
