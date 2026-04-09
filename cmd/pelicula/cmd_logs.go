package main

import (
	"path/filepath"
)

func cmdLogs(args []string) {
	scriptDir := getScriptDir()
	requireEnv(filepath.Join(scriptDir, ".env"))

	plat := Detect(scriptDir)
	c := NewCompose(scriptDir, plat.NeedsSudo)

	composeArgs := []string{"logs", "-f"}
	if len(args) > 0 {
		composeArgs = append(composeArgs, args...)
	}

	if err := c.Run(composeArgs...); err != nil {
		// logs -f exits on Ctrl+C — that's expected, not an error
		_ = err
	}
}
