package main

import (
	"os"
	"path/filepath"
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
		{"exact match", "5400.0", 90, "pass"},    // 90*60=5400
		{"within 10%", "5800.0", 90, "pass"},     // dev ~7.4% — under the 10% warn threshold
		{"just over 10%", "5941.0", 90, "warn"},  // dev ~10.02% — just over warn threshold
		{"just under 50%", "2701.0", 90, "warn"}, // dev ~49.98%
		{"exactly 50%", "2700.0", 90, "warn"},    // dev = 0.5, not > 0.5, so warn
		{"over 50%", "2699.0", 90, "fail"},       // dev >50%
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
	// isAllowedPath is the delete-gate: only /downloads and /processing are safe
	// to delete from on validation failure. Library paths under /media/ are excluded
	// by design to prevent an attacker-controlled webhook path from deleting imported media.
	cases := []struct {
		path string
		want bool
	}{
		{"/downloads/movie.mkv", true},
		{"/processing/out.mkv", true},
		{"/downloads", true},                     // exact prefix match via filepath.Clean
		{"/media/movies/Alien/alien.mkv", false}, // imported media — never delete
		{"/media/tv/show/s01e01.mkv", false},     // imported media — never delete
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

func TestIsAllowedJobPath(t *testing.T) {
	// isAllowedJobPath is the job-creation gate: accepts all known media directories.
	cases := []struct {
		path string
		want bool
	}{
		{"/downloads/movie.mkv", true},
		{"/media/movies/Alien/alien.mkv", true},
		{"/media/tv/show/s01e01.mkv", true},
		{"/media/custom/file.mkv", true},
		{"/processing/out.mkv", true},
		{"/etc/passwd", false},
		{"/home/user/file.mkv", false},
		{"", false},
		{"/downloads/../etc/passwd", false},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			got := isAllowedJobPath(c.path)
			if got != c.want {
				t.Errorf("isAllowedJobPath(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

func TestNormalizeLangCode(t *testing.T) {
	cases := []struct{ in, want string }{
		{"eng", "en"},
		{"spa", "es"},
		{"fre", "fr"},
		{"fra", "fr"},
		{"en", "en"},
		{"es", "es"},
		{"en-US", "en"},
		{"ENG", "en"},
		{"unknown", "unknown"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := normalizeLangCode(c.in)
			if got != c.want {
				t.Errorf("normalizeLangCode(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestCheckMissingSubtitles(t *testing.T) {
	t.Run("env unset — no check", func(t *testing.T) {
		t.Setenv("PELICULA_SUB_LANGS", "")
		got := checkMissingSubtitles([]string{"eng", "spa"})
		if got != nil {
			t.Errorf("expected nil when env unset, got %v", got)
		}
	})

	t.Run("all configured langs present (3-letter)", func(t *testing.T) {
		t.Setenv("PELICULA_SUB_LANGS", "en,es")
		got := checkMissingSubtitles([]string{"eng", "spa"})
		if len(got) != 0 {
			t.Errorf("expected no missing, got %v", got)
		}
	})

	t.Run("all configured langs present (2-letter)", func(t *testing.T) {
		t.Setenv("PELICULA_SUB_LANGS", "en,es")
		got := checkMissingSubtitles([]string{"en", "es"})
		if len(got) != 0 {
			t.Errorf("expected no missing, got %v", got)
		}
	})

	t.Run("one lang missing", func(t *testing.T) {
		t.Setenv("PELICULA_SUB_LANGS", "en,es")
		got := checkMissingSubtitles([]string{"eng"})
		if len(got) != 1 || got[0] != "es" {
			t.Errorf("expected [es], got %v", got)
		}
	})

	t.Run("all langs missing — no embedded subs", func(t *testing.T) {
		t.Setenv("PELICULA_SUB_LANGS", "en,fr")
		got := checkMissingSubtitles(nil)
		if len(got) != 2 {
			t.Errorf("expected [en fr], got %v", got)
		}
	})

	t.Run("extra embedded langs don't cause false missing", func(t *testing.T) {
		t.Setenv("PELICULA_SUB_LANGS", "en")
		got := checkMissingSubtitles([]string{"eng", "spa", "fra"})
		if len(got) != 0 {
			t.Errorf("expected no missing, got %v", got)
		}
	})
}

// ── Validate integration tests using helper process pattern ─────────────────

func setupFakeFFprobe(t *testing.T) {
	t.Helper()
	// Create a shell script that acts as a fake ffprobe.
	// The GO_TEST_FFPROBE env var controls behavior.
	dir := t.TempDir()
	script := filepath.Join(dir, "ffprobe")
	content := `#!/bin/sh
case "$GO_TEST_FFPROBE" in
success)
    # The last argument is the file path
    for last; do true; done
    cat <<EOJSON
{"format":{"filename":"$last","duration":"7200.0","size":"500000000"},"streams":[{"codec_type":"video","codec_name":"h264","width":1920,"height":1080},{"codec_type":"audio","codec_name":"aac"}]}
EOJSON
    exit 0
    ;;
fail)
    echo "ffprobe error: invalid data" >&2
    exit 1
    ;;
*)
    echo "GO_TEST_FFPROBE not set or unknown: $GO_TEST_FFPROBE" >&2
    exit 1
    ;;
esac
`
	os.WriteFile(script, []byte(content), 0755)
	old := ffprobeCommand
	ffprobeCommand = script
	t.Cleanup(func() { ffprobeCommand = old })
}

func TestValidate_FileNotFound(t *testing.T) {
	job := &Job{Source: JobSource{Path: "/nonexistent/movie.mkv"}}
	result, reason := Validate(job)
	if result.Passed {
		t.Error("expected validation to fail for missing file")
	}
	if result.Checks.Integrity != "fail" {
		t.Errorf("integrity = %q, want fail", result.Checks.Integrity)
	}
	if reason == "" {
		t.Error("expected non-empty failure reason")
	}
}

func TestValidate_FFprobeFails(t *testing.T) {
	setupFakeFFprobe(t)
	t.Setenv("GO_TEST_FFPROBE", "fail")

	// Create a real file so os.Stat passes
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")
	os.WriteFile(path, []byte("data"), 0644)

	job := &Job{Source: JobSource{Path: path}}
	result, reason := Validate(job)
	if result.Passed {
		t.Error("expected validation to fail when ffprobe fails")
	}
	if result.Checks.Integrity != "fail" {
		t.Errorf("integrity = %q, want fail", result.Checks.Integrity)
	}
	if reason == "" {
		t.Error("expected non-empty failure reason")
	}
}

func TestValidate_SampleTooSmall(t *testing.T) {
	setupFakeFFprobe(t)
	t.Setenv("GO_TEST_FFPROBE", "success")

	// Create a file that's too small (< 50 MB)
	dir := t.TempDir()
	path := filepath.Join(dir, "tiny.mkv")
	os.WriteFile(path, make([]byte, 1024), 0644) // 1 KB

	job := &Job{Source: JobSource{
		Path:                   path,
		ExpectedRuntimeMinutes: 120,
	}}
	result, reason := Validate(job)
	if result.Passed {
		t.Error("expected validation to fail for tiny file")
	}
	if result.Checks.Sample != "fail" {
		t.Errorf("sample = %q, want fail", result.Checks.Sample)
	}
	if reason == "" {
		t.Error("expected non-empty failure reason")
	}
}

func TestValidate_FullPass(t *testing.T) {
	setupFakeFFprobe(t)
	t.Setenv("GO_TEST_FFPROBE", "success")

	// Create a large enough file — must exceed both the 50 MB absolute floor
	// and the per-minute floor (expectedMinutes * 3 MB).
	// With 0 expected minutes, only the absolute 50 MB floor applies
	// and duration check is skipped.
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Truncate(60 * 1024 * 1024) // 60 MB
	f.Close()

	job := &Job{Source: JobSource{
		Path:                   path,
		ExpectedRuntimeMinutes: 0,
	}}
	result, reason := Validate(job)
	if !result.Passed {
		t.Fatalf("expected validation to pass, reason: %s", reason)
	}
	if result.Checks.Integrity != "pass" {
		t.Errorf("integrity = %q, want pass", result.Checks.Integrity)
	}
	if result.Checks.Codecs == nil {
		t.Fatal("codecs should be populated")
	}
	if result.Checks.Codecs.Video != "h264" {
		t.Errorf("video codec = %q, want h264", result.Checks.Codecs.Video)
	}
	if result.Checks.Codecs.Audio != "aac" {
		t.Errorf("audio codec = %q, want aac", result.Checks.Codecs.Audio)
	}
}
