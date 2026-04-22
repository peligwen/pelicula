package catalog_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pelicula-api/internal/app/catalog"
)

// stubArrClient implements catalog.ArrClient for tests.
type stubArrClient struct {
	radarrKey string
	sonarrKey string
	doGet     func(baseURL, apiKey, path string) ([]byte, error)
	doPost    func(baseURL, apiKey, path string, payload any) ([]byte, error)
	doPut     func(baseURL, apiKey, path string, payload any) ([]byte, error)
	doDelete  func(baseURL, apiKey, path string) ([]byte, error)
}

func (s *stubArrClient) Keys() (sonarr, radarr, prowlarr string) {
	return s.sonarrKey, s.radarrKey, ""
}
func (s *stubArrClient) ArrGet(baseURL, apiKey, path string) ([]byte, error) {
	if s.doGet != nil {
		return s.doGet(baseURL, apiKey, path)
	}
	return nil, nil
}
func (s *stubArrClient) ArrPost(baseURL, apiKey, path string, payload any) ([]byte, error) {
	if s.doPost != nil {
		return s.doPost(baseURL, apiKey, path, payload)
	}
	return nil, nil
}
func (s *stubArrClient) ArrPut(baseURL, apiKey, path string, payload any) ([]byte, error) {
	if s.doPut != nil {
		return s.doPut(baseURL, apiKey, path, payload)
	}
	return nil, nil
}
func (s *stubArrClient) ArrDelete(baseURL, apiKey, path string) ([]byte, error) {
	if s.doDelete != nil {
		return s.doDelete(baseURL, apiKey, path)
	}
	return nil, nil
}
func (s *stubArrClient) ArrGetAllQueueRecords(baseURL, apiKey, apiVer, extraParams string) ([]map[string]any, error) {
	return nil, nil
}

// newTestHandler builds a catalog.Handler backed by real httptest servers.
func newTestHandler(radarrSrv, sonarrSrv, proculaSrv *httptest.Server, radarrKey, sonarrKey string) *catalog.Handler {
	arr := &stubArrClient{radarrKey: radarrKey, sonarrKey: sonarrKey}
	arr.doGet = func(baseURL, apiKey, path string) ([]byte, error) {
		resp, err := http.Get(baseURL + path)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		buf := make([]byte, 0, 256)
		tmp := make([]byte, 256)
		for {
			n, rerr := resp.Body.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if rerr != nil {
				break
			}
		}
		return buf, nil
	}
	arr.doPost = func(baseURL, apiKey, path string, payload any) ([]byte, error) {
		data, _ := json.Marshal(payload)
		resp, err := http.Post(baseURL+path, "application/json", strings.NewReader(string(data)))
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		buf := make([]byte, 0, 256)
		tmp := make([]byte, 256)
		for {
			n, rerr := resp.Body.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if rerr != nil {
				break
			}
		}
		return buf, nil
	}
	arr.doPut = func(baseURL, apiKey, path string, payload any) ([]byte, error) {
		data, _ := json.Marshal(payload)
		req, _ := http.NewRequest(http.MethodPut, baseURL+path, strings.NewReader(string(data)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		buf := make([]byte, 0, 256)
		tmp := make([]byte, 256)
		for {
			n, rerr := resp.Body.Read(tmp)
			buf = append(buf, tmp[:n]...)
			if rerr != nil {
				break
			}
		}
		return buf, nil
	}

	radarrURL := ""
	if radarrSrv != nil {
		radarrURL = radarrSrv.URL
	}
	sonarrURL := ""
	if sonarrSrv != nil {
		sonarrURL = sonarrSrv.URL
	}
	proculaURL := ""
	if proculaSrv != nil {
		proculaURL = proculaSrv.URL
	}

	return &catalog.Handler{
		Arr:        arr,
		Client:     http.DefaultClient,
		RadarrURL:  radarrURL,
		SonarrURL:  sonarrURL,
		ProculaURL: proculaURL,
	}
}

func TestHandleCatalogListFansOut(t *testing.T) {
	t.Parallel()

	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/movie" {
			t.Errorf("radarr path = %q", r.URL.Path)
		}
		w.Write([]byte(`[{"id":1,"title":"Foo","year":2024},{"id":2,"title":"Bar","year":2023}]`)) //nolint:errcheck
	}))
	defer radarr.Close()

	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":10,"title":"Show","year":2020}]`)) //nolint:errcheck
	}))
	defer sonarr.Close()

	h := newTestHandler(radarr, sonarr, nil, "rk", "sk")

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/catalog", nil)
	w := httptest.NewRecorder()
	h.HandleCatalogList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Movies []map[string]any `json:"movies"`
		Series []map[string]any `json:"series"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Movies) != 2 {
		t.Errorf("movies = %d, want 2", len(resp.Movies))
	}
	if len(resp.Series) != 1 {
		t.Errorf("series = %d, want 1", len(resp.Series))
	}
}

func TestHandleCatalogSeasonMergesFiles(t *testing.T) {
	t.Parallel()

	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/episode":
			w.Write([]byte(`[{"id":1,"episodeFileId":100,"title":"Ep 1"},{"id":2,"episodeFileId":0,"title":"Ep 2"}]`)) //nolint:errcheck
		case r.URL.Path == "/api/v3/episodefile":
			w.Write([]byte(`[{"id":100,"path":"/tv/Show/S01/ep1.mkv"}]`)) //nolint:errcheck
		}
	}))
	defer sonarr.Close()

	h := newTestHandler(nil, sonarr, nil, "", "sk")

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/catalog/series/5/season/1", nil)
	req.SetPathValue("id", "5")
	req.SetPathValue("n", "1")
	w := httptest.NewRecorder()
	h.HandleCatalogSeason(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var eps []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &eps); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(eps) != 2 {
		t.Fatalf("eps = %d, want 2", len(eps))
	}
	if eps[0]["file"] == nil {
		t.Errorf("ep1 missing file merge")
	}
	if eps[1]["file"] != nil {
		t.Errorf("ep2 should not have file")
	}
}

func TestHandleCatalogFlagsProxies(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/procula/catalog/flags" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"rows":[{"path":"/movies/A/A.mkv","severity":"error","flags":[{"code":"validation_failed","severity":"error"}],"job_id":"job_a","updated_at":"2026-04-11T00:00:00Z"}]}`)) //nolint:errcheck
	}))
	defer upstream.Close()

	h := newTestHandler(nil, nil, upstream, "", "")

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/catalog/flags", nil)
	w := httptest.NewRecorder()
	h.HandleCatalogFlags(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"validation_failed"`) {
		t.Fatalf("body missing flag code: %s", w.Body.String())
	}
}

func TestHandleCatalogDetailMergesFlagsAndJob(t *testing.T) {
	t.Parallel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/procula/catalog/flags":
			w.Write([]byte(`{"rows":[{"path":"/movies/A/A.mkv","severity":"warn","flags":[{"code":"missing_subtitles","severity":"warn","fields":{"langs":["es"]}}],"job_id":"job_a","updated_at":"2026-04-11T00:00:00Z"}]}`)) //nolint:errcheck
		case "/api/procula/jobs":
			w.Write([]byte(`[{"id":"job_a","state":"completed","stage":"done","source":{"path":"/movies/A/A.mkv","title":"A"},"validation":{"passed":true,"checks":{"integrity":"pass","duration":"pass","sample":"pass","codecs":{"video":"h264","audio":"aac","subtitles":["eng"],"width":1920,"height":1080}}}}]`)) //nolint:errcheck
		}
	}))
	defer upstream.Close()

	h := newTestHandler(nil, nil, upstream, "", "")

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/catalog/detail?path=%2Fmovies%2FA%2FA.mkv", nil)
	w := httptest.NewRecorder()
	h.HandleCatalogDetail(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Path  string           `json:"path"`
		Flags []map[string]any `json:"flags"`
		Job   map[string]any   `json:"job"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Flags) != 1 || resp.Flags[0]["code"] != "missing_subtitles" {
		t.Fatalf("flags = %+v", resp.Flags)
	}
	if resp.Job == nil || resp.Job["id"] != "job_a" {
		t.Fatalf("job = %+v", resp.Job)
	}
}

func TestHandleCatalogCommandSearch_Radarr(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/command" && r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			w.Write([]byte(`{"id":1}`)) //nolint:errcheck
		}
	}))
	defer radarr.Close()

	h := newTestHandler(radarr, nil, nil, "rk", "")

	body := `{"arr_type":"radarr","arr_id":42,"command":"search"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/catalog/command", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCatalogCommand(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	if gotBody["name"] != "MoviesSearch" {
		t.Errorf("command name = %v, want MoviesSearch", gotBody["name"])
	}
	ids, _ := gotBody["movieIds"].([]any)
	if len(ids) != 1 || ids[0].(float64) != 42 {
		t.Errorf("movieIds = %v, want [42]", gotBody["movieIds"])
	}
}

func TestHandleCatalogCommandSearch_Sonarr(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/command" && r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			w.Write([]byte(`{"id":1}`)) //nolint:errcheck
		}
	}))
	defer sonarr.Close()

	h := newTestHandler(nil, sonarr, nil, "", "sk")

	body := `{"arr_type":"sonarr","arr_id":7,"command":"search"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/catalog/command", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCatalogCommand(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	if gotBody["name"] != "SeriesSearch" {
		t.Errorf("command name = %v, want SeriesSearch", gotBody["name"])
	}
	if gotBody["seriesId"].(float64) != 7 {
		t.Errorf("seriesId = %v, want 7", gotBody["seriesId"])
	}
}

func TestHandleCatalogCommandUnmonitor_Radarr(t *testing.T) {
	t.Parallel()

	var putBody map[string]any
	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/movie/42" && r.Method == http.MethodGet:
			w.Write([]byte(`{"id":42,"title":"Foo","monitored":true}`)) //nolint:errcheck
		case r.URL.Path == "/api/v3/movie/42" && r.Method == http.MethodPut:
			if err := json.NewDecoder(r.Body).Decode(&putBody); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			w.Write([]byte(`{"id":42}`)) //nolint:errcheck
		}
	}))
	defer radarr.Close()

	h := newTestHandler(radarr, nil, nil, "rk", "")

	body := `{"arr_type":"radarr","arr_id":42,"command":"unmonitor"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/catalog/command", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCatalogCommand(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	if monitored, _ := putBody["monitored"].(bool); monitored {
		t.Errorf("monitored = true after unmonitor, want false")
	}
}

func TestHandleCatalogReplaceValidation(t *testing.T) {
	t.Parallel()

	h := &catalog.Handler{
		Arr:    &stubArrClient{},
		Client: http.DefaultClient,
	}

	// Missing arr_type → 400
	body := `{"arr_id":1,"episode_id":2,"path":"/tv/Silo/S01E01.mkv"}`
	req := httptest.NewRequest("POST", "/api/pelicula/catalog/replace", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCatalogReplace(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing arr_type: want 400, got %d", w.Code)
	}

	// Missing arr_id → 400
	body = `{"arr_type":"sonarr","episode_id":2,"path":"/tv/Silo/S01E01.mkv"}`
	req = httptest.NewRequest("POST", "/api/pelicula/catalog/replace", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	h.HandleCatalogReplace(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing arr_id: want 400, got %d", w.Code)
	}
}

// TestHandleCatalogReplace_NoHistoryReturns409 verifies that HandleCatalogReplace
// returns HTTP 409 with {"error":"no import history found"} when findImportHistoryID
// returns zero (no downloadFolderImported event exists for the item).
func TestHandleCatalogReplace_NoHistoryReturns409(t *testing.T) {
	t.Parallel()

	// Sonarr history returns an empty records list — no import history found.
	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v3/history/episode"):
			// Return empty records list — triggers the 409 path.
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"records":[]}`)) //nolint:errcheck
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer sonarr.Close()

	h := newTestHandler(nil, sonarr, nil, "", "sk")

	body := `{"arr_type":"sonarr","arr_id":10,"episode_id":5,"path":"/tv/show/S01E01.mkv"}`
	req := httptest.NewRequest("POST", "/api/pelicula/catalog/replace", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCatalogReplace(w, req)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d (Conflict); body: %s", w.Code, http.StatusConflict, w.Body.String())
	}
	// Response must be JSON with {"error":"no import history found"}.
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var resp map[string]string
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if resp["error"] != "no import history found" {
		t.Errorf("error = %q, want %q", resp["error"], "no import history found")
	}
}

func TestHandleCatalogCommandRescan(t *testing.T) {
	t.Parallel()

	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/command" && r.Method == http.MethodPost {
			w.Write([]byte(`{"id":1}`)) //nolint:errcheck
		}
	}))
	defer sonarr.Close()

	h := newTestHandler(nil, sonarr, nil, "", "sk")

	req := httptest.NewRequest("POST", "/api/pelicula/catalog/command",
		strings.NewReader(`{"arr_type":"sonarr","arr_id":1,"command":"rescan"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleCatalogCommand(w, req)
	// Should NOT return 400 "unknown command"
	if w.Code == http.StatusBadRequest {
		var errResp map[string]string
		json.NewDecoder(w.Body).Decode(&errResp) //nolint:errcheck
		if errResp["error"] == "unknown command" {
			t.Error("rescan command not recognised")
		}
	}
}

func TestHandleCatalogQualityProfiles(t *testing.T) {
	t.Parallel()

	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/qualityprofile" {
			w.Write([]byte(`[{"id":1,"name":"HD-1080p"},{"id":2,"name":"Any"}]`)) //nolint:errcheck
		}
	}))
	defer radarr.Close()

	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/qualityprofile" {
			w.Write([]byte(`[{"id":4,"name":"HD TV"}]`)) //nolint:errcheck
		}
	}))
	defer sonarr.Close()

	h := newTestHandler(radarr, sonarr, nil, "rk", "sk")

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/catalog/qualityprofiles", nil)
	w := httptest.NewRecorder()
	h.HandleCatalogQualityProfiles(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Radarr map[string]string `json:"radarr"`
		Sonarr map[string]string `json:"sonarr"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Radarr["1"] != "HD-1080p" {
		t.Errorf("radarr profile 1 = %q, want HD-1080p", resp.Radarr["1"])
	}
	if resp.Sonarr["4"] != "HD TV" {
		t.Errorf("sonarr profile 4 = %q, want HD TV", resp.Sonarr["4"])
	}
}
