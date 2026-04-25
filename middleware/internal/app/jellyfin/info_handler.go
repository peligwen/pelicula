package jellyfin

import (
	"net/http"

	"pelicula-api/httputil"
)

// InfoHandler serves GET /api/pelicula/jellyfin/info — public, non-secret
// metadata that the register flow and dashboard use to point users at
// Jellyfin (web URL + LAN URL for native apps).
type InfoHandler struct {
	// EnvPath is the .env file path inside the container.
	EnvPath string
	// ParseEnvFile is settings.ParseEnvFile (or any compatible reader). Injected
	// so this package can stay free of the settings dependency cycle.
	ParseEnvFile func(path string) (map[string]string, error)
}

// NewInfoHandler constructs an InfoHandler.
func NewInfoHandler(envPath string, parseEnvFile func(string) (map[string]string, error)) *InfoHandler {
	return &InfoHandler{EnvPath: envPath, ParseEnvFile: parseEnvFile}
}

// ServeHTTP returns {"web_url": "/jellyfin/", "lan_url": "<JELLYFIN_PUBLISHED_URL or empty>"}.
// No secrets are returned (intentionally NOT the API key).
func (h *InfoHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	resp := struct {
		WebURL string `json:"web_url"`
		LanURL string `json:"lan_url"`
	}{
		WebURL: "/jellyfin/",
	}

	if h.ParseEnvFile != nil {
		if vars, err := h.ParseEnvFile(h.EnvPath); err == nil {
			resp.LanURL = vars["JELLYFIN_PUBLISHED_URL"]
		}
	}

	httputil.WriteJSON(w, resp)
}
