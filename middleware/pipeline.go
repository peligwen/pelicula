package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ── Procula job shape (subset) ────────────────────────────────────────────────

// ProculaJobMin is the fields we parse from /api/procula/jobs for aggregation.
type ProculaJobMin struct {
	ID         string    `json:"id"`
	State      string    `json:"state"`   // queued, processing, completed, failed, cancelled
	Stage      string    `json:"stage"`   // validate, process, catalog, done
	Progress   float64   `json:"progress"`
	Error      string    `json:"error,omitempty"`
	RetryCount int       `json:"retry_count"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Source     struct {
		Type         string `json:"type"`
		Title        string `json:"title"`
		Year         int    `json:"year"`
		ArrType      string `json:"arr_type"`
		DownloadHash string `json:"download_hash"`
	} `json:"source"`
	TranscodeProfile  string  `json:"transcode_profile,omitempty"`
	TranscodeDecision string  `json:"transcode_decision,omitempty"`
	TranscodeETA      float64 `json:"transcode_eta,omitempty"`
	Validation        *struct {
		Passed bool `json:"passed"`
		Checks struct {
			Integrity string `json:"integrity"`
			Duration  string `json:"duration"`
			Sample    string `json:"sample"`
		} `json:"checks"`
	} `json:"validation,omitempty"`
	MissingSubs []string `json:"missing_subs,omitempty"`
}

// ── Pipeline card types ───────────────────────────────────────────────────────

// ValidationChecksMin is the subset of validation check results surfaced on cards.
type ValidationChecksMin struct {
	Integrity string `json:"integrity,omitempty"`
	Duration  string `json:"duration,omitempty"`
	Sample    string `json:"sample,omitempty"`
}

// PipelineItemSource records which upstream(s) contributed to this card.
type PipelineItemSource struct {
	QbtHash string `json:"qbt_hash,omitempty"`
	JobID   string `json:"job_id,omitempty"`
	ArrType string `json:"arr_type,omitempty"`
}

// PipelineItem is a unified card for the pipeline board.
type PipelineItem struct {
	Key        string               `json:"key"`
	Title      string               `json:"title"`
	Year       int                  `json:"year,omitempty"`
	MediaType  string               `json:"media_type,omitempty"`
	Poster     string               `json:"poster,omitempty"`
	Lane       string               `json:"lane"`
	State      string               `json:"state"`   // active, paused, failed, done
	Progress   float64              `json:"progress"`
	ETASecs    int64                `json:"eta_seconds,omitempty"`
	Detail     string               `json:"detail,omitempty"`
	SpeedDown  int64                `json:"speed_down,omitempty"`
	SpeedUp    int64                `json:"speed_up,omitempty"`
	RetryCount int                  `json:"retry_count,omitempty"`
	Error      string               `json:"error,omitempty"`
	Source     PipelineItemSource   `json:"source"`
	Actions    []string             `json:"actions"`
	Checks     *ValidationChecksMin `json:"checks,omitempty"`
	MissingSubs []string            `json:"missing_subs,omitempty"`
	UpdatedAt  time.Time            `json:"updated_at"`
}

// PipelineStats is aggregated summary data for the board header.
type PipelineStats struct {
	Active  int   `json:"active"`
	Failed  int   `json:"failed"`
	DLSpeed int64 `json:"dl_speed"`
	UPSpeed int64 `json:"up_speed"`
}

// PipelineResponse is the full /api/pelicula/pipeline payload.
type PipelineResponse struct {
	Lanes       map[string][]PipelineItem `json:"lanes"`
	Stats       PipelineStats             `json:"stats"`
	GeneratedAt time.Time                 `json:"generated_at"`
}

// ── Dismissed store ───────────────────────────────────────────────────────────

// DismissedStore is a SQLite-backed set of dismissed Procula job IDs.
// Failed jobs in the dismissed set are hidden from the needs_attention lane.
type DismissedStore struct {
	db *sql.DB
}

var dismissedStore *DismissedStore

// NewDismissedStore creates a DismissedStore backed by db.
func NewDismissedStore(db *sql.DB) *DismissedStore {
	return &DismissedStore{db: db}
}

// IsDismissed reports whether the given job ID has been dismissed.
func (ds *DismissedStore) IsDismissed(id string) bool {
	var found int
	err := ds.db.QueryRow(`SELECT 1 FROM dismissed_jobs WHERE job_id = ?`, id).Scan(&found)
	return err == nil && found == 1
}

// Dismiss adds a job ID to the dismissed set.
func (ds *DismissedStore) Dismiss(id string) error {
	_, err := ds.db.Exec(`INSERT OR IGNORE INTO dismissed_jobs (job_id) VALUES (?)`, id)
	return err
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// handlePipelineGet aggregates qBittorrent torrents and Procula jobs into a
// unified pipeline board response. Items are joined by download hash, then
// classified into lanes based on their state.
func handlePipelineGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	type qbtFetchResult struct {
		torrents []Torrent
		dlSpeed  int64
		upSpeed  int64
		err      error
	}
	type proculaFetchResult struct {
		jobs []ProculaJobMin
		err  error
	}
	type monitoringFetchResult struct {
		items []PipelineItem
	}

	qbtCh := make(chan qbtFetchResult, 1)
	proculaCh := make(chan proculaFetchResult, 1)
	monitoringCh := make(chan monitoringFetchResult, 1)

	go func() {
		data, err := services.QbtGet("/api/v2/torrents/info")
		if err != nil {
			qbtCh <- qbtFetchResult{err: err}
			return
		}
		var rawList []map[string]any
		if err := json.Unmarshal(data, &rawList); err != nil {
			qbtCh <- qbtFetchResult{err: err}
			return
		}
		torrents := make([]Torrent, 0, len(rawList))
		for _, rt := range rawList {
			torrents = append(torrents, Torrent{
				Hash:     strVal(rt, "hash"),
				Name:     strVal(rt, "name"),
				Progress: floatVal(rt, "progress"),
				DLSpeed:  intVal(rt, "dlspeed"),
				UPSpeed:  intVal(rt, "upspeed"),
				ETA:      intVal(rt, "eta"),
				State:    strVal(rt, "state"),
				Size:     intVal(rt, "size"),
				Category: strVal(rt, "category"),
			})
		}
		var dlSpeed, upSpeed int64
		if statsData, err := services.QbtGet("/api/v2/transfer/info"); err == nil {
			var rawStats map[string]any
			if json.Unmarshal(statsData, &rawStats) == nil {
				dlSpeed = intVal(rawStats, "dl_info_speed")
				upSpeed = intVal(rawStats, "up_info_speed")
			}
		}
		qbtCh <- qbtFetchResult{torrents: torrents, dlSpeed: dlSpeed, upSpeed: upSpeed}
	}()

	go func() {
		resp, err := services.client.Get(proculaURL + "/api/procula/jobs")
		if err != nil {
			proculaCh <- proculaFetchResult{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			proculaCh <- proculaFetchResult{err: err}
			return
		}
		var jobs []ProculaJobMin
		if err := json.Unmarshal(body, &jobs); err != nil {
			proculaCh <- proculaFetchResult{err: err}
			return
		}
		proculaCh <- proculaFetchResult{jobs: jobs}
	}()

	go func() {
		if requestStore == nil {
			monitoringCh <- monitoringFetchResult{}
			return
		}
		reqs := requestStore.all()
		var items []PipelineItem
		for _, req := range reqs {
			if req.State != RequestGrabbed {
				continue
			}
			items = append(items, PipelineItem{
				Key:       "req:" + req.ID,
				Title:     req.Title,
				Year:      req.Year,
				MediaType: req.Type,
				Poster:    req.Poster,
				Lane:      "monitoring",
				State:     "active",
				Actions:   []string{},
				UpdatedAt: req.UpdatedAt,
			})
		}
		monitoringCh <- monitoringFetchResult{items: items}
	}()

	qbtRes := <-qbtCh
	proculaRes := <-proculaCh
	monitoringRes := <-monitoringCh

	lanes := map[string][]PipelineItem{
		"downloading":     {},
		"imported":        {},
		"monitoring":      {},
		"validating":      {},
		"processing":      {},
		"cataloging":      {},
		"completed":       {},
		"needs_attention": {},
	}
	var stats PipelineStats
	if qbtRes.err == nil {
		stats.DLSpeed = qbtRes.dlSpeed
		stats.UPSpeed = qbtRes.upSpeed
	}

	// Build a hash→job lookup so we can join qBt torrents to Procula jobs.
	// We only join by download hash (reliable) — torrent names are release-group
	// filenames and can't be matched to clean titles.
	jobsByHash := make(map[string]*ProculaJobMin) // lowercase hash → job
	if proculaRes.err == nil {
		for i := range proculaRes.jobs {
			j := &proculaRes.jobs[i]
			if h := strings.ToLower(j.Source.DownloadHash); h != "" {
				jobsByHash[h] = j
			}
		}
	}

	// Pair each qBt torrent with its Procula job (if any).
	matchedJobIDs := make(map[string]bool)   // job IDs that have a qBt partner
	matchedQbtHashes := make(map[string]bool) // qBt hashes that have a Procula job
	qbtByHash := make(map[string]Torrent)    // for supplementing Procula cards
	if qbtRes.err == nil {
		for _, t := range qbtRes.torrents {
			h := strings.ToLower(t.Hash)
			qbtByHash[h] = t
			if j, ok := jobsByHash[h]; ok {
				matchedJobIDs[j.ID] = true
				matchedQbtHashes[h] = true
			}
		}
	}

	// Classify unmatched qBt torrents into downloading / imported lanes.
	if qbtRes.err == nil {
		for _, t := range qbtRes.torrents {
			h := strings.ToLower(t.Hash)
			if matchedQbtHashes[h] {
				continue // this torrent's progress is shown on its Procula card
			}
			lane := qbtLaneFor(t.State)
			if lane == "" {
				continue
			}
			item := PipelineItem{
				Key:       "qbt:" + h,
				Title:     t.Name,
				Lane:      lane,
				State:     qbtItemState(t.State),
				Progress:  t.Progress,
				ETASecs:   t.ETA,
				SpeedDown: t.DLSpeed,
				SpeedUp:   t.UPSpeed,
				Source:    PipelineItemSource{QbtHash: t.Hash},
				Actions:   actionsForLane(lane),
				UpdatedAt: time.Now(),
			}
			lanes[lane] = append(lanes[lane], item)
			if lane == "downloading" {
				stats.Active++
			}
		}
	}

	// Classify Procula jobs and build their cards.
	if proculaRes.err == nil {
		now := time.Now()
		completedCutoff := now.Add(-6 * time.Hour)

		for i := range proculaRes.jobs {
			j := &proculaRes.jobs[i]
			lane := proculaLaneFor(j, dismissedStore)
			if lane == "" {
				continue
			}
			// Completed tail: skip items older than 6h (sorted + trimmed after loop)
			if lane == "completed" && j.UpdatedAt.Before(completedCutoff) {
				continue
			}

			item := PipelineItem{
				Key:         "job:" + j.ID,
				Title:       j.Source.Title,
				Year:        j.Source.Year,
				MediaType:   j.Source.Type,
				Lane:        lane,
				State:       proculaItemState(j),
				Progress:    j.Progress,
				RetryCount:  j.RetryCount,
				Error:       j.Error,
				MissingSubs: j.MissingSubs,
				Source: PipelineItemSource{
					JobID:   j.ID,
					ArrType: j.Source.ArrType,
				},
				Actions:   actionsForLane(lane),
				UpdatedAt: j.UpdatedAt,
			}

			// Include qBt hash on the card when available
			if h := strings.ToLower(j.Source.DownloadHash); h != "" {
				item.Source.QbtHash = j.Source.DownloadHash
				// Supplement with seeding speed if the torrent is still active
				if t, ok := qbtByHash[h]; ok {
					item.SpeedUp = t.UPSpeed
				}
			}

			// Build detail string (shown below the title in the card)
			switch lane {
			case "processing":
				if j.TranscodeProfile != "" {
					item.Detail = j.TranscodeProfile
					if j.TranscodeDecision != "" {
						item.Detail += " · " + j.TranscodeDecision
					}
				}
				if j.TranscodeETA > 0 {
					item.ETASecs = int64(j.TranscodeETA)
				}
			}

			// Attach validation check results for failed items
			if lane == "needs_attention" && j.Validation != nil {
				item.Checks = &ValidationChecksMin{
					Integrity: j.Validation.Checks.Integrity,
					Duration:  j.Validation.Checks.Duration,
					Sample:    j.Validation.Checks.Sample,
				}
			}

			lanes[lane] = append(lanes[lane], item)

			switch lane {
			case "validating", "processing", "cataloging":
				stats.Active++
			case "needs_attention":
				stats.Failed++
			}
		}
	}

	// Add grabbed requests to the monitoring lane.
	// Items naturally move out of monitoring once qBt picks them up —
	// they'll appear in the downloading lane (matched by arr_id) instead.
	lanes["monitoring"] = append(lanes["monitoring"], monitoringRes.items...)
	stats.Active += len(monitoringRes.items)

	// Sort completed lane newest-first, then trim to 20.
	sort.Slice(lanes["completed"], func(i, j int) bool {
		return lanes["completed"][i].UpdatedAt.After(lanes["completed"][j].UpdatedAt)
	})
	if len(lanes["completed"]) > 20 {
		lanes["completed"] = lanes["completed"][:20]
	}

	writeJSON(w, PipelineResponse{
		Lanes:       lanes,
		Stats:       stats,
		GeneratedAt: time.Now().UTC(),
	})
}

// handlePipelineDismiss records a failed job as dismissed so it no longer
// appears in the needs_attention lane.
func handlePipelineDismiss(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10) // 1 KB
	var req struct {
		JobID string `json:"job_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.JobID) == "" {
		writeError(w, "invalid request: job_id required", http.StatusBadRequest)
		return
	}
	if err := dismissedStore.Dismiss(req.JobID); err != nil {
		slog.Error("failed to persist dismissed job", "component", "pipeline", "job_id", req.JobID, "error", err)
		writeError(w, "failed to persist", http.StatusInternalServerError)
		return
	}
	slog.Info("dismissed job", "component", "pipeline", "job_id", req.JobID)
	writeJSON(w, map[string]string{"status": "dismissed"})
}

// ── Classification helpers ────────────────────────────────────────────────────

// qbtLaneFor maps a qBittorrent torrent state to a pipeline lane name.
// Returns "" for states that should be hidden (e.g. errored/completed inside qBt).
func qbtLaneFor(state string) string {
	switch state {
	case "downloading", "stalledDL", "forcedDL", "queuedDL",
		"pausedDL", "stoppedDL", "checkingDL", "metaDL":
		return "downloading"
	case "uploading", "stalledUP", "forcedUP", "queuedUP",
		"pausedUP", "stoppedUP", "checkingUP":
		return "imported"
	}
	return ""
}

// qbtItemState maps a qBittorrent state to the card's State field.
func qbtItemState(state string) string {
	switch state {
	case "pausedDL", "stoppedDL", "pausedUP", "stoppedUP":
		return "paused"
	}
	return "active"
}

// proculaLaneFor maps a Procula job to a pipeline lane name.
// Returns "" for items that should not appear (cancelled, dismissed, etc.).
func proculaLaneFor(j *ProculaJobMin, ds *DismissedStore) string {
	switch j.State {
	case "failed":
		if ds != nil && ds.IsDismissed(j.ID) {
			return ""
		}
		return "needs_attention"
	case "completed":
		return "completed"
	case "queued", "processing":
		switch j.Stage {
		case "validate":
			return "validating"
		case "process":
			return "processing"
		case "catalog", "done":
			return "cataloging"
		default:
			return "validating"
		}
	}
	return "" // cancelled, unknown
}

// proculaItemState maps a Procula job state to the card's State field.
func proculaItemState(j *ProculaJobMin) string {
	switch j.State {
	case "failed":
		return "failed"
	case "completed":
		return "done"
	}
	return "active"
}

// actionsForLane returns the set of action names available for a card in the
// given lane. The dashboard JS maps these to button renders.
func actionsForLane(lane string) []string {
	switch lane {
	case "downloading":
		return []string{"pause", "cancel", "blocklist"}
	case "imported":
		return []string{"cancel", "blocklist"}
	case "validating", "processing", "cataloging":
		return []string{"cancel_job", "view_log"}
	case "needs_attention":
		return []string{"retry", "cancel_job", "view_log", "dismiss"}
	case "completed":
		return []string{"view_log"}
	case "monitoring":
		return []string{}
	}
	return nil
}

// normKey produces a normalised lookup key for a title+year pair.
// Used by tests; production code only joins by download hash.
func normKey(title string, year int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	if year > 0 {
		b.WriteString(strconv.Itoa(year))
	}
	return b.String()
}
