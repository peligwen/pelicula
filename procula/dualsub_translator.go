package procula

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Translator translates a single plain-text string from one language to another.
type Translator interface {
	Translate(ctx context.Context, text, fromLang, toLang string) (string, error)
}

// newTranslator returns the requested Translator implementation.
// Unknown modes fall back to NoneTranslator (which returns an error on use).
func newTranslator(mode, configDir string) Translator {
	switch mode {
	case "argos":
		return &ArgosTranslator{cacheDir: filepath.Join(configDir, "dualsub-cache")}
	default:
		return NoneTranslator{}
	}
}

// NoneTranslator is a no-op that always returns an error.
// Used when DUALSUB_TRANSLATOR=none (or unset), signalling that translation
// should not be attempted and missing subtitle tracks are a hard stop.
type NoneTranslator struct{}

func (NoneTranslator) Translate(_ context.Context, _, _, _ string) (string, error) {
	return "", fmt.Errorf("no translator configured (set DUALSUB_TRANSLATOR=argos to enable)")
}

// ArgosTranslator shells out to the argos-translate CLI.
// Results are cached per (fromLang, toLang, text) in configDir/dualsub-cache/
// so repeated pipeline runs don't re-translate identical cues.
type ArgosTranslator struct {
	cacheDir string
}

func (t *ArgosTranslator) Translate(ctx context.Context, text, fromLang, toLang string) (string, error) {
	if text == "" {
		return "", nil
	}

	// Check cache first
	key := t.cacheKey(text, fromLang, toLang)
	cachePath := filepath.Join(t.cacheDir, key+".txt")
	if cached, err := os.ReadFile(cachePath); err == nil {
		return string(cached), nil
	}

	// Per-cue timeout: a hung argos-translate process must not block the whole
	// job forever. The pipeline ctx (passed in) will also cancel on job cancel.
	cueCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// Invoke argos-translate CLI. Text is passed via stdin to avoid shell
	// quoting issues with arbitrary subtitle content.
	cmd := exec.CommandContext(cueCtx, "argos-translate", "--from-lang", fromLang, "--to-lang", toLang)
	cmd.Stdin = strings.NewReader(text)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("argos-translate %s→%s: %w", fromLang, toLang, err)
	}
	result := strings.TrimSpace(string(out))

	// Write cache (best-effort — don't fail the job if the write fails)
	if err := os.MkdirAll(t.cacheDir, 0755); err == nil {
		if werr := os.WriteFile(cachePath, []byte(result), 0644); werr != nil {
			slog.Warn("could not write translator cache", "component", "dualsub", "path", cachePath, "error", werr)
		}
	}

	return result, nil
}

func (t *ArgosTranslator) cacheKey(text, fromLang, toLang string) string {
	h := sha256.Sum256([]byte(fromLang + "\x00" + toLang + "\x00" + text))
	return hex.EncodeToString(h[:16]) // 32 hex chars — enough for uniqueness
}
