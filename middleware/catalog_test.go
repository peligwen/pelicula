package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleCatalogListFansOut(t *testing.T) {
	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/movie" {
			t.Errorf("radarr path = %q", r.URL.Path)
		}
		w.Write([]byte(`[{"id":1,"title":"Foo","year":2024},{"id":2,"title":"Bar","year":2023}]`))
	}))
	defer radarr.Close()

	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":10,"title":"Show","year":2020}]`))
	}))
	defer sonarr.Close()

	origR, origS := radarrURL, sonarrURL
	origSvc := services
	radarrURL, sonarrURL = radarr.URL, sonarr.URL
	services = &ServiceClients{RadarrKey: "k", SonarrKey: "k"}
	services.client = &http.Client{}
	t.Cleanup(func() { radarrURL, sonarrURL = origR, origS; services = origSvc })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/catalog", nil)
	w := httptest.NewRecorder()
	handleCatalogList(w, req)

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
	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/episode":
			w.Write([]byte(`[{"id":1,"episodeFileId":100,"title":"Ep 1"},{"id":2,"episodeFileId":0,"title":"Ep 2"}]`))
		case r.URL.Path == "/api/v3/episodefile":
			w.Write([]byte(`[{"id":100,"path":"/tv/Show/S01/ep1.mkv"}]`))
		}
	}))
	defer sonarr.Close()

	origS := sonarrURL
	origSvc := services
	sonarrURL = sonarr.URL
	services = &ServiceClients{SonarrKey: "k"}
	services.client = &http.Client{}
	t.Cleanup(func() { sonarrURL = origS; services = origSvc })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/catalog/series/5/season/1", nil)
	req.SetPathValue("id", "5")
	req.SetPathValue("n", "1")
	w := httptest.NewRecorder()
	handleCatalogSeason(w, req)

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
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/procula/catalog/flags" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"rows":[{"path":"/movies/A/A.mkv","severity":"error","flags":[{"code":"validation_failed","severity":"error"}],"job_id":"job_a","updated_at":"2026-04-11T00:00:00Z"}]}`))
	}))
	defer upstream.Close()

	orig := proculaURL
	origSvc := services
	proculaURL = upstream.URL
	services = &ServiceClients{}
	services.client = &http.Client{}
	t.Cleanup(func() { proculaURL = orig; services = origSvc })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/catalog/flags", nil)
	w := httptest.NewRecorder()
	handleCatalogFlags(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"validation_failed"`) {
		t.Fatalf("body missing flag code: %s", w.Body.String())
	}
}

func TestHandleCatalogDetailMergesFlagsAndJob(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/procula/catalog/flags":
			w.Write([]byte(`{"rows":[{"path":"/movies/A/A.mkv","severity":"warn","flags":[{"code":"missing_subtitles","severity":"warn","fields":{"langs":["es"]}}],"job_id":"job_a","updated_at":"2026-04-11T00:00:00Z"}]}`))
		case "/api/procula/jobs":
			w.Write([]byte(`[{"id":"job_a","state":"completed","stage":"done","source":{"path":"/movies/A/A.mkv","title":"A"},"validation":{"passed":true,"checks":{"integrity":"pass","duration":"pass","sample":"pass","codecs":{"video":"h264","audio":"aac","subtitles":["eng"],"width":1920,"height":1080}}}}]`))
		}
	}))
	defer upstream.Close()

	orig := proculaURL
	origSvc := services
	proculaURL = upstream.URL
	services = &ServiceClients{}
	services.client = &http.Client{}
	t.Cleanup(func() { proculaURL = orig; services = origSvc })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/catalog/detail?path=%2Fmovies%2FA%2FA.mkv", nil)
	w := httptest.NewRecorder()
	handleCatalogDetail(w, req)

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
	var gotBody map[string]any
	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/command" && r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.Write([]byte(`{"id":1}`))
		}
	}))
	defer radarr.Close()

	origR := radarrURL
	origSvc := services
	radarrURL = radarr.URL
	services = &ServiceClients{RadarrKey: "k"}
	services.client = &http.Client{}
	t.Cleanup(func() { radarrURL = origR; services = origSvc })

	body := `{"arr_type":"radarr","arr_id":42,"command":"search"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/catalog/command", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleCatalogCommand(w, req)

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
	var gotBody map[string]any
	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/command" && r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.Write([]byte(`{"id":1}`))
		}
	}))
	defer sonarr.Close()

	origS := sonarrURL
	origSvc := services
	sonarrURL = sonarr.URL
	services = &ServiceClients{SonarrKey: "k"}
	services.client = &http.Client{}
	t.Cleanup(func() { sonarrURL = origS; services = origSvc })

	body := `{"arr_type":"sonarr","arr_id":7,"command":"search"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/catalog/command", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleCatalogCommand(w, req)

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
	var putBody map[string]any
	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/movie/42" && r.Method == http.MethodGet:
			w.Write([]byte(`{"id":42,"title":"Foo","monitored":true}`))
		case r.URL.Path == "/api/v3/movie/42" && r.Method == http.MethodPut:
			_ = json.NewDecoder(r.Body).Decode(&putBody)
			w.Write([]byte(`{"id":42}`))
		}
	}))
	defer radarr.Close()

	origR := radarrURL
	origSvc := services
	radarrURL = radarr.URL
	services = &ServiceClients{RadarrKey: "k"}
	services.client = &http.Client{}
	t.Cleanup(func() { radarrURL = origR; services = origSvc })

	body := `{"arr_type":"radarr","arr_id":42,"command":"unmonitor"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/catalog/command", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleCatalogCommand(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	if monitored, _ := putBody["monitored"].(bool); monitored {
		t.Errorf("monitored = true after unmonitor, want false")
	}
}

func TestHandleActionsRegistryCached(t *testing.T) {
	hits := 0
	procula := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/procula/actions/registry" {
			hits++
			w.Write([]byte(`[{"name":"validate","label":"Re-verify file"}]`))
		}
	}))
	defer procula.Close()

	origP := proculaURL
	origSvc := services
	proculaURL = procula.URL
	services = &ServiceClients{}
	services.client = &http.Client{}
	registryCache = actionRegistryCache{}
	t.Cleanup(func() { proculaURL = origP; services = origSvc })

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/pelicula/actions/registry", nil)
		w := httptest.NewRecorder()
		handleActionsRegistry(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d", w.Code)
		}
	}
	if hits != 1 {
		t.Errorf("procula hits = %d, want 1 (cache should serve the rest)", hits)
	}
}

func TestHandleCatalogQualityProfiles(t *testing.T) {
	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/qualityprofile" {
			w.Write([]byte(`[{"id":1,"name":"HD-1080p"},{"id":2,"name":"Any"}]`))
		}
	}))
	defer radarr.Close()

	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/qualityprofile" {
			w.Write([]byte(`[{"id":4,"name":"HD TV"}]`))
		}
	}))
	defer sonarr.Close()

	origR, origS := radarrURL, sonarrURL
	origSvc := services
	radarrURL, sonarrURL = radarr.URL, sonarr.URL
	services = &ServiceClients{RadarrKey: "k", SonarrKey: "k"}
	services.client = &http.Client{}
	t.Cleanup(func() { radarrURL, sonarrURL = origR, origS; services = origSvc })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/catalog/qualityprofiles", nil)
	w := httptest.NewRecorder()
	handleCatalogQualityProfiles(w, req)

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
