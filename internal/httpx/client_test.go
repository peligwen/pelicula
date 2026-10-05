package httpx

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fast(c *Client) *Client {
	c.Retry = RetryPolicy{MaxAttempts: 3, Delay: time.Millisecond}
	return c
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGetJSON_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v3/thing" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		if got := r.Header.Get("X-Api-Key"); got != "k3y" {
			t.Errorf("X-Api-Key = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "pelicula" {
			t.Errorf("User-Agent = %q", got)
		}
		w.Write([]byte(`{"name":"x","n":3}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/", "k3y", "X-Api-Key", time.Second) // trailing slash is trimmed
	var out struct {
		Name string `json:"name"`
		N    int    `json:"n"`
	}
	if err := c.GetJSON(context.Background(), "/v3/thing", &out); err != nil {
		t.Fatal(err)
	}
	if out.Name != "x" || out.N != 3 {
		t.Fatalf("out = %+v", out)
	}
}

func TestGetJSON_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not json`))
	}))
	defer srv.Close()
	err := New(srv.URL, "", "", time.Second).GetJSON(context.Background(), "/", &struct{}{})
	if err == nil || !strings.Contains(err.Error(), "decode JSON") {
		t.Fatalf("err = %v", err)
	}
}

func TestSetAPIKeyAndScheme(t *testing.T) {
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("Authorization"))
	}))
	defer srv.Close()

	c := New(srv.URL, "", "Authorization", time.Second)
	c.KeyScheme = "Bearer"
	if _, err := c.RawGet(context.Background(), "/"); err != nil {
		t.Fatal(err)
	}
	if got.Load() != "" {
		t.Fatalf("empty key must not set a header, got %q", got.Load())
	}
	c.SetAPIKey("abc")
	if _, err := c.RawGet(context.Background(), "/"); err != nil {
		t.Fatal(err)
	}
	if got.Load() != "Bearer abc" {
		t.Fatalf("Authorization = %q", got.Load())
	}
}

func TestRawGet_4xxReturnsBodyAndHTTPError(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, `{"error":"nope"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	body, err := fast(New(srv.URL, "", "", time.Second)).RawGet(context.Background(), "/missing")
	var he *HTTPError
	if !errors.As(err, &he) || he.StatusCode != 404 {
		t.Fatalf("err = %v, want HTTPError 404", err)
	}
	if !strings.Contains(string(body), "nope") || !strings.Contains(he.Body, "nope") {
		t.Fatalf("body = %q, he.Body = %q", body, he.Body)
	}
	if !strings.Contains(err.Error(), "GET /missing") || !strings.Contains(err.Error(), "404") {
		t.Fatalf("error text = %q", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("4xx must not retry, hits = %d", hits.Load())
	}
}

func TestRetryOn5xxThenSuccess(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte(`ok`))
	}))
	defer srv.Close()

	b, err := fast(New(srv.URL, "", "", time.Second)).RawGet(context.Background(), "/")
	if err != nil || string(b) != "ok" {
		t.Fatalf("b=%q err=%v", b, err)
	}
	if hits.Load() != 3 {
		t.Fatalf("hits = %d, want 3", hits.Load())
	}
}

func TestRetryExhausted5xx(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := fast(New(srv.URL, "", "", time.Second)).Delete(context.Background(), "/x")
	var he *HTTPError
	if !errors.As(err, &he) || he.StatusCode != 500 {
		t.Fatalf("err = %v", err)
	}
	if hits.Load() != 3 {
		t.Fatalf("hits = %d, want 3", hits.Load())
	}
}

func TestPost5xxIsRetried(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"a":1}` {
			t.Errorf("body on attempt %d = %q", hits.Load()+1, b)
		}
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"id":7}`))
	}))
	defer srv.Close()

	var out struct{ ID int }
	err := fast(New(srv.URL, "", "", time.Second)).PostJSON(context.Background(), "/add", map[string]int{"a": 1}, &out)
	if err != nil || out.ID != 7 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if hits.Load() != 2 {
		t.Fatalf("hits = %d", hits.Load())
	}
}

// Transport-error retry rules: POST only retries dial failures; GET retries
// anything but cancellation/timeouts.
func TestTransportErrorRetryRules(t *testing.T) {
	dialErr := &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	resetErr := &net.OpError{Op: "read", Err: errors.New("connection reset by peer")}

	cases := []struct {
		name   string
		call   func(c *Client) error
		err    error
		wanted int32
	}{
		{"GET dial error", func(c *Client) error { _, err := c.RawGet(context.Background(), "/"); return err }, dialErr, 3},
		{"GET reset error", func(c *Client) error { _, err := c.RawGet(context.Background(), "/"); return err }, resetErr, 3},
		{"PUT reset error", func(c *Client) error { _, err := c.PutJSON(context.Background(), "/", 1); return err }, resetErr, 3},
		{"POST dial error", func(c *Client) error { _, err := c.RawPost(context.Background(), "/", 1); return err }, dialErr, 3},
		{"POST reset error", func(c *Client) error { _, err := c.RawPost(context.Background(), "/", 1); return err }, resetErr, 1},
		{"POST form reset error", func(c *Client) error {
			_, err := c.PostForm(context.Background(), "/", url.Values{"a": {"b"}})
			return err
		}, resetErr, 1},
		{"GET deadline", func(c *Client) error { _, err := c.RawGet(context.Background(), "/"); return err }, context.DeadlineExceeded, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			c := fast(New("http://upstream.invalid", "", "", time.Second))
			c.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				return nil, &url.Error{Op: r.Method, URL: r.URL.String(), Err: tc.err}
			})}
			if err := tc.call(c); err == nil {
				t.Fatal("expected error")
			}
			if calls.Load() != tc.wanted {
				t.Fatalf("calls = %d, want %d", calls.Load(), tc.wanted)
			}
		})
	}
}

func TestContextCancelStopsRetry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, "", "", time.Second)
	c.Retry = RetryPolicy{MaxAttempts: 5, Delay: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for hits.Load() == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	_, err := c.RawGet(ctx, "/")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d", hits.Load())
	}
}

func TestPutDeleteAndPostForm(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch r.Method + " " + r.URL.Path {
		case "PUT /thing/1":
			if r.Header.Get("Content-Type") != "application/json" || string(b) != `{"n":2}` {
				t.Errorf("PUT ct=%q body=%q", r.Header.Get("Content-Type"), b)
			}
			w.Write([]byte(`{"id":1}`))
		case "DELETE /thing/1":
			w.WriteHeader(http.StatusNoContent)
		case "POST /form":
			if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Errorf("form ct = %q", r.Header.Get("Content-Type"))
			}
			if v, err := url.ParseQuery(string(b)); err != nil || v.Get("hashes") != "abc" {
				t.Errorf("form = %q (%v)", b, err)
			}
			w.Write([]byte("Ok."))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, "", "", time.Second)
	ctx := context.Background()
	if b, err := c.PutJSON(ctx, "/thing/1", map[string]int{"n": 2}); err != nil || string(b) != `{"id":1}` {
		t.Fatalf("PutJSON b=%q err=%v", b, err)
	}
	if err := c.Delete(ctx, "/thing/1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if b, err := c.PostForm(ctx, "/form", url.Values{"hashes": {"abc"}}); err != nil || string(b) != "Ok." {
		t.Fatalf("PostForm b=%q err=%v", b, err)
	}
	if err := c.Delete(ctx, "/nope"); err == nil {
		t.Fatal("expected 404 error from Delete")
	}
}

func TestPostNilBodyHasNoBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if len(b) != 0 || r.Header.Get("Content-Type") != "" {
			t.Errorf("body=%q ct=%q", b, r.Header.Get("Content-Type"))
		}
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "", "", time.Second).RawPost(context.Background(), "/x", nil); err != nil {
		t.Fatal(err)
	}
}

func TestProbe(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/ok" {
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := fast(New(srv.URL, "", "", time.Second))
	if err := c.Probe(context.Background(), "/ok"); err != nil {
		t.Fatal(err)
	}
	err := c.Probe(context.Background(), "/down")
	var he *HTTPError
	if !errors.As(err, &he) || he.StatusCode != 503 {
		t.Fatalf("err = %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("Probe must not retry, hits = %d", hits.Load())
	}
}

func TestRedaction(t *testing.T) {
	const secret = "supersecret-key-123"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key "+secret, http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := fast(New(srv.URL, secret, "X-Api-Key", time.Second))
	_, err := c.RawGet(context.Background(), "/q?apikey="+secret+"&term=hello")
	if err == nil {
		t.Fatal("expected error")
	}
	var he *HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(he.Body, secret) {
		t.Fatalf("secret leaked: %q / %q", err, he.Body)
	}
	if !strings.Contains(err.Error(), "term=hello") || !strings.Contains(err.Error(), "apikey=REDACTED") {
		t.Fatalf("expected redacted query in %q", err)
	}

	// Transport errors must not carry the raw URL either.
	srv.Close()
	c.Retry.MaxAttempts = 1
	_, err = c.RawGet(context.Background(), "/q?token="+secret)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("transport error leaked secret: %v", err)
	}
}

func TestRedactPath(t *testing.T) {
	cases := map[string]string{
		"/plain":                 "/plain",
		"/a?x=1":                 "/a?x=1",
		"/a?ApiKey=zzz&x=1":      "/a?ApiKey=REDACTED&x=1",
		"/a?password=p&secret=s": "/a?password=REDACTED&secret=REDACTED",
	}
	for in, want := range cases {
		if got := redactPath(in); got != want {
			t.Errorf("redactPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHTTPErrorString(t *testing.T) {
	if got := (&HTTPError{StatusCode: 500}).Error(); got != "HTTP 500" {
		t.Errorf("got %q", got)
	}
	long := &HTTPError{StatusCode: 400, Body: strings.Repeat("x", 500)}
	if len(long.Error()) > 220 {
		t.Errorf("error text not truncated: %d chars", len(long.Error()))
	}
}
