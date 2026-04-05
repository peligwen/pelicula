package main

import (
	"testing"
)

func TestCheckSample(t *testing.T) {
	const MB = 1024 * 1024
	cases := []struct {
		name            string
		size            int64
		expectedMinutes int
		want            string
	}{
		{"below absolute floor", 10 * MB, 0, "fail"},
		{"just under 50 MB", 50*MB - 1, 0, "fail"},
		{"exactly 50 MB", 50 * MB, 0, "pass"},
		{"above 50 MB no runtime", 100 * MB, 0, "pass"},
		{"1 byte", 1, 0, "fail"},
		{"above floor meets per-min", 500 * MB, 120, "pass"},
		{"above floor fails per-min", 100 * MB, 120, "fail"}, // 100MB < 120*3MB=360MB
		{"meets per-min exactly", 60 * MB, 10, "pass"},       // 60MB > 50MB floor and >= 10*3MB=30MB
		{"just under per-min", 29*MB + 999, 10, "fail"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := checkSample(c.size, c.expectedMinutes)
			if got != c.want {
				t.Errorf("checkSample(%d, %d) = %q, want %q", c.size, c.expectedMinutes, got, c.want)
			}
		})
	}
}

func TestCheckDuration(t *testing.T) {
	cases := []struct {
		name            string
		duration        string
		expectedMinutes int
		want            string
	}{
		{"empty duration", "", 90, "skip"},
		{"zero expected minutes", "5400.0", 0, "skip"},
		{"both empty", "", 0, "skip"},
		{"exact match", "5400.0", 90, "pass"},       // 90*60=5400
		{"within 10%", "5800.0", 90, "pass"},        // dev ~7.4% — under the 10% warn threshold
		{"just over 10%", "5941.0", 90, "warn"},     // dev ~10.02% — just over warn threshold
		{"just under 50%", "2701.0", 90, "warn"},    // dev ~49.98%
		{"exactly 50%", "2700.0", 90, "warn"},       // dev = 0.5, not > 0.5, so warn
		{"over 50%", "2699.0", 90, "fail"},          // dev >50%
		{"unparseable", "abc", 90, "skip"},
		{"zero duration string", "0", 90, "skip"},
		{"negative-like", "-100", 90, "skip"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := checkDuration(c.duration, c.expectedMinutes)
			if got != c.want {
				t.Errorf("checkDuration(%q, %d) = %q, want %q", c.duration, c.expectedMinutes, got, c.want)
			}
		})
	}
}

func TestExtractCodecs(t *testing.T) {
	t.Run("video and audio", func(t *testing.T) {
		probe := &ffprobeOutput{
			Streams: []ffprobeStream{
				{CodecType: "video", CodecName: "hevc", Width: 1920, Height: 1080},
				{CodecType: "audio", CodecName: "aac"},
			},
		}
		got := extractCodecs(probe)
		if got.Video != "hevc" {
			t.Errorf("Video = %q, want %q", got.Video, "hevc")
		}
		if got.Width != 1920 || got.Height != 1080 {
			t.Errorf("dims = %dx%d, want 1920x1080", got.Width, got.Height)
		}
		if got.Audio != "aac" {
			t.Errorf("Audio = %q, want %q", got.Audio, "aac")
		}
		if len(got.Subtitles) != 0 {
			t.Errorf("unexpected subtitles: %v", got.Subtitles)
		}
	})

	t.Run("first video stream wins", func(t *testing.T) {
		probe := &ffprobeOutput{
			Streams: []ffprobeStream{
				{CodecType: "video", CodecName: "h264", Width: 1280, Height: 720},
				{CodecType: "video", CodecName: "hevc", Width: 1920, Height: 1080},
			},
		}
		got := extractCodecs(probe)
		if got.Video != "h264" {
			t.Errorf("Video = %q, want first stream %q", got.Video, "h264")
		}
	})

	t.Run("subtitle with language tag", func(t *testing.T) {
		probe := &ffprobeOutput{
			Streams: []ffprobeStream{
				{CodecType: "subtitle", CodecName: "subrip", Tags: map[string]string{"language": "eng"}},
				{CodecType: "subtitle", CodecName: "subrip", Tags: map[string]string{"language": "spa"}},
			},
		}
		got := extractCodecs(probe)
		if len(got.Subtitles) != 2 || got.Subtitles[0] != "eng" || got.Subtitles[1] != "spa" {
			t.Errorf("Subtitles = %v, want [eng spa]", got.Subtitles)
		}
	})

	t.Run("subtitle falls back to codec name", func(t *testing.T) {
		probe := &ffprobeOutput{
			Streams: []ffprobeStream{
				{CodecType: "subtitle", CodecName: "dvd_subtitle"},
			},
		}
		got := extractCodecs(probe)
		if len(got.Subtitles) != 1 || got.Subtitles[0] != "dvd_subtitle" {
			t.Errorf("Subtitles = %v, want [dvd_subtitle]", got.Subtitles)
		}
	})

	t.Run("empty streams", func(t *testing.T) {
		probe := &ffprobeOutput{}
		got := extractCodecs(probe)
		if got.Video != "" || got.Audio != "" || got.Width != 0 || len(got.Subtitles) != 0 {
			t.Errorf("expected zero CodecInfo, got %+v", got)
		}
	})

	t.Run("audio only", func(t *testing.T) {
		probe := &ffprobeOutput{
			Streams: []ffprobeStream{
				{CodecType: "audio", CodecName: "flac"},
			},
		}
		got := extractCodecs(probe)
		if got.Video != "" {
			t.Errorf("Video = %q, want empty for audio-only", got.Video)
		}
		if got.Audio != "flac" {
			t.Errorf("Audio = %q, want %q", got.Audio, "flac")
		}
	})
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		b    int64
		want string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{1536 * 1024 * 1024, "1.5 GB"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			got := formatBytes(c.b)
			if got != c.want {
				t.Errorf("formatBytes(%d) = %q, want %q", c.b, got, c.want)
			}
		})
	}
}

func TestIsAllowedPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/downloads/movie.mkv", true},
		{"/movies/Alien/alien.mkv", true},
		{"/tv/show/s01e01.mkv", true},
		{"/processing/out.mkv", true},
		{"/downloads", true},  // exact prefix match via filepath.Clean
		{"/etc/passwd", false},
		{"/home/user/file.mkv", false},
		{"", false},
		{"/downloads/../etc/passwd", false}, // traversal resolved by filepath.Clean
		{"/var/downloads/file.mkv", false},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			got := isAllowedPath(c.path)
			if got != c.want {
				t.Errorf("isAllowedPath(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}
