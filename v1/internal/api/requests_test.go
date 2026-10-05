package api

import (
	"fmt"
	"testing"

	"pelicula/internal/store"
)

func reqs(t *testing.T, e *env, w who) []map[string]any {
	t.Helper()
	rec := e.do(w, "GET", "/api/requests", nil)
	wantStatus(t, rec, 200)
	var out []map[string]any
	for _, r := range decode(t, rec)["requests"].([]any) {
		out = append(out, r.(map[string]any))
	}
	return out
}

func movieRequest(tmdb int, title string) map[string]any {
	return map[string]any{"type": "movie", "tmdb_id": tmdb, "title": title, "year": 2021, "poster": "https://img/p.jpg"}
}

func TestCreateRequestAndDedupe(t *testing.T) {
	e := newEnv(t)
	rec := e.do(viewer, "POST", "/api/requests", movieRequest(438631, "Dune"))
	wantStatus(t, rec, 201)
	got := decode(t, rec)
	if got["media_type"] != "movie" || got["title"] != "Dune" || got["tmdb_id"] != float64(438631) ||
		got["year"] != float64(2021) || got["poster"] != "https://img/p.jpg" ||
		got["requested_by"] != "vera" || got["status"] != "pending" || got["id"] == nil {
		t.Fatalf("request = %v", got)
	}
	if len(e.radarr.addedMov)+len(e.radarr.commands) != 0 {
		t.Fatal("a pending request must not touch Radarr")
	}

	// Same title again, from a different user: conflict, naming the original.
	rec = e.do(viewer2, "POST", "/api/requests", movieRequest(438631, "Dune"))
	wantStatus(t, rec, 409)
	dup := decode(t, rec)
	if dup["error"] != "already requested" {
		t.Errorf("error = %v", dup["error"])
	}
	if orig, _ := dup["request"].(map[string]any); orig == nil || orig["id"] != got["id"] {
		t.Errorf("conflict should carry the existing request: %v", dup)
	}
	if n := len(reqs(t, e, admin)); n != 1 {
		t.Fatalf("%d requests stored, want 1", n)
	}

	// A series with the same numeric id is a different title.
	wantStatus(t, e.do(viewer, "POST", "/api/requests",
		map[string]any{"type": "series", "tvdb_id": 438631, "title": "Other"}), 201)

	// A declined request no longer blocks a new one.
	id := int64(got["id"].(float64))
	if err := e.store.UpdateRequestStatus(t.Context(), id, store.RequestDeclined, "mona", "no", 0); err != nil {
		t.Fatal(err)
	}
	wantStatus(t, e.do(viewer2, "POST", "/api/requests", movieRequest(438631, "Dune")), 201)
}

func TestCreateRequestValidation(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		name string
		body any
	}{
		{"not json", "{"},
		{"empty", nil},
		{"bad type", map[string]any{"type": "game", "tmdb_id": 1, "title": "x"}},
		{"movie without tmdb", map[string]any{"type": "movie", "title": "x"}},
		{"series without tvdb", map[string]any{"type": "series", "tmdb_id": 3, "title": "x"}},
		{"no title", map[string]any{"type": "movie", "tmdb_id": 1, "title": "  "}},
		{"huge title", map[string]any{"type": "movie", "tmdb_id": 1, "title": fmt.Sprint(make([]byte, 400))}},
		{"javascript poster", map[string]any{"type": "movie", "tmdb_id": 1, "title": "x", "poster": "javascript:alert(1)"}},
	}
	for _, c := range cases {
		if got := e.do(viewer, "POST", "/api/requests", c.body).Code; got != 400 {
			t.Errorf("%s: status = %d, want 400", c.name, got)
		}
	}
}

func TestRequestVisibility(t *testing.T) {
	e := newEnv(t)
	wantStatus(t, e.do(viewer, "POST", "/api/requests", movieRequest(1, "One")), 201)
	wantStatus(t, e.do(viewer2, "POST", "/api/requests", movieRequest(2, "Two")), 201)
	wantStatus(t, e.do(manager, "POST", "/api/requests", movieRequest(3, "Three")), 201)

	if got := reqs(t, e, viewer); len(got) != 1 || got[0]["title"] != "One" {
		t.Errorf("viewer sees %v, want only their own", got)
	}
	if got := reqs(t, e, viewer2); len(got) != 1 || got[0]["title"] != "Two" {
		t.Errorf("viewer2 sees %v", got)
	}
	if got := reqs(t, e, manager); len(got) != 3 {
		t.Errorf("manager sees %d, want 3", len(got))
	}
	if got := reqs(t, e, admin); len(got) != 3 || got[0]["title"] != "Three" {
		t.Errorf("admin sees %v, want all, newest first", got)
	}

	// Nothing requested: still a JSON array.
	rec := newEnv(t).do(viewer, "GET", "/api/requests", nil)
	if rec.Body.String() != "{\"requests\":[]}\n" {
		t.Errorf("empty list body = %q", rec.Body)
	}
}

func approveSetup(e *env) {
	e.radarr.byTmdb = []map[string]any{{"title": "Dune", "year": float64(2021), "id": float64(0)}}
	e.radarr.profiles = []map[string]any{{"id": float64(1)}}
	e.radarr.addResp = map[string]any{"id": float64(55)}
}

func TestApproveRequest(t *testing.T) {
	e := newEnv(t)
	approveSetup(e)
	rec := e.do(viewer, "POST", "/api/requests", movieRequest(438631, "Dune"))
	id := int64(decode(t, rec)["id"].(float64))
	path := fmt.Sprintf("/api/requests/%d/approve", id)

	wantStatus(t, e.do(viewer, "POST", path, nil), 403)

	rec = e.do(manager, "POST", path, nil)
	wantStatus(t, rec, 200)
	got := decode(t, rec)
	if got["status"] != "approved" || got["decided_by"] != "mona" || got["arr_id"] != float64(55) {
		t.Fatalf("approved request = %v", got)
	}
	if len(e.radarr.addedMov) != 1 {
		t.Fatalf("AddMovie calls = %d, want 1", len(e.radarr.addedMov))
	}

	// Approving twice is refused and does not hit Radarr again.
	wantStatus(t, e.do(manager, "POST", path, nil), 409)
	if len(e.radarr.addedMov) != 1 {
		t.Fatal("second approve reached Radarr")
	}

	wantStatus(t, e.do(manager, "POST", "/api/requests/999/approve", nil), 404)
	wantStatus(t, e.do(manager, "POST", "/api/requests/abc/approve", nil), 400)
}

func TestApproveFailureKeepsRequestPending(t *testing.T) {
	e := newEnv(t)
	e.radarr.lookupErr = fmt.Errorf("radarr down")
	rec := e.do(viewer, "POST", "/api/requests", movieRequest(1, "Dune"))
	id := int64(decode(t, rec)["id"].(float64))

	wantStatus(t, e.do(manager, "POST", fmt.Sprintf("/api/requests/%d/approve", id), nil), 502)
	got, err := e.store.GetRequest(t.Context(), id)
	if err != nil || got.Status != store.RequestPending {
		t.Fatalf("request = %+v, err %v; want still pending", got, err)
	}
}

func TestDeclineRequest(t *testing.T) {
	e := newEnv(t)
	rec := e.do(viewer, "POST", "/api/requests", movieRequest(1, "Dune"))
	id := int64(decode(t, rec)["id"].(float64))
	path := fmt.Sprintf("/api/requests/%d/decline", id)

	wantStatus(t, e.do(viewer, "POST", path, map[string]any{"note": "x"}), 403)

	rec = e.do(manager, "POST", path, map[string]any{"note": "already own it"})
	wantStatus(t, rec, 200)
	got := decode(t, rec)
	if got["status"] != "declined" || got["note"] != "already own it" || got["decided_by"] != "mona" {
		t.Fatalf("declined request = %v", got)
	}
	if len(e.radarr.addedMov)+len(e.radarr.commands) != 0 {
		t.Fatal("decline must not touch Radarr")
	}
	wantStatus(t, e.do(manager, "POST", path, nil), 409)

	// The requester sees the outcome.
	if mine := reqs(t, e, viewer); mine[0]["status"] != "declined" || mine[0]["note"] != "already own it" {
		t.Errorf("viewer's view = %v", mine[0])
	}

	// A note is optional: no body at all declines too.
	rec = e.do(viewer2, "POST", "/api/requests", movieRequest(2, "Two"))
	id2 := int64(decode(t, rec)["id"].(float64))
	wantStatus(t, e.do(manager, "POST", fmt.Sprintf("/api/requests/%d/decline", id2), nil), 200)

	// A declined request can be reconsidered.
	approveSetup(e)
	wantStatus(t, e.do(manager, "POST", fmt.Sprintf("/api/requests/%d/approve", id), nil), 200)
}

func TestAutoApproveSetting(t *testing.T) {
	e := newEnv(t)
	approveSetup(e)
	if err := e.store.SetSetting(t.Context(), store.SettingAutoApprove, "true"); err != nil {
		t.Fatal(err)
	}

	rec := e.do(viewer, "POST", "/api/requests", movieRequest(438631, "Dune"))
	wantStatus(t, rec, 201)
	got := decode(t, rec)
	if got["status"] != "approved" || got["decided_by"] != "auto" || got["arr_id"] != float64(55) || got["requested_by"] != "vera" {
		t.Fatalf("auto-approved request = %v", got)
	}
	if len(e.radarr.addedMov) != 1 {
		t.Fatalf("AddMovie calls = %d, want 1", len(e.radarr.addedMov))
	}

	// Requesting it again is a conflict: approved counts as open.
	wantStatus(t, e.do(viewer2, "POST", "/api/requests", movieRequest(438631, "Dune")), 409)

	// If *arr is down the request is kept, pending, for a manager.
	e.radarr.lookupErr = fmt.Errorf("radarr down")
	rec = e.do(viewer, "POST", "/api/requests", movieRequest(7, "Seven"))
	wantStatus(t, rec, 201)
	if got := decode(t, rec); got["status"] != "pending" || got["decided_by"] != nil {
		t.Fatalf("fallback request = %v", got)
	}

	// Off again: back to manual approval.
	if err := e.store.SetSetting(t.Context(), store.SettingAutoApprove, "false"); err != nil {
		t.Fatal(err)
	}
	e.radarr.lookupErr = nil
	before := len(e.radarr.addedMov)
	rec = e.do(viewer, "POST", "/api/requests", movieRequest(8, "Eight"))
	if decode(t, rec)["status"] != "pending" || len(e.radarr.addedMov) != before {
		t.Fatal("auto-approve off must leave requests pending")
	}
}
