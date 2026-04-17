// Package qbt provides a typed HTTP client for qBittorrent v5+.
//
// qBittorrent runs on gluetun's network namespace and is reached via an IP
// subnet bypass whitelist (no password needed from within the Docker network).
// All form-encoded POST endpoints follow the qBittorrent Web API v2 spec, with
// the v5 rename of pause→stop and resume→start.
package qbt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"pelicula-api/internal/httpx"
)

const defaultTimeout = 10 * time.Second

// Torrent represents a single torrent entry from /api/v2/torrents/info.
type Torrent struct {
	Hash     string  `json:"hash"`
	Name     string  `json:"name"`
	Progress float64 `json:"progress"`
	DLSpeed  int64   `json:"dlspeed"`
	UPSpeed  int64   `json:"upspeed"`
	ETA      int64   `json:"eta"`
	State    string  `json:"state"`
	Size     int64   `json:"size"`
	Category string  `json:"category"`
}

// TransferInfo represents the response from /api/v2/transfer/info.
type TransferInfo struct {
	DLSpeed int64 `json:"dl_info_speed"`
	UPSpeed int64 `json:"up_info_speed"`
}

// Client is a typed HTTP client for qBittorrent.
type Client struct {
	base *httpx.Client
}

// New constructs a Client for the given baseURL.
// No API key is required — qBittorrent is configured with a Docker-subnet bypass.
func New(baseURL string) *Client {
	return &Client{
		base: httpx.New(baseURL, "", "", defaultTimeout),
	}
}

// NewWithClient constructs a Client that shares an existing *httpx.Client.
func NewWithClient(base *httpx.Client) *Client {
	return &Client{base: base}
}

// ListTorrents returns all torrents from qBittorrent.
func (c *Client) ListTorrents(ctx context.Context) ([]Torrent, error) {
	raw, err := c.base.RawGet(ctx, "/api/v2/torrents/info")
	if err != nil {
		return nil, fmt.Errorf("list torrents: %w", err)
	}
	var out []Torrent
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse torrents: %w", err)
	}
	return out, nil
}

// GetTransferInfo returns current transfer speeds.
func (c *Client) GetTransferInfo(ctx context.Context) (*TransferInfo, error) {
	raw, err := c.base.RawGet(ctx, "/api/v2/transfer/info")
	if err != nil {
		return nil, fmt.Errorf("transfer info: %w", err)
	}
	var out TransferInfo
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse transfer info: %w", err)
	}
	return &out, nil
}

// StopTorrent pauses a torrent by hash (qBittorrent v5 "stop").
func (c *Client) StopTorrent(ctx context.Context, hash string) error {
	return c.formPost(ctx, "/api/v2/torrents/stop", url.Values{"hashes": {hash}})
}

// StartTorrent resumes a torrent by hash (qBittorrent v5 "start").
func (c *Client) StartTorrent(ctx context.Context, hash string) error {
	return c.formPost(ctx, "/api/v2/torrents/start", url.Values{"hashes": {hash}})
}

// DeleteTorrent removes a torrent and its downloaded files by hash.
func (c *Client) DeleteTorrent(ctx context.Context, hash string) error {
	return c.formPost(ctx, "/api/v2/torrents/delete", url.Values{
		"hashes":      {hash},
		"deleteFiles": {"true"},
	})
}

// formPost sends a form-encoded POST request. It exists because qBittorrent's
// Web API uses application/x-www-form-urlencoded for all mutation endpoints.
func (c *Client) formPost(ctx context.Context, path string, values url.Values) error {
	_, err := c.base.PostForm(ctx, path, values)
	return err
}
