package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	sonarrURL   = "http://sonarr:8989/sonarr"
	radarrURL   = "http://radarr:7878/radarr"
	prowlarrURL = "http://prowlarr:9696/prowlarr"
)

func AutoWire(s *ServiceClients) error {
	slog.Info("waiting for services to be ready", "component", "autowire")

	if err := waitForServices(s); err != nil {
		return fmt.Errorf("services not ready: %w", err)
	}

	// Reload keys in case they were generated after initial container start
	s.ReloadKeys()

	if s.SonarrKey == "" || s.RadarrKey == "" || s.ProwlarrKey == "" {
		return fmt.Errorf("missing API keys (sonarr=%v radarr=%v prowlarr=%v)",
			s.SonarrKey != "", s.RadarrKey != "", s.ProwlarrKey != "")
	}

	slog.Info("services ready, checking configuration", "component", "autowire")

	sonarrWired := wireDownloadClient(s, "Sonarr", sonarrURL, s.SonarrKey, "/api/v3") &&
		wireRootFolder(s, "Sonarr", sonarrURL, s.SonarrKey, "/api/v3", "/tv")

	radarrWired := wireDownloadClient(s, "Radarr", radarrURL, s.RadarrKey, "/api/v3") &&
		wireRootFolder(s, "Radarr", radarrURL, s.RadarrKey, "/api/v3", "/movies")

	prowlarrWired := wireProwlarrApp(s, "Sonarr", sonarrURL, s.SonarrKey) &&
		wireProwlarrApp(s, "Radarr", radarrURL, s.RadarrKey)

	// Wire Procula import webhooks into Radarr and Sonarr
	wireImportWebhook(s, "Sonarr", sonarrURL, s.SonarrKey, "/api/v3")
	wireImportWebhook(s, "Radarr", radarrURL, s.RadarrKey, "/api/v3")

	// Auto-configure Jellyfin: complete wizard, add media libraries
	wireJellyfin(s)

	// Wire Jellyseerr if enabled (opt-in via JELLYSEERR_ENABLED=true)
	wireJellyseerr(s)

	if sonarrWired && radarrWired && prowlarrWired {
		s.SetWired(true)
		slog.Info("all services wired successfully", "component", "autowire")
	} else {
		slog.Warn("some wiring failed — check logs above", "component", "autowire")
	}

	return nil
}

func waitForServices(s *ServiceClients) error {
	endpoints := map[string]string{
		"sonarr":      sonarrURL + "/ping",
		"radarr":      radarrURL + "/ping",
		"prowlarr":    prowlarrURL + "/ping",
		"qbittorrent": "http://gluetun:8080/",
		"jellyfin":    jellyfinURL + "/System/Info/Public",
	}
	if os.Getenv("JELLYSEERR_ENABLED") == "true" {
		endpoints["jellyseerr"] = "http://jellyseerr:5055/api/v1/status"
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

func wireDownloadClient(s *ServiceClients, name, baseURL, apiKey, apiPath string) bool {
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
			{"name": "category", "value": ""},
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

	hookURL := "http://pelicula-api:8181/api/pelicula/hooks/import"
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

// ── Jellyseerr ─────────────────────────────────────────────────────────────

func wireJellyseerr(s *ServiceClients) {
	if os.Getenv("JELLYSEERR_ENABLED") != "true" {
		return
	}
	slog.Info("checking Jellyseerr", "component", "autowire")

	// Check initialization status
	data, err := jsGet(s, "/api/v1/settings/public", "")
	if err != nil {
		slog.Warn("Jellyseerr not reachable", "component", "autowire", "error", err)
		return
	}

	var pub map[string]any
	json.Unmarshal(data, &pub) //nolint:errcheck
	initialized, _ := pub["initialized"].(bool)

	if !initialized {
		slog.Info("completing Jellyseerr setup wizard", "component", "autowire")
		if err := completeJellyseerrWizard(s); err != nil {
			slog.Error("Jellyseerr wizard setup failed", "component", "autowire", "error", err)
			return
		}
		time.Sleep(2 * time.Second)
	}

	// Get API key: read from settings.json (always present, even before wizard completes).
	apiKey, err := readJellyseerrAPIKey()
	if err != nil {
		slog.Error("can't get Jellyseerr API key", "component", "autowire", "error", err)
		return
	}

	s.mu.Lock()
	s.JellyseerrKey = apiKey
	s.mu.Unlock()
	slog.Info("Jellyseerr API key loaded", "component", "autowire")

	// Wire Radarr and Sonarr into Jellyseerr
	s.mu.RLock()
	radarrKey := s.RadarrKey
	sonarrKey := s.SonarrKey
	s.mu.RUnlock()

	wireJellyseerrService(s, apiKey, "radarr", "radarr", 7878, "/radarr", radarrKey, "/movies")
	wireJellyseerrService(s, apiKey, "sonarr", "sonarr", 8989, "/sonarr", sonarrKey, "/tv")
	slog.Info("Jellyseerr wired", "component", "autowire")
}

func completeJellyseerrWizard(s *ServiceClients) error {
	// Read the API key Jellyseerr generates at startup from its settings.json.
	// Jellyseerr creates this key before any wizard interaction, so it's always available.
	// We use it with X-API-Key to avoid cookie session issues in Go's http client.
	apiKey, err := readJellyseerrAPIKey()
	if err != nil {
		return fmt.Errorf("read jellyseerr api key: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}

	// Step 1: authenticate via Jellyfin — creates the initial admin user in Jellyseerr
	// and saves the Jellyfin server connection in settings.
	// hostname/port/urlBase are separate fields; Jellyseerr rejects a full URL with INVALID_URL.
	// serverType=2 is MediaServerType.JELLYFIN; required on first-run or Jellyseerr throws NoAdminUser.
	authPayload, _ := json.Marshal(map[string]any{
		"username":   "admin",
		"password":   os.Getenv("JELLYFIN_PASSWORD"),
		"hostname":   "jellyfin",
		"port":       8096,
		"urlBase":    "/jellyfin",
		"useSsl":     false,
		"serverType": 2, // MediaServerType.JELLYFIN
	})
	authReq, _ := http.NewRequest("POST", "http://jellyseerr:5055/api/v1/auth/jellyfin",
		bytes.NewReader(authPayload))
	authReq.Header.Set("Content-Type", "application/json")
	authReq.Header.Set("X-API-Key", apiKey)
	authResp, err := client.Do(authReq)
	if err != nil {
		return fmt.Errorf("jellyfin auth: %w", err)
	}
	authResp.Body.Close()
	if authResp.StatusCode >= 400 {
		return fmt.Errorf("jellyfin auth HTTP %d", authResp.StatusCode)
	}

	// Step 2: mark initialization complete using the same API key.
	initReq, _ := http.NewRequest("POST", "http://jellyseerr:5055/api/v1/settings/initialize",
		bytes.NewReader([]byte("{}")))
	initReq.Header.Set("Content-Type", "application/json")
	initReq.Header.Set("X-API-Key", apiKey)
	initResp, err := client.Do(initReq)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	initResp.Body.Close()
	if initResp.StatusCode >= 400 {
		return fmt.Errorf("initialize HTTP %d", initResp.StatusCode)
	}

	slog.Info("Jellyseerr wizard completed", "component", "autowire")
	return nil
}

// readJellyseerrAPIKey reads the API key from Jellyseerr's settings.json.
// Jellyseerr generates this key at startup before any wizard interaction,
// so it is available even on first run. The file is mounted read-only at
// /config/jellyseerr/settings.json inside the middleware container.
func readJellyseerrAPIKey() (string, error) {
	data, err := os.ReadFile("/config/jellyseerr/settings.json")
	if err != nil {
		return "", fmt.Errorf("read settings.json: %w", err)
	}
	var settings struct {
		Main struct {
			APIKey string `json:"apiKey"`
		} `json:"main"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return "", fmt.Errorf("parse settings.json: %w", err)
	}
	if settings.Main.APIKey == "" {
		return "", fmt.Errorf("no apiKey in settings.json")
	}
	return settings.Main.APIKey, nil
}


func wireJellyseerrService(s *ServiceClients, apiKey, svcType, hostname string, port int, urlBase, svcAPIKey, mediaDir string) {
	// Check if already configured
	data, err := jsGet(s, "/api/v1/settings/"+svcType, apiKey)
	if err != nil {
		slog.Error("can't check Jellyseerr service", "component", "autowire", "service_type", svcType, "error", err)
		return
	}

	var existing []map[string]any
	if json.Unmarshal(data, &existing) == nil && len(existing) > 0 {
		slog.Info("Jellyseerr service already configured, skipping", "component", "autowire", "service_type", svcType)
		return
	}

	name := strings.ToUpper(svcType[:1]) + svcType[1:]
	payload := map[string]any{
		"name":              name,
		"hostname":          hostname,
		"port":              port,
		"apiKey":            svcAPIKey,
		"urlBase":           urlBase,
		"useSsl":            false,
		"is4k":             false,
		"isDefault":         true,
		"syncEnabled":       false,
		"preventSearch":     false,
		"activeProfileId":   1,
		"activeProfileName": "Any",
		"activeDirectory":   mediaDir,
	}
	if svcType == "radarr" {
		payload["minimumAvailability"] = "released"
		payload["activeMinimumAvailability"] = "released"
	}
	if svcType == "sonarr" {
		payload["activeAnimeProfileId"] = 1
		payload["activeAnimeDirectory"] = mediaDir
		payload["enableSeasonFolders"] = true
	}

	_, err = jsPost(s, "/api/v1/settings/"+svcType, apiKey, payload)
	if err != nil {
		slog.Error("failed to add Jellyseerr service", "component", "autowire", "service_type", svcType, "error", err)
		return
	}
	slog.Info("added Jellyseerr service", "component", "autowire", "service_type", svcType)
}

func jsGet(s *ServiceClients, path, apiKey string) ([]byte, error) {
	req, err := http.NewRequest("GET", "http://jellyseerr:5055"+path, nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("X-Api-Key", apiKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return body, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func jsPost(s *ServiceClients, path, apiKey string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", "http://jellyseerr:5055"+path, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("X-Api-Key", apiKey)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return body, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
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
		if n, _ := a["name"].(string); n == appName {
			slog.Info("Prowlarr app already connected, skipping", "component", "autowire", "app", appName)
			return true
		}
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
