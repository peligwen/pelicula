package procula

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// setupRemoveTestEnv wires appDB + the debounce/API-key package vars for a
// runRemoveAction test and returns a cleanup-free helper: callers still use
// t.Cleanup via the individual setters, this just centralizes the boilerplate.
func setupRemoveTestEnv(t *testing.T) {
	t.Helper()
	db := testDB(t)
	oldDB := appDB
	appDB = db
	t.Cleanup(func() { appDB = oldDB })

	// Debounce=0 makes scheduleJellyfinRefresh fire synchronously so the
	// refresh POST (if any) completes before runRemoveAction returns —
	// avoids a background timer racing past test teardown.
	oldDebounce := refreshDebounce
	SetRefreshDebounceForTest(0)
	t.Cleanup(func() { SetRefreshDebounceForTest(oldDebounce) })

	oldKey := proculaAPIKey
	proculaAPIKey = "test-procula-key"
	t.Cleanup(func() { proculaAPIKey = oldKey })
}

// TestRunRemoveAction_ValidatesArrType verifies bad/missing arr_type is rejected.
func TestRunRemoveAction_ValidatesArrType(t *testing.T) {
	setupRemoveTestEnv(t)

	job := &Job{Params: map[string]any{"arr_type": "plex", "arr_id": float64(1)}}
	_, err := runRemoveAction(context.Background(), nil, job)
	if err == nil || !strings.Contains(err.Error(), "arr_type must be") {
		t.Errorf("expected arr_type validation error, got %v", err)
	}

	job = &Job{Params: map[string]any{"arr_id": float64(1)}}
	_, err = runRemoveAction(context.Background(), nil, job)
	if err == nil || !strings.Contains(err.Error(), "arr_type must be") {
		t.Errorf("expected arr_type validation error for missing arr_type, got %v", err)
	}
}

// TestRunRemoveAction_ValidatesArrID verifies a missing/zero/negative arr_id is rejected.
func TestRunRemoveAction_ValidatesArrID(t *testing.T) {
	setupRemoveTestEnv(t)

	for _, arrID := range []float64{0, -1} {
		job := &Job{Params: map[string]any{"arr_type": "radarr", "arr_id": arrID}}
		_, err := runRemoveAction(context.Background(), nil, job)
		if err == nil || !strings.Contains(err.Error(), "arr_id required") {
			t.Errorf("arr_id=%v: expected arr_id error, got %v", arrID, err)
		}
	}
}

// TestRunRemoveAction_HappyPath verifies the full success path: the correct
// request (method, path, X-API-Key header, JSON body) reaches the middleware
// stand-in, the returned file_paths are used to purge catalog_flags, and the
// result shape matches the documented contract.
func TestRunRemoveAction_HappyPath(t *testing.T) {
	tests := []struct {
		name    string
		arrType string
		arrID   int
	}{
		{"radarr movie", "radarr", 42},
		{"sonarr series", "sonarr", 7},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setupRemoveTestEnv(t)

			const filePath = "/media/movies/Foo (2024)/Foo.mkv"

			// Seed a catalog_flags row for the path so we can prove it gets purged.
			if err := UpsertFlagsForPath(appDB, filePath, "job_1", []Flag{
				{Code: "missing_subtitles", Severity: FlagSeverityWarn},
			}); err != nil {
				t.Fatalf("seed flags: %v", err)
			}

			var gotMethod, gotPath, gotKey string
			var gotBody map[string]any
			var refreshHits int

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/pelicula/catalog/remove":
					gotMethod = r.Method
					gotPath = r.URL.Path
					gotKey = r.Header.Get("X-API-Key")
					json.NewDecoder(r.Body).Decode(&gotBody) //nolint:errcheck
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(removeRespBody{ //nolint:errcheck
						Removed:   true,
						ArrType:   tc.arrType,
						ArrID:     tc.arrID,
						Title:     "Foo",
						FilePaths: []string{filePath},
					})
				case "/api/pelicula/jellyfin/refresh":
					refreshHits++
					w.WriteHeader(http.StatusOK)
				default:
					t.Errorf("unexpected request path %q", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()

			t.Setenv("PELICULA_API_URL", srv.URL)

			job := &Job{Params: map[string]any{
				"arr_type": tc.arrType,
				"arr_id":   float64(tc.arrID),
			}}
			result, err := runRemoveAction(context.Background(), nil, job)
			if err != nil {
				t.Fatalf("runRemoveAction: %v", err)
			}

			if gotMethod != http.MethodPost {
				t.Errorf("method = %q, want POST", gotMethod)
			}
			if gotPath != "/api/pelicula/catalog/remove" {
				t.Errorf("path = %q", gotPath)
			}
			if gotKey != "test-procula-key" {
				t.Errorf("X-API-Key = %q, want %q", gotKey, "test-procula-key")
			}
			if gotBody["arr_type"] != tc.arrType {
				t.Errorf("body arr_type = %v, want %q", gotBody["arr_type"], tc.arrType)
			}
			if int(gotBody["arr_id"].(float64)) != tc.arrID {
				t.Errorf("body arr_id = %v, want %d", gotBody["arr_id"], tc.arrID)
			}

			if result["removed"] != true {
				t.Errorf("result[removed] = %v, want true", result["removed"])
			}
			if result["arr_type"] != tc.arrType {
				t.Errorf("result[arr_type] = %v, want %q", result["arr_type"], tc.arrType)
			}
			if result["arr_id"] != tc.arrID {
				t.Errorf("result[arr_id] = %v, want %d", result["arr_id"], tc.arrID)
			}
			paths, _ := result["file_paths"].([]string)
			if len(paths) != 1 || paths[0] != filePath {
				t.Errorf("result[file_paths] = %v, want [%q]", result["file_paths"], filePath)
			}

			// catalog_flags row must be purged.
			row, err := flagsByPath(appDB, filePath)
			if err != nil {
				t.Fatalf("flagsByPath: %v", err)
			}
			if row != nil {
				t.Errorf("expected catalog_flags row purged, still present: %+v", row)
			}

			// Jellyfin refresh scheduled (debounce=0 → synchronous).
			if refreshHits != 1 {
				t.Errorf("jellyfin refresh hits = %d, want 1", refreshHits)
			}
		})
	}
}

// TestRunRemoveAction_MiddlewareErrorPropagates verifies a non-2xx from the
// middleware surfaces as an error and does not attempt flag purge/refresh.
func TestRunRemoveAction_MiddlewareErrorPropagates(t *testing.T) {
	setupRemoveTestEnv(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	t.Setenv("PELICULA_API_URL", srv.URL)

	job := &Job{Params: map[string]any{"arr_type": "radarr", "arr_id": float64(1)}}
	_, err := runRemoveAction(context.Background(), nil, job)
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("expected HTTP 500 error, got %v", err)
	}
}

// TestRunRemoveAction_IdempotentWhenAlreadyGone verifies that when the
// middleware reports the *arr entry was already absent (it tolerates *arr
// 404s as success), runRemoveAction still purges flags, refreshes Jellyfin,
// and returns removed:true — retrying a remove must be safe.
func TestRunRemoveAction_IdempotentWhenAlreadyGone(t *testing.T) {
	setupRemoveTestEnv(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/pelicula/catalog/remove":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(removeRespBody{ //nolint:errcheck
				Removed:   true,
				ArrType:   "radarr",
				ArrID:     99,
				Title:     "Already Gone",
				FilePaths: nil, // *arr had no movieFile to report
			})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	t.Setenv("PELICULA_API_URL", srv.URL)

	job := &Job{Params: map[string]any{"arr_type": "radarr", "arr_id": float64(99)}}
	result, err := runRemoveAction(context.Background(), nil, job)
	if err != nil {
		t.Fatalf("runRemoveAction: %v", err)
	}
	if result["removed"] != true {
		t.Errorf("result[removed] = %v, want true", result["removed"])
	}
}

// TestRemoveActionRegistered verifies the "remove" action is wired into the
// registry with the expected shape (movie+series, sync).
func TestRemoveActionRegistered(t *testing.T) {
	actionRegistry = map[string]*ActionDef{}
	registerBuiltinActions()

	def := Lookup("remove")
	if def == nil {
		t.Fatal("remove not registered")
	}
	if !def.Sync {
		t.Error("remove should be sync")
	}
	wantMovie, wantSeries := false, false
	for _, a := range def.AppliesTo {
		if a == "movie" {
			wantMovie = true
		}
		if a == "series" {
			wantSeries = true
		}
	}
	if !wantMovie || !wantSeries {
		t.Errorf("AppliesTo = %v, want movie+series", def.AppliesTo)
	}
	if def.Handler == nil {
		t.Error("Handler not set")
	}
}

// TestRunRemoveAction_FlagPurgeFailureNonFatal verifies a flag purge error for
// one path does not abort the action — the *arr deletion already happened
// and the middleware already dropped its catalog row; procula's local index
// cleanup is best-effort.
func TestRunRemoveAction_FlagPurgeFailureNonFatal(t *testing.T) {
	setupRemoveTestEnv(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/pelicula/catalog/remove":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(removeRespBody{ //nolint:errcheck
				Removed:   true,
				ArrType:   "sonarr",
				ArrID:     5,
				Title:     "Some Show",
				FilePaths: []string{""}, // empty path → DeleteFlagsForPath errors
			})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	t.Setenv("PELICULA_API_URL", srv.URL)

	job := &Job{Params: map[string]any{"arr_type": "sonarr", "arr_id": float64(5)}}
	result, err := runRemoveAction(context.Background(), nil, job)
	if err != nil {
		t.Fatalf("runRemoveAction should not fail on flag-purge error: %v", err)
	}
	if result["removed"] != true {
		t.Errorf("result[removed] = %v, want true", result["removed"])
	}
}
