package main

import (
	"encoding/json"
	"fmt"
	"log"
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
	json.Unmarshal(data, &clients)

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
	json.Unmarshal(data, &folders)

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

func wireProwlarrApp(s *ServiceClients, appName, appURL, appAPIKey string) bool {
	// Check existing applications
	data, err := s.ArrGet(prowlarrURL, s.ProwlarrKey, "/api/v1/applications")
	if err != nil {
		log.Printf("[autowire] Prowlarr: failed to check applications: %v", err)
		return false
	}

	var apps []map[string]any
	json.Unmarshal(data, &apps)

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
