package autowire

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	bazarrclient "pelicula-api/internal/clients/bazarr"
)

// bazarrDriftServer creates a test HTTP server that returns canned JSON for
// the Bazarr settings and language-profiles endpoints. The caller supplies the
// responses; any other path returns 200 OK with "{}".
func bazarrDriftServer(t *testing.T, settings, profiles any) *httptest.Server {
	t.Helper()
	settingsJSON, _ := json.Marshal(settings)
	profilesJSON, _ := json.Marshal(profiles)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/system/settings":
			w.Write(settingsJSON) //nolint:errcheck
		case "/api/system/languages/profiles":
			w.Write(profilesJSON) //nolint:errcheck
		default:
			w.Write([]byte("{}")) //nolint:errcheck
		}
	}))
}

func wiredSettings(sonarrKey, radarrKey string) map[string]any {
	return map[string]any{
		"general": map[string]any{
			"use_sonarr":        true,
			"use_radarr":        true,
			"enabled_providers": []string{"podnapisi", "yifysubtitles"},
		},
		"sonarr": map[string]any{
			"apikey":   sonarrKey,
			"ip":       "sonarr",
			"port":     8989,
			"base_url": "/sonarr",
		},
		"radarr": map[string]any{
			"apikey":   radarrKey,
			"ip":       "radarr",
			"port":     7878,
			"base_url": "/radarr",
		},
	}
}

func peliculaProfile(langs []string) map[string]any {
	items := make([]map[string]any, 0, len(langs))
	for i, l := range langs {
		items = append(items, map[string]any{
			"id":                 i + 1,
			"language":           l,
			"audio_only_include": "False",
		})
	}
	return map[string]any{
		"profileId": 1,
		"name":      "Pelicula",
		"items":     items,
	}
}

// TestBazarrAlreadyWiredHappyPath confirms the function returns true when
// all fields match.
func TestBazarrAlreadyWiredHappyPath(t *testing.T) {
	srv := bazarrDriftServer(t,
		wiredSettings("sk", "rk"),
		[]any{peliculaProfile([]string{"en"})},
	)
	defer srv.Close()

	c := bazarrclient.New(srv.URL, "key")
	if !bazarrAlreadyWired(c, "sk", "rk", []string{"en"}) {
		t.Error("expected bazarrAlreadyWired to return true when config matches")
	}
}

// TestBazarrAlreadyWiredSubLangDrift confirms false when PELICULA_SUB_LANGS
// changed (e.g., "en" → "en,fr").
func TestBazarrAlreadyWiredSubLangDrift(t *testing.T) {
	srv := bazarrDriftServer(t,
		wiredSettings("sk", "rk"),
		[]any{peliculaProfile([]string{"en"})},
	)
	defer srv.Close()

	c := bazarrclient.New(srv.URL, "key")
	if bazarrAlreadyWired(c, "sk", "rk", []string{"en", "fr"}) {
		t.Error("expected bazarrAlreadyWired to return false when sub-lang set drifted")
	}
}

// TestBazarrAlreadyWiredMissingProfile confirms false when the "Pelicula"
// profile is absent.
func TestBazarrAlreadyWiredMissingProfile(t *testing.T) {
	srv := bazarrDriftServer(t,
		wiredSettings("sk", "rk"),
		[]any{map[string]any{"name": "SomeOtherProfile", "items": []any{}}},
	)
	defer srv.Close()

	c := bazarrclient.New(srv.URL, "key")
	if bazarrAlreadyWired(c, "sk", "rk", []string{"en"}) {
		t.Error("expected bazarrAlreadyWired to return false when Pelicula profile is absent")
	}
}

// TestBazarrAlreadyWiredBrokenProfile confirms false when a profile item
// is missing audio_only_include (old broken wiring).
func TestBazarrAlreadyWiredBrokenProfile(t *testing.T) {
	brokenProfile := map[string]any{
		"profileId": 1,
		"name":      "Pelicula",
		"items": []map[string]any{
			{"id": 1, "language": "en"},
		},
	}
	srv := bazarrDriftServer(t,
		wiredSettings("sk", "rk"),
		[]any{brokenProfile},
	)
	defer srv.Close()

	c := bazarrclient.New(srv.URL, "key")
	if bazarrAlreadyWired(c, "sk", "rk", []string{"en"}) {
		t.Error("expected bazarrAlreadyWired to return false when audio_only_include is missing")
	}
}

// TestBazarrAlreadyWiredSonarrURLDrift confirms false when Sonarr's IP drifted.
func TestBazarrAlreadyWiredSonarrURLDrift(t *testing.T) {
	driftedSettings := map[string]any{
		"general": map[string]any{
			"use_sonarr":        true,
			"use_radarr":        true,
			"enabled_providers": []string{"podnapisi"},
		},
		"sonarr": map[string]any{
			"apikey":   "sk",
			"ip":       "sonarr-old",
			"port":     8989,
			"base_url": "/sonarr",
		},
		"radarr": map[string]any{
			"apikey":   "rk",
			"ip":       "radarr",
			"port":     7878,
			"base_url": "/radarr",
		},
	}
	srv := bazarrDriftServer(t,
		driftedSettings,
		[]any{peliculaProfile([]string{"en"})},
	)
	defer srv.Close()

	c := bazarrclient.New(srv.URL, "key")
	if bazarrAlreadyWired(c, "sk", "rk", []string{"en"}) {
		t.Error("expected bazarrAlreadyWired to return false when Sonarr IP drifted")
	}
}

// TestBazarrAlreadyWiredRadarrPortDrift confirms false when Radarr's port drifted.
func TestBazarrAlreadyWiredRadarrPortDrift(t *testing.T) {
	driftedSettings := map[string]any{
		"general": map[string]any{
			"use_sonarr":        true,
			"use_radarr":        true,
			"enabled_providers": []string{"podnapisi"},
		},
		"sonarr": map[string]any{
			"apikey":   "sk",
			"ip":       "sonarr",
			"port":     8989,
			"base_url": "/sonarr",
		},
		"radarr": map[string]any{
			"apikey":   "rk",
			"ip":       "radarr",
			"port":     9999,
			"base_url": "/radarr",
		},
	}
	srv := bazarrDriftServer(t,
		driftedSettings,
		[]any{peliculaProfile([]string{"en"})},
	)
	defer srv.Close()

	c := bazarrclient.New(srv.URL, "key")
	if bazarrAlreadyWired(c, "sk", "rk", []string{"en"}) {
		t.Error("expected bazarrAlreadyWired to return false when Radarr port drifted")
	}
}

// TestBazarrAlreadyWiredEmptyProviders confirms false when enabled_providers
// is empty (Bazarr ship-default state).
func TestBazarrAlreadyWiredEmptyProviders(t *testing.T) {
	settings := map[string]any{
		"general": map[string]any{
			"use_sonarr":        true,
			"use_radarr":        true,
			"enabled_providers": []string{},
		},
		"sonarr": map[string]any{"apikey": "sk", "ip": "sonarr", "port": 8989, "base_url": "/sonarr"},
		"radarr": map[string]any{"apikey": "rk", "ip": "radarr", "port": 7878, "base_url": "/radarr"},
	}
	srv := bazarrDriftServer(t,
		settings,
		[]any{peliculaProfile([]string{"en"})},
	)
	defer srv.Close()

	c := bazarrclient.New(srv.URL, "key")
	if bazarrAlreadyWired(c, "sk", "rk", []string{"en"}) {
		t.Error("expected bazarrAlreadyWired to return false when enabled_providers is empty")
	}
}
