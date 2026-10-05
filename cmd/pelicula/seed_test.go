package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFileT(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSetXMLElement(t *testing.T) {
	cases := []struct {
		name, doc, want string
	}{
		{
			name: "replaces an existing element",
			doc:  "<Config><UrlBase>/old</UrlBase></Config>",
			want: "<Config><UrlBase>/new</UrlBase></Config>",
		},
		{
			name: "replaces a self-closing element",
			doc:  "<Config><UrlBase /></Config>",
			want: "<Config><UrlBase>/new</UrlBase></Config>",
		},
		{
			name: "replaces an empty element",
			doc:  "<Config><UrlBase></UrlBase></Config>",
			want: "<Config><UrlBase>/new</UrlBase></Config>",
		},
		{
			name: "inserts before the closing tag",
			doc:  "<Config>\n  <Port>1</Port>\n</Config>\n",
			want: "<Config>\n  <Port>1</Port>\n  <UrlBase>/new</UrlBase>\n</Config>\n",
		},
		{
			name: "no root close tag leaves the document alone",
			doc:  "not xml",
			want: "not xml",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := setXMLElement(tc.doc, "UrlBase", "/new", "</Config>")
			if got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
			if again := setXMLElement(got, "UrlBase", "/new", "</Config>"); again != got {
				t.Errorf("not idempotent:\nfirst  %q\nsecond %q", got, again)
			}
		})
	}
}

func TestSetXMLElementEscapes(t *testing.T) {
	got := setXMLElement("<Config></Config>", "Name", `a&b<"c">`, "</Config>")
	if !strings.Contains(got, "<Name>a&amp;b&lt;&quot;c&quot;&gt;</Name>") {
		t.Errorf("value not escaped: %q", got)
	}
}

func TestEnforceArrConfigPatchesAuthBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.xml")
	// What an *arr app rewrites on first boot: auth switched back on.
	writeFileT(t, path, `<Config>
  <BindAddress>*</BindAddress>
  <UrlBase></UrlBase>
  <AuthenticationMethod>Forms</AuthenticationMethod>
  <AuthenticationRequired>Enabled</AuthenticationRequired>
  <AnalyticsEnabled>True</AnalyticsEnabled>
  <ApiKey>0123456789abcdef</ApiKey>
</Config>
`)
	if err := enforceArrConfig(path, "/sonarr"); err != nil {
		t.Fatal(err)
	}
	got := readFileT(t, path)
	for _, want := range []string{
		"<UrlBase>/sonarr</UrlBase>",
		"<AuthenticationMethod>External</AuthenticationMethod>",
		"<AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>",
		"<AnalyticsEnabled>False</AnalyticsEnabled>",
		"<ApiKey>0123456789abcdef</ApiKey>", // untouched
		"<BindAddress>*</BindAddress>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("patched config lacks %s:\n%s", want, got)
		}
	}

	// Idempotent: a second run must not touch the file at all.
	if err := os.Chtimes(path, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	if err := enforceArrConfig(path, "/sonarr"); err != nil {
		t.Fatal(err)
	}
	if readFileT(t, path) != got {
		t.Error("second enforceArrConfig changed the content")
	}
	if fi, _ := os.Stat(path); !fi.ModTime().Equal(fixedTime) {
		t.Error("second enforceArrConfig rewrote an unchanged file")
	}
}

func TestEnforceMissingFilesAreNoOps(t *testing.T) {
	dir := t.TempDir()
	if err := enforceArrConfig(filepath.Join(dir, "none.xml"), "/radarr"); err != nil {
		t.Errorf("missing *arr config: %v", err)
	}
	if err := enforceQBittorrentConf(filepath.Join(dir, "none.conf")); err != nil {
		t.Errorf("missing qBittorrent.conf: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("enforcing created files: %v", entries)
	}
}

func TestArrConfigXML(t *testing.T) {
	got := arrConfigXML("/prowlarr")
	for _, want := range []string{
		"<UrlBase>/prowlarr</UrlBase>",
		"<AuthenticationMethod>External</AuthenticationMethod>",
		"<AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>",
		"<AnalyticsEnabled>False</AnalyticsEnabled>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("seed lacks %s: %s", want, got)
		}
	}
}

func TestJellyfinNetworkXML(t *testing.T) {
	got := jellyfinNetworkXML()
	for _, want := range []string{"<BaseUrl>/jellyfin</BaseUrl>", "<KnownProxies><string>172.16.0.0/12</string></KnownProxies>"} {
		if !strings.Contains(got, want) {
			t.Errorf("network.xml lacks %s: %s", want, got)
		}
	}
	// Nothing environment-driven: same bytes however the host is configured.
	t.Setenv("JELLYFIN_PUBLISHED_URL", "http://example")
	t.Setenv("PELICULA_KNOWN_PROXIES", "10.0.0.0/8")
	if jellyfinNetworkXML() != got {
		t.Error("network.xml must not depend on the environment")
	}
}

func TestPatchINIKey(t *testing.T) {
	const key = `Bittorrent\DHT`
	cases := []struct {
		name, in, want string
	}{
		{
			name: "replaces in place",
			in:   "[Preferences]\nBittorrent\\DHT=true\nOther=1\n",
			want: "[Preferences]\nBittorrent\\DHT=false\nOther=1\n",
		},
		{
			name: "inserts at the end of the section before the next header",
			in:   "[Preferences]\nOther=1\n\n[BitTorrent]\nX=2\n",
			want: "[Preferences]\nOther=1\n\nBittorrent\\DHT=false\n[BitTorrent]\nX=2\n",
		},
		{
			name: "inserts when the section runs to EOF",
			in:   "[Preferences]\nOther=1\n",
			want: "[Preferences]\nOther=1\nBittorrent\\DHT=false\n",
		},
		{
			name: "creates a missing section",
			in:   "[Other]\nA=1\n",
			want: "[Other]\nA=1\n\n[Preferences]\nBittorrent\\DHT=false\n",
		},
		{
			name: "empty file",
			in:   "",
			want: "\n[Preferences]\nBittorrent\\DHT=false\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(patchINIKey([]byte(tc.in), "Preferences", key, "false"))
			if got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
			if again := string(patchINIKey([]byte(got), "Preferences", key, "false")); again != got {
				t.Errorf("not idempotent:\nfirst  %q\nsecond %q", got, again)
			}
		})
	}
}

func TestEnforceQBittorrentConf(t *testing.T) {
	path := filepath.Join(t.TempDir(), "qBittorrent.conf")
	writeFileT(t, path, "[Preferences]\n"+
		`Bittorrent\DHT=true`+"\n"+
		`Bittorrent\PeX=true`+"\n"+
		`RSS\Session\Enabled=true`+"\n"+
		`WebUI\Port=8080`+"\n")
	if err := enforceQBittorrentConf(path); err != nil {
		t.Fatal(err)
	}
	got := readFileT(t, path)
	for _, want := range []string{
		`Bittorrent\DHT=false`, `Bittorrent\PeX=false`, `Bittorrent\LSD=false`,
		`RSS\AutoDownloader\enabled=false`, `RSS\Session\Enabled=false`,
		`WebUI\Port=8080`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("conf lacks %s:\n%s", want, got)
		}
	}
	if err := enforceQBittorrentConf(path); err != nil {
		t.Fatal(err)
	}
	if readFileT(t, path) != got {
		t.Error("second enforce changed the file")
	}
}

func TestEnforceQBittorrentConfIgnoresLegacyEnvOverride(t *testing.T) {
	t.Setenv("PELICULA_QBIT_PRIVATE_PEERS", "false")
	path := filepath.Join(t.TempDir(), "qBittorrent.conf")
	writeFileT(t, path, "[Preferences]\n"+`Bittorrent\DHT=true`+"\n")
	if err := enforceQBittorrentConf(path); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFileT(t, path), `Bittorrent\DHT=false`) {
		t.Error("DHT must always be forced off")
	}
}

func TestSeedAllConfigs(t *testing.T) {
	dir := t.TempDir()
	if err := SeedAllConfigs(dir); err != nil {
		t.Fatal(err)
	}

	for _, s := range arrServices {
		got := readFileT(t, filepath.Join(dir, s.name, "config.xml"))
		if !strings.Contains(got, "<UrlBase>"+s.urlBase+"</UrlBase>") {
			t.Errorf("%s config.xml lacks its UrlBase: %s", s.name, got)
		}
		if !strings.Contains(got, "<AuthenticationMethod>External</AuthenticationMethod>") ||
			!strings.Contains(got, "<AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>") {
			t.Errorf("%s config.xml lacks external auth: %s", s.name, got)
		}
	}

	for _, path := range jellyfinNetworkPaths(dir) {
		if got := readFileT(t, path); !strings.Contains(got, "<BaseUrl>/jellyfin</BaseUrl>") {
			t.Errorf("%s lacks BaseUrl: %s", path, got)
		}
	}

	qbt := filepath.Join(dir, "qbittorrent", "qBittorrent")
	conf := readFileT(t, filepath.Join(qbt, "qBittorrent.conf"))
	for _, want := range []string{
		`WebUI\AuthSubnetWhitelistEnabled=true`, `WebUI\AuthSubnetWhitelist=172.16.0.0/12`,
		`Bittorrent\DHT=false`, `Bittorrent\PeX=false`, `Bittorrent\LSD=false`,
		`RSS\AutoDownloader\enabled=false`, `RSS\Session\Enabled=false`,
	} {
		if !strings.Contains(conf, want) {
			t.Errorf("qBittorrent.conf lacks %s", want)
		}
	}
	if cats := readFileT(t, filepath.Join(qbt, "categories.json")); !strings.Contains(cats, `"radarr"`) || !strings.Contains(cats, `"tv-sonarr"`) {
		t.Errorf("categories.json = %s", cats)
	}

	// Things deliberately not seeded any more.
	for _, gone := range []string{
		filepath.Join(dir, "bazarr"),
		filepath.Join(dir, "jellyfin", "config", "branding.xml"),
		filepath.Join(dir, "jellyfin", "config", "dlna.xml"),
	} {
		if fileExists(gone) {
			t.Errorf("%s must not be seeded", gone)
		}
	}
}

func TestSeedAllConfigsIsIdempotentAndKeepsExistingFiles(t *testing.T) {
	dir := t.TempDir()
	if err := SeedAllConfigs(dir); err != nil {
		t.Fatal(err)
	}

	// The apps' own additions (API keys, user edits) must survive another up.
	sonarr := filepath.Join(dir, "sonarr", "config.xml")
	writeFileT(t, sonarr, "<Config>\n  <ApiKey>abc123</ApiKey>\n  <UrlBase>/sonarr</UrlBase>\n  <AuthenticationMethod>External</AuthenticationMethod>\n  <AuthenticationRequired>DisabledForLocalAddresses</AuthenticationRequired>\n  <AnalyticsEnabled>False</AnalyticsEnabled>\n</Config>\n")
	qbtConfPath := filepath.Join(dir, "qbittorrent", "qBittorrent", "qBittorrent.conf")
	custom := readFileT(t, qbtConfPath) + "\n[Custom]\nMine=1\n"
	writeFileT(t, qbtConfPath, custom)

	before := snapshotTree(t, dir)
	if err := SeedAllConfigs(dir); err != nil {
		t.Fatal(err)
	}
	after := snapshotTree(t, dir)
	for path, content := range before {
		if after[path] != content {
			t.Errorf("%s changed on the second seed", path)
		}
	}
	if len(after) != len(before) {
		t.Errorf("second seed changed the file set: %d -> %d files", len(before), len(after))
	}
	if !strings.Contains(readFileT(t, sonarr), "<ApiKey>abc123</ApiKey>") {
		t.Error("API key lost")
	}
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out[path] = readFileT(t, path)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
