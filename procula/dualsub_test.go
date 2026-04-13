package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ── parseSRT ──────────────────────────────────────────────────────────────────

func TestParseSRT_Basic(t *testing.T) {
	srt := "1\n00:00:01,000 --> 00:00:03,000\nHello world\n\n2\n00:00:05,000 --> 00:00:08,000\nFine, thanks.\n"
	cues, err := parseSRT([]byte(srt))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cues) != 2 {
		t.Fatalf("expected 2 cues, got %d", len(cues))
	}
	if cues[0].Text != "Hello world" {
		t.Errorf("cue 0 text = %q, want %q", cues[0].Text, "Hello world")
	}
	if cues[0].Start != time.Second {
		t.Errorf("cue 0 start = %v, want 1s", cues[0].Start)
	}
	if cues[0].End != 3*time.Second {
		t.Errorf("cue 0 end = %v, want 3s", cues[0].End)
	}
	if cues[1].Text != "Fine, thanks." {
		t.Errorf("cue 1 text = %q, want %q", cues[1].Text, "Fine, thanks.")
	}
}

func TestParseSRT_MultiLine(t *testing.T) {
	srt := "1\n00:00:01,000 --> 00:00:03,000\nLine one\nLine two\n\n"
	cues, err := parseSRT([]byte(srt))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cues) != 1 {
		t.Fatalf("expected 1 cue, got %d", len(cues))
	}
	if cues[0].Text != "Line one\nLine two" {
		t.Errorf("cue text = %q", cues[0].Text)
	}
}

func TestParseSRT_HTMLTags(t *testing.T) {
	srt := "1\n00:00:01,000 --> 00:00:02,000\n<i>Italics</i>\n\n"
	cues, _ := parseSRT([]byte(srt))
	if len(cues) != 1 {
		t.Fatalf("expected 1 cue, got %d", len(cues))
	}
	// Raw text preserved in cue; stripping happens at write time
	if !strings.Contains(cues[0].Text, "Italics") {
		t.Errorf("expected 'Italics' in %q", cues[0].Text)
	}
}

func TestParseSRT_BOM(t *testing.T) {
	srt := "\xef\xbb\xbf1\n00:00:01,000 --> 00:00:02,000\nHello\n\n"
	cues, err := parseSRT([]byte(srt))
	if err != nil || len(cues) != 1 {
		t.Fatalf("BOM handling failed: err=%v cues=%d", err, len(cues))
	}
}

func TestParseSRT_WindowsLineEndings(t *testing.T) {
	srt := "1\r\n00:00:01,000 --> 00:00:02,000\r\nHello\r\n\r\n"
	cues, err := parseSRT([]byte(srt))
	if err != nil || len(cues) != 1 {
		t.Fatalf("CRLF handling failed: err=%v cues=%d", err, len(cues))
	}
}

func TestParseSRT_Empty(t *testing.T) {
	cues, err := parseSRT([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	if len(cues) != 0 {
		t.Errorf("expected 0 cues, got %d", len(cues))
	}
}

// ── parseSRTTime ──────────────────────────────────────────────────────────────

func TestParseSRTTime(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"00:00:01,000", time.Second},
		{"00:01:23,456", time.Minute + 23*time.Second + 456*time.Millisecond},
		{"01:00:00,000", time.Hour},
		{"00:00:00,000", 0},
	}
	for _, tc := range cases {
		got, err := parseSRTTime(tc.in)
		if err != nil {
			t.Errorf("parseSRTTime(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseSRTTime(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// ── fmtASS ────────────────────────────────────────────────────────────────────

func TestFmtASS(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0:00:00.00"},
		{time.Second, "0:00:01.00"},
		{1500 * time.Millisecond, "0:00:01.50"},
		{time.Minute + 23*time.Second + 456*time.Millisecond, "0:01:23.45"},
		{time.Hour, "1:00:00.00"},
	}
	for _, tc := range cases {
		got := fmtASS(tc.in)
		if got != tc.want {
			t.Errorf("fmtASS(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFmtASS_Negative(t *testing.T) {
	// Negative durations are clamped to 0
	if got := fmtASS(-time.Second); got != "0:00:00.00" {
		t.Errorf("fmtASS(-1s) = %q, want %q", got, "0:00:00.00")
	}
}

// ── alignCues ─────────────────────────────────────────────────────────────────

func makeCue(startMs, endMs int, text string) SubtitleCue {
	return SubtitleCue{
		Start: time.Duration(startMs) * time.Millisecond,
		End:   time.Duration(endMs) * time.Millisecond,
		Text:  text,
	}
}

func TestAlignCues_OneToOne(t *testing.T) {
	base := []SubtitleCue{makeCue(1000, 3000, "Hello")}
	sec := []SubtitleCue{makeCue(1200, 2800, "Hola")}
	got := alignCues(base, sec)
	if len(got) != 1 || got[0] != "Hola" {
		t.Errorf("1:1 align = %v, want [Hola]", got)
	}
}

func TestAlignCues_NoMatch(t *testing.T) {
	base := []SubtitleCue{makeCue(1000, 3000, "Hello")}
	// Secondary cue is entirely before the base cue
	sec := []SubtitleCue{makeCue(100, 500, "Anterior")}
	got := alignCues(base, sec)
	if len(got) != 1 || got[0] != "" {
		t.Errorf("no-match align = %v, want [\"\"]", got)
	}
}

func TestAlignCues_ManySecondaryToOneBase(t *testing.T) {
	// Two secondary cues whose midpoints both fall within one base cue
	base := []SubtitleCue{makeCue(0, 6000, "Long line")}
	sec := []SubtitleCue{
		makeCue(500, 2500, "Primera"),  // mid = 1500ms ✓
		makeCue(3000, 5000, "Segunda"), // mid = 4000ms ✓
	}
	got := alignCues(base, sec)
	if len(got) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got))
	}
	if !strings.Contains(got[0], "Primera") || !strings.Contains(got[0], "Segunda") {
		t.Errorf("expected both cues joined, got %q", got[0])
	}
}

func TestAlignCues_EmptySecondary(t *testing.T) {
	base := []SubtitleCue{makeCue(1000, 3000, "Hello")}
	got := alignCues(base, nil)
	if len(got) != 1 || got[0] != "" {
		t.Errorf("empty secondary = %v, want [\"\"]", got)
	}
}

// ── stripSubTags ──────────────────────────────────────────────────────────────

func TestStripSubTags(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<i>hello</i>", "hello"},
		{"<b>bold</b> text", "bold text"},
		{"{\\an8}top", "top"},
		{"<font color=\"red\">red</font>", "red"},
		{"plain text", "plain text"},
	}
	for _, tc := range cases {
		got := stripSubTags(tc.in)
		if got != tc.want {
			t.Errorf("stripSubTags(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ── dualSubPath ───────────────────────────────────────────────────────────────

func TestDualSubPath(t *testing.T) {
	got := dualSubPath("/movies/Foo (2020)/Foo (2020).mkv", "en-es")
	want := "/movies/Foo (2020)/Foo (2020).en-es.ass"
	if got != want {
		t.Errorf("dualSubPath = %q, want %q", got, want)
	}
}

// ── writeASS (integration) ────────────────────────────────────────────────────

func TestWriteASS_BasicOutput(t *testing.T) {
	tmp := t.TempDir() + "/test.en-es.ass"
	// topCues = secondary (learning, yellow, top); bottomTexts = base (familiar, white, bottom)
	topCues := []SubtitleCue{
		makeCue(1000, 3000, "Hola mundo"),
		makeCue(5000, 7000, ""),
	}
	bottomTexts := []string{"Hello world", "Goodbye"}
	prof := FindDualSubProfile(nil, "Default")

	if err := writeASS(tmp, prof, topCues, bottomTexts); err != nil {
		t.Fatalf("writeASS: %v", err)
	}

	data, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	content := string(data)

	checks := []string{
		"[Script Info]",
		"[V4+ Styles]",
		"[Events]",
		"Style: Bottom",
		"Style: Top",
		"Bottom,,0,0,0,,{\\an2}Hello world",
		"Top,,0,0,0,,{\\an2}Hola mundo",
		"0:00:01.00",
		"0:00:03.00",
	}
	for _, check := range checks {
		if !strings.Contains(content, check) {
			t.Errorf("ASS output missing %q", check)
		}
	}

	// Second cue has no secondary text — only Bottom line should appear for it
	if strings.Count(content, "Goodbye") != 1 {
		t.Errorf("expected exactly one 'Goodbye' dialogue line")
	}
}

func TestWriteASS_NewlineBecomesBackslashN(t *testing.T) {
	tmp := t.TempDir() + "/test.ass"
	prof := FindDualSubProfile(nil, "Default")
	topCues := []SubtitleCue{makeCue(0, 2000, "Line one\nLine two")}
	if err := writeASS(tmp, prof, topCues, []string{""}); err != nil {
		t.Fatalf("writeASS: %v", err)
	}
	data, _ := os.ReadFile(tmp)
	if !strings.Contains(string(data), `Line one\NLine two`) {
		t.Errorf("expected \\N in ASS output, got:\n%s", data)
	}
}

func TestWriteASS_EmptyBaseCues(t *testing.T) {
	tmp := t.TempDir() + "/empty.ass"
	prof := FindDualSubProfile(nil, "Default")
	if err := writeASS(tmp, prof, nil, nil); err != nil {
		t.Fatalf("writeASS with empty cues: %v", err)
	}
	data, _ := os.ReadFile(tmp)
	content := string(data)
	// Must still be valid ASS with all three sections
	for _, section := range []string{"[Script Info]", "[V4+ Styles]", "[Events]"} {
		if !strings.Contains(content, section) {
			t.Errorf("missing ASS section %q in empty output", section)
		}
	}
}

// ── parseSRTTime short-fraction bug ───────────────────────────────────────────

func TestParseSRTTime_ShortFraction(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"00:00:01,5", 1*time.Second + 500*time.Millisecond},    // 1 digit → ×100
		{"00:00:01,50", 1*time.Second + 500*time.Millisecond},   // 2 digits → ×10
		{"00:00:01,500", 1*time.Second + 500*time.Millisecond},  // 3 digits → exact
		{"00:00:01,5000", 1*time.Second + 500*time.Millisecond}, // 4 digits → truncate
	}
	for _, tc := range cases {
		got, err := parseSRTTime(tc.in)
		if err != nil {
			t.Errorf("parseSRTTime(%q): unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseSRTTime(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// ── parseSRT edge cases ───────────────────────────────────────────────────────

func TestParseSRT_NoSequenceNumber(t *testing.T) {
	// Some tools omit the sequence number and start directly with the timestamp
	srt := "00:00:01,000 --> 00:00:03,000\nHello\n\n"
	cues, err := parseSRT([]byte(srt))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cues) != 1 || cues[0].Text != "Hello" {
		t.Errorf("expected 1 cue with text 'Hello', got %+v", cues)
	}
}

func TestParseSRT_MultipleBlankLineSeparators(t *testing.T) {
	srt := "1\n00:00:01,000 --> 00:00:02,000\nFirst\n\n\n\n2\n00:00:03,000 --> 00:00:04,000\nSecond\n\n"
	cues, err := parseSRT([]byte(srt))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cues) != 2 {
		t.Errorf("expected 2 cues, got %d", len(cues))
	}
}

// ── alignCues boundary edge cases ─────────────────────────────────────────────

func TestAlignCues_MidpointExactlyAtBoundary(t *testing.T) {
	// Secondary cue whose midpoint is exactly at the end of the base cue.
	// mid = 3000ms, base end = 3000ms → included (mid <= b.End)
	base := []SubtitleCue{makeCue(1000, 3000, "Hello")}
	sec := []SubtitleCue{makeCue(2000, 4000, "Hola")} // mid = 3000ms
	got := alignCues(base, sec)
	if len(got) != 1 || got[0] != "Hola" {
		t.Errorf("boundary-inclusive align = %v, want [Hola]", got)
	}
}

func TestAlignCues_MidpointJustOutside(t *testing.T) {
	// Secondary cue whose midpoint is 1ms past the base cue end — must not match.
	base := []SubtitleCue{makeCue(1000, 3000, "Hello")}
	sec := []SubtitleCue{makeCue(2001, 4001, "Hola")} // mid = 3001ms
	got := alignCues(base, sec)
	if len(got) != 1 || got[0] != "" {
		t.Errorf("just-outside align = %v, want [\"\"]", got)
	}
}

// ── writeASS layout tests ─────────────────────────────────────────────────────

func TestWriteASSLayout_StackedBottom(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ass")
	prof := DualSubProfile{
		Layout: "stacked_bottom", FontSize: 52, FontName: "Arial",
		Outline: 2, MarginV: 40, Gap: 10,
	}
	cues := []SubtitleCue{{Start: 0, End: time.Second, Text: "Hello"}}
	sec := []string{"Hola"}
	if err := writeASS(out, prof, cues, sec); err != nil {
		t.Fatalf("writeASS: %v", err)
	}
	data, _ := os.ReadFile(out)
	content := string(data)
	// Bottom style: Alignment=2, MarginV=40
	if !strings.Contains(content, "Style: Bottom,Arial,52,") {
		t.Error("missing Bottom style with Arial,52")
	}
	// Top style: MarginV = margin_v + font_size + gap = 40+52+10 = 102
	if !strings.Contains(content, ",2,10,10,102,") {
		t.Errorf("Top style MarginV should be 102 (40+52+10); content:\n%s", content)
	}
}

func TestWriteASSLayout_Split(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ass")
	prof := DualSubProfile{
		Layout: "split", FontSize: 64, FontName: "Arial",
		Outline: 2, MarginV: 40,
	}
	cues := []SubtitleCue{{Start: 0, End: time.Second, Text: "Hello"}}
	sec := []string{"Hola"}
	if err := writeASS(out, prof, cues, sec); err != nil {
		t.Fatalf("writeASS: %v", err)
	}
	data, _ := os.ReadFile(out)
	content := string(data)
	// Top style: Alignment=8, MarginV=40
	if !strings.Contains(content, ",8,10,10,40,") {
		t.Errorf("Top style should have Alignment=8 MarginV=40; content:\n%s", content)
	}
	// Bottom style: Alignment=2, MarginV=40
	if !strings.Contains(content, ",2,10,10,40,") {
		t.Errorf("Bottom style should have Alignment=2 MarginV=40; content:\n%s", content)
	}
}

func TestWriteASSLayout_StackedTop(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ass")
	prof := DualSubProfile{
		Layout: "stacked_top", FontSize: 52, FontName: "Arial",
		Outline: 2, MarginV: 40, Gap: 10,
	}
	cues := []SubtitleCue{{Start: 0, End: time.Second, Text: "Hello"}}
	sec := []string{"Hola"}
	if err := writeASS(out, prof, cues, sec); err != nil {
		t.Fatalf("writeASS: %v", err)
	}
	data, _ := os.ReadFile(out)
	content := string(data)
	// Top style: Alignment=8, MarginV=40
	if !strings.Contains(content, ",8,10,10,40,") {
		t.Errorf("Top style should have Alignment=8 MarginV=40; content:\n%s", content)
	}
	// Bottom/secondary for stacked_top: MarginV = 40+52+10 = 102, also Alignment=8
	if !strings.Contains(content, ",8,10,10,102,") {
		t.Errorf("Secondary style MarginV should be 102; content:\n%s", content)
	}
}

// ── stripSubTags nested tags ──────────────────────────────────────────────────

func TestStripSubTags_Nested(t *testing.T) {
	got := stripSubTags("<b><i>bold italic</i></b>")
	if got != "bold italic" {
		t.Errorf("nested tags: got %q, want %q", got, "bold italic")
	}
}

// ── isUpToDate missing files ──────────────────────────────────────────────────

func TestIsUpToDate_MissingOutput(t *testing.T) {
	dir := t.TempDir()
	src := dir + "/movie.mkv"
	os.WriteFile(src, []byte("x"), 0644)
	if isUpToDate(dir+"/nonexistent.ass", src) {
		t.Error("isUpToDate should return false when output does not exist")
	}
}

func TestIsUpToDate_MissingSource(t *testing.T) {
	dir := t.TempDir()
	out := dir + "/movie.en-es.ass"
	os.WriteFile(out, []byte("x"), 0644)
	if isUpToDate(out, dir+"/nonexistent.mkv") {
		t.Error("isUpToDate should return false when source does not exist")
	}
}

// ── findSubSidecar precedence ─────────────────────────────────────────────────

func TestFindSubSidecar_SRTPrecedence(t *testing.T) {
	// When both .en.srt and .en.ass exist, .srt is checked first
	dir := t.TempDir()
	base := dir + "/movie.mkv"
	srt := dir + "/movie.en.srt"
	ass := dir + "/movie.en.ass"
	os.WriteFile(base, []byte("x"), 0644)
	os.WriteFile(srt, []byte("sub"), 0644)
	os.WriteFile(ass, []byte("sub"), 0644)

	got := findSubSidecar(base, "en")
	if got != srt {
		t.Errorf("findSubSidecar = %q, want %q (.srt should take precedence)", got, srt)
	}
}

func TestFindSubSidecar_ASSFallback(t *testing.T) {
	dir := t.TempDir()
	base := dir + "/movie.mkv"
	ass := dir + "/movie.en.ass"
	os.WriteFile(base, []byte("x"), 0644)
	os.WriteFile(ass, []byte("sub"), 0644)

	got := findSubSidecar(base, "en")
	if got != ass {
		t.Errorf("findSubSidecar = %q, want %q", got, ass)
	}
}

func TestFindSubSidecar_NotFound(t *testing.T) {
	dir := t.TempDir()
	base := dir + "/movie.mkv"
	os.WriteFile(base, []byte("x"), 0644)

	got := findSubSidecar(base, "en")
	if got != "" {
		t.Errorf("findSubSidecar = %q, want empty string", got)
	}
}

// ── dualSubPath with dotted filename ─────────────────────────────────────────

func TestDualSubPath_DottedFilename(t *testing.T) {
	// Only the final extension should be stripped
	got := dualSubPath("/tv/Show/S01.E01.mkv", "en-es")
	want := "/tv/Show/S01.E01.en-es.ass"
	if got != want {
		t.Errorf("dualSubPath = %q, want %q", got, want)
	}
}

// ── extractEmbeddedSub integration (stubbed ffmpeg) ───────────────────────────

func TestExtractEmbeddedSub_Success(t *testing.T) {
	// Fake ffmpeg that writes a known SRT to its last argument (the output path).
	dir := t.TempDir()
	script := dir + "/ffmpeg"
	srtContent := `1
00:00:01,000 --> 00:00:03,000
Hello from embedded sub

2
00:00:05,000 --> 00:00:07,000
Second cue
`
	scriptBody := "#!/bin/sh\nfor last; do true; done\ncat > \"$last\" <<'SREOF'\n" + srtContent + "SREOF\nexit 0\n"
	os.WriteFile(script, []byte(scriptBody), 0755)
	old := ffmpegCommand
	ffmpegCommand = script
	t.Cleanup(func() { ffmpegCommand = old })

	cues, err := extractEmbeddedSub(context.Background(), "/fake/movie.mkv", 0)
	if err != nil {
		t.Fatalf("extractEmbeddedSub: %v", err)
	}
	if len(cues) != 2 {
		t.Fatalf("expected 2 cues, got %d", len(cues))
	}
	if cues[0].Text != "Hello from embedded sub" {
		t.Errorf("cue[0].Text = %q", cues[0].Text)
	}
}

func TestExtractEmbeddedSub_FFmpegFails(t *testing.T) {
	dir := t.TempDir()
	script := dir + "/ffmpeg"
	os.WriteFile(script, []byte("#!/bin/sh\necho 'error' >&2\nexit 1\n"), 0755)
	old := ffmpegCommand
	ffmpegCommand = script
	t.Cleanup(func() { ffmpegCommand = old })

	_, err := extractEmbeddedSub(context.Background(), "/fake/movie.mkv", 0)
	if err == nil {
		t.Error("expected error from failing ffmpeg")
	}
}

// ── probeSubStreams integration (stubbed ffprobe) ─────────────────────────────

func TestProbeSubStreams_WithSubtitleStreams(t *testing.T) {
	probeJSON := `{
		"format": {"filename": "/fake/movie.mkv", "duration": "90.0", "size": "1000000"},
		"streams": [
			{"index": 0, "codec_type": "video", "codec_name": "h264", "width": 1920, "height": 1080},
			{"index": 1, "codec_type": "audio", "codec_name": "aac"},
			{"index": 2, "codec_type": "subtitle", "codec_name": "subrip", "tags": {"language": "eng"}},
			{"index": 3, "codec_type": "subtitle", "codec_name": "subrip", "tags": {"language": "spa"}}
		]
	}`
	dir := t.TempDir()
	script := dir + "/ffprobe"
	os.WriteFile(script, []byte("#!/bin/sh\necho '"+probeJSON+"'\n"), 0755)
	old := ffprobeCommand
	ffprobeCommand = script
	t.Cleanup(func() { ffprobeCommand = old })

	streams, err := probeSubStreams("/fake/movie.mkv")
	if err != nil {
		t.Fatalf("probeSubStreams: %v", err)
	}
	if len(streams) != 2 {
		t.Fatalf("expected 2 subtitle streams, got %d", len(streams))
	}
	if streams[0].Lang != "en" {
		t.Errorf("stream[0].Lang = %q, want %q", streams[0].Lang, "en")
	}
	if streams[1].Lang != "es" {
		t.Errorf("stream[1].Lang = %q, want %q", streams[1].Lang, "es")
	}
	if streams[0].SubIndex != 0 || streams[1].SubIndex != 1 {
		t.Errorf("SubIndex: stream[0]=%d stream[1]=%d, want 0 and 1", streams[0].SubIndex, streams[1].SubIndex)
	}
}

func TestProbeSubStreams_NoBitmapSubs(t *testing.T) {
	// PGS/hdmv_pgs_subtitle should be listed by probeSubStreams but filtered
	// out by isTextSubCodec when getCues is called.
	probeJSON := `{
		"format": {"filename": "/fake/movie.mkv", "duration": "90.0", "size": "1000000"},
		"streams": [
			{"index": 0, "codec_type": "subtitle", "codec_name": "hdmv_pgs_subtitle", "tags": {"language": "eng"}}
		]
	}`
	dir := t.TempDir()
	script := dir + "/ffprobe"
	os.WriteFile(script, []byte("#!/bin/sh\necho '"+probeJSON+"'\n"), 0755)
	old := ffprobeCommand
	ffprobeCommand = script
	t.Cleanup(func() { ffprobeCommand = old })

	streams, err := probeSubStreams("/fake/movie.mkv")
	if err != nil {
		t.Fatalf("probeSubStreams: %v", err)
	}
	if len(streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(streams))
	}
	// isTextSubCodec rejects this
	if isTextSubCodec(streams[0].CodecName) {
		t.Errorf("hdmv_pgs_subtitle should not be a text sub codec")
	}
}

func TestIsTextSubCodec(t *testing.T) {
	cases := []struct {
		codec string
		want  bool
	}{
		// Text-based codecs accepted by isTextSubCodec
		{"subrip", true},
		{"ass", true},
		{"ssa", true},
		{"webvtt", true},
		{"mov_text", true},
		{"text", true},
		// Codec names are lower-cased before the switch; verify case-insensitivity
		{"SUBRIP", true},
		{"ASS", true},
		// Bitmap codecs — not supported (require OCR)
		{"hdmv_pgs_subtitle", false},
		{"dvd_subtitle", false},
		{"dvb_subtitle", false},
		{"xsub", false},
		// Unknown / empty
		{"", false},
		{"unknown_codec", false},
	}
	for _, c := range cases {
		t.Run(c.codec, func(t *testing.T) {
			if got := isTextSubCodec(c.codec); got != c.want {
				t.Errorf("isTextSubCodec(%q) = %v, want %v", c.codec, got, c.want)
			}
		})
	}
}

func TestSubtitleTracksForPath(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "Movie.mkv")
	files := []string{
		"Movie.en.srt",
		"Movie.en.hi.srt",
		"Movie.es.srt",
		"Movie.es.forced.srt",
		"Movie.fr.sdh.srt",
	}
	for _, f := range files {
		os.WriteFile(filepath.Join(dir, f), []byte(""), 0644)
	}
	tracks := subtitleTracksForPath(base)
	if len(tracks) != len(files) {
		t.Fatalf("expected %d tracks, got %d", len(files), len(tracks))
	}
	byFile := map[string]SubtitleTrack{}
	for _, tr := range tracks {
		byFile[filepath.Base(tr.File)] = tr
	}
	if byFile["Movie.en.srt"].Variant != "regular" {
		t.Errorf("Movie.en.srt variant = %q, want regular", byFile["Movie.en.srt"].Variant)
	}
	if byFile["Movie.en.hi.srt"].Variant != "hi" {
		t.Errorf("Movie.en.hi.srt variant = %q, want hi", byFile["Movie.en.hi.srt"].Variant)
	}
	if byFile["Movie.es.forced.srt"].Variant != "forced" {
		t.Errorf("Movie.es.forced.srt variant = %q, want forced", byFile["Movie.es.forced.srt"].Variant)
	}
	if byFile["Movie.fr.sdh.srt"].Variant != "hi" {
		t.Errorf("Movie.fr.sdh.srt variant = %q, want hi (sdh maps to hi)", byFile["Movie.fr.sdh.srt"].Variant)
	}
	if byFile["Movie.en.srt"].Lang != "en" {
		t.Errorf("Movie.en.srt lang = %q, want en", byFile["Movie.en.srt"].Lang)
	}
}

func TestDetectSubVariant(t *testing.T) {
	cases := []struct{ file, want string }{
		{"Movie.en.srt", "regular"},
		{"Movie.en.hi.srt", "hi"},
		{"Movie.en.HI.srt", "hi"},
		{"Movie.es.forced.srt", "forced"},
		{"Movie.fr.sdh.srt", "hi"},
		{"Movie.de.srt", "regular"},
	}
	for _, c := range cases {
		got := detectSubVariant(c.file)
		if got != c.want {
			t.Errorf("detectSubVariant(%q) = %q, want %q", c.file, got, c.want)
		}
	}
}

// ── EmbeddedTrack struct ──────────────────────────────────────────────────────

func TestEmbeddedTrackStruct(t *testing.T) {
	// Verify the struct exists and JSON tags are correct
	et := EmbeddedTrack{SubIndex: 2, Lang: "es", CodecName: "subrip"}
	data, err := json.Marshal(et)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m["sub_index"] != float64(2) {
		t.Errorf("sub_index = %v, want 2", m["sub_index"])
	}
	if m["lang"] != "es" {
		t.Errorf("lang = %v, want es", m["lang"])
	}
	if m["codec"] != "subrip" {
		t.Errorf("codec = %v, want subrip", m["codec"])
	}
}

func TestFilterTextEmbeddedTracks(t *testing.T) {
	streams := []subStream{
		{SubIndex: 0, Lang: "en", CodecName: "subrip"},
		{SubIndex: 1, Lang: "es", CodecName: "ass"},
		{SubIndex: 2, Lang: "ja", CodecName: "hdmv_pgs_subtitle"},
		{SubIndex: 3, Lang: "fr", CodecName: "webvtt"},
	}
	got := filterTextEmbeddedTracks(streams)
	if len(got) != 3 {
		t.Fatalf("expected 3 text tracks, got %d", len(got))
	}
	wantIndices := []int{0, 1, 3}
	wantLangs := []string{"en", "es", "fr"}
	wantCodecs := []string{"subrip", "ass", "webvtt"}
	for i, et := range got {
		if et.SubIndex != wantIndices[i] {
			t.Errorf("[%d] SubIndex = %d, want %d", i, et.SubIndex, wantIndices[i])
		}
		if et.Lang != wantLangs[i] {
			t.Errorf("[%d] Lang = %q, want %q", i, et.Lang, wantLangs[i])
		}
		if et.CodecName != wantCodecs[i] {
			t.Errorf("[%d] CodecName = %q, want %q", i, et.CodecName, wantCodecs[i])
		}
	}
}

// ── DeleteDualSubSidecars ─────────────────────────────────────────────────────

func TestDeleteDualSubSidecars(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "Movie.mkv")

	// dual-sub sidecars — should be deleted
	dualSubs := []string{"Movie.en-es.ass", "Movie.en-fr.ass"}
	// regular sidecar — must NOT be deleted
	regular := []string{"Movie.en.srt", "Movie.es.ass"}

	for _, f := range append(dualSubs, regular...) {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(""), 0644); err != nil {
			t.Fatal(err)
		}
	}

	count := DeleteDualSubSidecars(base)
	if count != len(dualSubs) {
		t.Errorf("DeleteDualSubSidecars returned %d, want %d", count, len(dualSubs))
	}

	// verify dual-sub sidecars are gone
	for _, f := range dualSubs {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("%s should have been deleted", f)
		}
	}

	// verify regular sidecars still exist
	for _, f := range regular {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s should still exist: %v", f, err)
		}
	}
}
