// Package services provides the ServiceClients aggregator (now named Clients)
// that holds typed API clients for all downstream services.
package services

import (
	"context"
	"encoding/xml"
	"log/slog"
	"net/http"
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

// Clients aggregates all downstream service clients.
type Clients struct {
	configDir string

	// client is a shared *http.Client (10s timeout, UA injection, no retry)
	// used for backend operations that don't go through a typed client:
	//  - CheckHealth's parallel probes against the seven backends (typed
	//    clients would retry on 5xx, exceeding the per-probe ctx budget).
	//  - HTTPClient() exposes it to consumers that wrap it themselves: the
	//    Jellyfin/Procula typed clients via NewWithHTTPClient (constructed
	//    in bootstrap), the autowire service-readiness polling loop, the
	//    actions handler, and the cmd/pelicula-api jobs proxy.
	// All API calls to *arr/qBittorrent go through the typed Sonarr/Radarr/
	// Prowlarr/Qbt clients which have their own *httpx.Client backends with
	// retry/redaction/body-drain.
	client *http.Client

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

	// Typed clients — constructed once in New(); never reassigned.
	// Use the SonarrClient()/RadarrClient()/etc. accessors for new call sites.
	// API keys are updated via SetAPIKey on each client (not by replacing the
	// pointer).
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
	c.Sonarr = arrclient.New(c.sonarrURL, "")
	c.Radarr = arrclient.New(c.radarrURL, "")
	c.Prowlarr = arrclient.New(c.prowlarrURL, "")
	c.Bazarr = bazarrclient.New(c.bazarrURL, "")
	c.JellyfinAPIKey = jellyfinAPIKey
	c.loadKeys()
	return c
}

func (c *Clients) loadKeys() {
	// Read outside the lock to avoid holding it during file I/O.
	sonarr := readAPIKey(c.configDir + "/sonarr/config.xml")
	radarr := readAPIKey(c.configDir + "/radarr/config.xml")
	prowlarr := readAPIKey(c.configDir + "/prowlarr/config.xml")

	c.mu.Lock()
	c.SonarrKey = sonarr
	c.RadarrKey = radarr
	c.ProwlarrKey = prowlarr
	c.mu.Unlock()

	// Update typed-client API keys (their own mutex serialises against in-flight reads).
	c.Sonarr.SetAPIKey(sonarr)
	c.Radarr.SetAPIKey(radarr)
	c.Prowlarr.SetAPIKey(prowlarr)

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

// SetBazarrAPIKey updates the Bazarr API key on the existing typed client and
// persists it. The Bazarr typed client is constructed in New() and never
// replaced.
// Implements autowire.ArrSvc.
func (c *Clients) SetBazarrAPIKey(apiKey string) {
	c.mu.Lock()
	c.BazarrKey = apiKey
	c.mu.Unlock()
	c.Bazarr.SetAPIKey(apiKey)
}

// BazarrClient returns the Bazarr typed client. The pointer is constructed
// once in New() and never reassigned; reloading API keys mutates the client's
// internal state via SetAPIKey rather than replacing the pointer.
// Safe for concurrent use.
// Implements autowire.ArrSvc.
func (c *Clients) BazarrClient() *bazarrclient.Client {
	return c.Bazarr
}

// SonarrClient returns the typed Sonarr client. The pointer is constructed
// once in New() and never reassigned; reloading API keys mutates the
// client's internal state via SetAPIKey rather than replacing the pointer.
// Safe for concurrent use.
func (c *Clients) SonarrClient() *arrclient.Client { return c.Sonarr }

// RadarrClient returns the typed Radarr client. The pointer is constructed
// once in New() and never reassigned; reloading API keys mutates the
// client's internal state via SetAPIKey rather than replacing the pointer.
// Safe for concurrent use.
func (c *Clients) RadarrClient() *arrclient.Client { return c.Radarr }

// ProwlarrClient returns the typed Prowlarr client. The pointer is constructed
// once in New() and never reassigned; reloading API keys mutates the
// client's internal state via SetAPIKey rather than replacing the pointer.
// Safe for concurrent use.
func (c *Clients) ProwlarrClient() *arrclient.Client { return c.Prowlarr }

// QbtClient returns the typed qBittorrent client. The pointer is constructed
// once in New() and never reassigned.
// Safe for concurrent use.
func (c *Clients) QbtClient() *qbtclient.Client { return c.Qbt }

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
func (c *Clients) JellyfinGet(ctx context.Context, path, apiKey string) ([]byte, error) {
	return c.jellyfinGet(ctx, path, apiKey)
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
