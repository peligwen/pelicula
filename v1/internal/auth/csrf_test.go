package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCSRF(t *testing.T) {
	h := CSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	cases := []struct {
		name    string
		method  string
		host    string
		headers map[string]string
		want    int
	}{
		{"same-host origin", "POST", "pelicula.lan:7354", map[string]string{"Origin": "http://pelicula.lan:7354"}, 204},
		{"same-host origin, case differs", "POST", "Pelicula.LAN:7354", map[string]string{"Origin": "http://pelicula.lan:7354"}, 204},
		{"foreign origin", "POST", "pelicula.lan:7354", map[string]string{"Origin": "http://evil.example"}, 403},
		{"same hostname other port", "POST", "pelicula.lan:7354", map[string]string{"Origin": "http://pelicula.lan:8080"}, 403},
		{"no origin or referer", "POST", "pelicula.lan:7354", nil, 204},
		{"null origin", "DELETE", "pelicula.lan:7354", map[string]string{"Origin": "null"}, 403},
		{"referer same host", "PUT", "pelicula.lan:7354", map[string]string{"Referer": "http://pelicula.lan:7354/settings"}, 204},
		{"referer foreign", "PUT", "pelicula.lan:7354", map[string]string{"Referer": "https://evil.example/x"}, 403},
		{"origin wins over referer", "PATCH", "pelicula.lan:7354", map[string]string{"Origin": "http://evil.example", "Referer": "http://pelicula.lan:7354/"}, 403},
		{"forwarded host wins (allowed)", "POST", "pelicula:8181", map[string]string{"Origin": "http://pelicula.lan:7354", "X-Forwarded-Host": "pelicula.lan:7354"}, 204},
		{"forwarded host wins (blocked)", "POST", "pelicula.lan:7354", map[string]string{"Origin": "http://pelicula.lan:7354", "X-Forwarded-Host": "other.lan"}, 403},
		{"forwarded host list uses first", "POST", "pelicula:8181", map[string]string{"Origin": "http://pelicula.lan:7354", "X-Forwarded-Host": "pelicula.lan:7354, proxy.internal"}, 204},
		{"safe method ignores foreign origin", "GET", "pelicula.lan:7354", map[string]string{"Origin": "http://evil.example"}, 204},
		{"head ignores foreign origin", "HEAD", "pelicula.lan:7354", map[string]string{"Origin": "http://evil.example"}, 204},
		{"unparseable origin", "POST", "pelicula.lan:7354", map[string]string{"Origin": "://bad"}, 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "/api/anything", nil)
			req.Host = tc.host
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if tc.want == 403 {
				errorBody(t, rec)
			}
		})
	}
}

func TestCSRFWrapsAuthRoutes(t *testing.T) {
	e := newEnv(t)
	h := CSRF(e.h)

	body := `{"username":"bob","password":"bob-pass"}`
	post := func(origin string) int {
		req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(body))
		req.Host = "pelicula.lan:7354"
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := post("http://pelicula.lan:7354"); got != 200 {
		t.Errorf("same origin login = %d", got)
	}
	if got := post("http://evil.example"); got != 403 {
		t.Errorf("foreign origin login = %d", got)
	}
	if got := post(""); got != 200 {
		t.Errorf("no origin login = %d", got)
	}
}
