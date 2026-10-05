package qbt

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"pelicula/internal/httpx"
)

// fake serves canned bodies by path and captures the last form post.
func fake(t *testing.T, routes map[string]string) (*Client, *url.Values, *string) {
	t.Helper()
	var form url.Values
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost {
			if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
				t.Errorf("Content-Type = %q", ct)
			}
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			form, path = r.PostForm, r.URL.Path
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL), &form, &path
}

func TestPing(t *testing.T) {
	c, _, _ := fake(t, map[string]string{"GET /api/v2/app/version": "v5.0.2"})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPing_HTTPError(t *testing.T) {
	c, _, _ := fake(t, nil)
	err := c.Ping(context.Background())
	var he *httpx.HTTPError
	if !errors.As(err, &he) || he.StatusCode != 404 {
		t.Fatalf("err = %v", err)
	}
}

func TestListTorrents(t *testing.T) {
	c, _, _ := fake(t, map[string]string{"GET /api/v2/torrents/info": `[
		{"hash":"abc","name":"Movie","state":"downloading","category":"radarr","progress":0.5,
		 "dlspeed":1000,"upspeed":10,"eta":60,"size":4096,"added_on":1700000000}]`})
	got, err := c.ListTorrents(context.Background())
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v err %v", got, err)
	}
	want := Torrent{Hash: "abc", Name: "Movie", State: "downloading", Category: "radarr",
		Progress: 0.5, Dlspeed: 1000, Upspeed: 10, Eta: 60, Size: 4096, AddedOn: 1700000000}
	if got[0] != want {
		t.Fatalf("got %+v, want %+v", got[0], want)
	}
}

func TestListTorrents_Error(t *testing.T) {
	c, _, _ := fake(t, nil)
	if _, err := c.ListTorrents(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	c, _, _ = fake(t, map[string]string{"GET /api/v2/torrents/info": "<html>"})
	if _, err := c.ListTorrents(context.Background()); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestGetTransferInfo(t *testing.T) {
	c, _, _ := fake(t, map[string]string{"GET /api/v2/transfer/info": `{"dl_info_speed":123,"up_info_speed":45,"connection_status":"connected"}`})
	ti, err := c.GetTransferInfo(context.Background())
	if err != nil || ti.DlSpeed != 123 || ti.UpSpeed != 45 {
		t.Fatalf("ti=%+v err=%v", ti, err)
	}
}

func TestStopStartDelete(t *testing.T) {
	c, form, path := fake(t, map[string]string{
		"POST /api/v2/torrents/stop":   "",
		"POST /api/v2/torrents/start":  "",
		"POST /api/v2/torrents/delete": "",
	})
	ctx := context.Background()

	if err := c.StopTorrent(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	if *path != "/api/v2/torrents/stop" || form.Get("hashes") != "abc" {
		t.Fatalf("stop: %s %v", *path, *form)
	}
	if err := c.StartTorrent(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	if *path != "/api/v2/torrents/start" || form.Get("hashes") != "abc" {
		t.Fatalf("start: %s %v", *path, *form)
	}
	if err := c.DeleteTorrent(ctx, "abc", true); err != nil {
		t.Fatal(err)
	}
	if *path != "/api/v2/torrents/delete" || form.Get("deleteFiles") != "true" {
		t.Fatalf("delete: %s %v", *path, *form)
	}
	if err := c.DeleteTorrent(ctx, "abc", false); err != nil {
		t.Fatal(err)
	}
	if form.Get("deleteFiles") != "false" {
		t.Fatalf("deleteFiles = %q", form.Get("deleteFiles"))
	}
}

func TestMutationHTTPError(t *testing.T) {
	c, _, _ := fake(t, nil) // 404 on everything
	for name, err := range map[string]error{
		"stop":   c.StopTorrent(context.Background(), "x"),
		"start":  c.StartTorrent(context.Background(), "x"),
		"delete": c.DeleteTorrent(context.Background(), "x", true),
		"port":   c.SetListenPort(context.Background(), 1234),
	} {
		var he *httpx.HTTPError
		if !errors.As(err, &he) || he.StatusCode != 404 {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestListenPort(t *testing.T) {
	c, form, path := fake(t, map[string]string{
		"GET /api/v2/app/preferences":     `{"listen_port":6881,"dht":false}`,
		"POST /api/v2/app/setPreferences": "",
	})
	ctx := context.Background()
	port, err := c.GetListenPort(ctx)
	if err != nil || port != 6881 {
		t.Fatalf("port=%d err=%v", port, err)
	}
	if err := c.SetListenPort(ctx, 51413); err != nil {
		t.Fatal(err)
	}
	if *path != "/api/v2/app/setPreferences" || form.Get("json") != `{"listen_port":51413}` {
		t.Fatalf("set: %s %v", *path, *form)
	}
}

func TestGetListenPort_Error(t *testing.T) {
	c, _, _ := fake(t, nil)
	if _, err := c.GetListenPort(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}
