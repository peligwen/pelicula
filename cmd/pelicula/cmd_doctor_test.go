package main

import (
	"slices"
	"strings"
	"testing"
)

func TestIsSecretKey(t *testing.T) {
	for _, k := range []string{"WIREGUARD_PRIVATE_KEY", "JELLYFIN_PASSWORD", "WEBHOOK_SECRET", "GLUETUN_HTTP_PASS", "API_KEY", "AUTH_TOKEN"} {
		if !isSecretKey(k) {
			t.Errorf("%s should be treated as secret", k)
		}
	}
	for _, k := range []string{"CONFIG_DIR", "PUID", "TZ", "PELICULA_PORT", "SERVER_COUNTRIES", "GLUETUN_HTTP_USER", "JELLYFIN_ADMIN_USER"} {
		if isSecretKey(k) {
			t.Errorf("%s should not be treated as secret", k)
		}
	}
}

func TestSecretValues(t *testing.T) {
	env := fullEnv()
	got := secretValues(env)
	for _, k := range []string{"WIREGUARD_PRIVATE_KEY", "JELLYFIN_PASSWORD", "WEBHOOK_SECRET", "GLUETUN_HTTP_PASS"} {
		if !slices.Contains(got, env[k]) {
			t.Errorf("secretValues lacks the value of %s", k)
		}
	}
	for _, v := range []string{env["CONFIG_DIR"], env["TZ"], env["GLUETUN_HTTP_USER"]} {
		if slices.Contains(got, v) {
			t.Errorf("secretValues includes non-secret %q", v)
		}
	}
}

func TestRedactLiteralSecretValues(t *testing.T) {
	env := fullEnv()
	secrets := secretValues(env)
	in := "starting with key " + env["WIREGUARD_PRIVATE_KEY"] + " and password " + env["JELLYFIN_PASSWORD"] +
		"\nwebhook " + env["WEBHOOK_SECRET"] + " gluetun " + env["GLUETUN_HTTP_PASS"]
	got := redact(in, secrets)
	for _, k := range []string{"WIREGUARD_PRIVATE_KEY", "JELLYFIN_PASSWORD", "WEBHOOK_SECRET", "GLUETUN_HTTP_PASS"} {
		if strings.Contains(got, env[k]) {
			t.Errorf("%s value leaked:\n%s", k, got)
		}
	}
	if !strings.Contains(got, "starting with key") {
		t.Errorf("surrounding text must survive: %q", got)
	}
}

func TestRedactEnvAssignmentsByName(t *testing.T) {
	// No literal secrets supplied: the names alone must trigger redaction.
	cases := []struct {
		name, in string
		leaked   string
	}{
		{"wireguard key", "WIREGUARD_PRIVATE_KEY=yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=", "yAnz5TF"},
		{"jellyfin password", "JELLYFIN_PASSWORD=hunter2hunter2", "hunter2"},
		{"webhook secret", "environment: WEBHOOK_SECRET=s3cr3tvalue", "s3cr3tvalue"},
		{"gluetun pass", "GLUETUN_HTTP_PASS=abcdef123456", "abcdef123456"},
		{"lowercase", "password=letmein99", "letmein99"},
		{"token", "token=abc.def-ghi", "abc.def-ghi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redact(tc.in, nil)
			if strings.Contains(got, tc.leaked) {
				t.Errorf("leaked %q: %s", tc.leaked, got)
			}
			if !strings.Contains(got, "REDACTED") {
				t.Errorf("no REDACTED marker: %s", got)
			}
			if name := tc.in[:strings.Index(tc.in, "=")]; !strings.Contains(got, name[strings.LastIndex(name, " ")+1:]) {
				t.Errorf("key name must stay visible: %s", got)
			}
		})
	}
}

func TestRedactURLsHeadersAndJSON(t *testing.T) {
	cases := []struct{ name, in, leaked, kept string }{
		{
			"apikey in request url",
			"GET /api/v3/system/status?apikey=0123456789abcdef0123456789abcdef&page=1 HTTP/1.1",
			"0123456789abcdef", "page=1",
		},
		{
			"JSON-escaped url",
			`url":"http://sonarr:8989/api?x=1\u0026apikey=deadbeefdeadbeef\u0026y=2"`,
			"deadbeefdeadbeef", "x=1",
		},
		{"x-api-key header", "X-Api-Key: abcdef0123456789", "abcdef0123456789", "X-Api-Key"},
		{"authorization header", "Authorization: Bearer eyJhbGciOi.payload.sig", "eyJhbGciOi", "Authorization"},
		{"webhook header", "x-webhook-secret: topsecretvalue", "topsecretvalue", "x-webhook-secret"},
		{"emby header", `X-Emby-Authorization: MediaBrowser Client="a", Token="tok123456"`, "tok123456", "X-Emby-Authorization"},
		{"json password", `{"username":"admin","password":"p@ss w0rd!","other":"keep"}`, "p@ss w0rd!", `"other":"keep"`},
		{"json api key", `{"ApiKey":"abc123def456","Name":"x"}`, "abc123def456", `"Name":"x"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redact(tc.in, nil)
			if strings.Contains(got, tc.leaked) {
				t.Errorf("leaked %q: %s", tc.leaked, got)
			}
			if !strings.Contains(got, tc.kept) {
				t.Errorf("lost %q: %s", tc.kept, got)
			}
		})
	}
}

func TestRedactLeavesOrdinaryLogLinesAlone(t *testing.T) {
	lines := []string{
		"2026-04-22 12:00:00 info: Starting Sonarr",
		"PELICULA_PORT=7354",
		"CONFIG_DIR=/srv/config",
		"GET /api/health 200 3ms",
		"",
	}
	for _, l := range lines {
		if got := redact(l, secretValues(fullEnv())); got != l {
			t.Errorf("redact changed %q to %q", l, got)
		}
	}
}

func TestRedactIgnoresTinySecrets(t *testing.T) {
	// A 2-char "secret" would otherwise blank out ordinary words.
	if got := redact("do not touch this text", []string{"to", "no"}); got != "do not touch this text" {
		t.Errorf("tiny secrets must be ignored: %q", got)
	}
}

func TestParseInspectAndNeedsLogs(t *testing.T) {
	out := strings.Join([]string{
		"aaa|/pelicula-sonarr-1|running|healthy",
		"bbb|/pelicula-radarr-1|running|unhealthy",
		"ccc|/pelicula-jellyfin-1|exited|",
		"ddd|/pelicula-nginx-1|running|",
		"eee|/pelicula-gluetun-1|restarting|starting",
		"garbage line",
		"",
	}, "\n")
	cs := parseInspect(out)
	if len(cs) != 5 {
		t.Fatalf("parsed %d containers, want 5: %+v", len(cs), cs)
	}
	if cs[0].Name != "pelicula-sonarr-1" || cs[0].ID != "aaa" {
		t.Errorf("first container = %+v", cs[0])
	}
	want := []bool{false, true, true, false, true}
	for i, c := range cs {
		if c.needsLogs() != want[i] {
			t.Errorf("%s needsLogs = %v, want %v", c.Name, c.needsLogs(), want[i])
		}
	}
}
