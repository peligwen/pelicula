package autowire

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pelicula/internal/config"
)

func run(t *testing.T, d Deps) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return Run(ctx, d)
}

func TestRun_FreshStackWiresEverything(t *testing.T) {
	fastPoll(t)
	s := newStack()
	if err := run(t, s.deps()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Jellyfin: wizard completed with the configured admin, token used for both libraries.
	if s.jf.wizardUser != "admin" || s.jf.wizardPass != "hunter2hunter2" {
		t.Errorf("wizard credentials = %q/%q", s.jf.wizardUser, s.jf.wizardPass)
	}
	calls := s.jf.Calls()
	if idx := indexOf(calls, "CompleteStartupWizard"); idx < 0 || idx != indexOf(calls, "StartupWizardCompleted")+1 {
		t.Errorf("wizard not run right after the status check: %v", calls)
	}
	movies, tv := s.jf.addedByName["Movies"], s.jf.addedByName["TV Shows"]
	if movies.CollectionType != "movies" || len(movies.Locations) != 1 || movies.Locations[0] != "/media/movies" {
		t.Errorf("Movies library = %+v", movies)
	}
	if tv.CollectionType != "tvshows" || len(tv.Locations) != 1 || tv.Locations[0] != "/media/tv" {
		t.Errorf("TV Shows library = %+v", tv)
	}
	for _, tok := range s.jf.tokenSeen {
		if tok != "jf-token" {
			t.Errorf("Jellyfin call used token %q", tok)
		}
	}

	// Root folders.
	assertRootFolder(t, s.sonarr, "/media/tv")
	assertRootFolder(t, s.radarr, "/media/movies")

	// Download clients, category per app.
	for _, tc := range []struct {
		app      *fakeArr
		category string
		catField string
	}{
		{s.sonarr, "tv-sonarr", "tvCategory"},
		{s.radarr, "radarr", "movieCategory"},
	} {
		adds := tc.app.Calls("AddDownloadClient")
		if len(adds) != 1 {
			t.Fatalf("AddDownloadClient called %d times", len(adds))
		}
		dc := adds[0].payload
		if dc["implementation"] != "QBittorrent" || dc["configContract"] != "QBittorrentSettings" ||
			dc["protocol"] != "torrent" || dc["enable"] != true || dc["name"] != "qBittorrent" {
			t.Errorf("download client payload = %v", dc)
		}
		if field(t, dc, "host") != "gluetun" || field(t, dc, "port") != float64(8080) {
			t.Errorf("download client host/port = %v:%v", field(t, dc, "host"), field(t, dc, "port"))
		}
		if field(t, dc, "username") != "" || field(t, dc, "password") != "" {
			t.Errorf("download client should carry no credentials")
		}
		if field(t, dc, tc.catField) != tc.category || field(t, dc, "category") != tc.category {
			t.Errorf("category fields = %v / %v, want %q", field(t, dc, tc.catField), field(t, dc, "category"), tc.category)
		}
	}

	// Webhook notification with the secret in the header.
	for _, app := range []*fakeArr{s.sonarr, s.radarr} {
		adds := app.Calls("AddNotification")
		if len(adds) != 1 {
			t.Fatalf("AddNotification called %d times", len(adds))
		}
		n := adds[0].payload
		if n["name"] != "Pelicula" || n["implementation"] != "Webhook" || n["configContract"] != "WebhookSettings" {
			t.Errorf("notification identity = %v", n)
		}
		if n["onDownload"] != true || n["onUpgrade"] != true || n["onGrab"] != false || n["onHealthIssue"] != false {
			t.Errorf("notification triggers = %v", n)
		}
		if got := field(t, n, "url"); got != "http://pelicula:8181/api/hooks/import" {
			t.Errorf("webhook url = %v", got)
		}
		if got := field(t, n, "method"); got != float64(1) {
			t.Errorf("webhook method = %v, want 1 (POST)", got)
		}
		headers, _ := field(t, n, "headers").([]any)
		if len(headers) != 1 {
			t.Fatalf("headers = %v", headers)
		}
		h := headers[0].(map[string]any)
		if h["key"] != "X-Webhook-Secret" || h["value"] != "s3cret" {
			t.Errorf("header = %v", h)
		}
	}

	// Prowlarr applications.
	apps := s.prowlarr.Calls("AddApplication")
	if len(apps) != 2 {
		t.Fatalf("AddApplication called %d times, want 2", len(apps))
	}
	byName := map[string]map[string]any{}
	for _, a := range apps {
		byName[a.payload["name"].(string)] = a.payload
	}
	for name, want := range map[string]struct{ baseURL, key string }{
		"Sonarr": {"http://sonarr:8989/sonarr", "sonarr-key"},
		"Radarr": {"http://radarr:7878/radarr", "radarr-key"},
	} {
		a := byName[name]
		if a == nil {
			t.Fatalf("no %s application added", name)
		}
		if a["implementation"] != name || a["configContract"] != name+"Settings" || a["syncLevel"] != "fullSync" {
			t.Errorf("%s application identity = %v", name, a)
		}
		if field(t, a, "prowlarrUrl") != "http://gluetun:9696/prowlarr" ||
			field(t, a, "baseUrl") != want.baseURL || field(t, a, "apiKey") != want.key {
			t.Errorf("%s application fields = %v", name, a["fields"])
		}
	}
}

func TestRun_AlreadyWiredMakesNoChanges(t *testing.T) {
	fastPoll(t)
	s := newStack()
	if err := run(t, s.deps()); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	before := map[string]int{
		"sonarr": s.sonarr.Mutations(), "radarr": s.radarr.Mutations(),
		"prowlarr": s.prowlarr.Mutations(), "jellyfin": s.jf.Adds(),
	}
	wizardCalls := indexCount(s.jf.Calls(), "CompleteStartupWizard")

	// Second run against the state the first one produced.
	if err := run(t, s.deps()); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	for name, n := range map[string]int{
		"sonarr": s.sonarr.Mutations(), "radarr": s.radarr.Mutations(),
		"prowlarr": s.prowlarr.Mutations(), "jellyfin": s.jf.Adds(),
	} {
		if n != before[name] {
			t.Errorf("%s: second Run mutated state (%d -> %d calls)", name, before[name], n)
		}
	}
	if got := indexCount(s.jf.Calls(), "CompleteStartupWizard"); got != wizardCalls {
		t.Errorf("wizard re-run on an already completed Jellyfin")
	}
}

func TestRun_AlreadyWiredPreexistingResources(t *testing.T) {
	fastPoll(t)
	s := newStack()
	s.jf.wizardDone = true
	s.jf.libraries = []Library{
		{Name: "Movies", CollectionType: "movies", Locations: []string{"/media/movies"}},
		{Name: "TV Shows", CollectionType: "tvshows", Locations: []string{"/media/tv/"}}, // trailing slash is the same path
	}
	s.sonarr.rootFolders = []map[string]any{{"id": float64(1), "path": "/media/tv/"}}
	s.radarr.rootFolders = []map[string]any{{"id": float64(1), "path": "/media/movies"}}
	for _, app := range []struct {
		f   *fakeArr
		cat string
		fld string
	}{{s.sonarr, "tv-sonarr", "tvCategory"}, {s.radarr, "radarr", "movieCategory"}} {
		// A download client exactly as the *arr schema reports it: no legacy
		// "category" field and no useSsl.
		app.f.downloadClients = []map[string]any{{
			"id": float64(7), "implementation": "QBittorrent",
			"fields": []any{
				map[string]any{"name": "host", "value": "gluetun"},
				map[string]any{"name": "port", "value": float64(8080)},
				map[string]any{"name": app.fld, "value": app.cat},
			},
		}}
		app.f.notifications = []map[string]any{{
			"id": float64(9), "name": "Pelicula", "onDownload": true, "onUpgrade": true,
			"fields": []any{
				map[string]any{"name": "url", "value": "http://pelicula:8181/api/hooks/import"},
				map[string]any{"name": "method", "value": float64(1)},
				map[string]any{"name": "headers", "value": []any{
					map[string]any{"key": "X-Webhook-Secret", "value": "s3cret"},
				}},
			},
		}}
	}
	s.prowlarr.applications = []map[string]any{
		{"id": float64(1), "name": "Sonarr", "fields": []any{
			map[string]any{"name": "prowlarrUrl", "value": "http://GLUETUN:9696/prowlarr/"},
			map[string]any{"name": "baseUrl", "value": "http://sonarr:8989/sonarr"},
			map[string]any{"name": "apiKey", "value": "sonarr-key"},
		}},
		{"id": float64(2), "name": "Radarr", "fields": []any{
			map[string]any{"name": "prowlarrUrl", "value": "http://gluetun:9696/prowlarr"},
			map[string]any{"name": "baseUrl", "value": "http://radarr:7878/radarr"},
			map[string]any{"name": "apiKey", "value": "radarr-key"},
		}},
	}

	if err := run(t, s.deps()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for name, n := range map[string]int{
		"sonarr": s.sonarr.Mutations(), "radarr": s.radarr.Mutations(),
		"prowlarr": s.prowlarr.Mutations(), "jellyfin": s.jf.Adds(),
	} {
		if n != 0 {
			t.Errorf("%s: %d mutating calls against an already wired stack", name, n)
		}
	}
	if len(s.jf.Calls()) == 0 || indexCount(s.jf.Calls(), "CompleteStartupWizard") != 0 {
		t.Errorf("jellyfin calls = %v", s.jf.Calls())
	}
}

func TestRun_CorrectsDrift(t *testing.T) {
	fastPoll(t)
	s := newStack()
	s.jf.wizardDone = true
	s.sonarr.downloadClients = []map[string]any{{
		"id": float64(3), "name": "my qbt", "implementation": "QBittorrent", "priority": float64(5),
		"fields": []any{
			map[string]any{"name": "host", "value": "localhost"},
			map[string]any{"name": "port", "value": float64(8081)},
			map[string]any{"name": "tvCategory", "value": "wrong"},
			map[string]any{"name": "useSsl", "value": true},
			map[string]any{"name": "password", "value": "keep-me"},
		},
	}}
	s.radarr.notifications = []map[string]any{{
		"id": float64(4), "name": "Pelicula", "onDownload": false, "onUpgrade": true, "tags": []any{float64(2)},
		"fields": []any{
			map[string]any{"name": "url", "value": "http://old:8181/api/hooks/import"},
			map[string]any{"name": "method", "value": float64(1)},
			map[string]any{"name": "headers", "value": []any{
				map[string]any{"key": "X-Webhook-Secret", "value": "old-secret"},
			}},
		},
	}}
	s.prowlarr.applications = []map[string]any{{
		"id": float64(5), "name": "Radarr", "syncLevel": "addOnly", "fields": []any{
			map[string]any{"name": "prowlarrUrl", "value": "http://old:9696/prowlarr"},
			map[string]any{"name": "baseUrl", "value": "http://radarr:7878/radarr"},
			map[string]any{"name": "apiKey", "value": "stale"},
		},
	}}

	if err := run(t, s.deps()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Download client: updated in place (no Add), unrelated fields preserved.
	if n := len(s.sonarr.Calls("AddDownloadClient")); n != 0 {
		t.Errorf("AddDownloadClient called %d times, want update instead", n)
	}
	ups := s.sonarr.Calls("UpdateDownloadClient")
	if len(ups) != 1 || ups[0].id != 3 {
		t.Fatalf("UpdateDownloadClient calls = %+v", ups)
	}
	dc := ups[0].payload
	if field(t, dc, "host") != "gluetun" || field(t, dc, "port") != float64(8080) ||
		field(t, dc, "tvCategory") != "tv-sonarr" || field(t, dc, "useSsl") != false {
		t.Errorf("download client not corrected: %v", dc["fields"])
	}
	if field(t, dc, "password") != "keep-me" || dc["name"] != "my qbt" || dc["priority"] != float64(5) {
		t.Errorf("update dropped unrelated data: %v", dc)
	}

	// Webhook: url, secret and triggers corrected in place.
	ups = s.radarr.Calls("UpdateNotification")
	if len(ups) != 1 || ups[0].id != 4 || len(s.radarr.Calls("AddNotification")) != 0 {
		t.Fatalf("radarr notification calls: updates=%+v adds=%d", ups, len(s.radarr.Calls("AddNotification")))
	}
	n := ups[0].payload
	if field(t, n, "url") != "http://pelicula:8181/api/hooks/import" || n["onDownload"] != true {
		t.Errorf("webhook not corrected: %v", n)
	}
	h := field(t, n, "headers").([]any)[0].(map[string]any)
	if h["value"] != "s3cret" {
		t.Errorf("secret header = %v", h)
	}
	if tags, _ := n["tags"].([]any); len(tags) != 1 {
		t.Errorf("update dropped tags: %v", n)
	}

	// Prowlarr app: stale url and key corrected, Sonarr app added.
	ups = s.prowlarr.Calls("UpdateApplication")
	if len(ups) != 1 || ups[0].id != 5 {
		t.Fatalf("UpdateApplication calls = %+v", ups)
	}
	if a := ups[0].payload; field(t, a, "prowlarrUrl") != "http://gluetun:9696/prowlarr" ||
		field(t, a, "apiKey") != "radarr-key" || a["syncLevel"] != "addOnly" {
		t.Errorf("application not corrected: %v", a)
	}
	adds := s.prowlarr.Calls("AddApplication")
	if len(adds) != 1 || adds[0].payload["name"] != "Sonarr" {
		t.Errorf("AddApplication calls = %+v", adds)
	}
}

func TestRun_WebhookWithoutSecretOmitsHeader(t *testing.T) {
	fastPoll(t)
	s := newStack()
	d := s.deps()
	d.Cfg.WebhookSecret = ""
	if err := run(t, d); err != nil {
		t.Fatalf("Run: %v", err)
	}
	n := s.sonarr.Calls("AddNotification")[0].payload
	if _, ok := fieldValue(n, "headers"); ok {
		t.Errorf("headers field present without a secret: %v", n["fields"])
	}
}

func TestRun_VPNOffSkipsQBTAndProwlarr(t *testing.T) {
	fastPoll(t)
	s := newStack()
	d := s.deps()
	d.Prowlarr, d.QBT = nil, nil
	if err := run(t, d); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, app := range []*fakeArr{s.sonarr, s.radarr} {
		if n := len(app.Calls("AddDownloadClient")) + len(app.Calls("UpdateDownloadClient")); n != 0 {
			t.Errorf("download client touched without VPN (%d calls)", n)
		}
		if len(app.Calls("AddRootFolder")) != 1 || len(app.Calls("AddNotification")) != 1 {
			t.Errorf("root folder / webhook must still be wired without VPN: %+v", app.calls)
		}
	}
	if s.prowlarr.Mutations() != 0 || s.prowlarr.Pings() != 0 || s.qbt.pings != 0 {
		t.Errorf("prowlarr/qbt used although not configured")
	}
	if len(s.jf.libraries) != 2 {
		t.Errorf("jellyfin libraries = %v", s.jf.libraries)
	}
}

func TestRun_TypedNilClientsAreTreatedAsNil(t *testing.T) {
	fastPoll(t)
	s := newStack()
	d := s.deps()
	var nilArr *fakeArr
	var nilQBT *fakeQBT
	d.Prowlarr, d.QBT = nilArr, nilQBT // non-nil interfaces holding nil pointers
	if err := run(t, d); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(s.sonarr.Calls("AddDownloadClient")) != 0 {
		t.Errorf("typed-nil QBT was treated as configured")
	}
}

func TestRun_JellyfinFailureDoesNotStopArrWiring(t *testing.T) {
	fastPoll(t)
	for name, mutate := range map[string]func(*stack){
		"wizard fails":       func(s *stack) { s.jf.wizardErr = errors.New("wizard boom") },
		"status check fails": func(s *stack) { s.jf.statusErr = errors.New("status boom") },
		"list fails":         func(s *stack) { s.jf.wizardDone = true; s.jf.listErr = errors.New("list boom") },
		"add fails":          func(s *stack) { s.jf.wizardDone = true; s.jf.addErr["Movies"] = errors.New("add boom") },
	} {
		t.Run(name, func(t *testing.T) {
			s := newStack()
			mutate(s)
			err := run(t, s.deps())
			if err == nil {
				t.Fatal("Run returned nil although a Jellyfin step failed")
			}
			if !strings.Contains(err.Error(), "jellyfin") || !strings.Contains(err.Error(), "boom") {
				t.Errorf("error should name the Jellyfin step and cause: %v", err)
			}
			// Everything else was still wired.
			for _, app := range []*fakeArr{s.sonarr, s.radarr} {
				if len(app.Calls("AddRootFolder")) != 1 || len(app.Calls("AddDownloadClient")) != 1 || len(app.Calls("AddNotification")) != 1 {
					t.Errorf("*arr wiring stopped after the Jellyfin failure: %+v", app.calls)
				}
			}
			if len(s.prowlarr.Calls("AddApplication")) != 2 {
				t.Errorf("prowlarr wiring stopped after the Jellyfin failure")
			}
		})
	}
}

func TestRun_AddLibraryFailureStillAddsOtherLibrary(t *testing.T) {
	fastPoll(t)
	s := newStack()
	s.jf.wizardDone = true
	s.jf.addErr["Movies"] = errors.New("add boom")
	if err := run(t, s.deps()); err == nil {
		t.Fatal("expected an error")
	}
	if _, ok := s.jf.addedByName["TV Shows"]; !ok {
		t.Errorf("TV Shows library was not attempted after Movies failed: %v", s.jf.Calls())
	}
}

func TestRun_ReturnsFirstErrorAfterAllSteps(t *testing.T) {
	fastPoll(t)
	s := newStack()
	s.sonarr.failList["ListRootFolders"] = errors.New("sonarr boom")
	s.radarr.failList["ListNotifications"] = errors.New("radarr boom")
	err := run(t, s.deps())
	if err == nil || !strings.Contains(err.Error(), "sonarr boom") {
		t.Fatalf("Run error = %v, want the first (sonarr) failure", err)
	}
	// The later failure did not abort anything after it.
	if len(s.radarr.Calls("AddRootFolder")) != 1 || len(s.prowlarr.Calls("AddApplication")) != 2 {
		t.Errorf("steps after the failure were skipped")
	}
	if len(s.sonarr.Calls("AddDownloadClient")) != 1 || len(s.sonarr.Calls("AddNotification")) != 1 {
		t.Errorf("sonarr's other steps were skipped after its root folder failed")
	}
}

func TestRun_TokenRetriedAfterWizard(t *testing.T) {
	fastPoll(t)
	s := newStack()
	s.tokens.fails = 2
	if err := run(t, s.deps()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := s.tokens.Calls(); got != 3 {
		t.Errorf("Token called %d times, want 3", got)
	}
	if len(s.jf.libraries) != 2 {
		t.Errorf("libraries not added after the token recovered")
	}
}

func TestRun_TokenNotRetriedWhenWizardAlreadyDone(t *testing.T) {
	fastPoll(t)
	s := newStack()
	s.jf.wizardDone = true
	s.tokens.fails = 100
	err := run(t, s.deps())
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("Run error = %v", err)
	}
	if got := s.tokens.Calls(); got != 1 {
		t.Errorf("Token called %d times, want 1", got)
	}
}

func TestRun_WaitsForServices(t *testing.T) {
	fastPoll(t)
	s := newStack()
	s.sonarr.pingFails = 3
	s.jf.pingFails = 1
	s.prowlarr.pingFails = 2
	s.qbt.pingFails = 4
	if err := run(t, s.deps()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if s.sonarr.Pings() != 4 || s.qbt.pings != 5 {
		t.Errorf("pings: sonarr=%d qbt=%d", s.sonarr.Pings(), s.qbt.pings)
	}
	// A service that answered is not pinged again while others catch up.
	if s.radarr.Pings() != 1 {
		t.Errorf("radarr pinged %d times, want 1", s.radarr.Pings())
	}
	if s.sonarr.Mutations() == 0 {
		t.Errorf("nothing wired after the services came up")
	}
}

func TestRun_ContextCancelledWhileWaiting(t *testing.T) {
	fastPoll(t)
	s := newStack()
	s.radarr.pingFails = 1 << 30
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := Run(ctx, s.deps())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run error = %v, want deadline exceeded", err)
	}
	if !strings.Contains(err.Error(), "radarr") {
		t.Errorf("error should name the service still down: %v", err)
	}
	if s.sonarr.Mutations() != 0 || len(s.jf.Calls()) != 0 {
		t.Errorf("wiring started before all services were ready")
	}
}

func TestRun_MissingDepsIsAnError(t *testing.T) {
	s := newStack()
	d := s.deps()
	d.Jellyfin = nil
	if err := run(t, d); err == nil || !strings.Contains(err.Error(), "Jellyfin") {
		t.Fatalf("Run error = %v", err)
	}
	var nilTokens *fakeTokens
	d = s.deps()
	d.JFAdmin = nilTokens
	if err := run(t, d); err == nil || !strings.Contains(err.Error(), "JFAdmin") {
		t.Fatalf("Run error = %v", err)
	}
}

func TestRun_ProwlarrWithoutKeysFailsThatStepOnly(t *testing.T) {
	fastPoll(t)
	s := newStack()
	d := s.deps()
	d.SonarrAPIKey, d.RadarrAPIKey = "", ""
	err := run(t, d)
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("Run error = %v", err)
	}
	if s.prowlarr.Mutations() != 0 {
		t.Errorf("application added without an API key")
	}
	if len(s.sonarr.Calls("AddNotification")) != 1 {
		t.Errorf("other steps did not run")
	}
}

// ---- WaitForAPIKeys ----

func writeConfigXML(t *testing.T, dir, service, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, service), 0o755); err != nil {
		t.Error(err)
		return
	}
	if err := os.WriteFile(filepath.Join(dir, service, "config.xml"), []byte(body), 0o644); err != nil {
		t.Error(err)
	}
}

func xmlWithKey(key string) string {
	return "<Config><Port>8989</Port><ApiKey>" + key + "</ApiKey></Config>"
}

func TestWaitForAPIKeys_ReadsKeysThatAppearLater(t *testing.T) {
	fastPoll(t)
	dir := t.TempDir()
	cfg := config.Config{ConfigDir: dir}
	sonarr, radarr, prowlarr := newFakeArr(), newFakeArr(), newFakeArr()

	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(20 * time.Millisecond)
		// Sonarr first writes a config without a key (as on early boot).
		writeConfigXML(t, dir, "sonarr", "<Config><Port>8989</Port></Config>")
		time.Sleep(20 * time.Millisecond)
		writeConfigXML(t, dir, "radarr", xmlWithKey("radarr-key"))
		time.Sleep(20 * time.Millisecond)
		writeConfigXML(t, dir, "sonarr", xmlWithKey("sonarr-key"))
		time.Sleep(20 * time.Millisecond)
		writeConfigXML(t, dir, "prowlarr", xmlWithKey("prowlarr-key"))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sk, rk, err := WaitForAPIKeys(ctx, cfg, sonarr, radarr, prowlarr)
	<-done
	if err != nil {
		t.Fatalf("WaitForAPIKeys: %v", err)
	}
	if sk != "sonarr-key" || rk != "radarr-key" {
		t.Errorf("keys = %q, %q", sk, rk)
	}
	if sonarr.APIKey() != "sonarr-key" || radarr.APIKey() != "radarr-key" || prowlarr.APIKey() != "prowlarr-key" {
		t.Errorf("SetAPIKey: sonarr=%q radarr=%q prowlarr=%q", sonarr.APIKey(), radarr.APIKey(), prowlarr.APIKey())
	}
}

func TestWaitForAPIKeys_NilProwlarrNotWaitedFor(t *testing.T) {
	fastPoll(t)
	dir := t.TempDir()
	writeConfigXML(t, dir, "sonarr", xmlWithKey("sk"))
	writeConfigXML(t, dir, "radarr", xmlWithKey("rk"))
	sonarr, radarr := newFakeArr(), newFakeArr()
	var typedNil *fakeArr // what a nil *arr.Client looks like inside the interface

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sk, rk, err := WaitForAPIKeys(ctx, config.Config{ConfigDir: dir}, sonarr, radarr, typedNil)
	if err != nil || sk != "sk" || rk != "rk" {
		t.Fatalf("got %q %q %v", sk, rk, err)
	}
	sk, rk, err = WaitForAPIKeys(ctx, config.Config{ConfigDir: dir}, sonarr, radarr, nil)
	if err != nil || sk != "sk" || rk != "rk" {
		t.Fatalf("got %q %q %v", sk, rk, err)
	}
}

func TestWaitForAPIKeys_TimesOutAndLeavesClientsUntouched(t *testing.T) {
	fastPoll(t)
	dir := t.TempDir()
	writeConfigXML(t, dir, "sonarr", xmlWithKey("sk")) // radarr never appears
	sonarr, radarr := newFakeArr(), newFakeArr()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, _, err := WaitForAPIKeys(ctx, config.Config{ConfigDir: dir}, sonarr, radarr, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if !strings.Contains(err.Error(), "radarr") || strings.Contains(err.Error(), "sonarr") {
		t.Errorf("error should name only the missing service: %v", err)
	}
	if sonarr.APIKey() != "" || radarr.APIKey() != "" {
		t.Errorf("SetAPIKey called despite the error")
	}
}

func TestWaitForAPIKeys_DefaultInterval(t *testing.T) {
	if pollInterval != 3*time.Second {
		t.Errorf("pollInterval = %v, want 3s", pollInterval)
	}
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}

func indexCount(s []string, v string) int {
	n := 0
	for _, x := range s {
		if x == v {
			n++
		}
	}
	return n
}

func assertRootFolder(t *testing.T, app *fakeArr, path string) {
	t.Helper()
	adds := app.Calls("AddRootFolder")
	if len(adds) != 1 || adds[0].payload["path"] != path {
		t.Errorf("AddRootFolder calls = %+v, want one for %s", adds, path)
	}
}
