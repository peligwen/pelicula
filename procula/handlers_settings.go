package procula

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, GetSettings(s.db))
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	// Start from current settings so partial payloads (e.g. only storage
	// thresholds, or only pipeline toggles) don't zero out unrelated fields.
	settings := GetSettings(s.db)
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		writeError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	// Validate notification mode
	switch settings.NotifMode {
	case "internal", "apprise", "direct":
	default:
		settings.NotifMode = "internal"
	}
	switch settings.DualSubTranslator {
	case "argos", "none":
	default:
		settings.DualSubTranslator = "none"
	}
	if len(settings.DualSubPairs) == 0 {
		settings.DualSubPairs = []string{"en-es"}
	}
	// Clamp storage thresholds to [0, 100] and ensure warning < critical.
	if settings.StorageWarningPct < 0 {
		settings.StorageWarningPct = 0
	}
	if settings.StorageCriticalPct > 100 {
		settings.StorageCriticalPct = 100
	}
	if settings.StorageWarningPct >= settings.StorageCriticalPct {
		settings.StorageWarningPct = settings.StorageCriticalPct - 1
		if settings.StorageWarningPct < 0 {
			settings.StorageWarningPct = 0
		}
	}
	if err := SaveSettings(s.db, settings); err != nil {
		writeError(w, "failed to save settings: "+err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("settings saved", "component", "settings",
		"validation", settings.ValidationEnabled,
		"transcoding", settings.TranscodingEnabled,
		"catalog", settings.CatalogEnabled,
		"notif_mode", settings.NotifMode,
	)
	writeJSON(w, settings)
}

func (s *Server) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := LoadProfiles(s.configDir)
	if err != nil {
		writeError(w, "failed to load profiles: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if profiles == nil {
		profiles = []TranscodeProfile{}
	}
	writeJSON(w, profiles)
}

func (s *Server) handleSaveProfile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var p TranscodeProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if p.Name == "" {
		writeError(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := SaveProfile(s.configDir, p); err != nil {
		writeError(w, "failed to save profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, p)
}

func (s *Server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := DeleteProfile(s.configDir, name); err != nil {
		writeError(w, "failed to delete profile: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListDualSubProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := ListDualSubProfiles(s.db)
	if err != nil {
		writeError(w, "failed to list profiles: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, profiles)
}

func (s *Server) handleSaveDualSubProfile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var p DualSubProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	// On PUT, the URL segment is the authoritative name.
	if urlName := r.PathValue("name"); urlName != "" {
		p.Name = urlName
	}
	if p.Name == "" {
		writeError(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := SaveDualSubProfile(s.db, p); err != nil {
		if strings.HasPrefix(err.Error(), "cannot ") {
			writeError(w, err.Error(), http.StatusBadRequest)
		} else {
			writeError(w, "failed to save profile: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, p)
}

func (s *Server) handleDeleteDualSubProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := DeleteDualSubProfile(s.db, name); err != nil {
		if strings.HasPrefix(err.Error(), "cannot ") {
			writeError(w, err.Error(), http.StatusBadRequest)
		} else {
			writeError(w, "failed to delete profile: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSubtitleTracks(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, "path is required", http.StatusBadRequest)
		return
	}
	clean := filepath.Clean(path)
	if !isLibraryPath(clean) {
		writeError(w, "path must be under /media/", http.StatusBadRequest)
		return
	}
	tracks := subtitleTracksForPath(clean)
	if tracks == nil {
		tracks = []SubtitleTrack{}
	}
	dualsubs := dualsubSidecarsForPath(clean)
	if dualsubs == nil {
		dualsubs = []DualSubSidecar{}
	}

	var embedded []EmbeddedTrack
	streams, err := probeSubStreams(clean)
	if err == nil {
		embedded = filterTextEmbeddedTracks(streams)
	} else {
		slog.Warn("probe embedded subs failed", "path", clean, "error", err)
	}
	if embedded == nil {
		embedded = []EmbeddedTrack{}
	}

	writeJSON(w, map[string]any{
		"tracks":          tracks,
		"dualsubs":        dualsubs,
		"embedded_tracks": embedded,
	})
}

// handleDeleteDualSubSidecar removes a single dual-sub ASS sidecar file.
// The caller passes the exact sidecar file path (from the dualsubs list returned
// by handleSubtitleTracks). Only paths under /media/ are accepted.
func (s *Server) handleDeleteDualSubSidecar(w http.ResponseWriter, r *http.Request) {
	var body struct {
		File string `json:"file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.File == "" {
		writeError(w, "file is required", http.StatusBadRequest)
		return
	}
	clean := filepath.Clean(body.File)
	if !isLibraryPath(clean) {
		writeError(w, "path must be under /media/", http.StatusBadRequest)
		return
	}
	if !strings.HasSuffix(clean, ".ass") {
		writeError(w, "file must be an .ass file", http.StatusBadRequest)
		return
	}
	if err := os.Remove(clean); err != nil {
		if os.IsNotExist(err) {
			writeError(w, "file not found", http.StatusNotFound)
			return
		}
		writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slog.Info("deleted dual-sub sidecar", "component", "dualsub", "file", clean)
	writeJSON(w, map[string]any{"deleted": clean})
}
