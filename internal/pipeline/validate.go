package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Result is the outcome of validating one imported file. It is stored as
// JSON in the job's result column and rendered by the dashboard.
type Result struct {
	Passed      bool     `json:"passed"`
	Skipped     bool     `json:"skipped,omitempty"`   // validation disabled
	Integrity   string   `json:"integrity"`           // pass|fail|skip
	Sample      string   `json:"sample"`              // pass|fail|skip
	Duration    string   `json:"duration"`            // pass|warn|fail|skip
	Video       string   `json:"video,omitempty"`     // codec of the first video stream
	Audio       []string `json:"audio,omitempty"`     // codec(lang) per track
	Subtitles   []string `json:"subtitles,omitempty"` // languages
	Width       int      `json:"width,omitempty"`
	Height      int      `json:"height,omitempty"`
	DurationSec float64  `json:"duration_sec,omitempty"`
	Reason      string   `json:"reason,omitempty"`
}

const (
	minSampleBytes  = 50 << 20 // anything smaller is a sample
	minBytesPerMin  = 3 << 20  // expected floor when the runtime is known
	durationFailPct = 0.50
	durationWarnPct = 0.10
)

// probeTimeout bounds one ffprobe run. A variable so tests can shorten it.
var probeTimeout = 2 * time.Minute

type probeOutput struct {
	Format struct {
		Duration string `json:"duration"` // seconds, as a string
	} `json:"format"`
	Streams []probeStream `json:"streams"`
}

type probeStream struct {
	CodecType   string            `json:"codec_type"` // video | audio | subtitle
	CodecName   string            `json:"codec_name"`
	Width       int               `json:"width"`
	Height      int               `json:"height"`
	Tags        map[string]string `json:"tags"`
	Disposition map[string]int    `json:"disposition"`
}

// Validate checks one imported file: it exists, ffprobe can read it and it
// has a video stream (integrity), it is not a sample (size) and its length
// is plausible for runtimeMin (duration). size is the byte size reported by
// *arr; when it is 0 the size on disk is used. ffprobe is the binary to run
// ("ffprobe" when empty). The result's Reason explains a failure.
func Validate(ctx context.Context, ffprobe, path string, size int64, runtimeMin int) Result {
	res := Result{Integrity: "skip", Sample: "skip", Duration: "skip"}

	info, err := os.Stat(path)
	if err != nil {
		res.Integrity = "fail"
		res.Reason = "file not found: " + path
		return res
	}
	if info.IsDir() {
		res.Integrity = "fail"
		res.Reason = "not a regular file: " + path
		return res
	}
	if size <= 0 {
		size = info.Size()
	}

	probe, err := runProbe(ctx, ffprobe, path)
	if err != nil {
		res.Integrity = "fail"
		res.Reason = "ffprobe failed: " + err.Error()
		return res
	}
	describe(&res, probe)
	if res.Video == "" {
		res.Integrity = "fail"
		res.Reason = "no video stream"
		return res
	}
	res.Integrity = "pass"

	res.Sample = checkSample(size, runtimeMin)
	if res.Sample == "fail" {
		res.Reason = fmt.Sprintf("file too small (%s), likely a sample", formatBytes(size))
		return res
	}

	res.Duration = checkDuration(res.DurationSec, runtimeMin)
	if res.Duration == "fail" {
		res.Reason = fmt.Sprintf("duration mismatch: expected ~%d min, file is %.0f min",
			runtimeMin, res.DurationSec/60)
		return res
	}

	res.Passed = true
	return res
}

func runProbe(ctx context.Context, ffprobe, path string) (*probeOutput, error) {
	if ffprobe == "" {
		ffprobe = "ffprobe"
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_format", "-show_streams", "-of", "json", path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = 2 * time.Second // do not hang on orphaned children after a kill
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s", firstLine(msg))
		}
		return nil, err
	}

	var out probeOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return nil, fmt.Errorf("parse output: %w", err)
	}
	return &out, nil
}

// describe fills the codec, track and duration fields of res from probe.
func describe(res *Result, p *probeOutput) {
	if d, err := strconv.ParseFloat(strings.TrimSpace(p.Format.Duration), 64); err == nil && d > 0 {
		res.DurationSec = d
	}
	for _, s := range p.Streams {
		lang := s.Tags["language"]
		switch s.CodecType {
		case "video":
			if s.Disposition["attached_pic"] == 1 || res.Video != "" {
				continue // cover art, or a second video stream
			}
			res.Video = s.CodecName
			if res.Video == "" {
				res.Video = "unknown"
			}
			res.Width, res.Height = s.Width, s.Height
		case "audio":
			if lang != "" {
				res.Audio = append(res.Audio, fmt.Sprintf("%s(%s)", s.CodecName, lang))
			} else {
				res.Audio = append(res.Audio, s.CodecName)
			}
		case "subtitle":
			if lang == "" {
				lang = s.CodecName
			}
			res.Subtitles = append(res.Subtitles, lang)
		}
	}
}

// checkSample returns "pass" or "fail" from file size heuristics.
func checkSample(size int64, runtimeMin int) string {
	if size < minSampleBytes {
		return "fail"
	}
	if runtimeMin > 0 && size < int64(runtimeMin)*minBytesPerMin {
		return "fail"
	}
	return "pass"
}

// checkDuration compares the probed duration to the expected runtime:
// "fail" past 50% deviation, "warn" past 10%, "skip" when either is unknown.
func checkDuration(durationSec float64, runtimeMin int) string {
	if durationSec <= 0 || runtimeMin <= 0 {
		return "skip"
	}
	expected := float64(runtimeMin) * 60
	deviation := math.Abs(durationSec-expected) / expected
	switch {
	case deviation > durationFailPct:
		return "fail"
	case deviation > durationWarnPct:
		return "warn"
	default:
		return "pass"
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
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
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
