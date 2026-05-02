package clients

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// JellyfinLoginResult holds the fields Peligrosa needs after authenticating a
// user against Jellyfin's /Users/AuthenticateByName endpoint.
type JellyfinLoginResult struct {
	UserID          string
	Username        string
	IsAdministrator bool
	AccessToken     string
}

// JellyfinClient is the subset of Jellyfin operations that peligrosa-scope
// code needs. A concrete *jellyfinHTTPClient wraps the existing package-level
// helpers. Consumers depend on this interface so peligrosa can live in a
// subpackage without importing the main package.
type JellyfinClient interface {
	AuthenticateByName(ctx context.Context, username, password string) (*JellyfinLoginResult, error)
	CreateUser(ctx context.Context, username, password string) (string, error)
}

// ErrPasswordRequired is returned by CreateUser when password is empty.
var ErrPasswordRequired = errors.New("password is required")

// JellyfinHTTPError captures the HTTP status code from a Jellyfin API response.
// Both the main package (jellyfin.go) and peligrosa use this to distinguish
// e.g. 401 Unauthorized from 400 Bad Request in Jellyfin responses.
type JellyfinHTTPError struct {
	StatusCode int
}

func (e *JellyfinHTTPError) Error() string { return fmt.Sprintf("HTTP %d", e.StatusCode) }

// IsValidUsername reports whether the name is safe to send to Jellyfin:
// 1–64 chars, no leading/trailing whitespace, no control chars, no / or \.
// Mirrors validUsername in the main package so peligrosa can validate without
// importing package main.
func IsValidUsername(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	if strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}
