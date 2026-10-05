package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func results(t *testing.T, resp map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, r := range resp["results"].([]any) {
		out = append(out, r.(map[string]any))
	}
	return out
}

func TestSearchInterleavesAndNormalizes(t *testing.T) {
	e := newEnv(t)
	e.radarr.movieLookup = list(t,
		`{"title":"Dune","year":2021,"overview":"Sand.","tmdbId":438631,"remotePoster":"https://img/dune.jpg","id":0,"hasFile":false}`,
		`{"title":"Dune 2","year":2024,"tmdbId":693134,"images":[{"coverType":"fanart","remoteUrl":"https://img/fan.jpg"},{"coverType":"poster","remoteUrl":"https://img/dune2.jpg"}],"id":7,"hasFile":true}`,
		`{"title":"Dune 3","year":2026,"tmdbId":1,"id":0}`,
	)
	e.sonarr.seriesList = list(t,
		`{"title":"Dune: Prophecy","year":2024,"tvdbId":405000,"tmdbId":9,"overview":"Sisters.","remotePoster":"https://img/prophecy.jpg","id":3,"statistics":{"episodeFileCount":4}}`,
		`{"title":"Dune Docs","year":2000,"tvdbId":2,"id":0,"statistics":{"episodeFileCount":0}}`,
	)

	rec := e.do(viewer, "GET", "/api/search?q=dune", nil)
	wantStatus(t, rec, 200)
	res := results(t, decode(t, rec))

	var order []string
	for _, r := range res {
		order = append(order, r["type"].(string)+":"+r["title"].(string))
	}
	want := []string{"movie:Dune", "series:Dune: Prophecy", "movie:Dune 2", "series:Dune Docs", "movie:Dune 3"}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}

	m0 := res[0]
	if m0["year"] != float64(2021) || m0["overview"] != "Sand." || m0["poster"] != "https://img/dune.jpg" ||
		m0["tmdb_id"] != float64(438631) || m0["tvdb_id"] != float64(0) ||
		m0["in_library"] != false || m0["arr_id"] != float64(0) || m0["has_file"] != false {
		t.Errorf("movie 0 = %v", m0)
	}
	m1 := res[2]
	if m1["poster"] != "https://img/dune2.jpg" {
		t.Errorf("poster should come from the poster-type image, got %v", m1["poster"])
	}
	if m1["in_library"] != true || m1["arr_id"] != float64(7) || m1["has_file"] != true {
		t.Errorf("movie 1 = %v", m1)
	}
	s0, s1 := res[1], res[3]
	if s0["tvdb_id"] != float64(405000) || s0["in_library"] != true || s0["arr_id"] != float64(3) || s0["has_file"] != true {
		t.Errorf("series 0 = %v", s0)
	}
	if s1["in_library"] != false || s1["has_file"] != false {
		t.Errorf("series 1 = %v", s1)
	}
	if len(e.radarr.terms) != 1 || e.radarr.terms[0] != "dune" || e.sonarr.terms[0] != "dune" {
		t.Errorf("lookup terms radarr=%v sonarr=%v", e.radarr.terms, e.sonarr.terms)
	}
}

func TestSearchCapsAtForty(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 30; i++ {
		e.radarr.movieLookup = append(e.radarr.movieLookup, map[string]any{"title": fmt.Sprintf("M%d", i)})
		e.sonarr.seriesList = append(e.sonarr.seriesList, map[string]any{"title": fmt.Sprintf("S%d", i)})
	}
	res := results(t, decode(t, e.do(viewer, "GET", "/api/search?q=x", nil)))
	if len(res) != 40 {
		t.Fatalf("len = %d, want 40", len(res))
	}
	if res[0]["type"] != "movie" || res[1]["type"] != "series" {
		t.Errorf("not interleaved: %v %v", res[0]["type"], res[1]["type"])
	}
}

func TestSearchValidationAndPartialFailure(t *testing.T) {
	e := newEnv(t)
	for _, q := range []string{"/api/search", "/api/search?q=", "/api/search?q=%20%20"} {
		wantStatus(t, e.do(viewer, "GET", q, nil), 400)
	}

	e.radarr.lookupErr = errors.New("radarr down")
	e.sonarr.seriesList = list(t, `{"title":"Only Series","tvdbId":5}`)
	rec := e.do(viewer, "GET", "/api/search?q=x", nil)
	wantStatus(t, rec, 200)
	res := results(t, decode(t, rec))
	if len(res) != 1 || res[0]["title"] != "Only Series" {
		t.Fatalf("results = %v", res)
	}

	e.sonarr.lookupErr = errors.New("sonarr down")
	wantStatus(t, e.do(viewer, "GET", "/api/search?q=x", nil), 502)
}

func TestSearchEmptyResultsIsArray(t *testing.T) {
	e := newEnv(t)
	rec := e.do(viewer, "GET", "/api/search?q=nothing", nil)
	wantStatus(t, rec, 200)
	if got := rec.Body.String(); got != "{\"results\":[]}\n" {
		t.Fatalf("body = %q", got)
	}
}

func TestSearchAddMovie(t *testing.T) {
	e := newEnv(t)
	e.radarr.byTmdb = list(t, `{"title":"Dune","year":2021,"tmdbId":438631,"titleSlug":"dune-438631","images":[{"coverType":"poster","remoteUrl":"https://img/d.jpg"}],"id":0}`)
	e.radarr.profiles = list(t, `{"id":4,"name":"HD"}`, `{"id":9,"name":"4K"}`)
	e.radarr.addResp = jm(t, `{"id":42,"title":"Dune"}`)

	rec := e.do(manager, "POST", "/api/search/add", map[string]any{"type": "movie", "tmdb_id": 438631})
	wantStatus(t, rec, 200)
	if got := decode(t, rec); got["arr_id"] != float64(42) {
		t.Fatalf("response = %v", got)
	}
	if e.radarr.gotTmdbID != 438631 {
		t.Errorf("looked up tmdb %d", e.radarr.gotTmdbID)
	}
	if len(e.radarr.addedMov) != 1 {
		t.Fatalf("AddMovie calls = %d", len(e.radarr.addedMov))
	}
	// Round-trip through JSON so numbers compare the way Radarr would see them.
	raw, _ := json.Marshal(e.radarr.addedMov[0])
	got := jm(t, string(raw))
	want := jm(t, `{
		"title":"Dune","year":2021,"tmdbId":438631,"titleSlug":"dune-438631",
		"images":[{"coverType":"poster","remoteUrl":"https://img/d.jpg"}],
		"qualityProfileId":4,"rootFolderPath":"/media/movies","monitored":true,
		"minimumAvailability":"released","addOptions":{"searchForMovie":true}}`)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("AddMovie payload\n got  %v\n want %v", got, want)
	}
	if len(e.radarr.commands) != 0 {
		t.Errorf("a fresh add must not trigger a separate search: %v", e.radarr.commands)
	}
}

func TestSearchAddSeries(t *testing.T) {
	e := newEnv(t)
	e.sonarr.seriesList = list(t, `{"title":"Severance","year":2022,"tvdbId":371980,"titleSlug":"severance","images":[],"seasons":[{"seasonNumber":1,"monitored":true}],"id":0}`)
	e.sonarr.profiles = list(t, `{"id":2}`)
	e.sonarr.addResp = jm(t, `{"id":11}`)

	rec := e.do(manager, "POST", "/api/search/add", map[string]any{"type": "series", "tvdb_id": 371980})
	wantStatus(t, rec, 200)
	if decode(t, rec)["arr_id"] != float64(11) {
		t.Fatalf("body = %s", rec.Body)
	}
	if e.sonarr.terms[0] != "tvdb:371980" {
		t.Errorf("lookup term = %q", e.sonarr.terms[0])
	}
	raw, _ := json.Marshal(e.sonarr.addedSer[0])
	got := jm(t, string(raw))
	want := jm(t, `{
		"title":"Severance","year":2022,"tvdbId":371980,"titleSlug":"severance","images":[],
		"seasons":[{"seasonNumber":1,"monitored":true}],
		"qualityProfileId":2,"rootFolderPath":"/media/tv","seasonFolder":true,"monitored":true,
		"addOptions":{"searchForMissingEpisodes":true}}`)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("AddSeries payload\n got  %v\n want %v", got, want)
	}
}

func TestSearchAddAlreadyInLibraryTriggersSearch(t *testing.T) {
	e := newEnv(t)
	e.radarr.byTmdb = list(t, `{"title":"Dune","year":2021,"id":17}`)
	e.sonarr.seriesList = list(t, `{"title":"Severance","id":5}`)

	rec := e.do(manager, "POST", "/api/search/add", map[string]any{"type": "movie", "tmdb_id": 1})
	wantStatus(t, rec, 200)
	if decode(t, rec)["arr_id"] != float64(17) {
		t.Fatalf("body = %s", rec.Body)
	}
	if len(e.radarr.addedMov) != 0 {
		t.Fatal("must not re-add a movie that is already in the library")
	}
	if len(e.radarr.commands) != 1 || e.radarr.commands[0]["name"] != "MoviesSearch" {
		t.Fatalf("commands = %v", e.radarr.commands)
	}
	if ids, _ := e.radarr.commands[0]["movieIds"].([]int); len(ids) != 1 || ids[0] != 17 {
		t.Errorf("movieIds = %v", e.radarr.commands[0]["movieIds"])
	}

	rec = e.do(manager, "POST", "/api/search/add", map[string]any{"type": "series", "tvdb_id": 9})
	wantStatus(t, rec, 200)
	if len(e.sonarr.addedSer) != 0 {
		t.Fatal("must not re-add a series that is already in the library")
	}
	if len(e.sonarr.commands) != 1 || e.sonarr.commands[0]["name"] != "SeriesSearch" || e.sonarr.commands[0]["seriesId"] != 5 {
		t.Fatalf("commands = %v", e.sonarr.commands)
	}
}

func TestSearchAddErrors(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name string
		body any
		want int
	}{
		{"bad json", "{", 400},
		{"empty body", nil, 400},
		{"unknown type", map[string]any{"type": "album", "tmdb_id": 1}, 400},
		{"movie without tmdb id", map[string]any{"type": "movie", "tvdb_id": 1}, 400},
		{"series without tvdb id", map[string]any{"type": "series", "tmdb_id": 1}, 400},
		{"movie not found", map[string]any{"type": "movie", "tmdb_id": 1}, 404},
		{"series not found", map[string]any{"type": "series", "tvdb_id": 1}, 404},
	}
	for _, c := range cases {
		if got := e.do(manager, "POST", "/api/search/add", c.body).Code; got != c.want {
			t.Errorf("%s: status = %d, want %d", c.name, got, c.want)
		}
	}

	// Radarr answers but has no quality profiles.
	e.radarr.byTmdb = list(t, `{"title":"X","id":0}`)
	wantStatus(t, e.do(manager, "POST", "/api/search/add", map[string]any{"type": "movie", "tmdb_id": 1}), 502)

	// Add returns no id.
	e.radarr.profiles = list(t, `{"id":1}`)
	e.radarr.addResp = map[string]any{}
	wantStatus(t, e.do(manager, "POST", "/api/search/add", map[string]any{"type": "movie", "tmdb_id": 1}), 502)

	// Upstream lookup failure.
	e.radarr.lookupErr = errors.New("boom")
	wantStatus(t, e.do(manager, "POST", "/api/search/add", map[string]any{"type": "movie", "tmdb_id": 1}), 502)
}
