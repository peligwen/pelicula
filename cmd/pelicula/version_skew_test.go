package main

import (
	"errors"
	"strings"
	"testing"
)

func TestExtractCommit(t *testing.T) {
	cases := []struct{ in, want string }{
		{"887fd50", "887fd50"},                      // bare short hash (untagged repo)
		{"887fd50-dirty", "887fd50"},                // bare hash, dirty tree
		{"v1.2-47-g1304ae3", "1304ae3"},             // tag-relative describe
		{"v1.2-47-g1304ae3-dirty", "1304ae3"},       // tag-relative, dirty
		{"v1.2", "v1.2"},                            // tag-exact — rev-list resolves tags
		{"v1.2-dirty", "v1.2"},                      // tag-exact, dirty
		{"2.0-rc1-3-gdeadbeefcafe", "deadbeefcafe"}, // longer hash
	}
	for _, tc := range cases {
		if got := extractCommit(tc.in); got != tc.want {
			t.Errorf("extractCommit(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestClassifySkew(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name       string
		label      string
		labelErr   error
		behind     int
		behindErr  error
		dirty      bool
		wantStatus skewStatus
		wantRedep  bool
	}{
		{name: "image missing", labelErr: boom, wantStatus: skewMissing, wantRedep: false},
		{name: "no label", label: "", wantStatus: skewUnstamped, wantRedep: true},
		{name: "dev stamp", label: "dev", wantStatus: skewUnstamped, wantRedep: true},
		{name: "unknown commit", label: "aaaaaaa", behindErr: boom, wantStatus: skewUnknown, wantRedep: false},
		{name: "stale", label: "887fd50", behind: 12, wantStatus: skewStale, wantRedep: true},
		{name: "stale wins over dirty", label: "887fd50", behind: 3, dirty: true, wantStatus: skewStale, wantRedep: true},
		{name: "dirty only", label: "1304ae3", dirty: true, wantStatus: skewDirty, wantRedep: true},
		{name: "current", label: "1304ae3", wantStatus: skewCurrent, wantRedep: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := classifySkew("pelicula-api", "middleware", tc.label, tc.labelErr, tc.behind, tc.behindErr, tc.dirty)
			if r.Status != tc.wantStatus {
				t.Errorf("status = %v, want %v", r.Status, tc.wantStatus)
			}
			if r.needsRedeploy() != tc.wantRedep {
				t.Errorf("needsRedeploy = %v, want %v", r.needsRedeploy(), tc.wantRedep)
			}
			// Every verdict must be renderable and name the service.
			if v := r.verdict(); !strings.Contains(v, "pelicula-api") {
				t.Errorf("verdict %q does not name the service", v)
			}
		})
	}
}

func TestSkewVerdictWording(t *testing.T) {
	stale := classifySkew("pelicula-api", "middleware", "887fd50", nil, 12, nil, false)
	if v := stale.verdict(); !strings.Contains(v, "12 commits behind middleware/") || !strings.Contains(v, "pelicula redeploy") {
		t.Errorf("stale verdict missing count or remedy: %q", v)
	}

	one := classifySkew("procula", "procula", "887fd50", nil, 1, nil, false)
	if v := one.verdict(); !strings.Contains(v, "1 commit behind") || strings.Contains(v, "commits behind") {
		t.Errorf("singular verdict wrong: %q", v)
	}

	current := classifySkew("procula", "procula", "1304ae3", nil, 0, nil, false)
	if v := current.verdict(); !strings.Contains(v, "up to date") || strings.Contains(v, "redeploy") {
		t.Errorf("current verdict should not suggest redeploy: %q", v)
	}
}
