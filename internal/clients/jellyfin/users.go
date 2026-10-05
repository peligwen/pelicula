package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// ErrUserExists is returned (wrapped) by CreateUser when the name is taken.
var ErrUserExists = errors.New("jellyfin: user already exists")

// User is a Jellyfin account.
type User struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	IsAdmin    bool   `json:"is_admin"`
	IsDisabled bool   `json:"is_disabled"`
	LastLogin  string `json:"last_login"` // RFC 3339; empty if never
}

// ListUsers returns every Jellyfin user. token must belong to an administrator.
func (c *Client) ListUsers(ctx context.Context, token string) ([]User, error) {
	body, err := c.Get(ctx, "/Users", token)
	if err != nil {
		return nil, wrap("list users", err)
	}
	var raw []struct {
		ID            string `json:"Id"`
		Name          string `json:"Name"`
		LastLoginDate string `json:"LastLoginDate"`
		Policy        struct {
			IsAdministrator bool `json:"IsAdministrator"`
			IsDisabled      bool `json:"IsDisabled"`
		} `json:"Policy"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("jellyfin: parse users: %w", err)
	}
	users := make([]User, 0, len(raw))
	for _, u := range raw {
		users = append(users, User{
			ID:         u.ID,
			Name:       u.Name,
			IsAdmin:    u.Policy.IsAdministrator,
			IsDisabled: u.Policy.IsDisabled,
			LastLogin:  u.LastLoginDate,
		})
	}
	return users, nil
}

// CreateUser creates a user with a password and returns its ID. If setting
// the password fails the new user is deleted again so no passwordless account
// is left behind. A taken name returns an error matching ErrUserExists.
func (c *Client) CreateUser(ctx context.Context, token, name, password string) (string, error) {
	if name == "" || password == "" {
		return "", errors.New("jellyfin: user name and password are required")
	}
	// Check first so a duplicate is reported reliably; Jellyfin's own
	// duplicate error is a plain-text 400.
	if existing, err := c.ListUsers(ctx, token); err == nil {
		for _, u := range existing {
			if strings.EqualFold(u.Name, name) {
				return "", fmt.Errorf("%w: %q", ErrUserExists, name)
			}
		}
	}
	body, err := c.Post(ctx, "/Users/New", token, map[string]any{"Name": name})
	if err != nil {
		var he *HTTPError
		if errors.As(err, &he) && he.StatusCode == http.StatusBadRequest &&
			strings.Contains(strings.ToLower(he.Body), "already exists") {
			return "", fmt.Errorf("%w: %q: %w", ErrUserExists, name, err)
		}
		return "", wrap("create user", err)
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(body, &created); err != nil {
		return "", fmt.Errorf("jellyfin: parse create-user response: %w", err)
	}
	if !validID(created.ID) {
		return "", fmt.Errorf("jellyfin: unexpected user id %q", created.ID)
	}
	if _, err := c.Post(ctx, "/Users/"+created.ID+"/Password", token, map[string]any{
		"CurrentPw": "",
		"NewPw":     password,
	}); err != nil {
		if _, delErr := c.Delete(ctx, "/Users/"+created.ID, token); delErr != nil {
			slog.Warn("password set failed and rollback delete failed", "component", "jellyfin",
				"user", name, "id", created.ID, "error", delErr)
			return "", wrap("set password (rollback failed, delete the user manually)", err)
		}
		return "", wrap("set password (user removed)", err)
	}
	slog.Info("created Jellyfin user", "component", "jellyfin", "user", name)
	return created.ID, nil
}

// DeleteUser removes a user by Jellyfin ID.
func (c *Client) DeleteUser(ctx context.Context, token, userID string) error {
	if !validID(userID) {
		return fmt.Errorf("jellyfin: invalid user id %q", userID)
	}
	if _, err := c.Delete(ctx, "/Users/"+userID, token); err != nil {
		return wrap("delete user", err)
	}
	slog.Info("deleted Jellyfin user", "component", "jellyfin", "id", userID)
	return nil
}

// validID accepts the 32-char hex or 36-char dashed UUID form. IDs end up in
// URL paths, so anything else is rejected.
func validID(id string) bool {
	switch len(id) {
	case 32:
		return allHex(id)
	case 36:
		return id[8] == '-' && id[13] == '-' && id[18] == '-' && id[23] == '-' &&
			allHex(id[:8]+id[9:13]+id[14:18]+id[19:23]+id[24:])
	}
	return false
}

func allHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// wrap prefixes err with what was being attempted; errors.As/Is still see
// through it. A nil err stays nil.
func wrap(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("jellyfin: %s: %w", what, err)
}
