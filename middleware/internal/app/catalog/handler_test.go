package catalog

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// stubArrForHandler is a minimal ArrClient for handler-internal tests.
type stubArrForHandler struct {
	doGet func(baseURL, apiKey, path string) ([]byte, error)
}

func (s *stubArrForHandler) Keys() (sonarr, radarr, prowlarr string) { return "sk", "rk", "" }
func (s *stubArrForHandler) ArrGet(baseURL, apiKey, path string) ([]byte, error) {
	if s.doGet != nil {
		return s.doGet(baseURL, apiKey, path)
	}
	return nil, nil
}
func (s *stubArrForHandler) ArrPost(baseURL, apiKey, path string, payload any) ([]byte, error) {
	return nil, nil
}
func (s *stubArrForHandler) ArrPut(baseURL, apiKey, path string, payload any) ([]byte, error) {
	return nil, nil
}
func (s *stubArrForHandler) ArrDelete(baseURL, apiKey, path string) ([]byte, error) { return nil, nil }
func (s *stubArrForHandler) ArrGetAllQueueRecords(baseURL, apiKey, apiVer, extraParams string) ([]map[string]any, error) {
	return nil, nil
}

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

	arr := &stubArrForHandler{
		doGet: func(baseURL, apiKey, path string) ([]byte, error) {
			resp, err := http.Get(baseURL + path)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			buf := make([]byte, 512)
			n, _ := resp.Body.Read(buf)
			return buf[:n], nil
		},
	}

	h := &Handler{
		Arr:       arr,
		SonarrURL: srv.URL,
	}

	_, _, err := h.findImportHistoryID(srv.URL, "key", "sonarr", 1, 5)
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
