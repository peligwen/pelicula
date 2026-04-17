// Package services provides the ServiceClients type that wraps all downstream
// service HTTP calls (Sonarr, Radarr, Prowlarr, Bazarr, qBittorrent).
package services

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"pelicula-api/internal/config"
)

// ServiceClients holds API keys and an HTTP client for all downstream services.
type ServiceClients struct {
	ConfigDir string
	URLs      config.URLs
	client    *http.Client

	SonarrKey      string
	RadarrKey      string
	ProwlarrKey    string
	BazarrKey      string
	JellyfinAPIKey string
	JellyfinUserID string // pelicula-internal user ID; resolved lazily on first metadata sync

	wired bool
	mu    sync.RWMutex
}

type xmlConfig struct {
	XMLName xml.Name `xml:"Config"`
	ApiKey  string   `xml:"ApiKey"`
}

// New creates a ServiceClients and loads API keys from the config directory.
func New(configDir string, urls config.URLs) *ServiceClients {
	s := &ServiceClients{
		ConfigDir: configDir,
		URLs:      urls,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
	s.JellyfinAPIKey = os.Getenv("JELLYFIN_API_KEY")
	if s.JellyfinAPIKey == "" {
		if vars, err := parseEnvFile(envPath()); err == nil {
			s.JellyfinAPIKey = vars["JELLYFIN_API_KEY"]
		}
	}
	s.loadKeys()
	return s
}

// envPath returns the path to the .env file.
func envPath() string {
	if v := os.Getenv("ENV_FILE"); v != "" {
		return v
	}
	return "/config/pelicula/.env"
}

// parseEnvFile reads a .env file and returns its key=value pairs.
func parseEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	vars := make(map[string]string)
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		parts := bytes.SplitN(line, []byte("="), 2)
		if len(parts) != 2 {
			continue
		}
		key := string(bytes.TrimSpace(parts[0]))
		val := string(bytes.TrimSpace(parts[1]))
		// Strip surrounding quotes if present
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		vars[key] = val
	}
	return vars, nil
}

func (s *ServiceClients) loadKeys() {
	sonarr := readAPIKey(s.ConfigDir + "/sonarr/config.xml")
	radarr := readAPIKey(s.ConfigDir + "/radarr/config.xml")
	prowlarr := readAPIKey(s.ConfigDir + "/prowlarr/config.xml")

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

// ReloadKeys re-reads API keys from the config XML files.
func (s *ServiceClients) ReloadKeys() {
	s.loadKeys()
}

// Keys returns a snapshot of the API keys under read lock.
func (s *ServiceClients) Keys() (sonarr, radarr, prowlarr string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.SonarrKey, s.RadarrKey, s.ProwlarrKey
}

// SetWired marks the service as fully auto-wired.
func (s *ServiceClients) SetWired(v bool) {
	s.mu.Lock()
	s.wired = v
	s.mu.Unlock()
}

// IsWired reports whether auto-wiring completed successfully.
func (s *ServiceClients) IsWired() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.wired
}

// GetJellyfinAPIKey returns the Jellyfin API key.
func (s *ServiceClients) GetJellyfinAPIKey() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.JellyfinAPIKey
}

// GetJellyfinUserID returns the pelicula-internal Jellyfin user ID.
func (s *ServiceClients) GetJellyfinUserID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.JellyfinUserID
}

// SetJellyfinUserID caches the resolved Jellyfin user ID.
func (s *ServiceClients) SetJellyfinUserID(id string) {
	s.mu.Lock()
	s.JellyfinUserID = id
	s.mu.Unlock()
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

// ArrDelete makes a DELETE request to a *arr service.
func (s *ServiceClients) ArrDelete(baseURL, apiKey, path string) ([]byte, error) {
	return s.arrDo("DELETE", baseURL, apiKey, path, nil)
}

// ArrPut makes a PUT request to a *arr service.
func (s *ServiceClients) ArrPut(baseURL, apiKey, path string, payload any) ([]byte, error) {
	return s.arrDo("PUT", baseURL, apiKey, path, payload)
}

// QbtGet makes a GET request to qBittorrent (via Docker network, auth bypass).
func (s *ServiceClients) QbtGet(path string) ([]byte, error) {
	resp, err := s.client.Get(s.URLs.QBT + path)
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
	req, err := http.NewRequest("POST", s.URLs.QBT+path, bytes.NewBufferString(form))
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

// JellyfinGet makes a GET request to Jellyfin using the API key header.
func (s *ServiceClients) JellyfinGet(path, apiKey string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, s.URLs.Jellyfin+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Emby-Token", apiKey)
	req.Header.Set("X-MediaBrowser-Token", apiKey)
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
		return nil, fmt.Errorf("jellyfin HTTP %d for %s", resp.StatusCode, path)
	}
	return body, nil
}

// HTTPClient returns the underlying *http.Client for use by code that needs
// direct HTTP access (e.g. hooks forwardToProcula).
func (s *ServiceClients) HTTPClient() *http.Client {
	return s.client
}

// CheckHealth checks if each service is reachable.
// Each check uses a per-request 2-second context timeout.
func (s *ServiceClients) CheckHealth() map[string]string {
	results := make(map[string]string)
	checks := map[string]string{
		"sonarr":      s.URLs.Sonarr + "/ping",
		"radarr":      s.URLs.Radarr + "/ping",
		"prowlarr":    s.URLs.Prowlarr + "/ping",
		"qbittorrent": s.URLs.QBT + "/",
		"jellyfin":    s.URLs.Jellyfin + "/health",
		"procula":     s.URLs.Procula + "/ping",
		"bazarr":      s.URLs.Bazarr + "/",
	}
	var wg sync.WaitGroup
	var mu sync.Mutex

	for name, checkURL := range checks {
		wg.Add(1)
		go func(name, checkURL string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, checkURL, nil)
			status := "down"
			if err == nil {
				resp, err := s.client.Do(req)
				if err == nil {
					resp.Body.Close()
					if resp.StatusCode < 400 {
						status = "up"
					}
				}
			}
			mu.Lock()
			results[name] = status
			mu.Unlock()
		}(name, checkURL)
	}

	wg.Wait()
	return results
}

// RedactedURL returns the URL string with the "apikey" query parameter value
// replaced by "[REDACTED]". Use this before logging any *arr service URLs.
func RedactedURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	q := u.Query()
	if q.Get("apikey") != "" {
		q.Set("apikey", "[REDACTED]")
		copy := *u
		copy.RawQuery = q.Encode()
		return copy.String()
	}
	return u.String()
}
