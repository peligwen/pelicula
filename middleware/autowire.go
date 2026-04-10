package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"
)

var (
	sonarrURL   = envOr("SONARR_URL", "http://sonarr:8989/sonarr")
	radarrURL   = envOr("RADARR_URL", "http://radarr:7878/radarr")
	prowlarrURL = envOr("PROWLARR_URL", "http://gluetun:9696/prowlarr")
	bazarrURL   = envOr("BAZARR_URL", "http://bazarr:6767/bazarr")
)


func AutoWire(s *ServiceClients) error {
	slog.Info("waiting for services to be ready", "component", "autowire")

	if err := waitForServices(s); err != nil {
		return fmt.Errorf("services not ready: %w", err)
	}

	// Reload keys in case they were generated after initial container start
	s.ReloadKeys()

	if s.SonarrKey == "" || s.RadarrKey == "" {
		return fmt.Errorf("missing API keys (sonarr=%v radarr=%v)",
			s.SonarrKey != "", s.RadarrKey != "")
	}

	slog.Info("services ready, checking configuration", "component", "autowire")

	vpnConfigured := os.Getenv("WIREGUARD_PRIVATE_KEY") != ""

	sonarrWired := true
	radarrWired := true
	prowlarrWired := true

	if vpnConfigured {
		if s.ProwlarrKey == "" {
			slog.Warn("Prowlarr API key not found — skipping download client and indexer wiring", "component", "autowire")
			prowlarrWired = false
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

	// Wire Procula import webhooks (useful even without VPN, for manual imports)
	wireImportWebhook(s, "Sonarr", sonarrURL, s.SonarrKey, "/api/v3")
	wireImportWebhook(s, "Radarr", radarrURL, s.RadarrKey, "/api/v3")

	// Auto-configure Jellyfin: complete wizard, add media libraries
	wireJellyfin(s)

	// Wire Bazarr: connect Sonarr/Radarr and set subtitle languages
	wireBazarr(s)

	if sonarrWired && radarrWired && prowlarrWired {
		s.SetWired(true)
		slog.Info("all services wired successfully", "component", "autowire")
		// Force health re-check so stale "connection refused" errors clear from the *arr UI.
		triggerHealthCheck(s, "Sonarr", sonarrURL, s.SonarrKey, "/api/v3")
		triggerHealthCheck(s, "Radarr", radarrURL, s.RadarrKey, "/api/v3")
	} else {
		slog.Warn("some wiring failed — check logs above", "component", "autowire")
	}

	return nil
}

func triggerHealthCheck(s *ServiceClients, name, baseURL, apiKey, apiPath string) {
	_, err := s.ArrPost(baseURL, apiKey, apiPath+"/command", []byte(`{"name":"CheckHealth"}`))
	if err != nil {
		slog.Warn("failed to trigger health check", "component", "autowire", "service", name, "error", err)
	}
}

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

func wireDownloadClient(s *ServiceClients, name, baseURL, apiKey, apiPath, category string) bool {
	// Check existing download clients
	data, err := s.ArrGet(baseURL, apiKey, apiPath+"/downloadclient")
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

	// Add qBittorrent download client
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

	_, err = s.ArrPost(baseURL, apiKey, apiPath+"/downloadclient", payload)
	if err != nil {
		slog.Error("failed to add qBittorrent download client", "component", "autowire", "service", name, "error", err)
		return false
	}

	slog.Info("added qBittorrent download client", "component", "autowire", "service", name)
	return true
}

func wireRootFolder(s *ServiceClients, name, baseURL, apiKey, apiPath, folderPath string) bool {
	// Check existing root folders
	data, err := s.ArrGet(baseURL, apiKey, apiPath+"/rootfolder")
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

	payload := map[string]any{
		"path": folderPath,
	}

	_, err = s.ArrPost(baseURL, apiKey, apiPath+"/rootfolder", payload)
	if err != nil {
		slog.Error("failed to add root folder", "component", "autowire", "service", name, "path", folderPath, "error", err)
		return false
	}

	slog.Info("added root folder", "component", "autowire", "service", name, "path", folderPath)
	return true
}

// wireImportWebhook adds a Procula import webhook notification to a *arr app.
// It is idempotent — won't add a second "Procula" webhook if one already exists.
func wireImportWebhook(s *ServiceClients, name, baseURL, apiKey, apiPath string) {
	data, err := s.ArrGet(baseURL, apiKey, apiPath+"/notification")
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

	hookURL := envOr("PELICULA_API_URL", "http://pelicula-api:8181") + "/api/pelicula/hooks/import"
	if secret := strings.TrimSpace(os.Getenv("WEBHOOK_SECRET")); secret != "" {
		hookURL += "?secret=" + url.QueryEscape(secret)
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
		"onGrab":       false,
		"onDownload":   true,
		"onUpgrade":    true,
		"onHealthIssue": false,
		"onApplicationUpdate": false,
	}

	_, err = s.ArrPost(baseURL, apiKey, apiPath+"/notification", payload)
	if err != nil {
		slog.Error("failed to add Procula webhook", "component", "autowire", "service", name, "error", err)
		return
	}
	slog.Info("added Procula import webhook", "component", "autowire", "service", name, "url", hookURL)
}

// ── Bazarr ─────────────────────────────────────────────────────────────────

func wireBazarr(s *ServiceClients) {
	slog.Info("checking Bazarr", "component", "autowire")

	apiKey, err := readBazarrAPIKey(s.configDir)
	if err != nil {
		slog.Warn("Bazarr API key not available yet", "component", "autowire", "error", err)
		return
	}

	s.mu.Lock()
	s.BazarrKey = apiKey
	s.mu.Unlock()

	// Check current settings to avoid duplicate wiring
	data, err := bzGet(s, "/api/system/settings")
	if err != nil {
		slog.Warn("Bazarr settings not reachable", "component", "autowire", "error", err)
		return
	}

	var current map[string]any
	if json.Unmarshal(data, &current) != nil {
		slog.Warn("Bazarr settings unreadable", "component", "autowire")
		return
	}

	s.mu.RLock()
	sonarrKey := s.SonarrKey
	radarrKey := s.RadarrKey
	s.mu.RUnlock()

	// Wire Sonarr into Bazarr if not already configured
	if sonarrSec, ok := current["sonarr"].(map[string]any); !ok || sonarrSec["enabled"] != true {
		_, err = bzPost(s, "/api/system/settings", map[string]any{
			"sonarr": map[string]any{
				"enabled":        true,
				"ip":             "sonarr",
				"port":           8989,
				"apikey":         sonarrKey,
				"base_url":       "/sonarr",
				"ssl":            false,
				"only_monitored": false,
				"series_sync":    60,
				"full_update":    "Startup",
			},
		})
		if err != nil {
			slog.Error("failed to wire Sonarr into Bazarr", "component", "autowire", "error", err)
		} else {
			slog.Info("wired Sonarr into Bazarr", "component", "autowire")
		}
	} else {
		slog.Info("Bazarr Sonarr already configured, skipping", "component", "autowire")
	}

	// Wire Radarr into Bazarr if not already configured
	if radarrSec, ok := current["radarr"].(map[string]any); !ok || radarrSec["enabled"] != true {
		_, err = bzPost(s, "/api/system/settings", map[string]any{
			"radarr": map[string]any{
				"enabled":        true,
				"ip":             "radarr",
				"port":           7878,
				"apikey":         radarrKey,
				"base_url":       "/radarr",
				"ssl":            false,
				"only_monitored": false,
				"movies_sync":    60,
				"full_update":    "Startup",
			},
		})
		if err != nil {
			slog.Error("failed to wire Radarr into Bazarr", "component", "autowire", "error", err)
		} else {
			slog.Info("wired Radarr into Bazarr", "component", "autowire")
		}
	} else {
		slog.Info("Bazarr Radarr already configured, skipping", "component", "autowire")
	}

	// Create language profile from PELICULA_SUB_LANGS if any configured
	wireBazarrLanguageProfile(s)

	slog.Info("Bazarr wired", "component", "autowire")
}

func wireBazarrLanguageProfile(s *ServiceClients) {
	subLangs := strings.TrimSpace(os.Getenv("PELICULA_SUB_LANGS"))
	if subLangs == "" {
		return
	}

	// Check existing profiles
	data, err := bzGet(s, "/api/languagesprofiles")
	if err != nil {
		slog.Warn("can't check Bazarr language profiles", "component", "autowire", "error", err)
		return
	}

	var profiles []map[string]any
	if json.Unmarshal(data, &profiles) == nil {
		for _, p := range profiles {
			if name, _ := p["name"].(string); name == "Pelicula" {
				slog.Info("Bazarr language profile already exists, skipping", "component", "autowire")
				return
			}
		}
	}

	// Build language list for the profile
	langs := []map[string]any{}
	for _, code := range strings.Split(subLangs, ",") {
		code = strings.ToLower(strings.TrimSpace(code))
		if code == "" {
			continue
		}
		langs = append(langs, map[string]any{
			"language": code,
			"hi":       "False",
			"forced":   "False",
		})
	}
	if len(langs) == 0 {
		return
	}

	_, err = bzPost(s, "/api/languagesprofiles", map[string]any{
		"name":           "Pelicula",
		"languages":      langs,
		"originalFormat": false,
		"cutoff":         nil,
	})
	if err != nil {
		slog.Error("failed to create Bazarr language profile", "component", "autowire", "error", err)
		return
	}
	slog.Info("created Bazarr language profile", "component", "autowire", "langs", subLangs)
}

// readBazarrAPIKey reads the API key from Bazarr's config.ini.
// Bazarr generates this key on first startup. The file is mounted read-only
// at /config/bazarr/config/config.ini inside the middleware container.
func readBazarrAPIKey(configDir string) (string, error) {
	path := configDir + "/bazarr/config/config.ini"
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read bazarr config.ini: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "apikey") {
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				key := strings.TrimSpace(parts[1])
				if key != "" {
					return key, nil
				}
			}
		}
	}
	return "", fmt.Errorf("no apikey found in bazarr config.ini")
}

func bzGet(s *ServiceClients, path string) ([]byte, error) {
	s.mu.RLock()
	key := s.BazarrKey
	s.mu.RUnlock()
	return s.arrDo("GET", bazarrURL, key, path, nil)
}

func bzPost(s *ServiceClients, path string, payload any) ([]byte, error) {
	s.mu.RLock()
	key := s.BazarrKey
	s.mu.RUnlock()
	return s.arrDo("POST", bazarrURL, key, path, payload)
}


func wireProwlarrApp(s *ServiceClients, appName, appURL, appAPIKey string) bool {
	// Check existing applications
	data, err := s.ArrGet(prowlarrURL, s.ProwlarrKey, "/api/v1/applications")
	if err != nil {
		slog.Error("failed to check Prowlarr applications", "component", "autowire", "error", err)
		return false
	}

	var apps []map[string]any
	if err := json.Unmarshal(data, &apps); err != nil {
		slog.Error("failed to parse Prowlarr applications response", "component", "autowire", "error", err)
		return false
	}

	for _, a := range apps {
		if n, _ := a["name"].(string); n != appName {
			continue
		}

		// App exists — check if prowlarrUrl or apiKey are stale and update if so.
		fields, ok := a["fields"].([]any)
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
				if v, _ := field["value"].(string); v != prowlarrURL {
					needsUpdate = true
				}
			case "apiKey":
				if v, _ := field["value"].(string); v != appAPIKey {
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
				f["value"] = prowlarrURL
			case "apiKey":
				f["value"] = appAPIKey
			}
		}
		idVal, ok := a["id"].(float64)
		if !ok {
			slog.Error("unexpected id type in Prowlarr app", "component", "autowire", "app", appName)
			return false
		}
		id := int(idVal)
		_, err = s.ArrPut(prowlarrURL, s.ProwlarrKey, fmt.Sprintf("/api/v1/applications/%d", id), a)
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
			{"name": "prowlarrUrl", "value": prowlarrURL},
			{"name": "baseUrl", "value": appURL},
			{"name": "apiKey", "value": appAPIKey},
		},
	}

	_, err = s.ArrPost(prowlarrURL, s.ProwlarrKey, "/api/v1/applications", payload)
	if err != nil {
		slog.Error("failed to connect Prowlarr app", "component", "autowire", "app", appName, "error", err)
		return false
	}

	slog.Info("connected Prowlarr app", "component", "autowire", "app", appName)
	return true
}
