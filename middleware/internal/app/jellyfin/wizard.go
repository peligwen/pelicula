package jellyfin

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	jfclient "pelicula-api/internal/clients/jellyfin"
)

const ServiceUser = "pelicula-internal"

// Wizard holds the dependencies for completing the Jellyfin startup wizard and
// auto-wiring libraries.
type Wizard struct {
	Client    *jfclient.Client
	GenAPIKey func() string // generates a random key string (e.g. generateAPIKey from main)
}

// CompleteWizard runs the Jellyfin startup wizard and returns a session token
// obtained by authenticating as the service account. The throwaway password
// is generated in memory and never written to disk.
func (wiz *Wizard) CompleteWizard() (string, error) {
	slog.Info("completing Jellyfin startup wizard", "component", "autowire")

	// Step 1: initial config
	_, err := wiz.Client.Post("/Startup/Configuration", "", map[string]any{
		"UICulture":           "en-US",
		"MetadataCountryCode": "US",
	})
	if err != nil {
		return "", fmt.Errorf("set startup config: %w", err)
	}

	// Step 2: set admin user name and password.
	// Jellyfin 10.11+ lazily initializes the startup user; a GET triggers that
	// creation, only then does POST succeed.
	pass := wiz.GenAPIKey() // random throwaway, never stored
	adminUser := ServiceUser
	slog.Info("creating Jellyfin service account", "component", "autowire", "username", adminUser)
	if _, err = wiz.Client.Get("/Startup/User", ""); err != nil {
		slog.Warn("could not fetch initial Jellyfin startup user", "component", "autowire", "error", err)
	}
	_, err = wiz.Client.Post("/Startup/User", "", map[string]any{
		"Name":     adminUser,
		"Password": pass,
	})
	if err != nil {
		return "", fmt.Errorf("create admin user: %w", err)
	}

	// Step 3: mark wizard done
	_, err = wiz.Client.Post("/Startup/Complete", "", nil)
	if err != nil {
		return "", fmt.Errorf("complete wizard: %w", err)
	}

	slog.Info("Jellyfin wizard completed", "component", "autowire")

	// Step 4: authenticate with the throwaway password to get a session token.
	data, err := wiz.Client.Post("/Users/AuthenticateByName", "", map[string]any{
		"Username": adminUser,
		"Pw":       pass,
	})
	if err != nil {
		return "", fmt.Errorf("post-wizard auth: %w", err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("parse post-wizard auth: %w", err)
	}
	token, _ := result["AccessToken"].(string)
	if token == "" {
		return "", fmt.Errorf("empty access token from post-wizard auth")
	}
	return token, nil
}

// CreateAPIKey creates a persistent Jellyfin API key via POST /Auth/Keys.
// If a "Pelicula" key already exists it is reused to avoid duplicates on restart.
func CreateAPIKey(client *jfclient.Client, token string) (string, error) {
	// Check for an existing key first to avoid duplicates on restart.
	data, err := client.Get("/Auth/Keys", token)
	if err == nil {
		var existing struct {
			Items []struct {
				AccessToken string `json:"AccessToken"`
				AppName     string `json:"AppName"`
			} `json:"Items"`
		}
		if json.Unmarshal(data, &existing) == nil {
			for _, item := range existing.Items {
				if item.AppName == "Pelicula" {
					return item.AccessToken, nil
				}
			}
		}
	}

	// No existing key — create one. Jellyfin POST /Auth/Keys returns 204 with no body,
	// so we must fetch the key list again to get the token value.
	if _, err := client.Post("/Auth/Keys?app=Pelicula", token, nil); err != nil {
		return "", fmt.Errorf("create API key: %w", err)
	}
	data, err = client.Get("/Auth/Keys", token)
	if err != nil {
		return "", fmt.Errorf("list API keys: %w", err)
	}
	var result struct {
		Items []struct {
			AccessToken string `json:"AccessToken"`
			AppName     string `json:"AppName"`
		} `json:"Items"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("parse API keys: %w", err)
	}
	for _, item := range result.Items {
		if item.AppName == "Pelicula" {
			return item.AccessToken, nil
		}
	}
	return "", fmt.Errorf("API key not found after creation")
}

// ServiceUserID looks up the pelicula-internal user ID from Jellyfin's /Users list.
// Returns ("", nil) if the user is not found (non-fatal).
func ServiceUserID(client *jfclient.Client, token string) (string, error) {
	data, err := client.Get("/Users", token)
	if err != nil {
		return "", fmt.Errorf("list users: %w", err)
	}
	var users []map[string]any
	if err := json.Unmarshal(data, &users); err != nil {
		return "", fmt.Errorf("parse users: %w", err)
	}
	for _, u := range users {
		if name, _ := u["Name"].(string); name == ServiceUser {
			id, _ := u["Id"].(string)
			return id, nil
		}
	}
	return "", nil
}

// SystemInfo fetches /System/Info/Public and returns the raw JSON map.
func SystemInfo(client *jfclient.Client) (map[string]any, error) {
	data, err := client.Get("/System/Info/Public", "")
	if err != nil {
		return nil, err
	}
	var info map[string]any
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("parse system info: %w", err)
	}
	return info, nil
}

// TriggerLibraryRefresh asks Jellyfin to scan all libraries.
func TriggerLibraryRefresh(client *jfclient.Client, auth func() (string, error)) error {
	token, err := auth()
	if err != nil {
		return fmt.Errorf("auth failed: %w", err)
	}
	_, err = client.Post("/Library/Refresh", token, nil)
	if err != nil {
		return fmt.Errorf("refresh failed: %w", err)
	}
	slog.Info("library refresh triggered", "component", "jellyfin")
	return nil
}

// WizardSleep is a hook for tests to override the post-wizard sleep.
// Production code should leave this as the default 2-second sleep.
var WizardSleep = func() { time.Sleep(2 * time.Second) }
