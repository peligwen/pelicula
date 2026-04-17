// Package jellyfin provides a typed HTTP client for Jellyfin.
//
// Jellyfin uses the MediaBrowser / Emby authorization header scheme:
//
//	X-Emby-Authorization: MediaBrowser Client="...", Token="<key>"
//
// Unlike *arr services, the header value is composite and not a simple
// "X-Api-Key: <key>" pair, so this client builds the header manually.
package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"pelicula-api/internal/httpx"
)

const (
	defaultTimeout = 10 * time.Second
	embyAuthHeader = `MediaBrowser Client="Pelicula", Device="pelicula-api", DeviceId="pelicula-autowire", Version="1.0"`
)

// Client is a typed HTTP client for Jellyfin.
// Most requests require a token (API key or session token); pass "" for
// unauthenticated calls (e.g. /System/Info/Public during wizard setup).
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// New constructs a Jellyfin Client for the given baseURL.
func New(baseURL string) *Client {
	return &Client{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: defaultTimeout},
	}
}

// NewWithHTTPClient constructs a Client using the provided *http.Client.
// Use this when the caller manages the transport (e.g. test injection or shared clients).
func NewWithHTTPClient(baseURL string, hc *http.Client) *Client {
	return &Client{baseURL: baseURL, httpClient: hc}
}

// NewFromHTTPX constructs a Client by extracting the HTTP transport from an
// *httpx.Client. The base URL and HTTP client are taken from the httpx value.
func NewFromHTTPX(base *httpx.Client) *Client {
	hc := base.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{baseURL: base.BaseURL, httpClient: hc}
}

// ── Raw HTTP helpers ──────────────────────────────────────────────────────────

// Get makes an authenticated GET request to path.
func (c *Client) Get(ctx context.Context, path, token string) ([]byte, error) {
	return c.do(ctx, http.MethodGet, path, token, nil)
}

// Post makes an authenticated POST request with a JSON body to path.
func (c *Client) Post(ctx context.Context, path, token string, body any) ([]byte, error) {
	return c.do(ctx, http.MethodPost, path, token, body)
}

// Delete makes an authenticated DELETE request to path.
func (c *Client) Delete(ctx context.Context, path, token string) error {
	_, err := c.do(ctx, http.MethodDelete, path, token, nil)
	return err
}

// do is the shared implementation for all Jellyfin HTTP calls.
func (c *Client) do(ctx context.Context, method, path, token string, payload any) ([]byte, error) {
	var bodyReader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	c.setEmbyAuth(req, token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return body, &HTTPError{StatusCode: resp.StatusCode}
	}
	return body, nil
}

// setEmbyAuth writes the X-Emby-Authorization header with an optional Token.
func (c *Client) setEmbyAuth(req *http.Request, token string) {
	auth := embyAuthHeader
	if token != "" {
		auth += fmt.Sprintf(`, Token="%s"`, token)
	}
	req.Header.Set("X-Emby-Authorization", auth)
}

// HTTPError captures the HTTP status code returned by Jellyfin.
type HTTPError struct {
	StatusCode int
}

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP %d", e.StatusCode) }

// ── Typed domain methods ──────────────────────────────────────────────────────

// GetSystemInfo fetches /System/Info/Public (no auth required).
func (c *Client) GetSystemInfo(ctx context.Context) (map[string]any, error) {
	raw, err := c.Get(ctx, "/System/Info/Public", "")
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse system info: %w", err)
	}
	return out, nil
}

// GetStartupUser fetches the initial startup user (Jellyfin 10.11+).
// Triggers lazy user initialization inside Jellyfin.
func (c *Client) GetStartupUser(ctx context.Context) error {
	_, err := c.Get(ctx, "/Startup/User", "")
	return err
}

// ConfigureStartup sets the startup wizard UI culture and metadata country.
func (c *Client) ConfigureStartup(ctx context.Context, payload map[string]any) error {
	_, err := c.Post(ctx, "/Startup/Configuration", "", payload)
	return err
}

// SetStartupUser sets the admin username and password during wizard setup.
func (c *Client) SetStartupUser(ctx context.Context, payload map[string]any) error {
	_, err := c.Post(ctx, "/Startup/User", "", payload)
	return err
}

// CompleteWizard marks the startup wizard as done.
func (c *Client) CompleteWizard(ctx context.Context) error {
	_, err := c.Post(ctx, "/Startup/Complete", "", nil)
	return err
}

// AuthenticateByName authenticates username/password against Jellyfin.
// Returns the raw response body for the caller to decode.
func (c *Client) AuthenticateByName(ctx context.Context, username, password string) (map[string]any, error) {
	raw, err := c.Post(ctx, "/Users/AuthenticateByName", "", map[string]any{
		"Username": username,
		"Pw":       password,
	})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse auth response: %w", err)
	}
	return out, nil
}

// ListAPIKeys returns the list of API keys.
func (c *Client) ListAPIKeys(ctx context.Context, token string) (map[string]any, error) {
	raw, err := c.Get(ctx, "/Auth/Keys", token)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse API keys: %w", err)
	}
	return out, nil
}

// CreateAPIKey creates a new API key with the given app name.
// Jellyfin returns 204 No Content on success; the new key must be retrieved
// via ListAPIKeys.
func (c *Client) CreateAPIKey(ctx context.Context, token, appName string) error {
	_, err := c.Post(ctx, "/Auth/Keys?app="+url.QueryEscape(appName), token, nil)
	return err
}

// ListVirtualFolders returns the Jellyfin library list.
func (c *Client) ListVirtualFolders(ctx context.Context, token string) ([]map[string]any, error) {
	raw, err := c.Get(ctx, "/Library/VirtualFolders", token)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse virtual folders: %w", err)
	}
	return out, nil
}

// AddVirtualFolder creates a new media library (virtual folder).
// endpoint must include name, collectionType, and refreshLibrary query params.
func (c *Client) AddVirtualFolder(ctx context.Context, token, endpoint string, payload map[string]any) error {
	_, err := c.Post(ctx, endpoint, token, payload)
	return err
}

// RefreshLibrary triggers a full library scan.
func (c *Client) RefreshLibrary(ctx context.Context, token string) error {
	_, err := c.Post(ctx, "/Library/Refresh", token, nil)
	return err
}

// GetUsers lists all Jellyfin users.
func (c *Client) GetUsers(ctx context.Context, token string) ([]map[string]any, error) {
	raw, err := c.Get(ctx, "/Users", token)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse users: %w", err)
	}
	return out, nil
}

// GetUser fetches a single user by ID.
func (c *Client) GetUser(ctx context.Context, token, userID string) (map[string]any, error) {
	raw, err := c.Get(ctx, "/Users/"+userID, token)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse user: %w", err)
	}
	return out, nil
}

// CreateUser creates a new Jellyfin user and returns the new user ID.
func (c *Client) CreateUser(ctx context.Context, token, username, password string) (string, error) {
	raw, err := c.Post(ctx, "/Users/New", token, map[string]any{
		"Name":     username,
		"Password": password,
	})
	if err != nil {
		return "", err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("parse create user response: %w", err)
	}
	id, _ := result["Id"].(string)
	if id == "" {
		return "", fmt.Errorf("no user ID in Jellyfin create user response")
	}
	return id, nil
}

// UpdateUserPolicy replaces a user's policy object.
// Use GET /Users/{id} → merge changes → POST the full policy back to avoid
// zeroing out other policy fields.
func (c *Client) UpdateUserPolicy(ctx context.Context, token, userID string, policy map[string]any) error {
	_, err := c.Post(ctx, "/Users/"+userID+"/Policy", token, policy)
	return err
}

// UpdateUserConfiguration replaces a user's configuration object.
func (c *Client) UpdateUserConfiguration(ctx context.Context, token, userID string, config map[string]any) error {
	_, err := c.Post(ctx, "/Users/"+userID+"/Configuration", token, config)
	return err
}
