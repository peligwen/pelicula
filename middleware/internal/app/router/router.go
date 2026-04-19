// Package router wires all HTTP routes for the pelicula-api service.
// Business logic lives in the handler packages; this file is wiring only.
package router

import (
	"net/http"

	"pelicula-api/httputil"
	"pelicula-api/internal/app/actions"
	"pelicula-api/internal/app/adminops"
	"pelicula-api/internal/app/backup"
	"pelicula-api/internal/app/catalog"
	"pelicula-api/internal/app/downloads"
	"pelicula-api/internal/app/health"
	"pelicula-api/internal/app/hooks"
	jfapp "pelicula-api/internal/app/jellyfin"
	"pelicula-api/internal/app/library"
	"pelicula-api/internal/app/search"
	"pelicula-api/internal/app/settings"
	"pelicula-api/internal/app/sse"
	"pelicula-api/internal/app/sysinfo"
	"pelicula-api/internal/peligrosa"
)

// Config holds everything Register needs to wire routes. Handlers are passed
// as concrete types from their packages; auth is the shared Auth instance used
// for Guard/GuardAdmin/GuardManager and the adminops session lookup closure.
type Config struct {
	Auth          *peligrosa.Auth
	Deps          *peligrosa.Deps
	Health        *health.Handler
	SSE           *sse.Hub
	Sysinfo       *sysinfo.Handler
	Downloads     *downloads.Handler
	Hooks         *hooks.Handler
	Backup        *backup.Handler
	JF            *jfapp.Handler
	Library       *library.Handler
	Catalog       *catalog.Handler
	Search        *search.Handler
	Settings      *settings.Handler
	Actions       *actions.Handler
	Admin         *adminops.Handler
	StatusHandler http.HandlerFunc
	JobsHandler   http.HandlerFunc
}

// Register wires all /api/pelicula/* routes onto mux.
func Register(mux *http.ServeMux, cfg Config) {
	auth := cfg.Auth

	// Health check — no auth, called by bash check-vpn
	mux.Handle("/api/pelicula/health", cfg.Health)

	// Peligrosa routes: auth, invites, requests, open registration
	peligrosa.RegisterRoutes(mux, cfg.Deps)

	// Webhook receiver — no session auth needed (*arr services call this)
	mux.HandleFunc("/api/pelicula/hooks/import", cfg.Hooks.HandleImportHook)
	// Jellyfin refresh — called by Procula internally
	mux.HandleFunc("/api/pelicula/jellyfin/refresh", cfg.Hooks.HandleJellyfinRefresh)

	// viewer+: SSE stream
	mux.Handle("/api/pelicula/sse", auth.Guard(http.HandlerFunc(cfg.SSE.HandleSSE)))

	// viewer+: read-only dashboard data
	mux.Handle("/api/pelicula/host", auth.Guard(http.HandlerFunc(cfg.Sysinfo.ServeHost)))
	mux.Handle("/api/pelicula/status", auth.Guard(cfg.StatusHandler))
	mux.Handle("/api/pelicula/downloads", auth.Guard(http.HandlerFunc(cfg.Downloads.HandleDownloads)))
	mux.Handle("/api/pelicula/downloads/stats", auth.Guard(http.HandlerFunc(cfg.Downloads.HandleDownloadStats)))
	mux.Handle("/api/pelicula/processing", auth.Guard(http.HandlerFunc(cfg.Hooks.HandleProcessingProxy)))
	mux.Handle("/api/pelicula/notifications", auth.Guard(http.HandlerFunc(cfg.Hooks.HandleNotificationsProxy)))
	mux.Handle("/api/pelicula/storage", auth.Guard(http.HandlerFunc(cfg.Hooks.HandleStorageProxy)))
	mux.Handle("/api/pelicula/procula-settings", auth.GuardAdmin(http.HandlerFunc(cfg.Hooks.HandleProculaSettingsProxy)))
	mux.Handle("/api/pelicula/storage/scan", auth.GuardAdmin(http.HandlerFunc(cfg.Hooks.HandleStorageScanProxy)))
	mux.Handle("/api/pelicula/updates", auth.Guard(http.HandlerFunc(cfg.Hooks.HandleUpdatesProxy)))

	// admin only: *arr metadata for settings dropdowns
	mux.Handle("/api/pelicula/arr-meta", auth.GuardAdmin(http.HandlerFunc(cfg.Search.HandleArrMeta)))

	// manager+: search and add content, pause/resume downloads
	mux.Handle("/api/pelicula/search", auth.GuardManager(http.HandlerFunc(cfg.Search.HandleSearch)))
	mux.Handle("/api/pelicula/search/add", auth.GuardManager(http.HandlerFunc(cfg.Search.HandleSearchAdd)))
	mux.Handle("/api/pelicula/downloads/pause", auth.GuardManager(http.HandlerFunc(cfg.Downloads.HandleDownloadPause)))

	// admin only: destructive actions
	mux.Handle("/api/pelicula/downloads/cancel", auth.GuardAdmin(http.HandlerFunc(cfg.Downloads.HandleDownloadCancel)))

	// admin only: settings
	mux.Handle("/api/pelicula/settings", auth.GuardAdmin(httputil.RequireLocalOriginStrict(http.HandlerFunc(cfg.Settings.HandleSettings))))
	mux.Handle("/api/pelicula/settings/reset", auth.GuardAdmin(httputil.RequireLocalOriginStrict(http.HandlerFunc(cfg.Settings.HandleReset))))

	// admin only: backup export / import
	mux.Handle("/api/pelicula/export", auth.GuardAdmin(http.HandlerFunc(cfg.Backup.HandleExport)))
	mux.Handle("/api/pelicula/import-backup", auth.GuardAdmin(http.HandlerFunc(cfg.Backup.HandleImportBackup)))

	// admin only: Jellyfin user management
	mux.Handle("/api/pelicula/users", auth.GuardAdmin(httputil.RequireLocalOriginSoft(http.HandlerFunc(cfg.JF.HandleUsers))))
	mux.Handle("/api/pelicula/users/", auth.GuardAdmin(httputil.RequireLocalOriginSoft(http.HandlerFunc(cfg.JF.HandleUsersWithID))))

	// admin only: role management
	mux.Handle("/api/pelicula/operators", auth.GuardAdmin(httputil.RequireLocalOriginSoft(http.HandlerFunc(auth.HandleOperators))))
	mux.Handle("/api/pelicula/operators/", auth.GuardAdmin(httputil.RequireLocalOriginSoft(http.HandlerFunc(auth.HandleOperatorsWithID))))

	// viewer+: active Jellyfin sessions
	mux.Handle("/api/pelicula/sessions", auth.Guard(http.HandlerFunc(cfg.JF.HandleSessions)))

	// viewer+: library metadata
	mux.Handle("GET /api/pelicula/libraries", auth.Guard(http.HandlerFunc(cfg.Library.HandleListLibraries)))
	// admin only: library CRUD
	mux.Handle("POST /api/pelicula/libraries", auth.GuardAdmin(httputil.RequireLocalOriginStrict(http.HandlerFunc(cfg.Library.HandleAddLibrary))))
	mux.Handle("PUT /api/pelicula/libraries/{slug}", auth.GuardAdmin(httputil.RequireLocalOriginStrict(http.HandlerFunc(cfg.Library.HandleUpdateLibrary))))
	mux.Handle("DELETE /api/pelicula/libraries/{slug}", auth.GuardAdmin(httputil.RequireLocalOriginStrict(http.HandlerFunc(cfg.Library.HandleDeleteLibrary))))

	// admin only: library import scan + apply + browse
	mux.Handle("/api/pelicula/browse", auth.GuardAdmin(http.HandlerFunc(cfg.Library.HandleBrowse)))
	mux.Handle("/api/pelicula/library/scan", auth.GuardAdmin(http.HandlerFunc(cfg.Library.HandleLibraryScan)))
	mux.Handle("/api/pelicula/library/apply", auth.GuardAdmin(http.HandlerFunc(cfg.Library.HandleLibraryApply)))

	// admin only: transcoding
	mux.Handle("/api/pelicula/transcode/profiles", auth.GuardAdmin(http.HandlerFunc(cfg.Library.HandleTranscodeProfiles)))
	mux.Handle("/api/pelicula/transcode/profiles/{name}", auth.GuardAdmin(http.HandlerFunc(cfg.Library.HandleDeleteTranscodeProfile)))
	mux.Handle("/api/pelicula/library/retranscode", auth.GuardAdmin(http.HandlerFunc(cfg.Library.HandleLibraryRetranscode)))

	// admin only: subtitle re-acquisition
	mux.Handle("/api/pelicula/library/resub", auth.GuardAdmin(http.HandlerFunc(cfg.Library.HandleLibraryResub)))
	mux.Handle("/api/pelicula/procula/jobs/{id}/resub", auth.GuardAdmin(http.HandlerFunc(cfg.Library.HandleJobResub)))
	mux.Handle("/api/pelicula/procula/jobs/{id}/retry", auth.GuardAdmin(http.HandlerFunc(cfg.Library.HandleJobRetry)))

	// viewer+: catalog
	mux.Handle("/api/pelicula/catalog", auth.Guard(http.HandlerFunc(cfg.Catalog.HandleCatalogList)))
	mux.Handle("/api/pelicula/catalog/series/{id}", auth.Guard(http.HandlerFunc(cfg.Catalog.HandleCatalogSeriesDetail)))
	mux.Handle("/api/pelicula/catalog/series/{id}/season/{n}", auth.Guard(http.HandlerFunc(cfg.Catalog.HandleCatalogSeason)))
	mux.Handle("/api/pelicula/catalog/item/history", auth.Guard(http.HandlerFunc(cfg.Catalog.HandleCatalogItemHistory)))
	mux.Handle("/api/pelicula/catalog/flags", auth.Guard(http.HandlerFunc(cfg.Catalog.HandleCatalogFlags)))
	mux.Handle("/api/pelicula/catalog/detail", auth.Guard(http.HandlerFunc(cfg.Catalog.HandleCatalogDetail)))
	mux.Handle("/api/pelicula/catalog/items", auth.Guard(http.HandlerFunc(cfg.Catalog.HandleCatalogItems)))
	mux.Handle("/api/pelicula/catalog/items/{id}", auth.Guard(http.HandlerFunc(cfg.Catalog.HandleCatalogItemDetail)))
	mux.Handle("/api/pelicula/catalog/backfill", auth.GuardAdmin(http.HandlerFunc(cfg.Catalog.HandleCatalogBackfill)))
	mux.Handle("/api/pelicula/catalog/command", auth.GuardAdmin(http.HandlerFunc(cfg.Catalog.HandleCatalogCommand)))
	mux.Handle("/api/pelicula/catalog/replace", auth.GuardAdmin(http.HandlerFunc(cfg.Catalog.HandleCatalogReplace)))
	mux.Handle("/api/pelicula/catalog/blocklist/{id}", auth.GuardAdmin(http.HandlerFunc(cfg.Catalog.HandleCatalogUnblocklist)))
	mux.Handle("/api/pelicula/catalog/qualityprofiles", auth.Guard(http.HandlerFunc(cfg.Catalog.HandleCatalogQualityProfiles)))
	mux.Handle("/api/pelicula/jobs", auth.Guard(cfg.JobsHandler))

	// admin only: action bus
	mux.Handle("/api/pelicula/actions", auth.GuardAdmin(http.HandlerFunc(cfg.Actions.HandleCreate)))
	mux.Handle("/api/pelicula/actions/registry", auth.Guard(http.HandlerFunc(cfg.Actions.HandleRegistry)))

	// admin only: VPN speed test
	mux.Handle("/api/pelicula/speedtest", auth.GuardAdmin(http.HandlerFunc(cfg.Sysinfo.ServeSpeedtest)))

	// admin only: container control
	mux.Handle("/api/pelicula/admin/stack/restart", auth.GuardAdmin(http.HandlerFunc(cfg.Admin.HandleStackRestart)))
	mux.Handle("/api/pelicula/admin/vpn/restart", auth.GuardAdmin(http.HandlerFunc(cfg.Admin.HandleVPNRestart)))
	mux.Handle("/api/pelicula/admin/logs", auth.GuardAdmin(http.HandlerFunc(cfg.Admin.HandleServiceLogs)))
	mux.Handle("/api/pelicula/logs/aggregate", auth.GuardAdmin(http.HandlerFunc(cfg.Sysinfo.ServeLogs)))
}
