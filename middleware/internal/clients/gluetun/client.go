// Package gluetun provides a typed HTTP client for the Gluetun control API.
//
// Gluetun exposes a control API on port 8000 (separate from the VPN tunnel).
// Optional HTTP Basic Auth is supported when GLUETUN_HTTP_PASS is set.
package gluetun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"pelicula-api/internal/httpx"
)

const defaultTimeout = 5 * time.Second

// VPNStatus is the combined VPN health snapshot derived from Gluetun endpoints.
type VPNStatus struct {
	PublicIP string `json:"public_ip"`
	Country  string `json:"country"`
}

// PortForward holds the forwarded port returned by the port-forwarding endpoint.
type PortForward struct {
	Port int `json:"port"`
}

// Client is a typed HTTP client for the Gluetun control API.
// It handles optional HTTP Basic Auth when username/password are set.
type Client struct {
	base     *httpx.Client
	username string
	password string
}

// New constructs a Client for the given baseURL.
// Pass username and password when GLUETUN_HTTP_PASS is configured; leave both
// empty for deployments without HTTP auth on the control API.
func New(baseURL, username, password string) *Client {
	return &Client{
		base:     httpx.New(baseURL, "", "", defaultTimeout),
		username: username,
		password: password,
	}
}

// NewWithClient constructs a Client that wraps an existing *httpx.Client.
func NewWithClient(base *httpx.Client, username, password string) *Client {
	return &Client{base: base, username: username, password: password}
}

// get performs a GET against path with optional Basic Auth. Returns raw bytes.
func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base.BaseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if c.password != "" {
		req.SetBasicAuth(c.username, c.password)
	}
	hc := c.base.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, path)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}

// GetPublicIP fetches the current public IP and country from Gluetun.
func (c *Client) GetPublicIP(ctx context.Context) (*VPNStatus, error) {
	body, err := c.get(ctx, "/v1/publicip/ip")
	if err != nil {
		return nil, err
	}
	var out VPNStatus
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse public IP response: %w", err)
	}
	return &out, nil
}

// GetForwardedPort fetches the current port-forwarding status from Gluetun.
func (c *Client) GetForwardedPort(ctx context.Context) (*PortForward, error) {
	body, err := c.get(ctx, "/v1/openvpn/portforwarded")
	if err != nil {
		return nil, err
	}
	var out PortForward
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse port forward response: %w", err)
	}
	return &out, nil
}
