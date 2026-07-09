package main

import (
	"slices"
	"testing"
)

// TestRestartAcquireComposeForcesVPNProfileWithoutWireguardKey verifies the
// CIT-7 fix: cmdRestartAcquire must be able to target gluetun/qbittorrent/
// prowlarr (all gated behind the "vpn" Compose profile) even on installs
// that never set WIREGUARD_PRIVATE_KEY. Without forcing the profile,
// composeInvocation would omit "vpn" and Compose would reject the stop/start
// call with "no such service".
func TestRestartAcquireComposeForcesVPNProfileWithoutWireguardKey(t *testing.T) {
	ctx := &Context{
		ScriptDir: t.TempDir(),
		Env:       EnvMap{}, // no WIREGUARD_PRIVATE_KEY — simulates a non-VPN install
	}

	c := restartAcquireCompose(ctx)

	if !slices.Contains(c.profiles, "vpn") {
		t.Fatalf("profiles = %v, want to contain %q", c.profiles, "vpn")
	}

	args := c.buildArgs(append([]string{"stop"}, acquireServices...)...)
	idx := slices.Index(args, "--profile")
	if idx == -1 {
		t.Fatal("buildArgs did not include --profile")
	}
	if args[idx+1] != "vpn" {
		t.Errorf("expected --profile vpn, got --profile %q", args[idx+1])
	}
	// Profile flags must precede the subcommand (required by Compose v5+).
	stopIdx := slices.Index(args, "stop")
	if stopIdx == -1 || stopIdx < idx {
		t.Errorf("--profile must appear before the stop subcommand; args=%v", args)
	}
}

// TestRestartAcquireComposeKeepsVPNProfileWithWireguardKey ensures the forced
// override doesn't regress the already-working VPN-configured case: the vpn
// profile must still be present (not duplicated oddly) when
// WIREGUARD_PRIVATE_KEY is set.
func TestRestartAcquireComposeKeepsVPNProfileWithWireguardKey(t *testing.T) {
	ctx := &Context{
		ScriptDir: t.TempDir(),
		Env:       EnvMap{"WIREGUARD_PRIVATE_KEY": "test-key"},
	}

	c := restartAcquireCompose(ctx)

	if !slices.Contains(c.profiles, "vpn") {
		t.Fatalf("profiles = %v, want to contain %q", c.profiles, "vpn")
	}
}

// TestRestartAcquireComposeOmitsAppriseProfile confirms restartAcquireCompose
// doesn't drag in the "apprise" profile even when NOTIFICATIONS_MODE=apprise —
// none of acquireServices belong to that profile, so it would be a no-op
// service reference at best.
func TestRestartAcquireComposeOmitsAppriseProfile(t *testing.T) {
	ctx := &Context{
		ScriptDir: t.TempDir(),
		Env:       EnvMap{"NOTIFICATIONS_MODE": "apprise"},
	}

	c := restartAcquireCompose(ctx)

	if slices.Contains(c.profiles, "apprise") {
		t.Errorf("profiles = %v, want no %q", c.profiles, "apprise")
	}
}
