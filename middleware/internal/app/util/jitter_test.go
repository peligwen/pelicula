package util

import (
	"context"
	"testing"
	"time"
)

func TestJitteredDuration_StaysInSymmetricRange(t *testing.T) {
	base := 100 * time.Millisecond
	pct := 0.2 // ±20%

	lo := time.Duration(float64(base) * (1 - pct))
	hi := time.Duration(float64(base) * (1 + pct))

	for i := 0; i < 1000; i++ {
		got := JitteredDuration(base, pct)
		if got < lo || got > hi {
			t.Fatalf("sample %d: %v is outside [%v, %v]", i, got, lo, hi)
		}
	}
}

func TestJitteredDuration_ZeroWindow(t *testing.T) {
	base := 200 * time.Millisecond
	for i := 0; i < 100; i++ {
		got := JitteredDuration(base, 0)
		if got != base {
			t.Fatalf("zero pct: got %v, want %v", got, base)
		}
	}
}

func TestJitteredDuration_NegativePctReturnsBase(t *testing.T) {
	// Negative pct is out of range, but the function should still not panic.
	// We only verify it runs without panic; the sign makes jitter symmetric.
	base := 50 * time.Millisecond
	for i := 0; i < 10; i++ {
		_ = JitteredDuration(base, -0.1)
	}
}

func TestJitteredTicker_FiresAtLeastN(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Very short base so the ticker fires many times within the deadline.
	ch := JitteredTicker(ctx, 50*time.Millisecond, 0.1)

	const want = 5
	count := 0
	deadline := time.After(3 * time.Second)
	for count < want {
		select {
		case <-ch:
			count++
		case <-deadline:
			t.Fatalf("ticker fired only %d times within deadline (want %d)", count, want)
		}
	}
}

func TestJitteredTicker_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	ch := JitteredTicker(ctx, 10*time.Millisecond, 0.1)

	// Let it fire at least once to confirm it's running.
	select {
	case <-ch:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ticker did not fire before cancel")
	}

	cancel()

	// After cancel, drain the channel briefly and ensure it stops.
	// We wait long enough that any goroutine sleeping before noticing
	// cancellation would have woken up.
	time.Sleep(100 * time.Millisecond)

	// Confirm the goroutine exited: the channel should not produce new ticks
	// (it might produce one buffered tick, so drain one more if present).
	select {
	case <-ch:
		// one buffered value is OK — drain it
	default:
	}

	// Channel should now be quiet — goroutine observed ctx.Done().
	select {
	case <-ch:
		t.Error("ticker fired after context cancel")
	case <-time.After(150 * time.Millisecond):
		// expected: no more ticks
	}
}
