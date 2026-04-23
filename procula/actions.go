// actions.go — central action registry for procula's action bus.
//
// Actions are discrete operations on library items (validate, transcode,
// subtitle_refresh). Each action is registered with a Handler that runs inside
// the worker loop when a job's ActionType != "pipeline".
package procula

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ActionTarget identifies a library item an action applies to.
type ActionTarget struct {
	Path      string `json:"path,omitempty"`
	ArrType   string `json:"arr_type,omitempty"`
	ArrID     int    `json:"arr_id,omitempty"`
	EpisodeID int    `json:"episode_id,omitempty"`
}

// ActionRequest is the shape POSTed to /api/procula/actions.
type ActionRequest struct {
	Action string         `json:"action"`
	Target ActionTarget   `json:"target"`
	Params map[string]any `json:"params,omitempty"`
}

// ActionResult is returned inline when ?wait=N is used.
type ActionResult struct {
	JobID  string         `json:"job_id"`
	State  string         `json:"state"`
	Error  string         `json:"error,omitempty"`
	Result map[string]any `json:"result,omitempty"`
}

// ActionHandler runs in the worker goroutine.
type ActionHandler func(ctx context.Context, q *Queue, job *Job) (map[string]any, error)

// ActionDef describes one registered action.
type ActionDef struct {
	Name        string        `json:"name"`
	Label       string        `json:"label"`
	AppliesTo   []string      `json:"applies_to"`
	Sync        bool          `json:"sync"`
	Description string        `json:"description,omitempty"`
	Handler     ActionHandler `json:"-"`
}

var (
	actionRegistryMu sync.RWMutex
	actionRegistry   = map[string]*ActionDef{}
)

// Register adds an action to the registry.
func Register(def *ActionDef) {
	actionRegistryMu.Lock()
	defer actionRegistryMu.Unlock()
	actionRegistry[def.Name] = def
}

// Lookup returns the ActionDef for name, or nil if unknown.
func Lookup(name string) *ActionDef {
	actionRegistryMu.RLock()
	defer actionRegistryMu.RUnlock()
	return actionRegistry[name]
}

// List returns all registered actions sorted by name.
func List() []*ActionDef {
	actionRegistryMu.RLock()
	defer actionRegistryMu.RUnlock()
	out := make([]*ActionDef, 0, len(actionRegistry))
	for _, d := range actionRegistry {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// registerBuiltinActions wires the v1 action handlers. Called from main().
func registerBuiltinActions() {
	Register(&ActionDef{
		Name:        "validate",
		Label:       "Re-verify file",
		AppliesTo:   []string{"movie", "episode"},
		Sync:        true,
		Description: "Run ffprobe integrity/duration/sample checks on the source file.",
		Handler:     runValidateAction,
	})
	Register(&ActionDef{
		Name:        "transcode",
		Label:       "Re-transcode",
		AppliesTo:   []string{"movie", "episode"},
		Sync:        false,
		Description: "Run the current transcoding profile against the file.",
		Handler:     runTranscodeAction,
	})
	Register(&ActionDef{
		Name:        "subtitle_search",
		Label:       "Search subtitles\u2026",
		AppliesTo:   []string{"movie", "episode"},
		Sync:        true,
		Description: "Search Bazarr for subtitles with explicit languages, HI, and forced flags.",
		Handler:     runSubtitleSearchAction,
	})
	Register(&ActionDef{
		Name:        "dualsub",
		Label:       "Dual subtitles\u2026",
		AppliesTo:   []string{"movie", "episode"},
		Sync:        true,
		Description: "Generate dual-language ASS subtitle sidecars with a chosen profile and track pair.",
		Handler:     runDualSubAction,
	})
	Register(&ActionDef{
		Name:        "replace",
		Label:       "Replace\u2026",
		AppliesTo:   []string{"movie", "episode"},
		Sync:        true,
		Description: "Delete this file, blocklist the release in Sonarr/Radarr, and trigger a fresh search.",
		Handler:     runReplaceAction,
	})
}

// runValidateAction builds a synthetic Job and calls Validate.
func runValidateAction(ctx context.Context, q *Queue, job *Job) (map[string]any, error) {
	path, _ := job.Params["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("validate: path required")
	}
	syntheticJob := &Job{Source: JobSource{Path: path}}
	result, failReason := Validate(syntheticJob)
	out := map[string]any{
		"passed":      result.Passed,
		"checks":      result.Checks,
		"fail_reason": failReason,
	}
	return out, nil
}

// runTranscodeAction runs the manual transcode pipeline against a library file.
func runTranscodeAction(ctx context.Context, q *Queue, job *Job) (map[string]any, error) {
	path, _ := job.Params["path"].(string)
	profile, _ := job.Params["profile"].(string)
	if path == "" || profile == "" {
		return nil, fmt.Errorf("transcode: path and profile required")
	}

	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}
	arrType := arrTypeFromPath(path)
	mediaType := mediaTypeFromPath(path)
	title := strings.TrimSuffix(fi.Name(), filepath.Ext(fi.Name()))
	if parent := filepath.Base(filepath.Dir(path)); !isLibrarySlug(parent) {
		title = parent
	}

	err = q.Update(job.ID, func(j *Job) {
		j.Source = JobSource{Path: path, Size: fi.Size(), Title: title, ArrType: arrType, Type: mediaType}
		j.ManualProfile = profile
	})
	if err != nil {
		return nil, err
	}
	runManualTranscode(ctx, q, job.ID, configDir, env("PELICULA_API_URL", "http://pelicula-api:8181"))

	fresh, _ := q.Get(job.ID)
	return map[string]any{
		"decision": fresh.TranscodeDecision,
		"outputs":  fresh.TranscodeOutputs,
		"profile":  fresh.TranscodeProfile,
		"error":    fresh.TranscodeError,
	}, nil
}

// runSubtitleSearchAction dispatches a targeted Bazarr search. Params:
//
//	languages:  []string (required, at least one ISO 639-1 code)
//	hi:         bool (default false)
//	forced:     bool (default false)
//	arr_type:   "radarr" | "sonarr" (required)
//	arr_id:     int (required)
//	episode_id: int (required for sonarr)
func runSubtitleSearchAction(ctx context.Context, q *Queue, job *Job) (map[string]any, error) {
	arrType, _ := job.Params["arr_type"].(string)
	arrIDf, _ := job.Params["arr_id"].(float64)
	epIDf, _ := job.Params["episode_id"].(float64)
	if arrType == "" || arrIDf == 0 {
		return nil, fmt.Errorf("subtitle_search: arr_type and arr_id required")
	}

	var langs []string
	if raw, ok := job.Params["languages"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				langs = append(langs, s)
			}
		}
	}
	if len(langs) == 0 {
		return nil, fmt.Errorf("subtitle_search: languages required")
	}

	hi, _ := job.Params["hi"].(bool)
	forced, _ := job.Params["forced"].(bool)

	synthetic := &Job{
		ID: "action-" + job.ID,
		Source: JobSource{
			ArrType:   arrType,
			ArrID:     int(arrIDf),
			EpisodeID: int(epIDf),
		},
	}
	opts := BazarrSearchOpts{Languages: langs, HI: hi, Forced: forced}
	bazarrSearchSubtitlesWithOpts(ctx, configDir, synthetic, opts)
	return map[string]any{
		"triggered": true,
		"languages": langs,
		"hi":        hi,
		"forced":    forced,
	}, nil
}

// parseDualSubPairs parses raw JSON pair entries into TrackPairs.
// Each entry can use file paths, sub_index values, or a mix of both.
func parseDualSubPairs(rawPairs []any) []TrackPair {
	var pairs []TrackPair
	for _, rp := range rawPairs {
		m, ok := rp.(map[string]any)
		if !ok {
			continue
		}
		var p TrackPair

		p.TopFile, _ = m["top_file"].(string)
		p.BottomFile, _ = m["bottom_file"].(string)

		if v, ok := m["top_sub_index"].(float64); ok {
			n := int(v)
			p.TopSubIndex = &n
		}
		if v, ok := m["bottom_sub_index"].(float64); ok {
			n := int(v)
			p.BottomSubIndex = &n
		}

		// At least one source on each side
		hasTop := p.TopFile != "" || p.TopSubIndex != nil
		hasBot := p.BottomFile != "" || p.BottomSubIndex != nil
		if hasTop && hasBot {
			pairs = append(pairs, p)
		}
	}
	return pairs
}

// derefSubIndex converts a *int TrackPair index to the int sentinel convention
// used internally: nil → -1 (use file path), non-nil → the stream index.
func derefSubIndex(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

// resolvePairCues loads subtitle cues from either a sidecar file or an embedded stream.
// subIndex < 0 means use the file; subIndex >= 0 means use the embedded stream.
func resolvePairCues(ctx context.Context, mediaPath, file string, subIndex int) ([]SubtitleCue, error) {
	if subIndex >= 0 {
		return extractEmbeddedSub(ctx, mediaPath, subIndex)
	}
	return parseSidecarFile(file)
}

// pairSideLang determines the language tag for one side of a track pair.
// For file sources, extracts from the filename. For embedded streams,
// looks up the lang in the probed streams list.
func pairSideLang(file string, subIndex int, streams []subStream) string {
	if subIndex >= 0 {
		for _, s := range streams {
			if s.SubIndex == subIndex {
				return s.Lang
			}
		}
		return "und"
	}
	base := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	return langTagFromBase(base)
}

// runDualSubAction accepts a DualSubJob payload via job.Params:
//
//	"path"    string        (required) — media file path
//	"profile" string        (optional) — profile name; defaults to "Default"
//	"pairs"   []interface{} (required) — each entry may use top_file/bottom_file paths,
//	                                      top_sub_index/bottom_sub_index, or a mix
func runDualSubAction(ctx context.Context, q *Queue, job *Job) (map[string]any, error) {
	path, _ := job.Params["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("dualsub: path required")
	}

	// Probe embedded streams (needed for sub_index resolution and lang tags)
	streams, _ := probeSubStreams(ctx, path)

	rawPairs, _ := job.Params["pairs"].([]any)
	pairs := parseDualSubPairs(rawPairs)
	if len(pairs) == 0 {
		return nil, fmt.Errorf("dualsub: at least one track pair required")
	}

	profileName, _ := job.Params["profile"].(string)
	prof := FindDualSubProfile(appDB, profileName)

	var outputs []string
	var warnings []string
	for _, pair := range pairs {
		// Dereference *int sentinel fields; -1 means "not set, use file path".
		topIdx := derefSubIndex(pair.TopSubIndex)
		botIdx := derefSubIndex(pair.BottomSubIndex)

		// Determine language tags for the output filename
		topLang := pairSideLang(pair.TopFile, topIdx, streams)
		botLang := pairSideLang(pair.BottomFile, botIdx, streams)
		mediaBase := strings.TrimSuffix(path, filepath.Ext(path))
		outPath := mediaBase + "." + topLang + "-" + botLang + ".ass"

		os.Remove(outPath) //nolint:errcheck

		topCues, err := resolvePairCues(ctx, path, pair.TopFile, topIdx)
		if err != nil {
			label := pair.TopFile
			if topIdx >= 0 {
				label = fmt.Sprintf("embedded:%d", topIdx)
			}
			warnings = append(warnings, fmt.Sprintf("top track %s: %v", label, err))
			continue
		} else if len(topCues) == 0 {
			label := pair.TopFile
			if topIdx >= 0 {
				label = fmt.Sprintf("embedded:%d", topIdx)
			}
			warnings = append(warnings, fmt.Sprintf("top track %s: no cues found", label))
			continue
		}
		botCues, err := resolvePairCues(ctx, path, pair.BottomFile, botIdx)
		if err != nil {
			label := pair.BottomFile
			if botIdx >= 0 {
				label = fmt.Sprintf("embedded:%d", botIdx)
			}
			warnings = append(warnings, fmt.Sprintf("bottom track %s: %v", label, err))
			continue
		} else if len(botCues) == 0 {
			label := pair.BottomFile
			if botIdx >= 0 {
				label = fmt.Sprintf("embedded:%d", botIdx)
			}
			warnings = append(warnings, fmt.Sprintf("bottom track %s: no cues found", label))
			continue
		}

		bottomTexts := alignCues(topCues, botCues)
		if err := writeASS(outPath, prof, topCues, bottomTexts); err != nil {
			warnings = append(warnings, fmt.Sprintf("write %s: %v", filepath.Base(outPath), err))
			continue
		}
		outputs = append(outputs, outPath)
		slog.Info("dual sub generated", "component", "dualsub", "output", outPath, "profile", prof.Name)
	}

	result := map[string]any{"outputs": outputs}
	if len(warnings) > 0 {
		result["warnings"] = warnings
	}
	return result, nil
}

// langTagFromBase extracts the primary language tag from a subtitle base name.
// "Movie.en" → "en", "Movie.en.hi" → "en", "Movie.es.forced" → "es"
func langTagFromBase(base string) string {
	variantSuffixes := map[string]bool{"hi": true, "sdh": true, "forced": true}
	parts := strings.Split(base, ".")
	// Walk from the end, skip known variant suffixes
	for i := len(parts) - 1; i >= 0; i-- {
		seg := strings.ToLower(parts[i])
		if !variantSuffixes[seg] {
			return normalizeLangCode(seg)
		}
	}
	return "und" // undetermined — all segments were variant suffixes
}

// parseSidecarFile reads SubtitleCues from a .srt or .ass file.
func parseSidecarFile(path string) ([]SubtitleCue, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if strings.ToLower(filepath.Ext(path)) == ".ass" {
		return parseASSCues(data)
	}
	return parseSRT(data)
}

// parseASSCues extracts SubtitleCues from an ASS file by reading [Events] Dialogue lines.
func parseASSCues(data []byte) ([]SubtitleCue, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	var cues []SubtitleCue
	inEvents := false
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "[Events]" {
			inEvents = true
			continue
		}
		if !inEvents || !strings.HasPrefix(line, "Dialogue:") {
			continue
		}
		// Format: Layer,Start,End,Style,Name,MarginL,MarginR,MarginV,Effect,Text
		fields := strings.SplitN(line[len("Dialogue:"):], ",", 10)
		if len(fields) < 10 {
			continue
		}
		start, err1 := parseASSTime(strings.TrimSpace(fields[1]))
		end, err2 := parseASSTime(strings.TrimSpace(fields[2]))
		if err1 != nil || err2 != nil {
			continue
		}
		rawText := strings.TrimSpace(fields[9])
		rawText = assTagRE.ReplaceAllString(rawText, "")
		rawText = strings.ReplaceAll(rawText, "\\N", "\n")
		if rawText != "" {
			cues = append(cues, SubtitleCue{Start: start, End: end, Text: rawText})
		}
	}
	return cues, nil
}

// parseASSTime parses an ASS timestamp "H:MM:SS.cs" into a Duration.
func parseASSTime(s string) (time.Duration, error) {
	var h, m, sec, cs int
	if _, err := fmt.Sscanf(s, "%d:%d:%d.%d", &h, &m, &sec, &cs); err != nil {
		return 0, fmt.Errorf("parse ASS time %q: %w", s, err)
	}
	return time.Duration(h)*time.Hour +
		time.Duration(m)*time.Minute +
		time.Duration(sec)*time.Second +
		time.Duration(cs)*10*time.Millisecond, nil
}
