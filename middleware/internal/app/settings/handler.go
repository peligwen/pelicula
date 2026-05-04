// Package settings provides HTTP handlers for reading, updating, and resetting
// the .env configuration file via the pelicula-api admin endpoints.
package settings

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"pelicula-api/httputil"
	appsetup "pelicula-api/internal/app/setup"
	"pelicula-api/internal/envfile"
)

const maskedValue = "••••••••"

// Applier is the optional set of in-process callbacks the settings handler
// uses to apply changes that don't require `pelicula up`. Wired in bootstrap
// from concrete dependencies (docker client, remoteconfig package). Nil-safe:
// if Applier or any callback is nil, the corresponding change is reported as
// pending instead of applied.
type Applier struct {
	// SeedJellyfinNetworkXML rewrites Jellyfin's network.xml so the LAN
	// PublishedServerUrl matches the new value. The caller-side function
	// should write the file; RestartJellyfin then reloads it.
	SeedJellyfinNetworkXML func(publishedURL string) error
	// RestartJellyfin restarts the Jellyfin container so it re-reads
	// network.xml.
	RestartJellyfin func() error
	// SetOpenRegistration updates the in-memory open-registration flag in
	// peligrosa so the change takes effect without a middleware restart. Wired
	// in bootstrap to peligrosa.SetOpenRegistration.
	SetOpenRegistration func(bool)
}

// Handler handles settings GET/POST and reset endpoints.
type Handler struct {
	EnvPath        string
	GenerateAPIKey func() string
	// Apply, when non-nil, lets the handler apply some changes in-place
	// (Jellyfin published URL re-seed + restart). Other changes (compose-level
	// — port mappings, sidecar add/remove) are always returned as pending.
	Apply *Applier
	// EnvMu serializes .env reads/writes across handlers. Set by bootstrap to
	// a shared mutex; nil-safe — falls back to a private mu if not set (test
	// compatibility).
	EnvMu sync.Locker
	mu    sync.Mutex // fallback if EnvMu is nil
}

// New constructs a Handler with the given .env path and API key generator.
func New(envPath string, generateAPIKey func() string) *Handler {
	return &Handler{
		EnvPath:        envPath,
		GenerateAPIKey: generateAPIKey,
	}
}

func (h *Handler) envMu() sync.Locker {
	if h.EnvMu != nil {
		return h.EnvMu
	}
	return &h.mu
}

// settingsResponse is returned by GET /api/pelicula/settings.
type settingsResponse struct {
	WireguardKey         string `json:"wireguard_key"`
	Country              string `json:"country"`
	ConfigDir            string `json:"config_dir"`
	LibraryDir           string `json:"library_dir"`
	WorkDir              string `json:"work_dir"`
	Port                 string `json:"port"`
	OpenRegistration     string `json:"open_registration"`
	ProculaAPIKey        string `json:"procula_api_key"`
	TranscodingEnabled   string `json:"transcoding_enabled"`
	NotificationsEnabled string `json:"notifications_enabled"`
	NotificationsMode    string `json:"notifications_mode"`
	SubLangs             string `json:"sub_langs"`
	TZ                   string `json:"tz"`
	PUID                 string `json:"puid"`
	PGID                 string `json:"pgid"`
	// Seeding
	SeedingRemoveOnComplete string `json:"seeding_remove_on_complete"`
	// Request queue: per-type quality profile and root folder for approved requests
	RequestsRadarrProfileID string `json:"requests_radarr_profile_id"`
	RequestsRadarrRoot      string `json:"requests_radarr_root"`
	RequestsSonarrProfileID string `json:"requests_sonarr_profile_id"`
	RequestsSonarrRoot      string `json:"requests_sonarr_root"`
	// Search
	SearchMode string `json:"search_mode"` // "tmdb" (default) or "indexer"
	// Jellyfin client discovery
	LanUrl string `json:"lan_url"`
}

// HandleSettings dispatches GET (read settings) and POST (update settings).
func (h *Handler) HandleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleSettingsGet(w, r)
	case http.MethodPost:
		h.handleSettingsUpdate(w, r)
	default:
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	mu := h.envMu()
	mu.Lock()
	vars, err := ParseEnvFile(h.EnvPath)
	mu.Unlock()

	if err != nil {
		if os.IsNotExist(err) {
			httputil.WriteError(w, "not configured", http.StatusNotFound)
			return
		}
		slog.Error("failed to read .env", "error", err)
		httputil.WriteError(w, "failed to read config", http.StatusInternalServerError)
		return
	}

	// JELLYFIN_API_KEY is intentionally excluded from settingsResponse — it is
	// a bearer credential equivalent to a password and must never be sent to
	// the browser.  Keep it absent rather than masked so that future changes
	// adding a JellyfinAPIKey field to settingsResponse catch this comment.
	resp := settingsResponse{
		WireguardKey:            maskedValue,
		Country:                 vars["SERVER_COUNTRIES"],
		ConfigDir:               vars["CONFIG_DIR"],
		LibraryDir:              vars["LIBRARY_DIR"],
		WorkDir:                 vars["WORK_DIR"],
		Port:                    vars["PELICULA_PORT"],
		OpenRegistration:        vars["PELICULA_OPEN_REGISTRATION"],
		ProculaAPIKey:           maskedValue,
		TranscodingEnabled:      vars["TRANSCODING_ENABLED"],
		NotificationsEnabled:    vars["NOTIFICATIONS_ENABLED"],
		NotificationsMode:       vars["NOTIFICATIONS_MODE"],
		SubLangs:                vars["PELICULA_SUB_LANGS"],
		TZ:                      vars["TZ"],
		PUID:                    vars["PUID"],
		PGID:                    vars["PGID"],
		SeedingRemoveOnComplete: vars["SEEDING_REMOVE_ON_COMPLETE"],
		RequestsRadarrProfileID: vars["REQUESTS_RADARR_PROFILE_ID"],
		RequestsRadarrRoot:      vars["REQUESTS_RADARR_ROOT"],
		RequestsSonarrProfileID: vars["REQUESTS_SONARR_PROFILE_ID"],
		RequestsSonarrRoot:      vars["REQUESTS_SONARR_ROOT"],
		SearchMode:              vars["SEARCH_MODE"],
		LanUrl:                  vars["JELLYFIN_PUBLISHED_URL"],
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp) //nolint:errcheck
}

func (h *Handler) handleSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req settingsResponse
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate non-sensitive string fields
	toCheck := []struct{ name, val string }{
		{"country", req.Country},
		{"config_dir", req.ConfigDir},
		{"library_dir", req.LibraryDir},
		{"work_dir", req.WorkDir},
		{"port", req.Port},
		{"tz", req.TZ},
		{"sub_langs", req.SubLangs},
	}
	for _, c := range toCheck {
		if c.val != "" && strings.ContainsAny(c.val, "\"\n\r") {
			httputil.WriteError(w, c.name+" contains invalid characters", http.StatusBadRequest)
			return
		}
	}

	if err := validatePort(req.Port); err != nil {
		httputil.WriteError(w, "port "+err.Error(), http.StatusBadRequest)
		return
	}

	// Validate WireGuard key only if being changed
	if req.WireguardKey != "" && req.WireguardKey != maskedValue {
		key := strings.TrimSpace(req.WireguardKey)
		if len(key) != 44 || key[43] != '=' {
			httputil.WriteError(w, "wireguard_key must be a 44-character base64 WireGuard private key", http.StatusBadRequest)
			return
		}
		req.WireguardKey = key
	}

	mu := h.envMu()
	mu.Lock()
	defer mu.Unlock()

	vars, err := ParseEnvFile(h.EnvPath)
	if err != nil {
		slog.Error("failed to read .env for update", "error", err)
		httputil.WriteError(w, "failed to read config", http.StatusInternalServerError)
		return
	}

	// Snapshot pre-merge values so we can diff at the end and decide what
	// can be applied in-place vs what genuinely needs `pelicula up`.
	prev := snapshotApplyKeys(vars)

	// Merge changed fields; skip masked/empty values
	if req.WireguardKey != "" && req.WireguardKey != maskedValue {
		vars["WIREGUARD_PRIVATE_KEY"] = req.WireguardKey
	}
	if req.Country != "" {
		vars["SERVER_COUNTRIES"] = req.Country
	}
	if req.ConfigDir != "" {
		vars["CONFIG_DIR"] = req.ConfigDir
	}
	if req.LibraryDir != "" {
		vars["LIBRARY_DIR"] = req.LibraryDir
	}
	if req.WorkDir != "" {
		vars["WORK_DIR"] = req.WorkDir
	}
	if req.Port != "" {
		vars["PELICULA_PORT"] = req.Port
	}
	if req.OpenRegistration != "" {
		switch req.OpenRegistration {
		case "true", "false":
			vars["PELICULA_OPEN_REGISTRATION"] = req.OpenRegistration
		default:
			httputil.WriteError(w, "open_registration must be true or false", http.StatusBadRequest)
			return
		}
	}
	if req.TZ != "" {
		vars["TZ"] = req.TZ
	}
	if req.PUID != "" {
		vars["PUID"] = req.PUID
	}
	if req.PGID != "" {
		vars["PGID"] = req.PGID
	}
	if req.TranscodingEnabled != "" {
		vars["TRANSCODING_ENABLED"] = req.TranscodingEnabled
	}
	if req.NotificationsEnabled != "" {
		vars["NOTIFICATIONS_ENABLED"] = req.NotificationsEnabled
	}
	if req.NotificationsMode != "" {
		vars["NOTIFICATIONS_MODE"] = req.NotificationsMode
	}
	if req.SubLangs != "" {
		vars["PELICULA_SUB_LANGS"] = req.SubLangs
	}
	// Drop legacy key on every write so old .env files converge without it.
	delete(vars, "REMOTE_ACCESS_ENABLED")
	if req.SeedingRemoveOnComplete != "" {
		switch req.SeedingRemoveOnComplete {
		case "true", "false":
			vars["SEEDING_REMOVE_ON_COMPLETE"] = req.SeedingRemoveOnComplete
		default:
			httputil.WriteError(w, "seeding_remove_on_complete must be true or false", http.StatusBadRequest)
			return
		}
	}
	if req.RequestsRadarrProfileID != "" {
		vars["REQUESTS_RADARR_PROFILE_ID"] = req.RequestsRadarrProfileID
	}
	if req.RequestsRadarrRoot != "" {
		vars["REQUESTS_RADARR_ROOT"] = req.RequestsRadarrRoot
	}
	if req.RequestsSonarrProfileID != "" {
		vars["REQUESTS_SONARR_PROFILE_ID"] = req.RequestsSonarrProfileID
	}
	if req.RequestsSonarrRoot != "" {
		vars["REQUESTS_SONARR_ROOT"] = req.RequestsSonarrRoot
	}
	if req.SearchMode != "" {
		switch req.SearchMode {
		case "tmdb", "indexer":
			vars["SEARCH_MODE"] = req.SearchMode
		default:
			httputil.WriteError(w, "search_mode must be tmdb or indexer", http.StatusBadRequest)
			return
		}
	}
	if req.LanUrl != "" {
		if strings.ContainsAny(req.LanUrl, "\"\n\r") {
			httputil.WriteError(w, "lan_url contains invalid characters", http.StatusBadRequest)
			return
		}
		vars["JELLYFIN_PUBLISHED_URL"] = req.LanUrl
	}

	if err := WriteEnvFile(h.EnvPath, vars); err != nil {
		slog.Error("failed to write .env", "error", err)
		httputil.WriteError(w, "failed to write config", http.StatusInternalServerError)
		return
	}

	slog.Info("settings updated via browser wizard", "component", "settings")

	// Apply open_registration in-process immediately so the public
	// /register/check endpoint reflects the change without a restart.
	// The Applier callback is wired in bootstrap to peligrosa.SetOpenRegistration.
	if req.OpenRegistration != "" && h.Apply != nil && h.Apply.SetOpenRegistration != nil {
		h.Apply.SetOpenRegistration(vars["PELICULA_OPEN_REGISTRATION"] == "true")
	}

	// Compute applied/pending. .env is already on disk so the user sees their
	// values persist on reload regardless of which bucket each change lands in.
	applied, pending := h.applyChanges(prev, snapshotApplyKeys(vars))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"status":               "ok",
		"applied":              applied,
		"pending":              pending,
		"requires_pelicula_up": len(pending) > 0,
		// Backwards compatibility with older settings.js builds: a true value
		// here triggers their "saved — restart needed" toast. New builds
		// prefer applied/pending and ignore this.
		"restart_required": len(pending) > 0,
	})
}

// applyKeys is the subset of .env keys that the handler diffs and acts on.
type applyKeys struct {
	JellyfinPublishedURL string
}

func snapshotApplyKeys(vars map[string]string) applyKeys {
	return applyKeys{
		JellyfinPublishedURL: vars["JELLYFIN_PUBLISHED_URL"],
	}
}

// applyChanges diffs old vs new applyKeys and dispatches the changes the
// handler can do in-place. Returns (applied, pending) human-readable lists
// suitable for the dashboard banner.
//
// In-place:
//   - JELLYFIN_PUBLISHED_URL → re-seed network.xml + restart Jellyfin
//
// applied is "" if nothing applicable changed; pending is "" if nothing
// compose-level changed.
func (h *Handler) applyChanges(prev, next applyKeys) (applied, pending []string) {
	applied = []string{}
	pending = []string{}

	if prev.JellyfinPublishedURL != next.JellyfinPublishedURL {
		if h.Apply != nil && h.Apply.SeedJellyfinNetworkXML != nil {
			if err := h.Apply.SeedJellyfinNetworkXML(next.JellyfinPublishedURL); err != nil {
				slog.Error("failed to re-seed Jellyfin network.xml", "component", "settings", "error", err)
				pending = append(pending, "Jellyfin published URL (network.xml write failed: "+err.Error()+")")
			} else if h.Apply.RestartJellyfin != nil {
				if err := h.Apply.RestartJellyfin(); err != nil {
					slog.Warn("failed to restart Jellyfin after URL change", "component", "settings", "error", err)
					pending = append(pending, "Jellyfin restart (run `pelicula restart jellyfin` to apply published URL)")
				} else {
					applied = append(applied, "Jellyfin published URL")
				}
			} else {
				pending = append(pending, "Jellyfin restart")
			}
		} else {
			pending = append(pending, "Jellyfin published URL")
		}
	}

	return applied, pending
}

// validatePort reports whether s parses as a valid TCP port number (1..65535).
// Empty input is valid (no change requested).
func validatePort(s string) error {
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("must be a valid port number (1-65535)")
	}
	return nil
}

// HandleReset handles POST /api/pelicula/settings/reset — resets .env to defaults
// using the same wizard body shape as the initial setup.
func (h *Handler) HandleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req appsetup.SetupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Sanitize all string fields
	for _, check := range []struct{ name, val string }{
		{"wireguard_key", req.WireguardKey},
		{"config_dir", req.ConfigDir},
		{"media_dir", req.MediaDir},
		{"library_dir", req.LibraryDir},
		{"work_dir", req.WorkDir},
	} {
		if strings.ContainsAny(check.val, "\"\n\r") {
			httputil.WriteError(w, check.name+" contains invalid characters", http.StatusBadRequest)
			return
		}
	}

	// VPN: validate key if provided, or require vpn_skipped
	wgKey := strings.TrimSpace(req.WireguardKey)
	if !req.VPNSkipped {
		if wgKey == "" {
			httputil.WriteError(w, "wireguard_key is required (or set vpn_skipped)", http.StatusBadRequest)
			return
		}
		if len(wgKey) != 44 || wgKey[43] != '=' {
			httputil.WriteError(w, "wireguard_key must be a 44-character base64 WireGuard private key", http.StatusBadRequest)
			return
		}
	} else {
		wgKey = "" // ensure empty when skipped
	}

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

	mu := h.envMu()
	mu.Lock()
	defer mu.Unlock()

	existing, _ := ParseEnvFile(h.EnvPath)
	puid := envOr("HOST_PUID", orDefault(existing["PUID"], "1000"))
	pgid := envOr("HOST_PGID", orDefault(existing["PGID"], "1000"))
	tz := envOr("HOST_TZ", orDefault(existing["TZ"], "America/New_York"))
	proculaKey := h.GenerateAPIKey()
	// Preserve existing WEBHOOK_SECRET if present so autowired webhooks keep working
	webhookSecret := orDefault(existing["WEBHOOK_SECRET"], h.GenerateAPIKey())
	// Preserve existing JELLYFIN_API_KEY if present — the web wizard does not wipe Jellyfin's
	// database, so the existing key is still valid. (The CLI reset-config all DOES wipe
	// Jellyfin's DB and drops this key explicitly; that path doesn't call HandleReset.)
	jellyfinAPIKey := existing["JELLYFIN_API_KEY"]

	vars := map[string]string{
		"CONFIG_DIR":                 req.ConfigDir,
		"LIBRARY_DIR":                libraryDir,
		"WORK_DIR":                   workDir,
		"PUID":                       puid,
		"PGID":                       pgid,
		"TZ":                         tz,
		"WIREGUARD_PRIVATE_KEY":      wgKey,
		"SERVER_COUNTRIES":           "Netherlands",
		"PELICULA_PORT":              "7354",
		"PELICULA_OPEN_REGISTRATION": "false",
		"PROCULA_API_KEY":            proculaKey,
		"WEBHOOK_SECRET":             webhookSecret,
		"JELLYFIN_API_KEY":           jellyfinAPIKey,
		"TRANSCODING_ENABLED":        "false",
		"NOTIFICATIONS_ENABLED":      "false",
		"NOTIFICATIONS_MODE":         "internal",
		"PELICULA_SUB_LANGS":         "en",
		"SEEDING_REMOVE_ON_COMPLETE": "false",
	}

	if err := WriteEnvFile(h.EnvPath, vars); err != nil {
		slog.Error("failed to write .env during reset", "error", err)
		httputil.WriteError(w, "failed to write config", http.StatusInternalServerError)
		return
	}

	slog.Info("settings reset via browser wizard", "component", "settings")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"status":           "ok",
		"restart_required": true,
	})
}

// ParseEnvFile reads a .env file and returns a key→value map.
// Exported transitionally: cmd/ callers use this package as the access point.
func ParseEnvFile(path string) (map[string]string, error) {
	return envfile.Parse(path)
}

// WriteEnvFile writes a .env file from the provided key-value map in canonical order.
// Caller is responsible for holding any relevant mutex before calling.
// Exported transitionally: cmd/ callers use this package as the access point.
func WriteEnvFile(path string, vars map[string]string) error {
	return envfile.Write(path, vars)
}

func orDefault(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// envOr returns the environment variable named by key, or fallback if unset/empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
