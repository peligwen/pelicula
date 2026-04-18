package main

// setup_helpers.go — crypto generators and type alias that remain in cmd/
// because they are used by multiple cmd/ files that have not yet been migrated.
//
//   - generateAPIKey: used by jellyfin_wiring.go, settings.go, and injected
//     into internal/app/setup.Handler
//   - generateReadablePassword: used by main.go (deps.GenPassword) and
//     injected into internal/app/setup.Handler
//   - cryptoRandN: helper only for generateReadablePassword
//   - SetupRequest: type alias so settings.go (handleSettingsReset) continues
//     to compile without importing the setup package directly

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"math/big"

	appsetup "pelicula-api/internal/app/setup"
)

// SetupRequest is a local alias for the canonical type in internal/app/setup.
// settings.go uses this type in handleSettingsReset to decode the same wizard
// body shape; keeping the alias avoids a new import in that file.
type SetupRequest = appsetup.SetupRequest

// generateReadablePassword returns a 4-word passphrase like "calm-tiger-sobre-leaps",
// drawn from weightedPassphraseWords (wordlist.go). All lowercase, hyphen-separated.
// 5-letter words are most likely; 3- and 7-letter words are rare (bell curve).
func generateReadablePassword() string {
	n := len(weightedPassphraseWords)
	return weightedPassphraseWords[cryptoRandN(n)] + "-" +
		weightedPassphraseWords[cryptoRandN(n)] + "-" +
		weightedPassphraseWords[cryptoRandN(n)] + "-" +
		weightedPassphraseWords[cryptoRandN(n)]
}

// cryptoRandN returns a cryptographically random integer in [0, n).
func cryptoRandN(n int) int {
	max := big.NewInt(int64(n))
	v, err := rand.Int(rand.Reader, max)
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

func generateAPIKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read should never fail; log and proceed with whatever
		// partial bytes were written (consistent with generateReadablePassword).
		slog.Error("crypto/rand.Read failed generating API key", "error", err)
	}
	return hex.EncodeToString(b)
}
