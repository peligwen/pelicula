package main

import (
	"encoding/json"
	"log"
	"time"
)

// StartMissingWatcher runs in the background and periodically searches for
// monitored content that has no files and no active queue entry. This ensures
// that movies/series added directly through the Radarr/Sonarr UIs (which
// don't auto-search by default) get picked up automatically.
func StartMissingWatcher(s *ServiceClients, interval time.Duration) {
	// Wait for autowire to finish before starting
	for !s.IsWired() {
		time.Sleep(5 * time.Second)
	}

	log.Printf("[watcher] started — checking for missing content every %s", interval)

	for {
		time.Sleep(interval)
		searchMissingMovies(s)
		searchMissingSeries(s)
	}
}

func searchMissingMovies(s *ServiceClients) {
	if s.RadarrKey == "" {
		return
	}

	data, err := s.ArrGet(radarrURL, s.RadarrKey, "/api/v3/movie")
	if err != nil {
		log.Printf("[watcher] radarr: failed to fetch movies: %v", err)
		return
	}

	var movies []map[string]any
	if json.Unmarshal(data, &movies) != nil {
		return
	}

	// Get queue to avoid re-searching items already downloading
	queuedIDs := radarrQueuedMovieIDs(s)

	var missing []int
	for _, m := range movies {
		monitored, _ := m["monitored"].(bool)
		hasFile, _ := m["hasFile"].(bool)
		available, _ := m["isAvailable"].(bool)
		id := int(floatVal(m, "id"))

		if monitored && !hasFile && available && !queuedIDs[id] {
			missing = append(missing, id)
		}
	}

	if len(missing) == 0 {
		return
	}

	log.Printf("[watcher] radarr: found %d missing movie(s), triggering search", len(missing))
	_, err = s.ArrPost(radarrURL, s.RadarrKey, "/api/v3/command", map[string]any{
		"name":     "MoviesSearch",
		"movieIds": missing,
	})
	if err != nil {
		log.Printf("[watcher] radarr: search command failed: %v", err)
	}
}

func radarrQueuedMovieIDs(s *ServiceClients) map[int]bool {
	ids := make(map[int]bool)
	data, err := s.ArrGet(radarrURL, s.RadarrKey, "/api/v3/queue?pageSize=100")
	if err != nil {
		return ids
	}
	var queue struct {
		Records []map[string]any `json:"records"`
	}
	if json.Unmarshal(data, &queue) == nil {
		for _, r := range queue.Records {
			if id, ok := r["movieId"].(float64); ok {
				ids[int(id)] = true
			}
		}
	}
	return ids
}

func searchMissingSeries(s *ServiceClients) {
	if s.SonarrKey == "" {
		return
	}

	data, err := s.ArrGet(sonarrURL, s.SonarrKey, "/api/v3/wanted/missing?pageSize=100&sortKey=airDateUtc&sortDirection=descending")
	if err != nil {
		log.Printf("[watcher] sonarr: failed to fetch missing episodes: %v", err)
		return
	}

	var wanted struct {
		Records []map[string]any `json:"records"`
	}
	if json.Unmarshal(data, &wanted) != nil {
		return
	}

	// Get queue to avoid re-searching items already downloading
	queuedEpisodes := sonarrQueuedEpisodeIDs(s)

	var missing []int
	for _, ep := range wanted.Records {
		monitored, _ := ep["monitored"].(bool)
		id := int(floatVal(ep, "id"))
		if monitored && !queuedEpisodes[id] {
			missing = append(missing, id)
		}
	}

	if len(missing) == 0 {
		return
	}

	log.Printf("[watcher] sonarr: found %d missing episode(s), triggering search", len(missing))
	_, err = s.ArrPost(sonarrURL, s.SonarrKey, "/api/v3/command", map[string]any{
		"name":       "EpisodeSearch",
		"episodeIds": missing,
	})
	if err != nil {
		log.Printf("[watcher] sonarr: search command failed: %v", err)
	}
}

func sonarrQueuedEpisodeIDs(s *ServiceClients) map[int]bool {
	ids := make(map[int]bool)
	data, err := s.ArrGet(sonarrURL, s.SonarrKey, "/api/v3/queue?pageSize=100")
	if err != nil {
		return ids
	}
	var queue struct {
		Records []map[string]any `json:"records"`
	}
	if json.Unmarshal(data, &queue) == nil {
		for _, r := range queue.Records {
			if id, ok := r["episodeId"].(float64); ok {
				ids[int(id)] = true
			}
		}
	}
	return ids
}
