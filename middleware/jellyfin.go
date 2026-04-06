package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"
)

const (
	jellyfinURL     = "http://jellyfin:8096/jellyfin"
	embyAuthHeader  = `MediaBrowser Client="Pelicula", Device="pelicula-api", DeviceId="pelicula-autowire", Version="1.0"`
)

// wireJellyfin auto-configures Jellyfin: completes the startup wizard (if needed)
// and adds Movies + TV Shows libraries pointing to the same folders used everywhere else.
func wireJellyfin(s *ServiceClients) {
	// Check if startup wizard is complete
	data, err := jellyfinGet(s, "/System/Info/Public", "")
	if err != nil {
		slog.Warn("Jellyfin not reachable, skipping auto-config", "component", "autowire", "error", err)
		return
	}

	var info map[string]any
	if json.Unmarshal(data, &info) != nil {
		slog.Error("could not parse Jellyfin system info", "component", "autowire")
		return
	}

	wizardDone, _ := info["StartupWizardCompleted"].(bool)

	if !wizardDone {
		if err := completeJellyfinWizard(s); err != nil {
			slog.Error("Jellyfin wizard setup failed", "component", "autowire", "error", err)
			return
		}
		// Give Jellyfin a moment to settle after wizard completion
		time.Sleep(2 * time.Second)
	} else {
		slog.Info("Jellyfin startup wizard already completed", "component", "autowire")
	}

	// Authenticate to get an API token
	token, err := jellyfinAuth(s)
	if err != nil {
		slog.Error("Jellyfin auth failed, skipping library setup", "component", "autowire", "error", err)
		slog.Info("if you set a Jellyfin password, add libraries manually via the Jellyfin UI", "component", "autowire")
		return
	}

	wireJellyfinLibrary(s, token, "Movies", "movies", "/data/movies")
	wireJellyfinLibrary(s, token, "TV Shows", "tvshows", "/data/tv")
}

func completeJellyfinWizard(s *ServiceClients) error {
	slog.Info("completing Jellyfin startup wizard", "component", "autowire")

	// Step 1: initial config
	_, err := jellyfinPost(s, "/Startup/Configuration", "", map[string]any{
		"UICulture":          "en-US",
		"MetadataCountryCode": "US",
	})
	if err != nil {
		return fmt.Errorf("set startup config: %w", err)
	}

	// Step 2: create admin user. Uses JELLYFIN_PASSWORD if set; defaults to no password.
	// Note: Jellyfin 10.11+ requires a non-empty password via this endpoint.
	// Set JELLYFIN_PASSWORD in .env if the default empty password is rejected.
	pass := os.Getenv("JELLYFIN_PASSWORD")
	if pass == "" {
		slog.Info("creating Jellyfin admin user with no password — set JELLYFIN_PASSWORD in .env for Jellyfin 10.11+", "component", "autowire")
	} else {
		slog.Info("creating Jellyfin admin user with configured password", "component", "autowire")
	}
	_, err = jellyfinPost(s, "/Startup/User", "", map[string]any{
		"Name":     "admin",
		"Password": pass,
	})
	if err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}

	// Step 3: mark wizard done
	_, err = jellyfinPost(s, "/Startup/Complete", "", nil)
	if err != nil {
		return fmt.Errorf("complete wizard: %w", err)
	}

	slog.Info("Jellyfin wizard completed (admin user, no password)", "component", "autowire")
	return nil
}

func jellyfinAuth(s *ServiceClients) (string, error) {
	data, err := jellyfinPost(s, "/Users/AuthenticateByName", "", map[string]any{
		"Username": "admin",
		"Pw":       os.Getenv("JELLYFIN_PASSWORD"),
	})
	if err != nil {
		return "", err
	}

	var result map[string]any
	if json.Unmarshal(data, &result) != nil {
		return "", fmt.Errorf("invalid auth response")
	}

	token, _ := result["AccessToken"].(string)
	if token == "" {
		return "", fmt.Errorf("no access token in response")
	}

	slog.Info("Jellyfin authenticated as admin", "component", "autowire")
	return token, nil
}

func wireJellyfinLibrary(s *ServiceClients, token, name, collectionType, path string) {
	// Check existing libraries
	data, err := jellyfinGet(s, "/Library/VirtualFolders", token)
	if err != nil {
		slog.Error("failed to list Jellyfin libraries", "component", "autowire", "error", err)
		return
	}

	var libraries []map[string]any
	if json.Unmarshal(data, &libraries) == nil {
		for _, lib := range libraries {
			if n, _ := lib["Name"].(string); n == name {
				slog.Info("Jellyfin library already exists, skipping", "component", "autowire", "library", name)
				return
			}
		}
	}

	// Create library with media path
	endpoint := fmt.Sprintf("/Library/VirtualFolders?name=%s&collectionType=%s&refreshLibrary=false",
		url.QueryEscape(name), url.QueryEscape(collectionType))

	body := map[string]any{
		"LibraryOptions": map[string]any{
			"PathInfos": []map[string]any{
				{"Path": path},
			},
		},
	}

	_, err = jellyfinPost(s, endpoint, token, body)
	if err != nil {
		slog.Error("failed to create Jellyfin library", "component", "autowire", "library", name, "error", err)
		return
	}

	slog.Info("added Jellyfin library", "component", "autowire", "library", name, "path", path)
}

// TriggerLibraryRefresh asks Jellyfin to scan all libraries.
// Called by the middleware's /api/pelicula/jellyfin/refresh endpoint (invoked by Procula).
func TriggerLibraryRefresh(s *ServiceClients) error {
	token, err := jellyfinAuth(s)
	if err != nil {
		return fmt.Errorf("auth failed: %w", err)
	}
	_, err = jellyfinPost(s, "/Library/Refresh", token, nil)
	if err != nil {
		return fmt.Errorf("refresh failed: %w", err)
	}
	slog.Info("library refresh triggered", "component", "jellyfin")
	return nil
}

// jellyfinGet makes a GET request to Jellyfin with the Emby authorization header.
func jellyfinGet(s *ServiceClients, path, token string) ([]byte, error) {
	req, err := http.NewRequest("GET", jellyfinURL+path, nil)
	if err != nil {
		return nil, err
	}
	setEmbyAuth(req, token)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return body, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// jellyfinPost makes a POST request to Jellyfin with the Emby authorization header.
func jellyfinPost(s *ServiceClients, path, token string, payload any) ([]byte, error) {
	var bodyReader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(data)
	}

	req, err := http.NewRequest("POST", jellyfinURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	setEmbyAuth(req, token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return body, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func setEmbyAuth(req *http.Request, token string) {
	auth := embyAuthHeader
	if token != "" {
		auth += fmt.Sprintf(`, Token="%s"`, token)
	}
	req.Header.Set("X-Emby-Authorization", auth)
}
