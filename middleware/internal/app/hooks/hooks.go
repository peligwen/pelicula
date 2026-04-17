// Package hooks implements the *arr webhook receiver and Procula proxy handlers.
//
// Peligrosa trust boundary: webhook secret + path allowlist.
// The /hooks/import endpoint is also restricted to Docker-internal networks in nginx.conf.
package hooks

import (
	"bytes"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"pelicula-api/httputil"
	"pelicula-api/internal/app/catalog"
)

// HTTPDoer is the subset of *http.Client needed for outbound requests.
type HTTPDoer interface {
	Get(url string) (*http.Response, error)
	Do(req *http.Request) (*http.Response, error)
	Post(url, contentType string, body io.Reader) (*http.Response, error)
}

// ArrClient is the subset of ServiceClients needed for hooks.
type ArrClient interface {
	Keys() (sonarr, radarr, prowlarr string)
	ArrGet(baseURL, apiKey, path string) ([]byte, error)
	QbtPost(path, form string) error
}

// RequestMarker is the subset of peligrosa.RequestStore needed for hooks.
type RequestMarker interface {
	MarkAvailable(reqType string, tmdbID, tvdbID int, title string, notify func(string, string) error) error
}

// NotifyFunc is the function signature for the Apprise notifier.
type NotifyFunc func(title, body string) error

// Handler holds all dependencies needed by the hooks handlers.
type Handler struct {
	Svc        ArrClient
	HTTPClient HTTPDoer
	CatalogDB  *sql.DB
	ReqStore   RequestMarker
	Notify     NotifyFunc
	ProculaURL string
	SonarrURL  string
	RadarrURL  string
}

// ProculaJobSource mirrors procula's JobSource for the HTTP forwarding call.
type ProculaJobSource = catalog.ProculaJobSource

// HandleImportHook receives *arr import webhooks, normalizes the payload,
// and forwards a job to Procula.
func (h *Handler) HandleImportHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if secret := strings.TrimSpace(os.Getenv("WEBHOOK_SECRET")); secret != "" {
		provided := r.URL.Query().Get("secret")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) == 0 {
			httputil.WriteError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httputil.WriteError(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		httputil.WriteError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	eventType, _ := raw["eventType"].(string)
	if strings.EqualFold(eventType, "test") {
		httputil.WriteJSON(w, map[string]string{"status": "ok"})
		return
	}
	if !strings.EqualFold(eventType, "download") {
		slog.Info("ignoring webhook event", "component", "hooks", "event_type", eventType)
		httputil.WriteJSON(w, map[string]string{"status": "ignored"})
		return
	}

	source, err := normalizeHookPayload(raw)
	if err != nil {
		slog.Error("failed to normalize webhook", "component", "hooks", "error", err)
		httputil.WriteError(w, "invalid webhook payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	slog.Info("import webhook received", "component", "hooks",
		"arr_type", source.ArrType, "title", source.Title,
		"type", source.Type, "path", source.Path, "episode_id", source.EpisodeID)

	jobsURL := h.ProculaURL + "/api/procula/jobs"
	if err := h.forwardToProcula(jobsURL, source); err != nil {
		slog.Error("failed to forward to Procula", "component", "hooks", "error", err)
		httputil.WriteJSON(w, map[string]string{"status": "queued", "warning": err.Error()})
		return
	}

	if h.CatalogDB != nil {
		go func() {
			if err := catalog.UpsertFromHook(h.CatalogDB, source); err != nil {
				slog.Error("catalog upsert from hook failed", "component", "hooks", "error", err)
			}
		}()
	}

	if h.ReqStore != nil {
		reqType := source.Type
		if reqType == "episode" {
			reqType = "series"
		}
		go h.ReqStore.MarkAvailable(reqType, source.TmdbID, source.TvdbID, source.Title, h.Notify) //nolint:errcheck
	}

	if os.Getenv("SEEDING_REMOVE_ON_COMPLETE") == "true" && source.DownloadHash != "" {
		if err := h.Svc.QbtPost("/api/v2/torrents/delete",
			"hashes="+url.QueryEscape(source.DownloadHash)+"&deleteFiles=false"); err != nil {
			slog.Warn("remove-on-complete: failed to delete torrent", "component", "hooks",
				"hash", shortHash(source.DownloadHash), "error", err)
		} else {
			slog.Info("remove-on-complete: torrent removed", "component", "hooks",
				"hash", shortHash(source.DownloadHash))
		}
	}

	httputil.WriteJSON(w, map[string]string{"status": "queued"})
}

// normalizeHookPayload converts a Radarr or Sonarr webhook body into a ProculaJobSource.
func normalizeHookPayload(raw map[string]any) (source ProculaJobSource, err error) {
	downloadHash, _ := raw["downloadId"].(string)

	if movie, ok := raw["movie"].(map[string]any); ok {
		source.ArrType = "radarr"
		source.Type = "movie"
		source.Title, _ = movie["title"].(string)
		source.Year = int(floatVal(movie, "year"))
		source.ArrID = int(floatVal(movie, "id"))
		source.TmdbID = int(floatVal(movie, "tmdbId"))

		if mf, ok := raw["movieFile"].(map[string]any); ok {
			source.Path, _ = mf["path"].(string)
			source.Size = int64(floatVal(mf, "size"))
			if mi, ok := mf["mediaInfo"].(map[string]any); ok {
				secs := floatVal(mi, "runTimeSeconds")
				source.ExpectedRuntimeMinutes = int(secs / 60)
			}
		}
	} else if series, ok := raw["series"].(map[string]any); ok {
		source.ArrType = "sonarr"
		source.Type = "episode"
		source.Title, _ = series["title"].(string)
		source.Year = int(floatVal(series, "year"))
		source.ArrID = int(floatVal(series, "id"))
		source.TvdbID = int(floatVal(series, "tvdbId"))
		source.TmdbID = int(floatVal(series, "tmdbId"))

		if eps, ok := raw["episodes"].([]any); ok && len(eps) > 0 {
			if ep, ok := eps[0].(map[string]any); ok {
				source.EpisodeID = int(floatVal(ep, "id"))
				source.SeasonNumber = int(floatVal(ep, "seasonNumber"))
				source.EpisodeNumber = int(floatVal(ep, "episodeNumber"))
			}
		}

		if ef, ok := raw["episodeFile"].(map[string]any); ok {
			source.Path, _ = ef["path"].(string)
			source.Size = int64(floatVal(ef, "size"))
			if mi, ok := ef["mediaInfo"].(map[string]any); ok {
				secs := floatVal(mi, "runTimeSeconds")
				source.ExpectedRuntimeMinutes = int(secs / 60)
			}
		}
	} else {
		return source, fmt.Errorf("unrecognized payload: no 'movie' or 'series' key")
	}

	if source.Path == "" {
		return source, fmt.Errorf("no file path in webhook payload")
	}
	if !isAllowedWebhookPath(source.Path) {
		return source, fmt.Errorf("path not under an allowed media directory: %s", source.Path)
	}

	source.DownloadHash = downloadHash
	return source, nil
}

func (h *Handler) forwardToProcula(proculaURL string, source ProculaJobSource) error {
	data, err := json.Marshal(source)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, proculaURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(os.Getenv("PROCULA_API_KEY")); key != "" {
		req.Header.Set("X-API-Key", key)
	}
	resp, err := h.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("reach procula: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("procula HTTP %d", resp.StatusCode)
	}
	return nil
}

// HandleProcessingProxy proxies Procula's status + jobs for the dashboard Processing section.
func (h *Handler) HandleProcessingProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	type result struct {
		body       []byte
		statusCode int
		err        error
	}
	statusCh := make(chan result, 1)
	jobsCh := make(chan result, 1)

	go func() {
		resp, err := h.HTTPClient.Get(h.ProculaURL + "/api/procula/status")
		if err != nil {
			statusCh <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		statusCh <- result{body: b, statusCode: resp.StatusCode}
	}()
	go func() {
		resp, err := h.HTTPClient.Get(h.ProculaURL + "/api/procula/jobs")
		if err != nil {
			jobsCh <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		jobsCh <- result{body: b, statusCode: resp.StatusCode}
	}()

	statusRes := <-statusCh
	jobsRes := <-jobsCh

	if statusRes.err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"error":     "processing service unavailable",
			"retryable": true,
		})
		return
	}

	if statusRes.statusCode >= 300 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusRes.statusCode)
		w.Write(statusRes.body) //nolint:errcheck
		return
	}

	var statusData, jobsData any
	json.Unmarshal(statusRes.body, &statusData) //nolint:errcheck
	if jobsRes.err == nil && jobsRes.statusCode < 300 {
		json.Unmarshal(jobsRes.body, &jobsData) //nolint:errcheck
	}

	httputil.WriteJSON(w, map[string]any{
		"status": statusData,
		"jobs":   jobsData,
	})
}

// HandleNotificationsProxy merges Procula's notification feed with *arr history.
func (h *Handler) HandleNotificationsProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	all := []dashNotif{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := h.HTTPClient.Get(h.ProculaURL + "/api/procula/notifications")
		if err != nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				resp.Body.Close()
			}
			return
		}
		defer resp.Body.Close()
		var events []struct {
			ID        string    `json:"id"`
			Timestamp time.Time `json:"timestamp"`
			Type      string    `json:"type"`
			Message   string    `json:"message"`
			Detail    string    `json:"detail"`
			JobID     string    `json:"job_id"`
		}
		if json.NewDecoder(resp.Body).Decode(&events) == nil {
			mu.Lock()
			for _, e := range events {
				all = append(all, dashNotif{
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

	sonarrKey, radarrKey, _ := h.Svc.Keys()
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

// ProxyProcula returns an http.HandlerFunc that forwards a GET to the given Procula path.
func (h *Handler) ProxyProcula(path string, forwardQuery ...bool) http.HandlerFunc {
	fwd := len(forwardQuery) > 0 && forwardQuery[0]
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		target := h.ProculaURL + path
		if fwd {
			if q := r.URL.RawQuery; q != "" {
				target += "?" + q
			}
		}
		resp, err := h.HTTPClient.Get(target)
		if err != nil {
			httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		if _, err := io.Copy(w, resp.Body); err != nil {
			slog.Warn("failed to stream proxy response", "component", "proxy", "path", path, "error", err)
		}
	}
}

// ProxyProculaMutate returns an http.HandlerFunc that forwards the request to Procula.
func (h *Handler) ProxyProculaMutate(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body io.Reader
		if r.Body != nil {
			body = r.Body
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, h.ProculaURL+path, body)
		if err != nil {
			httputil.WriteError(w, "proxy error", http.StatusInternalServerError)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		if key := strings.TrimSpace(os.Getenv("PROCULA_API_KEY")); key != "" {
			req.Header.Set("X-API-Key", key)
		}
		resp, err := h.HTTPClient.Do(req)
		if err != nil {
			httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		if _, err := io.Copy(w, resp.Body); err != nil {
			slog.Warn("failed to stream proxy response", "component", "proxy", "path", path, "error", err)
		}
	}
}

// ProculaSettingsProxy handles GET/POST to Procula's settings endpoint.
func (h *Handler) ProculaSettingsProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.ProxyProcula("/api/procula/settings")(w, r)
		return
	}
	h.ProxyProculaMutate("/api/procula/settings")(w, r)
}

// dashNotif is the shape the dashboard notification panel expects.
type dashNotif struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	Detail    string    `json:"detail,omitempty"`
	JobID     string    `json:"job_id,omitempty"`
}

func (h *Handler) fetchArrHistory(baseURL, apiKey, arrType string) []dashNotif {
	data, err := h.Svc.ArrGet(baseURL, apiKey, "/api/v3/history?pageSize=20&sortKey=date&sortDir=desc")
	if err != nil {
		return nil
	}
	var resp struct {
		Records []map[string]any `json:"records"`
	}
	if json.Unmarshal(data, &resp) != nil {
		return nil
	}

	var notifs []dashNotif
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
		notifs = append(notifs, dashNotif{ID: id, Timestamp: ts, Type: nType, Message: msg, Detail: detail})
	}
	return notifs
}

// IsUnderPrefixes reports whether the cleaned path equals or is nested under
// one of the given prefixes. Exported for use by the library package.
func IsUnderPrefixes(p string, prefixes []string) bool {
	clean := filepath.Clean(p)
	for _, prefix := range prefixes {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}

// isAllowedWebhookPath checks that the path from a webhook payload is under a
// known media directory, preventing path traversal.
func isAllowedWebhookPath(p string) bool {
	if IsUnderPrefixes(p, []string{"/downloads", "/processing"}) {
		return true
	}
	clean := filepath.Clean(p)
	return clean == "/media" || strings.HasPrefix(clean, "/media/")
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

func parseArrDate(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, _ = time.Parse("2006-01-02T15:04:05Z", s)
	}
	return t
}

func strVal(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func floatVal(m map[string]any, key string) float64 {
	if v, ok := m[key].(float64); ok {
		return v
	}
	return 0
}

func shortHash(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}
