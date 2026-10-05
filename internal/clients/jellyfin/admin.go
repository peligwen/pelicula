package jellyfin

import (
	"context"
	"errors"
	"sync"
)

// Admin caches the Jellyfin administrator's token so server-side calls (user
// management, library refresh) do not log in every time. It logs in as
// DeviceServer, a device of its own, so dashboard logins by the same
// administrator (DeviceDashboard) do not revoke the cached token.
type Admin struct {
	c        *Client
	username string
	password string

	mu    sync.Mutex
	token string
}

// NewAdmin returns an Admin that authenticates as username/password against
// c's server, as DeviceServer.
func NewAdmin(c *Client, username, password string) *Admin {
	return &Admin{c: c.ForDevice(DeviceServer), username: username, password: password}
}

// Client returns the client every call made with the admin token must go
// through. It carries DeviceServer; Jellyfin moves a token to whatever device
// id presents it, so using the token through a DeviceDashboard client would
// hand it to the dashboard device, where the next login revokes it.
func (a *Admin) Client() *Client {
	return a.c
}

// Token returns the cached token, logging in first if there is none.
func (a *Admin) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" {
		return a.token, nil
	}
	res, err := a.c.AuthenticateByName(ctx, a.username, a.password)
	if err != nil {
		return "", wrap("admin login", err)
	}
	a.token = res.Token
	return a.token, nil
}

// Invalidate drops the cached token; the next Token call logs in again.
func (a *Admin) Invalidate() {
	a.mu.Lock()
	a.token = ""
	a.mu.Unlock()
}

// WithToken runs fn with the admin token. If fn fails with ErrUnauthorized
// (the token expired or was revoked) it logs in again and retries once.
func (a *Admin) WithToken(ctx context.Context, fn func(token string) error) error {
	token, err := a.Token(ctx)
	if err != nil {
		return err
	}
	err = fn(token)
	if !errors.Is(err, ErrUnauthorized) {
		return err
	}
	a.Invalidate()
	if token, err = a.Token(ctx); err != nil {
		return err
	}
	return fn(token)
}
