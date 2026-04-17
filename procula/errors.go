// errors.go — error classification and exponential-backoff helpers for the
// procula retry policy.
package main

import (
	"errors"
	"strings"
	"time"
)

// maxTransientRetries is the number of transient-failure retries before a job
// is permanently failed. A job exceeding this count gets state="failed" with
// error="max retries exceeded (transient)".
const maxTransientRetries = 4

// backoffDuration returns the delay before the next attempt given the current
// retry_count (already incremented before this call).
//
//	count 1 → 1 min
//	count 2 → 5 min
//	count 3 → 30 min
//	count 4 → 2 hours
//	count 5+ → treated as permanent by the caller
func backoffDuration(retryCount int) time.Duration {
	switch retryCount {
	case 1:
		return 1 * time.Minute
	case 2:
		return 5 * time.Minute
	case 3:
		return 30 * time.Minute
	default:
		return 2 * time.Hour
	}
}

// permanentErrStrings are substrings that identify non-retriable failures.
var permanentErrStrings = []string{
	// FFprobe/FFmpeg binary not found
	"executable file not found",
	"no such file or directory",
	// Unsupported codec detected during validation/transcode
	"unsupported codec",
	"codec not found",
	// Source file missing at job execution time
	"file not found",
	"stat: no such file",
}

// transientErrStrings are substrings that identify retriable failures.
var transientErrStrings = []string{
	// HTTP errors from Bazarr or other external services
	"status 502",
	"status 503",
	"status 504",
	"status 500",
	"connection refused",
	"connection reset",
	"context deadline exceeded",
	"i/o timeout",
	"timeout",
	// FFmpeg OOM (Linux OOM killer, exit code 137)
	"exit status 137",
	"signal: killed",
	// Temp-file write failures
	"no space left on device",
	"write /tmp",
}

// IsPermanentError reports whether err represents a non-retriable failure.
// The check is performed on the full unwrapped error chain and on the
// flat string representation.
func IsPermanentError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(flattenErr(err))
	for _, s := range permanentErrStrings {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// IsTransientError reports whether err represents a retriable failure.
// Returns false for permanent errors even if a transient substring matches.
func IsTransientError(err error) bool {
	if err == nil {
		return false
	}
	if IsPermanentError(err) {
		return false
	}
	msg := strings.ToLower(flattenErr(err))
	for _, s := range transientErrStrings {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// flattenErr walks the error chain and concatenates all messages.
func flattenErr(err error) string {
	var parts []string
	for e := err; e != nil; e = errors.Unwrap(e) {
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, " ")
}
