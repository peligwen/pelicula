package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// EnvMap is a string->string map of .env key/value pairs.
type EnvMap map[string]string

// envSection is one commented block of the generated .env. The same layout
// renders .env.example (see TestEnvExampleMatchesLayout), so the two cannot
// drift apart.
type envSection struct {
	title    string
	comments []string
	keys     []string
}

const envHeader = `# Pelicula stack environment
# ──────────────────────────────────────────────────────────────────────────────
# Created by ` + "`pelicula up`" + ` on first run. Edit freely, then run ` + "`pelicula up`" + ` again.
# This file holds secrets and is gitignored — keep it private.
# Leave a secret blank and the next ` + "`pelicula up`" + ` generates it.
# ──────────────────────────────────────────────────────────────────────────────`

var envLayout = []envSection{
	{
		title: "Paths (absolute)",
		comments: []string{
			"CONFIG_DIR  — service configs and databases. Keep it on fast local disk.",
			"LIBRARY_DIR — the media library; movies/ and tv/ live here.",
			"WORK_DIR    — downloads/ live here. Same filesystem as LIBRARY_DIR lets",
			"              Sonarr/Radarr hardlink imports instead of copying.",
		},
		keys: []string{"CONFIG_DIR", "LIBRARY_DIR", "WORK_DIR"},
	},
	{
		title: "Host identity",
		comments: []string{
			"PUID/PGID — owner of the files the containers write (run `id`).",
			"Synology DSM default: PUID=1026, PGID=100.",
		},
		keys: []string{"PUID", "PGID", "TZ"},
	},
	{
		title: "ProtonVPN / WireGuard",
		comments: []string{
			"Requires ProtonVPN Plus or higher (the free tier has no P2P or port forwarding).",
			"Create the key at Settings → Downloads → WireGuard configuration and do NOT",
			"enable \"Moderate NAT\" — it breaks port forwarding.",
			"Leave the key blank to run without a VPN: Sonarr, Radarr and Jellyfin start,",
			"but there is no download client or indexer manager.",
		},
		keys: []string{"WIREGUARD_PRIVATE_KEY", "SERVER_COUNTRIES"},
	},
	{
		title: "Dashboard",
		comments: []string{
			"PELICULA_PORT — host port nginx binds. 7354 spells PELI on a phone keypad.",
			"PELICULA_PROJECT_NAME — Docker Compose project name; change it only to run",
			"two stacks on one host.",
		},
		keys: []string{"PELICULA_PORT", "PELICULA_PROJECT_NAME"},
	},
	{
		title: "Jellyfin admin",
		comments: []string{
			"Created on first start and used to sign in to the dashboard as admin.",
		},
		keys: []string{"JELLYFIN_ADMIN_USER", "JELLYFIN_PASSWORD"},
	},
	{
		title: "Internal secrets (generated)",
		comments: []string{
			"WEBHOOK_SECRET — Sonarr/Radarr send it to Pelicula in X-Webhook-Secret.",
			"GLUETUN_HTTP_USER/PASS — credentials for gluetun's control API.",
		},
		keys: []string{"WEBHOOK_SECRET", "GLUETUN_HTTP_USER", "GLUETUN_HTTP_PASS"},
	},
}

// optionalEnvKeys are written commented out (as `# KEY=default`) while unset.
var optionalEnvKeys = map[string]string{"PELICULA_PROJECT_NAME": "pelicula"}

// ParseEnv reads a .env file into a map. Blank lines and # comments are
// skipped; values may be wrapped in single or double quotes. Unquoted values
// end at a " #" inline comment. A repeated key keeps its last value.
func ParseEnv(path string) (EnvMap, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	m := make(EnvMap)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			continue
		}
		key := strings.TrimPrefix(strings.TrimSpace(line[:idx]), "export ")
		m[strings.TrimSpace(key)] = parseEnvValue(strings.TrimSpace(line[idx+1:]))
	}
	return m, scanner.Err()
}

func parseEnvValue(val string) string {
	if len(val) >= 2 {
		if q := val[0]; (q == '"' || q == '\'') && val[len(val)-1] == q {
			return val[1 : len(val)-1]
		}
	}
	if i := strings.Index(val, " #"); i >= 0 {
		val = strings.TrimSpace(val[:i])
	}
	return val
}

// sanitizeEnvValue removes characters that would break the KEY="value" format.
func sanitizeEnvValue(v string) string {
	v = strings.ReplaceAll(v, `"`, "")
	v = strings.ReplaceAll(v, "\n", "")
	return strings.ReplaceAll(v, "\r", "")
}

// sectionRule returns a "# ── Title ───…" divider padded to 80 columns.
func sectionRule(title string) string {
	prefix := "# ── " + title + " "
	return prefix + strings.Repeat("─", max(3, 80-utf8.RuneCountInString(prefix)))
}

// renderEnv renders m using envLayout. Every layout key is written (empty
// ones as KEY="") so compose never warns about an unset variable; optional
// keys are commented out while unset. Keys outside the layout are kept,
// sorted, under a final "Other" block.
func renderEnv(m EnvMap) string {
	var b strings.Builder
	b.WriteString(envHeader + "\n")

	known := make(map[string]bool)
	for _, s := range envLayout {
		b.WriteString("\n" + sectionRule(s.title) + "\n")
		for _, c := range s.comments {
			b.WriteString("# " + c + "\n")
		}
		for _, k := range s.keys {
			known[k] = true
			v := m[k]
			if def, optional := optionalEnvKeys[k]; optional && v == "" {
				fmt.Fprintf(&b, "# %s=%s\n", k, def)
				continue
			}
			fmt.Fprintf(&b, "%s=\"%s\"\n", k, sanitizeEnvValue(v))
		}
	}

	var extra []string
	for k := range m {
		if !known[k] {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		b.WriteString("\n" + sectionRule("Other") + "\n")
		for _, k := range extra {
			fmt.Fprintf(&b, "%s=\"%s\"\n", k, sanitizeEnvValue(m[k]))
		}
	}
	return b.String()
}

// WriteEnv writes m to path (mode 0600) in the documented layout. An existing
// file is first copied to path+".bak". The write is atomic: a temp file in the
// same directory is renamed into place, so a crash never leaves a truncated
// .env behind.
func WriteEnv(path string, m EnvMap) error {
	if data, err := os.ReadFile(path); err == nil {
		_ = os.WriteFile(path+".bak", data, 0600) // best-effort safety net
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	renamed := false
	defer func() {
		if !renamed {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.WriteString(renderEnv(m)); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	renamed = true
	return nil
}

// envDefault returns env[key] if non-empty, else def.
func envDefault(env EnvMap, key, def string) string {
	if v := env[key]; v != "" {
		return v
	}
	return def
}

// completeEnv fills every blank non-path key of m with its default: identity
// and ports from the platform, fixed names, and freshly generated secrets.
// It returns the keys it set, so callers know whether to rewrite the file and
// whether a password was just created.
func completeEnv(m EnvMap, plat Platform) []string {
	var set []string
	fill := func(key, val string) {
		if m[key] == "" {
			m[key] = val
			set = append(set, key)
		}
	}
	fill("PUID", strconv.Itoa(plat.UID))
	fill("PGID", strconv.Itoa(plat.GID))
	fill("TZ", plat.TZ)
	fill("SERVER_COUNTRIES", "Netherlands")
	fill("PELICULA_PORT", "7354")
	fill("JELLYFIN_ADMIN_USER", "admin")
	fill("GLUETUN_HTTP_USER", "pelicula")
	fill("JELLYFIN_PASSWORD", generateSecret(16))
	fill("WEBHOOK_SECRET", generateSecret(32))
	fill("GLUETUN_HTTP_PASS", generateSecret(32))
	return set
}

// validateEnv checks the values `up` cannot guess: the three directories must
// be absolute (compose resolves relative bind-mount paths against compose/,
// not the repo root) and the port must be a valid TCP port.
func validateEnv(m EnvMap) error {
	for _, k := range []string{"CONFIG_DIR", "LIBRARY_DIR", "WORK_DIR"} {
		v := m[k]
		if v == "" {
			return fmt.Errorf("%s is empty in .env — set it to an absolute path (or delete .env and re-run `pelicula up` for the setup wizard)", k)
		}
		if !filepath.IsAbs(v) {
			return fmt.Errorf("%s=%q must be an absolute path", k, v)
		}
	}
	if p, err := strconv.Atoi(envDefault(m, "PELICULA_PORT", "7354")); err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("PELICULA_PORT=%q is not a valid port", m["PELICULA_PORT"])
	}
	return nil
}

// vpnEnabled reports whether a WireGuard key is configured.
func vpnEnabled(env EnvMap) bool {
	return env["WIREGUARD_PRIVATE_KEY"] != ""
}
