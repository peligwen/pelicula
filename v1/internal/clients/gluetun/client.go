// Package gluetun is a client for gluetun's HTTP control server (port 8000).
package gluetun

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"pelicula/internal/httpx"
)

const defaultTimeout = 5 * time.Second

// VPNStatus is the tunnel's public identity as reported by gluetun.
type VPNStatus struct {
	PublicIP string `json:"public_ip"`
	Country  string `json:"country"`
	City     string `json:"city"`
}

// Client talks to one gluetun control server.
type Client struct {
	base *httpx.Client
}

// New returns a client for baseURL. When password is non-empty every request
// carries HTTP Basic auth; leave it empty if the control API is open.
func New(baseURL, username, password string) *Client {
	c := httpx.New(baseURL, "", "", defaultTimeout)
	if password != "" {
		c.KeyHeader = "Authorization"
		c.KeyScheme = "Basic"
		c.SetAPIKey(base64.StdEncoding.EncodeToString([]byte(username + ":" + password)))
	}
	return &Client{base: c}
}

// Ping checks that the control server answers.
func (c *Client) Ping(ctx context.Context) error {
	return c.base.Probe(ctx, "/v1/vpn/status")
}

// GetPublicIP returns the tunnel's public IP and location.
func (c *Client) GetPublicIP(ctx context.Context) (*VPNStatus, error) {
	var out VPNStatus
	if err := c.base.GetJSON(ctx, "/v1/publicip/ip", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetForwardedPort returns the VPN-forwarded port, or 0 while none is assigned.
func (c *Client) GetForwardedPort(ctx context.Context) (int, error) {
	var out struct {
		Port int `json:"port"`
	}
	if err := c.base.GetJSON(ctx, "/v1/portforward", &out); err != nil {
		return 0, err
	}
	return out.Port, nil
}

// GetTunnelStatus returns the tunnel state, e.g. "running" or "stopped".
//
// It uses /v1/vpn/status, the protocol-agnostic route. The legacy
// /v1/openvpn/status reports the OpenVPN loop only, so WireGuard setups (the
// default here) would read "stopped" even with the tunnel up.
func (c *Client) GetTunnelStatus(ctx context.Context) (string, error) {
	var out struct {
		Status string `json:"status"`
	}
	if err := c.base.GetJSON(ctx, "/v1/vpn/status", &out); err != nil {
		return "", fmt.Errorf("tunnel status: %w", err)
	}
	return out.Status, nil
}
