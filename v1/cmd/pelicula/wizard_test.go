package main

import (
	"bytes"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var testPlat = Platform{
	OS: "linux", UID: 1001, GID: 1002, TZ: "Europe/Amsterdam",
	DefaultConfigDir:  "/repo/config",
	DefaultLibraryDir: "/home/u/media",
	DefaultWorkDir:    "/home/u/media",
}

var validWGKey = strings.Repeat("A", 43) + "="

func runWizardWith(input string) (EnvMap, string) {
	var out bytes.Buffer
	m := runWizard(strings.NewReader(input), &out, testPlat, "/repo", "/home/u")
	return m, out.String()
}

func TestWizardAllDefaults(t *testing.T) {
	// Five Enters: three paths, then the VPN key (skip), and a spare.
	m, out := runWizardWith("\n\n\n\n\n")

	for k, want := range map[string]string{
		"CONFIG_DIR":            "/repo/config",
		"LIBRARY_DIR":           "/home/u/media",
		"WORK_DIR":              "/home/u/media",
		"PUID":                  "1001",
		"PGID":                  "1002",
		"TZ":                    "Europe/Amsterdam",
		"SERVER_COUNTRIES":      "Netherlands",
		"PELICULA_PORT":         "7354",
		"JELLYFIN_ADMIN_USER":   "admin",
		"GLUETUN_HTTP_USER":     "pelicula",
		"WIREGUARD_PRIVATE_KEY": "",
	} {
		if m[k] != want {
			t.Errorf("%s = %q, want %q", k, m[k], want)
		}
	}
	alnum := regexp.MustCompile(`^[A-Za-z0-9]+$`)
	for k, n := range map[string]int{"JELLYFIN_PASSWORD": 16, "WEBHOOK_SECRET": 32, "GLUETUN_HTTP_PASS": 32} {
		if len(m[k]) != n || !alnum.MatchString(m[k]) {
			t.Errorf("%s = %q, want %d alphanumeric characters", k, m[k], n)
		}
	}

	// The prompts carry the VPN caveats the user must see.
	for _, want := range []string{"ProtonVPN", "Plus", "Moderate NAT", "[/repo/config]", "[/home/u/media]"} {
		if !strings.Contains(out, want) {
			t.Errorf("wizard output lacks %q:\n%s", want, out)
		}
	}
	// Without a key there is no countries question.
	if strings.Contains(out, "server countries") {
		t.Error("countries must only be asked when a key is given")
	}
}

func TestWizardCustomAnswers(t *testing.T) {
	input := strings.Join([]string{
		"~/pelicula-cfg",      // config: ~ expanded
		"/mnt/nas/media",      // library
		"",                    // work: defaults to the library
		validWGKey,            // key
		"Germany,Switzerland", // countries
	}, "\n") + "\n"
	m, out := runWizardWith(input)

	if m["CONFIG_DIR"] != "/home/u/pelicula-cfg" {
		t.Errorf("CONFIG_DIR = %q", m["CONFIG_DIR"])
	}
	if m["LIBRARY_DIR"] != "/mnt/nas/media" {
		t.Errorf("LIBRARY_DIR = %q", m["LIBRARY_DIR"])
	}
	if m["WORK_DIR"] != "/mnt/nas/media" {
		t.Errorf("WORK_DIR = %q, want it to follow the library", m["WORK_DIR"])
	}
	if m["WIREGUARD_PRIVATE_KEY"] != validWGKey {
		t.Errorf("WIREGUARD_PRIVATE_KEY = %q", m["WIREGUARD_PRIVATE_KEY"])
	}
	if m["SERVER_COUNTRIES"] != "Germany,Switzerland" {
		t.Errorf("SERVER_COUNTRIES = %q", m["SERVER_COUNTRIES"])
	}
	if !strings.Contains(out, "server countries [Netherlands]") {
		t.Errorf("countries prompt should offer the Netherlands default:\n%s", out)
	}
}

func TestWizardRelativePathsResolveAgainstRepo(t *testing.T) {
	m, _ := runWizardWith("data/config\nmedia\n\n\n")
	if m["CONFIG_DIR"] != "/repo/data/config" || m["LIBRARY_DIR"] != "/repo/media" {
		t.Errorf("relative paths: CONFIG_DIR=%q LIBRARY_DIR=%q", m["CONFIG_DIR"], m["LIBRARY_DIR"])
	}
}

func TestWizardRejectsMalformedKeyThenAcceptsGoodOne(t *testing.T) {
	m, out := runWizardWith("\n\n\nnot-a-key\n" + validWGKey + "\n\n")
	if m["WIREGUARD_PRIVATE_KEY"] != validWGKey {
		t.Errorf("key = %q, want the second, valid answer", m["WIREGUARD_PRIVATE_KEY"])
	}
	if !strings.Contains(out, "not a WireGuard private key") {
		t.Errorf("no complaint about the malformed key:\n%s", out)
	}
	if m["SERVER_COUNTRIES"] != "Netherlands" {
		t.Errorf("SERVER_COUNTRIES = %q, want the default", m["SERVER_COUNTRIES"])
	}
}

func TestWizardGivesUpOnRepeatedBadKeys(t *testing.T) {
	m, out := runWizardWith("\n\n\nbad1\nbad2\nbad3\n")
	if m["WIREGUARD_PRIVATE_KEY"] != "" {
		t.Errorf("key = %q, want none after three bad answers", m["WIREGUARD_PRIVATE_KEY"])
	}
	if !strings.Contains(out, "Skipping the VPN") {
		t.Errorf("missing skip notice:\n%s", out)
	}
}

func TestWizardEmptyAndTruncatedInputUseDefaults(t *testing.T) {
	for name, input := range map[string]string{
		"empty":            "",
		"windows newlines": "\r\n\r\n\r\n\r\n",
		"truncated":        "/only/config",
	} {
		t.Run(name, func(t *testing.T) {
			m, _ := runWizardWith(input)
			if m["LIBRARY_DIR"] == "" || m["WORK_DIR"] == "" || m["JELLYFIN_PASSWORD"] == "" {
				t.Errorf("incomplete result: %v", m)
			}
			if input == "/only/config" && m["CONFIG_DIR"] != "/only/config" {
				t.Errorf("CONFIG_DIR = %q, want the answer given before EOF", m["CONFIG_DIR"])
			}
		})
	}
}

func TestWizardOutputPassesValidationAndRoundTrips(t *testing.T) {
	m, _ := runWizardWith("/srv/cfg\n/srv/media\n\n" + validWGKey + "\nNew Zealand\n")
	if err := validateEnv(m); err != nil {
		t.Fatalf("wizard produced an invalid env: %v", err)
	}

	path := filepath.Join(t.TempDir(), ".env")
	if err := WriteEnv(path, m); err != nil {
		t.Fatal(err)
	}
	got, err := ParseEnv(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{
		"CONFIG_DIR", "LIBRARY_DIR", "WORK_DIR", "PUID", "PGID", "TZ", "WIREGUARD_PRIVATE_KEY",
		"SERVER_COUNTRIES", "PELICULA_PORT", "JELLYFIN_ADMIN_USER", "JELLYFIN_PASSWORD",
		"WEBHOOK_SECRET", "GLUETUN_HTTP_USER", "GLUETUN_HTTP_PASS",
	} {
		if got[k] != m[k] || got[k] == "" && k != "WIREGUARD_PRIVATE_KEY" {
			t.Errorf("%s: file has %q, wizard produced %q", k, got[k], m[k])
		}
	}
	if got["SERVER_COUNTRIES"] != "New Zealand" {
		t.Errorf("SERVER_COUNTRIES = %q (spaces must survive quoting)", got["SERVER_COUNTRIES"])
	}
}

func TestExpandPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/abs/path", "/abs/path"},
		{"/abs/path/", "/abs/path"},
		{"~", "/home/u"},
		{"~/media", "/home/u/media"},
		{"rel/dir", "/repo/rel/dir"},
		{"./config", "/repo/config"},
		{`"/quoted/path"`, "/quoted/path"},
		{"  /padded  ", "/padded"},
		{"/a/../b", "/b"},
	}
	for _, tc := range cases {
		if got := expandPath(tc.in, "/repo", "/home/u"); got != tc.want {
			t.Errorf("expandPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWGKeyRegexp(t *testing.T) {
	good := []string{validWGKey, "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk="}
	bad := []string{"", "short=", strings.Repeat("A", 44), validWGKey + "x", "AAAA AAAA", strings.Repeat("A", 42) + "=="}
	for _, k := range good {
		if !wgKeyRe.MatchString(k) {
			t.Errorf("%q should be accepted", k)
		}
	}
	for _, k := range bad {
		if wgKeyRe.MatchString(k) {
			t.Errorf("%q should be rejected", k)
		}
	}
}
