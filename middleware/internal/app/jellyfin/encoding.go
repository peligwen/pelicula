package jellyfin

import (
	"log/slog"
	"strings"
)

// HwAccelType is a Jellyfin hardware acceleration mode identifier.
type HwAccelType string

const (
	HwAccelNone         HwAccelType = "none"
	HwAccelVaapi        HwAccelType = "vaapi"
	HwAccelQSV          HwAccelType = "qsv"
	HwAccelVideoToolbox HwAccelType = "videotoolbox"

	vaapiDevice = "/dev/dri/renderD128"
)

// HwAccelProbe returns the probe-detected hardware acceleration type and,
// when VAAPI is chosen, the device path to configure in Jellyfin.
//
// Probe order:
//  1. envValue (os.Getenv("PELICULA_JELLYFIN_HWACCEL")) — accepted values:
//     vaapi, qsv, videotoolbox, none. Unknown values log a warning and
//     fail closed (treated as none) so a typo never silently activates
//     hardware acceleration the user did not request.
//  2. statFile("/dev/dri/renderD128") succeeds → vaapi.
//  3. goos == "darwin" && goarch == "arm64" → videotoolbox.
//  4. none.
//
// envValue, statFile, goos, and goarch are injected for testability.
func HwAccelProbe(envValue string, statFile func(string) error, goos, goarch string) (HwAccelType, string) {
	if envValue != "" {
		switch HwAccelType(strings.ToLower(envValue)) {
		case HwAccelNone:
			return HwAccelNone, ""
		case HwAccelVaapi:
			return HwAccelVaapi, vaapiDevice
		case HwAccelQSV:
			return HwAccelQSV, ""
		case HwAccelVideoToolbox:
			return HwAccelVideoToolbox, ""
		default:
			slog.Warn("unknown PELICULA_JELLYFIN_HWACCEL value, treating as none",
				"component", "autowire", "value", envValue)
			return HwAccelNone, ""
		}
	}

	if statFile(vaapiDevice) == nil {
		return HwAccelVaapi, vaapiDevice
	}

	if goos == "darwin" && goarch == "arm64" {
		return HwAccelVideoToolbox, ""
	}

	return HwAccelNone, ""
}
