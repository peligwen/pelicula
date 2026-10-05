// Package arr is a typed client for Sonarr, Radarr and Prowlarr. All three
// speak JSON over HTTP with an X-Api-Key header; only the API version differs
// ("v3" for Sonarr/Radarr, "v1" for Prowlarr).
package arr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"pelicula/internal/httpx"
)

const (
	defaultTimeout = 10 * time.Second
	queuePageSize  = 100
)

// Client talks to a single *arr instance.
type Client struct {
	base   *httpx.Client
	prefix string // "/api/v3" or "/api/v1"
}

// New returns a client for baseURL (including any URL base such as /sonarr).
// apiVersion is "v3" for Sonarr/Radarr and "v1" for Prowlarr.
func New(baseURL, apiKey, apiVersion string) *Client {
	return &Client{
		base:   httpx.New(baseURL, apiKey, "X-Api-Key", defaultTimeout),
		prefix: "/api/" + apiVersion,
	}
}

// SetAPIKey swaps the API key; safe for concurrent use.
func (c *Client) SetAPIKey(key string) { c.base.SetAPIKey(key) }

// Ping checks that the service is up. /ping lives outside /api and needs no key.
func (c *Client) Ping(ctx context.Context) error { return c.base.Probe(ctx, "/ping") }

// Raw helpers. path is relative to /api/<version> and includes any query.

func (c *Client) Get(ctx context.Context, path string) ([]byte, error) {
	return c.base.RawGet(ctx, c.prefix+path)
}

func (c *Client) Post(ctx context.Context, path string, body any) ([]byte, error) {
	return c.base.RawPost(ctx, c.prefix+path, body)
}

func (c *Client) Put(ctx context.Context, path string, body any) ([]byte, error) {
	return c.base.PutJSON(ctx, c.prefix+path, body)
}

func (c *Client) Delete(ctx context.Context, path string) error {
	return c.base.Delete(ctx, c.prefix+path)
}

// list GETs a JSON array of objects.
func (c *Client) list(ctx context.Context, path string) ([]map[string]any, error) {
	var out []map[string]any
	if err := c.base.GetJSON(ctx, c.prefix+path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// object GETs a single JSON object.
func (c *Client) object(ctx context.Context, path string) (map[string]any, error) {
	var out map[string]any
	if err := c.base.GetJSON(ctx, c.prefix+path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// create POSTs payload and returns the created object.
func (c *Client) create(ctx context.Context, path string, payload any) (map[string]any, error) {
	var out map[string]any
	if err := c.base.PostJSON(ctx, c.prefix+path, payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// TriggerCommand queues a command such as MoviesSearch or EpisodeSearch.
func (c *Client) TriggerCommand(ctx context.Context, payload map[string]any) error {
	_, err := c.Post(ctx, "/command", payload)
	return err
}

// Download clients, root folders, notifications (used by autowire).

func (c *Client) ListDownloadClients(ctx context.Context) ([]map[string]any, error) {
	return c.list(ctx, "/downloadclient")
}

func (c *Client) AddDownloadClient(ctx context.Context, payload any) error {
	_, err := c.Post(ctx, "/downloadclient", payload)
	return err
}

func (c *Client) UpdateDownloadClient(ctx context.Context, id int, payload any) error {
	_, err := c.Put(ctx, fmt.Sprintf("/downloadclient/%d", id), payload)
	return err
}

func (c *Client) ListRootFolders(ctx context.Context) ([]map[string]any, error) {
	return c.list(ctx, "/rootfolder")
}

func (c *Client) AddRootFolder(ctx context.Context, payload any) error {
	_, err := c.Post(ctx, "/rootfolder", payload)
	return err
}

func (c *Client) ListNotifications(ctx context.Context) ([]map[string]any, error) {
	return c.list(ctx, "/notification")
}

func (c *Client) AddNotification(ctx context.Context, payload any) error {
	_, err := c.Post(ctx, "/notification", payload)
	return err
}

func (c *Client) UpdateNotification(ctx context.Context, id int, payload any) error {
	_, err := c.Put(ctx, fmt.Sprintf("/notification/%d", id), payload)
	return err
}

// Prowlarr applications and indexers.

func (c *Client) ListApplications(ctx context.Context) ([]map[string]any, error) {
	return c.list(ctx, "/applications")
}

func (c *Client) AddApplication(ctx context.Context, payload any) error {
	_, err := c.Post(ctx, "/applications", payload)
	return err
}

func (c *Client) UpdateApplication(ctx context.Context, id int, payload any) error {
	_, err := c.Put(ctx, fmt.Sprintf("/applications/%d", id), payload)
	return err
}

func (c *Client) ListIndexers(ctx context.Context) ([]map[string]any, error) {
	return c.list(ctx, "/indexer")
}

// Library: Radarr movies.

func (c *Client) GetMovie(ctx context.Context, id int) (map[string]any, error) {
	return c.object(ctx, fmt.Sprintf("/movie/%d", id))
}

func (c *Client) GetMovies(ctx context.Context) ([]map[string]any, error) {
	return c.list(ctx, "/movie")
}

func (c *Client) AddMovie(ctx context.Context, payload map[string]any) (map[string]any, error) {
	return c.create(ctx, "/movie", payload)
}

// LookupMovie searches Radarr's metadata source by free-text term.
func (c *Client) LookupMovie(ctx context.Context, term string) ([]map[string]any, error) {
	return c.list(ctx, "/movie/lookup?term="+url.QueryEscape(term))
}

// LookupMovieByTmdbID returns zero or one candidate. Unlike the other lookups
// Radarr answers this one with a single object, and with 404 for an unknown
// id; both are mapped onto the slice callers expect. A non-zero "id" in the
// result means the movie is already in the library.
func (c *Client) LookupMovieByTmdbID(ctx context.Context, tmdbID int) ([]map[string]any, error) {
	m, err := c.object(ctx, fmt.Sprintf("/movie/lookup/tmdb?tmdbId=%d", tmdbID))
	if err != nil {
		var he *httpx.HTTPError
		if errors.As(err, &he) && he.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	if len(m) == 0 {
		return nil, nil
	}
	return []map[string]any{m}, nil
}

// Library: Sonarr series.

func (c *Client) GetSeries(ctx context.Context) ([]map[string]any, error) {
	return c.list(ctx, "/series")
}

func (c *Client) GetSeriesByID(ctx context.Context, id int) (map[string]any, error) {
	return c.object(ctx, fmt.Sprintf("/series/%d", id))
}

func (c *Client) AddSeries(ctx context.Context, payload map[string]any) (map[string]any, error) {
	return c.create(ctx, "/series", payload)
}

// LookupSeries searches Sonarr's metadata source; term may be "tvdb:<id>".
func (c *Client) LookupSeries(ctx context.Context, term string) ([]map[string]any, error) {
	return c.list(ctx, "/series/lookup?term="+url.QueryEscape(term))
}

func (c *Client) GetEpisodeFiles(ctx context.Context, seriesID int) ([]map[string]any, error) {
	return c.list(ctx, fmt.Sprintf("/episodefile?seriesId=%d", seriesID))
}

func (c *Client) GetQualityProfiles(ctx context.Context) ([]map[string]any, error) {
	return c.list(ctx, "/qualityprofile")
}

// Queue, history and failure handling.

// GetAllQueueRecords pages through /queue until every record is fetched.
// Both includeUnknown* flags are sent; each *arr ignores the one it lacks.
func (c *Client) GetAllQueueRecords(ctx context.Context) ([]map[string]any, error) {
	var all []map[string]any
	for page := 1; ; page++ {
		var pg struct {
			TotalRecords int              `json:"totalRecords"`
			Records      []map[string]any `json:"records"`
		}
		path := fmt.Sprintf("%s/queue?page=%d&pageSize=%d&includeUnknownMovieItems=true&includeUnknownSeriesItems=true",
			c.prefix, page, queuePageSize)
		if err := c.base.GetJSON(ctx, path, &pg); err != nil {
			return all, err
		}
		all = append(all, pg.Records...)
		if len(pg.Records) == 0 || len(all) >= pg.TotalRecords {
			return all, nil
		}
	}
}

// DeleteQueueItem removes a queue entry, optionally also removing the
// download from the client and adding the release to the blocklist.
func (c *Client) DeleteQueueItem(ctx context.Context, id int, removeFromClient, blocklist bool) error {
	return c.Delete(ctx, fmt.Sprintf("/queue/%d?removeFromClient=%t&blocklist=%t", id, removeFromClient, blocklist))
}

// GetHistory returns history records for a raw query string. The endpoint is
// chosen from the query: movieId= uses /history/movie (Radarr), seriesId=
// uses /history/series (Sonarr), anything else the paged /history.
// Example queries: "movieId=5&eventType=1", "seriesId=5&episodeId=9&eventType=1".
func (c *Client) GetHistory(ctx context.Context, query string) ([]map[string]any, error) {
	endpoint := "/history"
	if q, err := url.ParseQuery(query); err == nil {
		switch {
		case q.Has("movieId"):
			endpoint = "/history/movie"
		case q.Has("seriesId"):
			endpoint = "/history/series"
		}
	}
	if query != "" {
		endpoint += "?" + strings.TrimPrefix(query, "?")
	}
	raw, err := c.Get(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return parseRecords(raw)
}

// parseRecords accepts a bare array (history/movie, history/series) or the
// paged {"records":[...]} envelope (history).
func parseRecords(raw []byte) ([]map[string]any, error) {
	var out []map[string]any
	if s := strings.TrimSpace(string(raw)); strings.HasPrefix(s, "[") {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("parse history: %w", err)
		}
		return out, nil
	}
	var env struct {
		Records []map[string]any `json:"records"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("parse history: %w", err)
	}
	return env.Records, nil
}

// MarkHistoryFailed marks a grabbed release as failed; the *arr blocklists it.
func (c *Client) MarkHistoryFailed(ctx context.Context, historyID int) error {
	_, err := c.Post(ctx, fmt.Sprintf("/history/failed/%d", historyID), nil)
	return err
}

// DeleteMovieFile deletes a movie file through Radarr (removes it from disk).
func (c *Client) DeleteMovieFile(ctx context.Context, id int) error {
	return c.Delete(ctx, fmt.Sprintf("/moviefile/%d", id))
}

// DeleteEpisodeFile deletes an episode file through Sonarr (removes it from disk).
func (c *Client) DeleteEpisodeFile(ctx context.Context, id int) error {
	return c.Delete(ctx, fmt.Sprintf("/episodefile/%d", id))
}
