package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// resetNotes says what deleting each config directory loses.
var resetNotes = map[string]string{
	"sonarr":      "series library, history, API key",
	"radarr":      "movie library, history, API key",
	"prowlarr":    "indexers and API key",
	"qbittorrent": "client settings and torrent state",
	"jellyfin":    "users, libraries, watch history (media files are untouched)",
	"pelicula":    "Pelicula's database: roles, sessions, invites, requests, job history",
	"gluetun":     "cached VPN server list",
}

const resetUsage = "Usage: pelicula reset-config <svc|all> [--yes]\n  svc: sonarr | radarr | prowlarr | qbittorrent | jellyfin | pelicula | gluetun"

// parseResetArgs splits `reset-config` arguments into the target and --yes.
func parseResetArgs(args []string) (target string, yes bool, err error) {
	for _, a := range args {
		switch {
		case a == "--yes" || a == "-y":
			yes = true
		case strings.HasPrefix(a, "-"):
			return "", false, fmt.Errorf("unknown option %s", a)
		case target == "":
			target = a
		default:
			return "", false, fmt.Errorf("unexpected argument %s", a)
		}
	}
	if target == "" {
		return "", false, fmt.Errorf("missing target")
	}
	return target, yes, nil
}

// resetDirs returns the directories to delete for target ("all" or one name
// from configSubdirs), refusing a CONFIG_DIR that is not a safe place to
// delete from (empty, relative, the filesystem root, or the home directory).
func resetDirs(configDir, home, target string) ([]string, error) {
	clean := filepath.Clean(configDir)
	if configDir == "" || !filepath.IsAbs(clean) || filepath.Dir(clean) == clean || (home != "" && clean == filepath.Clean(home)) {
		return nil, fmt.Errorf("unsafe CONFIG_DIR %q — refusing to delete anything", configDir)
	}
	var names []string
	switch {
	case target == "all":
		names = configSubdirs
	case slices.Contains(configSubdirs, target):
		names = []string{target}
	default:
		return nil, fmt.Errorf("unknown target %q", target)
	}
	dirs := make([]string, len(names))
	for i, n := range names {
		dirs[i] = filepath.Join(clean, n)
	}
	return dirs, nil
}

// confirm asks a y/N question; anything but y/yes (or end of input) is no.
func confirm(in io.Reader, out io.Writer, prompt string) bool {
	fmt.Fprintf(out, "%s [y/N] ", prompt)
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// cmdResetConfig stops the stack and deletes one service's config directory
// (or all of them). .env and the media library are never touched.
func cmdResetConfig(ctx *Context, args []string) {
	target, yes, err := parseResetArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n%s\n", err, resetUsage)
		os.Exit(1)
	}
	ctx.LoadEnv()

	home, _ := os.UserHomeDir()
	dirs, err := resetDirs(ctx.Env["CONFIG_DIR"], home, target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n%s\n", err, resetUsage)
		os.Exit(1)
	}

	fmt.Printf("%sReset configuration: %s%s\n\nThis stops the stack and deletes:\n", colorBold, target, colorReset)
	for _, d := range dirs {
		fmt.Printf("  %s — %s\n", d, resetNotes[filepath.Base(d)])
	}
	fmt.Println("\n.env and your media library are kept.")
	if !yes && !confirm(os.Stdin, os.Stdout, "Continue?") {
		fmt.Println("Aborted.")
		return
	}

	progress("Stopping the stack...")
	if err := ctx.compose(true).Run("down", "--remove-orphans"); err != nil {
		warn("docker compose down failed: " + err.Error())
	}
	for _, d := range dirs {
		if err := os.RemoveAll(d); err != nil {
			fatal(fmt.Sprintf("Could not remove %s: %v (files written by containers may need sudo)", d, err))
		}
		info("Removed " + d)
	}
	ok("Config reset — run " + bold("pelicula up") + " to recreate and re-wire the stack.")
}
