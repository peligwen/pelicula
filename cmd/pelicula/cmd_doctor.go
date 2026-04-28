package main

import (
	"bytes"
	"fmt"
	"regexp"
	"time"
)

var errPattern = regexp.MustCompile(`(?i)(error|warn|fatal|panic)`)

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
		fmt.Println(line)
	}
	if len(lines) == 0 {
		fmt.Println("(no error or warning lines found)")
	}
}
