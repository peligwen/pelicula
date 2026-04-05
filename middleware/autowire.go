package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
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
	log.Println("[autowire] waiting for services to be ready...")

	if err := waitForServices(s); err != nil {
		return fmt.Errorf("services not ready: %w", err)
	}

	// Reload keys in case they were generated after initial container start
	s.ReloadKeys()

	if s.SonarrKey == "" || s.RadarrKey == "" || s.ProwlarrKey == "" {
		return fmt.Errorf("missing API keys (sonarr=%v radarr=%v prowlarr=%v)",
			s.SonarrKey != "", s.RadarrKey != "", s.ProwlarrKey != "")
	}

	log.Println("[autowire] services ready, checking configuration...")

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
		log.Println("[autowire] all services wired successfully")
	} else {
		log.Println("[autowire] some wiring failed — check logs above")
	}

	return nil
}

func waitForServices(s *ServiceClients) error {
	endpoints := map[string]string{
		"sonarr":       sonarrURL + "/ping",
		"radarr":       radarrURL + "/ping",
		"prowlarr":     prowlarrURL + "/ping",
		"qbittorrent":  "http://gluetun:8080/",
		"jellyfin":     jellyfinURL + "/System/Info/Public",
	}

	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		allReady := true
		for name, url := range endpoints {
			resp, err := s.client.Get(url)
			if err != nil || resp.StatusCode >= 500 {
				allReady = false
				break
			}
			resp.Body.Close()
			_ = name
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
		log.Printf("[autowire] %s: failed to check download clients: %v", name, err)
		return false
	}

	var clients []map[string]any
	if err := json.Unmarshal(data, &clients); err != nil {
		log.Printf("[autowire] %s: failed to parse download clients response: %v", name, err)
		return false
	}

	for _, c := range clients {
		if impl, _ := c["implementation"].(string); impl == "QBittorrent" {
			log.Printf("[autowire] %s: qBittorrent already configured, skipping", name)
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
		log.Printf("[autowire] %s: failed to add qBittorrent: %v", name, err)
		return false
	}

	log.Printf("[autowire] %s: added qBittorrent download client", name)
	return true
}

func wireRootFolder(s *ServiceClients, name, baseURL, apiKey, apiPath, folderPath string) bool {
	// Check existing root folders
	data, err := s.ArrGet(baseURL, apiKey, apiPath+"/rootfolder")
	if err != nil {
		log.Printf("[autowire] %s: failed to check root folders: %v", name, err)
		return false
	}

	var folders []map[string]any
	if err := json.Unmarshal(data, &folders); err != nil {
		log.Printf("[autowire] %s: failed to parse root folders response: %v", name, err)
		return false
	}

	for _, f := range folders {
		if path, _ := f["path"].(string); path == folderPath {
			log.Printf("[autowire] %s: root folder %s already configured, skipping", name, folderPath)
			return true
		}
	}

	payload := map[string]any{
		"path": folderPath,
	}

	_, err = s.ArrPost(baseURL, apiKey, apiPath+"/rootfolder", payload)
	if err != nil {
		log.Printf("[autowire] %s: failed to add root folder %s: %v", name, folderPath, err)
		return false
	}

	log.Printf("[autowire] %s: added root folder %s", name, folderPath)
	return true
}

// wireImportWebhook adds a Procula import webhook notification to a *arr app.
// It is idempotent — won't add a second "Procula" webhook if one already exists.
func wireImportWebhook(s *ServiceClients, name, baseURL, apiKey, apiPath string) {
	data, err := s.ArrGet(baseURL, apiKey, apiPath+"/notification")
	if err != nil {
		log.Printf("[autowire] %s: failed to check notifications: %v", name, err)
		return
	}

	var existing []map[string]any
	if err := json.Unmarshal(data, &existing); err != nil {
		log.Printf("[autowire] %s: failed to parse notifications response: %v", name, err)
		return
	}

	for _, n := range existing {
		if n, _ := n["name"].(string); n == "Procula" {
			log.Printf("[autowire] %s: Procula webhook already configured, skipping", name)
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
		log.Printf("[autowire] %s: failed to add Procula webhook: %v", name, err)
		return
	}
	log.Printf("[autowire] %s: added Procula import webhook → %s", name, hookURL)
}

// ── Jellyseerr ─────────────────────────────────────────────────────────────

func wireJellyseerr(s *ServiceClients) {
	if os.Getenv("JELLYSEERR_ENABLED") != "true" {
		return
	}
	log.Println("[autowire] Jellyseerr: checking...")

	// Check initialization status
	data, err := jsGet(s, "/api/v1/settings/public", "")
	if err != nil {
		log.Printf("[autowire] Jellyseerr: not reachable (%v)", err)
		return
	}

	var pub map[string]any
	json.Unmarshal(data, &pub) //nolint:errcheck
	initialized, _ := pub["initialized"].(bool)

	if !initialized {
		log.Println("[autowire] Jellyseerr: not initialized — open /jellyseerr to complete the setup wizard")
		return
	}

	// Authenticate via Jellyfin (admin/no-password, matches our default Jellyfin setup)
	apiKey, err := jellyseerrGetAPIKey()
	if err != nil {
		log.Printf("[autowire] Jellyseerr: can't get API key (%v)", err)
		return
	}

	s.mu.Lock()
	s.JellyseerrKey = apiKey
	s.mu.Unlock()
	log.Println("[autowire] Jellyseerr: API key loaded")

	// Wire Radarr and Sonarr into Jellyseerr
	s.mu.RLock()
	radarrKey := s.RadarrKey
	sonarrKey := s.SonarrKey
	s.mu.RUnlock()

	wireJellyseerrService(s, apiKey, "radarr", "radarr", 7878, "/radarr", radarrKey, "/movies")
	wireJellyseerrService(s, apiKey, "sonarr", "sonarr", 8989, "/sonarr", sonarrKey, "/tv")
	log.Println("[autowire] Jellyseerr: wired")
}

func jellyseerrGetAPIKey() (string, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 10 * time.Second, Jar: jar}

	// Auth via Jellyfin — admin account with no password (our default Jellyfin setup)
	payload, _ := json.Marshal(map[string]any{
		"username": "admin",
		"password": "",
		"hostname": "http://jellyfin:8096/jellyfin",
	})
	resp, err := client.Post("http://jellyseerr:5055/api/v1/auth/jellyfin",
		"application/json", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("auth request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("auth HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Get main settings — session cookie is carried by the jar
	req, _ := http.NewRequest("GET", "http://jellyseerr:5055/api/v1/settings/main", nil)
	resp2, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("get settings: %w", err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)

	var settings map[string]any
	if err := json.Unmarshal(body, &settings); err != nil {
		return "", fmt.Errorf("parse settings: %w", err)
	}
	apiKey, _ := settings["apiKey"].(string)
	if apiKey == "" {
		return "", fmt.Errorf("no apiKey in settings response")
	}
	return apiKey, nil
}

func wireJellyseerrService(s *ServiceClients, apiKey, svcType, hostname string, port int, urlBase, svcAPIKey, mediaDir string) {
	// Check if already configured
	data, err := jsGet(s, "/api/v1/settings/"+svcType, apiKey)
	if err != nil {
		log.Printf("[autowire] Jellyseerr: can't check %s: %v", svcType, err)
		return
	}

	var existing []map[string]any
	if json.Unmarshal(data, &existing) == nil && len(existing) > 0 {
		log.Printf("[autowire] Jellyseerr: %s already configured, skipping", svcType)
		return
	}

	name := strings.ToUpper(svcType[:1]) + svcType[1:]
	payload := map[string]any{
		"name":               name,
		"hostname":           hostname,
		"port":               port,
		"apiKey":             svcAPIKey,
		"urlBase":            urlBase,
		"useSsl":             false,
		"isDefault":          true,
		"syncEnabled":        false,
		"preventSearch":      false,
		"activeProfileId":    1,
		"activeProfileName":  "Any",
		"activeDirectory":    mediaDir,
	}
	if svcType == "sonarr" {
		payload["activeAnimeProfileId"] = 1
		payload["activeAnimeDirectory"] = mediaDir
		payload["enableSeasonFolders"]  = true
	}

	_, err = jsPost(s, "/api/v1/settings/"+svcType, apiKey, payload)
	if err != nil {
		log.Printf("[autowire] Jellyseerr: failed to add %s: %v", svcType, err)
		return
	}
	log.Printf("[autowire] Jellyseerr: added %s", svcType)
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
		return body, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
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
		return body, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

func wireProwlarrApp(s *ServiceClients, appName, appURL, appAPIKey string) bool {
	// Check existing applications
	data, err := s.ArrGet(prowlarrURL, s.ProwlarrKey, "/api/v1/applications")
	if err != nil {
		log.Printf("[autowire] Prowlarr: failed to check applications: %v", err)
		return false
	}

	var apps []map[string]any
	if err := json.Unmarshal(data, &apps); err != nil {
		log.Printf("[autowire] Prowlarr: failed to parse applications response: %v", err)
		return false
	}

	for _, a := range apps {
		if n, _ := a["name"].(string); n == appName {
			log.Printf("[autowire] Prowlarr: %s already connected, skipping", appName)
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
		log.Printf("[autowire] Prowlarr: failed to add %s: %v", appName, err)
		return false
	}

	log.Printf("[autowire] Prowlarr: connected to %s", appName)
	return true
}
