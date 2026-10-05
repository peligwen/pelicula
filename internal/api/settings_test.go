package api

import (
	"testing"

	"pelicula/internal/store"
)

func TestGetSettings(t *testing.T) {
	e := newEnv(t)
	e.srv.Cfg.HostConfigDir = "/srv/pelicula/config"
	e.srv.Cfg.HostLibraryDir = "/srv/media"
	e.srv.Cfg.HostWorkDir = "/srv/work"
	e.srv.Cfg.ServerCountries = "Netherlands"
	e.srv.Cfg.VPNEnabled = true
	e.srv.Cfg.TZ = "Europe/Amsterdam"

	rec := e.do(admin, "GET", "/api/settings", nil)
	wantStatus(t, rec, 200)
	got := decode(t, rec)

	settings := got["settings"].(map[string]any)
	if len(settings) != len(store.SettingDefaults) {
		t.Fatalf("settings = %v", settings)
	}
	for k, v := range store.SettingDefaults {
		if settings[k] != v {
			t.Errorf("settings[%s] = %v, want default %s", k, settings[k], v)
		}
	}
	info := got["info"].(map[string]any)
	want := map[string]any{
		"config_dir": "/srv/pelicula/config", "library_dir": "/srv/media", "work_dir": "/srv/work",
		"server_countries": "Netherlands", "vpn_enabled": true, "version": "test-1.0", "tz": "Europe/Amsterdam",
	}
	for k, v := range want {
		if info[k] != v {
			t.Errorf("info[%s] = %v, want %v", k, info[k], v)
		}
	}
	if len(info) != len(want) {
		t.Errorf("info = %v", info)
	}

	wantStatus(t, e.do(manager, "GET", "/api/settings", nil), 403)
}

func TestPutSettings(t *testing.T) {
	e := newEnv(t)
	rec := e.do(admin, "PUT", "/api/settings", map[string]any{
		store.SettingAutoApprove:       "true",
		store.SettingValidationEnabled: false, // a JSON bool is accepted too
		store.SettingAutoBlocklist:     "0",
	})
	wantStatus(t, rec, 200)
	got := decode(t, rec)
	want := map[string]string{
		store.SettingAutoApprove:       "true",
		store.SettingValidationEnabled: "false",
		store.SettingAutoBlocklist:     "false", // normalized
	}
	if len(got) != len(want) {
		t.Fatalf("response = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("response[%s] = %v, want %s", k, got[k], v)
		}
	}
	ctx := t.Context()
	if !e.store.BoolSetting(ctx, store.SettingAutoApprove) || e.store.BoolSetting(ctx, store.SettingValidationEnabled) ||
		e.store.BoolSetting(ctx, store.SettingAutoBlocklist) {
		t.Fatal("settings were not persisted")
	}

	// The change shows up in GET.
	settings := decode(t, e.do(admin, "GET", "/api/settings", nil))["settings"].(map[string]any)
	if settings[store.SettingAutoApprove] != "true" {
		t.Errorf("GET after PUT = %v", settings)
	}

	// A partial update leaves the others alone.
	wantStatus(t, e.do(admin, "PUT", "/api/settings", map[string]any{store.SettingAutoApprove: "false"}), 200)
	if e.store.BoolSetting(ctx, store.SettingValidationEnabled) {
		t.Error("validation_enabled changed by an unrelated PUT")
	}
}

func TestPutSettingsValidation(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name string
		body any
	}{
		{"unknown key", map[string]any{"nope": "true"}},
		{"non-bool string", map[string]any{store.SettingAutoApprove: "maybe"}},
		{"empty string", map[string]any{store.SettingAutoApprove: ""}},
		{"number", map[string]any{store.SettingAutoApprove: 5}},
		{"null", map[string]any{store.SettingAutoApprove: nil}},
		{"not an object", "[1,2]"},
		{"not json", "{"},
		{"empty body", nil},
		// One bad key rejects the whole request, valid keys included.
		{"mixed", map[string]any{store.SettingAutoApprove: "true", "nope": "true"}},
	}
	for _, c := range cases {
		if got := e.do(admin, "PUT", "/api/settings", c.body).Code; got != 400 {
			t.Errorf("%s: status = %d, want 400", c.name, got)
		}
	}
	if e.store.BoolSetting(t.Context(), store.SettingAutoApprove) {
		t.Fatal("a rejected PUT changed a setting")
	}
	wantStatus(t, e.do(manager, "PUT", "/api/settings", map[string]any{store.SettingAutoApprove: "true"}), 403)
}
