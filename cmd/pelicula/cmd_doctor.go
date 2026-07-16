package main

import (
	"bytes"
	"fmt"
	"regexp"
	"time"
)

var errPattern = regexp.MustCompile(`(?i)(error|warn|fatal|panic)`)

// Doctor output is written to be pasted into bug reports and chat, so any
// credential material in container logs must be scrubbed before printing.
// The *arr services echo their API keys in request URLs (Sonarr and Prowlarr
// log `apikey=<key>` verbatim; only Radarr self-redacts), and tracker URLs
// carry passkeys. Two shapes cover what the stack logs:
//   - query/assignment params: apikey=..., passkey=..., token=..., etc.
//     No leading \b — JSON-escaped URLs log the separator as &, which
//     puts a word character right before the key name.
//   - headers: X-Api-Key: ..., Authorization: ... (plain or JSON-quoted).
var (
	scrubParamPattern  = regexp.MustCompile(`(?i)(apikey|api_key|api-key|passkey|token|password|secret)=[^&\s"'\\\]),;}(]+`)
	scrubHeaderPattern = regexp.MustCompile(`(?i)(x-api-key|authorization)(["']?\s*[:=]\s*["']?)[A-Za-z0-9+/=._-]+`)
)

// scrubSecrets redacts credential values from a log line while leaving the
// key names in place, so the reader still sees *that* a key was sent.
func scrubSecrets(line string) string {
	line = scrubParamPattern.ReplaceAllString(line, "${1}=REDACTED")
	line = scrubHeaderPattern.ReplaceAllString(line, "${1}${2}REDACTED")
	return line
}

// filterErrorLines scans log output and returns lines that match the error
// pattern (case-insensitive: error, warn, fatal, panic). Matching is by
// substring, so "errored" also matches — this is intentional and reflects
// the pattern used in the doctor output.
func filterErrorLines(out []byte) []string {
	var result []string
	for _, line := range bytes.Split(out, []byte("\n")) {
		if errPattern.Match(line) {
			result = append(result, string(line))
		}
	}
	return result
}

func cmdDoctor(ctx *Context, _ []string) {
	requireEnv(ctx.EnvFile)
	ctx.LoadEnv()
	c := composeInvocation(ctx)

	// Header
	fmt.Println("=== pelicula doctor ===")
	fmt.Println("Timestamp :", time.Now().Format(time.RFC3339))
	fmt.Println("Version   :", version)
	if out, err := c.DockerRaw("version", "--format", "{{.Client.Version}} (client) / {{.Server.Version}} (server)"); err == nil {
		fmt.Print("Docker    : ", string(bytes.TrimSpace(out)), "\n")
	} else {
		fmt.Println("Docker    : unavailable")
	}
	if out, err := c.DockerRaw("compose", "version", "--short"); err == nil {
		fmt.Print("Compose   : ", string(bytes.TrimSpace(out)), "\n")
	} else {
		fmt.Println("Compose   : unavailable")
	}

	// Container status
	fmt.Println("\n=== Container Status ===")
	if out, err := c.RunSilent("ps"); err == nil {
		fmt.Print(string(out))
	} else {
		fmt.Println("(could not retrieve container status)")
	}

	// Error logs
	fmt.Println("\n=== Error Logs (last 200 lines per service, errors/warnings only) ===")
	out, err := c.RunSilent("logs", "--no-color", "--tail=200")
	if err != nil {
		fmt.Println("(could not retrieve logs)")
		return
	}
	lines := filterErrorLines(out)
	for _, line := range lines {
		fmt.Println(scrubSecrets(line))
	}
	if len(lines) == 0 {
		fmt.Println("(no error or warning lines found)")
	}
}
