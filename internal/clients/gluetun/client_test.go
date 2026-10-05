package gluetun

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"pelicula/internal/httpx"
)

func server(t *testing.T, user, pass string, routes map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pass != "" {
			u, p, ok := r.BasicAuth()
			if !ok || u != user || p != pass {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		} else if r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected Authorization header %q", r.Header.Get("Authorization"))
		}
		body, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGetPublicIP(t *testing.T) {
	srv := server(t, "pelicula", "pw", map[string]string{
		"/v1/publicip/ip": `{"public_ip":"1.2.3.4","region":"x","country":"Netherlands","city":"Amsterdam"}`,
	})
	got, err := New(srv.URL, "pelicula", "pw").GetPublicIP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if *got != (VPNStatus{PublicIP: "1.2.3.4", Country: "Netherlands", City: "Amsterdam"}) {
		t.Fatalf("got %+v", *got)
	}
}

func TestGetForwardedPort(t *testing.T) {
	srv := server(t, "u", "p", map[string]string{"/v1/portforward": `{"port":51413}`})
	port, err := New(srv.URL, "u", "p").GetForwardedPort(context.Background())
	if err != nil || port != 51413 {
		t.Fatalf("port=%d err=%v", port, err)
	}

	srv = server(t, "u", "p", map[string]string{"/v1/portforward": `{"port":0}`})
	if port, err := New(srv.URL, "u", "p").GetForwardedPort(context.Background()); err != nil || port != 0 {
		t.Fatalf("inactive: port=%d err=%v", port, err)
	}
}

func TestGetTunnelStatus_UsesProtocolAgnosticRoute(t *testing.T) {
	srv := server(t, "", "", map[string]string{
		"/v1/vpn/status":     `{"status":"running"}`,
		"/v1/openvpn/status": `{"status":"stopped"}`, // legacy route must not be used
	})
	got, err := New(srv.URL, "", "").GetTunnelStatus(context.Background())
	if err != nil || got != "running" {
		t.Fatalf("status=%q err=%v", got, err)
	}
}

func TestPing(t *testing.T) {
	srv := server(t, "u", "p", map[string]string{"/v1/vpn/status": `{"status":"running"}`})
	if err := New(srv.URL, "u", "p").Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBadCredentialsReturnHTTPError(t *testing.T) {
	srv := server(t, "u", "right", map[string]string{"/v1/vpn/status": `{"status":"running"}`})
	c := New(srv.URL, "u", "wrong")

	for name, err := range map[string]error{
		"Ping":   c.Ping(context.Background()),
		"Status": func() error { _, err := c.GetTunnelStatus(context.Background()); return err }(),
		"IP":     func() error { _, err := c.GetPublicIP(context.Background()); return err }(),
		"Port":   func() error { _, err := c.GetForwardedPort(context.Background()); return err }(),
	} {
		var he *httpx.HTTPError
		if !errors.As(err, &he) || he.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestMalformedBody(t *testing.T) {
	srv := server(t, "", "", map[string]string{"/v1/publicip/ip": `<html>`})
	if _, err := New(srv.URL, "", "").GetPublicIP(context.Background()); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestUnreachable(t *testing.T) {
	if err := New("http://127.0.0.1:1", "", "").Ping(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}
