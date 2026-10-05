package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"pelicula/internal/store"
)

// ---- fakes ----------------------------------------------------------------

type fakeArr struct {
	mu sync.Mutex

	history      []map[string]any
	historyErr   error
	movie        map[string]any
	episodeFiles []map[string]any
	panicOn      string // method name that panics

	queries         []string
	failedHistory   []int
	deletedMovies   []int
	deletedEpisodes []int
	commands        []map[string]any
	calls           int
}

func (f *fakeArr) enter(method string) {
	f.calls++
	if f.panicOn == method {
		panic("fake " + method + " exploded")
	}
}

func (f *fakeArr) TriggerCommand(_ context.Context, payload map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enter("TriggerCommand")
	f.commands = append(f.commands, payload)
	return nil
}

func (f *fakeArr) GetMovie(_ context.Context, _ int) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enter("GetMovie")
	return f.movie, nil
}

func (f *fakeArr) GetEpisodeFiles(_ context.Context, _ int) ([]map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enter("GetEpisodeFiles")
	return f.episodeFiles, nil
}

func (f *fakeArr) GetHistory(_ context.Context, query string) ([]map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enter("GetHistory")
	f.queries = append(f.queries, query)
	return f.history, f.historyErr
}

func (f *fakeArr) MarkHistoryFailed(_ context.Context, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enter("MarkHistoryFailed")
	f.failedHistory = append(f.failedHistory, id)
	return nil
}

func (f *fakeArr) DeleteMovieFile(_ context.Context, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enter("DeleteMovieFile")
	f.deletedMovies = append(f.deletedMovies, id)
	return nil
}

func (f *fakeArr) DeleteEpisodeFile(_ context.Context, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enter("DeleteEpisodeFile")
	f.deletedEpisodes = append(f.deletedEpisodes, id)
	return nil
}

func (f *fakeArr) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeJellyfin struct {
	mu     sync.Mutex
	tokens []string
}

func (f *fakeJellyfin) RefreshLibrary(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, token)
	return nil
}

func (f *fakeJellyfin) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tokens)
}

type fakeToken struct{}

func (fakeToken) Token(context.Context) (string, error) { return "tok", nil }

type failingToken struct{}

func (failingToken) Token(context.Context) (string, error) { return "", errors.New("no admin") }

// ---- environment ----------------------------------------------------------

type env struct {
	t      *testing.T
	st     *store.Store
	w      *Worker
	sonarr *fakeArr
	radarr *fakeArr
	jf     *fakeJellyfin
	dir    string
	cancel context.CancelFunc
	done   chan struct{}
}

// workerProbe prints a good probe for any path except ones containing "bad",
// where it exits non-zero like a corrupt file.
const workerProbe = `for last; do :; done
case "$last" in
*bad*) echo "invalid data found" >&2; exit 1;;
esac
cat <<'E'
{"streams":[{"index":0,"codec_type":"video","codec_name":"hevc","width":1280,"height":720},{"index":1,"codec_type":"audio","codec_name":"aac","tags":{"language":"eng"}}],"format":{"duration":"5400"}}
E
`

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{
		t:      t,
		st:     st,
		sonarr: &fakeArr{},
		radarr: &fakeArr{},
		jf:     &fakeJellyfin{},
		dir:    t.TempDir(),
		done:   make(chan struct{}),
	}
	e.w = New(Deps{
		Store:    st,
		Sonarr:   e.sonarr,
		Radarr:   e.radarr,
		Jellyfin: e.jf,
		JFAdmin:  fakeToken{},
		FFprobe:  writeScript(t, workerProbe),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	e.w.IdleWait = 20 * time.Millisecond
	e.w.RefreshDebounce = 50 * time.Millisecond
	return e
}

func (e *env) start() {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	go func() {
		defer close(e.done)
		e.w.Run(ctx)
	}()
	e.t.Cleanup(e.stop)
}

func (e *env) stop() {
	if e.cancel == nil {
		return
	}
	e.cancel()
	select {
	case <-e.done:
	case <-time.After(5 * time.Second):
		e.t.Error("worker did not stop")
	}
	e.cancel = nil
}

// media creates a 100 MB sparse file named name and returns its path.
func (e *env) media(name string) string {
	e.t.Helper()
	p := filepath.Join(e.dir, name)
	f, err := os.Create(p)
	if err != nil {
		e.t.Fatal(err)
	}
	f.Close()
	if err := os.Truncate(p, 100*mb); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func (e *env) enqueue(j store.Job) int64 {
	e.t.Helper()
	if err := e.st.EnqueueJob(context.Background(), &j); err != nil {
		e.t.Fatal(err)
	}
	return j.ID
}

func (e *env) waitStatus(id int64, want store.JobStatus) *store.Job {
	e.t.Helper()
	var j *store.Job
	waitFor(e.t, func() bool {
		var err error
		j, err = e.st.GetJob(context.Background(), id)
		return err == nil && j.Status == want
	}, "job %d to reach %s", id, want)
	return j
}

func waitFor(t *testing.T, cond func() bool, what string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for "+what, args...)
}

func (e *env) set(key, val string) {
	e.t.Helper()
	if err := e.st.SetSetting(context.Background(), key, val); err != nil {
		e.t.Fatal(err)
	}
}

func decodeResult(t *testing.T, j *store.Job) Result {
	t.Helper()
	var r Result
	if err := json.Unmarshal([]byte(j.Result), &r); err != nil {
		t.Fatalf("job result %q is not JSON: %v", j.Result, err)
	}
	return r
}

// ---- pass path ------------------------------------------------------------

func TestWorkerPassMarksRequestAvailableAndRefreshesOnce(t *testing.T) {
	e := newEnv(t)
	e.w.RefreshDebounce = 600 * time.Millisecond
	ctx := context.Background()

	req := &store.Request{MediaType: "movie", TmdbID: 1, Title: "Heat", RequestedBy: "ann"}
	if err := e.st.CreateRequest(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateRequestStatus(ctx, req.ID, store.RequestApproved, "bob", "", 42); err != nil {
		t.Fatal(err)
	}

	id1 := e.enqueue(store.Job{ArrType: "radarr", ArrID: 42, Title: "Heat", Path: e.media("heat.mkv"), Size: 100 * mb})
	id2 := e.enqueue(store.Job{ArrType: "radarr", ArrID: 43, Title: "Ronin", Path: e.media("ronin.mkv"), Size: 100 * mb})
	e.start()

	j1 := e.waitStatus(id1, store.JobPassed)
	e.waitStatus(id2, store.JobPassed)

	res := decodeResult(t, j1)
	if !res.Passed || res.Skipped || res.Video != "hevc" || res.Height != 720 {
		t.Errorf("result = %+v", res)
	}
	if j1.Error != "" {
		t.Errorf("error = %q", j1.Error)
	}
	got, err := e.st.GetRequest(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.RequestAvailable {
		t.Errorf("request status = %s, want available", got.Status)
	}

	waitFor(t, func() bool { return e.jf.count() >= 1 }, "jellyfin refresh")
	time.Sleep(time.Second) // well past a second debounce window
	if n := e.jf.count(); n != 1 {
		t.Errorf("refresh called %d times for two quick passes, want 1", n)
	}
	e.jf.mu.Lock()
	if e.jf.tokens[0] != "tok" {
		t.Errorf("refresh token = %q", e.jf.tokens[0])
	}
	e.jf.mu.Unlock()
	if n := e.radarr.callCount() + e.sonarr.callCount(); n != 0 {
		t.Errorf("passing jobs made %d *arr calls", n)
	}
}

func TestWorkerRefreshFiresAgainForLaterBurst(t *testing.T) {
	e := newEnv(t)
	e.start()
	id := e.enqueue(store.Job{ArrType: "radarr", ArrID: 1, Path: e.media("a.mkv"), Size: 100 * mb})
	e.waitStatus(id, store.JobPassed)
	waitFor(t, func() bool { return e.jf.count() == 1 }, "first refresh")

	id = e.enqueue(store.Job{ArrType: "radarr", ArrID: 2, Path: e.media("b.mkv"), Size: 100 * mb})
	e.w.Kick()
	e.waitStatus(id, store.JobPassed)
	waitFor(t, func() bool { return e.jf.count() == 2 }, "second refresh")
}

func TestWorkerValidationDisabledSkips(t *testing.T) {
	e := newEnv(t)
	e.set(store.SettingValidationEnabled, "false")
	// The file does not exist and would fail validation if it were checked.
	id := e.enqueue(store.Job{ArrType: "sonarr", ArrID: 3, Title: "Show", Path: filepath.Join(e.dir, "missing-bad.mkv")})
	e.start()

	j := e.waitStatus(id, store.JobPassed)
	res := decodeResult(t, j)
	if !res.Passed || !res.Skipped {
		t.Errorf("result = %+v, want passed+skipped", res)
	}
	if n := e.sonarr.callCount() + e.radarr.callCount(); n != 0 {
		t.Errorf("%d *arr calls for a skipped job", n)
	}
	waitFor(t, func() bool { return e.jf.count() == 1 }, "refresh after skipped pass")
}

// ---- failure path: radarr -------------------------------------------------

func radarrHistory() []map[string]any {
	return []map[string]any{
		{"id": float64(1), "downloadId": "AAA", "date": "2026-01-01T10:00:00Z"},
		{"id": float64(2), "downloadId": "BBB", "date": "2026-01-02T10:00:00Z"},
		{"id": float64(3), "downloadId": "CCC", "date": "2026-01-03T10:00:00Z"},
	}
}

func TestWorkerFailureRadarr(t *testing.T) {
	e := newEnv(t)
	path := e.media("bad-movie.mkv")
	e.radarr.history = radarrHistory()
	e.radarr.movie = map[string]any{"id": float64(7), "movieFile": map[string]any{"id": float64(55), "path": path}}

	id := e.enqueue(store.Job{ArrType: "radarr", ArrID: 7, Title: "Bad Movie", Path: path, Size: 100 * mb, DownloadID: "bbb"})
	e.start()
	j := e.waitStatus(id, store.JobFailed)
	waitFor(t, func() bool { e.radarr.mu.Lock(); defer e.radarr.mu.Unlock(); return len(e.radarr.commands) == 1 }, "search command")

	res := decodeResult(t, j)
	if res.Passed || res.Integrity != "fail" || !strings.Contains(res.Reason, "invalid data found") {
		t.Errorf("result = %+v", res)
	}
	if j.Error == "" || j.Error != res.Reason {
		t.Errorf("job error = %q, reason = %q", j.Error, res.Reason)
	}

	r := e.radarr
	r.mu.Lock()
	defer r.mu.Unlock()
	if !reflect.DeepEqual(r.queries, []string{"movieId=7&eventType=1"}) {
		t.Errorf("history queries = %v", r.queries)
	}
	if !reflect.DeepEqual(r.failedHistory, []int{2}) {
		t.Errorf("marked failed = %v, want the record whose downloadId matches case-insensitively", r.failedHistory)
	}
	if !reflect.DeepEqual(r.deletedMovies, []int{55}) {
		t.Errorf("deleted movie files = %v", r.deletedMovies)
	}
	wantCmd := map[string]any{"name": "MoviesSearch", "movieIds": []int{7}}
	if !reflect.DeepEqual(r.commands, []map[string]any{wantCmd}) {
		t.Errorf("commands = %v", r.commands)
	}
	if n := e.sonarr.callCount(); n != 0 {
		t.Errorf("sonarr got %d calls for a radarr job", n)
	}
	if e.jf.count() != 0 {
		t.Error("a failed job must not refresh Jellyfin")
	}
}

func TestWorkerFailureRadarrFallsBackToNewestGrab(t *testing.T) {
	e := newEnv(t)
	path := e.media("bad.mkv")
	e.radarr.history = radarrHistory()
	e.radarr.movie = map[string]any{"movieFile": map[string]any{"id": float64(9), "path": path}}

	// A webhook with no downloadId, and one whose id matches nothing.
	id1 := e.enqueue(store.Job{ArrType: "radarr", ArrID: 7, Path: path, Size: 100 * mb})
	e.start()
	e.waitStatus(id1, store.JobFailed)
	id2 := e.enqueue(store.Job{ArrType: "radarr", ArrID: 7, Path: e.media("bad2.mkv"), Size: 100 * mb, DownloadID: "ZZZ"})
	e.w.Kick()
	e.waitStatus(id2, store.JobFailed)
	waitFor(t, func() bool { e.radarr.mu.Lock(); defer e.radarr.mu.Unlock(); return len(e.radarr.commands) == 2 }, "both searches")

	e.radarr.mu.Lock()
	defer e.radarr.mu.Unlock()
	if !reflect.DeepEqual(e.radarr.failedHistory, []int{3, 3}) {
		t.Errorf("marked failed = %v, want newest grab (3) both times", e.radarr.failedHistory)
	}
}

func TestWorkerFailureRadarrNeverDeletesOtherFile(t *testing.T) {
	e := newEnv(t)
	path := e.media("bad.mkv")
	e.radarr.history = radarrHistory()
	// Radarr already upgraded to a different file; it is not ours to delete.
	e.radarr.movie = map[string]any{"movieFile": map[string]any{"id": float64(77), "path": "/media/movies/Other/other.mkv"}}

	id := e.enqueue(store.Job{ArrType: "radarr", ArrID: 7, Path: path, Size: 100 * mb, DownloadID: "AAA"})
	e.start()
	e.waitStatus(id, store.JobFailed)
	waitFor(t, func() bool { e.radarr.mu.Lock(); defer e.radarr.mu.Unlock(); return len(e.radarr.commands) == 1 }, "search")

	e.radarr.mu.Lock()
	defer e.radarr.mu.Unlock()
	if len(e.radarr.deletedMovies) != 0 {
		t.Errorf("deleted %v, want nothing", e.radarr.deletedMovies)
	}
	if !reflect.DeepEqual(e.radarr.failedHistory, []int{1}) {
		t.Errorf("marked failed = %v", e.radarr.failedHistory)
	}
}

func TestWorkerFailureStepsContinueAfterErrorAndPanic(t *testing.T) {
	e := newEnv(t)
	path := e.media("bad.mkv")
	e.radarr.historyErr = errors.New("history is down")
	e.radarr.movie = map[string]any{"movieFile": map[string]any{"id": float64(5), "path": path}}
	id := e.enqueue(store.Job{ArrType: "radarr", ArrID: 7, Path: path, Size: 100 * mb})
	e.start()
	e.waitStatus(id, store.JobFailed)
	waitFor(t, func() bool { e.radarr.mu.Lock(); defer e.radarr.mu.Unlock(); return len(e.radarr.commands) == 1 }, "search after history error")
	e.radarr.mu.Lock()
	if !reflect.DeepEqual(e.radarr.deletedMovies, []int{5}) || len(e.radarr.failedHistory) != 0 {
		t.Errorf("deleted=%v failed=%v", e.radarr.deletedMovies, e.radarr.failedHistory)
	}
	e.radarr.mu.Unlock()

	// A panicking step must not stop the later ones, nor kill the worker.
	e.radarr.mu.Lock()
	e.radarr.historyErr = nil
	e.radarr.history = radarrHistory()
	e.radarr.panicOn = "MarkHistoryFailed"
	e.radarr.mu.Unlock()
	id = e.enqueue(store.Job{ArrType: "radarr", ArrID: 7, Path: e.media("bad2.mkv"), Size: 100 * mb})
	e.w.Kick()
	e.waitStatus(id, store.JobFailed)
	waitFor(t, func() bool { e.radarr.mu.Lock(); defer e.radarr.mu.Unlock(); return len(e.radarr.commands) == 2 }, "search after panic")
}

// ---- failure path: sonarr -------------------------------------------------

func TestWorkerFailureSonarr(t *testing.T) {
	e := newEnv(t)
	path := e.media("bad-episode.mkv")
	e.sonarr.history = []map[string]any{
		{"id": float64(10), "downloadId": "OLD", "date": "2026-02-01T00:00:00Z"},
		{"id": float64(11), "downloadId": "ABCDEF", "date": "2026-01-01T00:00:00Z"},
	}
	e.sonarr.episodeFiles = []map[string]any{
		{"id": float64(100), "path": filepath.Join(e.dir, "other-episode.mkv")},
		{"id": float64(101), "path": path},
		{"id": float64(102), "path": filepath.Join(e.dir, "another.mkv")},
	}

	id := e.enqueue(store.Job{ArrType: "sonarr", ArrID: 3, EpisodeID: 9, Title: "Show S01E01", Path: path, Size: 100 * mb, DownloadID: "abcdef"})
	e.start()
	e.waitStatus(id, store.JobFailed)
	waitFor(t, func() bool { e.sonarr.mu.Lock(); defer e.sonarr.mu.Unlock(); return len(e.sonarr.commands) == 1 }, "search command")

	s := e.sonarr
	s.mu.Lock()
	defer s.mu.Unlock()
	if !reflect.DeepEqual(s.queries, []string{"seriesId=3&episodeId=9&eventType=1"}) {
		t.Errorf("history queries = %v", s.queries)
	}
	if !reflect.DeepEqual(s.failedHistory, []int{11}) {
		t.Errorf("marked failed = %v", s.failedHistory)
	}
	if !reflect.DeepEqual(s.deletedEpisodes, []int{101}) {
		t.Errorf("deleted episode files = %v, want only the one matching job.Path", s.deletedEpisodes)
	}
	wantCmd := map[string]any{"name": "EpisodeSearch", "episodeIds": []int{9}}
	if !reflect.DeepEqual(s.commands, []map[string]any{wantCmd}) {
		t.Errorf("commands = %v", s.commands)
	}
	if n := e.radarr.callCount(); n != 0 {
		t.Errorf("radarr got %d calls for a sonarr job", n)
	}
}

func TestWorkerFailureSonarrNoMatchingFile(t *testing.T) {
	e := newEnv(t)
	path := e.media("bad.mkv")
	e.sonarr.history = []map[string]any{{"id": 4, "downloadId": "X", "date": "2026-01-01T00:00:00Z"}} // plain int ids work too
	e.sonarr.episodeFiles = []map[string]any{{"id": float64(1), "path": "/tv/Show/other.mkv"}}
	id := e.enqueue(store.Job{ArrType: "sonarr", ArrID: 3, EpisodeID: 9, Path: path, Size: 100 * mb, DownloadID: "x"})
	e.start()
	e.waitStatus(id, store.JobFailed)
	waitFor(t, func() bool { e.sonarr.mu.Lock(); defer e.sonarr.mu.Unlock(); return len(e.sonarr.commands) == 1 }, "search")
	e.sonarr.mu.Lock()
	defer e.sonarr.mu.Unlock()
	if len(e.sonarr.deletedEpisodes) != 0 || !reflect.DeepEqual(e.sonarr.failedHistory, []int{4}) {
		t.Errorf("deleted=%v failed=%v", e.sonarr.deletedEpisodes, e.sonarr.failedHistory)
	}
}

// ---- settings and nil deps ------------------------------------------------

func TestWorkerAutoBlocklistOffMakesNoArrCalls(t *testing.T) {
	e := newEnv(t)
	e.set(store.SettingAutoBlocklist, "false")
	e.radarr.history = radarrHistory()
	id1 := e.enqueue(store.Job{ArrType: "radarr", ArrID: 7, Path: e.media("bad1.mkv"), Size: 100 * mb})
	id2 := e.enqueue(store.Job{ArrType: "sonarr", ArrID: 3, EpisodeID: 9, Path: e.media("bad2.mkv"), Size: 100 * mb})
	e.start()
	j1 := e.waitStatus(id1, store.JobFailed)
	e.waitStatus(id2, store.JobFailed)
	if j1.Error == "" {
		t.Error("failed job should still carry its reason")
	}
	if n := e.radarr.callCount() + e.sonarr.callCount(); n != 0 {
		t.Errorf("auto_blocklist=false but %d *arr calls were made", n)
	}
}

func TestWorkerNilDepsAreSkipped(t *testing.T) {
	st, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := &env{t: t, st: st, dir: t.TempDir(), done: make(chan struct{})}
	e.w = New(Deps{Store: st, FFprobe: writeScript(t, workerProbe), Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	e.w.IdleWait = 20 * time.Millisecond
	e.w.RefreshDebounce = 30 * time.Millisecond

	bad := e.enqueue(store.Job{ArrType: "radarr", ArrID: 1, Path: e.media("bad.mkv"), Size: 100 * mb})
	badTV := e.enqueue(store.Job{ArrType: "sonarr", ArrID: 1, EpisodeID: 2, Path: e.media("bad-tv.mkv"), Size: 100 * mb})
	odd := e.enqueue(store.Job{ArrType: "lidarr", ArrID: 1, Path: e.media("bad-odd.mkv"), Size: 100 * mb})
	good := e.enqueue(store.Job{ArrType: "radarr", ArrID: 2, Path: e.media("good.mkv"), Size: 100 * mb})
	e.start()
	for _, id := range []int64{bad, badTV, odd} {
		e.waitStatus(id, store.JobFailed)
	}
	e.waitStatus(good, store.JobPassed)
	time.Sleep(100 * time.Millisecond) // let the (skipped) refresh timer fire
}

func TestWorkerRefreshTokenFailureIsLogged(t *testing.T) {
	e := newEnv(t)
	e.w.d.JFAdmin = failingToken{}
	id := e.enqueue(store.Job{ArrType: "radarr", ArrID: 1, Path: e.media("good.mkv"), Size: 100 * mb})
	e.start()
	e.waitStatus(id, store.JobPassed)
	time.Sleep(150 * time.Millisecond)
	if n := e.jf.count(); n != 0 {
		t.Errorf("refresh called %d times without a token", n)
	}
}

// ---- resilience -----------------------------------------------------------

func TestWorkerPanicMarksJobFailedAndKeepsGoing(t *testing.T) {
	e := newEnv(t)
	real := e.w.validate
	e.w.validate = func(ctx context.Context, ffprobe, path string, size int64, runtimeMin int) Result {
		if strings.Contains(path, "boom") {
			panic("kaboom")
		}
		return real(ctx, ffprobe, path, size, runtimeMin)
	}
	boom := e.enqueue(store.Job{ArrType: "radarr", ArrID: 1, Path: e.media("boom.mkv"), Size: 100 * mb})
	good := e.enqueue(store.Job{ArrType: "radarr", ArrID: 2, Path: e.media("good.mkv"), Size: 100 * mb})
	e.start()

	j := e.waitStatus(boom, store.JobFailed)
	if !strings.Contains(j.Error, "kaboom") {
		t.Errorf("error = %q, want the panic message", j.Error)
	}
	if res := decodeResult(t, j); res.Passed || res.Reason == "" {
		t.Errorf("result = %+v", res)
	}
	e.waitStatus(good, store.JobPassed)

	// And the worker is still alive for jobs that arrive later.
	late := e.enqueue(store.Job{ArrType: "radarr", ArrID: 3, Path: e.media("late.mkv"), Size: 100 * mb})
	e.w.Kick()
	e.waitStatus(late, store.JobPassed)
}

func TestWorkerShutdownMidValidationDoesNotBlocklist(t *testing.T) {
	e := newEnv(t)
	started := make(chan struct{})
	e.w.validate = func(ctx context.Context, _, _ string, _ int64, _ int) Result {
		close(started)
		<-ctx.Done()
		return Result{Integrity: "fail", Reason: "ffprobe failed: " + ctx.Err().Error()}
	}
	id := e.enqueue(store.Job{ArrType: "radarr", ArrID: 7, Path: e.media("movie.mkv"), Size: 100 * mb})
	e.start()
	<-started
	e.stop()

	j, err := e.st.GetJob(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != store.JobRunning {
		t.Errorf("status = %s, want running so the next start requeues it", j.Status)
	}
	if n := e.radarr.callCount(); n != 0 {
		t.Errorf("interrupted job triggered %d *arr calls", n)
	}
}

func TestWorkerKickWakesIdleWorker(t *testing.T) {
	e := newEnv(t)
	e.w.IdleWait = time.Minute // only a Kick can make the worker prompt
	e.start()
	time.Sleep(150 * time.Millisecond) // let it find the queue empty and go to sleep

	id := e.enqueue(store.Job{ArrType: "radarr", ArrID: 1, Path: e.media("good.mkv"), Size: 100 * mb})
	start := time.Now()
	e.w.Kick()
	e.w.Kick() // extra kicks must not block
	e.waitStatus(id, store.JobPassed)
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("job took %v after Kick", d)
	}
}

func TestWorkerRunStopsOnContextCancel(t *testing.T) {
	e := newEnv(t)
	e.w.IdleWait = time.Minute
	e.start()
	time.Sleep(50 * time.Millisecond)
	e.stop() // fails the test if Run does not return
}

func TestWorkerRunWithoutStoreReturns(t *testing.T) {
	w := New(Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	done := make(chan struct{})
	go func() { w.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run with no store should return immediately")
	}
}

// ---- helpers --------------------------------------------------------------

func TestPickGrab(t *testing.T) {
	recs := []map[string]any{
		{"id": float64(1), "downloadId": "aaa", "date": "2026-01-01T00:00:00Z"},
		{"id": float64(2), "downloadId": "bbb", "date": "2026-03-01T00:00:00Z"},
		{"id": float64(3), "downloadId": "ccc", "date": "2026-02-01T00:00:00Z"},
	}
	idOf := func(m map[string]any) int {
		if m == nil {
			return -1
		}
		n, _ := toInt(m["id"])
		return n
	}
	if got := idOf(pickGrab(recs, "CCC")); got != 3 {
		t.Errorf("case-insensitive match = %d, want 3", got)
	}
	if got := idOf(pickGrab(recs, "nope")); got != 2 {
		t.Errorf("fallback = %d, want newest (2)", got)
	}
	if got := idOf(pickGrab(recs, "")); got != 2 {
		t.Errorf("no download id = %d, want newest (2)", got)
	}
	if got := idOf(pickGrab(nil, "x")); got != -1 {
		t.Errorf("empty history = %d, want nil", got)
	}
	// Undated records: the highest id wins.
	if got := idOf(pickGrab([]map[string]any{{"id": 5}, {"id": 8}, {"id": 6}}, "")); got != 8 {
		t.Errorf("undated fallback = %d, want 8", got)
	}
}

func TestToInt(t *testing.T) {
	for _, v := range []any{float64(12), 12, int64(12), json.Number("12"), "12"} {
		if n, ok := toInt(v); !ok || n != 12 {
			t.Errorf("toInt(%#v) = %d, %v", v, n, ok)
		}
	}
	for _, v := range []any{nil, "x", []int{1}} {
		if _, ok := toInt(v); ok {
			t.Errorf("toInt(%#v) should fail", v)
		}
	}
}
