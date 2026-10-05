package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const mb = 1 << 20

// cannedProbe is a plausible ffprobe answer for a two-hour film.
const cannedProbe = `{
  "streams": [
    {"index":0,"codec_type":"video","codec_name":"h264","width":1920,"height":1080,"tags":{"language":"eng"}},
    {"index":1,"codec_type":"audio","codec_name":"aac","tags":{"language":"eng"}},
    {"index":2,"codec_type":"audio","codec_name":"ac3"},
    {"index":3,"codec_type":"subtitle","codec_name":"subrip","tags":{"language":"eng"}},
    {"index":4,"codec_type":"subtitle","codec_name":"hdmv_pgs_subtitle"}
  ],
  "format": {"filename":"x.mkv","duration":"7200.500000","size":"419430400"}
}`

// fakeProbe writes an executable shell script that prints stdout and exits 0.
func fakeProbe(t *testing.T, stdout string) string {
	t.Helper()
	return writeScript(t, "cat <<'PROBE_EOF'\n"+stdout+"\nPROBE_EOF\n")
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ffprobe")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// sparseFile creates a file of the given apparent size without writing it.
func sparseFile(t *testing.T, size int64) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "movie.mkv")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := os.Truncate(p, size); err != nil {
		t.Fatal(err)
	}
	return p
}

func probeWithDuration(sec string) string {
	return strings.Replace(cannedProbe, "7200.500000", sec, 1)
}

func TestValidatePass(t *testing.T) {
	path := sparseFile(t, 400*mb)
	res := Validate(context.Background(), fakeProbe(t, cannedProbe), path, 400*mb, 120)
	if !res.Passed || res.Reason != "" {
		t.Fatalf("want pass, got %+v", res)
	}
	if res.Integrity != "pass" || res.Sample != "pass" || res.Duration != "pass" {
		t.Errorf("checks = %s/%s/%s", res.Integrity, res.Sample, res.Duration)
	}
	if res.Video != "h264" || res.Width != 1920 || res.Height != 1080 {
		t.Errorf("video = %q %dx%d", res.Video, res.Width, res.Height)
	}
	if got := strings.Join(res.Audio, ","); got != "aac(eng),ac3" {
		t.Errorf("audio = %q", got)
	}
	if got := strings.Join(res.Subtitles, ","); got != "eng,hdmv_pgs_subtitle" {
		t.Errorf("subtitles = %q (a missing language falls back to the codec)", got)
	}
	if res.DurationSec != 7200.5 {
		t.Errorf("duration_sec = %v", res.DurationSec)
	}
}

func TestValidatePassesExactProbeArgs(t *testing.T) {
	path := sparseFile(t, 100*mb)
	argsFile := filepath.Join(t.TempDir(), "args")
	probe := writeScript(t, `echo "$@" > "`+argsFile+`"`+"\n"+"cat <<'E'\n"+cannedProbe+"\nE\n")
	res := Validate(context.Background(), probe, path, 100*mb, 0)
	if !res.Passed {
		t.Fatalf("want pass, got %+v", res)
	}
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	want := "-v error -show_format -show_streams -of json " + path
	if strings.TrimSpace(string(got)) != want {
		t.Errorf("ffprobe args = %q, want %q", strings.TrimSpace(string(got)), want)
	}
}

func TestValidateSampleFail(t *testing.T) {
	// Under 50 MB is always a sample, even with an unknown runtime.
	path := sparseFile(t, 10*mb)
	res := Validate(context.Background(), fakeProbe(t, cannedProbe), path, 10*mb, 0)
	if res.Passed {
		t.Fatalf("want fail, got %+v", res)
	}
	if res.Integrity != "pass" || res.Sample != "fail" || res.Duration != "skip" {
		t.Errorf("checks = %s/%s/%s", res.Integrity, res.Sample, res.Duration)
	}
	if !strings.Contains(res.Reason, "sample") {
		t.Errorf("reason = %q", res.Reason)
	}
}

func TestValidateSampleFailPerMinute(t *testing.T) {
	// 60 MB clears the absolute floor but is far under 3 MB/min for a 100 min film.
	path := sparseFile(t, 60*mb)
	res := Validate(context.Background(), fakeProbe(t, cannedProbe), path, 60*mb, 100)
	if res.Passed || res.Sample != "fail" {
		t.Fatalf("want sample fail, got %+v", res)
	}
	// The same file is fine when the runtime is unknown.
	res = Validate(context.Background(), fakeProbe(t, cannedProbe), path, 60*mb, 0)
	if !res.Passed || res.Duration != "skip" {
		t.Fatalf("want pass with unknown runtime, got %+v", res)
	}
}

func TestValidateSizeFallsBackToDisk(t *testing.T) {
	path := sparseFile(t, 10*mb)
	res := Validate(context.Background(), fakeProbe(t, cannedProbe), path, 0, 0)
	if res.Sample != "fail" {
		t.Fatalf("size 0 should use the on-disk size, got %+v", res)
	}
}

func TestValidateDuration(t *testing.T) {
	path := sparseFile(t, 400*mb)
	cases := []struct {
		name     string
		duration string
		runtime  int
		want     string
		passed   bool
	}{
		{"exact", "7200", 120, "pass", true},
		{"within 10 percent", "7800", 120, "pass", true},
		{"warn", "6000", 120, "warn", true},        // 16.7% short
		{"fail", "1800", 120, "fail", false},       // 75% short
		{"fail long", "14400", 120, "fail", false}, // 100% over
		{"unknown runtime", "1800", 0, "skip", true},
		{"unknown duration", "N/A", 120, "skip", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Validate(context.Background(), fakeProbe(t, probeWithDuration(tc.duration)), path, 400*mb, tc.runtime)
			if res.Duration != tc.want || res.Passed != tc.passed {
				t.Fatalf("duration=%q passed=%v, want %q/%v (%+v)", res.Duration, res.Passed, tc.want, tc.passed, res)
			}
			if res.Duration == "fail" && !strings.Contains(res.Reason, "duration mismatch") {
				t.Errorf("reason = %q", res.Reason)
			}
		})
	}
}

func TestValidateFFprobeError(t *testing.T) {
	path := sparseFile(t, 100*mb)
	probe := writeScript(t, "echo 'moov atom not found' >&2\nexit 1\n")
	res := Validate(context.Background(), probe, path, 100*mb, 0)
	if res.Passed || res.Integrity != "fail" || res.Sample != "skip" || res.Duration != "skip" {
		t.Fatalf("want integrity fail, got %+v", res)
	}
	if !strings.Contains(res.Reason, "moov atom not found") {
		t.Errorf("reason = %q", res.Reason)
	}
}

func TestValidateFFprobeGarbageAndMissingBinary(t *testing.T) {
	path := sparseFile(t, 100*mb)
	res := Validate(context.Background(), fakeProbe(t, "not json"), path, 100*mb, 0)
	if res.Passed || res.Integrity != "fail" {
		t.Fatalf("garbage output: %+v", res)
	}
	res = Validate(context.Background(), filepath.Join(t.TempDir(), "nope"), path, 100*mb, 0)
	if res.Passed || res.Integrity != "fail" || !strings.Contains(res.Reason, "ffprobe failed") {
		t.Fatalf("missing binary: %+v", res)
	}
}

func TestValidateFFprobeTimeout(t *testing.T) {
	old := probeTimeout
	probeTimeout = 100 * time.Millisecond
	defer func() { probeTimeout = old }()

	path := sparseFile(t, 100*mb)
	probe := writeScript(t, "exec sleep 30\n")
	start := time.Now()
	res := Validate(context.Background(), probe, path, 100*mb, 0)
	if res.Passed || res.Integrity != "fail" {
		t.Fatalf("want integrity fail, got %+v", res)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("timeout not enforced, took %v", time.Since(start))
	}
}

func TestValidateMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone.mkv")
	// The probe must not even be consulted; point at a binary that does not exist.
	res := Validate(context.Background(), "/nonexistent/ffprobe", missing, 100*mb, 0)
	if res.Passed || res.Integrity != "fail" || res.Sample != "skip" || res.Duration != "skip" {
		t.Fatalf("want integrity fail, got %+v", res)
	}
	if !strings.Contains(res.Reason, "file not found") {
		t.Errorf("reason = %q", res.Reason)
	}
}

func TestValidateNoVideoStream(t *testing.T) {
	path := sparseFile(t, 100*mb)
	audioOnly := `{"streams":[{"index":0,"codec_type":"audio","codec_name":"mp3"}],"format":{"duration":"200"}}`
	res := Validate(context.Background(), fakeProbe(t, audioOnly), path, 100*mb, 0)
	if res.Passed || res.Integrity != "fail" || res.Reason != "no video stream" {
		t.Fatalf("want no-video fail, got %+v", res)
	}
	if len(res.Audio) != 1 || res.Audio[0] != "mp3" {
		t.Errorf("audio still reported: %v", res.Audio)
	}

	// Cover art is not a video stream.
	cover := `{"streams":[{"index":0,"codec_type":"video","codec_name":"mjpeg","width":600,"height":600,"disposition":{"attached_pic":1}}],"format":{"duration":"200"}}`
	res = Validate(context.Background(), fakeProbe(t, cover), path, 100*mb, 0)
	if res.Passed || res.Reason != "no video stream" {
		t.Fatalf("cover art only: %+v", res)
	}
}
