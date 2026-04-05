package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestUserRoleAtLeast(t *testing.T) {
	cases := []struct {
		role UserRole
		min  UserRole
		want bool
	}{
		{RoleViewer, RoleViewer, true},
		{RoleViewer, RoleManager, false},
		{RoleViewer, RoleAdmin, false},
		{RoleManager, RoleViewer, true},
		{RoleManager, RoleManager, true},
		{RoleManager, RoleAdmin, false},
		{RoleAdmin, RoleViewer, true},
		{RoleAdmin, RoleManager, true},
		{RoleAdmin, RoleAdmin, true},
	}
	for _, c := range cases {
		t.Run(string(c.role)+"/"+string(c.min), func(t *testing.T) {
			got := c.role.atLeast(c.min)
			if got != c.want {
				t.Errorf("%q.atLeast(%q) = %v, want %v", c.role, c.min, got, c.want)
			}
		})
	}
}

func TestHashPassword(t *testing.T) {
	t.Run("format is sha256v2:SALT:HASH", func(t *testing.T) {
		h := HashPassword("alice", "secret")
		parts := strings.Split(h, ":")
		if len(parts) != 3 {
			t.Fatalf("expected 3 colon-separated parts, got %d in %q", len(parts), h)
		}
		if parts[0] != "sha256v2" {
			t.Errorf("prefix = %q, want %q", parts[0], "sha256v2")
		}
		if len(parts[1]) == 0 {
			t.Error("salt is empty")
		}
		if len(parts[2]) == 0 {
			t.Error("hash is empty")
		}
	})

	t.Run("same password hashed twice has different salts", func(t *testing.T) {
		h1 := HashPassword("alice", "secret")
		h2 := HashPassword("alice", "secret")
		if h1 == h2 {
			t.Error("expected different hashes due to random salt")
		}
	})
}

func TestVerifyPassword(t *testing.T) {
	t.Run("correct password verifies", func(t *testing.T) {
		h := HashPassword("alice", "correct-horse")
		if !verifyPassword("alice", "correct-horse", h) {
			t.Error("expected correct password to verify")
		}
	})

	t.Run("wrong password fails", func(t *testing.T) {
		h := HashPassword("alice", "correct-horse")
		if verifyPassword("alice", "wrong-horse", h) {
			t.Error("expected wrong password to fail")
		}
	})

	t.Run("legacy format: plain sha256 of user:pass", func(t *testing.T) {
		// Legacy format: sha256(username + ":" + password) as plain hex, no prefix.
		raw := sha256.Sum256([]byte("alice:legacy"))
		legacyHash := hex.EncodeToString(raw[:])
		if !verifyPassword("alice", "legacy", legacyHash) {
			t.Error("expected legacy hash to verify correctly")
		}
	})

	t.Run("empty stored hash fails gracefully", func(t *testing.T) {
		if verifyPassword("alice", "anything", "") {
			t.Error("expected empty stored hash to return false")
		}
	})

	t.Run("wrong username fails even with correct password", func(t *testing.T) {
		h := HashPassword("alice", "secret")
		if verifyPassword("bob", "secret", h) {
			t.Error("expected wrong username to fail verification")
		}
	})
}
