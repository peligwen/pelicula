package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ffprobeOutput is the subset of ffprobe's JSON output we care about.
type ffprobeOutput struct {
	Format  ffprobeFormat   `json:"format"`
	Streams []ffprobeStream `json:"streams"`
}

type ffprobeFormat struct {
	Filename string `json:"filename"`
	Duration string `json:"duration"` // seconds as string
	Size     string `json:"size"`
}

type ffprobeStream struct {
	CodecType    string `json:"codec_type"` // "video", "audio", "subtitle"
	CodecName    string `json:"codec_name"`
	Tags         map[string]string `json:"tags"`
}

// Validate runs all validation checks on a job's source file.
// Returns the result and a failure reason string (empty on pass).
func Validate(job *Job) (result ValidationResult, failReason string) {
	result.Checks = ValidationChecks{
		Integrity: "pending",
		Duration:  "pending",
		Sample:    "pending",
	}

	path := job.Source.Path

	// ── 1. File existence ────────────────────────────────────────────────────
	info, err := os.Stat(path)
	if err != nil {
		result.Checks.Integrity = "fail"
		result.Checks.Duration = "skip"
		result.Checks.Sample = "skip"
		return result, fmt.Sprintf("file not found: %s", path)
	}
	fileSize := info.Size()

	// ── 2. FFprobe parse ─────────────────────────────────────────────────────
	probe, probeErr := runFFprobe(path)
	if probeErr != nil {
		result.Checks.Integrity = "fail"
		result.Checks.Duration = "skip"
		result.Checks.Sample = "skip"
		return result, fmt.Sprintf("ffprobe failed: %v", probeErr)
	}
	result.Checks.Integrity = "pass"

	// ── 3. Codec extraction ──────────────────────────────────────────────────
	codecs := extractCodecs(probe)
	result.Checks.Codecs = &codecs

	// ── 4. Sample detection ──────────────────────────────────────────────────
	result.Checks.Sample = checkSample(fileSize, job.Source.ExpectedRuntimeMinutes)
	if result.Checks.Sample == "fail" {
		return result, fmt.Sprintf("file too small (%s) — likely a sample", formatBytes(fileSize))
	}

	// ── 5. Duration sanity ───────────────────────────────────────────────────
	result.Checks.Duration = checkDuration(probe.Format.Duration, job.Source.ExpectedRuntimeMinutes)
	if result.Checks.Duration == "fail" {
		return result, fmt.Sprintf("duration mismatch: expected ~%d min, got %s from ffprobe",
			job.Source.ExpectedRuntimeMinutes, probe.Format.Duration)
	}

	result.Passed = true
	return result, ""
}

func runFFprobe(path string) (*ffprobeOutput, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	}
	cmd := exec.CommandContext(ctx, "ffprobe", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return nil, fmt.Errorf("%s", errMsg)
	}

	var out ffprobeOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return nil, fmt.Errorf("parse ffprobe output: %w", err)
	}
	if out.Format.Filename == "" {
		return nil, fmt.Errorf("ffprobe returned empty format")
	}

	log.Printf("[validate] ffprobe ok: duration=%s size=%s streams=%d",
		out.Format.Duration, out.Format.Size, len(out.Streams))
	return &out, nil
}

func extractCodecs(probe *ffprobeOutput) CodecInfo {
	var info CodecInfo
	for _, s := range probe.Streams {
		switch s.CodecType {
		case "video":
			if info.Video == "" {
				info.Video = s.CodecName
			}
		case "audio":
			if info.Audio == "" {
				info.Audio = s.CodecName
			}
		case "subtitle":
			lang := ""
			if s.Tags != nil {
				lang = s.Tags["language"]
			}
			if lang == "" {
				lang = s.CodecName
			}
			info.Subtitles = append(info.Subtitles, lang)
		}
	}
	return info
}

// checkSample returns "pass", "warn", or "fail" based on file size heuristics.
func checkSample(size int64, expectedMinutes int) string {
	const (
		minAbsolute  = 50 * 1024 * 1024  // 50 MB — definitely a sample
		warnPerMin   = 3 * 1024 * 1024   // expect at least ~3 MB/min
	)

	if size < minAbsolute {
		return "fail"
	}

	// If we know the expected runtime, use a per-minute floor
	if expectedMinutes > 0 {
		minExpected := int64(expectedMinutes) * warnPerMin
		if size < minExpected {
			return "fail"
		}
	}

	return "pass"
}

// checkDuration compares ffprobe duration to expected runtime.
// Returns "pass", "warn" (>10% deviation), or "fail" (>50% deviation).
// If either value is unknown, returns "skip".
func checkDuration(ffprobeDuration string, expectedMinutes int) string {
	if ffprobeDuration == "" || expectedMinutes <= 0 {
		return "skip"
	}

	var durationSec float64
	if _, err := fmt.Sscanf(ffprobeDuration, "%f", &durationSec); err != nil || durationSec <= 0 {
		return "skip"
	}

	expectedSec := float64(expectedMinutes * 60)
	deviation := math.Abs(durationSec-expectedSec) / expectedSec

	switch {
	case deviation > 0.50:
		return "fail"
	case deviation > 0.10:
		return "warn"
	default:
		return "pass"
	}
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
