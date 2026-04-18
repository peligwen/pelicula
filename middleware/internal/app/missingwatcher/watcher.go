// Package missingwatcher periodically scans Sonarr/Radarr for monitored
// content that has no files and triggers automatic searches.
package missingwatcher

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	services "pelicula-api/internal/app/services"
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
	Services  *services.Clients
	SonarrURL string
	RadarrURL string
	movie     *searchCooldown
	episode   *searchCooldown
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

// Run blocks forever, calling scan every interval.
// Waits for svc.IsWired() before starting.
func (w *Watcher) Run(interval time.Duration) {
	for !w.Services.IsWired() {
		time.Sleep(5 * time.Second)
	}

	slog.Info("started", "component", "watcher", "interval", interval.String())

	for {
		time.Sleep(interval)
		w.searchMissingMovies()
		w.searchMissingSeries()
	}
}

func (w *Watcher) searchMissingMovies() {
	_, radarrKey, _ := w.Services.Keys()
	if radarrKey == "" {
		return
	}

	data, err := w.Services.ArrGet(w.RadarrURL, radarrKey, "/api/v3/movie")
	if err != nil {
		slog.Error("failed to fetch movies", "component", "watcher", "service", "radarr", "error", err)
		return
	}

	var movies []map[string]any
	if json.Unmarshal(data, &movies) != nil {
		return
	}

	// Get queue to avoid re-searching items already downloading
	queuedIDs := w.radarrQueuedMovieIDs()

	var missing []int
	for _, m := range movies {
		monitored, _ := m["monitored"].(bool)
		hasFile, _ := m["hasFile"].(bool)
		available, _ := m["isAvailable"].(bool)
		id := int(floatVal(m, "id"))

		if monitored && !hasFile && available && !queuedIDs[id] && w.movie.shouldSearch(id) {
			missing = append(missing, id)
		}
	}

	if len(missing) == 0 {
		return
	}

	slog.Info("triggering search for missing movies", "component", "watcher", "service", "radarr", "count", len(missing))
	_, err = w.Services.ArrPost(w.RadarrURL, radarrKey, "/api/v3/command", map[string]any{
		"name":     "MoviesSearch",
		"movieIds": missing,
	})
	if err != nil {
		slog.Error("search command failed", "component", "watcher", "service", "radarr", "error", err)
	}
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

func (w *Watcher) searchMissingSeries() {
	sonarrKey, _, _ := w.Services.Keys()
	if sonarrKey == "" {
		return
	}

	data, err := w.Services.ArrGet(w.SonarrURL, sonarrKey, "/api/v3/wanted/missing?pageSize=100&sortKey=airDateUtc&sortDirection=descending")
	if err != nil {
		slog.Error("failed to fetch missing episodes", "component", "watcher", "service", "sonarr", "error", err)
		return
	}

	var wanted struct {
		Records []map[string]any `json:"records"`
	}
	if json.Unmarshal(data, &wanted) != nil {
		return
	}

	// Get queue to avoid re-searching items already downloading
	queuedEpisodes := w.sonarrQueuedEpisodeIDs()

	var missing []int
	for _, ep := range wanted.Records {
		monitored, _ := ep["monitored"].(bool)
		id := int(floatVal(ep, "id"))
		if monitored && !queuedEpisodes[id] && w.episode.shouldSearch(id) {
			missing = append(missing, id)
		}
	}

	if len(missing) == 0 {
		return
	}

	slog.Info("triggering search for missing episodes", "component", "watcher", "service", "sonarr", "count", len(missing))
	_, err = w.Services.ArrPost(w.SonarrURL, sonarrKey, "/api/v3/command", map[string]any{
		"name":       "EpisodeSearch",
		"episodeIds": missing,
	})
	if err != nil {
		slog.Error("search command failed", "component", "watcher", "service", "sonarr", "error", err)
	}
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
