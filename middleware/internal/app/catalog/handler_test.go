package catalog

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	arrclient "pelicula-api/internal/clients/arr"
)

// stubArrForHandler is a minimal ArrClient for handler-internal tests.
// It uses real arr.Client instances backed by the provided server URLs.
type stubArrForHandler struct {
	sonarr   *arrclient.Client
	radarr   *arrclient.Client
	prowlarr *arrclient.Client
}

func newStubArrForHandler(sonarrURL, radarrURL string) *stubArrForHandler {
	return &stubArrForHandler{
		sonarr:   arrclient.New(sonarrURL, "sk"),
		radarr:   arrclient.New(radarrURL, "rk"),
		prowlarr: arrclient.New("", ""),
	}
}

func (s *stubArrForHandler) Keys() (sonarr, radarr, prowlarr string) { return "sk", "rk", "" }
func (s *stubArrForHandler) SonarrClient() *arrclient.Client         { return s.sonarr }
func (s *stubArrForHandler) RadarrClient() *arrclient.Client         { return s.radarr }
func (s *stubArrForHandler) ProwlarrClient() *arrclient.Client       { return s.prowlarr }

// TestFindImportHistoryID_BothUnmarshalsFail verifies that when both the array
// and wrapped-object unmarshal attempts fail, the returned error unwraps to
// expose both individual parse errors (not just the first).
func TestFindImportHistoryID_BothUnmarshalsFail(t *testing.T) {
	// Serve a body that is neither a JSON array nor {"records":[...]} —
	// both unmarshal attempts will fail.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// First unmarshal (into []map[string]any) fails because this is an object,
		// not an array. Second unmarshal (into {Records []map[string]any}) fails
		// because "records" is a string, not an array of objects.
		w.Write([]byte(`{"records":"not-an-array"}`)) //nolint:errcheck
	}))
	defer srv.Close()

	client := arrclient.New(srv.URL, "key")
	h := &Handler{
		Arr:       newStubArrForHandler(srv.URL, ""),
		SonarrURL: srv.URL,
	}

	_, _, err := h.findImportHistoryID(context.Background(), client, "sonarr", 1, 5)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// The error message should mention "parse history".
	if !containsSubstr(err.Error(), "parse history") {
		t.Errorf("error = %q, want to contain 'parse history'", err.Error())
	}

	// errors.Join wraps multiple errors; unwrap should yield both.
	// Go 1.20 errors.Join produces an error whose Unwrap() []error returns both.
	var errs interface{ Unwrap() []error }
	if !errors.As(err, &errs) {
		t.Logf("error chain: %v", err)
		// The fmt.Errorf("%w", errors.Join(e1, e2)) wraps the joined error;
		// unwrap once to get the joined error, then check its Unwrap slice.
		unwrapped := errors.Unwrap(err)
		if unwrapped == nil {
			t.Fatal("expected wrapped errors, got a non-wrapping error")
		}
		if !errors.As(unwrapped, &errs) {
			t.Fatalf("expected errors.Join result in chain, got %T: %v", unwrapped, unwrapped)
		}
	}
	joinedErrs := errs.Unwrap()
	if len(joinedErrs) < 2 {
		t.Errorf("expected at least 2 joined errors, got %d: %v", len(joinedErrs), joinedErrs)
	}
}

// ── F15: slog.Warn on unmarshal failure ──────────────────────────────────────

// TestHandleCatalogDetail_WarnOnBadProculaResponse verifies that when Procula
// returns a flags response that cannot be unmarshalled into the expected shape,
// a slog.Warn is emitted rather than silently discarding the error.
func TestHandleCatalogDetail_WarnOnBadProculaResponse(t *testing.T) {
	// Procula stub: flags endpoint returns {"flags":"not-an-object"} which
	// cannot be unmarshalled into flagsWrap{Rows []map[string]any}.
	procula := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Invalid shape for flagsWrap — "rows" key is absent; top-level value
		// is a string, not an object at all.
		w.Write([]byte(`"not-an-object"`)) //nolint:errcheck
	}))
	defer procula.Close()

	// Capture slog output.
	var capture warnCapture
	orig := slog.Default()
	slog.SetDefault(slog.New(&capture))
	t.Cleanup(func() { slog.SetDefault(orig) })

	h := &Handler{
		Client:     NewProxyClient(http.DefaultClient),
		ProculaURL: procula.URL,
		Arr:        newStubArrForHandler("", ""),
	}

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/catalog/detail?path=/media/movie.mkv", nil)
	w := httptest.NewRecorder()
	h.HandleCatalogDetail(w, req)

	// Handler should still return 200 (best-effort degraded to empty flags).
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	// A warn must have been logged about the parse failure.
	if !capture.hasWarnContaining("failed to parse procula flags response") {
		t.Error("expected slog.Warn about flags parse failure, got none")
	}
}

// warnCapture is a slog.Handler that records warn+ messages.
type warnCapture struct {
	mu   sync.Mutex
	msgs []string
}

func (c *warnCapture) Enabled(_ context.Context, lvl slog.Level) bool {
	return lvl >= slog.LevelWarn
}
func (c *warnCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, r.Message)
	return nil
}
func (c *warnCapture) WithAttrs(attrs []slog.Attr) slog.Handler { return c }
func (c *warnCapture) WithGroup(name string) slog.Handler       { return c }

func (c *warnCapture) hasWarnContaining(sub string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range c.msgs {
		if containsSubstr(m, sub) {
			return true
		}
	}
	return false
}

func containsSubstr(s, sub string) bool {
	return len(sub) == 0 || func() bool {
		for i := range s {
			if i+len(sub) <= len(s) && s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()
}

// TestHandleCatalogDetail_RespectsRequestCtx verifies that cancelling the
// request context aborts the outbound Procula calls promptly.
func TestHandleCatalogDetail_RespectsRequestCtx(t *testing.T) {
	procula := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(500 * time.Millisecond):
			w.Write([]byte(`{}`)) //nolint:errcheck
		case <-r.Context().Done():
			// Client cancelled — don't write anything.
		}
	}))
	defer procula.Close()

	h := &Handler{
		Client:     NewProxyClient(http.DefaultClient),
		ProculaURL: procula.URL,
		Arr:        newStubArrForHandler("", ""),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/catalog/detail?path=/media/movie.mkv", nil).
		WithContext(ctx)
	w := httptest.NewRecorder()

	start := time.Now()
	h.HandleCatalogDetail(w, req)
	elapsed := time.Since(start)

	if elapsed >= 100*time.Millisecond {
		t.Errorf("HandleCatalogDetail took %v with cancelled ctx; expected < 100ms", elapsed)
	}
}
