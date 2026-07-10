package main

import (
	"path/filepath"
	"testing"
)

// TestResolveEnvFile covers the PELICULA_ENV_FILE override logic in
// isolation from newContext's platform detection (which shells out to
// `docker info` and so is deliberately not exercised by any test in this
// package — see platform_test.go).
func TestResolveEnvFile(t *testing.T) {
	t.Run("override unset — defaults to scriptDir/.env", func(t *testing.T) {
		got, err := resolveEnvFile("/repo/pelicula", "")
		if err != nil {
			t.Fatalf("resolveEnvFile: %v", err)
		}
		want := filepath.Join("/repo/pelicula", ".env")
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("override set, already absolute — used verbatim", func(t *testing.T) {
		got, err := resolveEnvFile("/repo/pelicula", "/custom/test-instance/.env")
		if err != nil {
			t.Fatalf("resolveEnvFile: %v", err)
		}
		if got != "/custom/test-instance/.env" {
			t.Errorf("got %q, want /custom/test-instance/.env (verbatim, not re-rooted under scriptDir)", got)
		}
	})

	t.Run("override set, relative — made absolute", func(t *testing.T) {
		got, err := resolveEnvFile("/repo/pelicula", "relative/test.env")
		if err != nil {
			t.Fatalf("resolveEnvFile: %v", err)
		}
		if !filepath.IsAbs(got) {
			t.Errorf("expected an absolute path, got %q", got)
		}
		if filepath.Base(got) != "test.env" {
			t.Errorf("got %q, expected basename test.env", got)
		}
	})
}

// TestContextNewComposeUsesContextEnvFile is the CIT-1 regression test
// proving Compose is built from the Context's env file rather than
// re-deriving scriptDir/.env on its own. Constructs the Context by struct
// literal (bypassing newContext, and therefore Detect's `docker info` call)
// so this stays a fast, hermetic unit test.
func TestContextNewComposeUsesContextEnvFile(t *testing.T) {
	const overrideEnvFile = "/custom/test-instance/.env"

	ctx := &Context{
		ScriptDir: "/repo/pelicula",
		EnvFile:   overrideEnvFile,
		Plat:      Platform{},
	}

	c := ctx.newCompose()
	if c.envFile != overrideEnvFile {
		t.Errorf("Compose.envFile = %q, want ctx.EnvFile %q", c.envFile, overrideEnvFile)
	}
	if c.envFile == filepath.Join(ctx.ScriptDir, ".env") {
		t.Error("Compose.envFile equals the scriptDir-derived default — override was not honored")
	}
}

// TestContextNewComposeDefaultEnvFile is the unset-override companion to the
// above: when ctx.EnvFile carries the ordinary scriptDir/.env default (as
// newContext sets it when PELICULA_ENV_FILE is unset), Compose must still
// end up with exactly that path.
func TestContextNewComposeDefaultEnvFile(t *testing.T) {
	ctx := &Context{
		ScriptDir: "/repo/pelicula",
		EnvFile:   filepath.Join("/repo/pelicula", ".env"),
		Plat:      Platform{},
	}

	c := ctx.newCompose()
	want := filepath.Join("/repo/pelicula", ".env")
	if c.envFile != want {
		t.Errorf("Compose.envFile = %q, want default %q", c.envFile, want)
	}
}
