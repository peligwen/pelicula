package main

import "path/filepath"

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
func newContext() *Context {
	scriptDir := getScriptDir()
	plat := Detect(scriptDir)
	return &Context{
		ScriptDir: scriptDir,
		EnvFile:   filepath.Join(scriptDir, ".env"),
		Plat:      plat,
	}
}

// LoadEnv parses the .env file and caches it on ctx.Env.
// If the file cannot be read it calls fatal() and does not return.
func (ctx *Context) LoadEnv() {
	ctx.Env = loadEnvOrFatal(ctx.EnvFile)
}

// newCompose returns a new *Compose rooted at ctx.ScriptDir using the cached
// platform info. Profiles are NOT set — callers that need profile-aware
// compose should use composeInvocation(ctx) instead.
func (ctx *Context) newCompose() *Compose {
	return NewCompose(ctx.ScriptDir, ctx.Plat.NeedsSudo, ctx.Plat.IsSynology)
}

// composeInvocation builds a fully configured *Compose with compose profiles
// (vpn, apprise) set from ctx.Env. Overlay files (override.yml, remote.yml,
// libraries.yml) are detected dynamically in Compose.buildArgs() as before.
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
