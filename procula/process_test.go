package main

import (
	"slices"
	"testing"
)

func TestBuildFFmpegArgs(t *testing.T) {
	t.Run("full transcode profile", func(t *testing.T) {
		p := &TranscodeProfile{
			Output: TranscodeOutput{
				VideoCodec:    "libx264",
				VideoCRF:      22,
				VideoPreset:   "medium",
				MaxHeight:     1080,
				AudioCodec:    "aac",
				AudioChannels: 2,
				Suffix:        ".x264",
			},
		}
		args := buildFFmpegArgs("/input/movie.mkv", "/output/movie.x264.mkv", p)
		mustContainSeq(t, args, "-i", "/input/movie.mkv")
		mustContain(t, args, "-y")
		mustContainSeq(t, args, "-c:v", "libx264")
		mustContainSeq(t, args, "-crf", "22")
		mustContainSeq(t, args, "-preset", "medium")
		mustContainSeq(t, args, "-vf", "scale=-2:1080")
		mustContainSeq(t, args, "-c:a", "aac")
		mustContainSeq(t, args, "-ac", "2")
		mustContainSeq(t, args, "-c:s", "copy")
		mustEndWith(t, args, "/output/movie.x264.mkv")
	})

	t.Run("video copy skips CRF/preset/scale", func(t *testing.T) {
		p := &TranscodeProfile{
			Output: TranscodeOutput{
				VideoCodec: "copy",
				AudioCodec: "copy",
			},
		}
		args := buildFFmpegArgs("/in.mkv", "/out.mkv", p)
		mustNotContain(t, args, "-crf")
		mustNotContain(t, args, "-preset")
		mustNotContain(t, args, "-vf")
		mustNotContain(t, args, "-ac")
	})

	t.Run("zero CRF omitted", func(t *testing.T) {
		p := &TranscodeProfile{
			Output: TranscodeOutput{
				VideoCodec: "libx265",
				VideoCRF:   0,
				AudioCodec: "copy",
			},
		}
		args := buildFFmpegArgs("/in.mkv", "/out.mkv", p)
		mustNotContain(t, args, "-crf")
	})

	t.Run("zero MaxHeight omitted", func(t *testing.T) {
		p := &TranscodeProfile{
			Output: TranscodeOutput{
				VideoCodec: "libx264",
				VideoCRF:   18,
				MaxHeight:  0,
				AudioCodec: "copy",
			},
		}
		args := buildFFmpegArgs("/in.mkv", "/out.mkv", p)
		mustNotContain(t, args, "-vf")
	})

	t.Run("zero AudioChannels omitted", func(t *testing.T) {
		p := &TranscodeProfile{
			Output: TranscodeOutput{
				VideoCodec:    "copy",
				AudioCodec:    "aac",
				AudioChannels: 0,
			},
		}
		args := buildFFmpegArgs("/in.mkv", "/out.mkv", p)
		mustNotContain(t, args, "-ac")
	})

	t.Run("always includes subtitle copy and output path", func(t *testing.T) {
		p := &TranscodeProfile{
			Output: TranscodeOutput{VideoCodec: "copy", AudioCodec: "copy"},
		}
		args := buildFFmpegArgs("/in.mkv", "/out.mkv", p)
		mustContainSeq(t, args, "-c:s", "copy")
		mustEndWith(t, args, "/out.mkv")
	})
}

func TestParseDuration(t *testing.T) {
	cases := []struct {
		line string
		want float64
	}{
		{"  Duration: 01:30:00.00, start: 0.000000, bitrate: 5000 kb/s", 5400.0},
		{"  Duration: 00:01:30.50, start: 0.000000, bitrate: 128 kb/s", 90.5},
		{"  Duration: 00:00:00.00, start: 0.000000, bitrate: 0 kb/s", -1},
		{"no duration here", -1},
		{"", -1},
		{"  Duration: 02:00:00.00, start:", 7200.0},
	}
	for _, c := range cases {
		t.Run(c.line, func(t *testing.T) {
			got := parseDuration(c.line)
			if got != c.want {
				t.Errorf("parseDuration(%q) = %v, want %v", c.line, got, c.want)
			}
		})
	}
}

func TestParseProgress(t *testing.T) {
	cases := []struct {
		line         string
		durationSecs float64
		want         float64
	}{
		{"frame= 100 fps=25 time=00:45:00.00 bitrate=", 5400.0, 0.5},
		{"time=01:00:00.00", 3600.0, 1.0},
		{"time=02:00:00.00", 3600.0, 1.0}, // capped at 1.0
		{"time=00:00:00.00", 3600.0, 0.0},
		{"no time here", 3600.0, -1},
		{"", 3600.0, -1},
	}
	for _, c := range cases {
		t.Run(c.line, func(t *testing.T) {
			got := parseProgress(c.line, c.durationSecs)
			if got != c.want {
				t.Errorf("parseProgress(%q, %v) = %v, want %v", c.line, c.durationSecs, got, c.want)
			}
		})
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func mustContain(t *testing.T, args []string, flag string) {
	t.Helper()
	if !slices.Contains(args, flag) {
		t.Errorf("expected %q in args %v", flag, args)
	}
}

func mustNotContain(t *testing.T, args []string, flag string) {
	t.Helper()
	if slices.Contains(args, flag) {
		t.Errorf("unexpected %q in args %v", flag, args)
	}
}

func mustContainSeq(t *testing.T, args []string, a, b string) {
	t.Helper()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return
		}
	}
	t.Errorf("expected %q %q adjacent in args %v", a, b, args)
}

func mustEndWith(t *testing.T, args []string, last string) {
	t.Helper()
	if len(args) == 0 || args[len(args)-1] != last {
		t.Errorf("expected args to end with %q, got %v", last, args)
	}
}
