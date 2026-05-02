package sysinfo

import "time"

// ResetSpeedTestState clears all package-level speedtest state between tests.
// Only for use in tests.
func ResetSpeedTestState() {
	speedTestMu.Lock()
	defer speedTestMu.Unlock()
	speedTestLastRun = time.Time{}
	speedTestLastResult = nil
	speedTestInflight = false
}

// SetSpeedTestURL overrides the download URL for tests.
func SetSpeedTestURL(u string) { speedTestURL = u }

// SetSpeedTestProxyDirect disables the HTTP proxy for tests (direct connection).
// Call ResetSpeedTestState to restore defaults.
func SetSpeedTestProxyDirect() {
	empty := ""
	speedTestProxyOverride = &empty
}

// ClearSpeedTestProxyOverride restores env-driven proxy resolution.
func ClearSpeedTestProxyOverride() { speedTestProxyOverride = nil }
