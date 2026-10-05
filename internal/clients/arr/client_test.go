package arr

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"pelicula/internal/httpx"
)

type call struct {
	Method, Path, Query, Body string
}

// fake records every request and answers from a "METHOD path" route table.
type fake struct {
	mu     sync.Mutex
	calls  []call
	routes map[string]func(w http.ResponseWriter, r *http.Request)
}

func newFake(t *testing.T, routes map[string]func(w http.ResponseWriter, r *http.Request)) (*Client, *fake) {
	t.Helper()
	f := &fake{routes: routes}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.calls = append(f.calls, call{r.Method, r.URL.Path, r.URL.RawQuery, string(b)})
		f.mu.Unlock()
		if got := r.Header.Get("X-Api-Key"); r.URL.Path != "/ping" && got != "key" {
			t.Errorf("X-Api-Key = %q on %s", got, r.URL.Path)
		}
		if h, ok := f.routes[r.Method+" "+r.URL.Path]; ok {
			h(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL+"/sonarr", "key", "v3"), f
}

func (f *fake) last() call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func reply(body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}
}

func TestPing(t *testing.T) {
	c, _ := newFake(t, map[string]func(http.ResponseWriter, *http.Request){"GET /sonarr/ping": reply(`{"status":"OK"}`)})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := New("http://127.0.0.1:1", "k", "v3").Ping(context.Background()); err == nil {
		t.Fatal("expected connection error")
	}
}

func TestPing_HTTPError(t *testing.T) {
	c, _ := newFake(t, nil) // everything 404s
	err := c.Ping(context.Background())
	var he *httpx.HTTPError
	if !errors.As(err, &he) || he.StatusCode != 404 {
		t.Fatalf("err = %v", err)
	}
}

func TestAPIVersionPrefix(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "k", "v1").ListIndexers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v1/indexer" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestSetAPIKey(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Api-Key")
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c := New(srv.URL, "", "v3")
	c.SetAPIKey("rotated")
	if _, err := c.GetMovies(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got != "rotated" {
		t.Fatalf("key = %q", got)
	}
}

// Radarr answers an unknown TMDB id with 404; that is "no candidate", not an error.
func TestLookupMovieByTmdbID_NotFound(t *testing.T) {
	c, _ := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /sonarr/api/v3/movie/lookup/tmdb": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "movie not found", http.StatusNotFound)
		},
	})
	r, err := c.LookupMovieByTmdbID(context.Background(), 1)
	if err != nil || len(r) != 0 {
		t.Fatalf("LookupMovieByTmdbID on 404: %v %v", r, err)
	}
}

func TestLibraryReadsAndLookups(t *testing.T) {
	c, f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /sonarr/api/v3/movie/5":           reply(`{"id":5,"runtime":120,"movieFile":{"id":9,"path":"/media/movies/a.mkv"}}`),
		"GET /sonarr/api/v3/movie":             reply(`[{"id":1},{"id":2}]`),
		"GET /sonarr/api/v3/movie/lookup":      reply(`[{"title":"Heat","tmdbId":949}]`),
		"GET /sonarr/api/v3/movie/lookup/tmdb": reply(`{"tmdbId":949,"id":0}`), // Radarr: one object, not an array
		"GET /sonarr/api/v3/series":            reply(`[{"id":3}]`),
		"GET /sonarr/api/v3/series/3":          reply(`{"id":3,"runtime":45}`),
		"GET /sonarr/api/v3/series/lookup":     reply(`[{"title":"Show","tvdbId":12345}]`),
		"GET /sonarr/api/v3/episodefile":       reply(`[{"id":7,"path":"/media/tv/s01e01.mkv"}]`),
		"GET /sonarr/api/v3/qualityprofile":    reply(`[{"id":4,"name":"HD"}]`),
		"GET /sonarr/api/v3/rootfolder":        reply(`[{"id":1,"path":"/media/tv"}]`),
		"GET /sonarr/api/v3/downloadclient":    reply(`[{"id":2,"name":"qBittorrent"}]`),
		"GET /sonarr/api/v3/notification":      reply(`[{"id":6,"name":"Pelicula"}]`),
		"GET /sonarr/api/v3/applications":      reply(`[{"id":8,"name":"Sonarr"}]`),
		"GET /sonarr/api/v3/indexer":           reply(`[{"id":1,"name":"idx"}]`),
	})
	ctx := context.Background()

	m, err := c.GetMovie(ctx, 5)
	if err != nil || m["runtime"].(float64) != 120 {
		t.Fatalf("GetMovie: %v %v", m, err)
	}
	if ms, err := c.GetMovies(ctx); err != nil || len(ms) != 2 {
		t.Fatalf("GetMovies: %v %v", ms, err)
	}
	if r, err := c.LookupMovie(ctx, "heat & co"); err != nil || len(r) != 1 {
		t.Fatalf("LookupMovie: %v %v", r, err)
	}
	if got := f.last().Query; got != "term=heat+%26+co" {
		t.Fatalf("lookup query = %q", got)
	}
	if r, err := c.LookupMovieByTmdbID(ctx, 949); err != nil || len(r) != 1 || r[0]["tmdbId"].(float64) != 949 {
		t.Fatalf("LookupMovieByTmdbID: %v %v", r, err)
	}
	if got := f.last().Query; got != "tmdbId=949" {
		t.Fatalf("tmdb query = %q", got)
	}
	if s, err := c.GetSeries(ctx); err != nil || len(s) != 1 {
		t.Fatalf("GetSeries: %v %v", s, err)
	}
	if s, err := c.GetSeriesByID(ctx, 3); err != nil || s["runtime"].(float64) != 45 {
		t.Fatalf("GetSeriesByID: %v %v", s, err)
	}
	if r, err := c.LookupSeries(ctx, "tvdb:12345"); err != nil || len(r) != 1 {
		t.Fatalf("LookupSeries: %v %v", r, err)
	}
	if got := f.last().Query; got != "term=tvdb%3A12345" {
		t.Fatalf("series lookup query = %q", got)
	}
	if r, err := c.GetEpisodeFiles(ctx, 3); err != nil || r[0]["path"] != "/media/tv/s01e01.mkv" {
		t.Fatalf("GetEpisodeFiles: %v %v", r, err)
	}
	if got := f.last().Query; got != "seriesId=3" {
		t.Fatalf("episodefile query = %q", got)
	}
	if r, err := c.GetQualityProfiles(ctx); err != nil || len(r) != 1 {
		t.Fatalf("GetQualityProfiles: %v %v", r, err)
	}
	if r, err := c.ListRootFolders(ctx); err != nil || len(r) != 1 {
		t.Fatalf("ListRootFolders: %v %v", r, err)
	}
	if r, err := c.ListDownloadClients(ctx); err != nil || len(r) != 1 {
		t.Fatalf("ListDownloadClients: %v %v", r, err)
	}
	if r, err := c.ListNotifications(ctx); err != nil || len(r) != 1 {
		t.Fatalf("ListNotifications: %v %v", r, err)
	}
	if r, err := c.ListApplications(ctx); err != nil || len(r) != 1 {
		t.Fatalf("ListApplications: %v %v", r, err)
	}
	if r, err := c.ListIndexers(ctx); err != nil || len(r) != 1 {
		t.Fatalf("ListIndexers: %v %v", r, err)
	}
}

func TestWrites(t *testing.T) {
	ok := func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"id":11}`)) }
	c, f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /sonarr/api/v3/command":         ok,
		"POST /sonarr/api/v3/movie":           ok,
		"POST /sonarr/api/v3/series":          ok,
		"POST /sonarr/api/v3/rootfolder":      ok,
		"POST /sonarr/api/v3/downloadclient":  ok,
		"PUT /sonarr/api/v3/downloadclient/2": ok,
		"POST /sonarr/api/v3/notification":    ok,
		"PUT /sonarr/api/v3/notification/6":   ok,
		"POST /sonarr/api/v3/applications":    ok,
		"PUT /sonarr/api/v3/applications/8":   ok,
	})
	ctx := context.Background()

	if err := c.TriggerCommand(ctx, map[string]any{"name": "MoviesSearch", "movieIds": []int{5}}); err != nil {
		t.Fatal(err)
	}
	if got := f.last().Body; got != `{"movieIds":[5],"name":"MoviesSearch"}` {
		t.Fatalf("command body = %s", got)
	}
	m, err := c.AddMovie(ctx, map[string]any{"title": "Heat"})
	if err != nil || m["id"].(float64) != 11 {
		t.Fatalf("AddMovie: %v %v", m, err)
	}
	s, err := c.AddSeries(ctx, map[string]any{"title": "Show"})
	if err != nil || s["id"].(float64) != 11 {
		t.Fatalf("AddSeries: %v %v", s, err)
	}
	for name, fn := range map[string]func() error{
		"AddRootFolder":        func() error { return c.AddRootFolder(ctx, map[string]any{"path": "/media/tv"}) },
		"AddDownloadClient":    func() error { return c.AddDownloadClient(ctx, map[string]any{"name": "q"}) },
		"UpdateDownloadClient": func() error { return c.UpdateDownloadClient(ctx, 2, map[string]any{"name": "q"}) },
		"AddNotification":      func() error { return c.AddNotification(ctx, map[string]any{"name": "n"}) },
		"UpdateNotification":   func() error { return c.UpdateNotification(ctx, 6, map[string]any{"name": "n"}) },
		"AddApplication":       func() error { return c.AddApplication(ctx, map[string]any{"name": "a"}) },
		"UpdateApplication":    func() error { return c.UpdateApplication(ctx, 8, map[string]any{"name": "a"}) },
	} {
		if err := fn(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestGetAllQueueRecords_Paginates(t *testing.T) {
	var pages []string
	c, f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /sonarr/api/v3/queue": func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			pages = append(pages, q.Get("page"))
			if q.Get("pageSize") != "100" || q.Get("includeUnknownMovieItems") != "true" || q.Get("includeUnknownSeriesItems") != "true" {
				t.Errorf("unexpected query %s", r.URL.RawQuery)
			}
			switch q.Get("page") {
			case "1":
				w.Write([]byte(`{"totalRecords":3,"records":[{"id":1},{"id":2}]}`))
			case "2":
				w.Write([]byte(`{"totalRecords":3,"records":[{"id":3}]}`))
			default:
				w.Write([]byte(`{"totalRecords":3,"records":[]}`))
			}
		},
	})
	recs, err := c.GetAllQueueRecords(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 || strings.Join(pages, ",") != "1,2" {
		t.Fatalf("recs=%d pages=%v calls=%d", len(recs), pages, len(f.calls))
	}
}

func TestGetAllQueueRecords_Error(t *testing.T) {
	c, _ := newFake(t, nil) // 404
	if _, err := c.GetAllQueueRecords(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestDeleteQueueItem(t *testing.T) {
	c, f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"DELETE /sonarr/api/v3/queue/42": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) },
	})
	if err := c.DeleteQueueItem(context.Background(), 42, true, false); err != nil {
		t.Fatal(err)
	}
	if got := f.last().Query; got != "removeFromClient=true&blocklist=false" {
		t.Fatalf("query = %q", got)
	}
	if err := c.DeleteQueueItem(context.Background(), 43, true, true); err == nil {
		t.Fatal("expected 404 error for unknown id")
	}
}

func TestGetHistory_RoutesAndShapes(t *testing.T) {
	c, f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /sonarr/api/v3/history/movie":  reply(`[{"id":1,"downloadId":"ABC","eventType":"grabbed"}]`),
		"GET /sonarr/api/v3/history/series": reply(`[{"id":2}]`),
		"GET /sonarr/api/v3/history":        reply(`{"page":1,"totalRecords":1,"records":[{"id":3}]}`),
	})
	ctx := context.Background()

	r, err := c.GetHistory(ctx, "movieId=5&eventType=1")
	if err != nil || len(r) != 1 || r[0]["downloadId"] != "ABC" {
		t.Fatalf("movie history: %v %v", r, err)
	}
	if got := f.last(); got.Path != "/sonarr/api/v3/history/movie" || got.Query != "movieId=5&eventType=1" {
		t.Fatalf("call = %+v", got)
	}

	r, err = c.GetHistory(ctx, "seriesId=5&episodeId=9&eventType=1")
	if err != nil || len(r) != 1 || r[0]["id"].(float64) != 2 {
		t.Fatalf("series history: %v %v", r, err)
	}
	if got := f.last(); got.Path != "/sonarr/api/v3/history/series" || got.Query != "seriesId=5&episodeId=9&eventType=1" {
		t.Fatalf("call = %+v", got)
	}

	r, err = c.GetHistory(ctx, "pageSize=20")
	if err != nil || len(r) != 1 || r[0]["id"].(float64) != 3 {
		t.Fatalf("paged history: %v %v", r, err)
	}
	if got := f.last().Path; got != "/sonarr/api/v3/history" {
		t.Fatalf("path = %q", got)
	}
}

func TestGetHistory_Errors(t *testing.T) {
	c, _ := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /sonarr/api/v3/history/movie": reply(`oops`),
	})
	if _, err := c.GetHistory(context.Background(), "movieId=1"); err == nil {
		t.Fatal("expected parse error")
	}
	if _, err := c.GetHistory(context.Background(), "seriesId=1"); err == nil {
		t.Fatal("expected 404 error")
	}
}

func TestMarkHistoryFailedAndDeleteFiles(t *testing.T) {
	var methods []string
	rec := func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}
	c, f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /sonarr/api/v3/history/failed/12": rec,
		"DELETE /sonarr/api/v3/moviefile/9":     rec,
		"DELETE /sonarr/api/v3/episodefile/7":   rec,
	})
	ctx := context.Background()
	if err := c.MarkHistoryFailed(ctx, 12); err != nil {
		t.Fatal(err)
	}
	if f.last().Body != "" {
		t.Fatalf("history/failed should send no body, got %q", f.last().Body)
	}
	if err := c.DeleteMovieFile(ctx, 9); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteEpisodeFile(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 3 {
		t.Fatalf("methods = %v", methods)
	}
	if err := c.DeleteMovieFile(ctx, 10); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestHTTPErrorSurfaces(t *testing.T) {
	c, _ := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"POST /sonarr/api/v3/movie": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `[{"errorMessage":"already added"}]`, http.StatusBadRequest)
		},
	})
	_, err := c.AddMovie(context.Background(), map[string]any{"tmdbId": 1})
	var he *httpx.HTTPError
	if !errors.As(err, &he) || he.StatusCode != 400 || !strings.Contains(he.Body, "already added") {
		t.Fatalf("err = %v", err)
	}
}

func TestRawHelpers(t *testing.T) {
	c, f := newFake(t, map[string]func(http.ResponseWriter, *http.Request){
		"GET /sonarr/api/v3/system/status": reply(`{"version":"4"}`),
		"PUT /sonarr/api/v3/config/host":   reply(`{}`),
		"POST /sonarr/api/v3/x":            reply(`{}`),
		"DELETE /sonarr/api/v3/x/1":        func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) },
	})
	ctx := context.Background()
	if b, err := c.Get(ctx, "/system/status"); err != nil || string(b) != `{"version":"4"}` {
		t.Fatalf("Get: %s %v", b, err)
	}
	if _, err := c.Put(ctx, "/config/host", map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Post(ctx, "/x", map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, "/x/1"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 4 {
		t.Fatalf("calls = %d", len(f.calls))
	}
}
