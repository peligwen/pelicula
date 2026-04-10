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

// defaultProfiles returns the 3 built-in starter profiles written on first startup.
func defaultProfiles() []TranscodeProfile {
	return []TranscodeProfile{
		{
			Name:        "Compatibility 1080p",
			Enabled:     true,
			Description: "Re-encode HEVC/AV1 to H.264 for broad device compatibility, capped at 1080p.",
			Conditions:  TranscodeConditions{CodecsInclude: []string{"hevc", "h265", "av1"}},
			Output: TranscodeOutput{
				VideoCodec:    "libx264",
				VideoPreset:   "medium",
				VideoCRF:      20,
				MaxHeight:     1080,
				AudioCodec:    "aac",
				AudioChannels: 2,
				Suffix:        "-compat",
			},
		},
		{
			Name:        "Compatibility 720p",
			Enabled:     true,
			Description: "Re-encode HEVC/AV1 to H.264 at 720p for mobile and older devices.",
			Conditions:  TranscodeConditions{CodecsInclude: []string{"hevc", "h265", "av1"}},
			Output: TranscodeOutput{
				VideoCodec:    "libx264",
				VideoPreset:   "medium",
				VideoCRF:      22,
				MaxHeight:     720,
				AudioCodec:    "aac",
				AudioChannels: 2,
				Suffix:        "-mobile",
			},
		},
		{
			Name:        "Downscale 4K to 1080p",
			Enabled:     true,
			Description: "Downscale 4K (2160p+) content to 1080p H.264 to save storage.",
			Conditions:  TranscodeConditions{MinHeight: 2160},
			Output: TranscodeOutput{
				VideoCodec:  "libx264",
				VideoPreset: "medium",
				VideoCRF:    20,
				MaxHeight:   1080,
				AudioCodec:  "copy",
				Suffix:      "-1080p",
			},
		},
	}
}

// SeedDefaultProfiles writes the default profile JSON files if the profiles
// directory exists but contains no .json files. Safe to call on every startup.
func SeedDefaultProfiles(configDir string) {
	dir := filepath.Join(configDir, "procula", "profiles")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			return // already has profiles
		}
	}
	for _, p := range defaultProfiles() {
		_ = saveProfile(dir, p)
	}
}

// SaveProfile writes a single profile to disk, creating or overwriting the file.
func SaveProfile(configDir string, p TranscodeProfile) error {
	dir := filepath.Join(configDir, "procula", "profiles")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return saveProfile(dir, p)
}

func saveProfile(dir string, p TranscodeProfile) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	filename := profileFilename(p.Name)
	return os.WriteFile(filepath.Join(dir, filename), data, 0644)
}

// DeleteProfile removes a profile JSON file by name. Returns nil if not found.
func DeleteProfile(configDir string, name string) error {
	dir := filepath.Join(configDir, "procula", "profiles")
	path := filepath.Join(dir, profileFilename(name))
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// profileFilename converts a profile name to a safe filename.
func profileFilename(name string) string {
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, name)
	return strings.ToLower(safe) + ".json"
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

// FindProfileByName returns the profile with the given name regardless of its
// enabled flag. Used for manual transcode requests where the user explicitly
// picks a profile. Returns nil if no profile with that name exists.
func FindProfileByName(profiles []TranscodeProfile, name string) *TranscodeProfile {
	for i, p := range profiles {
		if strings.EqualFold(p.Name, name) {
			return &profiles[i]
		}
	}
	return nil
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

// normalizeCodecName converts encoder names (e.g. "libx264") to the codec name
// that FFprobe would report (e.g. "h264") so passthrough comparisons work correctly.
func normalizeCodecName(codec string) string {
	switch strings.ToLower(codec) {
	case "libx264":
		return "h264"
	case "libx265":
		return "hevc"
	case "libvpx-vp9":
		return "vp9"
	case "libaom-av1":
		return "av1"
	default:
		return strings.ToLower(codec)
	}
}

// ShouldPassthrough returns true when the source already satisfies the profile's
// output requirements (same codec family, height already within target). In that
// case there is nothing to do and ffmpeg should be skipped entirely.
func ShouldPassthrough(codecs *CodecInfo, profile *TranscodeProfile) bool {
	if profile.Output.VideoCodec == "copy" {
		// "copy" means explicitly stream-copy; let the caller decide, not our concern
		return false
	}
	// Codec must already match the target (normalize encoder names → codec names)
	if normalizeCodecName(codecs.Video) != normalizeCodecName(profile.Output.VideoCodec) {
		return false
	}
	// Height is either unconstrained or source is already within the target
	if profile.Output.MaxHeight > 0 && codecs.Height > profile.Output.MaxHeight {
		return false
	}
	return true
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
