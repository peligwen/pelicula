package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// newTestRepo returns a fake repo root with compose/docker-compose.yml.
func newTestRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFileT(t, filepath.Join(root, "compose", "docker-compose.yml"), "services: {}\n")
	return root
}

// flagValues returns every value that follows flag in args.
func flagValues(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

func TestBuildArgsBase(t *testing.T) {
	root := newTestRepo(t)
	c := NewCompose(root, "", false, false, "")
	args := c.buildArgs("up", "-d")

	if args[0] != "compose" {
		t.Errorf("args[0] = %q, want compose", args[0])
	}
	if got := flagValues(args, "--project-name"); !slices.Equal(got, []string{"pelicula"}) {
		t.Errorf("--project-name = %v, want [pelicula]", got)
	}
	if got := flagValues(args, "--env-file"); !slices.Equal(got, []string{filepath.Join(root, ".env")}) {
		t.Errorf("--env-file = %v", got)
	}
	// Project directory is compose/: the compose file's relative paths ("..",
	// "../nginx/...") are relative to it.
	if got := flagValues(args, "--project-directory"); !slices.Equal(got, []string{filepath.Join(root, "compose")}) {
		t.Errorf("--project-directory = %v", got)
	}
	if got := flagValues(args, "-f"); !slices.Equal(got, []string{filepath.Join(root, "compose", "docker-compose.yml")}) {
		t.Errorf("-f = %v, want only the base file", got)
	}
	if slices.Contains(args, "--profile") {
		t.Errorf("no profile expected without the vpn: %v", args)
	}
	if !slices.Equal(args[len(args)-2:], []string{"up", "-d"}) {
		t.Errorf("extra args must come last: %v", args)
	}
}

func TestBuildArgsProjectNameAndEnvFile(t *testing.T) {
	root := newTestRepo(t)
	c := NewCompose(root, "/custom/.env", false, false, "my-stack")
	args := c.buildArgs("ps")
	if got := flagValues(args, "--project-name"); !slices.Equal(got, []string{"my-stack"}) {
		t.Errorf("--project-name = %v", got)
	}
	if got := flagValues(args, "--env-file"); !slices.Equal(got, []string{"/custom/.env"}) {
		t.Errorf("--env-file = %v", got)
	}
}

func TestBuildArgsProfileBeforeSubcommand(t *testing.T) {
	root := newTestRepo(t)
	c := NewCompose(root, "", false, false, "")
	c.profiles = []string{"vpn"}
	args := c.buildArgs("up", "-d", "--build", "--remove-orphans")

	p := slices.Index(args, "--profile")
	if p < 0 || args[p+1] != "vpn" {
		t.Fatalf("--profile vpn missing: %v", args)
	}
	if up := slices.Index(args, "up"); p > up {
		t.Errorf("--profile must precede the subcommand: %v", args)
	}
}

func TestBuildArgsOverrideSelection(t *testing.T) {
	root := newTestRepo(t)
	c := NewCompose(root, "", false, false, "")

	args := c.buildArgs("ps")
	if got := flagValues(args, "-f"); len(got) != 1 {
		t.Errorf("without an override file only the base is used: %v", got)
	}

	override := filepath.Join(root, "compose", "docker-compose.override.yml")
	writeFileT(t, override, tunOverrideYAML)
	args = c.buildArgs("ps")
	want := []string{filepath.Join(root, "compose", "docker-compose.yml"), override}
	if got := flagValues(args, "-f"); !slices.Equal(got, want) {
		t.Errorf("-f = %v, want base then override %v", got, want)
	}

	// Other overlays the old CLI knew about are no longer picked up.
	writeFileT(t, filepath.Join(root, "compose", "docker-compose.libraries.yml"), "services: {}\n")
	writeFileT(t, filepath.Join(root, "compose", "docker-compose.nfs.yml"), "services: {}\n")
	if got := flagValues(c.buildArgs("ps"), "-f"); !slices.Equal(got, want) {
		t.Errorf("unexpected overlays selected: %v", got)
	}
}

func TestEnvAssignments(t *testing.T) {
	c := NewCompose(t.TempDir(), "", false, false, "")
	c.version = "v1.2.3-4-gabc"

	c.vpn = false
	if got := c.envAssignments(); !slices.Equal(got, []string{"PELICULA_VERSION=v1.2.3-4-gabc", "PELICULA_VPN=false"}) {
		t.Errorf("assignments = %v", got)
	}
	c.vpn = true
	if got := c.envAssignments(); !slices.Contains(got, "PELICULA_VPN=true") {
		t.Errorf("vpn assignments = %v", got)
	}
}

func TestDockerCmdEnvironmentAndSudo(t *testing.T) {
	root := newTestRepo(t)
	c := NewCompose(root, "", false, false, "")
	c.version = "v9"
	c.vpn = true

	cmd := c.dockerCmd("compose", "ps")
	if cmd.Args[0] != "docker" || !slices.Equal(cmd.Args[1:], []string{"compose", "ps"}) {
		t.Errorf("args = %v", cmd.Args)
	}
	if !slices.Contains(cmd.Env, "PELICULA_VPN=true") || !slices.Contains(cmd.Env, "PELICULA_VERSION=v9") {
		t.Errorf("process env lacks the compose variables")
	}

	c.needsSudo = true
	cmd = c.dockerCmd("compose", "ps")
	want := []string{"sudo", "PELICULA_VERSION=v9", "PELICULA_VPN=true", "docker", "compose", "ps"}
	if !slices.Equal(cmd.Args, want) {
		t.Errorf("sudo args = %v, want %v", cmd.Args, want)
	}
}

func TestSynologyEnvReplacesHome(t *testing.T) {
	t.Setenv("HOME", "/var/services/homes/admin")
	root := newTestRepo(t)
	c := NewCompose(root, "", false, true, "")
	c.version = "dev"

	var homes []string
	for _, e := range c.dockerCmd("ps").Env {
		if strings.HasPrefix(e, "HOME=") {
			homes = append(homes, e)
		}
	}
	if !slices.Equal(homes, []string{"HOME=" + root}) {
		t.Errorf("HOME entries = %v, want only HOME=%s", homes, root)
	}
}

func TestContextComposeProfiles(t *testing.T) {
	root := newTestRepo(t)
	base := func(env EnvMap) *Context {
		return &Context{ScriptDir: root, EnvFile: filepath.Join(root, ".env"), Env: env}
	}

	noVPN := base(EnvMap{"WIREGUARD_PRIVATE_KEY": ""}).compose(false)
	if noVPN.vpn || len(noVPN.profiles) != 0 {
		t.Errorf("no key: vpn=%v profiles=%v, want none", noVPN.vpn, noVPN.profiles)
	}

	withVPN := base(EnvMap{"WIREGUARD_PRIVATE_KEY": "k"}).compose(false)
	if !withVPN.vpn || !slices.Equal(withVPN.profiles, []string{"vpn"}) {
		t.Errorf("key set: vpn=%v profiles=%v, want vpn profile", withVPN.vpn, withVPN.profiles)
	}

	// down/restart/logs enable the profile even without a key, but must not
	// claim the VPN is on.
	all := base(EnvMap{"WIREGUARD_PRIVATE_KEY": ""}).compose(true)
	if all.vpn || !slices.Equal(all.profiles, []string{"vpn"}) {
		t.Errorf("allProfiles: vpn=%v profiles=%v", all.vpn, all.profiles)
	}

	if got := base(EnvMap{"PELICULA_PROJECT_NAME": "second"}).compose(false).projectName; got != "second" {
		t.Errorf("project name = %q, want second", got)
	}
	if got := base(EnvMap{}).compose(false).projectName; got != "pelicula" {
		t.Errorf("default project name = %q, want pelicula", got)
	}
}

func TestLogsAndGluetunArgs(t *testing.T) {
	if got := logsArgs(nil); !slices.Equal(got, []string{"logs", "-f", "--tail", "100"}) {
		t.Errorf("logsArgs(nil) = %v", got)
	}
	if got := logsArgs([]string{"sonarr", "radarr"}); !slices.Equal(got, []string{"logs", "-f", "--tail", "100", "sonarr", "radarr"}) {
		t.Errorf("logsArgs(svc) = %v", got)
	}
	got := gluetunGetArgs("u", "p", "/v1/publicip/ip")
	want := []string{"exec", "-T", "gluetun", "wget", "-qO-", "--user=u", "--password=p", "http://localhost:8000/v1/publicip/ip"}
	if !slices.Equal(got, want) {
		t.Errorf("gluetunGetArgs = %v, want %v", got, want)
	}
}

// ---- compose/docker-compose.yml and Dockerfile structure ----------------------
// docker is not available to `go test`, so the compose file is checked
// structurally with a small line scanner instead of a YAML parser.

var topLevelKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]+:`)
var serviceNameRe = regexp.MustCompile(`^  ([A-Za-z0-9_-]+):\s*$`)

// serviceBlocks splits the compose file's `services:` section into name -> text.
func serviceBlocks(t *testing.T, doc string) map[string]string {
	t.Helper()
	blocks := map[string]string{}
	inServices, current := false, ""
	for _, line := range strings.Split(doc, "\n") {
		switch {
		case line == "services:":
			inServices = true
		case inServices && topLevelKeyRe.MatchString(line):
			inServices, current = false, ""
		case inServices:
			if m := serviceNameRe.FindStringSubmatch(line); m != nil {
				current = m[1]
				blocks[current] = ""
			} else if current != "" {
				blocks[current] += line + "\n"
			}
		}
	}
	return blocks
}

func readRepoFile(t *testing.T, rel ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(append([]string{"..", ".."}, rel...)...))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestComposeFileServices(t *testing.T) {
	doc := readRepoFile(t, "compose", "docker-compose.yml")
	blocks := serviceBlocks(t, doc)

	want := []string{"gluetun", "qbittorrent", "prowlarr", "sonarr", "radarr", "jellyfin", "pelicula", "nginx"}
	for _, name := range want {
		if _, ok := blocks[name]; !ok {
			t.Errorf("service %q missing from compose file", name)
		}
	}
	for name := range blocks {
		if !slices.Contains(want, name) {
			t.Errorf("unexpected service %q in compose file", name)
		}
	}
	for _, forbidden := range []string{"docker-proxy", "procula", "bazarr", "apprise", "pelicula-api"} {
		if _, ok := blocks[forbidden]; ok {
			t.Errorf("forbidden service %q present", forbidden)
		}
	}

	if strings.Contains(doc, "container_name") {
		t.Error("compose file must not set container_name")
	}
	if regexp.MustCompile(`(?m)^networks:`).MatchString(doc) {
		t.Error("compose file must not have a top-level networks block")
	}
	if regexp.MustCompile(`image:.*:latest\b`).MatchString(doc) {
		t.Error("images must be pinned, not :latest")
	}
	if !strings.Contains(doc, "x-logging: &default-logging") {
		t.Error("x-logging anchor missing")
	}
}

func TestComposeFileProfiles(t *testing.T) {
	blocks := serviceBlocks(t, readRepoFile(t, "compose", "docker-compose.yml"))
	for name, block := range blocks {
		hasVPN := strings.Contains(block, "profiles: [vpn]")
		switch name {
		case "gluetun", "qbittorrent", "prowlarr":
			if !hasVPN {
				t.Errorf("%s must be in the vpn profile", name)
			}
		default:
			if hasVPN || strings.Contains(block, "profiles:") {
				t.Errorf("%s must always run (no profile)", name)
			}
		}
	}
	for _, name := range []string{"qbittorrent", "prowlarr"} {
		if !strings.Contains(blocks[name], `network_mode: "service:gluetun"`) {
			t.Errorf("%s must use gluetun's network namespace", name)
		}
	}
}

func TestComposeFilePeliculaAndNginx(t *testing.T) {
	blocks := serviceBlocks(t, readRepoFile(t, "compose", "docker-compose.yml"))

	p := blocks["pelicula"]
	for _, want := range []string{
		"context: ..",
		"dockerfile: compose/Dockerfile",
		"VERSION: ${PELICULA_VERSION:-dev}",
		`user: "${PUID`,
		"PELICULA_VPN=${PELICULA_VPN:-false}",
		"WEBHOOK_SECRET=",
		"JELLYFIN_ADMIN_USER=",
		"JELLYFIN_PASSWORD=",
		"GLUETUN_HTTP_USER=",
		"GLUETUN_HTTP_PASS=",
		"SERVER_COUNTRIES=",
		"HOST_CONFIG_DIR=${CONFIG_DIR}",
		"HOST_LIBRARY_DIR=${LIBRARY_DIR}",
		"HOST_WORK_DIR=${WORK_DIR}",
		"/pelicula:/config/pelicula\n",
		"/sonarr:/config/sonarr:ro",
		"/radarr:/config/radarr:ro",
		"/prowlarr:/config/prowlarr:ro",
		":/media:ro",
		"/api/health",
		"stop_grace_period: 20s",
		"- sonarr\n",
		"- radarr\n",
		"- jellyfin\n",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("pelicula service lacks %q", want)
		}
	}

	n := blocks["nginx"]
	for _, want := range []string{
		"nginx:1.28.3-alpine",
		"../nginx/nginx.conf:/etc/nginx/nginx.conf:ro",
		"../nginx/html:/usr/share/nginx/html:ro",
		`"${PELICULA_PORT:-7354}:80"`,
		"- pelicula\n",
	} {
		if !strings.Contains(n, want) {
			t.Errorf("nginx service lacks %q", want)
		}
	}

	// The library is mounted directly: rw for the media managers, ro for the server.
	for _, name := range []string{"sonarr", "radarr", "jellyfin"} {
		if !regexp.MustCompile(`\$\{LIBRARY_DIR[^}]*\}:/media\n`).MatchString(blocks[name]) {
			t.Errorf("%s must mount LIBRARY_DIR at /media read-write", name)
		}
	}
	for _, name := range []string{"sonarr", "radarr", "qbittorrent"} {
		if !strings.Contains(blocks[name], "/downloads\n") {
			t.Errorf("%s must mount the downloads directory", name)
		}
	}
	if !strings.Contains(blocks["jellyfin"], `"8920:8920"`) || !strings.Contains(blocks["jellyfin"], `"7359:7359/udp"`) {
		t.Error("jellyfin must publish 8920 and 7359/udp")
	}
}

func TestDockerfile(t *testing.T) {
	doc := readRepoFile(t, "compose", "Dockerfile")
	for _, want := range []string{
		"FROM golang:1.25-alpine AS build",
		"COPY go.mod go.sum ./",
		"RUN go mod download",
		"COPY . .",
		`-ldflags "-X main.version=${VERSION}"`,
		"CGO_ENABLED=0 go build",
		"./cmd/pelicula-server",
		"FROM alpine:3.20",
		"ca-certificates ffmpeg",
		"EXPOSE 8181",
		`CMD ["pelicula-server"]`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("Dockerfile lacks %q", want)
		}
	}
}

func TestIgnoreFiles(t *testing.T) {
	lines := func(doc string) []string {
		var out []string
		for _, l := range strings.Split(doc, "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				out = append(out, l)
			}
		}
		return out
	}
	git := lines(readRepoFile(t, ".gitignore"))
	// /config/ and /bin/ are anchored to the repo root: an unanchored config/
	// would also hide the server's internal/config package.
	for _, want := range []string{".env", ".env.bak*", "/config/", "/bin/", "compose/docker-compose.override.yml", "node_modules/", "tests/playwright/report/", "test-results/", ".DS_Store"} {
		if !slices.Contains(git, want) {
			t.Errorf(".gitignore lacks %s", want)
		}
	}
	docker := lines(readRepoFile(t, ".dockerignore"))
	for _, want := range []string{"config/", "bin/", "nginx/", "docs/", "tests/", "compose/*.yml", ".env*", "*.md", ".git"} {
		if !slices.Contains(docker, want) {
			t.Errorf(".dockerignore lacks %s", want)
		}
	}
}
