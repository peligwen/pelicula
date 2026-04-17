package procula

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMatchesConditions(t *testing.T) {
	cases := []struct {
		name        string
		conditions  TranscodeConditions
		videoCodec  string
		videoHeight int
		want        bool
	}{
		{
			"codec in list matches",
			TranscodeConditions{CodecsInclude: []string{"hevc", "h265"}},
			"hevc", 1080, true,
		},
		{
			"codec not in list",
			TranscodeConditions{CodecsInclude: []string{"hevc"}},
			"h264", 1080, false,
		},
		{
			"codec match case-insensitive",
			TranscodeConditions{CodecsInclude: []string{"HEVC"}},
			"hevc", 1080, true,
		},
		{
			"min height met",
			TranscodeConditions{MinHeight: 2160},
			"h264", 2160, true,
		},
		{
			"min height exceeded",
			TranscodeConditions{MinHeight: 2160},
			"h264", 2200, true,
		},
		{
			"min height not met",
			TranscodeConditions{MinHeight: 2160},
			"h264", 1080, false,
		},
		{
			"catch-all: no conditions",
			TranscodeConditions{},
			"anything", 0, true,
		},
		{
			"height condition only, not met",
			TranscodeConditions{MinHeight: 1080},
			"h264", 720, false,
		},
		{
			"codec or height — codec matches",
			TranscodeConditions{CodecsInclude: []string{"av1"}, MinHeight: 2160},
			"av1", 1080, true,
		},
		{
			"codec or height — height matches",
			TranscodeConditions{CodecsInclude: []string{"av1"}, MinHeight: 2160},
			"h264", 2160, true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := matchesConditions(c.conditions, c.videoCodec, c.videoHeight)
			if got != c.want {
				t.Errorf("matchesConditions(%+v, %q, %d) = %v, want %v",
					c.conditions, c.videoCodec, c.videoHeight, got, c.want)
			}
		})
	}
}

func TestFindMatchingProfile(t *testing.T) {
	hevcProfile := TranscodeProfile{
		Name:    "hevc-to-h264",
		Enabled: true,
		Conditions: TranscodeConditions{
			CodecsInclude: []string{"hevc"},
		},
	}
	hdProfile := TranscodeProfile{
		Name:    "4k-downscale",
		Enabled: true,
		Conditions: TranscodeConditions{
			MinHeight: 2160,
		},
	}
	disabledProfile := TranscodeProfile{
		Name:    "disabled",
		Enabled: false,
		Conditions: TranscodeConditions{
			CodecsInclude: []string{"hevc"},
		},
	}

	t.Run("no profiles returns nil", func(t *testing.T) {
		got := FindMatchingProfile(nil, "hevc", 1080)
		if got != nil {
			t.Errorf("expected nil, got %+v", got)
		}
	})

	t.Run("all disabled returns nil", func(t *testing.T) {
		got := FindMatchingProfile([]TranscodeProfile{disabledProfile}, "hevc", 1080)
		if got != nil {
			t.Errorf("expected nil, got %+v", got)
		}
	})

	t.Run("first match returned", func(t *testing.T) {
		profiles := []TranscodeProfile{hevcProfile, hdProfile}
		got := FindMatchingProfile(profiles, "hevc", 1080)
		if got == nil || got.Name != "hevc-to-h264" {
			t.Errorf("expected hevc-to-h264, got %v", got)
		}
	})

	t.Run("disabled profile skipped, second profile matches", func(t *testing.T) {
		profiles := []TranscodeProfile{disabledProfile, hdProfile}
		got := FindMatchingProfile(profiles, "hevc", 2160)
		if got == nil || got.Name != "4k-downscale" {
			t.Errorf("expected 4k-downscale, got %v", got)
		}
	})

	t.Run("no match returns nil", func(t *testing.T) {
		profiles := []TranscodeProfile{hevcProfile}
		got := FindMatchingProfile(profiles, "h264", 1080)
		if got != nil {
			t.Errorf("expected nil, got %+v", got)
		}
	})

	t.Run("returns pointer into slice (not copy)", func(t *testing.T) {
		profiles := []TranscodeProfile{hevcProfile}
		got := FindMatchingProfile(profiles, "hevc", 1080)
		if got == nil {
			t.Fatal("expected match")
		}
		got.Name = "mutated"
		if profiles[0].Name != "mutated" {
			t.Error("FindMatchingProfile should return pointer into the slice")
		}
	})
}

func TestNormalizeCodecName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"libx264", "h264"},
		{"libx265", "hevc"},
		{"libvpx-vp9", "vp9"},
		{"libaom-av1", "av1"},
		{"h264", "h264"},
		{"HEVC", "hevc"},
		{"copy", "copy"},
	}
	for _, c := range cases {
		if got := normalizeCodecName(c.in); got != c.want {
			t.Errorf("normalizeCodecName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShouldPassthrough(t *testing.T) {
	cases := []struct {
		name    string
		codecs  CodecInfo
		profile TranscodeOutput
		want    bool
	}{
		{
			"same codec, no height constraint → passthrough",
			CodecInfo{Video: "h264", Height: 720},
			TranscodeOutput{VideoCodec: "libx264"},
			true,
		},
		{
			"same codec, source within max height → passthrough",
			CodecInfo{Video: "h264", Height: 720},
			TranscodeOutput{VideoCodec: "libx264", MaxHeight: 1080},
			true,
		},
		{
			"same codec, source exceeds max height → transcode",
			CodecInfo{Video: "h264", Height: 2160},
			TranscodeOutput{VideoCodec: "libx264", MaxHeight: 1080},
			false,
		},
		{
			"different codec → transcode",
			CodecInfo{Video: "hevc", Height: 1080},
			TranscodeOutput{VideoCodec: "libx264"},
			false,
		},
		{
			"copy codec never passthrough",
			CodecInfo{Video: "h264", Height: 1080},
			TranscodeOutput{VideoCodec: "copy"},
			false,
		},
		{
			"encoder name matches codec name → passthrough",
			CodecInfo{Video: "hevc", Height: 1080},
			TranscodeOutput{VideoCodec: "libx265"},
			true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			profile := &TranscodeProfile{Output: c.profile}
			got := ShouldPassthrough(&c.codecs, profile)
			if got != c.want {
				t.Errorf("ShouldPassthrough(%+v, %+v) = %v, want %v", c.codecs, c.profile, got, c.want)
			}
		})
	}
}

func TestLoadProfiles(t *testing.T) {
	t.Run("missing directory returns nil", func(t *testing.T) {
		dir := t.TempDir()
		profiles, err := LoadProfiles(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if profiles != nil {
			t.Errorf("expected nil profiles, got %v", profiles)
		}
	})

	t.Run("valid profiles loaded", func(t *testing.T) {
		dir := t.TempDir()
		profileDir := filepath.Join(dir, "procula", "profiles")
		if err := os.MkdirAll(profileDir, 0755); err != nil {
			t.Fatal(err)
		}

		p1 := `{"name":"hevc","enabled":true,"conditions":{"codecs_include":["hevc"]},"output":{"video_codec":"libx264","audio_codec":"aac","suffix":".x264"}}`
		p2 := `{"name":"4k","enabled":false,"conditions":{"min_height":2160},"output":{"video_codec":"copy","audio_codec":"copy","suffix":".copy"}}`
		if err := os.WriteFile(filepath.Join(profileDir, "hevc.json"), []byte(p1), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(profileDir, "4k.json"), []byte(p2), 0644); err != nil {
			t.Fatal(err)
		}

		profiles, err := LoadProfiles(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(profiles) != 2 {
			t.Fatalf("expected 2 profiles, got %d", len(profiles))
		}
	})

	t.Run("invalid JSON skipped", func(t *testing.T) {
		dir := t.TempDir()
		profileDir := filepath.Join(dir, "procula", "profiles")
		if err := os.MkdirAll(profileDir, 0755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(profileDir, "bad.json"), []byte("not json{{{"), 0644); err != nil {
			t.Fatal(err)
		}
		valid := `{"name":"ok","enabled":true,"output":{"video_codec":"copy","audio_codec":"copy","suffix":""}}`
		if err := os.WriteFile(filepath.Join(profileDir, "ok.json"), []byte(valid), 0644); err != nil {
			t.Fatal(err)
		}

		profiles, err := LoadProfiles(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(profiles) != 1 || profiles[0].Name != "ok" {
			t.Errorf("expected 1 valid profile, got %v", profiles)
		}
	})

	t.Run("non-JSON files skipped", func(t *testing.T) {
		dir := t.TempDir()
		profileDir := filepath.Join(dir, "procula", "profiles")
		if err := os.MkdirAll(profileDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(profileDir, "readme.txt"), []byte("not a profile"), 0644); err != nil {
			t.Fatal(err)
		}

		profiles, err := LoadProfiles(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(profiles) != 0 {
			t.Errorf("expected 0 profiles, got %v", profiles)
		}
	})
}
