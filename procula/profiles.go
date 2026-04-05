package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// TranscodeProfile is loaded from /config/procula/profiles/*.json.
// The JSON format is defined by install_default_profiles in the pelicula CLI.
type TranscodeProfile struct {
	Name        string              `json:"name"`
	Enabled     bool                `json:"enabled"`
	Description string              `json:"description"`
	Conditions  TranscodeConditions `json:"conditions"`
	Output      TranscodeOutput     `json:"output"`
}

type TranscodeConditions struct {
	// CodecsInclude: trigger if the input video codec is in this list (case-insensitive).
	// Example: ["hevc", "h265", "av1"]
	CodecsInclude []string `json:"codecs_include,omitempty"`

	// MinHeight: trigger if the input video height is >= this value.
	// Example: 2160 for 4K content
	MinHeight int `json:"min_height,omitempty"`
}

type TranscodeOutput struct {
	VideoCodec    string `json:"video_codec"`              // "libx264", "libx265", "copy", etc.
	VideoPreset   string `json:"video_preset,omitempty"`   // "medium", "slow", "fast"
	VideoCRF      int    `json:"video_crf,omitempty"`      // constant rate factor (18-28 typical)
	MaxHeight     int    `json:"max_height,omitempty"`     // scale down to this height; 0 = no scaling
	AudioCodec    string `json:"audio_codec"`              // "aac", "ac3", "copy"
	AudioChannels int    `json:"audio_channels,omitempty"` // 0 = keep original
	Suffix        string `json:"suffix"`                   // appended to output filename before extension
}

// LoadProfiles reads all enabled profiles from /config/procula/profiles/*.json.
func LoadProfiles(configDir string) ([]TranscodeProfile, error) {
	dir := filepath.Join(configDir, "procula", "profiles")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var profiles []TranscodeProfile
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var p TranscodeProfile
		if json.Unmarshal(data, &p) == nil {
			profiles = append(profiles, p)
		}
	}
	return profiles, nil
}

// FindMatchingProfile returns the first enabled profile whose conditions match
// the given video codec and height. Returns nil if no profile matches.
func FindMatchingProfile(profiles []TranscodeProfile, videoCodec string, videoHeight int) *TranscodeProfile {
	for i, p := range profiles {
		if !p.Enabled {
			continue
		}
		if matchesConditions(p.Conditions, videoCodec, videoHeight) {
			return &profiles[i]
		}
	}
	return nil
}

func matchesConditions(c TranscodeConditions, videoCodec string, videoHeight int) bool {
	// A profile matches if ANY of its conditions are satisfied.
	// (Profiles typically specify one condition type.)
	if len(c.CodecsInclude) > 0 {
		for _, codec := range c.CodecsInclude {
			if strings.EqualFold(codec, videoCodec) {
				return true
			}
		}
	}
	if c.MinHeight > 0 && videoHeight >= c.MinHeight {
		return true
	}
	// Profile with no conditions matches everything (catch-all).
	if len(c.CodecsInclude) == 0 && c.MinHeight == 0 {
		return true
	}
	return false
}
