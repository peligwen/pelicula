// Package ffwrap provides FFprobe and FFmpeg execution with context support.
// The ExecProber and ExecTranscoder types implement the Prober and Transcoder
// interfaces using configurable command names so tests can inject stubs.
package ffwrap

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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

	"procula/internal/queue"
)

// ── FFprobe ──────────────────────────────────────────────────────────────────

// ffprobeOutput is the subset of ffprobe's JSON output we use.
type ffprobeOutput struct {
	Format  ffprobeFormat   `json:"format"`
	Streams []ffprobeStream `json:"streams"`
}

type ffprobeFormat struct {
	Filename string `json:"filename"`
	Duration string `json:"duration"`
	Size     string `json:"size"`
}

type ffprobeStream struct {
	Index     int               `json:"index"`
	CodecType string            `json:"codec_type"`
	CodecName string            `json:"codec_name"`
	Width     int               `json:"width"`
	Height    int               `json:"height"`
	Channels  int               `json:"channels"`
	Tags      map[string]string `json:"tags"`
}

// ProbeResult contains the parsed output of an ffprobe run.
type ProbeResult struct {
	Duration string
	Size     string
	Filename string
	Streams  []ffprobeOutput_stream
}

// ffprobeOutput_stream is an internal alias to avoid leaking ffprobeStream.
type ffprobeOutput_stream = ffprobeStream

// ExtractCodecs derives a CodecInfo from a ProbeResult.
func ExtractCodecs(probe *ProbeResult) queue.CodecInfo {
	var info queue.CodecInfo
	for _, s := range probe.Streams {
		switch s.CodecType {
		case "video":
			if info.Video == "" {
				info.Video = s.CodecName
				info.Width = s.Width
				info.Height = s.Height
			}
		case "audio":
			if info.Audio == "" {
				info.Audio = s.CodecName
			}
			lang := ""
			if s.Tags != nil {
				lang = s.Tags["language"]
			}
			info.AudioTracks = append(info.AudioTracks, queue.AudioTrack{
				Index:    s.Index,
				Codec:    s.CodecName,
				Language: lang,
				Channels: s.Channels,
			})
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

// SubStream describes one subtitle stream inside a media file.
type SubStream struct {
	SubIndex  int    // ordinal among subtitle streams (0-based)
	Lang      string // normalized 2-letter code
	CodecName string
}

// ProbeSubStreams re-runs ffprobe and returns only the subtitle streams.
func (p *ExecProber) ProbeSubStreams(ctx context.Context, mediaPath string) ([]SubStream, error) {
	probe, err := p.runFFprobe(ctx, mediaPath)
	if err != nil {
		return nil, err
	}
	var streams []SubStream
	subIdx := 0
	for _, s := range probe.Streams {
		if s.CodecType != "subtitle" {
			continue
		}
		lang := ""
		if s.Tags != nil {
			lang = s.Tags["language"]
		}
		if lang == "" {
			lang = s.CodecName
		}
		streams = append(streams, SubStream{
			SubIndex:  subIdx,
			Lang:      normalizeLangCode(lang),
			CodecName: s.CodecName,
		})
		subIdx++
	}
	return streams, nil
}

// ExecProber runs ffprobe via exec.
type ExecProber struct {
	// Command is the ffprobe binary name or path. Defaults to "ffprobe".
	Command string
}

// NewExecProber creates an ExecProber using the system ffprobe.
func NewExecProber() *ExecProber { return &ExecProber{Command: "ffprobe"} }

// Probe runs ffprobe on path and returns the parsed result.
func (p *ExecProber) Probe(ctx context.Context, path string) (*ProbeResult, error) {
	out, err := p.runFFprobe(ctx, path)
	if err != nil {
		return nil, err
	}
	r := &ProbeResult{
		Duration: out.Format.Duration,
		Size:     out.Format.Size,
		Filename: out.Format.Filename,
		Streams:  out.Streams,
	}
	return r, nil
}

func (p *ExecProber) runFFprobe(ctx context.Context, path string) (*ffprobeOutput, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	args := []string{
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		path,
	}
	cmd := exec.CommandContext(probeCtx, p.Command, args...)
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

	slog.Info("ffprobe ok", "component", "validate",
		"duration", out.Format.Duration, "size", out.Format.Size, "streams", len(out.Streams))
	return &out, nil
}

// ── FFmpeg ───────────────────────────────────────────────────────────────────

var (
	reDuration = regexp.MustCompile(`Duration:\s+(\d+):(\d+):([\d.]+)`)
	reTime     = regexp.MustCompile(`time=\s*(\d+):(\d+):([\d.]+)`)
)

// ExecTranscoder runs ffmpeg via exec.
type ExecTranscoder struct {
	// Command is the ffmpeg binary name or path. Defaults to "ffmpeg".
	Command string
}

// NewExecTranscoder creates an ExecTranscoder using the system ffmpeg.
func NewExecTranscoder() *ExecTranscoder { return &ExecTranscoder{Command: "ffmpeg"} }

// ExtractEmbeddedSub uses ffmpeg to extract subtitle stream subIndex to a
// temporary SRT file, parses it, and returns the raw SRT bytes.
func (t *ExecTranscoder) ExtractEmbeddedSub(ctx context.Context, mediaPath string, subIndex int) ([]byte, error) {
	tmp, err := os.CreateTemp("", "procula-sub-*.srt")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	args := []string{
		"-nostdin",
		"-y",
		"-i", mediaPath,
		"-map", fmt.Sprintf("0:s:%d", subIndex),
		"-c:s", "srt",
		tmp.Name(),
	}
	cmd := exec.CommandContext(ctx, t.Command, args...)
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("ffmpeg extract sub: %s", msg)
	}

	return os.ReadFile(tmp.Name())
}

// TranscodeOpts carries everything needed to run a transcode job.
type TranscodeOpts struct {
	Input       string
	FinalPath   string
	PartialPath string
	Args        []string
}

// Transcode runs ffmpeg with the given args, streaming progress via progressFn.
// The partial file is renamed to finalPath on success.
func (t *ExecTranscoder) Transcode(ctx context.Context, opts TranscodeOpts, progressFn func(pct, etaSecs float64)) error {
	cmd := exec.CommandContext(ctx, t.Command, opts.Args...)
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start FFmpeg: %w", err)
	}

	var (
		durationSecs       float64
		startTime          time.Time
		lastProgressUpdate time.Time
	)
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()
		if durationSecs == 0 {
			if d := parseDurationLine(line); d > 0 {
				durationSecs = d
			}
		}
		if durationSecs > 0 {
			if pct := parseProgressLine(line, durationSecs); pct >= 0 && progressFn != nil {
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
		return fmt.Errorf("FFmpeg exited with error: %w", err)
	}

	if err := os.Rename(opts.PartialPath, opts.FinalPath); err != nil {
		return fmt.Errorf("rename output: %w", err)
	}
	slog.Info("transcoding complete", "component", "process", "output", opts.FinalPath)
	return nil
}

// ── Transcoding arg builder ───────────────────────────────────────────────────

// BuildFFmpegArgs builds the argument list for an ffmpeg transcode command.
// profile fields are passed in-line to avoid importing the profiles package
// (which would create a circular dependency).
func BuildFFmpegArgs(
	input, output string,
	videoCodec, videoPreset, audioCodec string,
	videoCRF, maxHeight, audioChannels int,
	codecs *queue.CodecInfo,
	preferredAudioLang string,
) []string {
	args := []string{
		"-nostdin",
		"-y",
		"-i", input,
	}

	args = append(args, "-map", "0:v", "-map", "0:a", "-map", "0:s?")

	args = append(args, "-c:v", videoCodec)
	if videoCodec != "copy" {
		if videoCRF > 0 {
			args = append(args, "-crf", strconv.Itoa(videoCRF))
		}
		if videoPreset != "" {
			args = append(args, "-preset", videoPreset)
		}
		if maxHeight > 0 {
			args = append(args, "-vf", fmt.Sprintf("scale=-2:%d", maxHeight))
		}
	}

	args = append(args, "-c:a", audioCodec)
	if audioCodec != "copy" && audioChannels > 0 {
		args = append(args, "-ac", strconv.Itoa(audioChannels))
	}

	if codecs != nil && len(codecs.AudioTracks) > 0 {
		for i, track := range codecs.AudioTracks {
			if normalizeLangCode(track.Language) == preferredAudioLang {
				args = append(args, "-disposition:a", "0")
				args = append(args, fmt.Sprintf("-disposition:a:%d", i), "default")
				break
			}
		}
	}

	args = append(args, "-c:s", "copy")
	args = append(args, output)
	return args
}

// ResolveOutputPath computes the output path for a transcode, avoiding collisions.
func ResolveOutputPath(input, suffix, dir string) string {
	base := strings.TrimSuffix(filepath.Base(input), filepath.Ext(input))
	outputPath := filepath.Join(dir, base+suffix+".mkv")
	if _, err := os.Stat(outputPath); err == nil {
		for i := 2; ; i++ {
			candidate := filepath.Join(dir, fmt.Sprintf("%s%s.%d.mkv", base, suffix, i))
			if _, err := os.Stat(candidate); os.IsNotExist(err) {
				outputPath = candidate
				break
			}
		}
	}
	return outputPath
}

// parseDurationLine extracts the total duration (in seconds) from an ffmpeg
// stderr line like "  Duration: 01:30:00.00, ...".
func parseDurationLine(line string) float64 {
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

// parseProgressLine extracts the progress fraction from an ffmpeg progress line.
func parseProgressLine(line string, durationSecs float64) float64 {
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

// IsTextSubCodec reports whether codec is a text-based subtitle format.
func IsTextSubCodec(codec string) bool {
	switch strings.ToLower(codec) {
	case "subrip", "ass", "ssa", "webvtt", "mov_text", "text":
		return true
	}
	return false
}

// ── Language helpers ──────────────────────────────────────────────────────────

var iso6392to1 = map[string]string{
	"eng": "en", "spa": "es", "fre": "fr", "fra": "fr", "ger": "de", "deu": "de",
	"ita": "it", "por": "pt", "rus": "ru", "jpn": "ja", "chi": "zh", "zho": "zh",
	"kor": "ko", "ara": "ar", "hin": "hi", "dut": "nl", "nld": "nl",
	"swe": "sv", "nor": "no", "dan": "da", "fin": "fi", "pol": "pl",
	"tur": "tr", "hun": "hu", "ces": "cs", "cze": "cs", "slk": "sk",
	"rum": "ro", "ron": "ro", "bul": "bg", "hrv": "hr", "srp": "sr",
	"ukr": "uk", "vie": "vi", "tha": "th", "ind": "id", "may": "ms",
}

// normalizeLangCode converts a language tag to a 2-letter ISO 639-1 code.
func normalizeLangCode(tag string) string {
	t := strings.ToLower(strings.TrimSpace(tag))
	if v, ok := iso6392to1[t]; ok {
		return v
	}
	if idx := strings.IndexByte(t, '-'); idx == 2 {
		return t[:2]
	}
	return t
}

// NormalizeLangCode is the exported version for use by other packages.
func NormalizeLangCode(tag string) string { return normalizeLangCode(tag) }
