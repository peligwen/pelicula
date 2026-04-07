package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// ── resolveOutputPath ────────────────────────────────────────────────────────

func TestResolveOutputPath_NoCollision(t *testing.T) {
	dir := t.TempDir()
	got := resolveOutputPath("/input/movie.mkv", ".x264", dir)
	want := filepath.Join(dir, "movie.x264.mkv")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveOutputPath_Collisions(t *testing.T) {
	dir := t.TempDir()

	// Create the initial output file so the collision logic kicks in
	base := filepath.Join(dir, "movie.x264.mkv")
	os.WriteFile(base, []byte("v1"), 0644)

	got := resolveOutputPath("/input/movie.mkv", ".x264", dir)
	want := filepath.Join(dir, "movie.x264.2.mkv")
	if got != want {
		t.Errorf("single collision: got %q, want %q", got, want)
	}

	// Create .2 as well, should skip to .3
	os.WriteFile(want, []byte("v2"), 0644)
	got = resolveOutputPath("/input/movie.mkv", ".x264", dir)
	want3 := filepath.Join(dir, "movie.x264.3.mkv")
	if got != want3 {
		t.Errorf("double collision: got %q, want %q", got, want3)
	}
}

func TestResolveOutputPath_DifferentInputs(t *testing.T) {
	dir := t.TempDir()
	out1 := resolveOutputPath("/input/movie.mkv", ".x264", dir)
	out2 := resolveOutputPath("/input/other.mkv", ".x264", dir)
	if out1 == out2 {
		t.Errorf("different inputs should produce different output paths")
	}
}

// ── Process tests using helper process pattern ──────────────────────────────

func setupFakeFFmpeg(t *testing.T) {
	t.Helper()
	// Create a shell script that acts as a fake ffmpeg.
	// The GO_TEST_FFMPEG env var controls behavior.
	dir := t.TempDir()
	script := filepath.Join(dir, "ffmpeg")
	content := `#!/bin/sh
# Get the output path (last argument)
for last; do true; done
case "$GO_TEST_FFMPEG" in
success)
    echo "  Duration: 00:01:00.00, start: 0.000000, bitrate: 5000 kb/s" >&2
    echo "frame=  100 fps=25 time=00:00:30.00 bitrate=5000kbits/s" >&2
    echo "frame=  200 fps=25 time=00:01:00.00 bitrate=5000kbits/s" >&2
    echo "fake video data" > "$last"
    exit 0
    ;;
fail)
    echo "Error: something went wrong" >&2
    exit 1
    ;;
*)
    echo "GO_TEST_FFMPEG not set or unknown: $GO_TEST_FFMPEG" >&2
    exit 1
    ;;
esac
`
	os.WriteFile(script, []byte(content), 0755)
	old := ffmpegCommand
	ffmpegCommand = script
	t.Cleanup(func() { ffmpegCommand = old })
}

func TestProcess_EmptyInput(t *testing.T) {
	job := &Job{Source: JobSource{Path: ""}}
	profile := &TranscodeProfile{Output: TranscodeOutput{Suffix: ".test"}}
	_, err := Process(context.Background(), job, profile, nil)
	if err == nil || err.Error() != "no input path" {
		t.Errorf("expected 'no input path' error, got %v", err)
	}
}

func TestProcess_Success(t *testing.T) {
	setupFakeFFmpeg(t)
	t.Setenv("GO_TEST_FFMPEG", "success")

	// Create a temp input file and processing dir
	dir := t.TempDir()
	input := filepath.Join(dir, "movie.mkv")
	os.WriteFile(input, []byte("input data"), 0644)

	// Use a temp processing dir so we don't need /processing on the host
	procDir := t.TempDir()

	job := &Job{Source: JobSource{Path: input}}
	profile := &TranscodeProfile{
		Name: "test",
		Output: TranscodeOutput{
			VideoCodec:  "libx264",
			VideoCRF:    22,
			VideoPreset: "ultrafast",
			AudioCodec:  "aac",
			Suffix:      ".test",
		},
	}

	var progressCalled bool
	progressFn := func(pct, _ float64) {
		progressCalled = true
	}

	out, err := processWithDir(context.Background(), job, profile, progressFn, procDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == "" {
		t.Error("expected non-empty output path")
	}
	if !progressCalled {
		t.Error("expected progress callback to be invoked")
	}
}

func TestProcess_FFmpegFails(t *testing.T) {
	setupFakeFFmpeg(t)
	t.Setenv("GO_TEST_FFMPEG", "fail")

	dir := t.TempDir()
	input := filepath.Join(dir, "movie.mkv")
	os.WriteFile(input, []byte("input data"), 0644)

	procDir := t.TempDir()
	job := &Job{Source: JobSource{Path: input}}
	profile := &TranscodeProfile{
		Name:   "test",
		Output: TranscodeOutput{VideoCodec: "libx264", AudioCodec: "aac", Suffix: ".test"},
	}

	_, err := processWithDir(context.Background(), job, profile, nil, procDir)
	if err == nil {
		t.Fatal("expected error from failing FFmpeg")
	}
	if !strings.Contains(err.Error(), "FFmpeg exited with error") {
		t.Errorf("error = %q, want it to contain 'FFmpeg exited with error'", err)
	}
}

// ── Atomic rename / partial cleanup tests ───────────────────────────────────

// TestProcess_AtomicRename verifies that on success the final file exists and
// no .partial file is left behind.
func TestProcess_AtomicRename(t *testing.T) {
	setupFakeFFmpeg(t)
	t.Setenv("GO_TEST_FFMPEG", "success")

	dir := t.TempDir()
	input := filepath.Join(dir, "movie.mkv")
	os.WriteFile(input, []byte("input"), 0644)

	job := &Job{Source: JobSource{Path: input}}
	profile := &TranscodeProfile{
		Name:   "test",
		Output: TranscodeOutput{VideoCodec: "libx264", AudioCodec: "aac", Suffix: " - 1080p"},
	}

	out, err := processWithDir(context.Background(), job, profile, nil, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Final file must exist
	if _, err := os.Stat(out); os.IsNotExist(err) {
		t.Errorf("final output file not found: %s", out)
	}
	// No .partial file should remain
	if _, err := os.Stat(out + ".partial"); !os.IsNotExist(err) {
		t.Errorf(".partial file still present after successful transcode")
	}
	// Output must be in the same directory as the input (sidecar)
	if filepath.Dir(out) != dir {
		t.Errorf("sidecar should be in %s, got %s", dir, filepath.Dir(out))
	}
}

// TestProcess_PartialCleanedOnFailure verifies that a .partial file is removed
// when FFmpeg fails (no orphaned partial output left behind).
func TestProcess_PartialCleanedOnFailure(t *testing.T) {
	setupFakeFFmpeg(t)
	t.Setenv("GO_TEST_FFMPEG", "fail")

	dir := t.TempDir()
	input := filepath.Join(dir, "movie.mkv")
	os.WriteFile(input, []byte("input"), 0644)

	job := &Job{Source: JobSource{Path: input}}
	profile := &TranscodeProfile{
		Name:   "test",
		Output: TranscodeOutput{VideoCodec: "libx264", AudioCodec: "aac", Suffix: ".test"},
	}

	_, err := processWithDir(context.Background(), job, profile, nil, dir)
	if err == nil {
		t.Fatal("expected error from failing FFmpeg")
	}

	// Verify no .partial file lingers
	pattern := filepath.Join(dir, "*.partial")
	matches, _ := filepath.Glob(pattern)
	if len(matches) > 0 {
		t.Errorf("partial file(s) not cleaned up after failure: %v", matches)
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
