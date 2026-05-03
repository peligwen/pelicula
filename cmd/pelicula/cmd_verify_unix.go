//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

func cmdVerify(ctx *Context, args []string) {
	verifyScript := filepath.Join(ctx.ScriptDir, "tests", "verify.sh")
	cmd := exec.Command("bash", append([]string{verifyScript}, args...)...)
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
