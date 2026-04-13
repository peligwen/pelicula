package main

import "testing"

func TestFormatUptime(t *testing.T) {
	cases := []struct {
		secs float64
		want string
	}{
		{0, "0h 0m"},
		{59, "0h 0m"},
		{60, "0h 1m"},
		{3599, "0h 59m"},
		{3600, "1h 0m"},
		{3661, "1h 1m"},
		{86399, "23h 59m"},
		{86400, "1d 0h"},
		{86400 + 3600, "1d 1h"},
		{3*86400 + 4*3600 + 30*60, "3d 4h"},
		{7 * 86400, "7d 0h"},
	}
	for _, tc := range cases {
		got := FormatUptime(tc.secs)
		if got != tc.want {
			t.Errorf("FormatUptime(%v) = %q, want %q", tc.secs, got, tc.want)
		}
	}
}
