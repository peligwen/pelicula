// Package jellyfin provides a typed HTTP client for the Jellyfin media server.
//
// Jellyfin uses a non-standard "X-Emby-Authorization" header rather than a
// simple API key header, so this client does not wrap httpx.Client directly.
// Instead it owns an *http.Client and injects the Emby auth header on every
// request. A token (session token or persistent API key) is passed per-call so
// callers can use different credentials within the same client instance
// (e.g. a wizard session token before a persistent API key exists).
package jellyfin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const embyAuthHeader = `MediaBrowser Client="Pelicula", Device="pelicula-api", DeviceId="pelicula-autowire", Version="1.0"`

const defaultTimeout = 10 * time.Second

// HTTPError captures the HTTP status code from a failed Jellyfin API response.
type HTTPError struct {
	StatusCode int
}

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP %d", e.StatusCode) }

// Client is a typed HTTP client for Jellyfin.
// All methods accept a token which is appended to the Emby authorization header.
// Pass an empty token for unauthenticated calls (e.g. /System/Info/Public).
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// New constructs a Client for baseURL with a default 10s timeout.
func New(baseURL string) *Client {
	return &Client{
		BaseURL:    baseURL,
		HTTPClient: &http.Client{Timeout: defaultTimeout},
	}
}

// NewWithHTTPClient constructs a Client that uses the supplied *http.Client.
// Useful for tests (httptest.Server) and for sharing a transport.
func NewWithHTTPClient(baseURL string, hc *http.Client) *Client {
	return &Client{BaseURL: baseURL, HTTPClient: hc}
}

// httpClient returns the underlying transport, falling back to http.DefaultClient.
func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// Do executes a raw HTTP request against path (appended to BaseURL).
// token is appended to the Emby authorization header when non-empty.
// payload is JSON-encoded and sent as the request body when non-nil.
// The caller is responsible for closing the response body.
func (c *Client) Do(method, path, token string, payload any) ([]byte, error) {
	var bodyReader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.BaseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	setEmbyAuth(req, token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return body, &HTTPError{StatusCode: resp.StatusCode}
	}
	return body, nil
}

// Get makes a GET request to Jellyfin.
func (c *Client) Get(path, token string) ([]byte, error) {
	return c.Do(http.MethodGet, path, token, nil)
}

// Post makes a POST request to Jellyfin with an optional JSON payload.
func (c *Client) Post(path, token string, payload any) ([]byte, error) {
	return c.Do(http.MethodPost, path, token, payload)
}

// Delete makes a DELETE request to Jellyfin.
func (c *Client) Delete(path, token string) ([]byte, error) {
	return c.Do(http.MethodDelete, path, token, nil)
}

// AuthenticateByName authenticates a username+password against Jellyfin and
// returns the resulting session token and user details.
func (c *Client) AuthenticateByName(username, password string) (*AuthResult, error) {
	payload, err := json.Marshal(map[string]string{"Username": username, "Pw": password})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.BaseURL+"/Users/AuthenticateByName", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	setEmbyAuth(req, "")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, &HTTPError{StatusCode: resp.StatusCode}
	}

	var result struct {
		User struct {
			Id     string `json:"Id"`
			Name   string `json:"Name"`
			Policy struct {
				IsAdministrator bool `json:"IsAdministrator"`
			} `json:"Policy"`
		} `json:"User"`
		AccessToken string `json:"AccessToken"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("invalid Jellyfin auth response: %w", err)
	}
	if result.User.Id == "" || result.AccessToken == "" {
		return nil, fmt.Errorf("incomplete Jellyfin auth response")
	}
	return &AuthResult{
		UserID:          result.User.Id,
		Username:        result.User.Name,
		IsAdministrator: result.User.Policy.IsAdministrator,
		AccessToken:     result.AccessToken,
	}, nil
}

// AuthResult holds the fields returned after a successful authentication.
type AuthResult struct {
	UserID          string
	Username        string
	IsAdministrator bool
	AccessToken     string
}

// ExtractMessage extracts a user-facing message from a Jellyfin error body.
// Jellyfin error responses typically carry {"Message":"..."}.
func ExtractMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var v struct {
		Message string `json:"Message"`
	}
	if json.Unmarshal(body, &v) == nil && v.Message != "" {
		if len(v.Message) > 120 {
			return v.Message[:120]
		}
		return v.Message
	}
	return ""
}

// setEmbyAuth injects the X-Emby-Authorization header into req.
// When token is non-empty it is appended as Token="<token>".
func setEmbyAuth(req *http.Request, token string) {
	auth := embyAuthHeader
	if token != "" {
		auth += fmt.Sprintf(`, Token="%s"`, token)
	}
	req.Header.Set("X-Emby-Authorization", auth)
}
