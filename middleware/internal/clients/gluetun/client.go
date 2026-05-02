// Package gluetun provides a typed HTTP client for the Gluetun control API.
//
// Gluetun exposes a control API on port 8000 (separate from the VPN tunnel).
// Optional HTTP Basic Auth is supported when GLUETUN_HTTP_PASS is set.
package gluetun

import (
	"context"
	"encoding/json"
	"fmt"
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

// basicAuthTransport injects HTTP Basic Auth on every request when a password is
// set. Layered outside uaTransport so both User-Agent and Basic Auth flow without
// bypassing httpx.RawGet's retry-on-5xx and secret-redaction logic.
type basicAuthTransport struct {
	base http.RoundTripper
	user string
	pass string
}

func (t *basicAuthTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.pass != "" {
		r = r.Clone(r.Context())
		r.SetBasicAuth(t.user, t.pass)
	}
	return t.base.RoundTrip(r)
}

// Client is a typed HTTP client for the Gluetun control API.
type Client struct {
	base *httpx.Client
}

// New constructs a Client for the given baseURL.
// Pass username and password when GLUETUN_HTTP_PASS is configured; leave both
// empty for deployments without HTTP auth on the control API.
func New(baseURL, username, password string) *Client {
	c := httpx.New(baseURL, "", "", defaultTimeout)
	c.HTTPClient.Transport = &basicAuthTransport{
		base: c.HTTPClient.Transport,
		user: username,
		pass: password,
	}
	return &Client{base: c}
}

// NewWithClient constructs a Client that wraps an existing *httpx.Client.
func NewWithClient(base *httpx.Client, username, password string) *Client {
	base.HTTPClient.Transport = &basicAuthTransport{
		base: base.HTTPClient.Transport,
		user: username,
		pass: password,
	}
	return &Client{base: base}
}

// GetPublicIP fetches the current public IP and country from Gluetun.
func (c *Client) GetPublicIP(ctx context.Context) (*VPNStatus, error) {
	body, err := c.base.RawGet(ctx, "/v1/publicip/ip")
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
	body, err := c.base.RawGet(ctx, "/v1/openvpn/portforwarded")
	if err != nil {
		return nil, err
	}
	var out PortForward
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse port forward response: %w", err)
	}
	return &out, nil
}

// GetPortForward fetches the active port-forward assignment from Gluetun's
// port-forwarding provider endpoint (/v1/portforward). This differs from
// GetForwardedPort: the portforward endpoint is used by the VPN watchdog
// to detect ProtonVPN port assignments, while portforwarded is used for
// health/status display.
func (c *Client) GetPortForward(ctx context.Context) (int, error) {
	body, err := c.base.RawGet(ctx, "/v1/portforward")
	if err != nil {
		return 0, err
	}
	var out struct {
		Port int `json:"port"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, fmt.Errorf("parse portforward response: %w", err)
	}
	return out.Port, nil
}

// GetTunnelStatus fetches the VPN tunnel connection status from Gluetun.
// Returns a status string such as "running" or "stopped", or "" on error.
func (c *Client) GetTunnelStatus(ctx context.Context) (string, error) {
	body, err := c.base.RawGet(ctx, "/v1/openvpn/status")
	if err != nil {
		return "", err
	}
	var out struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("parse tunnel status response: %w", err)
	}
	return out.Status, nil
}
