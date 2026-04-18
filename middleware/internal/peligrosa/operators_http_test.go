package peligrosa

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleOperatorsGetNilStore(t *testing.T) {
	// (*Auth)(nil) receiver — HandleOperators must return []
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/operators", nil)
	w := httptest.NewRecorder()
	(*Auth)(nil).HandleOperators(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// Must be a JSON array (even if empty), not null
	body := w.Body.String()
	if body == "null\n" || body == "null" {
		t.Error("expected [] not null")
	}
}

func TestHandleOperatorsWithID_InvalidRole(t *testing.T) {
	// 32-char dashless hex — valid Jellyfin ID format
	body, _ := json.Marshal(map[string]string{"role": "superadmin", "username": "alice"})
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/operators/aabbccddeeff00112233445566778899", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	(*Auth)(nil).HandleOperatorsWithID(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
