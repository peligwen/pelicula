package jellyfin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	jfclient "pelicula-api/internal/clients/jellyfin"
)

// fakeEncodingServer starts an httptest.Server with a GET /System/Configuration/encoding
// handler that returns currentAccel as HardwareAccelerationType. It also registers a
// POST handler that captures the posted body. Returns the server, a pointer to the
// captured body bytes, and a counter of POST calls.
func fakeEncodingServer(t *testing.T, currentAccel string) (*httptest.Server, *[]byte, *atomic.Int32) {
	t.Helper()
	var postBody []byte
	var postCalls atomic.Int32
	mux := http.NewServeMux()

	mux.HandleFunc("/System/Configuration/encoding", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
				"HardwareAccelerationType": currentAccel,
				"VaapiDevice":              "",
				"EncodingThreadCount":      -1,
			})
		case http.MethodPost:
			postCalls.Add(1)
			body, _ := io.ReadAll(r.Body)
			postBody = body
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &postBody, &postCalls
}

func TestWireHwAccel_VaapiWhenCurrentIsNone(t *testing.T) {
	srv, postBody, postCalls := fakeEncodingServer(t, "none")
	client := jfclient.NewWithHTTPClient(srv.URL, srv.Client())

	wireHwAccel(client, "test-token", HwAccelVaapi, vaapiDevice)

	if postCalls.Load() != 1 {
		t.Fatalf("POST called %d times, want 1", postCalls.Load())
	}
	var body map[string]any
	if err := json.Unmarshal(*postBody, &body); err != nil {
		t.Fatalf("invalid POST body JSON: %v", err)
	}
	if got, _ := body["HardwareAccelerationType"].(string); got != "vaapi" {
		t.Errorf("HardwareAccelerationType = %q, want vaapi", got)
	}
	if got, _ := body["VaapiDevice"].(string); got != vaapiDevice {
		t.Errorf("VaapiDevice = %q, want %q", got, vaapiDevice)
	}
}

func TestWireHwAccel_NoGetOrPostWhenProbeIsNone(t *testing.T) {
	// Wire guards on hwType != HwAccelNone before calling wireHwAccel at all.
	// Confirm the helper is not called by running a probe that returns none
	// against a server that would record any call — and asserting zero calls.
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/System/Configuration/encoding", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := jfclient.NewWithHTTPClient(srv.URL, srv.Client())

	// Probe returns none → Wire must not reach wireHwAccel at all.
	hwType, hwDevice := HwAccelProbe("none", errStat, "linux", "amd64")
	if hwType != HwAccelNone {
		t.Fatalf("probe should return none, got %q", hwType)
	}
	if hwType != HwAccelNone {
		wireHwAccel(client, "test-token", hwType, hwDevice)
	}

	if calls.Load() != 0 {
		t.Errorf("encoding endpoint called %d times, want 0 when probe returns none", calls.Load())
	}
}

func TestWireHwAccel_NoPostWhenAlreadySet(t *testing.T) {
	srv, _, postCalls := fakeEncodingServer(t, "qsv")
	client := jfclient.NewWithHTTPClient(srv.URL, srv.Client())

	wireHwAccel(client, "test-token", HwAccelVaapi, vaapiDevice)

	if postCalls.Load() != 0 {
		t.Errorf("POST called %d times, want 0 (user config respected)", postCalls.Load())
	}
}

func TestWireHwAccel_VideoToolboxWhenCurrentIsNone(t *testing.T) {
	srv, postBody, postCalls := fakeEncodingServer(t, "none")
	client := jfclient.NewWithHTTPClient(srv.URL, srv.Client())

	wireHwAccel(client, "test-token", HwAccelVideoToolbox, "")

	if postCalls.Load() != 1 {
		t.Fatalf("POST called %d times, want 1", postCalls.Load())
	}
	var body map[string]any
	if err := json.Unmarshal(*postBody, &body); err != nil {
		t.Fatalf("invalid POST body JSON: %v", err)
	}
	if got, _ := body["HardwareAccelerationType"].(string); got != "videotoolbox" {
		t.Errorf("HardwareAccelerationType = %q, want videotoolbox", got)
	}
}

func TestWireHwAccel_OtherFieldsPreserved(t *testing.T) {
	srv, postBody, postCalls := fakeEncodingServer(t, "none")
	client := jfclient.NewWithHTTPClient(srv.URL, srv.Client())

	wireHwAccel(client, "test-token", HwAccelQSV, "")

	if postCalls.Load() != 1 {
		t.Fatalf("POST called %d times, want 1", postCalls.Load())
	}
	var body map[string]any
	if err := json.Unmarshal(*postBody, &body); err != nil {
		t.Fatalf("invalid POST body JSON: %v", err)
	}
	// The existing EncodingThreadCount field should be echoed back unchanged.
	if v, ok := body["EncodingThreadCount"]; !ok {
		t.Error("EncodingThreadCount missing from POST body — GET-merge-POST not preserving other fields")
	} else if v.(float64) != -1 {
		t.Errorf("EncodingThreadCount = %v, want -1", v)
	}
}
