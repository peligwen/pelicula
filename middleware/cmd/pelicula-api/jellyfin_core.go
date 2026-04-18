package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"pelicula-api/clients"
	jfapp "pelicula-api/internal/app/jellyfin"
	"pelicula-api/internal/app/library"
	jfclient "pelicula-api/internal/clients/jellyfin"
)

// jellyfinURL is a var (not const) so tests can point it at an httptest.Server
// and so power users can override it via JELLYFIN_URL.
var jellyfinURL = envOr("JELLYFIN_URL", "http://jellyfin:8096/jellyfin")

const jellyfinServiceUser = jfapp.ServiceUser

// ErrPasswordRequired is returned by CreateJellyfinUser when password is empty.
// Aliased from clients package so peligrosa and main both reference the same sentinel.
var ErrPasswordRequired = clients.ErrPasswordRequired

// jellyfinHTTPError is a package-level alias for clients.JellyfinHTTPError.
// Using the clients type ensures errors.As checks in peligrosa and in this
// package both match the same concrete type.
type jellyfinHTTPError = clients.JellyfinHTTPError

// jfClient returns a *jfclient.Client pointed at the current jellyfinURL,
// using the shared HTTP client from ServiceClients. Overriding jellyfinURL
// in tests automatically redirects all calls to the httptest.Server.
func jfClient(s *ServiceClients) *jfclient.Client {
	return jfclient.NewWithHTTPClient(jellyfinURL, s.client)
}

// jellyfinHTTPClient is the production implementation of clients.JellyfinClient.
// It forwards to the internal jellyfin packages.
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
	jfc := jfclient.NewWithHTTPClient(jellyfinURL, c.httpClient)
	result, err := jfc.AuthenticateByName(username, password)
	if err != nil {
		// Map jfclient.HTTPError → clients.JellyfinHTTPError so peligrosa sees the right type.
		if jErr, ok := err.(*jfclient.HTTPError); ok {
			return nil, &clients.JellyfinHTTPError{StatusCode: jErr.StatusCode}
		}
		return nil, err
	}
	return &clients.JellyfinLoginResult{
		UserID:          result.UserID,
		Username:        result.Username,
		IsAdministrator: result.IsAdministrator,
		AccessToken:     result.AccessToken,
	}, nil
}

func (c *jellyfinHTTPClient) CreateUser(username, password string) (string, error) {
	return CreateJellyfinUser(c.services, username, password)
}

// jellyfinAuth returns a valid Jellyfin token for the service account.
// Uses persistent API key if available, falling back to reading from .env.
func jellyfinAuth(s *ServiceClients) (string, error) {
	s.mu.RLock()
	apiKey := s.JellyfinAPIKey
	s.mu.RUnlock()
	if apiKey != "" {
		return apiKey, nil
	}

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

	jfc := jfClient(s)
	data, err := jfc.Post("/Users/AuthenticateByName", "", map[string]any{
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

// jellyfinGet makes a GET request to Jellyfin with the Emby authorization header.
// Used by services.go to implement catalog.JellyfinMetaClient.
func jellyfinGet(s *ServiceClients, path, token string) ([]byte, error) {
	return jfClient(s).Get(path, token)
}

// wireJellyfin auto-configures Jellyfin: completes the startup wizard (if needed)
// and adds Movies + TV Shows libraries pointing to the same folders used everywhere else.
func wireJellyfin(s *ServiceClients, lh *library.Handler) {
	jfc := jfClient(s)

	info, err := jfapp.SystemInfo(jfc)
	if err != nil {
		slog.Warn("Jellyfin not reachable, skipping auto-config", "component", "autowire", "error", err)
		return
	}

	wizardDone, _ := info["StartupWizardCompleted"].(bool)

	var token string
	if !wizardDone {
		wiz := &jfapp.Wizard{
			Client:    jfc,
			GenAPIKey: generateAPIKey,
		}
		wizardToken, wizardErr := wiz.CompleteWizard()
		if wizardErr != nil {
			slog.Error("Jellyfin wizard setup failed", "component", "autowire", "error", wizardErr)
			return
		}
		token = wizardToken
		jfapp.WizardSleep()
	} else {
		slog.Info("Jellyfin startup wizard already completed", "component", "autowire")
		authToken, authErr := jellyfinAuth(s)
		if authErr != nil {
			slog.Error("Jellyfin auth failed, skipping library setup — to recover: re-run the setup wizard or run 'pelicula reset-config jellyfin'", "component", "autowire", "error", authErr)
			return
		}
		token = authToken
	}

	if s.JellyfinAPIKey == "" {
		apiKey, err := jfapp.CreateAPIKey(jfc, token)
		if err != nil {
			slog.Error("failed to create Jellyfin API key", "component", "autowire", "error", err)
			if !wizardDone {
				slog.Error("aborting library wiring — restart to retry API key creation", "component", "autowire")
				return
			}
		} else {
			s.mu.Lock()
			s.JellyfinAPIKey = apiKey
			s.mu.Unlock()
			token = apiKey

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
							if adminToken, authErr := jellyfinAuth(s); authErr == nil {
								jfapp.PromoteAdmin(jfc, adminToken, userID, adminUser)
							}
						}
					}
				}
			}

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
		jfapp.WireLibrary(jfc, token, lib.Name, collectionType, lib.ContainerPath())
	}

	// Set the service user's preferred audio language.
	if svcUserID, err := jfapp.ServiceUserID(jfc, token); err != nil {
		slog.Warn("could not fetch users for audio pref", "component", "autowire", "error", err)
	} else if svcUserID != "" {
		jfapp.SetAudioPref(jfc, token, svcUserID)
	}
}

// TriggerLibraryRefresh asks Jellyfin to scan all libraries.
// Called by the middleware's /api/pelicula/jellyfin/refresh endpoint (invoked by Procula).
func TriggerLibraryRefresh(s *ServiceClients) error {
	jfc := jfClient(s)
	return jfapp.TriggerLibraryRefresh(jfc, func() (string, error) { return jellyfinAuth(s) })
}

// CreateJellyfinUser creates a new Jellyfin user with the given name and password.
// Returns the new user's Jellyfin ID on success.
func CreateJellyfinUser(s *ServiceClients, username, password string) (string, error) {
	h := &jfapp.Handler{
		Client:      jfClient(s),
		Auth:        func() (string, error) { return jellyfinAuth(s) },
		ServiceUser: jellyfinServiceUser,
	}
	return h.CreateUser(username, password)
}
