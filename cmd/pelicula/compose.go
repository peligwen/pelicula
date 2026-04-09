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
}

// NewCompose creates a Compose helper rooted at scriptDir.
func NewCompose(scriptDir string, needsSudo bool) *Compose {
	return &Compose{
		projectDir: scriptDir,
		envFile:    filepath.Join(scriptDir, ".env"),
		needsSudo:  needsSudo,
	}
}

// dockerCmd returns an exec.Cmd for "docker <args...>", prefixed with sudo if needed.
func (c *Compose) dockerCmd(args ...string) *exec.Cmd {
	if c.needsSudo {
		return exec.Command("sudo", append([]string{"docker"}, args...)...)
	}
	return exec.Command("docker", args...)
}

// args builds the full docker compose argument list.
func (c *Compose) buildArgs(extra ...string) []string {
	args := []string{"compose", "--env-file", c.envFile, "-f", filepath.Join(c.projectDir, "docker-compose.yml")}

	// Optional override files
	override := filepath.Join(c.projectDir, "docker-compose.override.yml")
	if _, err := os.Stat(override); err == nil {
		args = append(args, "-f", override)
	}

	remote := filepath.Join(c.projectDir, "docker-compose.remote.yml")
	if _, err := os.Stat(remote); err == nil {
		args = append(args, "-f", remote)
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
	cmd := c.dockerCmd("compose", "-f", setupCompose, "up", "-d", "--build")
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

// runSetupDown tears down the setup compose stack.
func (c *Compose) runSetupDown(setupCompose string) error {
	return c.dockerCmd("compose", "-f", setupCompose, "down").Run()
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
