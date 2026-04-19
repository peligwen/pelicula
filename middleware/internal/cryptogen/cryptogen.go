// Package cryptogen provides cryptographic generation helpers shared across
// multiple packages in pelicula-api.
package cryptogen

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
)

// GenerateAPIKey returns a 32-character (16-byte) random hex string suitable
// for use as an API key.
func GenerateAPIKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read should never fail on any supported platform.
		slog.Error("crypto/rand.Read failed generating API key", "error", err)
	}
	return hex.EncodeToString(b)
}
