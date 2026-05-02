// Package services provides the ServiceClients aggregator (now named Clients)
// that holds typed API clients and HTTP helpers for all downstream services.
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
	"pelicula-api/internal/httpx"

	arrclient "pelicula-api/internal/clients/arr"
	bazarrclient "pelicula-api/internal/clients/bazarr"
	qbtclient "pelicula-api/internal/clients/qbt"
)

// Version is the middleware service version. Set at build time via ldflags:
//
//	-ldflags "-X pelicula-api/internal/app/services.Version=1.2.3"
var Version = "dev"

// uaTransport wraps an http.RoundTripper to inject a User-Agent header on
// every outbound request.
type uaTransport struct {
	base      http.RoundTripper
	userAgent string
}

func (t *uaTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("User-Agent", t.userAgent)
	return t.base.RoundTrip(r)
}

// Clients aggregates all downstream service clients and HTTP helpers.
type Clients struct {
	configDir string
	client    *http.Client

	// URL fields — set from cfg.URLs in New(); never use package-level globals.
	sonarrURL   string
	radarrURL   string
	prowlarrURL string
	qbtURL      string
	bazarrURL   string
	jellyfinURL string
	proculaURL  string

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

// New constructs a Clients instance from the given config. jellyfinAPIKey is
// pre-resolved by the caller (with .env fallback) to avoid an import cycle on
// parseEnvFile / envPath which live in cmd/.
func New(cfg *config.Config, jellyfinAPIKey string) *Clients {
	c := &Clients{
		configDir: cfg.ConfigDir,
		client: &http.Client{
			Timeout: 10 * time.Second,
			Transport: &uaTransport{
				base:      http.DefaultTransport,
				userAgent: httpx.DefaultUserAgent,
			},
		},
		sonarrURL:   cfg.URLs.Sonarr,
		radarrURL:   cfg.URLs.Radarr,
		prowlarrURL: cfg.URLs.Prowlarr,
		qbtURL:      cfg.URLs.QBT,
		bazarrURL:   cfg.URLs.Bazarr,
		jellyfinURL: cfg.URLs.Jellyfin,
		proculaURL:  cfg.URLs.Procula,
	}
	c.Qbt = qbtclient.New(c.qbtURL)
	c.JellyfinAPIKey = jellyfinAPIKey
	c.loadKeys()
	return c
}

func (c *Clients) loadKeys() {
	// Read outside the lock to avoid holding it during file I/O
	sonarr := readAPIKey(c.configDir + "/sonarr/config.xml")
	radarr := readAPIKey(c.configDir + "/radarr/config.xml")
	prowlarr := readAPIKey(c.configDir + "/prowlarr/config.xml")

	c.mu.Lock()
	c.SonarrKey = sonarr
	c.RadarrKey = radarr
	c.ProwlarrKey = prowlarr
	// (Re-)initialise typed clients so they always carry the current key.
	c.Sonarr = arrclient.New(c.sonarrURL, sonarr)
	c.Radarr = arrclient.New(c.radarrURL, radarr)
	c.Prowlarr = arrclient.New(c.prowlarrURL, prowlarr)
	c.mu.Unlock()

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

func (c *Clients) ReloadKeys() {
	c.loadKeys()
}

// Keys returns a snapshot of the API keys under read lock.
func (c *Clients) Keys() (sonarr, radarr, prowlarr string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.SonarrKey, c.RadarrKey, c.ProwlarrKey
}

func (c *Clients) SetWired(v bool) {
	c.mu.Lock()
	c.wired = v
	c.mu.Unlock()
}

func (c *Clients) IsWired() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.wired
}

// SonarrRadarrKeys returns a snapshot of the Sonarr and Radarr API keys.
// Implements autowire.ArrSvc.
func (c *Clients) SonarrRadarrKeys() (sonarr, radarr string) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.SonarrKey, c.RadarrKey
}

// GetProwlarrKey returns a snapshot of the Prowlarr API key.
// Implements autowire.ArrSvc.
func (c *Clients) GetProwlarrKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ProwlarrKey
}

// HTTPClient returns the shared HTTP client.
// Implements autowire.ArrSvc.
func (c *Clients) HTTPClient() *http.Client {
	return c.client
}

// ConfigDir returns the config directory root.
// Implements autowire.ArrSvc.
func (c *Clients) ConfigDir() string {
	return c.configDir
}

// SetBazarrClient installs the Bazarr typed client and persists the key.
// Implements autowire.ArrSvc.
func (c *Clients) SetBazarrClient(apiKey string, client *bazarrclient.Client) {
	c.mu.Lock()
	c.BazarrKey = apiKey
	c.Bazarr = client
	c.mu.Unlock()
}

// BazarrClient returns the current Bazarr typed client (may be nil before wiring).
// Implements autowire.ArrSvc.
func (c *Clients) BazarrClient() *bazarrclient.Client {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Bazarr
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
//
// Deprecated: prefer the typed arr.Client methods (GetMovies, GetSeries, etc.)
// which use the shared client pool and provide better context propagation.
// New call sites should use arr.Client; arrDo remains for the ~145 existing
// callers that pre-date arr.Client.
func (c *Clients) arrDo(method, baseURL, apiKey, path string, payload any) ([]byte, error) {
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
	resp, err := c.client.Do(req)
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
func (c *Clients) ArrGet(baseURL, apiKey, path string) ([]byte, error) {
	return c.arrDo("GET", baseURL, apiKey, path, nil)
}

// ArrPost makes a POST request to a *arr service.
func (c *Clients) ArrPost(baseURL, apiKey, path string, payload any) ([]byte, error) {
	return c.arrDo("POST", baseURL, apiKey, path, payload)
}

// QbtGet makes a GET request to qBittorrent (via Docker network, auth bypass).
func (c *Clients) QbtGet(path string) ([]byte, error) {
	resp, err := c.client.Get(c.qbtURL + path)
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
func (c *Clients) QbtPost(path string, form string) error {
	req, err := http.NewRequest("POST", c.qbtURL+path, bytes.NewBufferString(form))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.client.Do(req)
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
func (c *Clients) ArrDelete(baseURL, apiKey, path string) ([]byte, error) {
	return c.arrDo("DELETE", baseURL, apiKey, path, nil)
}

// ArrPut makes a PUT request to a *arr service.
func (c *Clients) ArrPut(baseURL, apiKey, path string, payload any) ([]byte, error) {
	return c.arrDo("PUT", baseURL, apiKey, path, payload)
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

// GetJellyfinAPIKey returns the cached Jellyfin API key.
// Implements catalog.JellyfinMetaClient.
func (c *Clients) GetJellyfinAPIKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.JellyfinAPIKey
}

// GetJellyfinUserID returns the cached Jellyfin user ID for pelicula-internal.
// Implements catalog.JellyfinMetaClient.
func (c *Clients) GetJellyfinUserID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.JellyfinUserID
}

// SetJellyfinAPIKey stores the Jellyfin API key.
func (c *Clients) SetJellyfinAPIKey(key string) {
	c.mu.Lock()
	c.JellyfinAPIKey = key
	c.mu.Unlock()
}

// SetJellyfinUserID stores the resolved Jellyfin user ID.
// Implements catalog.JellyfinMetaClient.
func (c *Clients) SetJellyfinUserID(id string) {
	c.mu.Lock()
	c.JellyfinUserID = id
	c.mu.Unlock()
}

// JellyfinGet makes an authenticated GET request to Jellyfin.
// Implements catalog.JellyfinMetaClient.
func (c *Clients) JellyfinGet(path, apiKey string) ([]byte, error) {
	return c.jellyfinGet(context.Background(), path, apiKey)
}

// ArrGetAllQueueRecords fetches all records from an *arr queue endpoint by paginating.
func (c *Clients) ArrGetAllQueueRecords(baseURL, apiKey, apiVer, extraParams string) ([]map[string]any, error) {
	const pageSize = 100
	var all []map[string]any
	page := 1
	for {
		path := fmt.Sprintf("%s/queue?pageSize=%d&page=%d%s", apiVer, pageSize, page, extraParams)
		data, err := c.ArrGet(baseURL, apiKey, path)
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
func (c *Clients) CheckHealth() map[string]string {
	results := make(map[string]string)
	checks := map[string]string{
		"sonarr":      c.sonarrURL + "/ping",
		"radarr":      c.radarrURL + "/ping",
		"prowlarr":    c.prowlarrURL + "/ping",
		"qbittorrent": c.qbtURL + "/",
		"jellyfin":    c.jellyfinURL + "/health",
		"procula":     c.proculaURL + "/ping",
		"bazarr":      c.bazarrURL + "/",
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
				resp, err := c.client.Do(req)
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

// ensure redactedURL is used (it's a utility; suppress unused warning if needed)
var _ = redactedURL
