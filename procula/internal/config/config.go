// Package config provides environment variable loading and path resolution for procula.
package config

import (
	"os"
	"strings"
)

// Config holds all runtime configuration for procula, read from environment variables.
type Config struct {
	ListenAddr    string
	DBPath        string
	ConfigDir     string
	PeliculaAPI   string
	ProculaAPIKey string
	FFmpegPath    string
	FFprobePath   string
	BazarrURL     string
	SubLangs      string
	AudioLang     string
	Version       string
}

// Load reads configuration from environment variables with sensible defaults.
func Load() Config {
	configDir := Env("CONFIG_DIR", "/config")
	return Config{
		ListenAddr:    Env("PROCULA_LISTEN_ADDR", ":8282"),
		DBPath:        configDir + "/procula.db",
		ConfigDir:     configDir,
		PeliculaAPI:   Env("PELICULA_API_URL", "http://pelicula-api:8181"),
		ProculaAPIKey: Env("PROCULA_API_KEY", ""),
		FFmpegPath:    Env("FFMPEG_PATH", "ffmpeg"),
		FFprobePath:   Env("FFPROBE_PATH", "ffprobe"),
		BazarrURL:     Env("BAZARR_URL", "http://bazarr:6767/bazarr"),
		SubLangs:      Env("PELICULA_SUB_LANGS", ""),
		AudioLang:     Env("PELICULA_AUDIO_LANG", "en"),
		Version:       Env("PROCULA_VERSION", "dev"),
	}
}

// Env returns the value of the environment variable named by key,
// trimmed of whitespace, or fallback when the variable is unset or empty.
func Env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
