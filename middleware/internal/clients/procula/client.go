// Package procula provides a typed HTTP client for the Procula processing
// pipeline API.
//
// Procula is an internal service (not exposed outside the Docker network).
// Authentication uses the X-API-Key header when PROCULA_API_KEY is set.
// All mutation requests go through the action bus at POST /api/procula/actions.
package procula

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"pelicula-api/internal/httpx"
)

const defaultTimeout = 10 * time.Second

// Client is a typed HTTP client for the Procula API.
type Client struct {
	base *httpx.Client
}

// New constructs a Client for the given baseURL, authenticated with apiKey.
// Pass an empty apiKey when PROCULA_API_KEY is not configured.
func New(baseURL, apiKey string) *Client {
	return &Client{
		base: httpx.New(baseURL, apiKey, "X-API-Key", defaultTimeout),
	}
}

// NewWithClient constructs a Client that wraps an existing *httpx.Client.
func NewWithClient(base *httpx.Client) *Client {
	return &Client{base: base}
}

// ── Action bus ────────────────────────────────────────────────────────────────

// EnqueueAction posts an action to the Procula action bus and returns the raw
// response body. body must be a JSON-serialisable value of the form:
//
//	{"action": "<name>", "target": {...}, "params": {...}}
//
// Pass waitQuery as "?wait=true" or "" to control synchronous vs async dispatch.
func (c *Client) EnqueueAction(ctx context.Context, body any, waitQuery string) ([]byte, error) {
	path := "/api/procula/actions" + waitQuery
	raw, err := c.base.RawPost(ctx, path, body)
	if err != nil {
		return nil, fmt.Errorf("enqueue action: %w", err)
	}
	return raw, nil
}

// GetActionsRegistry fetches the action handler registry from Procula.
func (c *Client) GetActionsRegistry(ctx context.Context) ([]byte, error) {
	raw, err := c.base.RawGet(ctx, "/api/procula/actions/registry")
	if err != nil {
		return nil, fmt.Errorf("actions registry: %w", err)
	}
	return raw, nil
}

// ── Jobs ──────────────────────────────────────────────────────────────────────

// CreateJob posts a new pipeline job to Procula. body must be a JSON-serialisable
// value matching Procula's JobSource schema.
func (c *Client) CreateJob(ctx context.Context, body any) ([]byte, error) {
	raw, err := c.base.RawPost(ctx, "/api/procula/jobs", body)
	if err != nil {
		return nil, fmt.Errorf("create job: %w", err)
	}
	return raw, nil
}

// GetStatus fetches the Procula queue status summary.
func (c *Client) GetStatus(ctx context.Context) ([]byte, error) {
	raw, err := c.base.RawGet(ctx, "/api/procula/status")
	if err != nil {
		return nil, fmt.Errorf("get status: %w", err)
	}
	return raw, nil
}

// ListJobs returns all jobs from Procula as raw JSON bytes.
func (c *Client) ListJobs(ctx context.Context) ([]byte, error) {
	raw, err := c.base.RawGet(ctx, "/api/procula/jobs")
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	return raw, nil
}

// GetJob fetches a single job by ID.
func (c *Client) GetJob(ctx context.Context, id string) ([]byte, error) {
	raw, err := c.base.RawGet(ctx, "/api/procula/jobs/"+url.PathEscape(id))
	if err != nil {
		return nil, fmt.Errorf("get job %s: %w", id, err)
	}
	return raw, nil
}

// RetryJob re-queues a failed job for processing.
func (c *Client) RetryJob(ctx context.Context, id string) ([]byte, error) {
	raw, err := c.base.RawPost(ctx, "/api/procula/jobs/"+url.PathEscape(id)+"/retry", nil)
	if err != nil {
		return nil, fmt.Errorf("retry job %s: %w", id, err)
	}
	return raw, nil
}

// ── Storage & Notifications ───────────────────────────────────────────────────

// GetStorage fetches the Procula storage report.
func (c *Client) GetStorage(ctx context.Context) ([]byte, error) {
	raw, err := c.base.RawGet(ctx, "/api/procula/storage")
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	return raw, nil
}

// GetNotifications fetches the Procula notification feed.
func (c *Client) GetNotifications(ctx context.Context) ([]byte, error) {
	raw, err := c.base.RawGet(ctx, "/api/procula/notifications")
	if err != nil {
		return nil, fmt.Errorf("notifications: %w", err)
	}
	return raw, nil
}

// GetCatalogFlags fetches the Procula catalog flags endpoint.
func (c *Client) GetCatalogFlags(ctx context.Context) ([]byte, error) {
	raw, err := c.base.RawGet(ctx, "/api/procula/catalog/flags")
	if err != nil {
		return nil, fmt.Errorf("catalog flags: %w", err)
	}
	return raw, nil
}

// ── Transcode profiles ────────────────────────────────────────────────────────

// ListProfiles fetches the configured transcode profiles.
func (c *Client) ListProfiles(ctx context.Context) ([]byte, error) {
	raw, err := c.base.RawGet(ctx, "/api/procula/profiles")
	if err != nil {
		return nil, fmt.Errorf("list profiles: %w", err)
	}
	return raw, nil
}

// CreateProfile creates a new transcode profile.
func (c *Client) CreateProfile(ctx context.Context, body []byte) ([]byte, error) {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse profile body: %w", err)
	}
	raw, err := c.base.RawPost(ctx, "/api/procula/profiles", payload)
	if err != nil {
		return nil, fmt.Errorf("create profile: %w", err)
	}
	return raw, nil
}

// DeleteProfile deletes a transcode profile by name.
func (c *Client) DeleteProfile(ctx context.Context, name string) error {
	if err := c.base.Delete(ctx, "/api/procula/profiles/"+url.PathEscape(name)); err != nil {
		return fmt.Errorf("delete profile %q: %w", name, err)
	}
	return nil
}
