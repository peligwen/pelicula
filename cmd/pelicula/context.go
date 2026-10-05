package main

import (
	"path/filepath"
)

// Context holds the state shared by every command: the repo root, the .env
// path, the platform (detected once — it runs `docker info`) and, once
// loaded, the parsed .env.
type Context struct {
	ScriptDir string
	EnvFile   string
	Plat      Platform
	Env       EnvMap // nil until LoadEnv
}

func newContext() *Context {
	scriptDir := getScriptDir()
	return &Context{
		ScriptDir: scriptDir,
		EnvFile:   filepath.Join(scriptDir, ".env"),
		Plat:      Detect(scriptDir),
	}
}

// LoadEnv parses .env into ctx.Env, exiting if it is missing or unreadable.
func (ctx *Context) LoadEnv() {
	ctx.Env = loadEnvOrFatal(ctx.EnvFile)
}

// projectName returns the compose project name: PELICULA_PROJECT_NAME from
// .env, default "pelicula". Passing it as --project-name keeps container names
// independent of the directory the repo was cloned into.
func (ctx *Context) projectName() string {
	return envDefault(ctx.Env, "PELICULA_PROJECT_NAME", "pelicula")
}

// compose returns a Compose configured from ctx.Env: the vpn profile and
// PELICULA_VPN=true when a WireGuard key is set. With allProfiles the vpn
// profile is always enabled, so down/restart/logs/reset see (and therefore
// handle) VPN containers even if the key was removed after `up`.
func (ctx *Context) compose(allProfiles bool) *Compose {
	c := NewCompose(ctx.ScriptDir, ctx.EnvFile, ctx.Plat.NeedsSudo, ctx.Plat.IsSynology, ctx.projectName())
	c.vpn = vpnEnabled(ctx.Env)
	if c.vpn || allProfiles {
		c.profiles = []string{"vpn"}
	}
	return c
}
