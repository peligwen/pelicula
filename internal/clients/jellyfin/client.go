// Package jellyfin is a client for the Jellyfin server API.
//
// Jellyfin authenticates with an X-Emby-Authorization header rather than a
// plain API key, and tokens differ per call (an end user's login, the admin's
// cached token), so the token is a per-call argument instead of client state.
// This package deliberately does not retry: login and user-management calls
// are not safe to repeat blindly.
package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	embyAuthHeader = `MediaBrowser Client="Pelicula", Device="pelicula-api", DeviceId="pelicula-autowire", Version="1.0"`
	defaultTimeout = 10 * time.Second
	maxBody        = 32 << 20
)

// ErrUnauthorized means Jellyfin rejected the credentials or token (HTTP 401).
// AuthenticateByName returns it for bad logins; any *HTTPError with status 401
// also satisfies errors.Is(err, ErrUnauthorized).
var ErrUnauthorized = errors.New("jellyfin: unauthorized")

// HTTPError is returned for any HTTP status >= 400.
type HTTPError struct {
	StatusCode int
	Body       string // may be empty; Jellyfin often replies with plain text
}

func (e *HTTPError) Error() string {
	msg := strings.Join(strings.Fields(e.Body), " ")
	if len(msg) > 120 {
		msg = msg[:120] + "..."
	}
	if msg == "" {
		return fmt.Sprintf("jellyfin: HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("jellyfin: HTTP %d: %s", e.StatusCode, msg)
}

// Is lets a 401 HTTPError match ErrUnauthorized.
func (e *HTTPError) Is(target error) bool {
	return target == ErrUnauthorized && e.StatusCode == http.StatusUnauthorized
}

// Client talks to one Jellyfin server.
type Client struct {
	BaseURL    string // includes the URL base, e.g. http://jellyfin:8096/jellyfin
	HTTPClient *http.Client
}

// New returns a client for baseURL with a 10s timeout.
func New(baseURL string) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: &http.Client{Timeout: defaultTimeout},
	}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// Do sends one request and returns the body. token is appended to the
// X-Emby-Authorization header when non-empty; payload, when non-nil, is sent
// as JSON. A status >= 400 returns the body together with an *HTTPError.
func (c *Client) Do(ctx context.Context, method, path, token string, payload any) ([]byte, error) {
	var rd io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return nil, err
	}
	setEmbyAuth(req, token)
	req.Header.Set("User-Agent", "pelicula")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return body, &HTTPError{StatusCode: resp.StatusCode, Body: string(body)}
	}
	return body, nil
}

func (c *Client) Get(ctx context.Context, path, token string) ([]byte, error) {
	return c.Do(ctx, http.MethodGet, path, token, nil)
}

func (c *Client) Post(ctx context.Context, path, token string, payload any) ([]byte, error) {
	return c.Do(ctx, http.MethodPost, path, token, payload)
}

func (c *Client) Delete(ctx context.Context, path, token string) ([]byte, error) {
	return c.Do(ctx, http.MethodDelete, path, token, nil)
}

// setEmbyAuth sets X-Emby-Authorization, adding Token="..." when token is set.
func setEmbyAuth(req *http.Request, token string) {
	auth := embyAuthHeader
	if token != "" {
		auth += fmt.Sprintf(`, Token="%s"`, token)
	}
	req.Header.Set("X-Emby-Authorization", auth)
}

// AuthResult is a successful login.
type AuthResult struct {
	Token    string
	UserID   string
	Username string
	IsAdmin  bool // Policy.IsAdministrator
}

// AuthenticateByName logs in with a Jellyfin username and password. Bad
// credentials return ErrUnauthorized; an unreachable server returns the
// transport error.
func (c *Client) AuthenticateByName(ctx context.Context, username, password string) (*AuthResult, error) {
	body, err := c.Post(ctx, "/Users/AuthenticateByName", "", map[string]string{
		"Username": username,
		"Pw":       password,
	})
	if err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.StatusCode == http.StatusUnauthorized {
			return nil, ErrUnauthorized
		}
		return nil, err
	}
	var res struct {
		AccessToken string `json:"AccessToken"`
		User        struct {
			ID     string `json:"Id"`
			Name   string `json:"Name"`
			Policy struct {
				IsAdministrator bool `json:"IsAdministrator"`
			} `json:"Policy"`
		} `json:"User"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("jellyfin: invalid auth response: %w", err)
	}
	if res.User.ID == "" || res.AccessToken == "" {
		return nil, errors.New("jellyfin: incomplete auth response")
	}
	return &AuthResult{
		Token:    res.AccessToken,
		UserID:   res.User.ID,
		Username: res.User.Name,
		IsAdmin:  res.User.Policy.IsAdministrator,
	}, nil
}

// Ping checks that Jellyfin answers (GET /System/Info/Public, no auth).
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.Get(ctx, "/System/Info/Public", "")
	return err
}

// StartupWizardCompleted reports whether first-run setup has been finished.
func (c *Client) StartupWizardCompleted(ctx context.Context) (bool, error) {
	body, err := c.Get(ctx, "/System/Info/Public", "")
	if err != nil {
		return false, err
	}
	var info struct {
		StartupWizardCompleted bool `json:"StartupWizardCompleted"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return false, fmt.Errorf("jellyfin: parse system info: %w", err)
	}
	return info.StartupWizardCompleted, nil
}
