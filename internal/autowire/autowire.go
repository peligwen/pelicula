// Package autowire performs the idempotent startup wiring of the stack:
// Jellyfin (startup wizard + libraries), Sonarr/Radarr (root folders,
// qBittorrent download client, import webhook) and Prowlarr (Sonarr/Radarr
// applications). Every step is "list, then add or update when fields differ",
// so running it against an already-wired stack changes nothing.
//
// The package depends only on narrow interfaces (see ArrClient and
// JellyfinClient); the concrete clients live in internal/clients/* and satisfy
// them at integration time.
package autowire

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"pelicula/internal/config"
)

// ArrClient is the slice of the Sonarr/Radarr/Prowlarr client autowire uses.
// Payloads are JSON-marshalled by the client; lists come back as decoded JSON.
type ArrClient interface {
	Ping(ctx context.Context) error
	SetAPIKey(key string)

	ListDownloadClients(ctx context.Context) ([]map[string]any, error)
	AddDownloadClient(ctx context.Context, cfg any) error
	UpdateDownloadClient(ctx context.Context, id int, payload any) error

	ListRootFolders(ctx context.Context) ([]map[string]any, error)
	AddRootFolder(ctx context.Context, payload any) error

	ListNotifications(ctx context.Context) ([]map[string]any, error)
	AddNotification(ctx context.Context, payload any) error
	UpdateNotification(ctx context.Context, id int, payload any) error

	// Prowlarr only.
	ListApplications(ctx context.Context) ([]map[string]any, error)
	AddApplication(ctx context.Context, payload any) error
	UpdateApplication(ctx context.Context, id int, payload any) error
}

// Pinger is the qBittorrent client as far as autowire is concerned.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Library is a Jellyfin virtual folder.
type Library struct {
	Name           string
	CollectionType string
	Locations      []string
}

// JellyfinClient is the slice of the Jellyfin client autowire uses.
type JellyfinClient interface {
	Ping(ctx context.Context) error
	StartupWizardCompleted(ctx context.Context) (bool, error)
	CompleteStartupWizard(ctx context.Context, adminUser, adminPass string) error
	ListLibraries(ctx context.Context, token string) ([]Library, error)
	AddLibrary(ctx context.Context, token, name, collectionType, path string) error
}

// TokenSource yields a Jellyfin admin access token (jellyfin.Admin).
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// Deps is everything Run needs. Prowlarr and QBT are nil when the VPN profile
// is off; a typed-nil pointer stored in the interface is treated as nil too.
type Deps struct {
	Cfg      config.Config
	Sonarr   ArrClient
	Radarr   ArrClient
	Prowlarr ArrClient
	QBT      Pinger
	Jellyfin JellyfinClient
	JFAdmin  TokenSource

	// SonarrAPIKey and RadarrAPIKey (from WaitForAPIKeys) go into the Prowlarr
	// application payloads.
	SonarrAPIKey string
	RadarrAPIKey string

	Log *slog.Logger
}

// pollInterval is how often we poll for services and API keys. Tests shorten it.
var pollInterval = 3 * time.Second

const (
	qbtHost       = "gluetun"
	qbtPort       = 8080
	webhookName   = "Pelicula"
	webhookHeader = "X-Webhook-Secret"
	webhookPath   = "/api/hooks/import"
	webhookPOST   = 1 // *arr's WebhookMethod enum: 1 = POST
)

// WaitForAPIKeys polls cfg.ArrAPIKey every 3 seconds until the key of every
// non-nil client exists (the *arr apps write config.xml on first boot), hands
// each key to its client via SetAPIKey, and returns the Sonarr and Radarr keys.
// ctx bounds the wait. On error no client has been touched.
func WaitForAPIKeys(ctx context.Context, cfg config.Config, sonarr, radarr, prowlarr ArrClient) (sonarrKey, radarrKey string, err error) {
	type target struct {
		service string
		client  ArrClient
		key     string
	}
	var targets []*target
	for _, t := range []target{{service: "sonarr", client: sonarr}, {service: "radarr", client: radarr}, {service: "prowlarr", client: prowlarr}} {
		if !isNil(t.client) {
			t := t
			targets = append(targets, &t)
		}
	}

	for {
		var missing []string
		for _, t := range targets {
			if t.key != "" {
				continue
			}
			if key, kerr := cfg.ArrAPIKey(t.service); kerr == nil && key != "" {
				t.key = key
			} else {
				missing = append(missing, t.service)
			}
		}
		if len(missing) == 0 {
			break
		}
		select {
		case <-ctx.Done():
			return "", "", fmt.Errorf("waiting for API keys of %s: %w", strings.Join(missing, ", "), ctx.Err())
		case <-time.After(pollInterval):
		}
	}

	for _, t := range targets {
		t.client.SetAPIKey(t.key)
		switch t.service {
		case "sonarr":
			sonarrKey = t.key
		case "radarr":
			radarrKey = t.key
		}
	}
	return sonarrKey, radarrKey, nil
}

// Run waits for the services to answer, then wires Jellyfin, Sonarr, Radarr
// and (when set) Prowlarr in that order. A failing step is logged and does not
// stop the others; Run returns the first error once every step was attempted.
// Failure to reach the services at all (ctx expiry) returns immediately.
func Run(ctx context.Context, d Deps) error {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "autowire")

	if isNil(d.Prowlarr) {
		d.Prowlarr = nil
	}
	if isNil(d.QBT) {
		d.QBT = nil
	}
	for _, req := range []struct {
		name string
		v    any
	}{{"Sonarr", d.Sonarr}, {"Radarr", d.Radarr}, {"Jellyfin", d.Jellyfin}, {"JFAdmin", d.JFAdmin}} {
		if isNil(req.v) {
			return fmt.Errorf("autowire: Deps.%s is required", req.name)
		}
	}

	log.Info("waiting for services")
	if err := waitForServices(ctx, log, d); err != nil {
		return fmt.Errorf("services not ready: %w", err)
	}
	log.Info("services ready, wiring")

	var first firstErr
	step := func(name string, fn func() error) {
		if err := fn(); err != nil {
			log.Error("wiring step failed", "step", name, "error", err)
			first.note(fmt.Errorf("%s: %w", name, err))
		}
	}

	step("jellyfin", func() error { return wireJellyfin(ctx, log, d) })

	for _, app := range []struct {
		name, root string
		client     ArrClient
		category   string
		catFields  []string
	}{
		{"sonarr", d.Cfg.TVPath, d.Sonarr, "tv-sonarr", []string{"tvCategory", "category"}},
		{"radarr", d.Cfg.MoviesPath, d.Radarr, "radarr", []string{"movieCategory", "category"}},
	} {
		step(app.name+" root folder", func() error { return wireRootFolder(ctx, log, app.name, app.client, app.root) })
		if d.QBT != nil {
			step(app.name+" download client", func() error {
				return wireDownloadClient(ctx, log, app.name, app.client, app.category, app.catFields)
			})
		}
		step(app.name+" webhook", func() error { return wireImportWebhook(ctx, log, app.name, app.client, d.Cfg) })
	}

	if d.Prowlarr != nil {
		step("prowlarr sonarr app", func() error {
			return wireProwlarrApp(ctx, log, d.Prowlarr, "Sonarr", d.Cfg.ProwlarrURL, d.Cfg.SonarrURL, d.SonarrAPIKey)
		})
		step("prowlarr radarr app", func() error {
			return wireProwlarrApp(ctx, log, d.Prowlarr, "Radarr", d.Cfg.ProwlarrURL, d.Cfg.RadarrURL, d.RadarrAPIKey)
		})
	}

	if first.err == nil {
		log.Info("all services wired")
	} else {
		log.Warn("some wiring failed, see errors above")
	}
	return first.err
}

// waitForServices pings every configured service until all answer, polling
// every pollInterval. It is bounded only by ctx.
func waitForServices(ctx context.Context, log *slog.Logger, d Deps) error {
	type svc struct {
		name string
		ping func(context.Context) error
	}
	pending := []svc{
		{"sonarr", d.Sonarr.Ping},
		{"radarr", d.Radarr.Ping},
		{"jellyfin", d.Jellyfin.Ping},
	}
	if d.Prowlarr != nil {
		pending = append(pending, svc{"prowlarr", d.Prowlarr.Ping})
	}
	if d.QBT != nil {
		pending = append(pending, svc{"qbittorrent", d.QBT.Ping})
	}

	for {
		var waiting []svc
		var names []string
		for _, s := range pending {
			if err := s.ping(ctx); err != nil {
				waiting = append(waiting, s)
				names = append(names, s.name)
				log.Debug("service not ready", "service", s.name, "error", err)
			} else {
				log.Info("service ready", "service", s.name)
			}
		}
		if len(waiting) == 0 {
			return nil
		}
		pending = waiting
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s: %w", strings.Join(names, ", "), ctx.Err())
		case <-time.After(pollInterval):
		}
	}
}

// firstErr remembers the first non-nil error it is shown.
type firstErr struct{ err error }

func (f *firstErr) note(err error) {
	if err != nil && f.err == nil {
		f.err = err
	}
}

// isNil reports whether v is nil or an interface holding a nil pointer, which
// is what a nil *arr.Client becomes once assigned to an ArrClient.
func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
		return rv.IsNil()
	}
	return false
}

var errNoID = errors.New("existing resource has no id")
