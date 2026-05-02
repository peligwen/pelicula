package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultUserAgent is injected as the User-Agent header on every request made
// through New(). Set it at program startup (before constructing any clients)
// to embed a build-time version, e.g. "Pelicula/1.2.3 (+https://…)".
var DefaultUserAgent = "Pelicula/dev"

// uaTransport wraps an http.RoundTripper to inject a User-Agent header on
// outbound requests that do not already carry one.
type uaTransport struct {
	base http.RoundTripper
	ua   string
}

func (t *uaTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("User-Agent") != "" {
		return t.base.RoundTrip(r)
	}
	r = r.Clone(r.Context())
	r.Header.Set("User-Agent", t.ua)
	return t.base.RoundTrip(r)
}

// Client is a typed HTTP client for a single upstream service.
// All calls carry context and inject authentication via a configurable header.
type Client struct {
	// BaseURL is the scheme+host+base-path prefix for all requests, e.g.
	// "http://sonarr:8989/sonarr" or "http://gluetun:8000".
	BaseURL string

	// apiKey is the credential injected into each request.
	// Access via SetAPIKey / setAuth only; protected by mu.
	apiKey string

	// mu protects apiKey for concurrent reads and writes.
	mu sync.RWMutex

	// KeyHeader is the HTTP header name used to carry the key, e.g. "X-Api-Key".
	// If empty, no auth header is set.
	KeyHeader string

	// KeyScheme, when non-empty, wraps the key value: the header value becomes
	// "<KeyScheme> <APIKey>" instead of just "<APIKey>".
	// E.g. KeyScheme="Bearer" produces "Authorization: Bearer <key>".
	KeyScheme string

	// HTTPClient is the underlying transport. If nil, http.DefaultClient is used.
	HTTPClient *http.Client

	// Retry configures automatic retry on transient failures (5xx, transport errors).
	// Zero value disables retries. New() sets {MaxAttempts: 3, Delay: 500ms}.
	Retry RetryPolicy
}

// RetryPolicy configures automatic retry on transient failures.
// Applies to all requests with a known-idempotent body (GET, DELETE, JSON POST/PUT).
// Errors classified as transient: transport errors and HTTP 5xx responses.
// HTTP 4xx responses are permanent — they are never retried.
type RetryPolicy struct {
	// MaxAttempts is the total number of tries (including the first). Zero or 1 means no retry.
	MaxAttempts int
	// Delay is the initial backoff duration; it doubles on each subsequent attempt.
	Delay time.Duration
}

// isTransientErr returns true for transport errors worth retrying.
// HTTP 5xx responses are handled separately (retry=true returned inline).
func isTransientErr(err error) bool {
	return err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded)
}

// withRetry calls fn until fn signals no-retry, the context is done, or
// the retry budget is exhausted. fn returns (shouldRetry bool, err error).
// When shouldRetry is false, err is returned immediately (may be nil on success).
// When shouldRetry is true and budget remains, fn is called again after backoff.
// The body for POST/PUT requests must be re-created inside fn because
// http.Request bodies are consumed after the first use.
func (c *Client) withRetry(ctx context.Context, fn func() (bool, error)) error {
	p := c.Retry
	maxAttempts := p.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	delay := p.Delay
	if delay <= 0 {
		delay = 500 * time.Millisecond
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
			delay *= 2
		}
		retry, err := fn()
		if !retry {
			return err
		}
		lastErr = err
	}
	return lastErr
}

// New constructs a Client with the given base URL, key, and header.
// A dedicated http.Client with the supplied timeout is created, wrapping
// http.DefaultTransport with a uaTransport that sets DefaultUserAgent on
// requests that do not already carry a User-Agent header.
// The default RetryPolicy retries up to 3 times on 5xx/transport errors.
func New(baseURL, apiKey, keyHeader string, timeout time.Duration) *Client {
	return &Client{
		BaseURL:   baseURL,
		apiKey:    apiKey,
		KeyHeader: keyHeader,
		HTTPClient: &http.Client{
			Timeout:   timeout,
			Transport: &uaTransport{base: http.DefaultTransport, ua: DefaultUserAgent},
		},
		Retry: RetryPolicy{MaxAttempts: 3, Delay: 500 * time.Millisecond},
	}
}

// SetAPIKey updates the API key used to authenticate requests. Safe for
// concurrent use with in-flight requests; the new key takes effect on
// subsequent requests.
func (c *Client) SetAPIKey(key string) {
	c.mu.Lock()
	c.apiKey = key
	c.mu.Unlock()
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
// Retries on transient failures per the configured RetryPolicy.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	return c.withRetry(ctx, func() (bool, error) {
		resp, err := c.Do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return isTransientErr(err), fmt.Errorf("GET %s: %w", redactPath(path), err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 500 {
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			return true, fmt.Errorf("HTTP %d from %s", resp.StatusCode, redactPath(path))
		}
		return false, c.decodeJSON(resp, path, out)
	})
}

// RawGet makes a GET request to path and returns the response body bytes.
// Retries on transient failures per the configured RetryPolicy.
func (c *Client) RawGet(ctx context.Context, path string) ([]byte, error) {
	var result []byte
	var resultErr error
	err := c.withRetry(ctx, func() (bool, error) {
		resp, err := c.Do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return isTransientErr(err), fmt.Errorf("GET %s: %w", redactPath(path), err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 500 {
			return true, fmt.Errorf("HTTP %d from %s", resp.StatusCode, redactPath(path))
		}
		if resp.StatusCode >= 400 {
			result, resultErr = body, fmt.Errorf("HTTP %d from %s", resp.StatusCode, redactPath(path))
		} else {
			result, resultErr = body, nil
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return result, resultErr
}

// PostJSON makes a POST request with a JSON-encoded body and decodes the response into out.
// out may be nil if the response body is not needed.
// Retries on transient failures per the configured RetryPolicy.
func (c *Client) PostJSON(ctx context.Context, path string, body, out any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}
	return c.withRetry(ctx, func() (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(encoded))
		if err != nil {
			return false, fmt.Errorf("build request: %w", err)
		}
		c.setAuth(req)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.httpClient().Do(req)
		if err != nil {
			return isTransientErr(err), fmt.Errorf("POST %s: %w", redactPath(path), err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 500 {
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			return true, fmt.Errorf("HTTP %d from %s", resp.StatusCode, redactPath(path))
		}
		if out == nil {
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			if resp.StatusCode >= 400 {
				return false, fmt.Errorf("HTTP %d from %s", resp.StatusCode, redactPath(path))
			}
			return false, nil
		}
		return false, c.decodeJSON(resp, path, out)
	})
}

// RawPost makes a POST request with a JSON-encoded body and returns the raw bytes.
// Retries on transient failures per the configured RetryPolicy.
func (c *Client) RawPost(ctx context.Context, path string, body any) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}
	var result []byte
	var resultErr error
	err = c.withRetry(ctx, func() (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(encoded))
		if err != nil {
			return false, fmt.Errorf("build request: %w", err)
		}
		c.setAuth(req)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.httpClient().Do(req)
		if err != nil {
			return isTransientErr(err), fmt.Errorf("POST %s: %w", redactPath(path), err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 500 {
			return true, fmt.Errorf("HTTP %d from %s", resp.StatusCode, redactPath(path))
		}
		if resp.StatusCode >= 400 {
			result, resultErr = b, fmt.Errorf("HTTP %d from %s", resp.StatusCode, redactPath(path))
		} else {
			result, resultErr = b, nil
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return result, resultErr
}

// PutJSON makes a PUT request with a JSON-encoded body and returns the raw bytes.
// Retries on transient failures per the configured RetryPolicy.
func (c *Client) PutJSON(ctx context.Context, path string, body any) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}
	var result []byte
	var resultErr error
	err = c.withRetry(ctx, func() (bool, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.BaseURL+path, bytes.NewReader(encoded))
		if err != nil {
			return false, fmt.Errorf("build request: %w", err)
		}
		c.setAuth(req)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.httpClient().Do(req)
		if err != nil {
			return isTransientErr(err), fmt.Errorf("PUT %s: %w", redactPath(path), err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode >= 500 {
			return true, fmt.Errorf("HTTP %d from %s", resp.StatusCode, redactPath(path))
		}
		if resp.StatusCode >= 400 {
			result, resultErr = b, fmt.Errorf("HTTP %d from %s", resp.StatusCode, redactPath(path))
		} else {
			result, resultErr = b, nil
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	return result, resultErr
}

// Delete makes a DELETE request to path.
// Retries on transient failures per the configured RetryPolicy.
func (c *Client) Delete(ctx context.Context, path string) error {
	return c.withRetry(ctx, func() (bool, error) {
		resp, err := c.Do(ctx, http.MethodDelete, path, nil)
		if err != nil {
			return isTransientErr(err), fmt.Errorf("DELETE %s: %w", redactPath(path), err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		if resp.StatusCode >= 500 {
			return true, fmt.Errorf("HTTP %d from %s", resp.StatusCode, redactPath(path))
		}
		if resp.StatusCode >= 400 {
			return false, fmt.Errorf("HTTP %d from %s", resp.StatusCode, redactPath(path))
		}
		return false, nil
	})
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
// The key is read under RLock so concurrent SetAPIKey calls are safe.
func (c *Client) setAuth(req *http.Request) {
	if c.KeyHeader == "" {
		return
	}
	c.mu.RLock()
	key := c.apiKey
	c.mu.RUnlock()
	if key == "" {
		return
	}
	if c.KeyScheme != "" {
		req.Header.Set(c.KeyHeader, c.KeyScheme+" "+key)
	} else {
		req.Header.Set(c.KeyHeader, key)
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

// sensitiveParams lists query parameter names whose values must be redacted in logs.
var sensitiveParams = []string{"apikey", "api_key", "token", "auth", "password", "secret"}

// redactPath redacts sensitive query parameters from path so credentials are not
// written to logs. Matching is case-insensitive.
func redactPath(path string) string {
	idx := strings.IndexByte(path, '?')
	if idx < 0 {
		return path
	}
	q, err := url.ParseQuery(path[idx+1:])
	if err != nil {
		return path
	}
	redacted := false
	for key := range q {
		lower := strings.ToLower(key)
		for _, sensitive := range sensitiveParams {
			if lower == sensitive {
				q.Set(key, "REDACTED")
				redacted = true
				break
			}
		}
	}
	if !redacted {
		return path
	}
	return path[:idx+1] + q.Encode()
}
