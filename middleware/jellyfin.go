package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
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
		log.Printf("[autowire] Jellyfin: not reachable, skipping auto-config: %v", err)
		return
	}

	var info map[string]any
	if json.Unmarshal(data, &info) != nil {
		log.Printf("[autowire] Jellyfin: could not parse system info")
		return
	}

	wizardDone, _ := info["StartupWizardCompleted"].(bool)

	if !wizardDone {
		if err := completeJellyfinWizard(s); err != nil {
			log.Printf("[autowire] Jellyfin: wizard setup failed: %v", err)
			return
		}
		// Give Jellyfin a moment to settle after wizard completion
		time.Sleep(2 * time.Second)
	} else {
		log.Println("[autowire] Jellyfin: startup wizard already completed")
	}

	// Authenticate to get an API token
	token, err := jellyfinAuth(s)
	if err != nil {
		log.Printf("[autowire] Jellyfin: auth failed, skipping library setup (%v)", err)
		log.Println("[autowire] Jellyfin: if you set a password, add libraries manually via the Jellyfin UI")
		return
	}

	wireJellyfinLibrary(s, token, "Movies", "movies", "/data/movies")
	wireJellyfinLibrary(s, token, "TV Shows", "tvshows", "/data/tv")
}

func completeJellyfinWizard(s *ServiceClients) error {
	log.Println("[autowire] Jellyfin: completing startup wizard...")

	// Step 1: initial config
	_, err := jellyfinPost(s, "/Startup/Configuration", "", map[string]any{
		"UICulture":          "en-US",
		"MetadataCountryCode": "US",
	})
	if err != nil {
		return fmt.Errorf("set startup config: %w", err)
	}

	// Step 2: create admin user with no password (matches stack's no-auth-by-default)
	_, err = jellyfinPost(s, "/Startup/User", "", map[string]any{
		"Name":     "admin",
		"Password": "",
	})
	if err != nil {
		return fmt.Errorf("create admin user: %w", err)
	}

	// Step 3: mark wizard done
	_, err = jellyfinPost(s, "/Startup/Complete", "", nil)
	if err != nil {
		return fmt.Errorf("complete wizard: %w", err)
	}

	log.Println("[autowire] Jellyfin: wizard completed (admin user, no password)")
	return nil
}

func jellyfinAuth(s *ServiceClients) (string, error) {
	data, err := jellyfinPost(s, "/Users/AuthenticateByName", "", map[string]any{
		"Username": "admin",
		"Pw":       "",
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

	log.Println("[autowire] Jellyfin: authenticated as admin")
	return token, nil
}

func wireJellyfinLibrary(s *ServiceClients, token, name, collectionType, path string) {
	// Check existing libraries
	data, err := jellyfinGet(s, "/Library/VirtualFolders", token)
	if err != nil {
		log.Printf("[autowire] Jellyfin: failed to list libraries: %v", err)
		return
	}

	var libraries []map[string]any
	if json.Unmarshal(data, &libraries) == nil {
		for _, lib := range libraries {
			if n, _ := lib["Name"].(string); n == name {
				log.Printf("[autowire] Jellyfin: library %q already exists, skipping", name)
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
		log.Printf("[autowire] Jellyfin: failed to create library %q: %v", name, err)
		return
	}

	log.Printf("[autowire] Jellyfin: added library %q → %s", name, path)
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
		return body, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
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
		return body, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
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
