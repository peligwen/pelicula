package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Compose wraps docker compose with project-specific settings.
type Compose struct {
	projectDir  string
	envFile     string
	needsSudo   bool
	isSynology  bool
	profiles    []string // active profiles (e.g. "vpn", "apprise")
	projectName string   // --project-name passed to every docker compose invocation
}

// NewCompose creates a Compose helper rooted at scriptDir.
// envFile is the --env-file path to use; pass "" to get the default
// scriptDir/.env (this is the fallback used when the PELICULA_ENV_FILE
// process-env override is unset — see (*Context).newCompose, the only
// production call site, which always passes ctx.EnvFile explicitly).
// isSynology should come from the Platform detected via Detect() — it is
// passed explicitly so that Synology detection is not duplicated here.
// projectName is passed as --project-name to every docker compose invocation so
// that container names are always pelicula-<service>-1 regardless of what
// directory the repo was cloned into.
func NewCompose(scriptDir, envFile string, needsSudo, isSynology bool, projectName string) *Compose {
	if projectName == "" {
		projectName = "pelicula"
	}
	if envFile == "" {
		envFile = filepath.Join(scriptDir, ".env")
	}
	return &Compose{
		projectDir:  scriptDir,
		envFile:     envFile,
		needsSudo:   needsSudo,
		isSynology:  isSynology,
		projectName: projectName,
	}
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

// validateComposeOverlay checks that the PELICULA_COMPOSE_OVERLAY path (if
// set) exists on disk. It is called once, at newContext() time, rather than
// on every Compose.buildArgs call: buildArgs runs on every single docker
// compose invocation within a command (sometimes several), and it has no
// error return — folding a Stat-and-fail check into it would mean either
// silently ignoring a missing overlay (letting the intended stub/override
// never actually merge in, which then fails much later and more confusingly
// inside `docker compose` itself) or making buildArgs a failing, non-pure
// function. Checking once at startup gives one clear, early error instead.
//
// path == "" (override unset) is not an error — it returns nil.
func validateComposeOverlay(path string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf(
			"PELICULA_COMPOSE_OVERLAY=%s does not exist (%v) — set it to an existing compose file, or unset it",
			path, err,
		)
	}
	return nil
}

// args builds the full docker compose argument list.
// Profile flags are inserted before the subcommand (required by Docker Compose v5+).
func (c *Compose) buildArgs(extra ...string) []string {
	args := []string{
		"compose",
		"--project-name", c.projectName,
		"--project-directory", c.projectDir,
		"--env-file", c.envFile,
		"-f", filepath.Join(c.projectDir, "compose", "docker-compose.yml"),
	}

	// Optional override files
	override := filepath.Join(c.projectDir, "compose", "docker-compose.override.yml")
	if _, err := os.Stat(override); err == nil {
		args = append(args, "-f", override)
	}

	libraries := filepath.Join(c.projectDir, "compose", "docker-compose.libraries.yml")
	if _, err := os.Stat(libraries); err == nil {
		args = append(args, "-f", libraries)
	}

	// PELICULA_COMPOSE_OVERLAY is a test/orchestration seam (not user-facing
	// config — see docs/ARCHITECTURE.md): one extra compose file, appended
	// last so it wins merges against everything above (docker-compose.yml,
	// the optional override.yml, and docker-compose.libraries.yml). Used by
	// tests/e2e.sh to stub services for the isolated test stack.
	//
	// Existence is validated once, at newContext() time (see
	// validateComposeOverlay) — by the time buildArgs runs the path is
	// already known-good, so this is a plain, non-failing append.
	if overlay := os.Getenv("PELICULA_COMPOSE_OVERLAY"); overlay != "" {
		args = append(args, "-f", overlay)
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

// RunProjectOnly runs docker compose using only the project name, without
// parsing compose files or requiring an env file.
// Useful for teardown when .env is missing.
func (c *Compose) RunProjectOnly(args ...string) error {
	cmdArgs := []string{
		"compose",
		"--project-name", c.projectName,
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

// runSetupBuild builds and starts the setup compose stack with the given
// environment variables. It runs `docker compose build --build-arg VERSION=...`
// first so the middleware image is stamped with the correct git version, then
// `docker compose up -d` to start the containers.
func (c *Compose) runSetupBuild(setupCompose string, env []string) error {
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

	buildCmd := c.dockerCmd("compose", "--project-directory", c.projectDir, "-f", setupCompose, "build", "--build-arg", "VERSION="+gitDescribe())
	buildCmd.Env = env
	buildCmd.Stdin = os.Stdin
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return err
	}

	upCmd := c.dockerCmd("compose", "--project-directory", c.projectDir, "-f", setupCompose, "up", "-d")
	upCmd.Env = env
	upCmd.Stdin = os.Stdin
	upCmd.Stdout = os.Stdout
	upCmd.Stderr = os.Stderr
	return upCmd.Run()
}

// runSetupDown tears down the setup compose stack.
func (c *Compose) runSetupDown(setupCompose string) error {
	return c.dockerCmd("compose", "--project-directory", c.projectDir, "-f", setupCompose, "down").Run()
}

// DockerRaw runs docker (not docker compose) with the given args and returns
// the combined output. Sudo is applied when c.needsSudo is set, so this is
// safe to use on Synology and other sudo-required hosts.
func (c *Compose) DockerRaw(args ...string) ([]byte, error) {
	return c.dockerCmd(args...).Output()
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
