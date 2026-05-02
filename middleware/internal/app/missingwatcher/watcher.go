// Package missingwatcher periodically scans Sonarr/Radarr for monitored
// content that has no files and triggers automatic searches.
package missingwatcher

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"pelicula-api/internal/app/catalog"
	services "pelicula-api/internal/app/services"
	"pelicula-api/internal/app/util"
)

// searchCooldown tracks per-item search history to prevent hammering the *arr
// APIs with repeated searches for content that has no available releases.
// The map lives in memory — losing it on restart triggers one extra search per
// item, which is fine.
type searchCooldown struct {
	mu      sync.Mutex
	entries map[int]cooldownEntry
}

type cooldownEntry struct {
	lastSearched time.Time
	attempts     int
}

// cooldownDurations defines backoff tiers by attempt number (0-indexed).
// Attempt 0 → immediate, 1 → 30min, 2 → 2hr, 3 → 12hr, 4+ → 24hr.
var cooldownDurations = []time.Duration{
	0,
	30 * time.Minute,
	2 * time.Hour,
	12 * time.Hour,
	24 * time.Hour,
}

func newSearchCooldown() *searchCooldown {
	return &searchCooldown{entries: make(map[int]cooldownEntry)}
}

// shouldSearch returns true if id has not been searched recently.
// If it returns true, the entry is updated (attempt count incremented,
// lastSearched set to now) so the next call respects the cooldown.
func (c *searchCooldown) shouldSearch(id int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	e := c.entries[id]
	tier := e.attempts
	if tier >= len(cooldownDurations) {
		tier = len(cooldownDurations) - 1
	}
	if cooldownDurations[tier] > 0 && time.Since(e.lastSearched) < cooldownDurations[tier] {
		return false
	}
	e.attempts++
	e.lastSearched = time.Now()
	c.entries[id] = e
	return true
}

// clear resets the cooldown for id (call when the item enters the queue or
// gets a file).
func (c *searchCooldown) clear(id int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, id)
}

// Watcher periodically scans Sonarr/Radarr for monitored content without files.
type Watcher struct {
	Services     *services.Clients
	SonarrURL    string
	RadarrURL    string
	CatalogCache *catalog.CatalogCache // optional; nil falls back to direct ArrGet
	movie        *searchCooldown
	episode      *searchCooldown
}

// New creates a Watcher wired to the given service clients and URLs.
func New(svc *services.Clients, sonarrURL, radarrURL string) *Watcher {
	return &Watcher{
		Services:  svc,
		SonarrURL: sonarrURL,
		RadarrURL: radarrURL,
		movie:     newSearchCooldown(),
		episode:   newSearchCooldown(),
	}
}

// Run blocks until ctx is cancelled, calling scan every interval.
// Waits for svc.IsWired() before starting.
// Consecutive arr fetch errors engage a skip-backoff of up to 5 iterations
// to avoid hammering unavailable services.
func (w *Watcher) Run(ctx context.Context, interval time.Duration) {
	for {
		if w.Services.IsWired() {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}

	slog.Info("started", "component", "watcher", "interval", interval.String())

	skip := util.NewSkipCounter(5)

	ticker := time.NewTicker(util.JitteredDuration(interval, 0.1))
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Debug("watcher: context cancelled, stopping", "component", "watcher")
			return
		case <-ticker.C:
			if skip.ShouldSkip() {
				slog.Debug("watcher: skipping iteration (backoff)", "component", "watcher")
				continue
			}
			radarrErr := w.searchMissingMovies(ctx)
			sonarrErr := w.searchMissingSeries(ctx)
			if radarrErr || sonarrErr {
				skip.RecordFailure()
			} else {
				skip.RecordSuccess()
			}
			// Reset ticker with new jittered duration for next interval.
			ticker.Reset(util.JitteredDuration(interval, 0.1))
		}
	}
}

// searchMissingMovies fetches Radarr's movie list and triggers searches for
// monitored movies without files. Returns true if an arr fetch error occurred.
func (w *Watcher) searchMissingMovies(ctx context.Context) bool {
	_, radarrKey, _ := w.Services.Keys()
	if radarrKey == "" {
		return false
	}

	var data []byte
	var err error
	if w.CatalogCache != nil {
		data, err = w.CatalogCache.GetMovies(ctx)
	} else {
		data, err = w.Services.ArrGet(w.RadarrURL, radarrKey, "/api/v3/movie")
	}
	if err != nil {
		slog.Error("failed to fetch movies", "component", "watcher", "service", "radarr", "error", err)
		return true
	}

	var movies []map[string]any
	if json.Unmarshal(data, &movies) != nil {
		return false
	}

	// Get queue to avoid re-searching items already downloading
	queuedIDs := w.radarrQueuedMovieIDs()

	var missing []int
	for _, m := range movies {
		monitored, _ := m["monitored"].(bool)
		hasFile, _ := m["hasFile"].(bool)
		available, _ := m["isAvailable"].(bool)
		id := int(floatVal(m, "id"))

		if !monitored {
			continue
		}
		if hasFile {
			w.movie.clear(id)
			continue
		}
		if queuedIDs[id] {
			w.movie.clear(id)
			continue
		}
		if !available {
			continue
		}
		if w.movie.shouldSearch(id) {
			missing = append(missing, id)
		}
	}

	if len(missing) == 0 {
		return false
	}

	slog.Info("triggering search for missing movies", "component", "watcher", "service", "radarr", "count", len(missing))
	_, err = w.Services.ArrPost(w.RadarrURL, radarrKey, "/api/v3/command", map[string]any{
		"name":     "MoviesSearch",
		"movieIds": missing,
	})
	if err != nil {
		slog.Error("search command failed", "component", "watcher", "service", "radarr", "error", err)
		return true
	}
	return false
}

func (w *Watcher) radarrQueuedMovieIDs() map[int]bool {
	_, radarrKey, _ := w.Services.Keys()
	ids := make(map[int]bool)
	records, err := w.Services.ArrGetAllQueueRecords(w.RadarrURL, radarrKey, "/api/v3", "")
	if err != nil {
		return ids
	}
	for _, r := range records {
		if id, ok := r["movieId"].(float64); ok {
			ids[int(id)] = true
		}
	}
	return ids
}

// searchMissingSeries fetches Sonarr's missing episodes and triggers searches.
// Returns true if an arr fetch error occurred.
// Note: the /wanted/missing endpoint already excludes episodes with files, so
// a hasFile check here is not needed — absence from the response is the implicit reset.
func (w *Watcher) searchMissingSeries(ctx context.Context) bool {
	sonarrKey, _, _ := w.Services.Keys()
	if sonarrKey == "" {
		return false
	}

	data, err := w.Services.ArrGet(w.SonarrURL, sonarrKey, "/api/v3/wanted/missing?pageSize=100&sortKey=airDateUtc&sortDirection=descending")
	if err != nil {
		slog.Error("failed to fetch missing episodes", "component", "watcher", "service", "sonarr", "error", err)
		return true
	}

	var wanted struct {
		Records []map[string]any `json:"records"`
	}
	if json.Unmarshal(data, &wanted) != nil {
		return false
	}

	// Get queue to avoid re-searching items already downloading
	queuedEpisodes := w.sonarrQueuedEpisodeIDs()

	var missing []int
	for _, ep := range wanted.Records {
		monitored, _ := ep["monitored"].(bool)
		id := int(floatVal(ep, "id"))
		if !monitored {
			continue
		}
		if queuedEpisodes[id] {
			w.episode.clear(id)
			continue
		}
		if w.episode.shouldSearch(id) {
			missing = append(missing, id)
		}
	}

	if len(missing) == 0 {
		return false
	}

	slog.Info("triggering search for missing episodes", "component", "watcher", "service", "sonarr", "count", len(missing))
	_, err = w.Services.ArrPost(w.SonarrURL, sonarrKey, "/api/v3/command", map[string]any{
		"name":       "EpisodeSearch",
		"episodeIds": missing,
	})
	if err != nil {
		slog.Error("search command failed", "component", "watcher", "service", "sonarr", "error", err)
		return true
	}
	return false
}

func (w *Watcher) sonarrQueuedEpisodeIDs() map[int]bool {
	sonarrKey, _, _ := w.Services.Keys()
	ids := make(map[int]bool)
	records, err := w.Services.ArrGetAllQueueRecords(w.SonarrURL, sonarrKey, "/api/v3", "")
	if err != nil {
		return ids
	}
	for _, r := range records {
		if id, ok := r["episodeId"].(float64); ok {
			ids[int(id)] = true
		}
	}
	return ids
}

// floatVal extracts a float64 from a map[string]any by key.
// Returns 0 if the key is absent or the value is not a float64.
func floatVal(m map[string]any, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}
