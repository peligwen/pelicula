package main

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Doctor output is meant to be pasted into bug reports, so credentials must
// never reach it. redact removes them in three ways:
//   - the literal values of secret-looking .env keys, wherever they appear;
//   - NAME=value assignments whose name looks secret (also apikey=... in URLs,
//     which Sonarr and Prowlarr log verbatim);
//   - credential headers and JSON fields.
var (
	secretKeyPattern = regexp.MustCompile(`(?i)(secret|pass|token|private_?key|api_?key|credential)`)

	scrubAssignPattern = regexp.MustCompile(`(?i)([a-z0-9_.-]*(?:apikey|api_key|api-key|passkey|token|passw(?:or)?d|pass|secret|private_?key)[a-z0-9_.-]*)=[^&\s"'\\\]),;}(]+`)
	scrubHeaderPattern = regexp.MustCompile(`(?i)(x-api-key|authorization|x-webhook-secret|x-emby-[a-z-]+)(["']?\s*[:=]\s*)[^\r\n]*`)
	scrubJSONPattern   = regexp.MustCompile(`(?i)("[a-z0-9_.-]*(?:apikey|api_key|passw(?:or)?d|pass|secret|token|private_?key)[a-z0-9_.-]*"\s*:\s*")[^"]*(")`)
)

// minSecretLen keeps very short values from redacting ordinary words.
const minSecretLen = 4

// isSecretKey reports whether an env var name looks like it holds a secret.
func isSecretKey(name string) bool { return secretKeyPattern.MatchString(name) }

// secretValues returns the values of env's secret-looking keys.
func secretValues(env EnvMap) []string {
	var out []string
	for k, v := range env {
		if isSecretKey(k) && len(v) >= minSecretLen {
			out = append(out, v)
		}
	}
	return out
}

// redact scrubs credentials from text; secrets are literal values to remove.
func redact(text string, secrets []string) string {
	for _, s := range secrets {
		if len(s) >= minSecretLen {
			text = strings.ReplaceAll(text, s, "REDACTED")
		}
	}
	text = scrubAssignPattern.ReplaceAllString(text, "${1}=REDACTED")
	text = scrubHeaderPattern.ReplaceAllString(text, "${1}${2}REDACTED")
	return scrubJSONPattern.ReplaceAllString(text, "${1}REDACTED${2}")
}

// containerInfo is one line of `docker inspect` output.
type containerInfo struct{ ID, Name, State, Health string }

const inspectFormat = "{{.Id}}|{{.Name}}|{{.State.Status}}|{{if .State.Health}}{{.State.Health.Status}}{{end}}"

// parseInspect parses lines produced with inspectFormat.
func parseInspect(out string) []containerInfo {
	var res []containerInfo
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(line), "|")
		if len(f) != 4 {
			continue
		}
		res = append(res, containerInfo{ID: f[0], Name: strings.TrimPrefix(f[1], "/"), State: f[2], Health: f[3]})
	}
	return res
}

// needsLogs reports whether a container is unhealthy or stopped abnormally,
// and so deserves a log excerpt in the report.
func (ci containerInfo) needsLogs() bool {
	switch ci.State {
	case "exited", "dead", "restarting":
		return true
	}
	return ci.Health == "unhealthy"
}

// cmdDoctor prints a support report: versions, container status, and the
// last 50 log lines of every unhealthy container, with secrets redacted.
func cmdDoctor(ctx *Context, _ []string) {
	ctx.LoadEnv()
	c := ctx.compose(true)
	secrets := secretValues(ctx.Env)
	show := func(s string) { fmt.Print(redact(s, secrets)) }

	fmt.Println("=== pelicula doctor ===")
	fmt.Println("Time      :", time.Now().Format(time.RFC3339))
	fmt.Println("CLI       :", version)
	fmt.Println("Repo      :", gitDescribe(ctx.ScriptDir))
	fmt.Println("Platform  :", ctx.Plat.PlatformLabel())
	fmt.Printf("VPN       : %v\n", vpnEnabled(ctx.Env))
	if out, err := c.Docker("version", "--format", "{{.Client.Version}} (client) / {{.Server.Version}} (server)"); err == nil {
		fmt.Println("Docker    :", strings.TrimSpace(string(out)))
	} else {
		fmt.Println("Docker    : unavailable")
	}
	if out, err := c.Docker("compose", "version", "--short"); err == nil {
		fmt.Println("Compose   :", strings.TrimSpace(string(out)))
	} else {
		fmt.Println("Compose   : unavailable")
	}

	fmt.Println("\n=== Container status ===")
	if out, err := c.Output("ps", "-a"); err == nil {
		show(string(out))
	} else {
		fmt.Println("(could not list containers)")
	}

	fmt.Println("\n=== Unhealthy containers (last 50 log lines) ===")
	ids, err := c.Output("ps", "-a", "-q")
	if err != nil {
		fmt.Println("(could not list containers)")
		return
	}
	inspectArgs := append([]string{"inspect", "--format=" + inspectFormat}, strings.Fields(string(ids))...)
	if len(inspectArgs) == 2 {
		fmt.Println("(no containers)")
		return
	}
	out, err := c.Docker(inspectArgs...)
	if err != nil {
		fmt.Println("(could not inspect containers)")
		return
	}
	containers := parseInspect(string(out))
	if len(containers) == 0 {
		fmt.Println("(no container details available)")
		return
	}
	shown := 0
	for _, ci := range containers {
		if !ci.needsLogs() {
			continue
		}
		shown++
		fmt.Printf("\n--- %s (state=%s health=%s) ---\n", ci.Name, ci.State, ci.Health)
		logs, _ := c.Docker("logs", "--tail", "50", ci.ID)
		show(string(bytes.TrimRight(logs, "\n")) + "\n")
	}
	if shown == 0 {
		fmt.Println("(all containers healthy)")
	}
}
