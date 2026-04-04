package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

type ServiceClients struct {
	configDir string
	client    *http.Client

	SonarrKey   string
	RadarrKey   string
	ProwlarrKey string

	wired bool
	mu    sync.RWMutex
}

type xmlConfig struct {
	XMLName xml.Name `xml:"Config"`
	ApiKey  string   `xml:"ApiKey"`
}

func NewServiceClients(configDir string) *ServiceClients {
	s := &ServiceClients{
		configDir: configDir,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
	s.loadKeys()
	return s
}

func (s *ServiceClients) loadKeys() {
	s.SonarrKey = readAPIKey(s.configDir + "/sonarr/config.xml")
	s.RadarrKey = readAPIKey(s.configDir + "/radarr/config.xml")
	s.ProwlarrKey = readAPIKey(s.configDir + "/prowlarr/config.xml")

	if s.SonarrKey != "" {
		log.Printf("[services] loaded Sonarr API key")
	}
	if s.RadarrKey != "" {
		log.Printf("[services] loaded Radarr API key")
	}
	if s.ProwlarrKey != "" {
		log.Printf("[services] loaded Prowlarr API key")
	}
}

func (s *ServiceClients) ReloadKeys() {
	s.loadKeys()
}

func (s *ServiceClients) SetWired(v bool) {
	s.mu.Lock()
	s.wired = v
	s.mu.Unlock()
}

func (s *ServiceClients) IsWired() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.wired
}

func readAPIKey(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cfg xmlConfig
	if err := xml.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	return cfg.ApiKey
}

// ArrGet makes a GET request to a *arr service.
func (s *ServiceClients) ArrGet(baseURL, apiKey, path string) ([]byte, error) {
	req, err := http.NewRequest("GET", baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", apiKey)
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

// ArrPost makes a POST request to a *arr service.
func (s *ServiceClients) ArrPost(baseURL, apiKey, path string, payload any) ([]byte, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", baseURL+path, bytes.NewReader(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", apiKey)
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

// QbtGet makes a GET request to qBittorrent (via Docker network, auth bypass).
func (s *ServiceClients) QbtGet(path string) ([]byte, error) {
	resp, err := s.client.Get("http://gluetun:8080" + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

// QbtPost makes a form-encoded POST request to qBittorrent.
func (s *ServiceClients) QbtPost(path string, form string) error {
	req, err := http.NewRequest("POST", "http://gluetun:8080"+path, bytes.NewBufferString(form))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// ArrDelete makes a DELETE request to a *arr service.
func (s *ServiceClients) ArrDelete(baseURL, apiKey, path string) ([]byte, error) {
	req, err := http.NewRequest("DELETE", baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", apiKey)
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

// ArrPut makes a PUT request to a *arr service.
func (s *ServiceClients) ArrPut(baseURL, apiKey, path string, payload any) ([]byte, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest("PUT", baseURL+path, bytes.NewReader(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", apiKey)
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

// CheckHealth checks if each service is reachable.
func (s *ServiceClients) CheckHealth() map[string]string {
	results := make(map[string]string)
	checks := map[string]string{
		"sonarr":      "http://sonarr:8989/sonarr/ping",
		"radarr":      "http://radarr:7878/radarr/ping",
		"prowlarr":    "http://prowlarr:9696/prowlarr/ping",
		"qbittorrent": "http://gluetun:8080/",
		"jellyfin":    "http://jellyfin:8096/jellyfin/health",
	}

	var wg sync.WaitGroup
	var mu sync.Mutex

	for name, url := range checks {
		wg.Add(1)
		go func(name, url string) {
			defer wg.Done()
			resp, err := s.client.Get(url)
			status := "down"
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode < 500 {
					status = "up"
				}
			}
			mu.Lock()
			results[name] = status
			mu.Unlock()
		}(name, url)
	}

	wg.Wait()
	return results
}
