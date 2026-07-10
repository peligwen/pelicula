package main

import (
	"os"
	"path/filepath"
)

// Context holds the bootstrapped state for a pelicula command invocation.
// It is built once in main() and passed to each command, avoiding redundant
// platform detection (which runs docker info with a 5-second timeout) and
// repeated .env parsing.
type Context struct {
	ScriptDir string
	EnvFile   string
	Plat      Platform // result of Detect(), cached
	Env       EnvMap   // loaded .env; nil for pre-setup or env-less commands
}

// newContext builds a Context by detecting the script directory and running
// platform detection once. It does not load the .env — callers that need env
// should call ctx.LoadEnv() or handle env loading themselves.
//
// Two process-env variables are test/orchestration seams (not user-facing
// configuration — see docs/ARCHITECTURE.md's compose-overlays section):
//
//   - PELICULA_ENV_FILE overrides the .env path used for the entire
//     invocation (ctx.EnvFile, and therefore every downstream compose
//     invocation — see (*Context).newCompose). This lets tests/e2e.sh run an
//     isolated stack against its own .env without ever touching the
//     repo-root one.
//   - PELICULA_COMPOSE_OVERLAY names one extra compose file to merge in
//     (applied in Compose.buildArgs). It is validated here, once, so a
//     misconfigured test harness fails fast with one clear message instead
//     of failing deep inside a later `docker compose` invocation.
func newContext() *Context {
	scriptDir := getScriptDir()
	plat := Detect(scriptDir)

	envFile, err := resolveEnvFile(scriptDir, os.Getenv("PELICULA_ENV_FILE"))
	if err != nil {
		fatal("PELICULA_ENV_FILE is not a usable path: " + err.Error())
	}

	if overlay := os.Getenv("PELICULA_COMPOSE_OVERLAY"); overlay != "" {
		if err := validateComposeOverlay(overlay); err != nil {
			fatal(err.Error())
		}
	}

	return &Context{
		ScriptDir: scriptDir,
		EnvFile:   envFile,
		Plat:      plat,
	}
}

// resolveEnvFile computes the .env path for scriptDir given the raw value of
// PELICULA_ENV_FILE (empty when the override is unset). It is factored out
// of newContext as a pure function — newContext also runs platform detection
// (which shells out to `docker info`), so keeping the override's path logic
// separate lets it be unit tested without invoking Docker.
//
// override == "" reproduces the default, pre-override behavior exactly:
// scriptDir/.env. A non-empty override is honored verbatim if already
// absolute, or resolved against the current working directory otherwise —
// matching filepath.Abs's normal semantics.
func resolveEnvFile(scriptDir, override string) (string, error) {
	if override == "" {
		return filepath.Join(scriptDir, ".env"), nil
	}
	abs, err := filepath.Abs(override)
	if err != nil {
		return "", err
	}
	return abs, nil
}

// LoadEnv parses the .env file and caches it on ctx.Env.
// If the file cannot be read it calls fatal() and does not return.
func (ctx *Context) LoadEnv() {
	ctx.Env = loadEnvOrFatal(ctx.EnvFile)
}

// projectName returns the Docker Compose project name from ctx.Env, defaulting
// to "pelicula". This is used to pass --project-name to every docker compose
// invocation so container names are always pelicula-<service>-1 regardless of
// the directory the repo was cloned into.
func (ctx *Context) projectName() string {
	if ctx.Env != nil {
		if name := ctx.Env["PELICULA_PROJECT_NAME"]; name != "" {
			return name
		}
	}
	return "pelicula"
}

// newCompose returns a new *Compose rooted at ctx.ScriptDir using the cached
// platform info. The Compose uses ctx.EnvFile (which honors the
// PELICULA_ENV_FILE override — see newContext) for --env-file rather than
// re-deriving scriptDir/.env itself. Profiles are NOT set — callers that need
// profile-aware compose should use composeInvocation(ctx) instead.
func (ctx *Context) newCompose() *Compose {
	return NewCompose(ctx.ScriptDir, ctx.EnvFile, ctx.Plat.NeedsSudo, ctx.Plat.IsSynology, ctx.projectName())
}

// composeInvocation builds a fully configured *Compose with compose profiles
// (vpn, apprise) set from ctx.Env. Overlay files (override.yml, libraries.yml)
// are detected dynamically in Compose.buildArgs() as before.
//
// If ctx.Env is nil the returned Compose has no profiles — suitable for
// commands that run before .env exists (e.g. the setup wizard path in up).
func composeInvocation(ctx *Context) *Compose {
	c := ctx.newCompose()
	if ctx.Env == nil {
		return c
	}
	if wgKey := ctx.Env["WIREGUARD_PRIVATE_KEY"]; wgKey != "" {
		c.profiles = append(c.profiles, "vpn")
	}
	if envDefault(ctx.Env, "NOTIFICATIONS_MODE", "internal") == "apprise" {
		c.profiles = append(c.profiles, "apprise")
	}
	return c
}
