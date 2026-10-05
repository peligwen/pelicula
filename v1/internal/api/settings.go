package api

import (
	"net/http"
	"strconv"

	"pelicula/internal/store"
)

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.Store.AllSettings(r.Context())
	if err != nil {
		s.log().Error("read settings", "err", err)
		writeError(w, http.StatusInternalServerError, "could not read settings")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": settings,
		"info": map[string]any{
			"config_dir":       s.Cfg.HostConfigDir,
			"library_dir":      s.Cfg.HostLibraryDir,
			"work_dir":         s.Cfg.HostWorkDir,
			"server_countries": s.Cfg.ServerCountries,
			"vpn_enabled":      s.Cfg.VPNEnabled,
			"version":          s.Cfg.Version,
			"tz":               s.Cfg.TZ,
		},
	})
}

// handlePutSettings applies {"key":"value",...}. Every key must be a known
// setting and every value a boolean ("true"/"false", or a JSON bool); nothing
// is written unless the whole body is valid. It answers with the full
// settings map as stored.
func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !readJSON(w, r, &body, false) {
		return
	}
	updates := make(map[string]string, len(body))
	for key, raw := range body {
		if _, known := store.SettingDefaults[key]; !known {
			writeError(w, http.StatusBadRequest, "unknown setting "+strconv.Quote(key))
			return
		}
		var b bool
		switch v := raw.(type) {
		case bool:
			b = v
		case string:
			parsed, err := strconv.ParseBool(v)
			if err != nil {
				writeError(w, http.StatusBadRequest, "setting "+strconv.Quote(key)+" must be true or false")
				return
			}
			b = parsed
		default:
			writeError(w, http.StatusBadRequest, "setting "+strconv.Quote(key)+" must be true or false")
			return
		}
		updates[key] = strconv.FormatBool(b)
	}
	for key, val := range updates {
		if err := s.Store.SetSetting(r.Context(), key, val); err != nil {
			s.log().Error("write setting", "key", key, "err", err)
			writeError(w, http.StatusInternalServerError, "could not save settings")
			return
		}
	}
	settings, err := s.Store.AllSettings(r.Context())
	if err != nil {
		s.log().Error("read settings", "err", err)
		writeError(w, http.StatusInternalServerError, "could not read settings")
		return
	}
	writeJSON(w, http.StatusOK, settings)
}
