package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
)

// JellyfinUser is a minimal representation of a Jellyfin user for the dashboard.
type JellyfinUser struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	HasPassword      bool     `json:"hasPassword"`
	IsAdmin          bool     `json:"isAdmin"`
	IsDisabled       bool     `json:"isDisabled"`
	EnableAllFolders bool     `json:"enableAllFolders"`
	EnabledFolders   []string `json:"enabledFolders,omitempty"`
	LastLoginDate    string   `json:"lastLoginDate,omitempty"`
}

// JellyfinSession is an active or recent Jellyfin session, for the now-playing card.
type JellyfinSession struct {
	UserName         string `json:"userName"`
	DeviceName       string `json:"deviceName"`
	Client           string `json:"client"`
	LastActivityDate string `json:"lastActivityDate,omitempty"`
	NowPlayingTitle  string `json:"nowPlayingTitle,omitempty"`
	NowPlayingType   string `json:"nowPlayingType,omitempty"`
}

// ListJellyfinUsers returns all non-system Jellyfin users.
func ListJellyfinUsers(s *ServiceClients) ([]JellyfinUser, error) {
	token, err := jellyfinAuth(s)
	if err != nil {
		return nil, fmt.Errorf("auth failed: %w", err)
	}
	data, err := jellyfinGet(s, "/Users", token)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse users: %w", err)
	}
	users := make([]JellyfinUser, 0, len(raw))
	for _, u := range raw {
		name, _ := u["Name"].(string)
		if name == jellyfinServiceUser {
			continue // hide internal service account from admin UI
		}
		id, _ := u["Id"].(string)
		hasPass, _ := u["HasPassword"].(bool)
		lastLogin, _ := u["LastLoginDate"].(string)
		isAdmin, isDisabled, enableAll := false, false, true
		var enabledFolders []string
		if policy, ok := u["Policy"].(map[string]any); ok {
			isAdmin, _ = policy["IsAdministrator"].(bool)
			isDisabled, _ = policy["IsDisabled"].(bool)
			if v, ok := policy["EnableAllFolders"].(bool); ok {
				enableAll = v
			}
			if raw, ok := policy["EnabledFolders"].([]any); ok {
				for _, f := range raw {
					if s, ok := f.(string); ok {
						enabledFolders = append(enabledFolders, s)
					}
				}
			}
		}
		users = append(users, JellyfinUser{
			ID:               id,
			Name:             name,
			HasPassword:      hasPass,
			IsAdmin:          isAdmin,
			IsDisabled:       isDisabled,
			EnableAllFolders: enableAll,
			EnabledFolders:   enabledFolders,
			LastLoginDate:    lastLogin,
		})
	}
	return users, nil
}

// DeleteJellyfinUser deletes a Jellyfin user by ID.
func DeleteJellyfinUser(s *ServiceClients, id string) error {
	if !validJellyfinID(id) {
		return fmt.Errorf("invalid user ID format: %q", id)
	}
	token, err := jellyfinAuth(s)
	if err != nil {
		return fmt.Errorf("auth failed: %w", err)
	}
	if _, err := jellyfinDelete(s, "/Users/"+id, token); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	slog.Info("deleted Jellyfin user", "component", "jellyfin", "userId", id)
	return nil
}

// SetJellyfinUserPassword sets a new password for a Jellyfin user.
// Uses a two-step flow: clear the existing password first (ResetPassword:true),
// then set the new one with an empty CurrentPw. This works for users with or
// without an existing password; Jellyfin rejects a bare NewPw when a password
// is already set.
func SetJellyfinUserPassword(s *ServiceClients, id, newPw string) error {
	if newPw == "" {
		return ErrPasswordRequired
	}
	if len(newPw) > 256 {
		return fmt.Errorf("password too long (max 256 chars)")
	}
	if !validJellyfinID(id) {
		return fmt.Errorf("invalid user ID format: %q", id)
	}
	token, err := jellyfinAuth(s)
	if err != nil {
		return fmt.Errorf("auth failed: %w", err)
	}
	if _, resetErr := jellyfinPost(s, "/Users/"+id+"/Password", token, map[string]any{
		"ResetPassword": true,
	}); resetErr != nil {
		slog.Error("password reset step failed", "component", "jellyfin", "userId", id, "error", resetErr)
	}
	body, err := jellyfinPost(s, "/Users/"+id+"/Password", token, map[string]any{
		"CurrentPw": "",
		"NewPw":     newPw,
	})
	if err != nil {
		msg := "set password failed"
		if detail := jellyfinMessage(body); detail != "" {
			msg += ": " + detail
		}
		return fmt.Errorf("%s: %w", msg, err)
	}
	slog.Info("reset Jellyfin user password", "component", "jellyfin", "userId", id)
	return nil
}

// SetJellyfinUserDisabled enables or disables a Jellyfin user account.
// It GETs the full current policy, sets IsDisabled, then POSTs the entire
// policy back — Jellyfin replaces the full policy on POST, so partial updates
// would zero out other fields.
func SetJellyfinUserDisabled(s *ServiceClients, id string, disabled bool) error {
	if !validJellyfinID(id) {
		return fmt.Errorf("invalid user ID format: %q", id)
	}
	token, err := jellyfinAuth(s)
	if err != nil {
		return fmt.Errorf("auth failed: %w", err)
	}
	userData, err := jellyfinGet(s, "/Users/"+id, token)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	var user map[string]any
	if err := json.Unmarshal(userData, &user); err != nil {
		return fmt.Errorf("decode user: %w", err)
	}
	policy, _ := user["Policy"].(map[string]any)
	if policy == nil {
		policy = map[string]any{}
	}
	policy["IsDisabled"] = disabled
	if _, err := jellyfinPost(s, "/Users/"+id+"/Policy", token, policy); err != nil {
		return fmt.Errorf("post policy: %w", err)
	}
	action := "disabled"
	if !disabled {
		action = "enabled"
	}
	slog.Info("Jellyfin user account "+action, "component", "jellyfin", "userId", id)
	return nil
}

// SetJellyfinUserLibraryAccess patches the user's policy to control access to
// the "Movies" and "TV Shows" libraries. When both are true, EnableAllFolders
// is set to true. When partial, EnableAllFolders is false and EnabledFolders
// lists the selected library IDs.
func SetJellyfinUserLibraryAccess(s *ServiceClients, id string, movies, tv bool) error {
	if !validJellyfinID(id) {
		return fmt.Errorf("invalid user ID format: %q", id)
	}
	token, err := jellyfinAuth(s)
	if err != nil {
		return fmt.Errorf("auth failed: %w", err)
	}
	userData, err := jellyfinGet(s, "/Users/"+id, token)
	if err != nil {
		return fmt.Errorf("get user: %w", err)
	}
	var user map[string]any
	if err := json.Unmarshal(userData, &user); err != nil {
		return fmt.Errorf("decode user: %w", err)
	}
	policy, _ := user["Policy"].(map[string]any)
	if policy == nil {
		policy = map[string]any{}
	}

	if movies && tv {
		policy["EnableAllFolders"] = true
		policy["EnabledFolders"] = []string{}
	} else {
		libIDs, err := jellyfinLibraryIDs(s, token)
		if err != nil {
			return fmt.Errorf("get library IDs: %w", err)
		}
		var folders []string
		if movies {
			if fid, ok := libIDs["Movies"]; ok {
				folders = append(folders, fid)
			}
		}
		if tv {
			if fid, ok := libIDs["TV Shows"]; ok {
				folders = append(folders, fid)
			}
		}
		policy["EnableAllFolders"] = false
		policy["EnabledFolders"] = folders
	}

	if _, err := jellyfinPost(s, "/Users/"+id+"/Policy", token, policy); err != nil {
		return fmt.Errorf("post policy: %w", err)
	}
	slog.Info("updated library access", "component", "jellyfin", "userId", id, "movies", movies, "tv", tv)
	return nil
}

// GetJellyfinSessions returns active/recent Jellyfin sessions for the now-playing card.
func GetJellyfinSessions(s *ServiceClients) ([]JellyfinSession, error) {
	token, err := jellyfinAuth(s)
	if err != nil {
		return nil, fmt.Errorf("auth failed: %w", err)
	}
	data, err := jellyfinGet(s, "/Sessions", token)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse sessions: %w", err)
	}
	sessions := make([]JellyfinSession, 0, len(raw))
	for _, r := range raw {
		userName, _ := r["UserName"].(string)
		if userName == "" {
			continue // skip system/device sessions with no user
		}
		deviceName, _ := r["DeviceName"].(string)
		client, _ := r["Client"].(string)
		lastActivity, _ := r["LastActivityDate"].(string)
		sess := JellyfinSession{
			UserName:         userName,
			DeviceName:       deviceName,
			Client:           client,
			LastActivityDate: lastActivity,
		}
		if npi, ok := r["NowPlayingItem"].(map[string]any); ok {
			sess.NowPlayingTitle, _ = npi["Name"].(string)
			sess.NowPlayingType, _ = npi["Type"].(string)
		}
		sessions = append(sessions, sess)
	}
	return sessions, nil
}

// CreateJellyfinUser creates a new Jellyfin user with the given name and password.
// password must be non-empty. Returns the new user's Jellyfin ID on success.
func CreateJellyfinUser(s *ServiceClients, username, password string) (string, error) {
	if password == "" {
		return "", ErrPasswordRequired
	}
	if len(password) > 256 {
		return "", fmt.Errorf("password too long (max 256 chars)")
	}
	token, err := jellyfinAuth(s)
	if err != nil {
		return "", fmt.Errorf("auth failed: %w", err)
	}
	// Create the user account
	data, err := jellyfinPost(s, "/Users/New", token, map[string]any{"Name": username})
	if err != nil {
		return "", fmt.Errorf("create user: %w", err)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("parse create response: %w", err)
	}
	id, _ := result["Id"].(string)
	if id == "" {
		return "", fmt.Errorf("no user ID in create response")
	}
	// Validate the ID is a UUID before embedding it in a URL path.
	// Jellyfin always returns UUIDs; anything else is malformed or adversarial.
	if !validJellyfinID(id) {
		return "", fmt.Errorf("unexpected user ID format from Jellyfin: %q", id)
	}
	// Set the password. If this fails, attempt to delete the user so the admin
	// isn't left with a passwordless account they can't see from Pelicula.
	pwBody, err := jellyfinPost(s, "/Users/"+id+"/Password", token, map[string]any{
		"CurrentPw": "",
		"NewPw":     password,
	})
	if err != nil {
		detail := jellyfinMessage(pwBody)
		msg := "set password failed"
		if detail != "" {
			msg += ": " + detail
		}
		if _, delErr := jellyfinDelete(s, "/Users/"+id, token); delErr != nil {
			slog.Warn("password set failed and rollback delete also failed", "component", "jellyfin", "userId", id, "deleteError", delErr)
			return "", fmt.Errorf("%s (rollback failed — delete user %q manually in Jellyfin): %w", msg, username, err)
		}
		return "", fmt.Errorf("%s (user was removed): %w", msg, err)
	}
	slog.Info("created Jellyfin user", "component", "jellyfin", "username", username)

	// Set preferred audio language on the new user.
	setJellyfinAudioPref(s, token, id)

	return id, nil
}
