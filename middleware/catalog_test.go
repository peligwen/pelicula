package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	radarrURL, sonarrURL = radarr.URL, sonarr.URL
	services = &ServiceClients{RadarrKey: "k", SonarrKey: "k"}
	services.client = &http.Client{}
	t.Cleanup(func() { radarrURL, sonarrURL = origR, origS })

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
	sonarrURL = sonarr.URL
	services = &ServiceClients{SonarrKey: "k"}
	services.client = &http.Client{}
	t.Cleanup(func() { sonarrURL = origS })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/catalog/series/5/season/1", nil)
	req.SetPathValue("id", "5")
	req.SetPathValue("n", "1")
	w := httptest.NewRecorder()
	handleCatalogSeason(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var eps []map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &eps)
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
	proculaURL = procula.URL
	services = &ServiceClients{}
	services.client = &http.Client{}
	registryCache = actionRegistryCache{}
	t.Cleanup(func() { proculaURL = origP })

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
