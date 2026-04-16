package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

// Compose wraps docker compose with project-specific settings.
type Compose struct {
	projectDir string
	envFile    string
	needsSudo  bool
	isSynology bool
	profiles   []string // active profiles (e.g. "vpn", "apprise")
}

// NewCompose creates a Compose helper rooted at scriptDir.
func NewCompose(scriptDir string, needsSudo bool) *Compose {
	return &Compose{
		projectDir: scriptDir,
		envFile:    filepath.Join(scriptDir, ".env"),
		needsSudo:  needsSudo,
		isSynology: isSynologyHost(),
	}
}

// isSynologyHost returns true when running on a Synology NAS.
func isSynologyHost() bool {
	if _, err := os.Stat("/proc/syno_platform"); err == nil {
		return true
	}
	_, err := os.Stat("/volume1")
	return err == nil
}

// synologyEnv returns a copy of the current environment with HOME replaced
// by the script directory. Synology's Docker Compose fork tries to mkdir the
// parent of $HOME (/var/services/homes), which is a symlink and causes
// "file exists" errors. Using a real directory avoids this.
func (c *Compose) synologyEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env))
	for _, e := range env {
		if len(e) >= 5 && e[:5] == "HOME=" {
			continue
		}
		out = append(out, e)
	}
	return append(out, "HOME="+c.projectDir)
}

// dockerCmd returns an exec.Cmd for "docker <args...>", prefixed with sudo if needed.
func (c *Compose) dockerCmd(args ...string) *exec.Cmd {
	var cmd *exec.Cmd
	if c.needsSudo {
		cmd = exec.Command("sudo", append([]string{"docker"}, args...)...)
	} else {
		cmd = exec.Command("docker", args...)
	}
	if c.isSynology {
		cmd.Env = c.synologyEnv()
	}
	return cmd
}

// args builds the full docker compose argument list.
// Profile flags are inserted before the subcommand (required by Docker Compose v5+).
func (c *Compose) buildArgs(extra ...string) []string {
	args := []string{
		"compose",
		"--project-directory", c.projectDir,
		"--env-file", c.envFile,
		"-f", filepath.Join(c.projectDir, "compose", "docker-compose.yml"),
	}

	// Optional override files
	override := filepath.Join(c.projectDir, "compose", "docker-compose.override.yml")
	if _, err := os.Stat(override); err == nil {
		args = append(args, "-f", override)
	}

	remote := filepath.Join(c.projectDir, "compose", "docker-compose.remote.yml")
	if _, err := os.Stat(remote); err == nil {
		args = append(args, "-f", remote)
	}

	libraries := filepath.Join(c.projectDir, "compose", "docker-compose.libraries.yml")
	if _, err := os.Stat(libraries); err == nil {
		args = append(args, "-f", libraries)
	}

	// Profiles must come before the subcommand
	for _, p := range c.profiles {
		args = append(args, "--profile", p)
	}

	args = append(args, extra...)
	return args
}

// Run runs docker compose with the given subcommand args, attaching stdin/stdout/stderr.
func (c *Compose) Run(args ...string) error {
	cmd := c.dockerCmd(c.buildArgs(args...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// RunSilent runs docker compose and captures output, not attaching to terminal.
func (c *Compose) RunSilent(args ...string) ([]byte, error) {
	cmd := c.dockerCmd(c.buildArgs(args...)...)
	return cmd.CombinedOutput()
}

// RunQuiet runs docker compose silently when not in verbose mode.
// In verbose mode it attaches to the terminal. In quiet mode it captures
// output and only dumps it to stderr if the command fails.
func (c *Compose) RunQuiet(args ...string) error {
	if verboseMode {
		return c.Run(args...)
	}
	out, err := c.RunSilent(args...)
	if err != nil {
		os.Stderr.Write(out)
	}
	return err
}

// RunProjectOnly runs docker compose using only the project name derived from
// projectDir, without parsing compose files or requiring an env file.
// Useful for teardown when .env is missing.
func (c *Compose) RunProjectOnly(args ...string) error {
	cmdArgs := []string{
		"compose",
		"--project-directory", c.projectDir,
	}
	cmdArgs = append(cmdArgs, args...)
	cmd := c.dockerCmd(cmdArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// DockerExec runs a docker exec command, attaching stdin/stdout/stderr.
func (c *Compose) DockerExec(container string, cmdArgs ...string) error {
	cmd := c.dockerCmd(append([]string{"exec", container}, cmdArgs...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// buildSetupCmd creates an exec.Cmd for `docker compose -f setupCompose up -d --build`
// with the given environment variables.
func (c *Compose) buildSetupCmd(setupCompose string, env []string) *exec.Cmd {
	cmd := c.dockerCmd("compose", "--project-directory", c.projectDir, "-f", setupCompose, "up", "-d", "--build")
	// Start from caller-supplied env; strip HOME and re-add a safe one on Synology.
	if c.isSynology {
		out := make([]string, 0, len(env)+1)
		for _, e := range env {
			if len(e) >= 5 && e[:5] == "HOME=" {
				continue
			}
			out = append(out, e)
		}
		env = append(out, "HOME="+c.projectDir)
	}
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

// runSetupDown tears down the setup compose stack.
func (c *Compose) runSetupDown(setupCompose string) error {
	return c.dockerCmd("compose", "--project-directory", c.projectDir, "-f", setupCompose, "down").Run()
}

// DockerInspect runs docker inspect --format=... on a container.
func (c *Compose) DockerInspect(format, container string) (string, error) {
	cmd := c.dockerCmd("inspect", "--format="+format, container)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	result := string(out)
	// trim trailing newline
	if len(result) > 0 && result[len(result)-1] == '\n' {
		result = result[:len(result)-1]
	}
	return result, nil
}
