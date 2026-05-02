package procula

import "time"

// SetRefreshDebounceForTest overrides the debounce window for a single test.
// Call via t.Cleanup to restore the original value.
func SetRefreshDebounceForTest(d time.Duration) { refreshDebounceMs = d }

// SetPreferredAudioLangForTest overrides the preferred audio language for a single test.
// Call via t.Cleanup to restore the original value.
func SetPreferredAudioLangForTest(s string) { preferredAudioLangVal = s }
