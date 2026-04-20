package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"time"
)

var errPattern = regexp.MustCompile(`(?i)(error|warn|fatal|panic)`)

func cmdDoctor(ctx *Context, _ []string) {
	requireEnv(ctx.EnvFile)
	ctx.LoadEnv()
	c := composeInvocation(ctx)

	// Header
	fmt.Println("=== pelicula doctor ===")
	fmt.Println("Timestamp :", time.Now().Format(time.RFC3339))
	fmt.Println("Version   :", version)
	if out, err := exec.Command("docker", "version", "--format", "{{.Client.Version}} (client) / {{.Server.Version}} (server)").Output(); err == nil {
		fmt.Print("Docker    : ", string(bytes.TrimSpace(out)), "\n")
	} else {
		fmt.Println("Docker    : unavailable")
	}
	if out, err := exec.Command("docker", "compose", "version", "--short").Output(); err == nil {
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
	matched := 0
	for _, line := range bytes.Split(out, []byte("\n")) {
		if errPattern.Match(line) {
			fmt.Println(string(line))
			matched++
		}
	}
	if matched == 0 {
		fmt.Println("(no error or warning lines found)")
	}
}
