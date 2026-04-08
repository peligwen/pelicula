package main

import (
	"bytes"
	"crypto/subtle"
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
)

// handleImportHook receives *arr import webhooks, normalizes the payload,
// and forwards a job to Procula.
func handleImportHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Verify shared secret (passed as ?secret= query param by autowire).
	// If WEBHOOK_SECRET is unset the check is skipped (backward compat with
	// existing installs that haven't been re-run through setup/reset).
	if secret := strings.TrimSpace(os.Getenv("WEBHOOK_SECRET")); secret != "" {
		provided := r.URL.Query().Get("secret")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) == 0 {
			writeError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		writeError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	eventType, _ := raw["eventType"].(string)
	// Only process Download (import) events; silently accept test pings
	if strings.EqualFold(eventType, "test") {
		writeJSON(w, map[string]string{"status": "ok"})
		return
	}
	if !strings.EqualFold(eventType, "download") {
		slog.Info("ignoring webhook event", "component", "hooks", "event_type", eventType)
		writeJSON(w, map[string]string{"status": "ignored"})
		return
	}

	source, err := normalizeHookPayload(raw)
	if err != nil {
		slog.Error("failed to normalize webhook", "component", "hooks", "error", err)
		writeError(w, "invalid webhook payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	slog.Info("import webhook received", "component", "hooks", "arr_type", source.ArrType, "title", source.Title, "type", source.Type, "path", source.Path)

	// Forward to Procula
	proculaURL := proculaBaseURL() + "/api/procula/jobs"
	if err := forwardToProcula(proculaURL, source); err != nil {
		slog.Error("failed to forward to Procula", "component", "hooks", "error", err)
		// Don't fail the webhook — *arr doesn't retry sensibly on 5xx
		writeJSON(w, map[string]string{"status": "queued", "warning": err.Error()})
		return
	}

	// When SEEDING_REMOVE_ON_COMPLETE is set, delete the torrent from qBittorrent
	// immediately after *arr has imported (and hardlinked) the file. The file itself
	// is preserved; only the torrent entry is removed.
	if os.Getenv("SEEDING_REMOVE_ON_COMPLETE") == "true" && source.DownloadHash != "" {
		if err := services.QbtPost("/api/v2/torrents/delete",
			"hashes="+url.QueryEscape(source.DownloadHash)+"&deleteFiles=false"); err != nil {
			slog.Warn("remove-on-complete: failed to delete torrent", "component", "hooks",
				"hash", shortHash(source.DownloadHash), "error", err)
		} else {
			slog.Info("remove-on-complete: torrent removed", "component", "hooks",
				"hash", shortHash(source.DownloadHash))
		}
	}

	writeJSON(w, map[string]string{"status": "queued"})
}

// normalizeHookPayload converts a Radarr or Sonarr webhook body into a JobSource.
func normalizeHookPayload(raw map[string]any) (source ProculaJobSource, err error) {
	downloadHash, _ := raw["downloadId"].(string)

	// Detect *arr type by payload shape
	if movie, ok := raw["movie"].(map[string]any); ok {
		// Radarr
		source.ArrType = "radarr"
		source.Type = "movie"
		source.Title, _ = movie["title"].(string)
		source.Year = int(floatVal(movie, "year"))
		source.ArrID = int(floatVal(movie, "id"))

		if mf, ok := raw["movieFile"].(map[string]any); ok {
			source.Path, _ = mf["path"].(string)
			source.Size = int64(floatVal(mf, "size"))
			if mi, ok := mf["mediaInfo"].(map[string]any); ok {
				secs := floatVal(mi, "runTimeSeconds")
				source.ExpectedRuntimeMinutes = int(secs / 60)
			}
		}
	} else if series, ok := raw["series"].(map[string]any); ok {
		// Sonarr
		source.ArrType = "sonarr"
		source.Type = "episode"
		source.Title, _ = series["title"].(string)
		source.Year = int(floatVal(series, "year"))
		source.ArrID = int(floatVal(series, "id"))

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

// ProculaJobSource mirrors procula's JobSource for the HTTP call.
type ProculaJobSource struct {
	Type                   string `json:"type"`
	Title                  string `json:"title"`
	Year                   int    `json:"year"`
	Path                   string `json:"path"`
	Size                   int64  `json:"size"`
	ArrID                  int    `json:"arr_id"`
	ArrType                string `json:"arr_type"`
	DownloadHash           string `json:"download_hash"`
	ExpectedRuntimeMinutes int    `json:"expected_runtime_minutes"`
}

func forwardToProcula(url string, source ProculaJobSource) error {
	data, err := json.Marshal(source)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if key := strings.TrimSpace(os.Getenv("PROCULA_API_KEY")); key != "" {
		req.Header.Set("X-API-Key", key)
	}
	resp, err := services.client.Do(req)
	if err != nil {
		return fmt.Errorf("reach procula: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("procula HTTP %d", resp.StatusCode)
	}
	return nil
}

// handleProcessingProxy proxies Procula's status + jobs for the dashboard Processing section.
func handleProcessingProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	base := proculaBaseURL()

	// Fetch status and jobs in parallel
	type result struct {
		body []byte
		err  error
	}
	statusCh := make(chan result, 1)
	jobsCh := make(chan result, 1)

	go func() {
		resp, err := services.client.Get(base + "/api/procula/status")
		if err != nil {
			statusCh <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		statusCh <- result{body: b}
	}()
	go func() {
		resp, err := services.client.Get(base + "/api/procula/jobs")
		if err != nil {
			jobsCh <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		jobsCh <- result{body: b}
	}()

	statusRes := <-statusCh
	jobsRes := <-jobsCh

	if statusRes.err != nil {
		writeError(w, "procula unavailable", http.StatusBadGateway)
		return
	}

	// Merge into one response: {status: {...}, jobs: [...]}
	var statusData, jobsData any
	json.Unmarshal(statusRes.body, &statusData)
	if jobsRes.err == nil {
		json.Unmarshal(jobsRes.body, &jobsData)
	}

	writeJSON(w, map[string]any{
		"status": statusData,
		"jobs":   jobsData,
	})
}

// handleJellyfinRefresh triggers a Jellyfin library scan. Called by Procula (internal only).
func handleJellyfinRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Verify Procula API key so only Procula can trigger refreshes.
	if key := strings.TrimSpace(os.Getenv("PROCULA_API_KEY")); key != "" {
		provided := r.Header.Get("X-API-Key")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(key)) == 0 {
			writeError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	if err := TriggerLibraryRefresh(services); err != nil {
		slog.Error("library refresh failed", "component", "jellyfin", "error", err)
		writeError(w, "refresh failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// dashNotif is the shape the dashboard notification panel expects.
type dashNotif struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`    // "content_ready", "download_failed", "validation_failed"
	Message   string    `json:"message"`
}

// handleNotificationsProxy merges Procula's notification feed with recent
// Sonarr and Radarr history events so the bell shows useful activity even
// before Procula has processed anything.
func handleNotificationsProxy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	all := []dashNotif{}
	var mu sync.Mutex
	var wg sync.WaitGroup

	// ── Procula feed ──────────────────────────────────────────────────────────
	wg.Add(1)
	go func() {
		defer wg.Done()
		resp, err := services.client.Get(proculaBaseURL() + "/api/procula/notifications")
		if err != nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				resp.Body.Close()
			}
			return
		}
		defer resp.Body.Close()
		// Procula uses its own NotificationEvent struct; we only need the shared fields.
		var events []struct {
			ID        string    `json:"id"`
			Timestamp time.Time `json:"timestamp"`
			Type      string    `json:"type"`
			Message   string    `json:"message"`
		}
		if json.NewDecoder(resp.Body).Decode(&events) == nil {
			mu.Lock()
			for _, e := range events {
				all = append(all, dashNotif{ID: e.ID, Timestamp: e.Timestamp, Type: e.Type, Message: e.Message})
			}
			mu.Unlock()
		}
	}()

	// ── Sonarr history ────────────────────────────────────────────────────────
	sonarrKey, radarrKey, _ := services.Keys()
	if sonarrKey != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			notifs := fetchArrHistory(sonarrURL, sonarrKey, "sonarr")
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
			notifs := fetchArrHistory(radarrURL, radarrKey, "radarr")
			mu.Lock()
			all = append(all, notifs...)
			mu.Unlock()
		}()
	}

	wg.Wait()

	// Deduplicate by ID, sort newest-first, cap at 30
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

	writeJSON(w, deduped)
}

// fetchArrHistory fetches the last 20 history records from a Sonarr or Radarr
// instance and maps import/failure events into dashboard notifications.
func fetchArrHistory(baseURL, apiKey, arrType string) []dashNotif {
	data, err := services.ArrGet(baseURL, apiKey, "/api/v3/history?pageSize=20&sortKey=date&sortDir=desc")
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
		id := fmt.Sprintf("%s:%v", arrType, rec["id"])
		ts := parseArrDate(strVal(rec, "date"))
		notifs = append(notifs, dashNotif{ID: id, Timestamp: ts, Type: nType, Message: msg})
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

func parseArrDate(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, _ = time.Parse("2006-01-02T15:04:05Z", s)
	}
	return t
}

// isUnderPrefixes reports whether the cleaned path equals or is nested under
// one of the given prefixes. Used by both webhook and browse allowlist checks.
func isUnderPrefixes(p string, prefixes []string) bool {
	clean := filepath.Clean(p)
	for _, prefix := range prefixes {
		if clean == prefix || strings.HasPrefix(clean, prefix+"/") {
			return true
		}
	}
	return false
}

// isAllowedWebhookPath checks that the path from a webhook payload is under a
// known media directory, preventing path traversal to arbitrary filesystem locations.
func isAllowedWebhookPath(p string) bool {
	return isUnderPrefixes(p, []string{"/downloads", "/movies", "/tv", "/processing"})
}

// proxyProcula returns an http.HandlerFunc that forwards a GET to the given
// Procula path and streams the JSON response back. Used for simple dashboard
// proxy endpoints that need no request-side logic.
// When forwardQuery is true the incoming request's raw query string is appended.
func proxyProcula(path string, forwardQuery ...bool) http.HandlerFunc {
	fwd := len(forwardQuery) > 0 && forwardQuery[0]
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		target := proculaBaseURL() + path
		if fwd {
			if q := r.URL.RawQuery; q != "" {
				target += "?" + q
			}
		}
		resp, err := services.client.Get(target)
		if err != nil {
			writeError(w, "procula unavailable", http.StatusBadGateway)
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

// handleStorageProxy proxies Procula's storage report for the dashboard Storage section.
var handleStorageProxy = proxyProcula("/api/procula/storage")

// handleProculaSettingsProxy proxies GET/POST to Procula's settings endpoint.
var handleProculaSettingsProxy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		proxyProcula("/api/procula/settings")(w, r)
		return
	}
	proxyProculaMutate("/api/procula/settings")(w, r)
})

// handleStorageScanProxy proxies a POST scan trigger to Procula.
var handleStorageScanProxy = proxyProculaMutate("/api/procula/storage/scan")

// proxyProculaMutate returns an http.HandlerFunc that forwards the request
// (method, body, Content-Type) to the given Procula path, injecting
// X-API-Key if PROCULA_API_KEY is set. Used for admin mutating endpoints.
func proxyProculaMutate(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body io.Reader
		if r.Body != nil {
			body = r.Body
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, proculaBaseURL()+path, body)
		if err != nil {
			writeError(w, "proxy error", http.StatusInternalServerError)
			return
		}
		if ct := r.Header.Get("Content-Type"); ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		if key := strings.TrimSpace(os.Getenv("PROCULA_API_KEY")); key != "" {
			req.Header.Set("X-API-Key", key)
		}
		resp, err := services.client.Do(req)
		if err != nil {
			writeError(w, "procula unavailable", http.StatusBadGateway)
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

// handleUpdatesProxy proxies Procula's update check result for the dashboard footer.
var handleUpdatesProxy = proxyProcula("/api/procula/updates")

// handleEventsProxy proxies Procula's event log, forwarding pagination/filter query params.
var handleEventsProxy = proxyProcula("/api/procula/events", true)

func proculaBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("PROCULA_URL")); v != "" {
		return v
	}
	return "http://procula:8282"
}
