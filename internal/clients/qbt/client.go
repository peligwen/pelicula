// Package qbt is a client for the qBittorrent v5 Web API.
//
// qBittorrent shares gluetun's network namespace and trusts the Docker subnet
// (whitelist seeded by the CLI), so no login is needed. v5 renamed
// pause/resume to stop/start; this client uses the v5 endpoints.
package qbt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"pelicula/internal/httpx"
)

const defaultTimeout = 10 * time.Second

// Torrent is one entry of /api/v2/torrents/info.
type Torrent struct {
	Hash     string  `json:"hash"`
	Name     string  `json:"name"`
	State    string  `json:"state"`
	Category string  `json:"category"`
	Progress float64 `json:"progress"` // 0..1
	Dlspeed  int64   `json:"dlspeed"`  // bytes/s
	Upspeed  int64   `json:"upspeed"`  // bytes/s
	Eta      int64   `json:"eta"`      // seconds; 8640000 means unknown
	Size     int64   `json:"size"`     // bytes
	AddedOn  int64   `json:"added_on"` // unix seconds
}

// TransferInfo is the global speed summary from /api/v2/transfer/info.
type TransferInfo struct {
	DlSpeed int64 `json:"dl_info_speed"`
	UpSpeed int64 `json:"up_info_speed"`
}

// Client talks to one qBittorrent instance.
type Client struct {
	base *httpx.Client
}

// New returns a client for baseURL, e.g. http://gluetun:8080.
func New(baseURL string) *Client {
	return &Client{base: httpx.New(baseURL, "", "", defaultTimeout)}
}

// Ping checks that the Web API answers.
func (c *Client) Ping(ctx context.Context) error {
	return c.base.Probe(ctx, "/api/v2/app/version")
}

func (c *Client) ListTorrents(ctx context.Context) ([]Torrent, error) {
	var out []Torrent
	if err := c.base.GetJSON(ctx, "/api/v2/torrents/info", &out); err != nil {
		return nil, fmt.Errorf("list torrents: %w", err)
	}
	return out, nil
}

func (c *Client) GetTransferInfo(ctx context.Context) (*TransferInfo, error) {
	var out TransferInfo
	if err := c.base.GetJSON(ctx, "/api/v2/transfer/info", &out); err != nil {
		return nil, fmt.Errorf("transfer info: %w", err)
	}
	return &out, nil
}

// StopTorrent pauses a torrent (v5 "stop").
func (c *Client) StopTorrent(ctx context.Context, hash string) error {
	return c.form(ctx, "/api/v2/torrents/stop", url.Values{"hashes": {hash}})
}

// StartTorrent resumes a torrent (v5 "start").
func (c *Client) StartTorrent(ctx context.Context, hash string) error {
	return c.form(ctx, "/api/v2/torrents/start", url.Values{"hashes": {hash}})
}

// DeleteTorrent removes a torrent, and its downloaded files when deleteFiles is set.
func (c *Client) DeleteTorrent(ctx context.Context, hash string, deleteFiles bool) error {
	return c.form(ctx, "/api/v2/torrents/delete", url.Values{
		"hashes":      {hash},
		"deleteFiles": {strconv.FormatBool(deleteFiles)},
	})
}

// GetListenPort returns the incoming connection port from preferences.
func (c *Client) GetListenPort(ctx context.Context) (int, error) {
	var prefs struct {
		ListenPort int `json:"listen_port"`
	}
	if err := c.base.GetJSON(ctx, "/api/v2/app/preferences", &prefs); err != nil {
		return 0, fmt.Errorf("get preferences: %w", err)
	}
	return prefs.ListenPort, nil
}

// SetListenPort changes the incoming connection port.
func (c *Client) SetListenPort(ctx context.Context, port int) error {
	prefs, err := json.Marshal(map[string]int{"listen_port": port})
	if err != nil {
		return err
	}
	return c.form(ctx, "/api/v2/app/setPreferences", url.Values{"json": {string(prefs)}})
}

// form sends a form-encoded POST; every qBittorrent mutation uses one.
func (c *Client) form(ctx context.Context, path string, v url.Values) error {
	_, err := c.base.PostForm(ctx, path, v)
	return err
}
