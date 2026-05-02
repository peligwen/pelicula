package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"sync"

	"pelicula-api/clients"
	"pelicula-api/internal/app/library"
	appservices "pelicula-api/internal/app/services"
	jfclient "pelicula-api/internal/clients/jellyfin"
)

// Wirer holds the injected dependencies for Jellyfin startup wiring.
// Construct one with NewWirer and call Wire to auto-configure Jellyfin.
type Wirer struct {
	Services     *appservices.Clients
	JellyfinURL  string
	ServiceUser  string
	AudioLang    string
	GenAPIKey    func() string
	ParseEnvFile func(path string) (map[string]string, error)
	WriteEnvFile func(path string, vars map[string]string) error
	EnvPath      string
	EnvMu        sync.Locker // caller-provided mutex (use *sync.Mutex)
}

// NewWirer constructs a Wirer with the given dependencies.
func NewWirer(
	svc *appservices.Clients,
	jellyfinURL, envPath, audioLang string,
	genKey func() string,
	parseEnv func(string) (map[string]string, error),
	writeEnv func(string, map[string]string) error,
	envMu sync.Locker,
) *Wirer {
	return &Wirer{
		Services:     svc,
		JellyfinURL:  jellyfinURL,
		ServiceUser:  ServiceUser,
		AudioLang:    audioLang,
		GenAPIKey:    genKey,
		ParseEnvFile: parseEnv,
		WriteEnvFile: writeEnv,
		EnvPath:      envPath,
		EnvMu:        envMu,
	}
}

// client returns a *jfclient.Client pointed at this Wirer's JellyfinURL,
// using the shared HTTP client from Services.
func (w *Wirer) client() *jfclient.Client {
	return jfclient.NewWithHTTPClient(w.JellyfinURL, w.Services.HTTPClient())
}

// Auth returns a valid Jellyfin token for the service account.
// Uses persistent API key if available, falling back to reading from .env.
func (w *Wirer) Auth(ctx context.Context) (string, error) {
	if apiKey := w.Services.GetJellyfinAPIKey(); apiKey != "" {
		return apiKey, nil
	}

	// Hold EnvMu while reading .env so we don't race against Wire()'s
	// concurrent rewrites (handover from password-based auth to API-key
	// auth on the first wizard run, or any other env mutation). Without
	// this, a concurrent caller could see a torn read mid-write.
	w.EnvMu.Lock()
	vars, err := w.ParseEnvFile(w.EnvPath)
	w.EnvMu.Unlock()
	if err != nil {
		return "", fmt.Errorf("no API key and cannot read .env: %w", err)
	}
	if fileKey := vars["JELLYFIN_API_KEY"]; fileKey != "" {
		w.Services.SetJellyfinAPIKey(fileKey)
		slog.Info("loaded Jellyfin API key from .env file", "component", "jellyfin")
		return fileKey, nil
	}

	// Fallback: password-based auth (first boot or upgrade from older version).
	adminUser := vars["JELLYFIN_ADMIN_USER"]
	if adminUser == "" {
		adminUser = w.ServiceUser
	}
	pass := vars["JELLYFIN_PASSWORD"]
	if pass == "" {
		return "", fmt.Errorf("no API key and no password in .env — run setup again")
	}

	jfc := w.client()
	data, err := jfc.Post(ctx, "/Users/AuthenticateByName", "", map[string]any{
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

// Wire auto-configures Jellyfin: completes the startup wizard (if needed)
// and adds Movies + TV Shows libraries pointing to the same folders used elsewhere.
func (w *Wirer) Wire(ctx context.Context, lh *library.Handler) {
	jfc := w.client()

	info, err := SystemInfo(ctx, jfc)
	if err != nil {
		slog.Warn("Jellyfin not reachable, skipping auto-config", "component", "autowire", "error", err)
		return
	}

	wizardDone, _ := info["StartupWizardCompleted"].(bool)

	var token string
	if !wizardDone {
		wiz := &Wizard{
			Client:    jfc,
			GenAPIKey: w.GenAPIKey,
		}
		wizardToken, wizardErr := wiz.CompleteWizard(ctx)
		if wizardErr != nil {
			slog.Error("Jellyfin wizard setup failed", "component", "autowire", "error", wizardErr)
			return
		}
		token = wizardToken
		WizardSleep()
	} else {
		slog.Info("Jellyfin startup wizard already completed", "component", "autowire")
		authToken, authErr := w.Auth(ctx)
		if authErr != nil {
			slog.Error("Jellyfin auth failed, skipping library setup — to recover: re-run the setup wizard or run 'pelicula reset-config jellyfin'", "component", "autowire", "error", authErr)
			return
		}
		token = authToken
	}

	if w.Services.GetJellyfinAPIKey() == "" {
		apiKey, err := CreateAPIKey(ctx, jfc, token)
		if err != nil {
			slog.Error("failed to create Jellyfin API key", "component", "autowire", "error", err)
			if !wizardDone {
				slog.Error("aborting library wiring — restart to retry API key creation", "component", "autowire")
				return
			}
		} else {
			w.Services.SetJellyfinAPIKey(apiKey)
			token = apiKey

			if !wizardDone {
				w.EnvMu.Lock()
				credVars, credErr := w.ParseEnvFile(w.EnvPath)
				w.EnvMu.Unlock()
				if credErr == nil {
					adminUser := credVars["JELLYFIN_ADMIN_USER"]
					adminPass := credVars["JELLYFIN_PASSWORD"]
					if adminUser != "" && adminUser != w.ServiceUser && adminPass != "" {
						createdUser := false
						if userID, createErr := w.CreateUser(ctx, adminUser, adminPass); createErr != nil {
							// Leave credentials in .env so the next startup can retry.
							slog.Warn("could not create operator admin account — credentials retained for retry", "component", "autowire", "username", adminUser, "error", createErr)
						} else {
							createdUser = true
							slog.Info("operator admin account created", "component", "autowire", "username", adminUser)
							if adminToken, authErr := w.Auth(ctx); authErr == nil {
								PromoteAdmin(ctx, jfc, adminToken, userID, adminUser)
							}
						}
						if !createdUser {
							// Skip credential erasure — we need them on the next attempt.
							w.EnvMu.Lock()
							vars, readErr := w.ParseEnvFile(w.EnvPath)
							if readErr != nil {
								vars = make(map[string]string)
							}
							vars["JELLYFIN_API_KEY"] = apiKey
							if writeErr := w.WriteEnvFile(w.EnvPath, vars); writeErr != nil {
								slog.Error("failed to persist Jellyfin API key to .env", "component", "autowire", "error", writeErr)
							} else {
								slog.Info("Jellyfin API key created and saved", "component", "autowire")
							}
							w.EnvMu.Unlock()
							return
						}
					}
				}
			}

			w.EnvMu.Lock()
			vars, readErr := w.ParseEnvFile(w.EnvPath)
			if readErr != nil {
				vars = make(map[string]string)
			}
			vars["JELLYFIN_API_KEY"] = apiKey
			delete(vars, "JELLYFIN_PASSWORD")
			delete(vars, "JELLYFIN_ADMIN_USER")
			if writeErr := w.WriteEnvFile(w.EnvPath, vars); writeErr != nil {
				slog.Error("failed to persist Jellyfin API key to .env", "component", "autowire", "error", writeErr)
			} else {
				slog.Info("Jellyfin API key created and saved", "component", "autowire")
			}
			w.EnvMu.Unlock()
		}
	}

	for _, lib := range lh.GetLibraries() {
		collectionType := lib.Type
		if collectionType == "other" {
			collectionType = "mixed"
		}
		WireLibrary(ctx, jfc, token, lib.Name, collectionType, lib.ContainerPath())
	}

	// Set the service user's preferred audio language.
	if svcUserID, err := ServiceUserID(ctx, jfc, token); err != nil {
		slog.Warn("could not fetch users for audio pref", "component", "autowire", "error", err)
	} else if svcUserID != "" {
		SetAudioPref(ctx, jfc, token, svcUserID, w.AudioLang)
	}

	hwType, hwDevice := HwAccelProbe(
		os.Getenv("PELICULA_JELLYFIN_HWACCEL"),
		func(p string) error { _, err := os.Stat(p); return err },
		runtime.GOOS,
		runtime.GOARCH,
	)
	if hwType != HwAccelNone {
		wireHwAccel(ctx, jfc, token, hwType, hwDevice)
	}
}

// wireHwAccel applies hwType (and optionally vaapiDevice) to Jellyfin's
// /System/Configuration/encoding endpoint, using GET-merge-POST so that
// all other encoding settings are preserved. It is a no-op when the current
// HardwareAccelerationType is already non-"none" (respects user-set config).
func wireHwAccel(ctx context.Context, client *jfclient.Client, token string, hwType HwAccelType, vaapiDevice string) {
	data, err := client.Get(ctx, "/System/Configuration/encoding", token)
	if err != nil {
		slog.Warn("could not read Jellyfin encoding config", "component", "autowire", "error", err)
		return
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		slog.Warn("could not parse Jellyfin encoding config", "component", "autowire", "error", err)
		return
	}
	if current, _ := cfg["HardwareAccelerationType"].(string); current != "none" && current != "" {
		slog.Info("Jellyfin hardware acceleration already configured, skipping probe result",
			"component", "autowire", "current", current)
		return
	}
	cfg["HardwareAccelerationType"] = string(hwType)
	if vaapiDevice != "" {
		cfg["VaapiDevice"] = vaapiDevice
	}
	if _, err := client.Post(ctx, "/System/Configuration/encoding", token, cfg); err != nil {
		slog.Warn("could not apply Jellyfin hardware acceleration config", "component", "autowire",
			"type", hwType, "error", err)
		return
	}
	slog.Info("Jellyfin hardware acceleration configured", "component", "autowire", "type", hwType)
}

// TriggerRefresh asks Jellyfin to scan all libraries.
// Called by the middleware's /api/pelicula/jellyfin/refresh endpoint (invoked by Procula).
func (w *Wirer) TriggerRefresh(ctx context.Context) error {
	jfc := w.client()
	return TriggerLibraryRefresh(ctx, jfc, w.Auth)
}

// CreateUser creates a new Jellyfin user with the given name and password.
// Returns the new user's Jellyfin ID on success.
func (w *Wirer) CreateUser(ctx context.Context, username, password string) (string, error) {
	h := &Handler{
		Client:      w.client(),
		Auth:        w.Auth,
		ServiceUser: w.ServiceUser,
		AudioLang:   w.AudioLang,
	}
	return h.CreateUser(ctx, username, password)
}

// jellyfinHTTPClient is the production implementation of clients.JellyfinClient.
// It forwards to the internal jellyfin packages.
type jellyfinHTTPClient struct {
	httpClient  *http.Client
	jellyfinURL string
	auth        func(context.Context) (string, error)
	createUser  func(ctx context.Context, username, password string) (string, error)
}

// NewJellyfinHTTPClient returns a clients.JellyfinClient backed by the given
// http.Client (for authenticate calls), auth function, and createUser function.
func NewJellyfinHTTPClient(
	hc *http.Client,
	auth func(context.Context) (string, error),
	createUser func(context.Context, string, string) (string, error),
	jellyfinURL string,
) clients.JellyfinClient {
	return &jellyfinHTTPClient{
		httpClient:  hc,
		jellyfinURL: jellyfinURL,
		auth:        auth,
		createUser:  createUser,
	}
}

func (c *jellyfinHTTPClient) AuthenticateByName(username, password string) (*clients.JellyfinLoginResult, error) {
	jfc := jfclient.NewWithHTTPClient(c.jellyfinURL, c.httpClient)
	result, err := jfc.AuthenticateByName(context.Background(), username, password)
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
	return c.createUser(context.Background(), username, password)
}
