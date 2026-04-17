//go:build integration

package procula

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const (
	cacheDir  = "testcache"
	cacheFile = "testcache/generated_film.mp4"
	filmTitle = "Generated Test Film"
	filmYear  = 2024
	filmMin   = 2 // expected runtime in minutes

	minFileSize = 50 * 1024 * 1024 // 50 MB — must exceed validation floor
)

// ensureTestFilm generates a ~55+ MB test video with FFmpeg on first run,
// then returns the cached path on subsequent runs.
func ensureTestFilm(t *testing.T) string {
	t.Helper()

	abs, err := filepath.Abs(cacheFile)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	// Use cache if it exists and is large enough.
	if info, err := os.Stat(abs); err == nil && info.Size() > minFileSize {
		return abs
	}

	requireTool(t, "ffmpeg")

	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		t.Fatalf("mkdir testcache: %v", err)
	}

	// 2 minutes, 720p mandelbrot + sine tone, ultrafast/CRF 18 → ~55-80 MB.
	tmp := abs + ".tmp"
	cmd := exec.Command("ffmpeg",
		"-f", "lavfi", "-i", "mandelbrot=s=1280x720:rate=24",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "18",
		"-c:a", "aac",
		"-t", "120",
		"-y", tmp,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.Remove(tmp)
		t.Fatalf("ffmpeg generate: %v", err)
	}

	// Atomic rename so a partial file is never used as cache.
	if err := os.Rename(tmp, abs); err != nil {
		os.Remove(tmp)
		t.Fatalf("rename: %v", err)
	}

	info, err := os.Stat(abs)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() < minFileSize {
		t.Fatalf("generated file too small (%d bytes) — tune FFmpeg params", info.Size())
	}

	return abs
}

func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s not found on PATH, skipping", name)
	}
}

func copyToTemp(t *testing.T, src string) string {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	defer in.Close()

	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("create dest: %v", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		t.Fatalf("copy: %v", err)
	}
	return dst
}

func TestIntegration_Pipeline(t *testing.T) {
	requireTool(t, "ffprobe")
	cachedPath := ensureTestFilm(t)

	t.Run("happy_path", func(t *testing.T) {
		workPath := copyToTemp(t, cachedPath)

		overrideSettings(t, PipelineSettings{
			ValidationEnabled:  true,
			DeleteOnFailure:    false,
			TranscodingEnabled: false,
			CatalogEnabled:     true,
			NotifMode:          "internal",
		})

		q := newTestQueue(t)
		api := fakePeliculaAPI(t)
		cfgDir := t.TempDir()

		src := JobSource{
			Type:                   "movie",
			Title:                  filmTitle,
			Year:                   filmYear,
			Path:                   workPath,
			Size:                   fileSize(t, workPath),
			ArrType:                "radarr",
			ExpectedRuntimeMinutes: filmMin,
		}
		job, err := q.Create(src)
		if err != nil {
			t.Fatalf("create job: %v", err)
		}

		processJob(q, job.ID, cfgDir, api)

		job, ok := q.Get(job.ID)
		if !ok {
			t.Fatal("job not found after processing")
		}
		if job.State != StateCompleted {
			t.Fatalf("state = %q, want %q (error: %s)", job.State, StateCompleted, job.Error)
		}
		if job.Validation == nil {
			t.Fatal("validation result is nil")
		}
		if !job.Validation.Passed {
			t.Fatalf("validation failed: %+v", job.Validation.Checks)
		}
		if job.Validation.Checks.Integrity != "pass" {
			t.Errorf("integrity = %q, want pass", job.Validation.Checks.Integrity)
		}
		if job.Validation.Checks.Sample != "pass" {
			t.Errorf("sample = %q, want pass", job.Validation.Checks.Sample)
		}
		if job.Validation.Checks.Codecs == nil {
			t.Error("codecs should be populated")
		} else {
			if job.Validation.Checks.Codecs.Video != "h264" {
				t.Errorf("video codec = %q, want h264", job.Validation.Checks.Codecs.Video)
			}
			if job.Validation.Checks.Codecs.Audio != "aac" {
				t.Errorf("audio codec = %q, want aac", job.Validation.Checks.Codecs.Audio)
			}
		}
	})

	t.Run("degraded_truncation", func(t *testing.T) {
		workPath := copyToTemp(t, cachedPath)

		// Truncate to 30% — short enough to cause duration mismatch,
		// or integrity failure if moov atom was at the end.
		info, err := os.Stat(workPath)
		if err != nil {
			t.Fatal(err)
		}
		truncSize := int64(float64(info.Size()) * 0.30)
		if err := os.Truncate(workPath, truncSize); err != nil {
			t.Fatalf("truncate: %v", err)
		}

		job := &Job{
			Source: JobSource{
				Path:                   workPath,
				ExpectedRuntimeMinutes: filmMin,
			},
		}
		result, reason := Validate(job)
		if result.Passed {
			t.Fatal("expected validation to fail on truncated file")
		}
		// Truncation may break integrity (moov at end) or duration (moov at start).
		integrityFailed := result.Checks.Integrity == "fail"
		durationFailed := result.Checks.Duration == "fail"
		if !integrityFailed && !durationFailed {
			t.Errorf("expected integrity or duration to fail, got: %+v (reason: %s)", result.Checks, reason)
		}
	})

	t.Run("degraded_corruption", func(t *testing.T) {
		workPath := copyToTemp(t, cachedPath)

		// Overwrite the MP4 header with zeros — FFprobe cannot parse the file.
		f, err := os.OpenFile(workPath, os.O_WRONLY, 0644)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(make([]byte, 4096)); err != nil {
			f.Close()
			t.Fatal(err)
		}
		f.Close()

		job := &Job{
			Source: JobSource{
				Path:                   workPath,
				ExpectedRuntimeMinutes: filmMin,
			},
		}
		result, _ := Validate(job)
		if result.Passed {
			t.Fatal("expected validation to fail on corrupted file")
		}
		if result.Checks.Integrity != "fail" {
			t.Errorf("integrity = %q, want fail", result.Checks.Integrity)
		}
	})

	t.Run("degraded_undersized", func(t *testing.T) {
		requireTool(t, "ffmpeg")

		// Re-encode 5 seconds at very low bitrate — valid video but tiny (<1 MB).
		dst := filepath.Join(t.TempDir(), "tiny.mp4")
		cmd := exec.Command("ffmpeg",
			"-i", cachedPath,
			"-t", "5",
			"-c:v", "libx264", "-b:v", "30k",
			"-c:a", "aac", "-b:a", "16k",
			"-y", dst,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg re-encode: %v\n%s", err, out)
		}

		job := &Job{
			Source: JobSource{
				Path:                   dst,
				ExpectedRuntimeMinutes: filmMin,
			},
		}
		result, _ := Validate(job)
		if result.Passed {
			t.Fatal("expected validation to fail on undersized file")
		}
		if result.Checks.Sample != "fail" {
			t.Errorf("sample = %q, want fail", result.Checks.Sample)
		}
	})
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
}
