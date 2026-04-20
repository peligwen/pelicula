// Package app defines the App struct that wires together all pelicula-api
// application state. It is constructed by package bootstrap and consumed by
// package supervisor and cmd/pelicula-api/main.go.
package app

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"pelicula-api/httputil"
	"pelicula-api/internal/app/actions"
	"pelicula-api/internal/app/adminops"
	"pelicula-api/internal/app/autowire"
	"pelicula-api/internal/app/backup"
	"pelicula-api/internal/app/catalog"
	"pelicula-api/internal/app/downloads"
	"pelicula-api/internal/app/health"
	"pelicula-api/internal/app/hooks"
	jfapp "pelicula-api/internal/app/jellyfin"
	"pelicula-api/internal/app/library"
	"pelicula-api/internal/app/network"
	"pelicula-api/internal/app/search"
	appservices "pelicula-api/internal/app/services"
	"pelicula-api/internal/app/settings"
	"pelicula-api/internal/app/sse"
	"pelicula-api/internal/app/sysinfo"
	"pelicula-api/internal/app/vpnwatchdog"
	"pelicula-api/internal/config"
	"pelicula-api/internal/peligrosa"
)

// App holds all wired-up application state. Fields are exported so that
// cmd/pelicula-api/main.go, bootstrap, and supervisor can access them.
type App struct {
	Svc             *appservices.Clients
	URLs            config.URLs
	SSEHub          *sse.Hub
	SSEPoller       *sse.Poller
	CatalogDB       *sql.DB
	ArrCatalogCache *catalog.CatalogCache
	MainDB          *sql.DB
	Auth            *peligrosa.Auth
	Deps            *peligrosa.Deps
	Invites         *peligrosa.InviteStore
	Requests        *peligrosa.RequestStore
	IdxCache        IndexerCountCache
	StatusTTL       StatusTTLCache
	BackupHandler   *backup.Handler
	DLHandler       *downloads.Handler
	HealthHandler   *health.Handler
	SysinfoHandler  *sysinfo.Handler
	HooksHandler    *hooks.Handler
	LibHandler      *library.Handler
	CatalogHandler  *catalog.Handler
	JFHandler       *jfapp.Handler
	SearchHandler   *search.Handler
	SettingsHandler *settings.Handler
	ActionsHandler  *actions.Handler
	AdminHandler    *adminops.Handler
	NetworkHandler  *network.Handler
	VPNConfigured   bool
	AutowireState   *autowire.AutowireState
	Autowirer       *autowire.Autowirer
	Watchdog        *vpnwatchdog.Watchdog
}

// ── IndexerCountCache ──────────────────────────────────────────────────────────

const indexerCountTTL = 5 * time.Minute

// IndexerCountCache caches the Prowlarr indexer count with a TTL.
type IndexerCountCache struct {
	mu          sync.Mutex
	count       *int
	fetchedAt   time.Time
	ProwlarrURL string // set by bootstrap
}

// Get returns the cached count if fresh, or fetches it from Prowlarr.
func (c *IndexerCountCache) Get(svc *appservices.Clients) *int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.count != nil && time.Since(c.fetchedAt) < indexerCountTTL {
		return c.count
	}
	_, _, prowlarrKey := svc.Keys()
	if prowlarrKey == "" {
		return c.count
	}
	data, err := svc.ArrGet(c.ProwlarrURL, prowlarrKey, "/api/v1/indexer")
	if err != nil {
		return c.count
	}
	var indexers []map[string]any
	if json.Unmarshal(data, &indexers) != nil {
		return c.count
	}
	n := len(indexers)
	c.count = &n
	c.fetchedAt = time.Now()
	return c.count
}

// Invalidate clears the cached count so the next Get re-fetches.
func (c *IndexerCountCache) Invalidate() {
	c.mu.Lock()
	c.fetchedAt = time.Time{}
	c.mu.Unlock()
}

// ── statusTTLCache ─────────────────────────────────────────────────────────────

// StatusTTLCache is a simple single-value cache with a TTL for the status endpoint.
type StatusTTLCache struct {
	mu        sync.Mutex
	value     map[string]string
	fetchedAt time.Time
	ttl       time.Duration
}

// NewStatusTTLCache constructs a StatusTTLCache with the given TTL.
func NewStatusTTLCache(ttl time.Duration) StatusTTLCache {
	return StatusTTLCache{ttl: ttl}
}

// Get returns the cached value if fresh, otherwise calls fetch to refresh it.
func (c *StatusTTLCache) Get(fetch func() (map[string]string, error)) (map[string]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.value != nil && time.Since(c.fetchedAt) < c.ttl {
		return c.value, nil
	}
	v, err := fetch()
	if err == nil {
		c.value = v
		c.fetchedAt = time.Now()
	}
	return v, err
}

// Invalidate clears the cached value so the next Get re-fetches.
func (c *StatusTTLCache) Invalidate() {
	c.mu.Lock()
	c.value = nil
	c.fetchedAt = time.Time{}
	c.mu.Unlock()
}

// ── HandleStatus ──────────────────────────────────────────────────────────────

// HandleStatus serves GET /api/pelicula/status. It is an exported method so
// the router package can register it as an http.HandlerFunc.
func (a *App) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	_, _, prowlarrKey := a.Svc.Keys()
	var idxCount *int
	if prowlarrKey != "" {
		idxCount = a.IdxCache.Get(a.Svc)
	}

	svcHealth, _ := a.StatusTTL.Get(func() (map[string]string, error) {
		return a.Svc.CheckHealth(), nil
	})
	status := map[string]any{
		"status":         "ok",
		"services":       svcHealth,
		"wired":          a.Svc.IsWired(),
		"indexers":       idxCount,
		"vpn_configured": a.VPNConfigured,
		"warnings":       a.LibHandler.CheckLibraryAccess(),
	}
	httputil.WriteJSON(w, status)
}

// ── WatchdogStateAdapter ──────────────────────────────────────────────────────

// WatchdogStateAdapter converts a vpnwatchdog.State to the health package's
// WatchdogState representation.
func WatchdogStateAdapter(ws vpnwatchdog.State) health.WatchdogState {
	return health.WatchdogState{
		PortForwardStatus: ws.PortForwardStatus,
		ForwardedPort:     ws.ForwardedPort,
		LastSyncedAt:      ws.LastSyncedAt,
		RestartAttempts:   ws.RestartAttempts,
		ConsecutiveZero:   ws.ConsecutiveZero,
		GraceRemaining:    ws.GraceRemaining,
		CooldownRemaining: ws.CooldownRemaining,
		LastTransitionAt:  ws.LastTransitionAt,
		VPNTunnelStatus:   ws.VPNTunnelStatus,
	}
}
