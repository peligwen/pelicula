package main

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

// wgKeyRe matches a WireGuard private key: 32 bytes of base64 (44 chars).
var wgKeyRe = regexp.MustCompile(`^[A-Za-z0-9+/]{43}=$`)

// prompter asks line-based questions with defaults. At EOF every question
// answers with its default, so a piped or empty stdin yields a default setup.
type prompter struct {
	in  *bufio.Reader
	out io.Writer
}

// ask prints "  label [def]: " and returns the trimmed answer, or def when
// the answer is empty or input has ended.
func (p *prompter) ask(label, def string) string {
	if def != "" {
		fmt.Fprintf(p.out, "  %s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.out, "  %s: ", label)
	}
	line, _ := p.in.ReadString('\n')
	if line = strings.TrimSpace(line); line != "" {
		return line
	}
	return def
}

// askPath asks for a directory and returns it absolute and cleaned.
func (p *prompter) askPath(label, def, base, home string) string {
	return expandPath(p.ask(label, def), base, home)
}

// askKey asks for the WireGuard private key. Blank means no VPN. A malformed
// key is rejected up to three times, then the VPN is skipped.
func (p *prompter) askKey() string {
	for i := 0; i < 3; i++ {
		key := p.ask("WireGuard private key (Enter to skip)", "")
		if key == "" || wgKeyRe.MatchString(key) {
			return key
		}
		fmt.Fprintln(p.out, "  That is not a WireGuard private key (44 base64 characters ending in \"=\").")
	}
	fmt.Fprintln(p.out, "  Skipping the VPN; add WIREGUARD_PRIVATE_KEY to .env later.")
	return ""
}

// expandPath turns a user-typed path into an absolute, cleaned one: surrounding
// quotes are dropped, a leading ~ becomes home, and a relative path is taken
// relative to base (the repo root).
func expandPath(p, base, home string) string {
	p = strings.Trim(strings.TrimSpace(p), `"'`)
	switch {
	case p == "~":
		p = home
	case strings.HasPrefix(p, "~/"):
		p = filepath.Join(home, p[2:])
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p)
}

// runWizard is the first-run terminal setup. It asks for the three
// directories and the optional VPN key (with platform defaults), then
// completes the rest of the .env with generated secrets (see completeEnv).
func runWizard(in io.Reader, out io.Writer, plat Platform, scriptDir, home string) EnvMap {
	p := &prompter{in: bufio.NewReader(in), out: out}
	m := EnvMap{}

	fmt.Fprintln(out, bold("Pelicula setup")+" — press Enter to accept the [default].")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Where should Pelicula keep its files? Use absolute paths.")
	m["CONFIG_DIR"] = p.askPath("Config directory (service settings and databases)", plat.DefaultConfigDir, scriptDir, home)
	m["LIBRARY_DIR"] = p.askPath("Media library (movies/ and tv/ are created here)", plat.DefaultLibraryDir, scriptDir, home)
	// Downloads default to the library's filesystem so imports can hardlink.
	m["WORK_DIR"] = p.askPath("Work directory (downloads/ is created here)", m["LIBRARY_DIR"], scriptDir, home)

	fmt.Fprintln(out)
	fmt.Fprintln(out, "VPN (optional). Downloads and indexers run through a ProtonVPN WireGuard tunnel.")
	fmt.Fprintln(out, "  Requires a paid ProtonVPN plan (Plus or higher). Create the key at")
	fmt.Fprintln(out, "  Settings → Downloads → WireGuard configuration, and leave \"Moderate NAT\" OFF")
	fmt.Fprintln(out, "  (it breaks port forwarding). Without a key Pelicula runs without a VPN and")
	fmt.Fprintln(out, "  without download clients.")
	if key := p.askKey(); key != "" {
		m["WIREGUARD_PRIVATE_KEY"] = key
		m["SERVER_COUNTRIES"] = sanitizeEnvValue(p.ask("VPN server countries", "Netherlands"))
	}

	completeEnv(m, plat)
	return m
}
