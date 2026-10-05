// Package httpx is the retrying JSON/HTTP client shared by the service
// clients (*arr, qBittorrent, gluetun). It retries transient failures,
// injects an API key header, and keeps credentials out of error strings.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DefaultUserAgent is sent on every request that does not set its own.
var DefaultUserAgent = "pelicula"

const (
	maxBody    = 64 << 20 // cap on any response body we read
	maxErrBody = 4 << 10  // cap on the body kept inside an HTTPError
)

// HTTPError is returned (wrapped) for any HTTP status >= 400. Use errors.As
// to branch on StatusCode.
type HTTPError struct {
	StatusCode int
	Body       string // truncated, with the API key redacted
}

func (e *HTTPError) Error() string {
	msg := strings.Join(strings.Fields(e.Body), " ")
	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}
	if msg == "" {
		return fmt.Sprintf("HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, msg)
}

// RetryPolicy configures retries for transient failures.
//
// GET, PUT and DELETE retry on any transport error except cancellation or
// timeout. POST creates resources, so a transport error is retried only when
// the request provably never left the machine (dial failures); a reset after
// send could mean the server already committed the create. HTTP 5xx is
// retried for every method. HTTP 4xx is never retried.
type RetryPolicy struct {
	MaxAttempts int           // total tries including the first; <1 means 1
	Delay       time.Duration // first backoff; doubles each attempt (default 500ms)
}

// Client talks to one upstream service.
type Client struct {
	BaseURL    string       // scheme+host+prefix, no trailing slash
	KeyHeader  string       // header carrying the key; empty disables auth
	KeyScheme  string       // optional prefix, e.g. "Bearer" or "Basic"
	HTTPClient *http.Client // nil means http.DefaultClient
	Retry      RetryPolicy

	mu     sync.RWMutex
	apiKey string
}

// New returns a client with a 3-attempt retry policy.
func New(baseURL, apiKey, keyHeader string, timeout time.Duration) *Client {
	return &Client{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		KeyHeader:  keyHeader,
		HTTPClient: &http.Client{Timeout: timeout},
		Retry:      RetryPolicy{MaxAttempts: 3, Delay: 500 * time.Millisecond},
		apiKey:     apiKey,
	}
}

// SetAPIKey swaps the key; safe while requests are in flight.
func (c *Client) SetAPIKey(key string) {
	c.mu.Lock()
	c.apiKey = key
	c.mu.Unlock()
}

func (c *Client) key() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.apiKey
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// newRequest builds a request with User-Agent and auth applied.
func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", DefaultUserAgent)
	if key := c.key(); c.KeyHeader != "" && key != "" {
		if c.KeyScheme != "" {
			key = c.KeyScheme + " " + key
		}
		req.Header.Set(c.KeyHeader, key)
	}
	return req, nil
}

// Do sends one request with no retry. The caller closes the response body.
func (c *Client) Do(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	return c.httpClient().Do(req)
}

// Probe does a single GET (no retry) and returns nil for any status < 400.
// It is the building block for the clients' Ping methods.
func (c *Client) Probe(ctx context.Context, path string) error {
	resp, err := c.Do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return c.wrapErr(http.MethodGet, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
	if resp.StatusCode >= 400 {
		return c.statusErr(http.MethodGet, path, resp.StatusCode, b)
	}
	return nil
}

// GetJSON GETs path and decodes the JSON response into out.
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	b, err := c.send(ctx, http.MethodGet, path, "", nil, true)
	if err != nil {
		return err
	}
	return decode(b, path, out)
}

// RawGet GETs path and returns the body. On a 4xx the body is returned
// together with the error.
func (c *Client) RawGet(ctx context.Context, path string) ([]byte, error) {
	return c.send(ctx, http.MethodGet, path, "", nil, true)
}

// PostJSON POSTs body as JSON and decodes the response into out (may be nil).
func (c *Client) PostJSON(ctx context.Context, path string, body, out any) error {
	enc, err := encode(body)
	if err != nil {
		return err
	}
	b, err := c.send(ctx, http.MethodPost, path, "application/json", enc, false)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return decode(b, path, out)
}

// RawPost POSTs body as JSON (nil sends no body) and returns the response.
func (c *Client) RawPost(ctx context.Context, path string, body any) ([]byte, error) {
	enc, err := encode(body)
	if err != nil {
		return nil, err
	}
	return c.send(ctx, http.MethodPost, path, "application/json", enc, false)
}

// PutJSON PUTs body as JSON and returns the response.
func (c *Client) PutJSON(ctx context.Context, path string, body any) ([]byte, error) {
	enc, err := encode(body)
	if err != nil {
		return nil, err
	}
	return c.send(ctx, http.MethodPut, path, "application/json", enc, true)
}

// Delete sends DELETE to path.
func (c *Client) Delete(ctx context.Context, path string) error {
	_, err := c.send(ctx, http.MethodDelete, path, "", nil, true)
	return err
}

// PostForm POSTs url-encoded values and returns the response body.
func (c *Client) PostForm(ctx context.Context, path string, values url.Values) ([]byte, error) {
	return c.send(ctx, http.MethodPost, path, "application/x-www-form-urlencoded", []byte(values.Encode()), false)
}

// send runs one logical request with retries. idempotent selects the
// transport-error retry rule (see RetryPolicy).
func (c *Client) send(ctx context.Context, method, path, ctype string, body []byte, idempotent bool) ([]byte, error) {
	attempts := max(c.Retry.MaxAttempts, 1)
	delay := c.Retry.Delay
	if delay <= 0 {
		delay = 500 * time.Millisecond
	}
	var (
		resp []byte
		err  error
	)
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			delay *= 2
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var retry bool
		resp, retry, err = c.once(ctx, method, path, ctype, body, idempotent)
		if !retry {
			break
		}
	}
	return resp, err
}

// once performs a single attempt and reports whether it is worth retrying.
func (c *Client) once(ctx context.Context, method, path, ctype string, body []byte, idempotent bool) ([]byte, bool, error) {
	var rd io.Reader
	if len(body) > 0 {
		rd = bytes.NewReader(body)
	}
	req, err := c.newRequest(ctx, method, path, rd)
	if err != nil {
		return nil, false, err
	}
	if rd != nil && ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		retry := isRetriableSendErr(err)
		if idempotent {
			retry = isTransientErr(err)
		}
		return nil, retry, c.wrapErr(method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, idempotent && isTransientErr(err), c.wrapErr(method, path, err)
	}
	if resp.StatusCode >= 400 {
		return b, resp.StatusCode >= 500, c.statusErr(method, path, resp.StatusCode, b)
	}
	return b, false, nil
}

// statusErr builds the error for an HTTP status >= 400.
func (c *Client) statusErr(method, path string, status int, body []byte) error {
	if len(body) > maxErrBody {
		body = body[:maxErrBody]
	}
	s := string(body)
	if key := c.key(); len(key) >= 8 {
		s = strings.ReplaceAll(s, key, "REDACTED")
	}
	return fmt.Errorf("%s %s: %w", method, redactPath(path), &HTTPError{StatusCode: status, Body: s})
}

// wrapErr adds the redacted request line to a transport error. The *url.Error
// wrapper is dropped because it embeds the full URL, query string included.
func (c *Client) wrapErr(method, path string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("%s %s: %w", method, redactPath(path), err)
}

func encode(body any) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal body: %w", err)
	}
	return b, nil
}

func decode(b []byte, path string, out any) error {
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("decode JSON from %s: %w", redactPath(path), err)
	}
	return nil
}

// isTransientErr: transport errors worth retrying on idempotent requests.
func isTransientErr(err error) bool {
	return err != nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded)
}

// isRetriableSendErr: the error happened while dialing, so the request cannot
// have reached the server and a retry is safe even for POST.
func isRetriableSendErr(err error) bool {
	if !isTransientErr(err) {
		return false
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

var sensitiveParams = map[string]bool{
	"apikey": true, "api_key": true, "token": true,
	"auth": true, "password": true, "secret": true,
}

// redactPath masks sensitive query parameter values so paths are safe to log.
func redactPath(path string) string {
	i := strings.IndexByte(path, '?')
	if i < 0 {
		return path
	}
	q, err := url.ParseQuery(path[i+1:])
	if err != nil {
		return path[:i+1] + "REDACTED"
	}
	hit := false
	for k := range q {
		if sensitiveParams[strings.ToLower(k)] {
			q.Set(k, "REDACTED")
			hit = true
		}
	}
	if !hit {
		return path
	}
	return path[:i+1] + q.Encode()
}
