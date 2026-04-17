package main

import (
	"context"
	"errors"
	"testing"
)

func TestIsPermanentError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "ffmpeg binary not found",
			err:  errors.New("exec: ffmpeg: executable file not found in $PATH"),
			want: true,
		},
		{
			name: "ffprobe binary not found",
			err:  errors.New("exec: ffprobe: executable file not found in $PATH"),
			want: true,
		},
		{
			name: "file not found",
			err:  errors.New("open /media/movies/foo.mkv: no such file or directory"),
			want: true,
		},
		{
			name: "stat no such file",
			err:  errors.New("stat: no such file"),
			want: true,
		},
		{
			name: "explicit file not found",
			err:  errors.New("file not found: /downloads/movie.mkv"),
			want: true,
		},
		{
			name: "connection refused — transient, not permanent",
			err:  errors.New("dial tcp: connection refused"),
			want: false,
		},
		{
			name: "timeout — transient, not permanent",
			err:  errors.New("i/o timeout"),
			want: false,
		},
		{
			name: "context.Canceled — not permanent",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "context.DeadlineExceeded — not permanent",
			err:  context.DeadlineExceeded,
			want: false,
		},
		{
			name: "unknown error — not permanent",
			err:  errors.New("something unexpected happened"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsPermanentError(tc.err)
			if got != tc.want {
				t.Errorf("IsPermanentError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsTransientError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "connection refused",
			err:  errors.New("dial tcp: connection refused"),
			want: true,
		},
		{
			name: "i/o timeout",
			err:  errors.New("read tcp: i/o timeout"),
			want: true,
		},
		{
			name: "deadline exceeded",
			err:  errors.New("context deadline exceeded"),
			want: true,
		},
		{
			name: "bazarr 502",
			err:  errors.New("bazarr returned status 502 Bad Gateway"),
			want: true,
		},
		{
			name: "bazarr 503",
			err:  errors.New("bazarr returned status 503 Service Unavailable"),
			want: true,
		},
		{
			name: "context.DeadlineExceeded sentinel",
			err:  context.DeadlineExceeded,
			want: true,
		},
		// Permanent errors must NOT be classified as transient even if they
		// accidentally match a transient substring.
		{
			name: "ffmpeg not found — permanent, not transient",
			err:  errors.New("exec: ffmpeg: executable file not found in $PATH"),
			want: false,
		},
		{
			name: "file not found — permanent, not transient",
			err:  errors.New("no such file or directory"),
			want: false,
		},
		// context.Canceled is neither permanent nor transient — cancellations
		// should not mark jobs as failed permanently.
		{
			name: "context.Canceled — neither permanent nor transient",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "unknown error defaults to transient (not in permanent list)",
			err:  errors.New("something unexpected happened"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsTransientError(tc.err)
			if got != tc.want {
				t.Errorf("IsTransientError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestContextCanceledNotPermanent is a focused regression test: context.Canceled
// must not be classified as permanent. If it were, a cancelled job would be
// marked StateFailed instead of StateCancelled.
func TestContextCanceledNotPermanent(t *testing.T) {
	if IsPermanentError(context.Canceled) {
		t.Error("IsPermanentError(context.Canceled) = true — cancellations would be marked as permanent failures")
	}
	if IsTransientError(context.Canceled) {
		t.Error("IsTransientError(context.Canceled) = true — cancellations would be re-queued with backoff")
	}
}
