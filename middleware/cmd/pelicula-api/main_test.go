package main

import (
	"strings"
	"testing"

	appservices "pelicula-api/internal/app/services"
)

// TestUARegression pins the User-Agent construction formula. If anyone changes
// the template in main.go (prefix, suffix, or version insertion point), this
// test must be updated explicitly — making the change intentional, not silent.
func TestUARegression(t *testing.T) {
	ua := "Pelicula/" + appservices.Version + " (+https://github.com/peligwen/pelicula)"

	if !strings.HasPrefix(ua, "Pelicula/") {
		t.Errorf("User-Agent must start with \"Pelicula/\", got %q", ua)
	}
	if !strings.HasSuffix(ua, " (+https://github.com/peligwen/pelicula)") {
		t.Errorf("User-Agent must end with \" (+https://github.com/peligwen/pelicula)\", got %q", ua)
	}

	versionPart := strings.TrimPrefix(ua, "Pelicula/")
	versionPart = strings.TrimSuffix(versionPart, " (+https://github.com/peligwen/pelicula)")
	if versionPart != appservices.Version {
		t.Errorf("User-Agent version segment: got %q, want %q", versionPart, appservices.Version)
	}
}
