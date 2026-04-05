package main

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	reDuration = regexp.MustCompile(`Duration:\s+(\d+):(\d+):([\d.]+)`)
	reTime     = regexp.MustCompile(`time=\s*(\d+):(\d+):([\d.]+)`)
)

// Process runs FFmpeg to transcode the job's source file using the given profile.
// Progress is reported via progressFn (0.0–1.0) as transcoding proceeds.
// Returns the output file path on success.
func Process(ctx context.Context, job *Job, profile *TranscodeProfile, progressFn func(float64)) (string, error) {
	input := job.Source.Path
	if input == "" {
		return "", fmt.Errorf("no input path")
	}

	// Build output path: /processing/<basename><suffix>.mkv
	base := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
	outputPath := filepath.Join("/processing", base+profile.Output.Suffix+".mkv")

	args := buildFFmpegArgs(input, outputPath, profile)
	slog.Info("starting FFmpeg transcode", "component", "process", "input", input, "output", outputPath, "profile", profile.Name)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start FFmpeg: %w", err)
	}

	// Parse duration and time progress from FFmpeg stderr
	var durationSecs float64
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
				progressFn(pct)
			}
		}
	}

	if err := cmd.Wait(); err != nil {
		os.Remove(outputPath) //nolint:errcheck
		return "", fmt.Errorf("FFmpeg exited with error: %w", err)
	}

	slog.Info("transcoding complete", "component", "process", "output", outputPath)
	return outputPath, nil
}

func buildFFmpegArgs(input, output string, p *TranscodeProfile) []string {
	args := []string{
		"-i", input,
		"-y", // overwrite output without asking
	}

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
