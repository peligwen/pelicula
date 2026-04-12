package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SubtitleCue is a single timed subtitle entry.
type SubtitleCue struct {
	Start time.Duration
	End   time.Duration
	Text  string // raw text; may contain newlines
}

// subStream describes one subtitle stream found inside a media file.
type subStream struct {
	SubIndex  int    // ordinal among subtitle streams in the file (0-based), for ffmpeg -map 0:s:N
	Lang      string // normalized 2-letter code (via normalizeLangCode)
	CodecName string
}

var (
	srtTagRE = regexp.MustCompile(`<[^>]+>`)
	assTagRE = regexp.MustCompile(`\{[^}]*\}`)
)

// GenerateDualSubs is the pipeline stage entry point.
// For each pair in settings.DualSubPairs it generates a stacked ASS sidecar
// file alongside the source media (e.g. Movie.en-es.ass) and returns the paths
// of written files. Non-fatal errors (no source sub, translator unavailable)
// are logged and skipped; the first such error is also returned so the caller
// can emit a meaningful event when no outputs are produced.
func GenerateDualSubs(ctx context.Context, job *Job, settings PipelineSettings, configDir string) (outputs []string, firstErr error) {
	for _, pair := range settings.DualSubPairs {
		parts := strings.SplitN(pair, "-", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			slog.Warn("invalid dualsub pair (expected 'baselang-seclang')", "component", "dualsub", "pair", pair)
			continue
		}
		baseLang, secLang := normalizeLangCode(parts[0]), normalizeLangCode(parts[1])
		outPath := dualSubPath(job.Source.Path, pair)

		// Idempotency: skip if the output already exists and is newer than the source
		if isUpToDate(outPath, job.Source.Path) {
			slog.Info("dual sub up-to-date, skipping", "component", "dualsub", "pair", pair)
			outputs = append(outputs, outPath)
			continue
		}

		path, err := generatePair(ctx, job, baseLang, secLang, outPath, settings, configDir)
		if err != nil {
			slog.Warn("dual sub pair failed", "component", "dualsub", "pair", pair, "job_id", job.ID, "error", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		slog.Info("dual sub generated", "component", "dualsub", "pair", pair, "output", path)
		outputs = append(outputs, path)
	}
	return outputs, firstErr
}

// generatePair produces a single ASS sidecar for one language pair.
func generatePair(ctx context.Context, job *Job, baseLang, secLang, outPath string, settings PipelineSettings, configDir string) (string, error) {
	streams, err := probeSubStreams(job.Source.Path)
	if err != nil {
		return "", fmt.Errorf("probe: %w", err)
	}

	// Get base language cues — required
	baseCues, err := getCues(ctx, job.Source.Path, baseLang, streams)
	if err != nil {
		return "", fmt.Errorf("base cues (%s): %w", baseLang, err)
	}
	if len(baseCues) == 0 {
		return "", fmt.Errorf("no subtitles found for base language %q", baseLang)
	}

	// Get secondary language cues — attempt source first, fall back to translation
	secCues, err := getCues(ctx, job.Source.Path, secLang, streams)
	if err != nil || len(secCues) == 0 {
		t := newTranslator(settings.DualSubTranslator, configDir)
		secCues, err = translateCues(ctx, baseCues, baseLang, secLang, t)
		if err != nil {
			return "", fmt.Errorf("secondary cues (%s): %w", secLang, err)
		}
	}

	// Align secondary cues to base cue timing
	secTexts := alignCues(baseCues, secCues)

	if err := writeASS(outPath, baseLang, secLang, baseCues, secTexts); err != nil {
		return "", fmt.Errorf("write ASS: %w", err)
	}
	return outPath, nil
}

// probeSubStreams re-runs ffprobe on the media file to collect subtitle stream
// metadata including per-stream ordinal index for ffmpeg extraction.
func probeSubStreams(mediaPath string) ([]subStream, error) {
	probe, err := runFFprobe(mediaPath)
	if err != nil {
		return nil, err
	}
	var streams []subStream
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
		streams = append(streams, subStream{
			SubIndex:  subIdx,
			Lang:      normalizeLangCode(lang),
			CodecName: s.CodecName,
		})
		subIdx++
	}
	return streams, nil
}

// getCues finds subtitle cues for lang: first checks embedded streams, then
// sidecar files. Returns nil, nil when no source is available.
func getCues(ctx context.Context, mediaPath, lang string, streams []subStream) ([]SubtitleCue, error) {
	// Embedded stream
	for _, s := range streams {
		if s.Lang == lang && isTextSubCodec(s.CodecName) {
			return extractEmbeddedSub(ctx, mediaPath, s.SubIndex)
		}
	}
	// Sidecar file (.lang.srt or .lang.ass)
	if sidecar := findSubSidecar(mediaPath, lang); sidecar != "" {
		data, err := os.ReadFile(sidecar)
		if err != nil {
			return nil, err
		}
		return parseSRT(data)
	}
	return nil, nil
}

// extractEmbeddedSub uses ffmpeg to extract the N-th subtitle stream to a
// temporary SRT file, parses it, and returns the cues.
func extractEmbeddedSub(ctx context.Context, mediaPath string, subIndex int) ([]SubtitleCue, error) {
	tmp, err := os.CreateTemp("", "procula-sub-*.srt")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

	args := []string{
		"-y",
		"-i", mediaPath,
		"-map", fmt.Sprintf("0:s:%d", subIndex),
		"-c:s", "srt",
		tmp.Name(),
	}
	cmd := exec.CommandContext(ctx, ffmpegCommand, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("ffmpeg extract sub: %s", msg)
	}

	data, err := os.ReadFile(tmp.Name())
	if err != nil {
		return nil, err
	}
	return parseSRT(data)
}

// findSubSidecar looks for {media_base}.{lang}.srt or .{lang}.ass alongside
// the media file. Returns the first match, or empty string.
func findSubSidecar(mediaPath, lang string) string {
	base := strings.TrimSuffix(mediaPath, filepath.Ext(mediaPath))
	for _, ext := range []string{".srt", ".ass"} {
		p := base + "." + lang + ext
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// isTextSubCodec reports whether the codec is a text-based subtitle format
// that ffmpeg can decode and convert to SRT. Bitmap formats (PGS, DVD, XSUB)
// require OCR and are not supported.
func isTextSubCodec(codec string) bool {
	switch strings.ToLower(codec) {
	case "subrip", "ass", "ssa", "webvtt", "mov_text", "text":
		return true
	}
	return false
}

// translateCues runs the translator on each base cue to produce secondary cues
// sharing the same timing as the base.
func translateCues(ctx context.Context, baseCues []SubtitleCue, fromLang, toLang string, t Translator) ([]SubtitleCue, error) {
	result := make([]SubtitleCue, len(baseCues))
	for i, c := range baseCues {
		plain := stripSubTags(c.Text)
		translated, err := t.Translate(ctx, plain, fromLang, toLang)
		if err != nil {
			return nil, fmt.Errorf("cue %d: %w", i+1, err)
		}
		result[i] = SubtitleCue{Start: c.Start, End: c.End, Text: translated}
	}
	return result, nil
}

// alignCues matches secondary cues to base cues by midpoint containment.
// Returns a slice parallel to baseCues where each entry is the joined text of
// all secondary cues whose midpoint falls within the base cue's time range.
// Empty string means no secondary cue matched that base cue.
func alignCues(base, secondary []SubtitleCue) []string {
	result := make([]string, len(base))
	for i, b := range base {
		var matched []string
		for _, s := range secondary {
			mid := s.Start + (s.End-s.Start)/2
			if mid >= b.Start && mid <= b.End {
				matched = append(matched, stripSubTags(s.Text))
			}
		}
		result[i] = strings.Join(matched, "\\N")
	}
	return result
}

// parseSRT parses a SubRip (.srt) file into SubtitleCues.
// It tolerates Windows line endings, UTF-8 BOM, and minor formatting variations.
func parseSRT(data []byte) ([]SubtitleCue, error) {
	text := strings.TrimPrefix(string(data), "\xef\xbb\xbf") // strip BOM
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	var cues []SubtitleCue
	for _, block := range strings.Split(text, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		lines := strings.Split(block, "\n")
		idx := 0

		// Skip optional sequence number line. Detect by absence of " --> ";
		// a timestamp line always contains it, a sequence-number line never does.
		if len(lines) > 0 && !strings.Contains(lines[0], " --> ") {
			idx = 1
		}
		if idx >= len(lines) {
			continue
		}

		// Timestamp line
		start, end, err := parseSRTTimestamp(lines[idx])
		if err != nil {
			continue
		}
		idx++

		textLines := lines[idx:]
		body := strings.TrimSpace(strings.Join(textLines, "\n"))
		if body == "" {
			continue
		}
		cues = append(cues, SubtitleCue{Start: start, End: end, Text: body})
	}
	return cues, nil
}

func parseSRTTimestamp(line string) (start, end time.Duration, err error) {
	// "00:01:23,456 --> 00:01:25,789"
	parts := strings.SplitN(line, " --> ", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid SRT timestamp: %q", line)
	}
	if start, err = parseSRTTime(strings.TrimSpace(parts[0])); err != nil {
		return 0, 0, err
	}
	end, err = parseSRTTime(strings.TrimSpace(parts[1]))
	return
}

func parseSRTTime(s string) (time.Duration, error) {
	// Find the decimal separator (SRT uses comma; some tools use period)
	sepIdx := strings.IndexByte(s, ',')
	if sepIdx < 0 {
		sepIdx = strings.IndexByte(s, '.')
	}

	var h, m, sec int
	var subSecStr string

	if sepIdx >= 0 {
		if _, err := fmt.Sscanf(s[:sepIdx], "%d:%d:%d", &h, &m, &sec); err != nil {
			return 0, fmt.Errorf("parse SRT time %q: %w", s, err)
		}
		subSecStr = strings.TrimSpace(s[sepIdx+1:])
	} else {
		if _, err := fmt.Sscanf(s, "%d:%d:%d", &h, &m, &sec); err != nil {
			return 0, fmt.Errorf("parse SRT time %q: %w", s, err)
		}
	}

	// Scale sub-second portion to milliseconds regardless of how many digits
	// are present (SRT standard is 3, but "1" should mean 100ms, "50" → 500ms).
	var ms int
	if subSecStr != "" {
		v, err := strconv.Atoi(subSecStr)
		if err != nil {
			return 0, fmt.Errorf("parse sub-second in %q: %w", s, err)
		}
		switch len(subSecStr) {
		case 1:
			ms = v * 100
		case 2:
			ms = v * 10
		case 3:
			ms = v
		default:
			// >3 digits: truncate to millisecond precision
			scale := 1
			for i := 3; i < len(subSecStr); i++ {
				scale *= 10
			}
			ms = v / scale
		}
	}

	return time.Duration(h)*time.Hour +
		time.Duration(m)*time.Minute +
		time.Duration(sec)*time.Second +
		time.Duration(ms)*time.Millisecond, nil
}

// stripSubTags removes HTML (<i>, <b>, …) and ASS ({...}) inline tags from
// subtitle text, leaving plain text suitable for display or translation.
func stripSubTags(text string) string {
	text = srtTagRE.ReplaceAllString(text, "")
	text = assTagRE.ReplaceAllString(text, "")
	return strings.TrimSpace(text)
}

// toASSText converts plain subtitle text to an ASS text field:
// newlines become \N (ASS line-break), and no inline tags are added.
func toASSText(text string) string {
	text = stripSubTags(text)
	return strings.ReplaceAll(text, "\n", "\\N")
}

// fmtASS formats a duration as an ASS timestamp: H:MM:SS.cs (centiseconds).
func fmtASS(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := d.Milliseconds()
	cs := (total % 1000) / 10
	sec := (total / 1000) % 60
	min := (total / 60000) % 60
	hr := total / 3600000
	return fmt.Sprintf("%d:%02d:%02d.%02d", hr, min, sec, cs)
}

// dualSubPath returns the sidecar path for the given pair alongside the media.
// e.g. /movies/Foo (2020)/Foo.mkv + "en-es" → /movies/Foo (2020)/Foo.en-es.ass
func dualSubPath(mediaPath, pair string) string {
	base := strings.TrimSuffix(mediaPath, filepath.Ext(mediaPath))
	return base + "." + pair + ".ass"
}

// isUpToDate reports whether outPath exists and is newer than srcPath.
func isUpToDate(outPath, srcPath string) bool {
	outInfo, err := os.Stat(outPath)
	if err != nil {
		return false
	}
	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return false
	}
	return outInfo.ModTime().After(srcInfo.ModTime())
}

// writeASS writes a dual-language stacked ASS subtitle file.
// Base language cues appear at the bottom in white; secondary appears at the
// top in yellow. Uses atomic write (partial → final rename).
func writeASS(outPath, baseLang, secLang string, baseCues []SubtitleCue, secTexts []string) error {
	tmp := outPath + ".partial"
	if err := writeASSContent(tmp, baseLang, secLang, baseCues, secTexts); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return err
	}
	return os.Rename(tmp, outPath)
}

func writeASSContent(path, baseLang, secLang string, baseCues []SubtitleCue, secTexts []string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := bufio.NewWriter(f)

	// Script Info
	fmt.Fprintf(w, "[Script Info]\n")
	fmt.Fprintf(w, "; Dual subtitles: %s (bottom) + %s (top)\n", strings.ToUpper(baseLang), strings.ToUpper(secLang))
	fmt.Fprintf(w, "; Generated by Procula\n")
	fmt.Fprintf(w, "ScriptType: v4.00+\n")
	fmt.Fprintf(w, "Collisions: Normal\n")
	fmt.Fprintf(w, "PlayResX: 1920\n")
	fmt.Fprintf(w, "PlayResY: 1080\n")
	fmt.Fprintf(w, "\n")

	// V4+ Styles
	// Colour format: &HAABBGGRR& (alpha, blue, green, red)
	// White = &H00FFFFFF&, Yellow (RGB 255,255,0) = &H0000FFFF&, Black = &H00000000&
	// Alignment: 2 = bottom-center, 8 = top-center
	fmt.Fprintf(w, "[V4+ Styles]\n")
	fmt.Fprintf(w, "Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	fmt.Fprintf(w, "Style: Bottom,Arial,52,&H00FFFFFF&,&H000000FF&,&H00000000&,&H80000000&,-1,0,0,0,100,100,0,0,1,2,0,2,10,10,20,1\n")
	fmt.Fprintf(w, "Style: Top,Arial,52,&H0000FFFF&,&H000000FF&,&H00000000&,&H80000000&,-1,0,0,0,100,100,0,0,1,2,0,8,10,10,20,1\n")
	fmt.Fprintf(w, "\n")

	// Events
	fmt.Fprintf(w, "[Events]\n")
	fmt.Fprintf(w, "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")

	for i, cue := range baseCues {
		start := fmtASS(cue.Start)
		end := fmtASS(cue.End)

		// Bottom: base language (familiar)
		if bt := toASSText(cue.Text); bt != "" {
			fmt.Fprintf(w, "Dialogue: 0,%s,%s,Bottom,,0,0,0,,{\\an2}%s\n", start, end, bt)
		}

		// Top: secondary language (learning)
		if i < len(secTexts) {
			if st := toASSText(secTexts[i]); st != "" {
				fmt.Fprintf(w, "Dialogue: 0,%s,%s,Top,,0,0,0,,{\\an8}%s\n", start, end, st)
			}
		}
	}

	return w.Flush()
}
