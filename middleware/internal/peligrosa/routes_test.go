package peligrosa

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newRequestsRouteMux wires a full mux via RegisterRoutes, backed by real
// Auth/Requests stores, so tests can exercise the actual route-level
// middleware (Guard/GuardAdmin + httputil.RequireLocalOriginSoft) rather than
// calling handlers directly.
func newRequestsRouteMux(t *testing.T) (*http.ServeMux, *Auth) {
	t.Helper()
	a := newTestJellyfinAuth(t, nil, nil)
	deps := &Deps{
		DB:       nil,
		Auth:     a,
		Requests: newRequestStore(t),
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, deps)
	return mux, a
}

// ── MWD-4: /api/pelicula/requests and /requests/ must carry the CSRF origin
// wrapper (httputil.RequireLocalOriginSoft), matching every other
// state-mutating admin route — see the invites routes in routes.go for the
// established pattern this mirrors. ──────────────────────────────────────

func TestRequestsRoute_ForeignOrigin_Rejected(t *testing.T) {
	mux, a := newRequestsRouteMux(t)
	token := insertSession(a, "alice", RoleViewer, time.Now().Add(time.Hour))

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests", bytes.NewReader([]byte(`{}`)))
	addSessionCookie(req, token)
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("POST /requests with foreign origin: status = %d, want 403", w.Code)
	}
}

func TestRequestsRoute_NoOrigin_PassesToHandler(t *testing.T) {
	mux, a := newRequestsRouteMux(t)
	token := insertSession(a, "alice", RoleViewer, time.Now().Add(time.Hour))

	// No Origin header (API/curl caller) — RequireLocalOriginSoft must let it
	// through to the handler, which then rejects the empty body with 400.
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests", bytes.NewReader(nil))
	addSessionCookie(req, token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code == http.StatusForbidden {
		t.Error("POST /requests with no origin should reach the handler, got 403 from the origin wrapper")
	}
}

func TestRequestOpRoute_ForeignOrigin_Rejected(t *testing.T) {
	mux, a := newRequestsRouteMux(t)
	token := insertSession(a, "admin", RoleAdmin, time.Now().Add(time.Hour))

	req := httptest.NewRequest(http.MethodDelete, "/api/pelicula/requests/req-does-not-exist", nil)
	addSessionCookie(req, token)
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("DELETE /requests/{id} with foreign origin: status = %d, want 403", w.Code)
	}
}

func TestRequestOpRoute_NoOrigin_PassesToHandler(t *testing.T) {
	mux, a := newRequestsRouteMux(t)
	token := insertSession(a, "admin", RoleAdmin, time.Now().Add(time.Hour))

	// No Origin header — must reach the handler, which 404s on the unknown id
	// rather than the wrapper 403ing it.
	req := httptest.NewRequest(http.MethodDelete, "/api/pelicula/requests/req-does-not-exist", nil)
	addSessionCookie(req, token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code == http.StatusForbidden {
		t.Error("DELETE /requests/{id} with no origin should reach the handler, got 403 from the origin wrapper")
	}
}

// ── Phase 5: unseen/acknowledge routes must win the admin subtree ──────────

// TestRequestUnseenRoute_ViewerNotBlockedByAdminSubtree proves the
// method+exact-path registration for GET /api/pelicula/requests/unseen wins
// Go's ServeMux precedence over the admin-gated "/api/pelicula/requests/"
// subtree — a viewer session must reach the handler (200), not get 403'd by
// auth.GuardAdmin.
func TestRequestUnseenRoute_ViewerNotBlockedByAdminSubtree(t *testing.T) {
	mux, a := newRequestsRouteMux(t)
	token := insertSession(a, "alice", RoleViewer, time.Now().Add(time.Hour))

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/requests/unseen", nil)
	addSessionCookie(req, token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code == http.StatusForbidden {
		t.Fatal("GET /requests/unseen: viewer session got 403 — the admin subtree shadowed the viewer route")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

// TestRequestAcknowledgeRoute_ViewerNotBlockedByAdminSubtree is the POST
// twin of the above for /api/pelicula/requests/acknowledge.
func TestRequestAcknowledgeRoute_ViewerNotBlockedByAdminSubtree(t *testing.T) {
	mux, a := newRequestsRouteMux(t)
	token := insertSession(a, "alice", RoleViewer, time.Now().Add(time.Hour))

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests/acknowledge", bytes.NewReader([]byte(`{}`)))
	addSessionCookie(req, token)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code == http.StatusForbidden {
		t.Fatal("POST /requests/acknowledge: viewer session got 403 — the admin subtree shadowed the viewer route")
	}
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
}

// TestRequestAcknowledgeRoute_ForeignOrigin_Rejected verifies the new
// acknowledge route carries the same CSRF origin wrap as its create-request
// sibling.
func TestRequestAcknowledgeRoute_ForeignOrigin_Rejected(t *testing.T) {
	mux, a := newRequestsRouteMux(t)
	token := insertSession(a, "alice", RoleViewer, time.Now().Add(time.Hour))

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/requests/acknowledge", bytes.NewReader([]byte(`{}`)))
	addSessionCookie(req, token)
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("POST /requests/acknowledge with foreign origin: status = %d, want 403", w.Code)
	}
}
