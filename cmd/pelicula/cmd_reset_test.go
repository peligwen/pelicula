package main

import (
	"bytes"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseResetArgs(t *testing.T) {
	cases := []struct {
		args    []string
		target  string
		yes     bool
		wantErr bool
	}{
		{[]string{"sonarr"}, "sonarr", false, false},
		{[]string{"all", "--yes"}, "all", true, false},
		{[]string{"--yes", "all"}, "all", true, false},
		{[]string{"-y", "jellyfin"}, "jellyfin", true, false},
		{nil, "", false, true},
		{[]string{"--yes"}, "", false, true},
		{[]string{"sonarr", "radarr"}, "", false, true},
		{[]string{"--force", "all"}, "", false, true},
	}
	for _, tc := range cases {
		target, yes, err := parseResetArgs(tc.args)
		if (err != nil) != tc.wantErr {
			t.Errorf("%v: err = %v, wantErr %v", tc.args, err, tc.wantErr)
			continue
		}
		if err == nil && (target != tc.target || yes != tc.yes) {
			t.Errorf("%v: got (%q, %v), want (%q, %v)", tc.args, target, yes, tc.target, tc.yes)
		}
	}
}

func TestResetDirs(t *testing.T) {
	all, err := resetDirs("/srv/config", "/home/u", "all")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range all {
		if filepath.Dir(d) != "/srv/config" {
			t.Errorf("%s is not directly under CONFIG_DIR", d)
		}
		names = append(names, filepath.Base(d))
	}
	if !slices.Equal(names, []string{"sonarr", "radarr", "prowlarr", "qbittorrent", "jellyfin", "pelicula", "gluetun"}) {
		t.Errorf("all = %v", names)
	}

	one, err := resetDirs("/srv/config/", "/home/u", "radarr")
	if err != nil || !slices.Equal(one, []string{"/srv/config/radarr"}) {
		t.Errorf("radarr = %v, %v", one, err)
	}

	for _, bad := range []string{"", "/", "/home/u", "/home/u/", "relative/config"} {
		if dirs, err := resetDirs(bad, "/home/u", "all"); err == nil {
			t.Errorf("CONFIG_DIR %q accepted: %v", bad, dirs)
		}
	}
	for _, target := range []string{"nginx", "", "../etc", "bazarr", "procula-jobs"} {
		if dirs, err := resetDirs("/srv/config", "/home/u", target); err == nil {
			t.Errorf("target %q accepted: %v", target, dirs)
		}
	}
}

func TestConfirm(t *testing.T) {
	for in, want := range map[string]bool{
		"y\n": true, "Y\n": true, "yes\n": true, "YES\n": true, " y \n": true,
		"n\n": false, "\n": false, "": false, "yep\n": false, "reset\n": false,
	} {
		var out bytes.Buffer
		if got := confirm(strings.NewReader(in), &out, "Continue?"); got != want {
			t.Errorf("confirm(%q) = %v, want %v", in, got, want)
		}
		if !strings.Contains(out.String(), "Continue? [y/N]") {
			t.Errorf("prompt = %q", out.String())
		}
	}
}

func TestResetNotesCoverEveryDir(t *testing.T) {
	for _, d := range configSubdirs {
		if resetNotes[d] == "" {
			t.Errorf("no reset note for %s", d)
		}
	}
}
