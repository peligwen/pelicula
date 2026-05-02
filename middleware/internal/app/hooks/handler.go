// Package hooks implements inbound webhook receipt and outbound proxying to
// Procula and the Apprise notification container.
//
// All state is on Handler — no package-level globals.
package hooks

import (
	"database/sql"
	"net/http"

	"pelicula-api/internal/peligrosa"

	arrclient "pelicula-api/internal/clients/arr"
	proculaclient "pelicula-api/internal/clients/procula"
	qbtclient "pelicula-api/internal/clients/qbt"
)

// Handler holds all dependencies for the hooks subsystem.
type Handler struct {
	// Procula typed client — used for CreateJob, GetStatus, ListJobs,
	// GetNotifications, and raw proxying.
	Procula *proculaclient.Client

	// HTTPClient is used for raw reverse-proxy calls (storage, updates, etc.).
	// If nil, http.DefaultClient is used.
	HTTPClient *http.Client

	// ProculaURL is the base URL for raw proxy calls (e.g. /api/procula/storage).
	// Set from PROCULA_URL env var by the caller.
	ProculaURL string

	// ProculaAPIKey is the API key for authenticating mutating requests to Procula.
	// Set from PROCULA_API_KEY env var by the caller.
	ProculaAPIKey string

	// SonarrURL and RadarrURL are used when fetching *arr history for the
	// notifications endpoint.
	SonarrURL string
	RadarrURL string

	// GetKeys returns the current Sonarr, Radarr, and Prowlarr API keys.
	// Called on each request so that key reloads are picked up without restart.
	GetKeys func() (sonarr, radarr, prowlarr string)

	// SonarrClient and RadarrClient are the typed arr clients used to fetch
	// history for the notifications endpoint.
	SonarrClient *arrclient.Client
	RadarrClient *arrclient.Client

	// CatalogDB is the catalog SQLite handle used for UpsertFromHook.
	CatalogDB *sql.DB

	// RequestStore is used to mark content as available after import.
	RequestStore *peligrosa.RequestStore

	// Qbt is the qBittorrent client used for SEEDING_REMOVE_ON_COMPLETE.
	Qbt *qbtclient.Client

	// TriggerJellyfinRefresh is called by handleJellyfinRefresh (POST from Procula).
	// Keeping this as a callback avoids a direct import of jellyfin_core.
	TriggerJellyfinRefresh func() error

	// Notify is called after a request is marked available (Apprise notifications).
	// Signature matches peligrosa.RequestStore.MarkAvailable's notify param.
	Notify func(title, body string) error

	// WebhookSecret is the shared secret verified against X-Webhook-Secret on
	// inbound *arr import webhooks. Empty means the check is skipped.
	WebhookSecret string

	// SeedingRemoveOnComplete is read once at startup from
	// os.Getenv("SEEDING_REMOVE_ON_COMPLETE"); per-request paths read this
	// field instead of calling os.Getenv on the hot webhook path.
	SeedingRemoveOnComplete bool
}

// httpClient returns the configured HTTP client or http.DefaultClient.
func (h *Handler) httpClient() *http.Client {
	if h.HTTPClient != nil {
		return h.HTTPClient
	}
	return http.DefaultClient
}
