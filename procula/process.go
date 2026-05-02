package procula

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reDuration = regexp.MustCompile(`Duration:\s+(\d+):(\d+):([\d.]+)`)
	reTime     = regexp.MustCompile(`time=\s*(\d+):(\d+):([\d.]+)`)
	// ffmpegCommand is set at startup; overridden in tests to inject a mock binary.
	ffmpegCommand = "ffmpeg"
	// transcodeTimeoutFn computes the wall-clock deadline for a transcode subprocess.
	// Overridden in tests to inject short timeouts without spawning real media.
	transcodeTimeoutFn = transcodeTimeout
)

// transcodeTimeout returns a generous wall-clock budget for a single FFmpeg
// transcode. The 6× factor covers software encodes on slow CPUs (Synology,
// Pi-class) where libx265 at slower presets can run well below real-time.
func transcodeTimeout(expectedRuntimeMinutes int) time.Duration {
	const floor = 60 * time.Minute
	const cap = 24 * time.Hour
	if expectedRuntimeMinutes <= 0 {
		return floor
	}
	d := time.Duration(expectedRuntimeMinutes) * 6 * time.Minute
	if d < floor {
		d = floor
	}
	if d > cap {
		d = cap
	}
	return d
}

// Process runs FFmpeg to transcode the job's source file using the given profile.
// The transcoded file is written as a sidecar alongside the source (same directory)
// using an atomic .partial → final rename so no half-written file is ever visible.
// progressFn is called at most once per second with (pct 0.0–1.0, etaSecs).
// Returns the sidecar file path on success.
func Process(ctx context.Context, job *Job, profile *TranscodeProfile, progressFn func(pct, etaSecs float64)) (string, error) {
	if job.Source.Path == "" {
		return "", fmt.Errorf("no input path")
	}
	outputDir := filepath.Dir(job.Source.Path)
	return processWithDir(ctx, job, profile, progressFn, outputDir)
}

func processWithDir(ctx context.Context, job *Job, profile *TranscodeProfile, progressFn func(pct, etaSecs float64), outputDir string) (string, error) {
	input := job.Source.Path
	if input == "" {
		return "", fmt.Errorf("no input path")
	}

	finalPath := resolveOutputPath(input, profile.Output.Suffix, outputDir)
	partialPath := finalPath + ".partial"

	// Always clean up the partial file on any non-success exit.
	succeeded := false
	defer func() {
		if !succeeded {
			os.Remove(partialPath) //nolint:errcheck
		}
	}()

	// Apply a hard wall-clock deadline. ctx is the parent cancellable context
	// (from q.registerCancel). We derive a child timeout here so we can
	// distinguish our timeout from a user-initiated cancel after cmd.Wait().
	timeoutCtx, timeoutCancel := context.WithTimeout(ctx, transcodeTimeoutFn(job.Source.ExpectedRuntimeMinutes))
	defer timeoutCancel()

	var codecs *CodecInfo
	if job.Validation != nil {
		codecs = job.Validation.Checks.Codecs
	}
	args := buildFFmpegArgs(input, partialPath, profile, codecs)
	slog.Info("starting FFmpeg transcode", "component", "process", "input", input, "output", finalPath, "profile", profile.Name)

	cmd := exec.CommandContext(timeoutCtx, ffmpegCommand, args...)
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start FFmpeg: %w", err)
	}

	// Parse duration and time progress from FFmpeg stderr.
	// Throttle updates to ~1/s and compute a simple ETA.
	var (
		durationSecs       float64
		startTime          time.Time
		lastProgressUpdate time.Time
	)
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()
		if durationSecs == 0 {
			if d := parseDuration(line); d > 0 {
				durationSecs = d
			}
		}
		if durationSecs > 0 {
			if pct := parseProgress(line, durationSecs); pct >= 0 && progressFn != nil {
				now := time.Now()
				if startTime.IsZero() && pct > 0 {
					startTime = now
				}
				if now.Sub(lastProgressUpdate) >= time.Second {
					var etaSecs float64
					if !startTime.IsZero() && pct > 0 && pct < 1 {
						elapsed := now.Sub(startTime).Seconds()
						etaSecs = elapsed * (1 - pct) / pct
					}
					progressFn(pct, etaSecs)
					lastProgressUpdate = now
				}
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		// Check for our hard timeout before the raw error. When the timeout
		// fires, exec.CommandContext kills ffmpeg with SIGKILL, so cmd.Wait()
		// returns "signal: killed" — which is in the transient list. We must
		// intercept the deadline here and return the permanent sentinel instead.
		// We also verify the parent ctx is still live to avoid mis-classifying
		// a user-initiated cancel (which also sends SIGKILL) as a timeout.
		if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return "", fmt.Errorf("ffmpeg killed after deadline: %w", errTranscodeTimeout)
		}
		return "", fmt.Errorf("FFmpeg exited with error: %w", err)
	}

	if err := os.Rename(partialPath, finalPath); err != nil {
		return "", fmt.Errorf("rename output: %w", err)
	}
	succeeded = true

	slog.Info("transcoding complete", "component", "process", "output", finalPath)
	return finalPath, nil
}

// resolveOutputPath computes the output path for a transcode job,
// appending a counter suffix to avoid overwriting existing files.
func resolveOutputPath(input, suffix, processingDir string) string {
	base := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
	outputPath := filepath.Join(processingDir, base+suffix+".mkv")
	if _, err := os.Stat(outputPath); err == nil {
		for i := 2; ; i++ {
			candidate := filepath.Join(processingDir, fmt.Sprintf("%s%s.%d.mkv", base, suffix, i))
			if _, err := os.Stat(candidate); os.IsNotExist(err) {
				outputPath = candidate
				break
			}
		}
	}
	return outputPath
}

// preferredAudioLangVal is the normalized preferred audio language, loaded once
// at startup from PELICULA_AUDIO_LANG. Defaults to "en".
var preferredAudioLangVal = "en"

// preferredAudioLang returns the preferred audio language resolved at startup.
func preferredAudioLang() string { return preferredAudioLangVal }

func buildFFmpegArgs(input, output string, p *TranscodeProfile, codecs *CodecInfo) []string {
	args := []string{
		"-nostdin", // never read from stdin
		"-y",       // overwrite output without prompting
		"-i", input,
	}

	// Explicit stream mapping: preserve all video, audio, and subtitle streams.
	// Without -map, FFmpeg picks only one "best" stream per type — dropping all
	// other audio tracks (e.g., keeping only Italian when English is also present).
	// The ? on subtitles makes it optional so files without subtitle streams don't fail.
	args = append(args, "-map", "0:v", "-map", "0:a", "-map", "0:s?")

	// Video
	args = append(args, "-c:v", p.Output.VideoCodec)
	if p.Output.VideoCodec != "copy" {
		if p.Output.VideoCRF > 0 {
			args = append(args, "-crf", strconv.Itoa(p.Output.VideoCRF))
		}
		if p.Output.VideoPreset != "" {
			args = append(args, "-preset", p.Output.VideoPreset)
		}
		if p.Output.MaxHeight > 0 {
			// Scale to MaxHeight, keeping aspect ratio; -2 ensures even width for codec compatibility
			args = append(args, "-vf", fmt.Sprintf("scale=-2:%d", p.Output.MaxHeight))
		}
	}

	// Audio
	args = append(args, "-c:a", p.Output.AudioCodec)
	if p.Output.AudioCodec != "copy" && p.Output.AudioChannels > 0 {
		args = append(args, "-ac", strconv.Itoa(p.Output.AudioChannels))
	}

	// Set default audio disposition on the preferred language track.
	if codecs != nil && len(codecs.AudioTracks) > 0 {
		preferred := preferredAudioLang()
		for i, track := range codecs.AudioTracks {
			if normalizeLangCode(track.Language) == preferred {
				// Clear default on all audio tracks, then set it on the preferred one.
				args = append(args, "-disposition:a", "0")
				args = append(args, fmt.Sprintf("-disposition:a:%d", i), "default")
				break
			}
		}
	}

	// Subtitles — always copy through
	args = append(args, "-c:s", "copy")

	args = append(args, output)
	return args
}

func parseDuration(line string) float64 {
	m := reDuration.FindStringSubmatch(line)
	if m == nil {
		return -1
	}
	h, _ := strconv.ParseFloat(m[1], 64)
	min, _ := strconv.ParseFloat(m[2], 64)
	sec, _ := strconv.ParseFloat(m[3], 64)
	total := h*3600 + min*60 + sec
	if total <= 0 {
		return -1
	}
	return total
}

func parseProgress(line string, durationSecs float64) float64 {
	m := reTime.FindStringSubmatch(line)
	if m == nil {
		return -1
	}
	h, _ := strconv.ParseFloat(m[1], 64)
	min, _ := strconv.ParseFloat(m[2], 64)
	sec, _ := strconv.ParseFloat(m[3], 64)
	elapsed := h*3600 + min*60 + sec
	pct := elapsed / durationSecs
	if pct > 1.0 {
		pct = 1.0
	}
	return pct
}
