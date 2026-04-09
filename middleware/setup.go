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
	WireguardKey string `json:"wireguard_key"`
	Country      string `json:"country"`
	ConfigDir    string `json:"config_dir"`
	LibraryDir   string `json:"library_dir"`
	WorkDir      string `json:"work_dir"`
	Port         string `json:"port"`
	AuthEnabled  bool   `json:"auth_enabled"`
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

func handleSetupSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Refuse if .env already exists
	if _, err := os.Stat(envPath); err == nil {
		http.Error(w, "already configured", http.StatusConflict)
		return
	}

	var req SetupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Sanitize all string fields: reject values containing characters
	// that could corrupt the .env file format.
	for _, check := range []struct {
		name, val string
	}{
		{"wireguard_key", req.WireguardKey},
		{"country", req.Country},
		{"config_dir", req.ConfigDir},
		{"library_dir", req.LibraryDir},
		{"work_dir", req.WorkDir},
		{"port", req.Port},
	} {
		if strings.ContainsAny(check.val, "\"\n\r") {
			http.Error(w, check.name+" contains invalid characters", http.StatusBadRequest)
			return
		}
	}

	// Validate WireGuard key: must be 44-char base64 ending in =
	key := strings.TrimSpace(req.WireguardKey)
	if key == "" {
		http.Error(w, "wireguard_key is required", http.StatusBadRequest)
		return
	}
	if len(key) != 44 || key[43] != '=' {
		http.Error(w, "wireguard_key must be a 44-character base64 WireGuard private key", http.StatusBadRequest)
		return
	}
	req.WireguardKey = key

	// Defaults
	if req.Country == "" {
		req.Country = "Netherlands"
	}
	if req.Port == "" {
		req.Port = "7354"
	}

	// Use host-detected defaults for paths if not provided
	if req.ConfigDir == "" {
		req.ConfigDir = envOr("HOST_CONFIG_DIR", "./config")
	}
	if req.LibraryDir == "" {
		req.LibraryDir = envOr("HOST_LIBRARY_DIR", "~/media")
	}
	if req.WorkDir == "" {
		req.WorkDir = envOr("HOST_WORK_DIR", "~/media")
	}

	authMode := "off"
	if req.AuthEnabled {
		authMode = "jellyfin"
	}

	puid := envOr("HOST_PUID", "1000")
	pgid := envOr("HOST_PGID", "1000")
	tz := envOr("HOST_TZ", "America/New_York")
	proculaKey := generateAPIKey()
	webhookSecret := generateAPIKey()
	jellyfinPassword := generateReadablePassword()

	envMu.Lock()
	defer envMu.Unlock()

	vars := map[string]string{
		"CONFIG_DIR":            req.ConfigDir,
		"LIBRARY_DIR":           req.LibraryDir,
		"WORK_DIR":              req.WorkDir,
		"PUID":                  puid,
		"PGID":                  pgid,
		"TZ":                    tz,
		"WIREGUARD_PRIVATE_KEY": req.WireguardKey,
		"SERVER_COUNTRIES":      req.Country,
		"PELICULA_PORT":         req.Port,
		"PELICULA_AUTH":         authMode,
		"JELLYFIN_PASSWORD":     jellyfinPassword,
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

	slog.Info("setup wizard completed, wrote .env", "component", "setup")

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
