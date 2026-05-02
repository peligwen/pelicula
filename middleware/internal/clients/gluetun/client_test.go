package gluetun

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pelicula-api/internal/httpx"
)

func TestGetPublicIP_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"public_ip":"1.2.3.4","country":"Netherlands"}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := New(srv.URL, "", "")
	got, err := c.GetPublicIP(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.PublicIP != "1.2.3.4" {
		t.Errorf("expected public_ip 1.2.3.4, got %q", got.PublicIP)
	}
	if got.Country != "Netherlands" {
		t.Errorf("expected country Netherlands, got %q", got.Country)
	}
}

func TestGetForwardedPort_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"port":12345}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := New(srv.URL, "", "")
	got, err := c.GetForwardedPort(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Port != 12345 {
		t.Errorf("expected port 12345, got %d", got.Port)
	}
}

func TestRetryOn5xx(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"public_ip":"9.9.9.9","country":"US"}`)) //nolint:errcheck
	}))
	defer srv.Close()

	c := New(srv.URL, "", "")
	c.base.Retry.Delay = 1 * time.Millisecond

	_, err := c.GetPublicIP(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if attempts < 3 {
		t.Errorf("expected at least 3 attempts (retry on 5xx), got %d", attempts)
	}
}

func TestBasicAuth_HeaderSent(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"public_ip":"1.1.1.1","country":"US"}`)) //nolint:errcheck
	}))
	defer srv.Close()

	t.Run("with password", func(t *testing.T) {
		gotAuth = ""
		c := New(srv.URL, "admin", "secret")
		if _, err := c.GetPublicIP(context.Background()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:secret"))
		if gotAuth != expected {
			t.Errorf("expected Authorization %q, got %q", expected, gotAuth)
		}
	})

	t.Run("without password", func(t *testing.T) {
		gotAuth = ""
		c := New(srv.URL, "", "")
		if _, err := c.GetPublicIP(context.Background()); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if gotAuth != "" {
			t.Errorf("expected no Authorization header, got %q", gotAuth)
		}
	})
}

func TestUserAgentSet(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"public_ip":"1.1.1.1","country":"US"}`)) //nolint:errcheck
	}))
	defer srv.Close()

	prev := httpx.DefaultUserAgent
	t.Cleanup(func() { httpx.DefaultUserAgent = prev })
	httpx.DefaultUserAgent = "Pelicula/test"

	c := New(srv.URL, "user", "pass")
	if _, err := c.GetPublicIP(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(gotUA, "Pelicula/") {
		t.Errorf("expected User-Agent to start with Pelicula/, got %q", gotUA)
	}
}
