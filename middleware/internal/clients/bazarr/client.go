// Package bazarr provides a typed HTTP client for Bazarr.
//
// Bazarr is the only form-consuming service in the stack (Flask-RESTx reads
// request.form). GET requests use X-API-KEY authentication; POST requests use
// application/x-www-form-urlencoded bodies.
package bazarr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"pelicula-api/internal/httpx"
)

const defaultTimeout = 10 * time.Second

// Client is a typed HTTP client for Bazarr.
type Client struct {
	base *httpx.Client
}

// New constructs a Client for baseURL authenticated with apiKey.
// Bazarr uses the header "X-API-KEY" (all caps) for authentication.
func New(baseURL, apiKey string) *Client {
	return &Client{
		base: httpx.New(baseURL, apiKey, "X-API-KEY", defaultTimeout),
	}
}

// NewWithClient constructs a Client that shares an existing *httpx.Client.
func NewWithClient(base *httpx.Client) *Client {
	return &Client{base: base}
}

// GetSettings fetches the current Bazarr system settings.
func (c *Client) GetSettings(ctx context.Context) (map[string]any, error) {
	raw, err := c.base.RawGet(ctx, "/api/system/settings")
	if err != nil {
		return nil, fmt.Errorf("get bazarr settings: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse bazarr settings: %w", err)
	}
	return out, nil
}

// GetLanguageProfiles fetches the configured subtitle language profiles.
func (c *Client) GetLanguageProfiles(ctx context.Context) ([]map[string]any, error) {
	raw, err := c.base.RawGet(ctx, "/api/system/languages/profiles")
	if err != nil {
		return nil, fmt.Errorf("get language profiles: %w", err)
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse language profiles: %w", err)
	}
	return out, nil
}

// SaveSettings posts a form-encoded settings payload to Bazarr's save_settings
// endpoint. Bazarr requires form encoding (not JSON) for /api/system/settings.
func (c *Client) SaveSettings(ctx context.Context, form url.Values) error {
	_, err := c.base.PostForm(ctx, "/api/system/settings", form)
	return err
}

// RawGet performs a GET and returns the raw response bytes without
// deserialisation. Use when the caller needs to inspect the response as
// raw JSON (e.g., for custom struct unmarshalling in the wiring logic).
func (c *Client) RawGet(ctx context.Context, path string) ([]byte, error) {
	return c.base.RawGet(ctx, path)
}
