// actions.go — central action registry for procula's action bus.
//
// Actions are discrete operations on library items (validate, transcode,
// subtitle_refresh). Each action is registered with a Handler that runs inside
// the worker loop when a job's ActionType != "pipeline".
package main

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
}

func arrTypeFromPath(p string) string {
	if strings.HasPrefix(p, "/tv/") || p == "/tv" {
		return "sonarr"
	}
	return "radarr"
}

func mediaTypeFromPath(p string) string {
	if strings.HasPrefix(p, "/tv/") || p == "/tv" {
		return "episode"
	}
	return "movie"
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
	if parent := filepath.Base(filepath.Dir(path)); parent != "movies" && parent != "tv" {
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

// runDualSubAction accepts a DualSubJob payload via job.Params:
//
//	"path"    string        (required) — media file path
//	"profile" string        (optional) — profile name; defaults to "Default"
//	"pairs"   []interface{} (required) — each entry: {"top_file":"…","bottom_file":"…"}
func runDualSubAction(ctx context.Context, q *Queue, job *Job) (map[string]any, error) {
	path, _ := job.Params["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("dualsub: path required")
	}

	rawPairs, _ := job.Params["pairs"].([]any)
	var pairs []TrackPair
	for _, rp := range rawPairs {
		m, ok := rp.(map[string]any)
		if !ok {
			continue
		}
		top, _ := m["top_file"].(string)
		bottom, _ := m["bottom_file"].(string)
		if top != "" && bottom != "" {
			pairs = append(pairs, TrackPair{TopFile: top, BottomFile: bottom})
		}
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("dualsub: at least one track pair required")
	}

	profileName, _ := job.Params["profile"].(string)
	prof := FindDualSubProfile(appDB, profileName)

	var outputs []string
	var warnings []string
	for _, pair := range pairs {
		topBase := strings.TrimSuffix(filepath.Base(pair.TopFile), filepath.Ext(pair.TopFile))
		botBase := strings.TrimSuffix(filepath.Base(pair.BottomFile), filepath.Ext(pair.BottomFile))
		topLang := langTagFromBase(topBase)
		botLang := langTagFromBase(botBase)
		mediaBase := strings.TrimSuffix(path, filepath.Ext(path))
		outPath := mediaBase + "." + topLang + "-" + botLang + ".ass"

		os.Remove(outPath) //nolint:errcheck

		topCues, err := parseSidecarFile(pair.TopFile)
		if err != nil || len(topCues) == 0 {
			warnings = append(warnings, fmt.Sprintf("top track %s: %v", filepath.Base(pair.TopFile), err))
			continue
		}
		botCues, err := parseSidecarFile(pair.BottomFile)
		if err != nil || len(botCues) == 0 {
			warnings = append(warnings, fmt.Sprintf("bottom track %s: %v", filepath.Base(pair.BottomFile), err))
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
// "Movie.en" → "en", "Movie.en.hi" → "en"
func langTagFromBase(base string) string {
	parts := strings.Split(base, ".")
	if len(parts) >= 2 {
		return normalizeLangCode(parts[len(parts)-1])
	}
	return base
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
