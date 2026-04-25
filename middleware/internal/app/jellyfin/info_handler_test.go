package jellyfin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInfoHandler_ReturnsLanURLAndWebURL(t *testing.T) {
	parse := func(string) (map[string]string, error) {
		return map[string]string{"JELLYFIN_PUBLISHED_URL": "http://192.168.1.42:7354/jellyfin"}, nil
	}
	h := NewInfoHandler("/project/.env", parse)

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/jellyfin/info", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var got struct {
		WebURL string `json:"web_url"`
		LanURL string `json:"lan_url"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.WebURL != "/jellyfin/" {
		t.Errorf("web_url = %q, want /jellyfin/", got.WebURL)
	}
	if got.LanURL != "http://192.168.1.42:7354/jellyfin" {
		t.Errorf("lan_url = %q, want LAN URL from .env", got.LanURL)
	}
}

func TestInfoHandler_EmptyLanURLWhenUnset(t *testing.T) {
	parse := func(string) (map[string]string, error) {
		return map[string]string{}, nil
	}
	h := NewInfoHandler("/project/.env", parse)

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/jellyfin/info", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var got struct {
		WebURL string `json:"web_url"`
		LanURL string `json:"lan_url"`
	}
	json.NewDecoder(rec.Body).Decode(&got) //nolint:errcheck
	if got.LanURL != "" {
		t.Errorf("lan_url = %q, want empty", got.LanURL)
	}
	if got.WebURL != "/jellyfin/" {
		t.Errorf("web_url = %q, want /jellyfin/ even with no env", got.WebURL)
	}
}

func TestInfoHandler_DoesNotLeakAPIKey(t *testing.T) {
	// Defense in depth: even if the env file accidentally exposes JELLYFIN_API_KEY,
	// the handler must NEVER include it in the response.
	parse := func(string) (map[string]string, error) {
		return map[string]string{
			"JELLYFIN_API_KEY":       "super-secret-bearer-token",
			"JELLYFIN_PUBLISHED_URL": "http://192.168.1.42:7354/jellyfin",
		}, nil
	}
	h := NewInfoHandler("/project/.env", parse)

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/jellyfin/info", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	if got := body; len(got) > 0 && (containsString(got, "JELLYFIN_API_KEY") || containsString(got, "super-secret-bearer-token")) {
		t.Fatalf("response leaked API key: %s", body)
	}
}

func TestInfoHandler_RejectsNonGET(t *testing.T) {
	h := NewInfoHandler("/project/.env", nil)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(m, "/api/pelicula/jellyfin/info", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s status = %d, want 405", m, rec.Code)
		}
	}
}

func containsString(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
