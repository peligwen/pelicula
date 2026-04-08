// Peligrosa: roles store for jellyfin auth mode.
// Maps Jellyfin user ID → Pelicula role. No passwords stored — Jellyfin is
// the authority. See ../PELIGROSA.md for the trust model.
package main

import (
	"encoding/json"
	"os"
	"sync"
)

// RolesEntry maps a Jellyfin user ID to a Pelicula role.
type RolesEntry struct {
	JellyfinID string   `json:"jellyfin_id"`
	Username   string   `json:"username"`
	Role       UserRole `json:"role"`
}

type rolesFile struct {
	Version int          `json:"version"`
	Users   []RolesEntry `json:"users"`
}

// RolesStore persists the Jellyfin user ID → Pelicula role mapping at path.
// Thread-safe; safe to call from multiple goroutines.
type RolesStore struct {
	path string
	mu   sync.RWMutex
	data rolesFile
}

// NewRolesStore creates a RolesStore backed by path.
// If the file does not exist it starts with an empty store (not an error).
func NewRolesStore(path string) *RolesStore {
	rs := &RolesStore{path: path}
	rs.load()
	return rs
}

func (rs *RolesStore) load() {
	data, err := os.ReadFile(rs.path)
	if err != nil {
		rs.data = rolesFile{Version: 1}
		return
	}
	var f rolesFile
	if json.Unmarshal(data, &f) != nil {
		rs.data = rolesFile{Version: 1}
		return
	}
	if f.Users == nil {
		f.Users = []RolesEntry{}
	}
	rs.data = f
}

// IsEmpty reports whether the store has no user entries.
func (rs *RolesStore) IsEmpty() bool {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return len(rs.data.Users) == 0
}

// Lookup returns the stored role for the given Jellyfin user ID.
func (rs *RolesStore) Lookup(jellyfinID string) (UserRole, bool) {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	for _, u := range rs.data.Users {
		if u.JellyfinID == jellyfinID {
			return u.Role, true
		}
	}
	return "", false
}

// Upsert sets the role for a Jellyfin user ID, creating the entry if absent.
// Also refreshes the stored display name.
func (rs *RolesStore) Upsert(jellyfinID, username string, role UserRole) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for i, u := range rs.data.Users {
		if u.JellyfinID == jellyfinID {
			rs.data.Users[i].Role = role
			rs.data.Users[i].Username = username
			return rs.save()
		}
	}
	rs.data.Users = append(rs.data.Users, RolesEntry{
		JellyfinID: jellyfinID,
		Username:   username,
		Role:       role,
	})
	return rs.save()
}

// All returns a snapshot of all role entries.
func (rs *RolesStore) All() []RolesEntry {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	result := make([]RolesEntry, len(rs.data.Users))
	copy(result, rs.data.Users)
	return result
}

// save writes the store to disk. Caller must hold rs.mu (write lock).
func (rs *RolesStore) save() error {
	rs.data.Version = 1
	data, err := json.MarshalIndent(rs.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(rs.path, data, 0600)
}
