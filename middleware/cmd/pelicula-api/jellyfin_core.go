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
	"strings"
	"time"
	"unicode"

	"pelicula-api/clients"
	"pelicula-api/internal/app/library"
)

// jellyfinURL is a var (not const) so tests can point it at an httptest.Server
// and so power users can override it via JELLYFIN_URL.
var jellyfinURL = envOr("JELLYFIN_URL", "http://jellyfin:8096/jellyfin")

const embyAuthHeader = `MediaBrowser Client="Pelicula", Device="pelicula-api", DeviceId="pelicula-autowire", Version="1.0"`

const jellyfinServiceUser = "pelicula-internal"

// ErrPasswordRequired is returned by CreateJellyfinUser when password is empty.
// Aliased from clients package so peligrosa and main both reference the same sentinel.
var ErrPasswordRequired = clients.ErrPasswordRequired

// jellyfinHTTPClient is the production implementation of clients.JellyfinClient.
// It forwards to the existing package-level helpers which already handle
// URL construction, header auth, and error wrapping.
type jellyfinHTTPClient struct {
	httpClient *http.Client
	services   *ServiceClients
}

// NewJellyfinHTTPClient returns a clients.JellyfinClient backed by the given http.Client
// (for authenticate calls) and ServiceClients (for user CRUD that needs API key auth).
func NewJellyfinHTTPClient(hc *http.Client, s *ServiceClients) clients.JellyfinClient {
	return &jellyfinHTTPClient{httpClient: hc, services: s}
}

func (c *jellyfinHTTPClient) AuthenticateByName(username, password string) (*clients.JellyfinLoginResult, error) {
	return jellyfinAuthenticateByName(c.httpClient, username, password)
}

func (c *jellyfinHTTPClient) CreateUser(username, password string) (string, error) {
	return CreateJellyfinUser(c.services, username, password)
}

// jellyfinAuthenticateByName authenticates username/password against Jellyfin.
// Uses the package-level jellyfinURL so tests can point it at an httptest.Server.
// Returns a *jellyfinHTTPError (with StatusCode 401) for bad credentials.
func jellyfinAuthenticateByName(client *http.Client, username, password string) (*clients.JellyfinLoginResult, error) {
	payload, err := json.Marshal(map[string]string{"Username": username, "Pw": password})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, jellyfinURL+"/Users/AuthenticateByName", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	setEmbyAuth(req, "")
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, &jellyfinHTTPError{StatusCode: resp.StatusCode}
	}

	var result struct {
		User struct {
			Id     string `json:"Id"`
			Name   string `json:"Name"`
			Policy struct {
				IsAdministrator bool `json:"IsAdministrator"`
			} `json:"Policy"`
		} `json:"User"`
		AccessToken string `json:"AccessToken"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("invalid Jellyfin auth response: %w", err)
	}
	if result.User.Id == "" || result.AccessToken == "" {
		return nil, fmt.Errorf("incomplete Jellyfin auth response")
	}
	return &clients.JellyfinLoginResult{
		UserID:          result.User.Id,
		Username:        result.User.Name,
		IsAdministrator: result.User.Policy.IsAdministrator,
		AccessToken:     result.AccessToken,
	}, nil
}

// jellyfinHTTPError is a package-level alias for clients.JellyfinHTTPError.
// Using the clients type ensures errors.As checks in peligrosa and in this
// package both match the same concrete type.
type jellyfinHTTPError = clients.JellyfinHTTPError

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
func wireJellyfin(s *ServiceClients, lh *library.Handler) {
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

	var token string
	if !wizardDone {
		wizardToken, wizardErr := completeJellyfinWizard(s)
		if wizardErr != nil {
			slog.Error("Jellyfin wizard setup failed", "component", "autowire", "error", wizardErr)
			return
		}
		token = wizardToken
		// Give Jellyfin a moment to settle after wizard completion
		time.Sleep(2 * time.Second)
	} else {
		slog.Info("Jellyfin startup wizard already completed", "component", "autowire")
		// Authenticate using the stored API key (or password fallback for upgrades)
		authToken, authErr := jellyfinAuth(s)
		if authErr != nil {
			slog.Error("Jellyfin auth failed, skipping library setup — to recover: re-run the setup wizard or run 'pelicula reset-config jellyfin'", "component", "autowire", "error", authErr)
			return
		}
		token = authToken
	}

	// Create a persistent API key if we don't have one yet.
	// On a fresh install (wizard just ran) the token is a short-lived session
	// token; we must obtain a persistent API key before wiring libraries so
	// that subsequent middleware→Jellyfin calls work after the session expires.
	if s.JellyfinAPIKey == "" {
		apiKey, err := createJellyfinAPIKey(s, token)
		if err != nil {
			slog.Error("failed to create Jellyfin API key", "component", "autowire", "error", err)
			if !wizardDone {
				// Fresh install: token is a session token that will expire.
				// Without a persistent key the middleware cannot authenticate
				// to Jellyfin after this boot.  Bail out so the operator
				// can investigate and re-run setup.
				slog.Error("aborting library wiring — restart to retry API key creation", "component", "autowire")
				return
			}
			// Upgrade path: token came from jellyfinAuth (password fallback).
			// Libraries will be wired now but auth will fall back to the
			// stored password on subsequent boots until the key is created.
		} else {
			s.mu.Lock()
			s.JellyfinAPIKey = apiKey
			s.mu.Unlock()
			// Use the persistent key for all subsequent calls in this run.
			token = apiKey

			// On first boot, create the operator admin account while
			// JELLYFIN_ADMIN_USER / JELLYFIN_PASSWORD are still in .env.
			// jellyfinAuth(s) will use the API key we just stored on the struct.
			if !wizardDone {
				envMu.Lock()
				credVars, credErr := parseEnvFile(envPath)
				envMu.Unlock()
				if credErr == nil {
					adminUser := credVars["JELLYFIN_ADMIN_USER"]
					adminPass := credVars["JELLYFIN_PASSWORD"]
					if adminUser != "" && adminUser != jellyfinServiceUser && adminPass != "" {
						if userID, createErr := CreateJellyfinUser(s, adminUser, adminPass); createErr != nil {
							slog.Warn("could not create operator admin account", "component", "autowire", "username", adminUser, "error", createErr)
						} else {
							slog.Info("operator admin account created", "component", "autowire", "username", adminUser)
							// Promote to Jellyfin admin so Pelicula grants them RoleAdmin.
							// jellyfinAuth(s) uses the API key stored above.
							// GET the full current policy first, then merge IsAdministrator:true
							// before POSTing back — Jellyfin replaces the entire policy object,
							// so a partial body would zero-out all other fields.
							if adminToken, authErr := jellyfinAuth(s); authErr == nil {
								promoteJellyfinAdmin(s, adminToken, userID, adminUser)
							}
						}
					}
				}
			}

			// Persist to .env and clean up legacy password
			envMu.Lock()
			vars, readErr := parseEnvFile(envPath)
			if readErr != nil {
				vars = make(map[string]string)
			}
			vars["JELLYFIN_API_KEY"] = apiKey
			delete(vars, "JELLYFIN_PASSWORD")
			delete(vars, "JELLYFIN_ADMIN_USER")
			if writeErr := writeEnvFile(envPath, vars); writeErr != nil {
				slog.Error("failed to persist Jellyfin API key to .env", "component", "autowire", "error", writeErr)
			} else {
				slog.Info("Jellyfin API key created and saved", "component", "autowire")
			}
			envMu.Unlock()
		}
	}

	for _, lib := range lh.GetLibraries() {
		collectionType := lib.Type
		if collectionType == "other" {
			collectionType = "mixed"
		}
		wireJellyfinLibrary(s, token, lib.Name, collectionType, lib.ContainerPath())
	}

	// Set the service user's preferred audio language so Jellyfin defaults to
	// the right track on playback (handles multi-audio files like Silo where
	// the first track is a foreign language but English is also present).
	// Use GET /Users (list all users) rather than /Users/Me — the token at this
	// point is an API key, not a session token, and /Users/Me returns 400 without
	// a valid session token on Jellyfin 10.9+.
	if usersData, err := jellyfinGet(s, "/Users", token); err != nil {
		slog.Warn("could not fetch users for audio pref", "component", "autowire", "error", err)
	} else {
		var users []map[string]any
		if jsonErr := json.Unmarshal(usersData, &users); jsonErr == nil {
			for _, u := range users {
				if name, _ := u["Name"].(string); name == jellyfinServiceUser {
					if svcUserID, _ := u["Id"].(string); svcUserID != "" {
						setJellyfinAudioPref(s, token, svcUserID)
					}
					break
				}
			}
		}
	}
}

// completeJellyfinWizard runs the Jellyfin startup wizard and returns a session
// token obtained by authenticating as the service account. The throwaway password
// is generated in memory and never written to disk.
func completeJellyfinWizard(s *ServiceClients) (string, error) {
	slog.Info("completing Jellyfin startup wizard", "component", "autowire")

	// Step 1: initial config
	_, err := jellyfinPost(s, "/Startup/Configuration", "", map[string]any{
		"UICulture":           "en-US",
		"MetadataCountryCode": "US",
	})
	if err != nil {
		return "", fmt.Errorf("set startup config: %w", err)
	}

	// Step 2: set admin user name and password.
	// Jellyfin 10.11+ changed /Startup/User to update an auto-created initial user
	// rather than creating one from scratch. The user is initialized lazily — a GET
	// to /Startup/User triggers the creation; only then does POST succeed.
	pass := generateAPIKey() // random throwaway, never stored
	adminUser := jellyfinServiceUser
	slog.Info("creating Jellyfin service account", "component", "autowire", "username", adminUser)
	// GET first to trigger lazy user initialization (Jellyfin 10.11+).
	if _, err = jellyfinGet(s, "/Startup/User", ""); err != nil {
		slog.Warn("could not fetch initial Jellyfin startup user", "component", "autowire", "error", err)
	}
	_, err = jellyfinPost(s, "/Startup/User", "", map[string]any{
		"Name":     adminUser,
		"Password": pass,
	})
	if err != nil {
		return "", fmt.Errorf("create admin user: %w", err)
	}

	// Step 3: mark wizard done
	_, err = jellyfinPost(s, "/Startup/Complete", "", nil)
	if err != nil {
		return "", fmt.Errorf("complete wizard: %w", err)
	}

	slog.Info("Jellyfin wizard completed", "component", "autowire")

	// Step 4: authenticate with the throwaway password to get a session token.
	// This token is returned to the caller so it can create an API key before
	// the password is discarded.
	data, err := jellyfinPost(s, "/Users/AuthenticateByName", "", map[string]any{
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

func jellyfinAuth(s *ServiceClients) (string, error) {
	// Use persistent API key if available (normal path after first boot)
	s.mu.RLock()
	apiKey := s.JellyfinAPIKey
	s.mu.RUnlock()
	if apiKey != "" {
		return apiKey, nil
	}

	// Read .env once; check for API key first, then fall back to password auth.
	// The API key is present when autowire has completed on a previous boot but
	// the container was restarted without a full down/up (Docker does not re-read
	// .env on restart, so the env var is stale even though the file has the key).
	vars, err := parseEnvFile(envPath)
	if err != nil {
		return "", fmt.Errorf("no API key and cannot read .env: %w", err)
	}
	if fileKey := vars["JELLYFIN_API_KEY"]; fileKey != "" {
		s.mu.Lock()
		s.JellyfinAPIKey = fileKey
		s.mu.Unlock()
		slog.Info("loaded Jellyfin API key from .env file", "component", "jellyfin")
		return fileKey, nil
	}

	// Fallback: password-based auth (first boot or upgrade from older version).
	adminUser := vars["JELLYFIN_ADMIN_USER"]
	if adminUser == "" {
		adminUser = jellyfinServiceUser
	}
	pass := vars["JELLYFIN_PASSWORD"]
	if pass == "" {
		return "", fmt.Errorf("no API key and no password in .env — run setup again")
	}

	data, err := jellyfinPost(s, "/Users/AuthenticateByName", "", map[string]any{
		"Username": adminUser,
		"Pw":       pass,
	})
	if err != nil {
		return "", err
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return "", err
	}
	token, _ := result["AccessToken"].(string)
	if token == "" {
		return "", fmt.Errorf("empty access token from Jellyfin")
	}
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

// createJellyfinAPIKey creates a persistent Jellyfin API key via POST /Auth/Keys.
// The key uses the same Token= header format as session tokens.
// If a "Pelicula" key already exists (e.g. from a prior boot), it is reused.
func createJellyfinAPIKey(s *ServiceClients, token string) (string, error) {
	// Check for an existing key first to avoid duplicates on restart.
	data, err := jellyfinGet(s, "/Auth/Keys", token)
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

	// No existing key — create one.  Jellyfin's POST /Auth/Keys returns 204
	// with no body, so we must fetch the key list again to get the token value.
	if _, err := jellyfinPost(s, "/Auth/Keys?app=Pelicula", token, nil); err != nil {
		return "", fmt.Errorf("create API key: %w", err)
	}
	data, err = jellyfinGet(s, "/Auth/Keys", token)
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
		return body, &jellyfinHTTPError{StatusCode: resp.StatusCode}
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

// jellyfinLibraryIDs returns a map of library name → Jellyfin folder ID.
func jellyfinLibraryIDs(s *ServiceClients, token string) (map[string]string, error) {
	data, err := jellyfinGet(s, "/Library/VirtualFolders", token)
	if err != nil {
		return nil, err
	}
	var folders []struct {
		Name   string `json:"Name"`
		ItemId string `json:"ItemId"`
	}
	if err := json.Unmarshal(data, &folders); err != nil {
		return nil, err
	}
	ids := make(map[string]string, len(folders))
	for _, f := range folders {
		ids[f.Name] = f.ItemId
	}
	return ids, nil
}

// promoteJellyfinAdmin promotes userID to Jellyfin administrator.
// It GETs the user's current policy first, sets IsAdministrator:true, then POSTs
// the full policy back. Sending only {"IsAdministrator":true} would zero-out all
// other policy fields (EnableMediaPlayback, EnableAllFolders, etc.) which breaks
// user access even though the admin flag is technically set.
func promoteJellyfinAdmin(s *ServiceClients, token, userID, username string) {
	userData, getErr := jellyfinGet(s, "/Users/"+userID, token)
	if getErr != nil {
		slog.Warn("could not fetch user for admin promotion", "component", "autowire", "username", username, "error", getErr)
		return
	}
	var user map[string]any
	if jsonErr := json.Unmarshal(userData, &user); jsonErr != nil {
		slog.Warn("could not parse user data for admin promotion", "component", "autowire", "username", username, "error", jsonErr)
		return
	}
	policy, _ := user["Policy"].(map[string]any)
	if policy == nil {
		policy = map[string]any{}
	}
	policy["IsAdministrator"] = true
	if _, polErr := jellyfinPost(s, "/Users/"+userID+"/Policy", token, policy); polErr != nil {
		slog.Warn("could not promote operator admin to Jellyfin admin", "component", "autowire", "username", username, "error", polErr)
		return
	}
	slog.Info("operator admin promoted to Jellyfin administrator", "component", "autowire", "username", username)
}

// setJellyfinAudioPref sets the user's preferred audio language in Jellyfin.
// Jellyfin uses ISO 639-2 three-letter codes (e.g. "eng") for AudioLanguagePreference.
// Uses the same GET-merge-POST pattern as promoteJellyfinAdmin to avoid zeroing
// out other configuration fields.
func setJellyfinAudioPref(s *ServiceClients, token, userID string) {
	lang := os.Getenv("PELICULA_AUDIO_LANG")
	if lang == "" {
		lang = "eng"
	}

	userData, err := jellyfinGet(s, "/Users/"+userID, token)
	if err != nil {
		slog.Warn("could not fetch user for audio pref", "component", "autowire", "userId", userID, "error", err)
		return
	}
	var user map[string]any
	if jsonErr := json.Unmarshal(userData, &user); jsonErr != nil {
		slog.Warn("could not parse user data for audio pref", "component", "autowire", "userId", userID, "error", jsonErr)
		return
	}
	config, _ := user["Configuration"].(map[string]any)
	if config == nil {
		config = map[string]any{}
	}
	config["AudioLanguagePreference"] = lang
	config["PlayDefaultAudioTrack"] = false // honour AudioLanguagePreference, not just "first track"
	if _, cfgErr := jellyfinPost(s, "/Users/"+userID+"/Configuration", token, config); cfgErr != nil {
		slog.Warn("could not set Jellyfin audio language preference", "component", "autowire", "userId", userID, "lang", lang, "error", cfgErr)
		return
	}
	slog.Info("Jellyfin audio language preference set", "component", "autowire", "userId", userID, "lang", lang)
}
