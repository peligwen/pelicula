package jellyfin

import (
	"errors"
	"testing"
)

func errStat(_ string) error { return errors.New("not found") }
func okStat(_ string) error  { return nil }

func TestHwAccelProbe(t *testing.T) {
	tests := []struct {
		name       string
		env        string
		stat       func(string) error
		goos       string
		goarch     string
		wantType   HwAccelType
		wantDevice string
	}{
		{
			name:       "env vaapi",
			env:        "vaapi",
			stat:       errStat,
			goos:       "linux",
			goarch:     "amd64",
			wantType:   HwAccelVaapi,
			wantDevice: vaapiDevice,
		},
		{
			name:       "env vaapi uppercase",
			env:        "VAAPI",
			stat:       errStat,
			goos:       "linux",
			goarch:     "amd64",
			wantType:   HwAccelVaapi,
			wantDevice: vaapiDevice,
		},
		{
			name:       "env qsv",
			env:        "qsv",
			stat:       errStat,
			goos:       "linux",
			goarch:     "amd64",
			wantType:   HwAccelQSV,
			wantDevice: "",
		},
		{
			name:       "env videotoolbox",
			env:        "videotoolbox",
			stat:       errStat,
			goos:       "darwin",
			goarch:     "arm64",
			wantType:   HwAccelVideoToolbox,
			wantDevice: "",
		},
		{
			name:       "env none",
			env:        "none",
			stat:       okStat, // stat would succeed but env wins
			goos:       "linux",
			goarch:     "amd64",
			wantType:   HwAccelNone,
			wantDevice: "",
		},
		{
			name:       "env unknown fails closed even when stat would succeed",
			env:        "typo",
			stat:       okStat,
			goos:       "linux",
			goarch:     "amd64",
			wantType:   HwAccelNone,
			wantDevice: "",
		},
		{
			name:       "env unknown fails closed even on arm64 darwin",
			env:        "typo",
			stat:       errStat,
			goos:       "darwin",
			goarch:     "arm64",
			wantType:   HwAccelNone,
			wantDevice: "",
		},
		{
			name:       "env unknown fails closed",
			env:        "typo",
			stat:       errStat,
			goos:       "linux",
			goarch:     "amd64",
			wantType:   HwAccelNone,
			wantDevice: "",
		},
		{
			name:       "no env vaapi probe",
			env:        "",
			stat:       okStat,
			goos:       "linux",
			goarch:     "amd64",
			wantType:   HwAccelVaapi,
			wantDevice: vaapiDevice,
		},
		{
			name:       "no env videotoolbox darwin arm64",
			env:        "",
			stat:       errStat,
			goos:       "darwin",
			goarch:     "arm64",
			wantType:   HwAccelVideoToolbox,
			wantDevice: "",
		},
		{
			name:       "no env none linux amd64 no dev",
			env:        "",
			stat:       errStat,
			goos:       "linux",
			goarch:     "amd64",
			wantType:   HwAccelNone,
			wantDevice: "",
		},
		{
			name:       "darwin amd64 no dev is none not videotoolbox",
			env:        "",
			stat:       errStat,
			goos:       "darwin",
			goarch:     "amd64",
			wantType:   HwAccelNone,
			wantDevice: "",
		},
		{
			name:       "darwin arm64 with dev prefers vaapi",
			env:        "",
			stat:       okStat,
			goos:       "darwin",
			goarch:     "arm64",
			wantType:   HwAccelVaapi,
			wantDevice: vaapiDevice,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotType, gotDevice := HwAccelProbe(tc.env, tc.stat, tc.goos, tc.goarch)
			if gotType != tc.wantType {
				t.Errorf("type: got %q, want %q", gotType, tc.wantType)
			}
			if gotDevice != tc.wantDevice {
				t.Errorf("device: got %q, want %q", gotDevice, tc.wantDevice)
			}
		})
	}
}
