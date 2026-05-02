package jellyfin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jfclient "pelicula-api/internal/clients/jellyfin"
)

// ── TestGet_PassesContext ────────────────────────────────────────────────────

// TestGet_PassesContext verifies that a cancelled context aborts an in-flight
// Get call and the error wraps context.Canceled or context.DeadlineExceeded.
func TestGet_PassesContext(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the client's context is cancelled.
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	c := jfclient.NewWithHTTPClient(srv.URL, srv.Client())
	_, err := c.Get(ctx, "/ping", "")
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v; want context.Canceled or context.DeadlineExceeded", err)
	}
}

// ── TestPost_HTTPError_StatusPreserved ───────────────────────────────────────

// TestPost_HTTPError_StatusPreserved checks that a 4xx/5xx HTTP response
// returns a typed *HTTPError with the correct status code.
func TestPost_HTTPError_StatusPreserved(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"Message":"unauthorized"}`, http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	c := jfclient.NewWithHTTPClient(srv.URL, srv.Client())
	_, err := c.Post(context.Background(), "/Users/AuthenticateByName", "", map[string]string{"Username": "x", "Pw": "y"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var httpErr *jfclient.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error type = %T, want *jfclient.HTTPError", err)
	}
	if httpErr.StatusCode != http.StatusUnauthorized {
		t.Errorf("StatusCode = %d, want 401", httpErr.StatusCode)
	}
}

// ── TestAuthenticateByName_Success ───────────────────────────────────────────

// TestAuthenticateByName_Success verifies the happy-path JSON parsing and
// AuthResult field mapping.
func TestAuthenticateByName_Success(t *testing.T) {
	t.Parallel()

	const wantToken = "abc123token"
	const wantUserID = "deadbeef-dead-beef-dead-beefdeadbeef"
	const wantUsername = "alice"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"AccessToken": wantToken,
			"User": map[string]any{
				"Id":   wantUserID,
				"Name": wantUsername,
				"Policy": map[string]any{
					"IsAdministrator": true,
				},
			},
		})
	}))
	t.Cleanup(srv.Close)

	c := jfclient.NewWithHTTPClient(srv.URL, srv.Client())
	result, err := c.AuthenticateByName(context.Background(), "alice", "password")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.AccessToken != wantToken {
		t.Errorf("AccessToken = %q, want %q", result.AccessToken, wantToken)
	}
	if result.UserID != wantUserID {
		t.Errorf("UserID = %q, want %q", result.UserID, wantUserID)
	}
	if result.Username != wantUsername {
		t.Errorf("Username = %q, want %q", result.Username, wantUsername)
	}
	if !result.IsAdministrator {
		t.Error("IsAdministrator should be true")
	}
}

// ── TestAuthenticateByName_IncompleteResponse ────────────────────────────────

// TestAuthenticateByName_IncompleteResponse checks that a response missing
// UserID (or AccessToken) returns the "incomplete" sentinel error.
func TestAuthenticateByName_IncompleteResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Valid JSON but missing both AccessToken and User.Id.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"AccessToken": "",
			"User":        map[string]any{"Id": "", "Name": "nobody"},
		})
	}))
	t.Cleanup(srv.Close)

	c := jfclient.NewWithHTTPClient(srv.URL, srv.Client())
	_, err := c.AuthenticateByName(context.Background(), "nobody", "pass")
	if err == nil {
		t.Fatal("expected error for incomplete response, got nil")
	}
	if !strings.Contains(err.Error(), "incomplete Jellyfin auth response") {
		t.Errorf("error = %q, want mention of 'incomplete Jellyfin auth response'", err.Error())
	}
}

// ── TestEmbyAuthHeader_TokenAppended ────────────────────────────────────────

// TestEmbyAuthHeader_TokenAppended verifies that the X-Emby-Authorization
// header contains Token="..." when a token is provided, and no Token segment
// when it is empty.
func TestEmbyAuthHeader_TokenAppended(t *testing.T) {
	t.Parallel()

	var capturedHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHeader = r.Header.Get("X-Emby-Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c := jfclient.NewWithHTTPClient(srv.URL, srv.Client())

	// With token.
	const token = "mytoken123"
	capturedHeader = ""
	if _, err := c.Get(context.Background(), "/test", token); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(capturedHeader, `Token="mytoken123"`) {
		t.Errorf("header with token = %q, want Token= segment", capturedHeader)
	}

	// Without token.
	capturedHeader = ""
	if _, err := c.Get(context.Background(), "/test", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(capturedHeader, "Token=") {
		t.Errorf("header without token = %q, should not contain Token= segment", capturedHeader)
	}
}

// ── TestExtractMessage_Truncates ─────────────────────────────────────────────

// TestExtractMessage_Truncates verifies that a body with a "Message" field
// longer than 120 chars is truncated to 120 chars in the extracted string.
func TestExtractMessage_Truncates(t *testing.T) {
	t.Parallel()

	longMsg := strings.Repeat("x", 200)
	body, _ := json.Marshal(map[string]string{"Message": longMsg})

	got := jfclient.ExtractMessage(body)
	if len(got) != 120 {
		t.Errorf("len(ExtractMessage) = %d, want 120", len(got))
	}
	if got != longMsg[:120] {
		t.Errorf("truncated message mismatch")
	}
}
