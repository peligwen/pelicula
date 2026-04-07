package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"
)

// jellyfinURL is a var (not const) so tests can point it at an httptest.Server.
var jellyfinURL = "http://jellyfin:8096/jellyfin"

const embyAuthHeader = `MediaBrowser Client="Pelicula", Device="pelicula-api", DeviceId="pelicula-autowire", Version="1.0"`

// ErrPasswordRequired is returned by CreateJellyfinUser when password is empty.
var ErrPasswordRequired = errors.New("password is required")

// jellyfinHTTPError captures the HTTP status code from a Jellyfin API response.
type jellyfinHTTPError struct {
	StatusCode int
}

func (e *jellyfinHTTPError) Error() string { return fmt.Sprintf("HTTP %d", e.StatusCode) }

// validJellyfinID returns true when id looks like a Jellyfin UUID
// ("xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"). Validates before building URL paths
// so a malicious or malformed Id cannot introduce path traversal.
func validJellyfinID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// validUsername returns true when the name is safe to send to Jellyfin:
// 1–64 chars, no leading/trailing whitespace, no control chars, no / or \.
func validUsername(s string) bool {
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

// JellyfinUser is a minimal representation of a Jellyfin user for the dashboard.
type JellyfinUser struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	HasPassword   bool   `json:"hasPassword"`
	LastLoginDate string `json:"lastLoginDate,omitempty"`
}

// ListJellyfinUsers returns all non-system Jellyfin users.
func ListJellyfinUsers(s *ServiceClients) ([]JellyfinUser, error) {
	token, err := jellyfinAuth(s)
	if err != nil {
		return nil, fmt.Errorf("auth failed: %w", err)
	}
	data, err := jellyfinGet(s, "/Users", token)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse users: %w", err)
	}
	users := make([]JellyfinUser, 0, len(raw))
	for _, u := range raw {
		name, _ := u["Name"].(string)
		id, _ := u["Id"].(string)
		hasPass, _ := u["HasPassword"].(bool)
		lastLogin, _ := u["LastLoginDate"].(string)
		users = append(users, JellyfinUser{
			ID:            id,
			Name:          name,
			HasPassword:   hasPass,
			LastLoginDate: lastLogin,
		})
	}
	return users, nil
}

// CreateJellyfinUser creates a new Jellyfin user with the given name and password.
// password must be non-empty; Jellyfin users without passwords should not be created
// via this API since they would have unrestricted access to Jellyseerr.
func CreateJellyfinUser(s *ServiceClients, username, password string) error {
	if password == "" {
		return ErrPasswordRequired
	}
	if len(password) > 256 {
		return fmt.Errorf("password too long (max 256 chars)")
	}
	token, err := jellyfinAuth(s)
	if err != nil {
		return fmt.Errorf("auth failed: %w", err)
	}
	// Create the user account
	data, err := jellyfinPost(s, "/Users/New", token, map[string]any{"Name": username})
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("parse create response: %w", err)
	}
	id, _ := result["Id"].(string)
	if id == "" {
		return fmt.Errorf("no user ID in create response")
	}
	// Validate the ID is a UUID before embedding it in a URL path.
	// Jellyfin always returns UUIDs; anything else is malformed or adversarial.
	if !validJellyfinID(id) {
		return fmt.Errorf("unexpected user ID format from Jellyfin: %q", id)
	}
	// Set the password. If this fails, attempt to delete the user so the admin
	// isn't left with a passwordless account they can't see from Pelicula.
	_, err = jellyfinPost(s, "/Users/"+id+"/Password", token, map[string]any{
		"CurrentPw": "",
		"NewPw":     password,
	})
	if err != nil {
		if _, delErr := jellyfinDelete(s, "/Users/"+id, token); delErr != nil {
			slog.Warn("password set failed and rollback delete also failed", "component", "jellyfin", "userId", id, "deleteError", delErr)
			return fmt.Errorf("set password failed (rollback failed — delete user %q manually in Jellyfin): %w", username, err)
		}
		return fmt.Errorf("set password failed (user was removed): %w", err)
	}
	slog.Info("created Jellyfin user", "component", "jellyfin", "username", username)
	return nil
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
		return body, &jellyfinHTTPError{resp.StatusCode}
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
		return body, &jellyfinHTTPError{resp.StatusCode}
	}
	return body, nil
}

// jellyfinDelete makes a DELETE request to Jellyfin with the Emby authorization header.
func jellyfinDelete(s *ServiceClients, path, token string) ([]byte, error) {
	req, err := http.NewRequest("DELETE", jellyfinURL+path, nil)
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
		return body, &jellyfinHTTPError{resp.StatusCode}
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

// handleUsers handles GET /api/pelicula/users (list) and POST /api/pelicula/users (create).
func handleUsers(w http.ResponseWriter, r *http.Request) {
	// Block state-mutating requests when auth is off: anyone on the network could
	// create Jellyfin accounts (immediately usable via Jellyseerr) without credentials.
	// Read-only GET is fine in off mode since the dashboard uses it for display only.
	if r.Method != http.MethodGet && authMiddleware != nil && authMiddleware.IsOffMode() {
		writeError(w, "user management requires PELICULA_AUTH to be enabled", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodGet:
		users, err := ListJellyfinUsers(services)
		if err != nil {
			slog.Error("list jellyfin users failed", "component", "users", "error", err)
			writeError(w, "could not list users", http.StatusBadGateway)
			return
		}
		writeJSON(w, users)

	case http.MethodPost:
		// CSRF guard: reject cross-origin requests (mirrors handleSettingsUpdate).
		if origin := r.Header.Get("Origin"); origin != "" && !isLocalOrigin(origin) {
			writeError(w, "forbidden", http.StatusForbidden)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB cap
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if !validUsername(req.Username) {
			if req.Username == "" {
				writeError(w, "username is required", http.StatusBadRequest)
			} else {
				writeError(w, "username is invalid (1–64 chars, no leading/trailing whitespace, no control chars or slashes)", http.StatusBadRequest)
			}
			return
		}
		if err := CreateJellyfinUser(services, req.Username, req.Password); err != nil {
			slog.Error("create jellyfin user failed", "component", "users", "username", req.Username, "error", err)
			if errors.Is(err, ErrPasswordRequired) {
				writeError(w, "password is required", http.StatusBadRequest)
				return
			}
			var jErr *jellyfinHTTPError
			if errors.As(err, &jErr) && jErr.StatusCode == http.StatusBadRequest {
				writeError(w, "could not create user: name already taken or invalid", http.StatusBadRequest)
				return
			}
			writeError(w, "could not create user", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusCreated)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
