// notif.go — notification aggregation from Procula and *arr history.
package hooks

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"pelicula-api/httputil"
)

// DashNotif is the shape the dashboard notification panel expects.
type DashNotif struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"` // "content_ready", "download_failed", "validation_failed", "transcode_failed"
	Message   string    `json:"message"`
	Detail    string    `json:"detail,omitempty"` // error text / release info for drawer
	JobID     string    `json:"job_id,omitempty"` // procula job ID; enables Retry action
}

// HandleNotificationsProxy merges Procula's notification feed with recent
// Sonarr and Radarr history events.
func (h *Handler) HandleNotificationsProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	all := []DashNotif{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	// ── Procula feed ──────────────────────────────────────────────────────────
	wg.Add(1)
	go func() {
		defer wg.Done()
		raw, err := h.Procula.GetNotifications(r.Context())
		if err != nil {
			return
		}
		var events []struct {
			ID        string    `json:"id"`
			Timestamp time.Time `json:"timestamp"`
			Type      string    `json:"type"`
			Message   string    `json:"message"`
			Detail    string    `json:"detail"`
			JobID     string    `json:"job_id"`
		}
		if json.Unmarshal(raw, &events) == nil {
			mu.Lock()
			for _, e := range events {
				all = append(all, DashNotif{
					ID:        e.ID,
					Timestamp: e.Timestamp,
					Type:      e.Type,
					Message:   e.Message,
					Detail:    e.Detail,
					JobID:     e.JobID,
				})
			}
			mu.Unlock()
		}
	}()

	// ── Sonarr history ────────────────────────────────────────────────────────
	sonarrKey, radarrKey, _ := h.GetKeys()
	if sonarrKey != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			notifs := h.fetchArrHistory(h.SonarrURL, sonarrKey, "sonarr")
			mu.Lock()
			all = append(all, notifs...)
			mu.Unlock()
		}()
	}

	// ── Radarr history ────────────────────────────────────────────────────────
	if radarrKey != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			notifs := h.fetchArrHistory(h.RadarrURL, radarrKey, "radarr")
			mu.Lock()
			all = append(all, notifs...)
			mu.Unlock()
		}()
	}

	wg.Wait()

	// Deduplicate by ID, sort newest-first, cap at 30.
	seen := make(map[string]bool, len(all))
	deduped := all[:0]
	for _, n := range all {
		if !seen[n.ID] {
			seen[n.ID] = true
			deduped = append(deduped, n)
		}
	}
	sort.Slice(deduped, func(i, j int) bool {
		return deduped[i].Timestamp.After(deduped[j].Timestamp)
	})
	if len(deduped) > 30 {
		deduped = deduped[:30]
	}

	httputil.WriteJSON(w, deduped)
}

// fetchArrHistory fetches the last 20 history records from a Sonarr or Radarr
// instance and maps import/failure events into dashboard notifications.
func (h *Handler) fetchArrHistory(baseURL, apiKey, arrType string) []DashNotif {
	data, err := h.ArrGet(baseURL, apiKey, "/api/v3/history?pageSize=20&sortKey=date&sortDir=desc")
	if err != nil {
		slog.Warn("fetchArrHistory: request failed", "component", "hooks", "arr_type", arrType, "error", err)
		return nil
	}
	var resp struct {
		Records []map[string]any `json:"records"`
	}
	if json.Unmarshal(data, &resp) != nil {
		return nil
	}

	var notifs []DashNotif
	for _, rec := range resp.Records {
		eventType, _ := rec["eventType"].(string)
		var nType, msg string
		switch eventType {
		case "downloadFolderImported":
			nType = "content_ready"
			msg = arrImportMessage(rec, arrType)
		case "downloadFailed":
			nType = "download_failed"
			msg = arrFailedMessage(rec, arrType)
		default:
			continue
		}
		detail := strVal(rec, "sourceTitle")
		if nType == "download_failed" {
			if data, ok := rec["data"].(map[string]any); ok {
				if reason := strVal(data, "reason"); reason != "" {
					detail += " · " + reason
				}
			}
		}
		id := fmt.Sprintf("%s:%v", arrType, rec["id"])
		ts := parseArrDate(strVal(rec, "date"))
		notifs = append(notifs, DashNotif{ID: id, Timestamp: ts, Type: nType, Message: msg, Detail: detail})
	}
	return notifs
}

func arrImportMessage(rec map[string]any, arrType string) string {
	if arrType == "radarr" {
		if movie, ok := rec["movie"].(map[string]any); ok {
			title := strVal(movie, "title")
			year := int(floatVal(movie, "year"))
			if year > 0 {
				return fmt.Sprintf("Movie ready: %s (%d)", title, year)
			}
			return "Movie ready: " + title
		}
	}
	// Sonarr
	seriesTitle := ""
	if series, ok := rec["series"].(map[string]any); ok {
		seriesTitle = strVal(series, "title")
	}
	epTitle := ""
	if ep, ok := rec["episode"].(map[string]any); ok {
		s := int(floatVal(ep, "seasonNumber"))
		e := int(floatVal(ep, "episodeNumber"))
		if s > 0 || e > 0 {
			epTitle = fmt.Sprintf(" S%02dE%02d", s, e)
		}
	}
	return fmt.Sprintf("Episode ready: %s%s", seriesTitle, epTitle)
}

func arrFailedMessage(rec map[string]any, arrType string) string {
	title := ""
	if arrType == "radarr" {
		if movie, ok := rec["movie"].(map[string]any); ok {
			title = strVal(movie, "title")
		}
	} else {
		if series, ok := rec["series"].(map[string]any); ok {
			title = strVal(series, "title")
		}
	}
	if title == "" {
		return "Download failed"
	}
	return "Download failed: " + title
}
