package main

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

	arrclient "pelicula-api/internal/clients/arr"
	bazarrclient "pelicula-api/internal/clients/bazarr"
	qbtclient "pelicula-api/internal/clients/qbt"
)

type ServiceClients struct {
	configDir string
	client    *http.Client

	SonarrKey      string
	RadarrKey      string
	ProwlarrKey    string
	BazarrKey      string
	JellyfinAPIKey string
	JellyfinUserID string // pelicula-internal user ID; resolved lazily on first metadata sync

	// Typed clients — initialised by loadKeys() using the loaded API keys.
	// Use these for new call sites; the legacy ArrGet/ArrPost helpers remain
	// for existing code until progressively replaced.
	Sonarr   *arrclient.Client
	Radarr   *arrclient.Client
	Prowlarr *arrclient.Client
	Qbt      *qbtclient.Client
	Bazarr   *bazarrclient.Client

	wired bool
	mu    sync.RWMutex
}

type xmlConfig struct {
	XMLName xml.Name `xml:"Config"`
	ApiKey  string   `xml:"ApiKey"`
}

// qbtBaseURL is the base URL for qBittorrent (runs on gluetun's network namespace).
// No password is required: qBittorrent's config.xml is seeded with the Docker subnet
// (172.16.0.0/12) in the IP bypass whitelist, so requests from within the Docker
// network are admitted without credentials.
var qbtBaseURL = envOr("QBITTORRENT_URL", "http://gluetun:8080")

func NewServiceClients(configDir string) *ServiceClients {
	s := &ServiceClients{
		configDir: configDir,
		client:    &http.Client{Timeout: 10 * time.Second},
		Qbt:       qbtclient.New(qbtBaseURL),
	}
	s.JellyfinAPIKey = os.Getenv("JELLYFIN_API_KEY")
	// If the env var is empty (e.g. container restarted without a full down/up),
	// fall back to reading the key from the mounted .env file directly.
	if s.JellyfinAPIKey == "" {
		if vars, err := parseEnvFile(envPath); err == nil {
			s.JellyfinAPIKey = vars["JELLYFIN_API_KEY"]
		}
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
	// (Re-)initialise typed clients so they always carry the current key.
	s.Sonarr = arrclient.New(sonarrURL, sonarr)
	s.Radarr = arrclient.New(radarrURL, radarr)
	s.Prowlarr = arrclient.New(prowlarrURL, prowlarr)
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

// SonarrRadarrKeys returns a snapshot of the Sonarr and Radarr API keys.
// Implements autowire.ArrSvc.
func (s *ServiceClients) SonarrRadarrKeys() (sonarr, radarr string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.SonarrKey, s.RadarrKey
}

// GetProwlarrKey returns a snapshot of the Prowlarr API key.
// Implements autowire.ArrSvc.
func (s *ServiceClients) GetProwlarrKey() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ProwlarrKey
}

// HTTPClient returns the shared HTTP client.
// Implements autowire.ArrSvc.
func (s *ServiceClients) HTTPClient() *http.Client {
	return s.client
}

// ConfigDir returns the config directory root.
// Implements autowire.ArrSvc.
func (s *ServiceClients) ConfigDir() string {
	return s.configDir
}

// SetBazarrClient installs the Bazarr typed client and persists the key.
// Implements autowire.ArrSvc.
func (s *ServiceClients) SetBazarrClient(apiKey string, client *bazarrclient.Client) {
	s.mu.Lock()
	s.BazarrKey = apiKey
	s.Bazarr = client
	s.mu.Unlock()
}

// BazarrClient returns the current Bazarr typed client (may be nil before wiring).
// Implements autowire.ArrSvc.
func (s *ServiceClients) BazarrClient() *bazarrclient.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Bazarr
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
	resp, err := s.client.Get(qbtBaseURL + path)
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
	req, err := http.NewRequest("POST", qbtBaseURL+path, bytes.NewBufferString(form))
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

// redactedURL returns the URL string with the "apikey" query parameter value
// replaced by "[REDACTED]". Use this before logging any *arr service URLs.
func redactedURL(u *url.URL) string {
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
// Each check uses a per-request 2-second context timeout so one dead backend
// cannot block the entire call.
func (s *ServiceClients) CheckHealth() map[string]string {
	results := make(map[string]string)
	checks := map[string]string{
		"sonarr":      sonarrURL + "/ping",
		"radarr":      radarrURL + "/ping",
		"prowlarr":    prowlarrURL + "/ping",
		"qbittorrent": qbtBaseURL + "/",
		"jellyfin":    jellyfinURL + "/health",
		"procula":     proculaURL + "/ping",
		"bazarr":      bazarrURL + "/",
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
