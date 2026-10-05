package autowire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"pelicula/internal/config"
)

// fastPoll shortens the service/key poll interval for the test.
func fastPoll(t *testing.T) {
	t.Helper()
	old := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = old })
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testConfig() config.Config {
	return config.Config{
		SonarrURL:             "http://sonarr:8989/sonarr",
		RadarrURL:             "http://radarr:7878/radarr",
		ProwlarrURL:           "http://gluetun:9696/prowlarr",
		SelfURL:               "http://pelicula:8181",
		WebhookSecret:         "s3cret",
		JellyfinAdminUser:     "admin",
		JellyfinAdminPassword: "hunter2hunter2",
		MoviesPath:            "/media/movies",
		TVPath:                "/media/tv",
	}
}

type call struct {
	op      string
	id      int
	payload map[string]any
}

// fakeArr is a stateful in-memory *arr: Add* appends to what List* returns, so
// a second Run sees the first Run's result exactly as a real app would return
// it (decoded JSON).
type fakeArr struct {
	mu sync.Mutex

	pingFails int // number of Ping calls that fail before success
	pings     int
	apiKey    string

	downloadClients []map[string]any
	rootFolders     []map[string]any
	notifications   []map[string]any
	applications    []map[string]any
	nextID          int

	failList map[string]error // op name -> error returned by that List call
	calls    []call
}

func newFakeArr() *fakeArr { return &fakeArr{nextID: 1, failList: map[string]error{}} }

func (f *fakeArr) Ping(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pings++
	if f.pings <= f.pingFails {
		return errors.New("connection refused")
	}
	return nil
}

func (f *fakeArr) SetAPIKey(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apiKey = key
}

func (f *fakeArr) APIKey() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.apiKey
}

func decode(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		panic(fmt.Sprintf("payload is not a JSON object: %v: %s", err, b))
	}
	return m
}

func (f *fakeArr) list(op string, items []map[string]any) ([]map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failList[op]; err != nil {
		return nil, err
	}
	return append([]map[string]any(nil), items...), nil
}

func (f *fakeArr) add(op string, items *[]map[string]any, payload any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := decode(payload)
	f.calls = append(f.calls, call{op: op, payload: m})
	m["id"] = float64(f.nextID)
	f.nextID++
	*items = append(*items, m)
	return nil
}

func (f *fakeArr) update(op string, items *[]map[string]any, id int, payload any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := decode(payload)
	f.calls = append(f.calls, call{op: op, id: id, payload: m})
	for i, it := range *items {
		if got, _ := it["id"].(float64); int(got) == id {
			(*items)[i] = m
			return nil
		}
	}
	return fmt.Errorf("%s: no item with id %d", op, id)
}

func (f *fakeArr) ListDownloadClients(ctx context.Context) ([]map[string]any, error) {
	return f.list("ListDownloadClients", f.downloadClients)
}
func (f *fakeArr) AddDownloadClient(ctx context.Context, cfg any) error {
	return f.add("AddDownloadClient", &f.downloadClients, cfg)
}
func (f *fakeArr) UpdateDownloadClient(ctx context.Context, id int, p any) error {
	return f.update("UpdateDownloadClient", &f.downloadClients, id, p)
}
func (f *fakeArr) ListRootFolders(ctx context.Context) ([]map[string]any, error) {
	return f.list("ListRootFolders", f.rootFolders)
}
func (f *fakeArr) AddRootFolder(ctx context.Context, p any) error {
	return f.add("AddRootFolder", &f.rootFolders, p)
}
func (f *fakeArr) ListNotifications(ctx context.Context) ([]map[string]any, error) {
	return f.list("ListNotifications", f.notifications)
}
func (f *fakeArr) AddNotification(ctx context.Context, p any) error {
	return f.add("AddNotification", &f.notifications, p)
}
func (f *fakeArr) UpdateNotification(ctx context.Context, id int, p any) error {
	return f.update("UpdateNotification", &f.notifications, id, p)
}
func (f *fakeArr) ListApplications(ctx context.Context) ([]map[string]any, error) {
	return f.list("ListApplications", f.applications)
}
func (f *fakeArr) AddApplication(ctx context.Context, p any) error {
	return f.add("AddApplication", &f.applications, p)
}
func (f *fakeArr) UpdateApplication(ctx context.Context, id int, p any) error {
	return f.update("UpdateApplication", &f.applications, id, p)
}

// Calls returns the recorded calls with the given op.
func (f *fakeArr) Calls(op string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []call
	for _, c := range f.calls {
		if c.op == op {
			out = append(out, c)
		}
	}
	return out
}

// Mutations counts every Add*/Update* call.
func (f *fakeArr) Mutations() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeArr) Pings() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pings
}

// field returns the value of the named field of a decoded resource.
func field(t *testing.T, res map[string]any, name string) any {
	t.Helper()
	v, ok := fieldValue(res, name)
	if !ok {
		t.Fatalf("resource has no field %q: %v", name, res)
	}
	return v
}

type fakeQBT struct {
	mu        sync.Mutex
	pingFails int
	pings     int
}

func (f *fakeQBT) Ping(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pings++
	if f.pings <= f.pingFails {
		return errors.New("connection refused")
	}
	return nil
}

type fakeJellyfin struct {
	mu sync.Mutex

	wizardDone  bool
	libraries   []Library
	wizardErr   error
	listErr     error
	addErr      map[string]error // by library name
	statusErr   error
	pingFails   int
	pings       int
	calls       []string
	wizardUser  string
	wizardPass  string
	tokenSeen   []string
	addedByName map[string]Library
}

func newFakeJellyfin() *fakeJellyfin {
	return &fakeJellyfin{addErr: map[string]error{}, addedByName: map[string]Library{}}
}

func (f *fakeJellyfin) record(s string) { f.calls = append(f.calls, s) }

func (f *fakeJellyfin) Ping(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pings++
	if f.pings <= f.pingFails {
		return errors.New("connection refused")
	}
	return nil
}

func (f *fakeJellyfin) StartupWizardCompleted(ctx context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("StartupWizardCompleted")
	return f.wizardDone, f.statusErr
}

func (f *fakeJellyfin) CompleteStartupWizard(ctx context.Context, user, pass string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("CompleteStartupWizard")
	f.wizardUser, f.wizardPass = user, pass
	if f.wizardErr != nil {
		return f.wizardErr
	}
	f.wizardDone = true
	return nil
}

func (f *fakeJellyfin) ListLibraries(ctx context.Context, token string) ([]Library, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("ListLibraries")
	f.tokenSeen = append(f.tokenSeen, token)
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]Library(nil), f.libraries...), nil
}

func (f *fakeJellyfin) AddLibrary(ctx context.Context, token, name, collectionType, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("AddLibrary:" + name)
	f.tokenSeen = append(f.tokenSeen, token)
	if err := f.addErr[name]; err != nil {
		return err
	}
	lib := Library{Name: name, CollectionType: collectionType, Locations: []string{path}}
	f.libraries = append(f.libraries, lib)
	f.addedByName[name] = lib
	return nil
}

func (f *fakeJellyfin) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeJellyfin) Adds() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.addedByName)
}

type fakeTokens struct {
	mu    sync.Mutex
	fails int // Token calls that fail before success
	calls int
}

func (f *fakeTokens) Token(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls <= f.fails {
		return "", errors.New("401")
	}
	return "jf-token", nil
}

func (f *fakeTokens) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// stack bundles a full VPN-on set of fakes.
type stack struct {
	sonarr, radarr, prowlarr *fakeArr
	qbt                      *fakeQBT
	jf                       *fakeJellyfin
	tokens                   *fakeTokens
}

func newStack() *stack {
	return &stack{
		sonarr: newFakeArr(), radarr: newFakeArr(), prowlarr: newFakeArr(),
		qbt: &fakeQBT{}, jf: newFakeJellyfin(), tokens: &fakeTokens{},
	}
}

func (s *stack) deps() Deps {
	return Deps{
		Cfg:          testConfig(),
		Sonarr:       s.sonarr,
		Radarr:       s.radarr,
		Prowlarr:     s.prowlarr,
		QBT:          s.qbt,
		Jellyfin:     s.jf,
		JFAdmin:      s.tokens,
		SonarrAPIKey: "sonarr-key",
		RadarrAPIKey: "radarr-key",
		Log:          quietLogger(),
	}
}
