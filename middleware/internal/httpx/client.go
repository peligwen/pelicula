package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a typed HTTP client for a single upstream service.
// All calls carry context and inject authentication via a configurable header.
type Client struct {
	// BaseURL is the scheme+host+base-path prefix for all requests, e.g.
	// "http://sonarr:8989/sonarr" or "http://gluetun:8000".
	BaseURL string

	// APIKey is the credential injected into each request.
	// Leave empty for services that need no key (e.g. qBittorrent via subnet bypass).
	APIKey string

	// KeyHeader is the HTTP header name used to carry the key, e.g. "X-Api-Key".
	// If empty, no auth header is set.
	KeyHeader string

	// KeyScheme, when non-empty, wraps the key value: the header value becomes
	// "<KeyScheme> <APIKey>" instead of just "<APIKey>".
	// E.g. KeyScheme="Bearer" produces "Authorization: Bearer <key>".
	KeyScheme string

	// HTTPClient is the underlying transport. If nil, http.DefaultClient is used.
	HTTPClient *http.Client
}

// New constructs a Client with the given base URL, key, and header.
// A dedicated http.Client with the supplied timeout is created.
func New(baseURL, apiKey, keyHeader string, timeout time.Duration) *Client {
	return &Client{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		KeyHeader:  keyHeader,
		HTTPClient: &http.Client{Timeout: timeout},
	}
}

// httpClient returns the transport to use, defaulting to http.DefaultClient.
func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// Do executes a raw HTTP request against path (appended to BaseURL).
// The caller is responsible for closing the response body.
// Authentication is injected via the configured key header.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	c.setAuth(req)
	return c.httpClient().Do(req)
}

// GetJSON makes a GET request to path and JSON-decodes the response into out.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	resp, err := c.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return fmt.Errorf("GET %s: %w", redactPath(path), err)
	}
	defer resp.Body.Close()
	return c.decodeJSON(resp, path, out)
}

// RawGet makes a GET request to path and returns the response body bytes.
func (c *Client) RawGet(ctx context.Context, path string) ([]byte, error) {
	resp, err := c.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", redactPath(path), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return body, fmt.Errorf("HTTP %d from %s", resp.StatusCode, redactPath(path))
	}
	return io.ReadAll(resp.Body)
}

// PostJSON makes a POST request with a JSON-encoded body and decodes the response into out.
// out may be nil if the response body is not needed.
func (c *Client) PostJSON(ctx context.Context, path string, body, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	c.setAuth(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", redactPath(path), err)
	}
	defer resp.Body.Close()
	if out == nil {
		// Drain the body so the connection can be reused.
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		if resp.StatusCode >= 400 {
			return fmt.Errorf("HTTP %d from %s", resp.StatusCode, redactPath(path))
		}
		return nil
	}
	return c.decodeJSON(resp, path, out)
}

// RawPost makes a POST request with a JSON-encoded body and returns the raw bytes.
func (c *Client) RawPost(ctx context.Context, path string, body any) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	c.setAuth(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", redactPath(path), err)
	}
	defer resp.Body.Close()
	b, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return b, fmt.Errorf("HTTP %d from %s", resp.StatusCode, redactPath(path))
	}
	return b, readErr
}

// PutJSON makes a PUT request with a JSON-encoded body and returns the raw bytes.
func (c *Client) PutJSON(ctx context.Context, path string, body any) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.BaseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	c.setAuth(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("PUT %s: %w", redactPath(path), err)
	}
	defer resp.Body.Close()
	b, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return b, fmt.Errorf("HTTP %d from %s", resp.StatusCode, redactPath(path))
	}
	return b, readErr
}

// Delete makes a DELETE request to path.
func (c *Client) Delete(ctx context.Context, path string) error {
	resp, err := c.Do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return fmt.Errorf("DELETE %s: %w", redactPath(path), err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body) //nolint:errcheck
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, redactPath(path))
	}
	return nil
}

// PostForm makes a POST with application/x-www-form-urlencoded body and
// returns the raw response body. The caller is responsible for checking
// the response status via the returned error.
func (c *Client) PostForm(ctx context.Context, path string, values url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	c.setAuth(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("POST %s: %w", redactPath(path), err)
	}
	defer resp.Body.Close()
	b, readErr := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return b, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, redactPath(path), string(b))
	}
	return b, readErr
}

// setAuth injects the configured API key into req using the configured header.
func (c *Client) setAuth(req *http.Request) {
	if c.KeyHeader == "" || c.APIKey == "" {
		return
	}
	if c.KeyScheme != "" {
		req.Header.Set(c.KeyHeader, c.KeyScheme+" "+c.APIKey)
	} else {
		req.Header.Set(c.KeyHeader, c.APIKey)
	}
}

// decodeJSON reads resp.Body into out, returning an error on non-2xx status or
// JSON decode failure.
func (c *Client) decodeJSON(resp *http.Response, path string, out any) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, redactPath(path))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode JSON from %s: %w", redactPath(path), err)
	}
	return nil
}

// redactPath replaces "apikey=<val>" in query strings with "apikey=REDACTED"
// so API keys are not written to logs.
func redactPath(path string) string {
	idx := strings.IndexByte(path, '?')
	if idx < 0 {
		return path
	}
	q, err := url.ParseQuery(path[idx+1:])
	if err != nil {
		return path
	}
	if q.Get("apikey") == "" {
		return path
	}
	q.Set("apikey", "REDACTED")
	return path[:idx+1] + q.Encode()
}
