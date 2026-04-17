//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

func cmdTest(ctx *Context, args []string) {
	testScript := filepath.Join(ctx.ScriptDir, "tests", "e2e.sh")
	cmd := exec.Command("bash", append([]string{testScript}, args...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
}
