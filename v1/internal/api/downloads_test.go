package api

import (
	"errors"
	"testing"
)

const (
	hashA = "0123456789abcdef0123456789abcdef01234567"
	hashB = "fedcba9876543210fedcba9876543210fedcba98"
)

func withQBT(e *env) *fakeQBT {
	q := &fakeQBT{
		torrents: []Torrent{
			{Hash: hashA, Name: "Dune.2021.1080p", State: "downloading", Category: "radarr", Progress: 0.5, Dlspeed: 1000, Upspeed: 10, Eta: 60, Size: 9000},
			{Hash: hashB, Name: "Severance.S01E01", State: "pausedDL", Category: "tv-sonarr", Progress: 0.1, Size: 500},
		},
		transfer: &TransferInfo{DlSpeed: 1000, UpSpeed: 10},
	}
	e.srv.QBT = q
	return q
}

func TestDownloadsWithoutVPN(t *testing.T) {
	e := newEnv(t)
	rec := e.do(viewer, "GET", "/api/downloads", nil)
	wantStatus(t, rec, 200)
	want := `{"downloads":[],"transfer":{"dl_speed":0,"up_speed":0},"vpn":false}` + "\n"
	if rec.Body.String() != want {
		t.Fatalf("body = %s, want %s", rec.Body, want)
	}
	wantStatus(t, e.do(manager, "POST", "/api/downloads/"+hashA+"/pause", nil), 503)
}

func TestListDownloads(t *testing.T) {
	e := newEnv(t)
	withQBT(e)
	rec := e.do(viewer, "GET", "/api/downloads", nil)
	wantStatus(t, rec, 200)
	got := decode(t, rec)
	if got["vpn"] != true {
		t.Errorf("vpn = %v", got["vpn"])
	}
	dl := got["downloads"].([]any)
	if len(dl) != 2 {
		t.Fatalf("downloads = %v", dl)
	}
	first := dl[0].(map[string]any)
	wantKeys := map[string]any{
		"hash": hashA, "name": "Dune.2021.1080p", "state": "downloading", "progress": 0.5,
		"dlspeed": float64(1000), "upspeed": float64(10), "eta": float64(60), "size": float64(9000), "category": "radarr",
	}
	for k, v := range wantKeys {
		if first[k] != v {
			t.Errorf("downloads[0].%s = %v, want %v", k, first[k], v)
		}
	}
	if len(first) != len(wantKeys) {
		t.Errorf("downloads[0] has keys %v, want exactly %d", first, len(wantKeys))
	}
	if tr := got["transfer"].(map[string]any); tr["dl_speed"] != float64(1000) || tr["up_speed"] != float64(10) {
		t.Errorf("transfer = %v", tr)
	}
}

func TestListDownloadsErrors(t *testing.T) {
	e := newEnv(t)
	q := withQBT(e)
	q.listErr = errors.New("down")
	wantStatus(t, e.do(viewer, "GET", "/api/downloads", nil), 502)

	// Transfer info is cosmetic: a failure there does not fail the list.
	q.listErr = nil
	q.transfer = nil
	rec := e.do(viewer, "GET", "/api/downloads", nil)
	wantStatus(t, rec, 200)
	if tr := decode(t, rec)["transfer"].(map[string]any); tr["dl_speed"] != float64(0) {
		t.Errorf("transfer = %v", tr)
	}
}

func TestPauseResume(t *testing.T) {
	e := newEnv(t)
	q := withQBT(e)
	wantStatus(t, e.do(manager, "POST", "/api/downloads/"+hashA+"/pause", nil), 204)
	wantStatus(t, e.do(manager, "POST", "/api/downloads/"+hashB+"/resume", nil), 204)
	if len(q.stopped) != 1 || q.stopped[0] != hashA || len(q.started) != 1 || q.started[0] != hashB {
		t.Fatalf("stopped=%v started=%v", q.stopped, q.started)
	}

	// qBittorrent treats these as selectors for every torrent; never forward them.
	for _, bad := range []string{"all", "ALL", "abc%7Cdef", "zzzz"} {
		if got := e.do(manager, "POST", "/api/downloads/"+bad+"/pause", nil).Code; got != 400 {
			t.Errorf("pause %q = %d, want 400", bad, got)
		}
	}
	if len(q.stopped) != 1 {
		t.Fatalf("a bad hash reached qBittorrent: %v", q.stopped)
	}
}

func TestDeleteDownloadViaRadarrQueue(t *testing.T) {
	e := newEnv(t)
	q := withQBT(e)
	// Radarr reports the hash upper-case; qBittorrent lower-case.
	e.radarr.queue = list(t,
		`{"id":3,"downloadId":"FEDCBA9876543210FEDCBA9876543210FEDCBA98"}`,
		`{"id":8,"downloadId":"0123456789ABCDEF0123456789ABCDEF01234567"}`,
	)
	rec := e.do(admin, "DELETE", "/api/downloads/"+hashA+"?blocklist=true", nil)
	wantStatus(t, rec, 204)
	if len(e.radarr.deleted) != 1 || e.radarr.deleted[0] != (deleteCall{8, true, true}) {
		t.Fatalf("radarr deletes = %v, want queue item 8 with removeFromClient+blocklist", e.radarr.deleted)
	}
	if len(q.deleted) != 0 || len(e.sonarr.deleted) != 0 {
		t.Fatalf("only the owning *arr should be asked: qbt=%v sonarr=%v", q.deleted, e.sonarr.deleted)
	}

	// Without the flag the release is not blocklisted.
	rec = e.do(admin, "DELETE", "/api/downloads/"+hashA, nil)
	wantStatus(t, rec, 204)
	if got := e.radarr.deleted[1]; got != (deleteCall{8, true, false}) {
		t.Fatalf("second delete = %v", got)
	}
}

func TestDeleteDownloadViaSonarrQueue(t *testing.T) {
	e := newEnv(t)
	q := withQBT(e)
	e.radarr.queue = list(t, `{"id":1,"downloadId":"other"}`)
	e.sonarr.queue = list(t, `{"id":21,"downloadId":"`+hashB+`"}`)
	wantStatus(t, e.do(admin, "DELETE", "/api/downloads/"+hashB+"?blocklist=true", nil), 204)
	if len(e.sonarr.deleted) != 1 || e.sonarr.deleted[0] != (deleteCall{21, true, true}) {
		t.Fatalf("sonarr deletes = %v", e.sonarr.deleted)
	}
	if len(e.radarr.deleted) != 0 || len(q.deleted) != 0 {
		t.Fatalf("radarr=%v qbt=%v", e.radarr.deleted, q.deleted)
	}
}

func TestDeleteDownloadFallsBackToQBT(t *testing.T) {
	e := newEnv(t)
	q := withQBT(e)
	e.radarr.queue = list(t, `{"id":1,"downloadId":"someone-else"}`)
	e.sonarr.queueErr = errors.New("sonarr down") // a failed lookup is skipped, not fatal

	wantStatus(t, e.do(admin, "DELETE", "/api/downloads/"+hashA+"?blocklist=true", nil), 204)
	if len(q.deleted) != 1 || q.deleted[0] != hashA || !q.deletedFiles[0] {
		t.Fatalf("qbt deletes = %v files=%v, want %s with files", q.deleted, q.deletedFiles, hashA)
	}
	if len(e.radarr.deleted)+len(e.sonarr.deleted) != 0 {
		t.Fatal("nothing should be deleted through *arr when no record matches")
	}
}

func TestDeleteDownloadNotFoundWithoutQBT(t *testing.T) {
	e := newEnv(t)
	wantStatus(t, e.do(admin, "DELETE", "/api/downloads/"+hashA, nil), 404)
	wantStatus(t, e.do(admin, "DELETE", "/api/downloads/all", nil), 400)
}
