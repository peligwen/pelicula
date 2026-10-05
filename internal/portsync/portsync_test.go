package portsync

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSource struct {
	mu    sync.Mutex
	calls int
	// results[i] is returned on call i; the last entry repeats.
	ports []int
	errs  []error
}

func (f *fakeSource) GetForwardedPort(ctx context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	i := f.calls
	f.calls++
	if i >= len(f.ports) {
		i = len(f.ports) - 1
	}
	var err error
	if i < len(f.errs) {
		err = f.errs[i]
	}
	return f.ports[i], err
}

func (f *fakeSource) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeTarget struct {
	mu     sync.Mutex
	listen int
	sets   []int
	getErr error
	setErr error
}

func (f *fakeTarget) GetListenPort(ctx context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listen, f.getErr
}

func (f *fakeTarget) SetListenPort(ctx context.Context, port int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.sets = append(f.sets, port)
	f.listen = port
	return nil
}

func (f *fakeTarget) Sets() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.sets...)
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func testLogger(buf *syncBuffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// runFor starts Run and stops it once cond holds (or the test times out),
// returning after Run has exited.
func runFor(t *testing.T, src PortSource, dst PortTarget, log *slog.Logger, cond func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, src, dst, 5*time.Millisecond, log)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("condition not met before timeout")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

func TestRun_SyncsWhenDifferent(t *testing.T) {
	src := &fakeSource{ports: []int{51413}}
	dst := &fakeTarget{listen: 6881}
	var buf syncBuffer
	runFor(t, src, dst, testLogger(&buf), func() bool { return src.Calls() >= 4 })

	sets := dst.Sets()
	if len(sets) != 1 || sets[0] != 51413 {
		t.Fatalf("sets = %v, want exactly [51413]", sets)
	}
	// Only the change is logged, not the three no-op ticks that followed.
	if n := strings.Count(buf.String(), "listen port updated"); n != 1 {
		t.Errorf("change logged %d times, want 1\n%s", n, buf.String())
	}
	if !strings.Contains(buf.String(), "port=51413") {
		t.Errorf("log lacks the new port:\n%s", buf.String())
	}
}

func TestRun_NoopWhenEqual(t *testing.T) {
	src := &fakeSource{ports: []int{51413}}
	dst := &fakeTarget{listen: 51413}
	var buf syncBuffer
	runFor(t, src, dst, testLogger(&buf), func() bool { return src.Calls() >= 4 })

	if sets := dst.Sets(); len(sets) != 0 {
		t.Errorf("SetListenPort called %v, want never", sets)
	}
	if out := buf.String(); out != "" {
		t.Errorf("expected no log output, got:\n%s", out)
	}
}

func TestRun_IgnoresZeroPort(t *testing.T) {
	src := &fakeSource{ports: []int{0}}
	dst := &fakeTarget{listen: 6881}
	var buf syncBuffer
	runFor(t, src, dst, testLogger(&buf), func() bool { return src.Calls() >= 3 })

	if sets := dst.Sets(); len(sets) != 0 {
		t.Errorf("SetListenPort called %v for forwarded port 0", sets)
	}
	if out := buf.String(); out != "" {
		t.Errorf("expected no log output, got:\n%s", out)
	}
}

func TestRun_FollowsPortChanges(t *testing.T) {
	src := &fakeSource{ports: []int{0, 4000, 4000, 5000}}
	dst := &fakeTarget{listen: 6881}
	runFor(t, src, dst, nil, func() bool { return len(dst.Sets()) >= 2 })

	sets := dst.Sets()
	if len(sets) < 2 || sets[0] != 4000 || sets[1] != 5000 {
		t.Errorf("sets = %v, want [4000 5000]", sets)
	}
}

func TestRun_KeepsRunningAfterSourceError(t *testing.T) {
	boom := errors.New("gluetun down")
	src := &fakeSource{ports: []int{0, 0, 51413}, errs: []error{boom, boom}}
	dst := &fakeTarget{listen: 6881}
	var buf syncBuffer
	runFor(t, src, dst, testLogger(&buf), func() bool { return len(dst.Sets()) == 1 })

	if sets := dst.Sets(); len(sets) != 1 || sets[0] != 51413 {
		t.Fatalf("sets = %v, want [51413]", sets)
	}
	out := buf.String()
	if !strings.Contains(out, "gluetun down") {
		t.Errorf("error not logged:\n%s", out)
	}
	// The same failure twice in a row is logged once.
	if n := strings.Count(out, "port sync failed"); n != 1 {
		t.Errorf("repeated error logged %d times, want 1\n%s", n, out)
	}
}

func TestRun_KeepsRunningAfterTargetErrors(t *testing.T) {
	src := &fakeSource{ports: []int{51413}}
	dst := &fakeTarget{listen: 6881, setErr: errors.New("qbt refused")}
	var buf syncBuffer
	log := testLogger(&buf)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, src, dst, 5*time.Millisecond, log)
	}()
	for src.Calls() < 3 {
		time.Sleep(time.Millisecond)
	}
	// qBittorrent recovers: the next tick must apply the port.
	dst.mu.Lock()
	dst.setErr = nil
	dst.mu.Unlock()
	for len(dst.Sets()) == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done

	if sets := dst.Sets(); sets[0] != 51413 {
		t.Errorf("sets = %v", sets)
	}
	if !strings.Contains(buf.String(), "qbt refused") {
		t.Errorf("set error not logged:\n%s", buf.String())
	}
}

func TestRun_GetListenPortErrorDoesNotSet(t *testing.T) {
	src := &fakeSource{ports: []int{51413}}
	dst := &fakeTarget{listen: 6881, getErr: errors.New("qbt down")}
	var buf syncBuffer
	runFor(t, src, dst, testLogger(&buf), func() bool { return src.Calls() >= 3 })

	if sets := dst.Sets(); len(sets) != 0 {
		t.Errorf("SetListenPort called %v although the listen port was unknown", sets)
	}
	if !strings.Contains(buf.String(), "qbt down") {
		t.Errorf("error not logged:\n%s", buf.String())
	}
}

func TestRun_ImmediateFirstTick(t *testing.T) {
	src := &fakeSource{ports: []int{51413}}
	dst := &fakeTarget{listen: 6881}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, src, dst, time.Hour, nil) // the ticker never fires within the test
	}()
	deadline := time.Now().Add(3 * time.Second)
	for len(dst.Sets()) == 0 {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("no immediate sync")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
}

func TestRun_ReturnsWhenContextAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, &fakeSource{ports: []int{1}}, &fakeTarget{}, time.Hour, nil)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return")
	}
}
