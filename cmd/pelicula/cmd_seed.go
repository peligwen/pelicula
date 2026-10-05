package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// cmdSeed writes and re-enforces the service configs under a config
// directory without touching Docker. `pelicula up` does the same on every
// start; the command exists so the seeds can be inspected or applied on
// their own, and so the integration test can start compose directly without
// re-implementing them. It needs no .env and runs before platform detection.
func cmdSeed(args []string) {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "usage: pelicula seed <config_dir>")
		os.Exit(1)
	}
	configDir, err := filepath.Abs(args[0])
	if err != nil {
		fatal("resolve config dir: " + err.Error())
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		fatal("create config dir: " + err.Error())
	}
	if err := SeedAllConfigs(configDir); err != nil {
		fatal("Config seeding failed: " + err.Error())
	}
	fmt.Printf("Seeded service configs in %s\n", configDir)
}
