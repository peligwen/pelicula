package main

// setup_helpers.go — crypto generators that remain in cmd/
// because they are used by multiple cmd/ files that have not yet been migrated.
//
//   - generateAPIKey: used by jellyfin_wiring.go and injected into
//     internal/app/setup.Handler and internal/app/settings.Handler
//   - generateReadablePassword: used by main.go (deps.GenPassword) and
//     injected into internal/app/setup.Handler
//   - cryptoRandN: helper only for generateReadablePassword

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"math/big"
)

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
