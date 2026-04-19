package autowire

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	bazarrclient "pelicula-api/internal/clients/bazarr"
)

// AutowireState holds completion state for the auto-wiring run.
// The zero value is "not done".
type AutowireState struct {
	done atomic.Bool
}

// Done reports whether the auto-wiring run has completed successfully.
func (s *AutowireState) Done() bool {
	return s.done.Load()
}

// Library is the minimal library descriptor that Autowirer needs.
// It mirrors cmd/pelicula-api.Library without depending on package main.
type Library struct {
	Name          string
	ContainerPath string // resolved absolute path inside the container
	Arr           string // "sonarr" | "radarr" | "none"
}

// ArrSvc is the subset of ServiceClients that the Autowirer uses.
// Defined here (not in package main) so the package has no import cycle.
type ArrSvc interface {
	// ReloadKeys re-reads API keys from config files.
	ReloadKeys()
	// SonarrRadarrKeys returns the current Sonarr and Radarr keys.
	SonarrRadarrKeys() (sonarr, radarr string)
	// GetProwlarrKey returns the current Prowlarr key.
	GetProwlarrKey() string
	// SetWired marks the service as fully wired.
	SetWired(v bool)
	// ArrGet makes a GET request to a *arr service.
	ArrGet(baseURL, apiKey, path string) ([]byte, error)
	// ArrPost makes a POST request to a *arr service.
	ArrPost(baseURL, apiKey, path string, payload any) ([]byte, error)
	// ArrPut makes a PUT request to a *arr service.
	ArrPut(baseURL, apiKey, path string, payload any) ([]byte, error)
	// HTTPClient returns the shared HTTP client (used for health polling).
	HTTPClient() *http.Client
	// ConfigDir returns the config directory root (e.g. "/config").
	ConfigDir() string
	// SetBazarrClient installs the Bazarr typed client and key.
	SetBazarrClient(apiKey string, client *bazarrclient.Client)
	// BazarrClient returns the current Bazarr typed client (may be nil).
	BazarrClient() *bazarrclient.Client
}

// URLs holds all the service endpoint URLs that the Autowirer needs.
type URLs struct {
	Sonarr      string
	Radarr      string
	Prowlarr    string
	Bazarr      string
	Jellyfin    string
	QBT         string // qBittorrent (via gluetun network)
	PeliculaAPI string // self-referencing URL for webhook registration
}

// Autowirer runs the *arr stack auto-wiring sequence on startup.
// It has no package-level globals; all dependencies are fields.
type Autowirer struct {
	svc           ArrSvc
	urls          URLs
	vpnConfigured bool
	webhookSecret string
	subLangs      string // PELICULA_SUB_LANGS env value
	audioLang     string // PELICULA_AUDIO_LANG env value (unused here, for Jellyfin)
	getLibraries  func() []Library
	wireJellyfin  func() // callback into cmd/ for Jellyfin-specific wiring
	invalidateIdx func() // callback to clear indexer count cache
	state         *AutowireState
}

// Config is the constructor argument bag for NewAutowirer.
type Config struct {
	Svc           ArrSvc
	URLs          URLs
	VPNConfigured bool
	WebhookSecret string
	SubLangs      string
	AudioLang     string
	// GetLibraries returns the current library slice (called at wiring time).
	GetLibraries func() []Library
	// WireJellyfin is called during Run() to wire Jellyfin.
	// It may be nil if Jellyfin wiring is not needed (e.g. tests).
	WireJellyfin func()
	// InvalidateIndexerCache clears any cached indexer count.
	// May be nil (no-op in that case).
	InvalidateIndexerCache func()
}

// NewAutowirer constructs an Autowirer from Config.
// The returned *AutowireState can be queried before Run completes.
func NewAutowirer(cfg Config) (*Autowirer, *AutowireState) {
	state := &AutowireState{}
	a := &Autowirer{
		svc:           cfg.Svc,
		urls:          cfg.URLs,
		vpnConfigured: cfg.VPNConfigured,
		webhookSecret: cfg.WebhookSecret,
		subLangs:      cfg.SubLangs,
		audioLang:     cfg.AudioLang,
		getLibraries:  cfg.GetLibraries,
		wireJellyfin:  cfg.WireJellyfin,
		invalidateIdx: cfg.InvalidateIndexerCache,
		state:         state,
	}
	if a.getLibraries == nil {
		a.getLibraries = func() []Library { return nil }
	}
	if a.invalidateIdx == nil {
		a.invalidateIdx = func() {}
	}
	if a.wireJellyfin == nil {
		a.wireJellyfin = func() {}
	}
	return a, state
}

// Run executes the auto-wiring sequence and blocks until it completes (or fails).
// It is designed to be called in a goroutine from main so the HTTP server starts
// immediately. ctx is honoured during the service-readiness polling loop.
func (a *Autowirer) Run(ctx context.Context) error {
	slog.Info("waiting for services to be ready", "component", "autowire")

	if err := a.waitForServices(ctx); err != nil {
		return fmt.Errorf("services not ready: %w", err)
	}

	// Reload keys in case they were generated after initial container start.
	a.svc.ReloadKeys()

	sonarrKey, radarrKey := a.svc.SonarrRadarrKeys()
	if sonarrKey == "" || radarrKey == "" {
		return fmt.Errorf("missing API keys (sonarr=%v radarr=%v)",
			sonarrKey != "", radarrKey != "")
	}

	slog.Info("services ready, checking configuration", "component", "autowire")

	sonarrWired := true
	radarrWired := true
	prowlarrWired := true

	if a.vpnConfigured {
		prowlarrKey := a.svc.GetProwlarrKey()
		if prowlarrKey == "" {
			slog.Warn("Prowlarr API key not found — skipping download client and indexer wiring", "component", "autowire")
			prowlarrWired = false
		} else {
			sonarrWired = a.wireDownloadClient("Sonarr", a.urls.Sonarr, sonarrKey, "/api/v3", "tv-sonarr")
			radarrWired = a.wireDownloadClient("Radarr", a.urls.Radarr, radarrKey, "/api/v3", "radarr")
			prowlarrWired = a.wireProwlarrApp("Sonarr", a.urls.Sonarr, sonarrKey) &&
				a.wireProwlarrApp("Radarr", a.urls.Radarr, radarrKey)
		}
	} else {
		slog.Info("VPN not configured — skipping download client and indexer wiring", "component", "autowire")
	}

	// Root folders are needed regardless of VPN (for library management + import).
	for _, lib := range a.getLibraries() {
		switch lib.Arr {
		case "sonarr":
			a.wireRootFolder("Sonarr", a.urls.Sonarr, sonarrKey, "/api/v3", lib.ContainerPath)
		case "radarr":
			a.wireRootFolder("Radarr", a.urls.Radarr, radarrKey, "/api/v3", lib.ContainerPath)
		}
	}

	// Wire Procula import webhooks (useful even without VPN, for manual imports).
	a.wireImportWebhook("Sonarr", a.urls.Sonarr, sonarrKey, "/api/v3")
	a.wireImportWebhook("Radarr", a.urls.Radarr, radarrKey, "/api/v3")

	// Auto-configure Jellyfin (via callback into cmd/).
	a.wireJellyfin()

	// Wire Bazarr: connect Sonarr/Radarr and set subtitle languages.
	a.wireBazarr()

	if sonarrWired && radarrWired && prowlarrWired {
		a.svc.SetWired(true)
		a.invalidateIdx()
		slog.Info("all services wired successfully", "component", "autowire")
		// Force health re-check so stale "connection refused" errors clear from the *arr UI.
		a.triggerHealthCheck("Sonarr", a.urls.Sonarr, sonarrKey, "/api/v3")
		a.triggerHealthCheck("Radarr", a.urls.Radarr, radarrKey, "/api/v3")
	} else {
		slog.Warn("some wiring failed — check logs above", "component", "autowire")
	}

	a.state.done.Store(true)
	return nil
}

func (a *Autowirer) triggerHealthCheck(name, baseURL, apiKey, apiPath string) {
	_, err := a.svc.ArrPost(baseURL, apiKey, apiPath+"/command", map[string]string{"name": "CheckHealth"})
	if err != nil {
		slog.Warn("failed to trigger health check", "component", "autowire", "service", name, "error", err)
	}
}

func (a *Autowirer) waitForServices(ctx context.Context) error {
	endpoints := map[string]string{
		"sonarr":   a.urls.Sonarr + "/ping",
		"radarr":   a.urls.Radarr + "/ping",
		"jellyfin": a.urls.Jellyfin + "/System/Info/Public",
		"bazarr":   a.urls.Bazarr + "/",
	}
	if a.vpnConfigured {
		endpoints["prowlarr"] = a.urls.Prowlarr + "/ping"
		endpoints["qbittorrent"] = a.urls.QBT + "/"
	}

	client := a.svc.HTTPClient()
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		allReady := true
		for _, u := range endpoints {
			resp, err := client.Get(u)
			if err != nil {
				allReady = false
				break
			}
			notReady := resp.StatusCode < 200 || resp.StatusCode >= 300
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

func (a *Autowirer) wireDownloadClient(name, baseURL, apiKey, apiPath, category string) bool {
	data, err := a.svc.ArrGet(baseURL, apiKey, apiPath+"/downloadclient")
	if err != nil {
		slog.Error("failed to check download clients", "component", "autowire", "service", name, "error", err)
		return false
	}

	var clients []map[string]any
	if err := json.Unmarshal(data, &clients); err != nil {
		slog.Error("failed to parse download clients response", "component", "autowire", "service", name, "error", err)
		return false
	}

	for _, c := range clients {
		if impl, _ := c["implementation"].(string); impl == "QBittorrent" {
			slog.Info("qBittorrent already configured, skipping", "component", "autowire", "service", name)
			return true
		}
	}

	payload := map[string]any{
		"name":           "qBittorrent",
		"implementation": "QBittorrent",
		"configContract": "QBittorrentSettings",
		"protocol":       "torrent",
		"enable":         true,
		"priority":       1,
		"fields": []map[string]any{
			{"name": "host", "value": "gluetun"},
			{"name": "port", "value": 8080},
			{"name": "username", "value": ""},
			{"name": "password", "value": ""},
			{"name": "category", "value": category},
		},
	}

	_, err = a.svc.ArrPost(baseURL, apiKey, apiPath+"/downloadclient", payload)
	if err != nil {
		slog.Error("failed to add qBittorrent download client", "component", "autowire", "service", name, "error", err)
		return false
	}

	slog.Info("added qBittorrent download client", "component", "autowire", "service", name)
	return true
}

func (a *Autowirer) wireRootFolder(name, baseURL, apiKey, apiPath, folderPath string) bool {
	data, err := a.svc.ArrGet(baseURL, apiKey, apiPath+"/rootfolder")
	if err != nil {
		slog.Error("failed to check root folders", "component", "autowire", "service", name, "error", err)
		return false
	}

	var folders []map[string]any
	if err := json.Unmarshal(data, &folders); err != nil {
		slog.Error("failed to parse root folders response", "component", "autowire", "service", name, "error", err)
		return false
	}

	for _, f := range folders {
		if path, _ := f["path"].(string); path == folderPath {
			slog.Info("root folder already configured, skipping", "component", "autowire", "service", name, "path", folderPath)
			return true
		}
	}

	_, err = a.svc.ArrPost(baseURL, apiKey, apiPath+"/rootfolder", map[string]any{"path": folderPath})
	if err != nil {
		slog.Error("failed to add root folder", "component", "autowire", "service", name, "path", folderPath, "error", err)
		return false
	}

	slog.Info("added root folder", "component", "autowire", "service", name, "path", folderPath)
	return true
}

// wireImportWebhook adds a Procula import webhook notification to a *arr app.
// It is idempotent — won't add a second "Procula" webhook if one already exists.
func (a *Autowirer) wireImportWebhook(name, baseURL, apiKey, apiPath string) {
	data, err := a.svc.ArrGet(baseURL, apiKey, apiPath+"/notification")
	if err != nil {
		slog.Error("failed to check notifications", "component", "autowire", "service", name, "error", err)
		return
	}

	var existing []map[string]any
	if err := json.Unmarshal(data, &existing); err != nil {
		slog.Error("failed to parse notifications response", "component", "autowire", "service", name, "error", err)
		return
	}

	for _, n := range existing {
		if n, _ := n["name"].(string); n == "Procula" {
			slog.Info("Procula webhook already configured, skipping", "component", "autowire", "service", name)
			return
		}
	}

	hookURL := a.urls.PeliculaAPI + "/api/pelicula/hooks/import"
	if a.webhookSecret != "" {
		hookURL += "?secret=" + url.QueryEscape(a.webhookSecret)
	}
	payload := map[string]any{
		"name":           "Procula",
		"implementation": "Webhook",
		"configContract": "WebhookSettings",
		"fields": []map[string]any{
			{"name": "url", "value": hookURL},
			{"name": "method", "value": 1}, // 1 = POST
			{"name": "username", "value": ""},
			{"name": "password", "value": ""},
		},
		"onGrab":              false,
		"onDownload":          true,
		"onUpgrade":           true,
		"onHealthIssue":       false,
		"onApplicationUpdate": false,
	}

	_, err = a.svc.ArrPost(baseURL, apiKey, apiPath+"/notification", payload)
	if err != nil {
		slog.Error("failed to add Procula webhook", "component", "autowire", "service", name, "error", err)
		return
	}
	slog.Info("added Procula import webhook", "component", "autowire", "service", name, "url", hookURL)
}

func (a *Autowirer) wireProwlarrApp(appName, appURL, appAPIKey string) bool {
	data, err := a.svc.ArrGet(a.urls.Prowlarr, a.svc.GetProwlarrKey(), "/api/v1/applications")
	if err != nil {
		slog.Error("failed to check Prowlarr applications", "component", "autowire", "error", err)
		return false
	}

	var apps []map[string]any
	if err := json.Unmarshal(data, &apps); err != nil {
		slog.Error("failed to parse Prowlarr applications response", "component", "autowire", "error", err)
		return false
	}

	prowlarrKey := a.svc.GetProwlarrKey()

	for _, app := range apps {
		if n, _ := app["name"].(string); n != appName {
			continue
		}

		// App exists — check if prowlarrUrl or apiKey are stale and update if so.
		fields, ok := app["fields"].([]any)
		if !ok {
			slog.Warn("unexpected fields type in Prowlarr app", "component", "autowire", "app", appName)
			return false
		}
		needsUpdate := false
		for _, f := range fields {
			field, ok := f.(map[string]any)
			if !ok {
				continue
			}
			switch field["name"] {
			case "prowlarrUrl":
				if v, _ := field["value"].(string); normalizeURL(v) != normalizeURL(a.urls.Prowlarr) {
					slog.Debug("prowlarr app URL mismatch", "component", "autowire", "app", appName, "have", v, "want", a.urls.Prowlarr)
					needsUpdate = true
				}
			case "apiKey":
				if v, _ := field["value"].(string); v != appAPIKey {
					slog.Debug("prowlarr app key mismatch", "component", "autowire", "app", appName)
					needsUpdate = true
				}
			}
		}
		if !needsUpdate {
			slog.Info("Prowlarr app already connected, skipping", "component", "autowire", "app", appName)
			return true
		}

		// Patch the fields in the existing payload and PUT.
		for _, fRaw := range fields {
			f, ok := fRaw.(map[string]any)
			if !ok {
				continue
			}
			switch f["name"] {
			case "prowlarrUrl":
				f["value"] = a.urls.Prowlarr
			case "apiKey":
				f["value"] = appAPIKey
			}
		}
		idVal, ok := app["id"].(float64)
		if !ok {
			slog.Error("unexpected id type in Prowlarr app", "component", "autowire", "app", appName)
			return false
		}
		id := int(idVal)
		_, err = a.svc.ArrPut(a.urls.Prowlarr, prowlarrKey, fmt.Sprintf("/api/v1/applications/%d", id), app)
		if err != nil {
			slog.Error("failed to update Prowlarr app", "component", "autowire", "app", appName, "error", err)
			return false
		}
		slog.Info("updated Prowlarr app (stale key or URL)", "component", "autowire", "app", appName)
		return true
	}

	payload := map[string]any{
		"name":           appName,
		"implementation": appName,
		"configContract": appName + "Settings",
		"syncLevel":      "fullSync",
		"fields": []map[string]any{
			{"name": "prowlarrUrl", "value": a.urls.Prowlarr},
			{"name": "baseUrl", "value": appURL},
			{"name": "apiKey", "value": appAPIKey},
		},
	}

	_, err = a.svc.ArrPost(a.urls.Prowlarr, prowlarrKey, "/api/v1/applications", payload)
	if err != nil {
		slog.Error("failed to connect Prowlarr app", "component", "autowire", "app", appName, "error", err)
		return false
	}

	slog.Info("connected Prowlarr app", "component", "autowire", "app", appName)
	return true
}

// normalizeURL strips trailing slashes and lowercases scheme+host so that
// URL comparisons are not sensitive to Prowlarr's normalization behavior.
func normalizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return strings.TrimRight(raw, "/")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String()
}

// ── Bazarr ─────────────────────────────────────────────────────────────────

// wireBazarr configures Bazarr with Sonarr+Radarr credentials and installs a
// "Pelicula" language profile built from PELICULA_SUB_LANGS. Bazarr's REST API
// is Flask-RESTx and reads request.form, so every mutation must be
// form-encoded — settings keys follow the `settings-<section>-<field>` shape
// and language profiles are written via the `languages-profiles` form field
// (a JSON-encoded list, not a separate endpoint). Bazarr only schedules its
// background missing-subtitle searches when `use_sonarr`/`use_radarr` are
// true, so this wiring is load-bearing for the whole subtitle pipeline.
func (a *Autowirer) wireBazarr() {
	slog.Info("checking Bazarr", "component", "autowire")

	apiKey, err := readBazarrAPIKey(a.svc.ConfigDir())
	if err != nil {
		slog.Warn("Bazarr API key not available yet", "component", "autowire", "error", err)
		return
	}
	a.svc.SetBazarrClient(apiKey, bazarrclient.New(a.urls.Bazarr, apiKey))

	sonarrKey, radarrKey := a.svc.SonarrRadarrKeys()
	if sonarrKey == "" || radarrKey == "" {
		slog.Warn("Bazarr wiring skipped, sonarr/radarr keys not ready", "component", "autowire")
		return
	}

	subLangs := parseSubLangs(a.subLangs)
	if len(subLangs) == 0 {
		subLangs = []string{"en"}
	}

	bzClient := a.svc.BazarrClient()
	if bzClient == nil {
		slog.Warn("Bazarr client not available after SetBazarrClient — skipping", "component", "autowire")
		return
	}

	if bazarrAlreadyWired(bzClient, sonarrKey, radarrKey) {
		slog.Info("Bazarr already wired, skipping", "component", "autowire")
		return
	}

	profileJSON, _ := json.Marshal([]any{buildPeliculaProfile(subLangs)})

	form := url.Values{}
	for _, c := range subLangs {
		form.Add("languages-enabled", c)
	}
	form.Set("languages-profiles", string(profileJSON))

	// Bazarr's save_settings coerces "true"/"false" (lowercase) to Python bools;
	// any other casing stays as a string and fails dynaconf type validation.
	form.Set("settings-general-use_sonarr", "true")
	form.Set("settings-general-use_radarr", "true")
	form.Set("settings-general-serie_default_enabled", "true")
	form.Set("settings-general-serie_default_profile", "1")
	form.Set("settings-general-movie_default_enabled", "true")
	form.Set("settings-general-movie_default_profile", "1")

	form.Set("settings-sonarr-ip", "sonarr")
	form.Set("settings-sonarr-port", "8989")
	form.Set("settings-sonarr-base_url", "/sonarr")
	form.Set("settings-sonarr-ssl", "false")
	form.Set("settings-sonarr-apikey", sonarrKey)
	form.Set("settings-sonarr-only_monitored", "false")
	form.Set("settings-sonarr-series_sync", "60")
	form.Set("settings-sonarr-full_update", "Daily")

	form.Set("settings-radarr-ip", "radarr")
	form.Set("settings-radarr-port", "7878")
	form.Set("settings-radarr-base_url", "/radarr")
	form.Set("settings-radarr-ssl", "false")
	form.Set("settings-radarr-apikey", radarrKey)
	form.Set("settings-radarr-only_monitored", "false")
	form.Set("settings-radarr-movies_sync", "60")
	form.Set("settings-radarr-full_update", "Daily")

	// Enable free, credential-less subtitle providers. Bazarr ships with
	// enabled_providers = [] out of the box, which makes every search
	// immediately return "All providers are throttled" — the same symptom
	// as real throttling, but the root cause is that nothing is configured.
	// podnapisi covers movies + TV, yifysubtitles is movies-only but
	// reliable. Users can add/remove from Bazarr's UI later; our idempotency
	// check only runs wireBazarr once, so we won't clobber their edits.
	for _, p := range []string{"podnapisi", "yifysubtitles"} {
		form.Add("settings-general-enabled_providers", p)
	}

	if err := bzClient.SaveSettings(context.Background(), form); err != nil {
		slog.Error("failed to wire Bazarr", "component", "autowire", "error", err)
		return
	}
	slog.Info("Bazarr wired", "component", "autowire", "langs", subLangs)
}

func parseSubLangs(raw string) []string {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if c := strings.ToLower(strings.TrimSpace(s)); c != "" {
			out = append(out, c)
		}
	}
	return out
}

func buildPeliculaProfile(langs []string) map[string]any {
	items := make([]map[string]any, 0, len(langs))
	for i, code := range langs {
		items = append(items, map[string]any{
			"id":                 i + 1,
			"language":           code,
			"audio_exclude":      "False",
			"audio_only_include": "False",
			"hi":                 "False",
			"forced":             "False",
		})
	}
	// originalFormat must be int-parseable (Bazarr calls int(item['originalFormat']))
	// or one of ['null', 'undefined', '', None]. 0 means "keep original-format off".
	// items: hi/forced/audio_exclude/audio_only_include are strings "True"/"False"
	// per Bazarr's subtitles/indexer code, not JSON booleans. audio_only_include
	// is load-bearing: Bazarr's startup migration backfills it on profiles
	// loaded from disk, but profiles written via the API go straight to the DB,
	// so omitting it makes list_missing_subtitles_movies crash with KeyError.
	return map[string]any{
		"profileId":      1,
		"name":           "Pelicula",
		"cutoff":         nil,
		"items":          items,
		"mustContain":    []string{},
		"mustNotContain": []string{},
		"originalFormat": 0,
		"tag":            nil,
	}
}

func bazarrAlreadyWired(bzClient *bazarrclient.Client, sonarrKey, radarrKey string) bool {
	data, err := bzClient.RawGet(context.Background(), "/api/system/settings")
	if err != nil {
		return false
	}
	var cur struct {
		General struct {
			UseSonarr        bool     `json:"use_sonarr"`
			UseRadarr        bool     `json:"use_radarr"`
			EnabledProviders []string `json:"enabled_providers"`
		} `json:"general"`
		Sonarr struct {
			Apikey string `json:"apikey"`
		} `json:"sonarr"`
		Radarr struct {
			Apikey string `json:"apikey"`
		} `json:"radarr"`
	}
	if json.Unmarshal(data, &cur) != nil {
		return false
	}
	if !cur.General.UseSonarr || !cur.General.UseRadarr {
		return false
	}
	if cur.Sonarr.Apikey != sonarrKey || cur.Radarr.Apikey != radarrKey {
		return false
	}
	// Empty enabled_providers is Bazarr's ship-default and makes every
	// search return "All providers are throttled". Treat it as unwired so
	// we install our credential-less defaults. Once the user edits the
	// provider list in Bazarr's UI (even to remove one), it'll stay
	// non-empty and we'll leave it alone.
	if len(cur.General.EnabledProviders) == 0 {
		return false
	}
	pdata, err := bzClient.RawGet(context.Background(), "/api/system/languages/profiles")
	if err != nil {
		return false
	}
	var profiles []struct {
		Name  string `json:"name"`
		Items []struct {
			AudioOnlyInclude string `json:"audio_only_include"`
		} `json:"items"`
	}
	if json.Unmarshal(pdata, &profiles) != nil {
		return false
	}
	for _, p := range profiles {
		if p.Name != "Pelicula" {
			continue
		}
		// Require every item to carry audio_only_include — older wirings
		// omitted it, which makes Bazarr's subtitle indexer crash with
		// KeyError and silently disables missing-sub detection. Treat
		// that as "not wired" so we overwrite the broken profile.
		for _, it := range p.Items {
			if it.AudioOnlyInclude == "" {
				return false
			}
		}
		return true
	}
	return false
}

// readBazarrAPIKey reads the API key from Bazarr's config.yaml.
// Bazarr generates this key on first startup and stores it under auth.apikey.
// The file is mounted read-only at /config/bazarr/config/config.yaml inside
// the middleware container.
func readBazarrAPIKey(configDir string) (string, error) {
	path := configDir + "/bazarr/config/config.yaml"
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read bazarr config.yaml: %w", err)
	}
	inAuth := false
	for _, raw := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Top-level key (no leading whitespace) starts a new section.
		if raw[0] != ' ' && raw[0] != '\t' {
			inAuth = strings.HasPrefix(raw, "auth:")
			continue
		}
		if !inAuth {
			continue
		}
		if strings.HasPrefix(trimmed, "apikey:") {
			key := strings.TrimSpace(strings.TrimPrefix(trimmed, "apikey:"))
			key = strings.Trim(key, `"'`)
			if key == "" || key == "null" {
				return "", fmt.Errorf("auth.apikey empty in bazarr config.yaml")
			}
			return key, nil
		}
	}
	return "", fmt.Errorf("no auth.apikey found in bazarr config.yaml")
}
