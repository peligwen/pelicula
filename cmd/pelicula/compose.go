package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Compose wraps `docker compose` with Pelicula's project settings.
type Compose struct {
	projectDir  string // repository root
	envFile     string
	docker      string // docker executable
	needsSudo   bool
	isSynology  bool
	profiles    []string // compose profiles to enable ("vpn")
	vpn         bool     // exported to compose as PELICULA_VPN
	projectName string
	version     string // exported as PELICULA_VERSION; "" = git describe
}

// NewCompose creates a Compose rooted at scriptDir. envFile "" means
// scriptDir/.env; projectName "" means "pelicula".
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
		docker:      dockerBinary(isSynology, fileExists),
		needsSudo:   needsSudo,
		isSynology:  isSynology,
		projectName: projectName,
	}
}

// composeDir is the directory holding docker-compose.yml. It is also compose's
// project directory: the file's relative paths (build context "..", nginx
// mounts "../nginx/...") resolve against it.
func (c *Compose) composeDir() string { return filepath.Join(c.projectDir, "compose") }

// buildArgs builds the full `docker compose` argument list: base file, the
// generated override when it exists (TUN device mapping), profile flags, then
// extra. Profile flags must precede the subcommand.
func (c *Compose) buildArgs(extra ...string) []string {
	args := []string{
		"compose",
		"--project-name", c.projectName,
		"--project-directory", c.composeDir(),
		"--env-file", c.envFile,
		"-f", filepath.Join(c.composeDir(), "docker-compose.yml"),
	}
	if override := filepath.Join(c.composeDir(), "docker-compose.override.yml"); fileExists(override) {
		args = append(args, "-f", override)
	}
	for _, p := range c.profiles {
		args = append(args, "--profile", p)
	}
	return append(args, extra...)
}

func (c *Compose) versionString() string {
	if c.version != "" {
		return c.version
	}
	return cachedGitVersion(c.projectDir)
}

// envAssignments are the process-env variables the compose file interpolates
// on every invocation: PELICULA_VERSION stamps the server image build, and
// PELICULA_VPN tells the server whether the VPN services exist.
func (c *Compose) envAssignments() []string {
	return []string{
		"PELICULA_VERSION=" + c.versionString(),
		"PELICULA_VPN=" + strconv.FormatBool(c.vpn),
	}
}

// synologyEnv returns the current environment with HOME pointing at the repo
// root. Synology's Docker Compose fork tries to mkdir the parent of $HOME
// (/var/services/homes, a symlink) and fails with "file exists".
func (c *Compose) synologyEnv() []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if !strings.HasPrefix(e, "HOME=") {
			out = append(out, e)
		}
	}
	return append(out, "HOME="+c.projectDir)
}

// dockerCmd returns an exec.Cmd for `docker <args...>`, behind sudo when the
// host needs it. sudo resets the environment, so the assignments are passed
// through sudo's own VAR=value support as well as the process env.
func (c *Compose) dockerCmd(args ...string) *exec.Cmd {
	assigns := c.envAssignments()
	var cmd *exec.Cmd
	if c.needsSudo {
		sudoArgs := slices.Concat(assigns, []string{c.docker}, args)
		cmd = exec.Command("sudo", sudoArgs...)
	} else {
		cmd = exec.Command(c.docker, args...)
	}
	env := os.Environ()
	if c.isSynology {
		env = c.synologyEnv()
	}
	cmd.Env = append(env, assigns...)
	return cmd
}

func attach(cmd *exec.Cmd) *exec.Cmd {
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

// Run runs docker compose with the terminal attached.
func (c *Compose) Run(args ...string) error {
	return attach(c.dockerCmd(c.buildArgs(args...)...)).Run()
}

// Output runs docker compose and returns stdout+stderr.
func (c *Compose) Output(args ...string) ([]byte, error) {
	return c.dockerCmd(c.buildArgs(args...)...).CombinedOutput()
}

// RunProjectOnly runs docker compose using only the project name, with no
// compose file or env file. It is the teardown path when .env is missing.
func (c *Compose) RunProjectOnly(args ...string) error {
	cmdArgs := append([]string{"compose", "--project-name", c.projectName}, args...)
	return attach(c.dockerCmd(cmdArgs...)).Run()
}

// Docker runs plain `docker <args...>` and returns stdout+stderr.
func (c *Compose) Docker(args ...string) ([]byte, error) {
	return c.dockerCmd(args...).CombinedOutput()
}

// ServiceHealth returns the docker health status ("healthy", "starting",
// "unhealthy", or "" when the container has no healthcheck) of a compose
// service's container.
func (c *Compose) ServiceHealth(service string) (string, error) {
	out, err := c.Output("ps", "-q", service)
	if err != nil {
		return "", fmt.Errorf("docker compose ps %s: %w", service, err)
	}
	id := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	if id == "" {
		return "", fmt.Errorf("no container for service %q", service)
	}
	out, err = c.Docker("inspect", "--format={{if .State.Health}}{{.State.Health.Status}}{{end}}", id)
	if err != nil {
		return "", fmt.Errorf("docker inspect %s: %w", id, err)
	}
	return strings.TrimSpace(string(out)), nil
}
