// Package cryptogen provides cryptographic generation helpers shared across
// multiple packages in pelicula-api.
package cryptogen

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// GenerateAPIKey returns a 32-character (16-byte) random hex string suitable
// for use as an API key.
func GenerateAPIKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Panic is intentional: a zero-key fallback would be a silent security failure.
		panic(fmt.Errorf("crypto/rand failed: %w", err))
	}
	return hex.EncodeToString(b)
}
