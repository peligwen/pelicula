// Package app defines the App struct that wires together all pelicula-api
// application state. It is constructed by package bootstrap and consumed by
// package supervisor and cmd/pelicula-api/main.go.
package app

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"sort"
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
	"pelicula-api/internal/app/journey"
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
	IdxCache        IndexerStatusCache
	StatusTTL       StatusTTLCache
	BackupHandler   *backup.Handler
	DLHandler       *downloads.Handler
	HealthHandler   *health.Handler
	SysinfoHandler  *sysinfo.Handler
	HooksHandler    *hooks.Handler
	LibHandler      *library.Handler
	CatalogHandler  *catalog.Handler
	JFHandler       *jfapp.Handler
	JFInfoHandler   *jfapp.InfoHandler
	SearchHandler   *search.Handler
	JourneyHandler  *journey.Handler
	SettingsHandler *settings.Handler
	ActionsHandler  *actions.Handler
	AdminHandler    *adminops.Handler
	NetworkHandler  *network.Handler
	VPNConfigured   bool
	Autowirer       *autowire.Autowirer
	Watchdog        *vpnwatchdog.Watchdog
}

// Close stops the auth cleanup goroutine and closes the SQLite handles. Safe
// to call once; subsequent calls are no-ops on nil fields.
func (a *App) Close() error {
	if a.Auth != nil {
		a.Auth.Stop()
	}
	var firstErr error
	if a.MainDB != nil {
		if err := a.MainDB.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if a.CatalogDB != nil {
		if err := a.CatalogDB.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ── IndexerStatusCache ─────────────────────────────────────────────────────────

const indexerStatusTTL = 5 * time.Minute

// PausedIndexer is one Prowlarr indexer currently inside a failure-driven
// disable window (repeated errors or remote rate limiting — Prowlarr backs
// off with an escalating disabledTill). Manual enable/disable is a user
// choice and is deliberately not reported here.
type PausedIndexer struct {
	ID           int       `json:"id"`
	Name         string    `json:"name"`
	DisabledTill time.Time `json:"disabledTill"`
}

// IndexerStatusCache caches the Prowlarr indexer count and the list of
// failure-paused indexers with a TTL.
type IndexerStatusCache struct {
	mu        sync.Mutex
	count     *int
	paused    []PausedIndexer
	fetchedAt time.Time
}

// Get returns the cached count and paused list if fresh, or fetches both
// from Prowlarr. Errors keep the previous values — a stale answer beats a
// flapping one on the 30s dashboard poll. The paused list is re-filtered
// against the clock on every call so an expired backoff window never
// lingers for the remainder of the TTL.
func (c *IndexerStatusCache) Get(ctx context.Context, svc *appservices.Clients) (*int, []PausedIndexer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.count != nil && time.Since(c.fetchedAt) < indexerStatusTTL {
		return c.count, activePauses(c.paused, time.Now())
	}
	_, _, prowlarrKey := svc.Keys()
	if prowlarrKey == "" {
		return c.count, activePauses(c.paused, time.Now())
	}
	indexers, err := svc.ProwlarrClient().ListIndexers(ctx, "/api/v1")
	if err != nil {
		return c.count, activePauses(c.paused, time.Now())
	}
	statuses, err := svc.ProwlarrClient().ListIndexerStatuses(ctx, "/api/v1")
	if err != nil {
		return c.count, activePauses(c.paused, time.Now())
	}
	now := time.Now()
	n := len(indexers)
	c.count = &n
	c.paused = mergePausedIndexers(indexers, statuses, now)
	c.fetchedAt = now
	return c.count, c.paused
}

// Invalidate clears the cache so the next Get re-fetches.
func (c *IndexerStatusCache) Invalidate() {
	c.mu.Lock()
	c.fetchedAt = time.Time{}
	c.mu.Unlock()
}

// mergePausedIndexers joins Prowlarr's indexer list (names) with its
// indexerstatus records (failure backoff windows) into the PausedIndexer
// list: only records whose disabledTill is in the future count, since
// status records persist after an indexer recovers. Unparseable or absent
// timestamps are skipped — no false banner beats a wrong one. Results are
// sorted by name for stable output.
func mergePausedIndexers(indexers, statuses []map[string]any, now time.Time) []PausedIndexer {
	names := make(map[int]string, len(indexers))
	for _, idx := range indexers {
		if id, ok := idx["id"].(float64); ok {
			name, _ := idx["name"].(string)
			names[int(id)] = name
		}
	}
	out := []PausedIndexer{}
	for _, st := range statuses {
		id, ok := st["indexerId"].(float64)
		if !ok {
			continue
		}
		tillStr, _ := st["disabledTill"].(string)
		if tillStr == "" {
			continue
		}
		till, err := time.Parse(time.RFC3339, tillStr)
		if err != nil || !till.After(now) {
			continue
		}
		name := names[int(id)]
		if name == "" {
			name = fmt.Sprintf("indexer %d", int(id))
		}
		out = append(out, PausedIndexer{ID: int(id), Name: name, DisabledTill: till.UTC()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// activePauses filters paused down to entries whose window is still open at
// now. Returns a fresh slice; the cached one is never mutated.
func activePauses(paused []PausedIndexer, now time.Time) []PausedIndexer {
	out := make([]PausedIndexer, 0, len(paused))
	for _, p := range paused {
		if p.DisabledTill.After(now) {
			out = append(out, p)
		}
	}
	return out
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
	idxPaused := []PausedIndexer{}
	if prowlarrKey != "" {
		idxCount, idxPaused = a.IdxCache.Get(r.Context(), a.Svc)
	}

	svcHealth, _ := a.StatusTTL.Get(func() (map[string]string, error) {
		return a.Svc.CheckHealth(), nil
	})
	status := map[string]any{
		"status":          "ok",
		"services":        svcHealth,
		"wired":           a.Svc.IsWired(),
		"indexers":        idxCount,
		"indexers_paused": idxPaused,
		"vpn_configured":  a.VPNConfigured,
		"warnings":        a.LibHandler.CheckLibraryAccess(),
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
