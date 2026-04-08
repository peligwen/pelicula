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

// validJellyfinID returns true when id looks like a Jellyfin user ID.
// Jellyfin returns IDs as 32-char dashless hex strings over the API, but also
// accepts the 36-char dashed UUID form. Both are allowed here to guard against
// path traversal — only hex digits (and dashes in the right positions) pass.
func validJellyfinID(id string) bool {
	switch len(id) {
	case 32:
		// Dashless hex: Jellyfin's actual wire format from /Users.
		for _, c := range id {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
		return true
	case 36:
		// Dashed UUID form: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
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
	default:
		return false
	}
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

	// Step 2: set admin user name and password.
	// Jellyfin 10.11+ changed /Startup/User to update an auto-created initial user
	// rather than creating one from scratch. The user is initialized lazily — a GET
	// to /Startup/User triggers the creation; only then does POST succeed.
	pass := os.Getenv("JELLYFIN_PASSWORD")
	if pass == "" {
		slog.Info("creating Jellyfin admin user with no password — set JELLYFIN_PASSWORD in .env for Jellyfin 10.11+", "component", "autowire")
	} else {
		slog.Info("creating Jellyfin admin user with configured password", "component", "autowire")
	}
	// GET first to trigger lazy user initialization (Jellyfin 10.11+).
	if _, err = jellyfinGet(s, "/Startup/User", ""); err != nil {
		slog.Warn("could not fetch initial Jellyfin startup user", "component", "autowire", "error", err)
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

	slog.Info("Jellyfin wizard completed", "component", "autowire")
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
	IsAdmin       bool   `json:"isAdmin"`
	LastLoginDate string `json:"lastLoginDate,omitempty"`
}

// JellyfinSession is an active or recent Jellyfin session, for the now-playing card.
type JellyfinSession struct {
	UserName         string `json:"userName"`
	DeviceName       string `json:"deviceName"`
	Client           string `json:"client"`
	LastActivityDate string `json:"lastActivityDate,omitempty"`
	NowPlayingTitle  string `json:"nowPlayingTitle,omitempty"`
	NowPlayingType   string `json:"nowPlayingType,omitempty"`
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
		isAdmin := false
		if policy, ok := u["Policy"].(map[string]any); ok {
			isAdmin, _ = policy["IsAdministrator"].(bool)
		}
		users = append(users, JellyfinUser{
			ID:            id,
			Name:          name,
			HasPassword:   hasPass,
			IsAdmin:       isAdmin,
			LastLoginDate: lastLogin,
		})
	}
	return users, nil
}

// jellyfinMessage extracts a user-facing message from a Jellyfin error body.
// Jellyfin error responses typically carry {"Message":"..."} — we surface that
// so the admin sees something actionable rather than just an HTTP status code.
func jellyfinMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var v struct {
		Message string `json:"Message"`
	}
	if json.Unmarshal(body, &v) == nil && v.Message != "" {
		if len(v.Message) > 120 {
			return v.Message[:120]
		}
		return v.Message
	}
	return ""
}

// DeleteJellyfinUser deletes a Jellyfin user by ID.
func DeleteJellyfinUser(s *ServiceClients, id string) error {
	if !validJellyfinID(id) {
		return fmt.Errorf("invalid user ID format: %q", id)
	}
	token, err := jellyfinAuth(s)
	if err != nil {
		return fmt.Errorf("auth failed: %w", err)
	}
	if _, err := jellyfinDelete(s, "/Users/"+id, token); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	slog.Info("deleted Jellyfin user", "component", "jellyfin", "userId", id)
	return nil
}

// SetJellyfinUserPassword sets a new password for a Jellyfin user.
// Called with an admin token so CurrentPw is not required.
func SetJellyfinUserPassword(s *ServiceClients, id, newPw string) error {
	if newPw == "" {
		return ErrPasswordRequired
	}
	if len(newPw) > 256 {
		return fmt.Errorf("password too long (max 256 chars)")
	}
	if !validJellyfinID(id) {
		return fmt.Errorf("invalid user ID format: %q", id)
	}
	token, err := jellyfinAuth(s)
	if err != nil {
		return fmt.Errorf("auth failed: %w", err)
	}
	body, err := jellyfinPost(s, "/Users/"+id+"/Password", token, map[string]any{
		"NewPw": newPw,
	})
	if err != nil {
		msg := "set password failed"
		if detail := jellyfinMessage(body); detail != "" {
			msg += ": " + detail
		}
		return fmt.Errorf("%s: %w", msg, err)
	}
	slog.Info("reset Jellyfin user password", "component", "jellyfin", "userId", id)
	return nil
}

// GetJellyfinSessions returns active/recent Jellyfin sessions for the now-playing card.
func GetJellyfinSessions(s *ServiceClients) ([]JellyfinSession, error) {
	token, err := jellyfinAuth(s)
	if err != nil {
		return nil, fmt.Errorf("auth failed: %w", err)
	}
	data, err := jellyfinGet(s, "/Sessions", token)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse sessions: %w", err)
	}
	sessions := make([]JellyfinSession, 0, len(raw))
	for _, r := range raw {
		userName, _ := r["UserName"].(string)
		if userName == "" {
			continue // skip system/device sessions with no user
		}
		deviceName, _ := r["DeviceName"].(string)
		client, _ := r["Client"].(string)
		lastActivity, _ := r["LastActivityDate"].(string)
		sess := JellyfinSession{
			UserName:         userName,
			DeviceName:       deviceName,
			Client:           client,
			LastActivityDate: lastActivity,
		}
		if npi, ok := r["NowPlayingItem"].(map[string]any); ok {
			sess.NowPlayingTitle, _ = npi["Name"].(string)
			sess.NowPlayingType, _ = npi["Type"].(string)
		}
		sessions = append(sessions, sess)
	}
	return sessions, nil
}

// CreateJellyfinUser creates a new Jellyfin user with the given name and password.
// password must be non-empty. Returns the new user's Jellyfin ID on success.
func CreateJellyfinUser(s *ServiceClients, username, password string) (string, error) {
	if password == "" {
		return "", ErrPasswordRequired
	}
	if len(password) > 256 {
		return "", fmt.Errorf("password too long (max 256 chars)")
	}
	token, err := jellyfinAuth(s)
	if err != nil {
		return "", fmt.Errorf("auth failed: %w", err)
	}
	// Create the user account
	data, err := jellyfinPost(s, "/Users/New", token, map[string]any{"Name": username})
	if err != nil {
		return "", fmt.Errorf("create user: %w", err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("parse create response: %w", err)
	}
	id, _ := result["Id"].(string)
	if id == "" {
		return "", fmt.Errorf("no user ID in create response")
	}
	// Validate the ID is a UUID before embedding it in a URL path.
	// Jellyfin always returns UUIDs; anything else is malformed or adversarial.
	if !validJellyfinID(id) {
		return "", fmt.Errorf("unexpected user ID format from Jellyfin: %q", id)
	}
	// Set the password. If this fails, attempt to delete the user so the admin
	// isn't left with a passwordless account they can't see from Pelicula.
	pwBody, err := jellyfinPost(s, "/Users/"+id+"/Password", token, map[string]any{
		"CurrentPw": "",
		"NewPw":     password,
	})
	if err != nil {
		detail := jellyfinMessage(pwBody)
		msg := "set password failed"
		if detail != "" {
			msg += ": " + detail
		}
		if _, delErr := jellyfinDelete(s, "/Users/"+id, token); delErr != nil {
			slog.Warn("password set failed and rollback delete also failed", "component", "jellyfin", "userId", id, "deleteError", delErr)
			return "", fmt.Errorf("%s (rollback failed — delete user %q manually in Jellyfin): %w", msg, username, err)
		}
		return "", fmt.Errorf("%s (user was removed): %w", msg, err)
	}
	slog.Info("created Jellyfin user", "component", "jellyfin", "username", username)
	return id, nil
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

// jellyfinDo is the shared implementation for all Jellyfin HTTP calls.
// For POST with a payload, pass the body as payload (JSON-encoded); for GET/DELETE pass nil.
func jellyfinDo(s *ServiceClients, method, path, token string, payload any) ([]byte, error) {
	var bodyReader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, jellyfinURL+path, bodyReader)
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

// jellyfinGet makes a GET request to Jellyfin with the Emby authorization header.
func jellyfinGet(s *ServiceClients, path, token string) ([]byte, error) {
	return jellyfinDo(s, "GET", path, token, nil)
}

// jellyfinPost makes a POST request to Jellyfin with the Emby authorization header.
func jellyfinPost(s *ServiceClients, path, token string, payload any) ([]byte, error) {
	return jellyfinDo(s, "POST", path, token, payload)
}

// jellyfinDelete makes a DELETE request to Jellyfin with the Emby authorization header.
func jellyfinDelete(s *ServiceClients, path, token string) ([]byte, error) {
	return jellyfinDo(s, "DELETE", path, token, nil)
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
	// create accounts without credentials.
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
		if _, err := CreateJellyfinUser(services, req.Username, req.Password); err != nil {
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

// handleUsersWithID dispatches requests to /api/pelicula/users/{id} and
// /api/pelicula/users/{id}/password based on path suffix and HTTP method.
func handleUsersWithID(w http.ResponseWriter, r *http.Request) {
	// Mutations require auth to be enabled (same guard as handleUsers POST).
	if authMiddleware != nil && authMiddleware.IsOffMode() {
		writeError(w, "user management requires PELICULA_AUTH to be enabled", http.StatusForbidden)
		return
	}
	// CSRF guard for all methods (this handler has no safe read-only calls).
	if origin := r.Header.Get("Origin"); origin != "" && !isLocalOrigin(origin) {
		writeError(w, "forbidden", http.StatusForbidden)
		return
	}

	// Strip the route prefix to get "{id}" or "{id}/password".
	tail := strings.TrimPrefix(r.URL.Path, "/api/pelicula/users/")

	if strings.HasSuffix(tail, "/password") {
		id := strings.TrimSuffix(tail, "/password")
		if !validJellyfinID(id) {
			writeError(w, "invalid user ID", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		handleUserPassword(w, r, id)
		return
	}

	if !validJellyfinID(tail) {
		writeError(w, "invalid user ID", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodDelete:
		handleUserDelete(w, r, tail)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleUserDelete handles DELETE /api/pelicula/users/{id}.
// It prevents deletion of the last admin account.
func handleUserDelete(w http.ResponseWriter, r *http.Request, id string) {
	users, err := ListJellyfinUsers(services)
	if err != nil {
		slog.Error("list users for delete check failed", "component", "users", "error", err)
		writeError(w, "could not verify user before deletion", http.StatusBadGateway)
		return
	}
	var target *JellyfinUser
	adminCount := 0
	for i := range users {
		if users[i].ID == id {
			target = &users[i]
		}
		if users[i].IsAdmin {
			adminCount++
		}
	}
	if target == nil {
		writeError(w, "user not found", http.StatusNotFound)
		return
	}
	if target.IsAdmin && adminCount <= 1 {
		writeError(w, "cannot delete the only admin account", http.StatusConflict)
		return
	}
	if err := DeleteJellyfinUser(services, id); err != nil {
		slog.Error("delete jellyfin user failed", "component", "users", "userId", id, "error", err)
		writeError(w, "could not delete user", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUserPassword handles POST /api/pelicula/users/{id}/password.
// Resets the user's password; no current password required (admin operation).
func handleUserPassword(w http.ResponseWriter, r *http.Request, id string) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB cap
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := SetJellyfinUserPassword(services, id, req.Password); err != nil {
		slog.Error("reset password failed", "component", "users", "userId", id, "error", err)
		if errors.Is(err, ErrPasswordRequired) {
			writeError(w, "password is required", http.StatusBadRequest)
			return
		}
		var jErr *jellyfinHTTPError
		if errors.As(err, &jErr) && jErr.StatusCode == http.StatusBadRequest {
			writeError(w, "could not set password: invalid or rejected by Jellyfin", http.StatusBadRequest)
			return
		}
		writeError(w, "could not set password", http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSessions handles GET /api/pelicula/sessions.
// Returns active Jellyfin sessions for the now-playing dashboard card.
func handleSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sessions, err := GetJellyfinSessions(services)
	if err != nil {
		slog.Error("list sessions failed", "component", "sessions", "error", err)
		writeError(w, "could not list sessions", http.StatusBadGateway)
		return
	}
	writeJSON(w, sessions)
}
