package library

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// ── Filename parsing ──────────────────────────────────────────────────────────

var (
	// yearRe matches a standalone 4-digit year (1900–2099).
	yearRe = regexp.MustCompile(`\b(19\d{2}|20[012]\d)\b`)
	// tvEpRe matches SxxExx / sxxexx episode patterns.
	tvEpRe = regexp.MustCompile(`(?i)\bS\d{1,2}E\d{1,2}\b`)
	// seasonRe captures the season number from a TV filename.
	seasonRe = regexp.MustCompile(`(?i)\bS(\d{1,2})E\d{1,2}\b`)
	// episodeRe captures the episode number from a TV filename.
	episodeRe = regexp.MustCompile(`(?i)\bS\d{1,2}E(\d{1,2})\b`)
)

// cleanFilename extracts a search-ready title, year, and TV flag from a
// media filename. Handles dot-delimited (`The.Dark.Knight.2008.mkv`),
// paren-year (`Alien (1979).mkv`), and TV episode (`Breaking.Bad.S01E01`) patterns.
func cleanFilename(filename string) (title string, year int, isTV bool) {
	// Drop extension.
	name := filename
	if ext := filepath.Ext(filename); ext != "" {
		name = name[:len(name)-len(ext)]
	}

	// Replace dot/underscore separators with spaces first so regexes
	// work on the cleaned string.
	name = strings.NewReplacer(".", " ", "_", " ").Replace(name)

	isTV = tvEpRe.MatchString(name)

	cutIdx := len(name)

	// Find year — cut title there.
	if loc := yearRe.FindStringIndex(name); loc != nil {
		digits := yearRe.FindString(name[loc[0]:])
		year, _ = strconv.Atoi(digits)
		if loc[0] < cutIdx {
			cutIdx = loc[0]
		}
	}

	// Find TV episode tag — cut title there too (whichever is earlier).
	if loc := tvEpRe.FindStringIndex(name); loc != nil {
		if loc[0] < cutIdx {
			cutIdx = loc[0]
		}
	}

	// Trim trailing separators and junk left after the cut.
	title = strings.TrimRight(name[:cutIdx], " -_([")
	title = strings.TrimSpace(strings.Join(strings.Fields(title), " "))
	return
}

// extractSeason returns the season number from a TV filename, or 0 if unknown.
func extractSeason(filename string) int {
	name := strings.NewReplacer(".", " ", "_", " ").Replace(filename)
	if m := seasonRe.FindStringSubmatch(name); m != nil {
		s, _ := strconv.Atoi(m[1])
		return s
	}
	return 0
}

// extractEpisode returns the episode number from a TV filename, or 0 if unknown.
func extractEpisode(filename string) int {
	name := strings.NewReplacer(".", " ", "_", " ").Replace(filename)
	if m := episodeRe.FindStringSubmatch(name); m != nil {
		e, _ := strconv.Atoi(m[1])
		return e
	}
	return 0
}

// ── Match helpers ─────────────────────────────────────────────────────────────

type cacheKeyT = string

func cachedLookup(
	cache map[cacheKeyT]*MediaMatch,
	mu *sync.Mutex,
	key string,
	lookup func() *MediaMatch,
) *MediaMatch {
	mu.Lock()
	if m, ok := cache[key]; ok {
		mu.Unlock()
		return m
	}
	mu.Unlock()

	m := lookup()

	mu.Lock()
	cache[key] = m
	mu.Unlock()
	return m
}

// confidenceRank maps confidence strings to numeric priority (higher = better).
var confidenceRank = map[string]int{"high": 3, "medium": 2, "low": 1}

// pickBestMatch iterates up to 5 Arr lookup results and returns the one that
// scores highest against (cleanTitle, year). Ties go to the first (most
// popular) result. Returns nil if nothing scores above "unmatched".
func pickBestMatch(results []map[string]any, cleanTitle string, year int, kind string) *MediaMatch {
	var best *MediaMatch
	bestRank := 0
	for i, r := range results {
		if i >= 5 {
			break
		}
		mt := strVal(r, "title")
		my := int(floatVal(r, "year"))
		confidence := scoreMatch(cleanTitle, year, mt, my)
		if confidenceRank[confidence] > bestRank {
			m := &MediaMatch{
				Type:       kind,
				Title:      mt,
				Year:       my,
				TmdbID:     int(floatVal(r, "tmdbId")),
				Confidence: confidence,
			}
			if kind == "series" {
				m.TvdbID = int(floatVal(r, "tvdbId"))
			}
			best = m
			bestRank = confidenceRank[confidence]
			if bestRank == 3 {
				break // can't do better than "high"
			}
		}
	}
	return best
}

func (h *Handler) lookupMovie(ctx context.Context, apiKey, encoded, cleanTitle string, year int) *MediaMatch {
	data, err := h.Svc.ArrGet(ctx, h.RadarrURL, apiKey, "/api/v3/movie/lookup?term="+encoded)
	if err != nil {
		return nil
	}
	var results []map[string]any
	if err := json.Unmarshal(data, &results); err != nil || len(results) == 0 {
		return nil
	}
	return pickBestMatch(results, cleanTitle, year, "movie")
}

func (h *Handler) lookupSeries(ctx context.Context, apiKey, encoded, cleanTitle string, year int) *MediaMatch {
	data, err := h.Svc.ArrGet(ctx, h.SonarrURL, apiKey, "/api/v3/series/lookup?term="+encoded)
	if err != nil {
		return nil
	}
	var results []map[string]any
	if err := json.Unmarshal(data, &results); err != nil || len(results) == 0 {
		return nil
	}
	return pickBestMatch(results, cleanTitle, year, "series")
}

// matchFile resolves a single ScanFile to a MatchItem.
func (h *Handler) matchFile(
	ctx context.Context,
	f ScanFile,
	radarrKey, sonarrKey string,
	existingMovies, existingSeries map[int]bool,
	cache map[cacheKeyT]*MediaMatch,
	cacheMu *sync.Mutex,
) MatchItem {
	item := MatchItem{File: f.Path, Size: f.Size, Status: "unmatched"}

	filename := filepath.Base(f.Path)
	title, year, isTV := cleanFilename(filename)
	if title == "" {
		return item
	}

	encoded := url.QueryEscape(title)
	movieRoot := h.FirstLibraryPath("radarr", "/media/movies")
	tvRoot := h.FirstLibraryPath("sonarr", "/media/tv")

	if isTV {
		m := cachedLookup(cache, cacheMu, fmt.Sprintf("series:%s:%d", title, year), func() *MediaMatch {
			return h.lookupSeries(ctx, sonarrKey, encoded, title, year)
		})
		if m != nil {
			season := extractSeason(filename)
			episode := extractEpisode(filename)
			mc := *m // copy — do not mutate the shared cache entry
			mc.Season = season
			mc.Episode = episode
			item.Match = &mc
			item.SuggestedPath = suggestedTVPath(tvRoot, m.Title, season, filename)
			item.Aliases = f.Aliases
			if existingSeries[m.TvdbID] {
				item.Status = "exists"
			} else {
				item.Status = "new"
			}
		}
	} else {
		// Try Radarr first, fall back to Sonarr.
		m := cachedLookup(cache, cacheMu, fmt.Sprintf("movie:%s:%d", title, year), func() *MediaMatch {
			return h.lookupMovie(ctx, radarrKey, encoded, title, year)
		})
		if m == nil {
			m = cachedLookup(cache, cacheMu, fmt.Sprintf("series:%s:%d", title, year), func() *MediaMatch {
				return h.lookupSeries(ctx, sonarrKey, encoded, title, year)
			})
		}
		if m != nil {
			item.Aliases = f.Aliases
			if m.Type == "movie" {
				item.Match = m // movie matches have no Season/Episode; safe to share
				item.Edition = extractEdition(filename)
				item.SuggestedPath = suggestedMoviePath(movieRoot, m.Title, m.Year, filename, item.Edition)
				if existingMovies[m.TmdbID] {
					item.Status = "exists"
				} else {
					item.Status = "new"
				}
			} else {
				season := extractSeason(filename)
				episode := extractEpisode(filename)
				mc := *m
				mc.Season = season
				mc.Episode = episode
				item.Match = &mc
				item.SuggestedPath = suggestedTVPath(tvRoot, m.Title, season, filename)
				if existingSeries[m.TvdbID] {
					item.Status = "exists"
				} else {
					item.Status = "new"
				}
			}
		}
	}

	// If the file is already at its suggested destination path, mark it as
	// in-place so the UI can skip the strategy prompt.
	if item.Status == "new" && item.SuggestedPath != "" &&
		filepath.Clean(f.Path) == filepath.Clean(item.SuggestedPath) {
		item.Status = "in_place"
	}

	return item
}

// ── Edition extraction ────────────────────────────────────────────────────────

// editionKeywords maps lowercase tokens found in filenames to their canonical labels.
// Longer/more-specific phrases must appear before shorter ones so the first match wins.
var editionKeywords = []struct {
	tokens []string
	label  string
}{
	{[]string{"final cut"}, "Final Cut"},
	{[]string{"director's cut", "directors cut"}, "Director's Cut"},
	{[]string{"theatrical cut", "theatrical"}, "Theatrical Cut"},
	{[]string{"extended cut", "extended"}, "Extended Cut"},
	{[]string{"unrated cut", "unrated"}, "Unrated"},
	{[]string{"international cut", "international"}, "International Cut"},
	{[]string{"special edition"}, "Special Edition"},
	{[]string{"definitive edition"}, "Definitive Edition"},
	{[]string{"ultimate edition"}, "Ultimate Edition"},
	{[]string{"remastered"}, "Remastered"},
	{[]string{"restored"}, "Restored"},
	{[]string{"redux"}, "Redux"},
}

// extractEdition returns a human-readable edition label extracted from the
// filename, or an empty string if no known cut marker is found.
func extractEdition(filename string) string {
	lower := strings.ToLower(strings.NewReplacer(".", " ", "_", " ").Replace(filename))
	for _, e := range editionKeywords {
		for _, tok := range e.tokens {
			if strings.Contains(lower, tok) {
				return e.label
			}
		}
	}
	return ""
}

// ── Suggested path helpers ────────────────────────────────────────────────────

// suggestedMoviePath returns the destination path for a movie file. When
// edition is non-empty the filename is rewritten to Jellyfin's multi-version
// format: "{title} ({year}) - {edition}.{ext}".
func suggestedMoviePath(root, title string, year int, filename, edition string) string {
	folder := title
	if year > 0 {
		folder = fmt.Sprintf("%s (%d)", title, year)
	}
	if edition != "" {
		ext := filepath.Ext(filename)
		return root + "/" + folder + "/" + folder + " - " + edition + ext
	}
	return root + "/" + folder + "/" + filepath.Base(filename)
}

func suggestedTVPath(root, title string, season int, filename string) string {
	if season > 0 {
		return fmt.Sprintf("%s/%s/Season %02d/%s", root, title, season, filepath.Base(filename))
	}
	return fmt.Sprintf("%s/%s/%s", root, title, filepath.Base(filename))
}

// ── Group key helpers ─────────────────────────────────────────────────────────

// matchItemGroupKey returns a stable group key for a MatchItem.
func matchItemGroupKey(item MatchItem) string {
	if item.Match == nil {
		return "unmatched:" + item.File
	}
	switch item.Match.Type {
	case "movie":
		if item.Match.TmdbID > 0 {
			return fmt.Sprintf("movie:%d", item.Match.TmdbID)
		}
	case "series":
		if item.Match.TvdbID > 0 {
			return fmt.Sprintf("series:%d:s%de%d", item.Match.TvdbID, item.Match.Season, item.Match.Episode)
		}
	}
	return "unmatched:" + item.File
}

// assignGroupKeys sets GroupKey on each MatchItem in place.
func assignGroupKeys(items []MatchItem) {
	for i, item := range items {
		items[i].GroupKey = matchItemGroupKey(item)
	}
}

// ── Scoring ───────────────────────────────────────────────────────────────────

func scoreMatch(cleanedTitle string, year int, matchTitle string, matchYear int) string {
	ct := normalizeTitle(cleanedTitle)
	mt := normalizeTitle(matchTitle)
	if ct == "" || mt == "" {
		return "unmatched"
	}

	yearOK := year == 0 || matchYear == 0 || absInt(year-matchYear) <= 1

	if ct == mt {
		if yearOK {
			return "high"
		}
		return "medium"
	}
	if strings.Contains(mt, ct) || strings.Contains(ct, mt) {
		if yearOK {
			return "medium"
		}
		return "low"
	}

	// Word overlap.
	ctWords := strings.Fields(ct)
	mtSet := make(map[string]bool, len(mt))
	for _, w := range strings.Fields(mt) {
		mtSet[w] = true
	}
	matches := 0
	for _, w := range ctWords {
		if mtSet[w] {
			matches++
		}
	}
	if len(ctWords) == 0 {
		return "unmatched"
	}
	ratio := matches * 100 / len(ctWords)
	switch {
	case ratio >= 80 && yearOK:
		return "medium"
	case ratio >= 60:
		return "low"
	default:
		return "unmatched"
	}
}

func normalizeTitle(s string) string {
	s = strings.ToLower(s)
	for _, pfx := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(s, pfx) {
			s = s[len(pfx):]
		}
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(strings.Join(strings.Fields(b.String()), " "))
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
