package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
)

type ServiceClients struct {
	configDir string
	client    *http.Client

	SonarrKey     string
	RadarrKey     string
	ProwlarrKey   string
	JellyseerrKey string
	BazarrKey     string

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
	// Read outside the lock to avoid holding it during file I/O
	sonarr := readAPIKey(s.configDir + "/sonarr/config.xml")
	radarr := readAPIKey(s.configDir + "/radarr/config.xml")
	prowlarr := readAPIKey(s.configDir + "/prowlarr/config.xml")

	s.mu.Lock()
	s.SonarrKey = sonarr
	s.RadarrKey = radarr
	s.ProwlarrKey = prowlarr
	s.mu.Unlock()

	if sonarr != "" {
		slog.Info("loaded API key", "component", "services", "service", "sonarr")
	}
	if radarr != "" {
		slog.Info("loaded API key", "component", "services", "service", "radarr")
	}
	if prowlarr != "" {
		slog.Info("loaded API key", "component", "services", "service", "prowlarr")
	}
}

func (s *ServiceClients) ReloadKeys() {
	s.loadKeys()
}

// Keys returns a snapshot of the API keys under read lock.
func (s *ServiceClients) Keys() (sonarr, radarr, prowlarr string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.SonarrKey, s.RadarrKey, s.ProwlarrKey
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

// arrDo is the shared implementation for all *arr-compatible HTTP calls.
// The apiKey is sent as X-Api-Key. For POST/PUT a JSON payload is required;
// for GET/DELETE pass nil.
func (s *ServiceClients) arrDo(method, baseURL, apiKey, path string, payload any) ([]byte, error) {
	var bodyReader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", apiKey)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
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

// ArrGet makes a GET request to a *arr service.
func (s *ServiceClients) ArrGet(baseURL, apiKey, path string) ([]byte, error) {
	return s.arrDo("GET", baseURL, apiKey, path, nil)
}

// ArrPost makes a POST request to a *arr service.
func (s *ServiceClients) ArrPost(baseURL, apiKey, path string, payload any) ([]byte, error) {
	return s.arrDo("POST", baseURL, apiKey, path, payload)
}

// QbtGet makes a GET request to qBittorrent (via Docker network, auth bypass).
func (s *ServiceClients) QbtGet(path string) ([]byte, error) {
	resp, err := s.client.Get("http://gluetun:8080" + path)
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
	return s.arrDo("DELETE", baseURL, apiKey, path, nil)
}

// ArrPut makes a PUT request to a *arr service.
func (s *ServiceClients) ArrPut(baseURL, apiKey, path string, payload any) ([]byte, error) {
	return s.arrDo("PUT", baseURL, apiKey, path, payload)
}

// ArrGetAllQueueRecords fetches all records from an *arr queue endpoint by paginating.
func (s *ServiceClients) ArrGetAllQueueRecords(baseURL, apiKey, apiVer, extraParams string) ([]map[string]any, error) {
	const pageSize = 100
	var all []map[string]any
	page := 1
	for {
		path := fmt.Sprintf("%s/queue?pageSize=%d&page=%d%s", apiVer, pageSize, page, extraParams)
		data, err := s.ArrGet(baseURL, apiKey, path)
		if err != nil {
			return all, err
		}
		var resp struct {
			TotalRecords int              `json:"totalRecords"`
			Records      []map[string]any `json:"records"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return all, err
		}
		all = append(all, resp.Records...)
		if len(all) >= resp.TotalRecords || len(resp.Records) == 0 {
			break
		}
		page++
	}
	return all, nil
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
		"procula":     "http://procula:8282/ping",
		"bazarr":      "http://bazarr:6767/bazarr/",
	}
	if os.Getenv("JELLYSEERR_ENABLED") == "true" {
		checks["jellyseerr"] = "http://jellyseerr:5055/api/v1/status"
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
				if resp.StatusCode < 400 {
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
