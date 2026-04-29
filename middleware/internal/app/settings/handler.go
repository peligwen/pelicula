// Package settings provides HTTP handlers for reading, updating, and resetting
// the .env configuration file via the pelicula-api admin endpoints.
package settings

import (
	"bufio"
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
}

// Handler handles settings GET/POST and reset endpoints.
type Handler struct {
	EnvPath        string
	GenerateAPIKey func() string
	// Apply, when non-nil, lets the handler apply some changes in-place
	// (Jellyfin published URL re-seed + restart). Other changes (compose-level
	// — port mappings, sidecar add/remove) are always returned as pending.
	Apply *Applier
	mu    sync.Mutex
}

// New constructs a Handler with the given .env path and API key generator.
func New(envPath string, generateAPIKey func() string) *Handler {
	return &Handler{
		EnvPath:        envPath,
		GenerateAPIKey: generateAPIKey,
	}
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
	// Peligrosa remote access. The wire field stays a "true"/"false" string —
	// the dashboard toggle controls the hardened nginx port-forward vhost. The
	// .env stores it as REMOTE_MODE ("portforward" / "disabled" / alternative
	// modes like "cloudflared" or "tailscale"); this handler is the translator.
	RemoteAccessEnabled string `json:"remote_access_enabled"`
	RemoteHostname      string `json:"remote_hostname"`
	RemoteHTTPPort      string `json:"remote_http_port"`
	RemoteHTTPSPort     string `json:"remote_https_port"`
	RemoteCertMode      string `json:"remote_cert_mode"`
	RemoteLEEmail       string `json:"remote_le_email"`
	RemoteLEStaging     string `json:"remote_le_staging"`
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
	h.mu.Lock()
	vars, err := ParseEnvFile(h.EnvPath)
	h.mu.Unlock()

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
		RemoteAccessEnabled:     remoteAccessEnabledFromMode(vars["REMOTE_MODE"]),
		RemoteHostname:          vars["REMOTE_HOSTNAME"],
		RemoteHTTPPort:          vars["REMOTE_HTTP_PORT"],
		RemoteHTTPSPort:         vars["REMOTE_HTTPS_PORT"],
		RemoteCertMode:          vars["REMOTE_CERT_MODE"],
		RemoteLEEmail:           vars["REMOTE_LE_EMAIL"],
		RemoteLEStaging:         vars["REMOTE_LE_STAGING"],
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

	// Validate remote access fields if being changed
	if req.RemoteHostname != "" {
		if strings.ContainsAny(req.RemoteHostname, "\"/ \n\r:") {
			httputil.WriteError(w, "remote_hostname must be a bare hostname with no scheme, port, or path", http.StatusBadRequest)
			return
		}
	}
	if req.RemoteCertMode != "" {
		switch req.RemoteCertMode {
		case "letsencrypt", "byo", "self-signed":
			// valid
		default:
			httputil.WriteError(w, "remote_cert_mode must be one of: letsencrypt, byo, self-signed", http.StatusBadRequest)
			return
		}
	}
	if req.RemoteLEEmail != "" && !strings.Contains(req.RemoteLEEmail, "@") {
		httputil.WriteError(w, "remote_le_email must be a valid email address", http.StatusBadRequest)
		return
	}
	for _, p := range []struct{ name, val string }{
		{"port", req.Port},
		{"remote_http_port", req.RemoteHTTPPort},
		{"remote_https_port", req.RemoteHTTPSPort},
	} {
		if err := validatePort(p.val); err != nil {
			httputil.WriteError(w, p.name+" "+err.Error(), http.StatusBadRequest)
			return
		}
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

	h.mu.Lock()
	defer h.mu.Unlock()

	vars, err := ParseEnvFile(h.EnvPath)
	if err != nil {
		slog.Error("failed to read .env for update", "error", err)
		httputil.WriteError(w, "failed to read config", http.StatusInternalServerError)
		return
	}

	// Reject port collisions before persisting. The dashboard listener and the
	// remote vhost share the same nginx instance; binding the same host port
	// twice fails opaquely at compose time.
	effPelicula := orDefault(req.Port, vars["PELICULA_PORT"])
	effRemoteHTTPS := orDefault(req.RemoteHTTPSPort, vars["REMOTE_HTTPS_PORT"])
	effRemoteHTTP := orDefault(req.RemoteHTTPPort, vars["REMOTE_HTTP_PORT"])
	if effPelicula != "" && effRemoteHTTPS == effPelicula {
		httputil.WriteError(w, "remote_https_port must differ from the dashboard port ("+effPelicula+")", http.StatusBadRequest)
		return
	}
	if effPelicula != "" && effRemoteHTTP == effPelicula {
		httputil.WriteError(w, "remote_http_port must differ from the dashboard port ("+effPelicula+")", http.StatusBadRequest)
		return
	}
	if effRemoteHTTPS != "" && effRemoteHTTP != "" && effRemoteHTTPS == effRemoteHTTP {
		httputil.WriteError(w, "remote_https_port and remote_http_port must differ", http.StatusBadRequest)
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
	if req.RemoteAccessEnabled != "" {
		applyRemoteModeChange(vars, req.RemoteAccessEnabled)
	}
	// Drop the legacy key on every write so old .env files converge to the
	// REMOTE_MODE schema even when the user isn't toggling remote access.
	delete(vars, "REMOTE_ACCESS_ENABLED")
	// RemoteHostname is always written when RemoteAccessEnabled is present in
	// the payload — an empty hostname is valid (simple mode: self-signed, no DNS).
	if req.RemoteAccessEnabled != "" {
		vars["REMOTE_HOSTNAME"] = req.RemoteHostname
	} else if req.RemoteHostname != "" {
		vars["REMOTE_HOSTNAME"] = req.RemoteHostname
	}
	if req.RemoteHTTPPort != "" {
		vars["REMOTE_HTTP_PORT"] = req.RemoteHTTPPort
	}
	if req.RemoteHTTPSPort != "" {
		vars["REMOTE_HTTPS_PORT"] = req.RemoteHTTPSPort
	}
	if req.RemoteCertMode != "" {
		vars["REMOTE_CERT_MODE"] = req.RemoteCertMode
	}
	if req.RemoteLEEmail != "" {
		vars["REMOTE_LE_EMAIL"] = req.RemoteLEEmail
	}
	if req.RemoteLEStaging != "" {
		vars["REMOTE_LE_STAGING"] = req.RemoteLEStaging
	}
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
	RemoteMode           string
	RemoteHostname       string
	RemoteHTTPPort       string
	RemoteHTTPSPort      string
	RemoteCertMode       string
	RemoteLEEmail        string
	RemoteLEStaging      string
}

func snapshotApplyKeys(vars map[string]string) applyKeys {
	return applyKeys{
		JellyfinPublishedURL: vars["JELLYFIN_PUBLISHED_URL"],
		RemoteMode:           vars["REMOTE_MODE"],
		RemoteHostname:       vars["REMOTE_HOSTNAME"],
		RemoteHTTPPort:       vars["REMOTE_HTTP_PORT"],
		RemoteHTTPSPort:      vars["REMOTE_HTTPS_PORT"],
		RemoteCertMode:       vars["REMOTE_CERT_MODE"],
		RemoteLEEmail:        vars["REMOTE_LE_EMAIL"],
		RemoteLEStaging:      vars["REMOTE_LE_STAGING"],
	}
}

// applyChanges diffs old vs new applyKeys and dispatches the changes the
// handler can do in-place. Returns (applied, pending) human-readable lists
// suitable for the dashboard banner.
//
// In-place:
//   - JELLYFIN_PUBLISHED_URL → re-seed network.xml + restart Jellyfin
//
// Pending (compose-level — needs `pelicula up` to recreate containers with
// new ports / new sidecars):
//   - any REMOTE_* change
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

	// Remote-access changes are compose-level (port mapping, sidecar add/remove,
	// nginx vhost generation). Middleware can't run `docker compose up -d`, so
	// they are always pending until the user runs `pelicula up`.
	if prev.RemoteMode != next.RemoteMode {
		pending = append(pending, "Remote access "+remoteModeLabel(next.RemoteMode))
	}
	if prev.RemoteHostname != next.RemoteHostname {
		pending = append(pending, "Remote hostname")
	}
	if prev.RemoteHTTPPort != next.RemoteHTTPPort {
		pending = append(pending, "Remote HTTP port → "+next.RemoteHTTPPort)
	}
	if prev.RemoteHTTPSPort != next.RemoteHTTPSPort {
		pending = append(pending, "Remote HTTPS port → "+next.RemoteHTTPSPort)
	}
	if prev.RemoteCertMode != next.RemoteCertMode {
		pending = append(pending, "Remote cert mode → "+next.RemoteCertMode)
	}
	if prev.RemoteLEEmail != next.RemoteLEEmail {
		pending = append(pending, "Let's Encrypt email")
	}
	if prev.RemoteLEStaging != next.RemoteLEStaging {
		pending = append(pending, "Let's Encrypt staging toggle")
	}

	return applied, pending
}

// validatePort reports whether s parses as a valid TCP port number (1..65535).
// Empty input is valid (no change requested). Mirror this rule in the CLI's
// RenderRemoteConfigs so both surfaces reject the same way.
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

// remoteAccessEnabledFromMode translates the REMOTE_MODE .env value into the
// boolean wire field "remote_access_enabled" the dashboard toggle reads. The
// toggle is specifically about the hardened nginx port-forward vhost; the
// alternative modes (cloudflared, tailscale) report as "false" so the toggle
// reflects "port-forward not active" — they are configured manually via .env.
func remoteAccessEnabledFromMode(mode string) string {
	if mode == "portforward" {
		return "true"
	}
	return "false"
}

// applyRemoteModeChange writes REMOTE_MODE based on the boolean wire field
// from the dashboard. Toggling OFF when the user is on an alternative mode
// (cloudflared / tailscale) is a no-op — the toggle does not own those modes,
// so we don't clobber them. Toggling ON always switches to portforward.
func applyRemoteModeChange(vars map[string]string, remoteAccessEnabled string) {
	switch remoteAccessEnabled {
	case "true":
		vars["REMOTE_MODE"] = "portforward"
	case "false":
		if vars["REMOTE_MODE"] == "portforward" || vars["REMOTE_MODE"] == "" {
			vars["REMOTE_MODE"] = "disabled"
		}
	}
}

// remoteModeLabel renders REMOTE_MODE for the dashboard's pending banner.
func remoteModeLabel(mode string) string {
	switch mode {
	case "portforward":
		return "enabled"
	case "disabled", "":
		return "disabled"
	default:
		return "→ " + mode
	}
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

	h.mu.Lock()
	defer h.mu.Unlock()

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
		"REMOTE_MODE":                "disabled",
		"REMOTE_HOSTNAME":            "",
		"REMOTE_HTTP_PORT":           "80",
		"REMOTE_HTTPS_PORT":          "8920",
		"REMOTE_CERT_MODE":           "self-signed",
		"REMOTE_LE_EMAIL":            "",
		"REMOTE_LE_STAGING":          "false",
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
// Handles quoted values, comments (#), and blank lines.
//
// Exported transitionally so cmd/ callers (search.go, main.go, jfapp.NewWirer)
// can delegate through the envfile.go shim without importing this package
// directly. Re-privatize once those call sites are migrated or extracted.
func ParseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	result := make(map[string]string)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		// Strip surrounding double quotes
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		result[key] = val
	}
	return result, scanner.Err()
}

// WriteEnvFile writes a .env file from the provided key-value map.
// Known keys are written in canonical order; any extra keys follow.
// Caller is responsible for holding any relevant mutex before calling.
//
// Exported transitionally so cmd/ callers (search.go, main.go, jfapp.NewWirer)
// can delegate through the envfile.go shim without importing this package
// directly. Re-privatize once those call sites are migrated or extracted.
func WriteEnvFile(path string, vars map[string]string) error {
	// Canonical order matching the setup wizard (.env produced by internal/app/setup)
	order := []string{
		"CONFIG_DIR", "LIBRARY_DIR", "WORK_DIR",
		"PUID", "PGID", "TZ",
		"WIREGUARD_PRIVATE_KEY", "SERVER_COUNTRIES",
		"PELICULA_PORT",
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
		"REMOTE_MODE", "REMOTE_HOSTNAME",
		"REMOTE_HTTP_PORT", "REMOTE_HTTPS_PORT",
		"REMOTE_CERT_MODE", "REMOTE_LE_EMAIL", "REMOTE_LE_STAGING",
		"SEEDING_REMOVE_ON_COMPLETE",
		"SEARCH_MODE",
	}
	inOrder := make(map[string]bool, len(order))
	for _, k := range order {
		inOrder[k] = true
	}

	var sb strings.Builder
	sb.WriteString("# Generated by Pelicula setup wizard\n")
	for _, k := range order {
		v, ok := vars[k]
		if !ok {
			continue
		}
		writeLine(&sb, k, v)
	}
	// Preserve any extra keys not in the canonical list
	for k, v := range vars {
		if !inOrder[k] {
			writeLine(&sb, k, v)
		}
	}

	// Direct write (not tmp+rename): .env is bind-mounted as a single file
	// into the container, so a rename from an overlay-fs tmp would fail with
	// EXDEV and, even if it didn't, would replace the in-container mount
	// point rather than the host file.
	if err := os.WriteFile(path, []byte(sb.String()), 0600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func writeLine(sb *strings.Builder, k, v string) {
	// Booleans written unquoted; everything else double-quoted
	if v == "true" || v == "false" {
		fmt.Fprintf(sb, "%s=%s\n", k, v)
	} else {
		fmt.Fprintf(sb, "%s=\"%s\"\n", k, v)
	}
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
