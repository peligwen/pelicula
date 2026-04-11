// actions.go — central action registry for procula's action bus.
//
// Actions are discrete operations on library items (validate, transcode,
// subtitle_refresh). Each action is registered with a Handler that runs inside
// the worker loop when a job's ActionType != "pipeline".
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
		Name:        "subtitle_refresh",
		Label:       "Refresh subtitles",
		AppliesTo:   []string{"movie", "episode"},
		Sync:        true,
		Description: "Ask Bazarr to re-search subtitles for this item.",
		Handler:     runSubtitleRefreshAction,
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

// runSubtitleRefreshAction calls Bazarr directly using the target's arr IDs.
func runSubtitleRefreshAction(ctx context.Context, q *Queue, job *Job) (map[string]any, error) {
	arrType, _ := job.Params["arr_type"].(string)
	arrIDf, _ := job.Params["arr_id"].(float64)
	epIDf, _ := job.Params["episode_id"].(float64)
	if arrType == "" || arrIDf == 0 {
		return nil, fmt.Errorf("subtitle_refresh: arr_type and arr_id required")
	}
	synthetic := &Job{
		ID: "action-" + job.ID,
		Source: JobSource{
			ArrType:   arrType,
			ArrID:     int(arrIDf),
			EpisodeID: int(epIDf),
		},
	}
	bazarrSearchSubtitles(ctx, configDir, synthetic)
	return map[string]any{"triggered": true}, nil
}
