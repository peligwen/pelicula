package main

import "testing"

// TestFilterErrorLines verifies that filterErrorLines correctly matches lines
// containing error-like keywords. Matching is by substring (case-insensitive),
// so "errored" also matches — this is expected behavior given the regex used.
func TestFilterErrorLines(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantLen  int
		wantNone bool
	}{
		{
			name:     "clean log — no matches",
			input:    "INFO starting service\nINFO ready\nDEBUG poll tick",
			wantLen:  0,
			wantNone: true,
		},
		{
			name:    "lowercase error",
			input:   "2026-04-22 some error occurred",
			wantLen: 1,
		},
		{
			name:    "uppercase ERROR",
			input:   "2026-04-22 [ERROR] connection refused",
			wantLen: 1,
		},
		{
			name:    "Warning (mixed case)",
			input:   "Warning: disk usage high",
			wantLen: 1,
		},
		{
			name:    "Fatal",
			input:   "Fatal: could not bind port",
			wantLen: 1,
		},
		{
			name:    "PANIC",
			input:   "PANIC: nil pointer dereference",
			wantLen: 1,
		},
		{
			name: "substring errored also matches",
			// "errored" contains "error" — substring matching is the documented behavior.
			input:   "request errored after timeout",
			wantLen: 1,
		},
		{
			name:     "empty input — empty slice",
			input:    "",
			wantLen:  0,
			wantNone: true,
		},
		{
			name:    "multiple matching lines",
			input:   "INFO ok\nERROR boom\nWARNING: disk low\nINFO done",
			wantLen: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterErrorLines([]byte(tc.input))
			if tc.wantNone && got != nil && len(got) != 0 {
				t.Errorf("expected empty/nil slice, got %v", got)
			}
			if len(got) != tc.wantLen {
				t.Errorf("expected %d lines, got %d: %v", tc.wantLen, len(got), got)
			}
		})
	}
}

// TestScrubSecrets verifies credential values are redacted from doctor log
// output. The realistic cases are verbatim shapes from a production doctor
// run: Sonarr/Prowlarr echo the Prowlarr API key in request URLs (plain and
// JSON-escaped), while Radarr self-redacts.
func TestScrubSecrets(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "sonarr query param",
			in:   `[Warn] HttpClient: HTTP Error - Res: HTTP/1.1 [GET] http://gluetun:9696/prowlarr/5/api?t=tvsearch&cat=5000,5070&extended=1&apikey=708ee94b4e9b401fbfb52b7764cda65c&offset=0&limit=100: 429.TooManyRequests (142 bytes)`,
			want: `[Warn] HttpClient: HTTP Error - Res: HTTP/1.1 [GET] http://gluetun:9696/prowlarr/5/api?t=tvsearch&cat=5000,5070&extended=1&apikey=REDACTED&offset=0&limit=100: 429.TooManyRequests (142 bytes)`,
		},
		{
			name: "prowlarr JSON-escaped ampersand (no word boundary before key)",
			in:   `"errorMessage": "HTTP request failed: [429:TooManyRequests] [GET] at [http://gluetun:9696/prowlarr/5/api?t=caps&apikey=708ee94b4e9b401fbfb52b7764cda65c]",`,
			want: `"errorMessage": "HTTP request failed: [429:TooManyRequests] [GET] at [http://gluetun:9696/prowlarr/5/api?t=caps&apikey=REDACTED]",`,
		},
		{
			// Radarr redacts its own keys; a value opening with "(" is
			// deliberately not matched so "(removed)" survives intact.
			name: "radarr already self-redacted stays untouched",
			in:   `[Warn] HttpClient: HTTP Error - Res: HTTP/1.1 [GET] http://gluetun:9696/prowlarr/5/api?t=caps&apikey=(removed) 429.TooManyRequests (142 bytes)`,
			want: `[Warn] HttpClient: HTTP Error - Res: HTTP/1.1 [GET] http://gluetun:9696/prowlarr/5/api?t=caps&apikey=(removed) 429.TooManyRequests (142 bytes)`,
		},
		{
			name: "x-api-key header",
			in:   `ERROR request failed X-Api-Key: 708ee94b4e9b401fbfb52b7764cda65c status=502`,
			want: `ERROR request failed X-Api-Key: REDACTED status=502`,
		},
		{
			name: "json header form",
			in:   `WARN {"X-Api-Key": "708ee94b4e9b401fbfb52b7764cda65c"}`,
			want: `WARN {"X-Api-Key": "REDACTED"}`,
		},
		{
			name: "tracker passkey in URL",
			in:   `error announcing to https://tracker.example/announce?passkey=deadbeefcafe1234&event=started`,
			want: `error announcing to https://tracker.example/announce?passkey=REDACTED&event=started`,
		},
		{
			name: "no credentials — unchanged",
			in:   `[Warn] Torznab: Unable to connect to indexer`,
			want: `[Warn] Torznab: Unable to connect to indexer`,
		},
		{
			name: "value stops at closing bracket",
			in:   `at [http://gluetun:9696/api?apikey=708ee94b4e9b401fbfb52b7764cda65c]`,
			want: `at [http://gluetun:9696/api?apikey=REDACTED]`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scrubSecrets(tc.in); got != tc.want {
				t.Errorf("scrubSecrets mismatch\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}
