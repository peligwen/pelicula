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
	ConfigDir    string `json:"config_dir"`
	MediaDir     string `json:"media_dir"`
	LibraryDir   string `json:"library_dir"`
	WorkDir      string `json:"work_dir"`
	WireguardKey string `json:"wireguard_key"`
	VPNSkipped   bool   `json:"vpn_skipped"`
	LANUrl       string `json:"lan_url"`
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
	LANUrl     string `json:"lan_url"`
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
	// The CLI sets these when starting compose/docker-compose.setup.yml.
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

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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

	// Sanitize all string fields
	for _, check := range []struct{ name, val string }{
		{"wireguard_key", req.WireguardKey},
		{"config_dir", req.ConfigDir},
		{"media_dir", req.MediaDir},
		{"library_dir", req.LibraryDir},
		{"work_dir", req.WorkDir},
		{"lan_url", req.LANUrl},
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
		"PROCULA_API_KEY":       proculaKey,
		"WEBHOOK_SECRET":        webhookSecret,
		"TRANSCODING_ENABLED":   "false",
		"NOTIFICATIONS_ENABLED": "false",
		"NOTIFICATIONS_MODE":    "internal",
		"PELICULA_SUB_LANGS":    "en",
	}

	// Only persist JELLYFIN_PUBLISHED_URL when the user provided a value.
	if lan := strings.TrimSpace(req.LANUrl); lan != "" {
		vars["JELLYFIN_PUBLISHED_URL"] = lan
	}

	if err := writeEnvFile(envPath, vars); err != nil {
		slog.Error("failed to write .env", "error", err)
		http.Error(w, "failed to write config", http.StatusInternalServerError)
		return
	}

	slog.Info("setup wizard completed", "component", "setup", "vpn", !req.VPNSkipped)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// generateReadablePassword returns a 4-word passphrase like "calm-tiger-sobre-leaps",
// drawn from weightedPassphraseWords (wordlist.go). All lowercase, hyphen-separated.
// 5-letter words are most likely; 3- and 7-letter words are rare (bell curve).
func generateReadablePassword() string {
	n := len(weightedPassphraseWords)
	return weightedPassphraseWords[cryptoRandN(n)] + "-" +
		weightedPassphraseWords[cryptoRandN(n)] + "-" +
		weightedPassphraseWords[cryptoRandN(n)] + "-" +
		weightedPassphraseWords[cryptoRandN(n)]
}

// cryptoRandN returns a cryptographically random integer in [0, n).
func cryptoRandN(n int) int {
	max := big.NewInt(int64(n))
	v, err := rand.Int(rand.Reader, max)
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

func generateAPIKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read should never fail; log and proceed with whatever
		// partial bytes were written (consistent with generateReadablePassword).
		slog.Error("crypto/rand.Read failed generating API key", "error", err)
	}
	return hex.EncodeToString(b)
}
