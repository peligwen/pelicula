package util

import (
	"context"
	"math/rand"
	"time"
)

// JitteredDuration returns d +/- pct*d of random jitter (pct in range 0–1).
func JitteredDuration(d time.Duration, pct float64) time.Duration {
	jitter := time.Duration(float64(d) * pct * (rand.Float64()*2 - 1))
	return d + jitter
}

// JitteredTicker returns a channel that fires approximately every base interval
// with ±pct jitter applied per tick. The goroutine stops when ctx is done.
// The channel is never closed — let it be GC'd when the goroutine exits.
func JitteredTicker(ctx context.Context, base time.Duration, pct float64) <-chan time.Time {
	ch := make(chan time.Time, 1)
	go func() {
		for {
			timer := time.NewTimer(JitteredDuration(base, pct))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case t := <-timer.C:
				select {
				case ch <- t:
				case <-ctx.Done():
					return
				default: // drop if consumer hasn't caught up
				}
			}
		}
	}()
	return ch
}
