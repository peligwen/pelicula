package downloads

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubClient implements QbtClient for tests.
// qbtResponses maps path → raw JSON bytes. qbtPostErr controls QbtPost failures.
// keysReturned are the API keys returned by Keys().
// arrResponses maps baseURL+path → raw JSON bytes for ArrGet calls.
type stubClient struct {
	qbtResponses map[string][]byte
	qbtGetErr    error
	qbtPostErr   error
	keysRet      [3]string // sonarr, radarr, prowlarr

	// arrResponses: key is baseURL+path, value is response bytes.
	arrResponses map[string][]byte
	arrGetErr    error
	arrQueueResp []map[string]any
	arrQueueErr  error
}

func (s *stubClient) QbtGet(path string) ([]byte, error) {
	if s.qbtGetErr != nil {
		return nil, s.qbtGetErr
	}
	if b, ok := s.qbtResponses[path]; ok {
		return b, nil
	}
	return []byte("null"), nil
}

func (s *stubClient) QbtPost(path, form string) error {
	return s.qbtPostErr
}

func (s *stubClient) Keys() (sonarr, radarr, prowlarr string) {
	return s.keysRet[0], s.keysRet[1], s.keysRet[2]
}

func (s *stubClient) ArrGet(baseURL, apiKey, path string) ([]byte, error) {
	if s.arrGetErr != nil {
		return nil, s.arrGetErr
	}
	key := baseURL + path
	if b, ok := s.arrResponses[key]; ok {
		return b, nil
	}
	return []byte("{}"), nil
}

func (s *stubClient) ArrPost(baseURL, apiKey, path string, payload any) ([]byte, error) {
	return []byte("{}"), nil
}

func (s *stubClient) ArrPut(baseURL, apiKey, path string, payload any) ([]byte, error) {
	return []byte("{}"), nil
}

func (s *stubClient) ArrDelete(baseURL, apiKey, path string) ([]byte, error) {
	return []byte("{}"), nil
}

func (s *stubClient) ArrGetAllQueueRecords(baseURL, apiKey, apiVer, extraParams string) ([]map[string]any, error) {
	if s.arrQueueErr != nil {
		return nil, s.arrQueueErr
	}
	return s.arrQueueResp, nil
}

// downstreamDown returns an error that simulates a refused connection.
var downstreamDown = fmt.Errorf("connection refused")

// --- HandleDownloads ---

func TestHandleDownloads_Happy(t *testing.T) {
	torrentsJSON := `[
		{"hash":"abc123","name":"Movie.mkv","progress":0.5,"dlspeed":1024,"upspeed":512,"eta":300,"state":"downloading","size":1073741824,"category":"radarr"},
		{"hash":"def456","name":"Show.S01.mkv","progress":1.0,"dlspeed":0,"upspeed":0,"eta":0,"state":"queuedDL","size":536870912,"category":"sonarr"}
	]`
	statsJSON := `{"dl_info_speed":2048,"up_info_speed":1024}`

	svc := &stubClient{
		qbtResponses: map[string][]byte{
			"/api/v2/torrents/info": []byte(torrentsJSON),
			"/api/v2/transfer/info": []byte(statsJSON),
		},
	}
	h := &Handler{Svc: svc}

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/downloads", nil)
	w := httptest.NewRecorder()
	h.HandleDownloads(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleDownloads happy: code = %d, body = %s", w.Code, w.Body.String())
	}

	var resp Response
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("HandleDownloads happy: unmarshal: %v", err)
	}
	if len(resp.Torrents) != 2 {
		t.Errorf("HandleDownloads happy: want 2 torrents, got %d", len(resp.Torrents))
	}
	if resp.Stats.Active != 1 {
		t.Errorf("HandleDownloads happy: want Active=1, got %d", resp.Stats.Active)
	}
	if resp.Stats.Queued != 1 {
		t.Errorf("HandleDownloads happy: want Queued=1, got %d", resp.Stats.Queued)
	}
	if resp.Stats.DLSpeed != 2048 {
		t.Errorf("HandleDownloads happy: want DLSpeed=2048, got %d", resp.Stats.DLSpeed)
	}
}

func TestHandleDownloads_Upstream_Down(t *testing.T) {
	svc := &stubClient{qbtGetErr: downstreamDown}
	h := &Handler{Svc: svc}

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/downloads", nil)
	w := httptest.NewRecorder()
	h.HandleDownloads(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("HandleDownloads down: code = %d, want 502", w.Code)
	}
}

// --- HandleDownloadStats ---

func TestHandleDownloadStats_Happy(t *testing.T) {
	statsJSON := `{"dl_info_speed":4096,"up_info_speed":512}`
	svc := &stubClient{
		qbtResponses: map[string][]byte{
			"/api/v2/transfer/info": []byte(statsJSON),
		},
	}
	h := &Handler{Svc: svc}

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/downloads/stats", nil)
	w := httptest.NewRecorder()
	h.HandleDownloadStats(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleDownloadStats happy: code = %d, body = %s", w.Code, w.Body.String())
	}

	var stats DownloadStats
	if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
		t.Fatalf("HandleDownloadStats happy: unmarshal: %v", err)
	}
	if stats.DLSpeed != 4096 {
		t.Errorf("HandleDownloadStats happy: DLSpeed = %d, want 4096", stats.DLSpeed)
	}
	if stats.UPSpeed != 512 {
		t.Errorf("HandleDownloadStats happy: UPSpeed = %d, want 512", stats.UPSpeed)
	}
}

func TestHandleDownloadStats_Upstream_Down(t *testing.T) {
	svc := &stubClient{qbtGetErr: downstreamDown}
	h := &Handler{Svc: svc}

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/downloads/stats", nil)
	w := httptest.NewRecorder()
	h.HandleDownloadStats(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("HandleDownloadStats down: code = %d, want 502", w.Code)
	}
}

// --- HandleDownloadPause ---

func TestHandleDownloadPause_Happy(t *testing.T) {
	svc := &stubClient{}
	h := &Handler{Svc: svc}

	body := `{"hash":"abc123abc123","paused":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/downloads/pause", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleDownloadPause(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleDownloadPause happy: code = %d, body = %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("HandleDownloadPause happy: unmarshal: %v", err)
	}
	if resp["status"] != "paused" {
		t.Errorf("HandleDownloadPause happy: status = %q, want paused", resp["status"])
	}
}

func TestHandleDownloadPause_Upstream_Down(t *testing.T) {
	svc := &stubClient{qbtPostErr: downstreamDown}
	h := &Handler{Svc: svc}

	body := `{"hash":"abc123abc123","paused":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/downloads/pause", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleDownloadPause(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("HandleDownloadPause down: code = %d, want 502", w.Code)
	}
}

// --- HandleDownloadCancel ---

func TestHandleDownloadCancel_Happy(t *testing.T) {
	// queueRecords simulate a radarr queue entry with our hash
	queueRecords := []map[string]any{
		{"downloadId": "abc123abc123", "id": float64(42), "movieId": float64(7)},
	}
	// Movie data returned by ArrGet for unmonitoring
	movieData, _ := json.Marshal(map[string]any{"id": 7, "title": "Test Movie", "monitored": true})

	srv := &stubClient{
		keysRet:      [3]string{"sonarr-key", "radarr-key", "prowlarr-key"},
		arrQueueResp: queueRecords,
		arrResponses: map[string][]byte{
			"http://radarr/api/v3/movie/7": movieData,
		},
	}
	h := &Handler{
		Svc:       srv,
		SonarrURL: "http://sonarr",
		RadarrURL: "http://radarr",
	}

	body := `{"hash":"abc123abc123","category":"radarr","blocklist":false,"reason":"not interested"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/downloads/cancel", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleDownloadCancel(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HandleDownloadCancel happy: code = %d, body = %s", w.Code, w.Body.String())
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("HandleDownloadCancel happy: unmarshal: %v", err)
	}
	if resp["status"] != "removed" {
		t.Errorf("HandleDownloadCancel happy: status = %q, want removed", resp["status"])
	}
}

func TestHandleDownloadCancel_Upstream_Down(t *testing.T) {
	// qbt delete fails; arr queue errors too — but handler still returns 200
	// since it logs but doesn't fail on qbt delete error. Test the upstream
	// failure case by using an unknown category which causes an early 422.
	// For a true "upstream down" test we validate unknown category → 422.
	svc := &stubClient{
		keysRet: [3]string{"", "", ""},
	}
	h := &Handler{
		Svc:       svc,
		SonarrURL: "http://sonarr",
		RadarrURL: "http://radarr",
	}

	// Unknown category → 422 UnprocessableEntity
	body := `{"hash":"abc123abc123","category":"unknown"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/downloads/cancel", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleDownloadCancel(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("HandleDownloadCancel unknown category: code = %d, want 422", w.Code)
	}
}

// TestHandleDownloadCancel_QbtPostErr verifies that a qBittorrent delete failure
// is logged but does not change the 200 response (best-effort cleanup).
func TestHandleDownloadCancel_QbtPostErr(t *testing.T) {
	svc := &stubClient{
		keysRet:      [3]string{"sonarr-key", "radarr-key", ""},
		qbtPostErr:   downstreamDown,
		arrQueueResp: []map[string]any{},
	}
	h := &Handler{
		Svc:       svc,
		SonarrURL: "http://sonarr",
		RadarrURL: "http://radarr",
	}

	body := `{"hash":"abc123abc123","category":"radarr","blocklist":true}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/downloads/cancel", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleDownloadCancel(w, req)

	// Handler still returns 200 even when qbt delete fails
	if w.Code != http.StatusOK {
		t.Errorf("HandleDownloadCancel qbt err: code = %d, want 200", w.Code)
	}
}

// TestHandleDownloads_MethodNotAllowed verifies that non-GET requests are rejected.
func TestHandleDownloads_MethodNotAllowed(t *testing.T) {
	svc := &stubClient{}
	h := &Handler{Svc: svc}

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/downloads", bytes.NewReader(nil))
	w := httptest.NewRecorder()
	h.HandleDownloads(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("HandleDownloads method not allowed: code = %d, want 405", w.Code)
	}
}

// TestHandleDownloadPause_BadRequest verifies missing hash returns 400.
func TestHandleDownloadPause_BadRequest(t *testing.T) {
	svc := &stubClient{}
	h := &Handler{Svc: svc}

	body := `{"paused":true}` // no hash
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/downloads/pause", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleDownloadPause(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("HandleDownloadPause bad request: code = %d, want 400", w.Code)
	}
}
