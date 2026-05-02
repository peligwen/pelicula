// Package arr provides a typed HTTP client for *arr services (Sonarr, Radarr,
// Prowlarr). All three share the same REST shape: JSON over HTTP with an
// X-Api-Key header.
package arr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"pelicula-api/internal/httpx"
)

const defaultTimeout = 10 * time.Second

// Client is a typed HTTP client for a single *arr service instance.
type Client struct {
	base *httpx.Client
}

// New constructs a Client for baseURL authenticated with apiKey.
func New(baseURL, apiKey string) *Client {
	return &Client{
		base: httpx.New(baseURL, apiKey, "X-Api-Key", defaultTimeout),
	}
}

// NewWithClient constructs a Client that shares an existing *httpx.Client.
// Useful when the caller manages the HTTP transport (e.g. for test injection).
func NewWithClient(base *httpx.Client) *Client {
	return &Client{base: base}
}

// SetAPIKey updates the API key used to authenticate requests. Safe for
// concurrent use with in-flight requests.
func (c *Client) SetAPIKey(apiKey string) {
	c.base.SetAPIKey(apiKey)
}

// ── Generic raw helpers (used internally and by callers that need raw bytes) ──

// Get makes a GET request and returns the raw response body.
// path must include any query parameters.
func (c *Client) Get(ctx context.Context, path string) ([]byte, error) {
	return c.base.RawGet(ctx, path)
}

// Post makes a POST request with a JSON body and returns the raw response body.
func (c *Client) Post(ctx context.Context, path string, body any) ([]byte, error) {
	return c.base.RawPost(ctx, path, body)
}

// Put makes a PUT request with a JSON body and returns the raw response body.
func (c *Client) Put(ctx context.Context, path string, body any) ([]byte, error) {
	return c.base.PutJSON(ctx, path, body)
}

// Delete makes a DELETE request.
func (c *Client) Delete(ctx context.Context, path string) error {
	return c.base.Delete(ctx, path)
}

// ── Typed domain methods (based on actual call sites) ────────────────────────

// TriggerCommand sends a named command to the *arr instance (e.g. "CheckHealth",
// "RescanMovie", "RefreshSeries").
func (c *Client) TriggerCommand(ctx context.Context, apiVer string, payload map[string]any) error {
	_, err := c.Post(ctx, apiVer+"/command", payload)
	return err
}

// ListDownloadClients returns the current download client configuration.
func (c *Client) ListDownloadClients(ctx context.Context, apiVer string) ([]map[string]any, error) {
	raw, err := c.Get(ctx, apiVer+"/downloadclient")
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse download clients: %w", err)
	}
	return out, nil
}

// AddDownloadClient creates a new download client configuration.
func (c *Client) AddDownloadClient(ctx context.Context, apiVer string, cfg map[string]any) error {
	_, err := c.Post(ctx, apiVer+"/downloadclient", cfg)
	return err
}

// UpdateDownloadClient replaces a download client record by ID.
func (c *Client) UpdateDownloadClient(ctx context.Context, apiVer string, id int, payload map[string]any) error {
	_, err := c.Put(ctx, fmt.Sprintf("%s/downloadclient/%d", apiVer, id), payload)
	return err
}

// ListRootFolders returns the configured root folders.
func (c *Client) ListRootFolders(ctx context.Context, apiVer string) ([]map[string]any, error) {
	raw, err := c.Get(ctx, apiVer+"/rootfolder")
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse root folders: %w", err)
	}
	return out, nil
}

// AddRootFolder creates a new root folder.
func (c *Client) AddRootFolder(ctx context.Context, apiVer string, payload map[string]any) error {
	_, err := c.Post(ctx, apiVer+"/rootfolder", payload)
	return err
}

// ListNotifications returns the current notification configuration.
func (c *Client) ListNotifications(ctx context.Context, apiVer string) ([]map[string]any, error) {
	raw, err := c.Get(ctx, apiVer+"/notification")
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse notifications: %w", err)
	}
	return out, nil
}

// AddNotification creates a new notification.
func (c *Client) AddNotification(ctx context.Context, apiVer string, payload map[string]any) error {
	_, err := c.Post(ctx, apiVer+"/notification", payload)
	return err
}

// UpdateNotification replaces a notification record by ID.
func (c *Client) UpdateNotification(ctx context.Context, apiVer string, id int, payload map[string]any) error {
	_, err := c.Put(ctx, fmt.Sprintf("%s/notification/%d", apiVer, id), payload)
	return err
}

// GetQueuePage fetches one page of the download queue. extraParams are appended
// verbatim (e.g. "&includeUnknownMovieItems=true"). Returns the page response.
type QueuePage struct {
	TotalRecords int              `json:"totalRecords"`
	Records      []map[string]any `json:"records"`
}

// GetQueuePage fetches a single page of the download queue.
func (c *Client) GetQueuePage(ctx context.Context, apiVer string, page, pageSize int, extraParams string) (*QueuePage, error) {
	path := fmt.Sprintf("%s/queue?pageSize=%d&page=%d%s", apiVer, pageSize, page, extraParams)
	raw, err := c.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	var out QueuePage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse queue page: %w", err)
	}
	return &out, nil
}

// DeleteQueueItem removes a queue entry by ID.
func (c *Client) DeleteQueueItem(ctx context.Context, id int) error {
	return c.Delete(ctx, fmt.Sprintf("/api/v3/queue/%d", id))
}

// DeleteBlocklistItem removes an item from the blocklist by ID.
func (c *Client) DeleteBlocklistItem(ctx context.Context, apiVer string, id int) error {
	return c.Delete(ctx, fmt.Sprintf("%s/blocklist/%d", apiVer, id))
}

// GetMovie fetches a single movie by Radarr internal ID.
func (c *Client) GetMovie(ctx context.Context, apiVer string, id int) (map[string]any, error) {
	raw, err := c.Get(ctx, fmt.Sprintf("%s/movie/%d", apiVer, id))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse movie: %w", err)
	}
	return out, nil
}

// UpdateMovie replaces a movie record.
func (c *Client) UpdateMovie(ctx context.Context, apiVer string, id int, payload map[string]any) error {
	_, err := c.Put(ctx, fmt.Sprintf("%s/movie/%d", apiVer, id), payload)
	return err
}

// GetMovies fetches all movies.
func (c *Client) GetMovies(ctx context.Context, apiVer string) ([]map[string]any, error) {
	raw, err := c.Get(ctx, apiVer+"/movie")
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse movies: %w", err)
	}
	return out, nil
}

// GetMoviesByPath fetches movies matching a file path.
func (c *Client) GetMoviesByPath(ctx context.Context, apiVer, path string) ([]map[string]any, error) {
	raw, err := c.Get(ctx, apiVer+"/movie?path="+url.QueryEscape(path))
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse movies: %w", err)
	}
	return out, nil
}

// AddMovie adds a movie to Radarr.
func (c *Client) AddMovie(ctx context.Context, apiVer string, payload map[string]any) (map[string]any, error) {
	raw, err := c.Post(ctx, apiVer+"/movie", payload)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse add movie response: %w", err)
	}
	return out, nil
}

// GetEpisode fetches a single episode by Sonarr internal ID.
func (c *Client) GetEpisode(ctx context.Context, apiVer string, id int) (map[string]any, error) {
	raw, err := c.Get(ctx, fmt.Sprintf("%s/episode/%d", apiVer, id))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse episode: %w", err)
	}
	return out, nil
}

// UpdateEpisode replaces an episode record.
func (c *Client) UpdateEpisode(ctx context.Context, apiVer string, id int, payload map[string]any) error {
	_, err := c.Put(ctx, fmt.Sprintf("%s/episode/%d", apiVer, id), payload)
	return err
}

// GetEpisodes fetches all episodes for a series.
func (c *Client) GetEpisodes(ctx context.Context, apiVer string, seriesID int) ([]map[string]any, error) {
	raw, err := c.Get(ctx, fmt.Sprintf("%s/episode?seriesId=%d", apiVer, seriesID))
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse episodes: %w", err)
	}
	return out, nil
}

// GetEpisodeFiles fetches episode files for a series.
func (c *Client) GetEpisodeFiles(ctx context.Context, apiVer string, seriesID int) ([]map[string]any, error) {
	raw, err := c.Get(ctx, fmt.Sprintf("%s/episodefile?seriesId=%d", apiVer, seriesID))
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse episode files: %w", err)
	}
	return out, nil
}

// GetEpisodeFilesByPath fetches episode files matching a file path.
func (c *Client) GetEpisodeFilesByPath(ctx context.Context, apiVer, path string) ([]map[string]any, error) {
	raw, err := c.Get(ctx, apiVer+"/episodefile?path="+url.QueryEscape(path))
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse episode files: %w", err)
	}
	return out, nil
}

// GetSeries fetches all series.
func (c *Client) GetSeries(ctx context.Context, apiVer string) ([]map[string]any, error) {
	raw, err := c.Get(ctx, apiVer+"/series")
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse series: %w", err)
	}
	return out, nil
}

// GetSeriesByID fetches a single series by Sonarr internal ID or slug.
func (c *Client) GetSeriesByID(ctx context.Context, apiVer, id string) (map[string]any, error) {
	raw, err := c.Get(ctx, apiVer+"/series/"+url.PathEscape(id))
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse series: %w", err)
	}
	return out, nil
}

// AddSeries adds a series to Sonarr.
func (c *Client) AddSeries(ctx context.Context, apiVer string, payload map[string]any) (map[string]any, error) {
	raw, err := c.Post(ctx, apiVer+"/series", payload)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse add series response: %w", err)
	}
	return out, nil
}

// GetQualityProfiles fetches the list of quality profiles.
func (c *Client) GetQualityProfiles(ctx context.Context, apiVer string) ([]map[string]any, error) {
	raw, err := c.Get(ctx, apiVer+"/qualityprofile")
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse quality profiles: %w", err)
	}
	return out, nil
}

// GetTags fetches the tag list.
func (c *Client) GetTags(ctx context.Context, apiVer string) ([]map[string]any, error) {
	raw, err := c.Get(ctx, apiVer+"/tag")
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse tags: %w", err)
	}
	return out, nil
}

// AddTag creates a tag and returns the created tag object.
func (c *Client) AddTag(ctx context.Context, apiVer string, payload map[string]any) (map[string]any, error) {
	raw, err := c.Post(ctx, apiVer+"/tag", payload)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse add tag response: %w", err)
	}
	return out, nil
}

// ListApplications returns the connected application list (Prowlarr only).
func (c *Client) ListApplications(ctx context.Context) ([]map[string]any, error) {
	raw, err := c.Get(ctx, "/api/v1/applications")
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse applications: %w", err)
	}
	return out, nil
}

// UpdateApplication replaces an application record (Prowlarr only).
func (c *Client) UpdateApplication(ctx context.Context, id int, payload map[string]any) error {
	_, err := c.Put(ctx, fmt.Sprintf("/api/v1/applications/%d", id), payload)
	return err
}

// AddApplication registers a new application (Prowlarr only).
func (c *Client) AddApplication(ctx context.Context, payload map[string]any) error {
	_, err := c.Post(ctx, "/api/v1/applications", payload)
	return err
}

// Search queries Prowlarr's indexer search endpoint.
func (c *Client) Search(ctx context.Context, query string) ([]map[string]any, error) {
	path := "/api/v1/search?query=" + url.QueryEscape(query) + "&type=search&limit=100"
	raw, err := c.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse search results: %w", err)
	}
	return out, nil
}

// GetItemByPath fetches an item by path using the provided path param key (e.g.
// "path" for both /movie?path= and /episodefile?path=).
func (c *Client) GetItemByPath(ctx context.Context, endpoint, path string) ([]map[string]any, error) {
	raw, err := c.Get(ctx, endpoint+"?path="+url.QueryEscape(path))
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return out, nil
}

// ── Lookup ───────────────────────────────────────────────────────────────────

// LookupMovie searches Radarr's movie metadata sources (TMDB) by free-form term.
// Returns the array of candidate matches. Used for "add movie by search term" flows.
func (c *Client) LookupMovie(ctx context.Context, apiVer, term string) ([]map[string]any, error) {
	raw, err := c.Get(ctx, apiVer+"/movie/lookup?term="+url.QueryEscape(term))
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse lookup movie: %w", err)
	}
	return out, nil
}

// LookupMovieByTmdbID looks up a single movie by its TMDB ID. Returns one or
// zero candidate(s).
func (c *Client) LookupMovieByTmdbID(ctx context.Context, apiVer string, tmdbID int) ([]map[string]any, error) {
	raw, err := c.Get(ctx, fmt.Sprintf("%s/movie/lookup/tmdb?tmdbId=%d", apiVer, tmdbID))
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse lookup movie by tmdb id: %w", err)
	}
	return out, nil
}

// LookupSeries searches Sonarr's series metadata sources (TVDB) by free-form term.
// Term may include a "tvdb:<id>" prefix to look up by TVDB ID.
func (c *Client) LookupSeries(ctx context.Context, apiVer, term string) ([]map[string]any, error) {
	raw, err := c.Get(ctx, apiVer+"/series/lookup?term="+url.QueryEscape(term))
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse lookup series: %w", err)
	}
	return out, nil
}

// ── Queue (all pages) ─────────────────────────────────────────────────────────

// GetAllQueueRecords paginates through every page of the *arr download queue
// at the given apiVer. extraParams (if non-empty) is appended verbatim to each
// page request, e.g. "&includeUnknownMovieItems=true".
//
// Iterates GetQueuePage until either the cumulative records reach the
// reported TotalRecords, or a page returns zero records.
func (c *Client) GetAllQueueRecords(ctx context.Context, apiVer, extraParams string) ([]map[string]any, error) {
	const pageSize = 100
	var all []map[string]any
	page := 1
	for {
		pg, err := c.GetQueuePage(ctx, apiVer, page, pageSize, extraParams)
		if err != nil {
			return all, err
		}
		all = append(all, pg.Records...)
		if len(all) >= pg.TotalRecords || len(pg.Records) == 0 {
			break
		}
		page++
	}
	return all, nil
}

// ── Release profiles ──────────────────────────────────────────────────────────

// ListReleaseProfiles returns the configured release profiles.
func (c *Client) ListReleaseProfiles(ctx context.Context, apiVer string) ([]map[string]any, error) {
	raw, err := c.Get(ctx, apiVer+"/releaseprofile")
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse release profiles: %w", err)
	}
	return out, nil
}

// AddReleaseProfile creates a release profile.
func (c *Client) AddReleaseProfile(ctx context.Context, apiVer string, payload map[string]any) error {
	_, err := c.Post(ctx, apiVer+"/releaseprofile", payload)
	return err
}

// UpdateReleaseProfile replaces a release profile by ID.
func (c *Client) UpdateReleaseProfile(ctx context.Context, apiVer string, id int, payload map[string]any) error {
	_, err := c.Put(ctx, fmt.Sprintf("%s/releaseprofile/%d", apiVer, id), payload)
	return err
}

// ── History / Missing ─────────────────────────────────────────────────────────

// GetHistory fetches the *arr history endpoint. extraParams (if non-empty) is
// appended verbatim, e.g. "?pageSize=20&sortKey=date&sortDir=desc". The caller
// is responsible for the leading "?" if non-empty (matches existing legacy shape).
func (c *Client) GetHistory(ctx context.Context, apiVer, extraParams string) ([]byte, error) {
	return c.Get(ctx, apiVer+"/history"+extraParams)
}

// GetMissing fetches the wanted/missing endpoint. extraParams is appended to
// the path (must include leading "?" when non-empty).
func (c *Client) GetMissing(ctx context.Context, apiVer, extraParams string) ([]byte, error) {
	return c.Get(ctx, apiVer+"/wanted/missing"+extraParams)
}

// ── Indexers ──────────────────────────────────────────────────────────────────

// ListIndexers returns the configured indexer list. Path format depends on
// apiVer ("/api/v1/indexer" for Prowlarr). Caller passes apiVer.
func (c *Client) ListIndexers(ctx context.Context, apiVer string) ([]map[string]any, error) {
	raw, err := c.Get(ctx, apiVer+"/indexer")
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse indexers: %w", err)
	}
	return out, nil
}
