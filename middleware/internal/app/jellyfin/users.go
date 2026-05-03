package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"unicode"

	jfclient "pelicula-api/internal/clients/jellyfin"
)

// ErrPasswordRequired is returned by CreateUser when password is empty.
var ErrPasswordRequired = errors.New("password is required")

// User is a minimal representation of a Jellyfin user for the dashboard.
type User struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	HasPassword      bool     `json:"hasPassword"`
	IsAdmin          bool     `json:"isAdmin"`
	IsDisabled       bool     `json:"isDisabled"`
	EnableAllFolders bool     `json:"enableAllFolders"`
	EnabledFolders   []string `json:"enabledFolders,omitempty"`
	LastLoginDate    string   `json:"lastLoginDate,omitempty"`
}

// Session is an active or recent Jellyfin session.
type Session struct {
	UserName         string `json:"userName"`
	DeviceName       string `json:"deviceName"`
	Client           string `json:"client"`
	LastActivityDate string `json:"lastActivityDate,omitempty"`
	NowPlayingTitle  string `json:"nowPlayingTitle,omitempty"`
	NowPlayingType   string `json:"nowPlayingType,omitempty"`
}

// ValidUsername reports whether the name is safe to send to Jellyfin:
// 1–64 chars, no leading/trailing whitespace, no control chars, no / or \.
func ValidUsername(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	if strings.TrimSpace(s) != s {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

// ValidJellyfinID returns true when id looks like a Jellyfin user ID.
// Accepts both the 32-char dashless hex form (wire format) and the 36-char
// dashed UUID form. Guards against path traversal.
func ValidJellyfinID(id string) bool {
	switch len(id) {
	case 32:
		for _, c := range id {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
		return true
	case 36:
		for i, c := range id {
			if i == 8 || i == 13 || i == 18 || i == 23 {
				if c != '-' {
					return false
				}
			} else if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// ListUsers returns all non-system Jellyfin users.
func (h *Handler) ListUsers(ctx context.Context) ([]User, error) {
	token, err := h.Auth(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth failed: %w", err)
	}
	data, err := h.Client.Get(ctx, "/Users", token)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse users: %w", err)
	}
	users := make([]User, 0, len(raw))
	for _, u := range raw {
		name, _ := u["Name"].(string)
		if name == h.ServiceUser {
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
		users = append(users, User{
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

// CreateUser creates a new Jellyfin user with the given name and password.
// Returns the new user's Jellyfin ID on success.
func (h *Handler) CreateUser(ctx context.Context, username, password string) (string, error) {
	if password == "" {
		return "", ErrPasswordRequired
	}
	if len(password) > 256 {
		return "", fmt.Errorf("password too long (max 256 chars)")
	}
	token, err := h.Auth(ctx)
	if err != nil {
		return "", fmt.Errorf("auth failed: %w", err)
	}
	data, err := h.Client.Post(ctx, "/Users/New", token, map[string]any{"Name": username})
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
	if !ValidJellyfinID(id) {
		return "", fmt.Errorf("unexpected user ID format from Jellyfin: %q", id)
	}
	// Set the password. If this fails, attempt to delete the user so the admin
	// isn't left with a passwordless account they can't see from Pelicula.
	pwBody, err := h.Client.Post(ctx, "/Users/"+id+"/Password", token, map[string]any{
		"CurrentPw": "",
		"NewPw":     password,
	})
	if err != nil {
		detail := jfclient.ExtractMessage(pwBody)
		msg := "set password failed"
		if detail != "" {
			msg += ": " + detail
		}
		if _, delErr := h.Client.Delete(ctx, "/Users/"+id, token); delErr != nil {
			slog.Warn("password set failed and rollback delete also failed", "component", "jellyfin", "userId", id, "deleteError", delErr)
			return "", fmt.Errorf("%s (rollback failed — delete user %q manually in Jellyfin): %w", msg, username, err)
		}
		return "", fmt.Errorf("%s (user was removed): %w", msg, err)
	}
	slog.Info("created Jellyfin user", "component", "jellyfin", "username", username)

	// Set preferred audio language on the new user.
	SetAudioPref(ctx, h.Client, token, id, h.AudioLang)

	return id, nil
}

// DeleteUser deletes a Jellyfin user by ID.
func (h *Handler) DeleteUser(ctx context.Context, id string) error {
	if !ValidJellyfinID(id) {
		return fmt.Errorf("invalid user ID format: %q", id)
	}
	token, err := h.Auth(ctx)
	if err != nil {
		return fmt.Errorf("auth failed: %w", err)
	}
	if _, err := h.Client.Delete(ctx, "/Users/"+id, token); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	slog.Info("deleted Jellyfin user", "component", "jellyfin", "userId", id)
	return nil
}

// SetUserPassword sets a new password for a Jellyfin user using a two-step
// clear-then-set flow.
func (h *Handler) SetUserPassword(ctx context.Context, id, newPw string) error {
	if newPw == "" {
		return ErrPasswordRequired
	}
	if len(newPw) > 256 {
		return fmt.Errorf("password too long (max 256 chars)")
	}
	if !ValidJellyfinID(id) {
		return fmt.Errorf("invalid user ID format: %q", id)
	}
	token, err := h.Auth(ctx)
	if err != nil {
		return fmt.Errorf("auth failed: %w", err)
	}
	if _, resetErr := h.Client.Post(ctx, "/Users/"+id+"/Password", token, map[string]any{
		"ResetPassword": true,
	}); resetErr != nil {
		slog.Error("password reset step failed", "component", "jellyfin", "userId", id, "error", resetErr)
	}
	body, err := h.Client.Post(ctx, "/Users/"+id+"/Password", token, map[string]any{
		"CurrentPw": "",
		"NewPw":     newPw,
	})
	if err != nil {
		msg := "set password failed"
		if detail := jfclient.ExtractMessage(body); detail != "" {
			msg += ": " + detail
		}
		return fmt.Errorf("%s: %w", msg, err)
	}
	slog.Info("reset Jellyfin user password", "component", "jellyfin", "userId", id)
	return nil
}

// SetUserDisabled enables or disables a Jellyfin user account.
// Uses GET-merge-POST to avoid zeroing out other policy fields.
func (h *Handler) SetUserDisabled(ctx context.Context, id string, disabled bool) error {
	if !ValidJellyfinID(id) {
		return fmt.Errorf("invalid user ID format: %q", id)
	}
	token, err := h.Auth(ctx)
	if err != nil {
		return fmt.Errorf("auth failed: %w", err)
	}
	userData, err := h.Client.Get(ctx, "/Users/"+id, token)
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
	if _, err := h.Client.Post(ctx, "/Users/"+id+"/Policy", token, policy); err != nil {
		return fmt.Errorf("post policy: %w", err)
	}
	action := "disabled"
	if !disabled {
		action = "enabled"
	}
	slog.Info("Jellyfin user account "+action, "component", "jellyfin", "userId", id)
	return nil
}

// SetUserLibraryAccess patches the user's policy to control access to libraries.
func (h *Handler) SetUserLibraryAccess(ctx context.Context, id string, movies, tv bool) error {
	if !ValidJellyfinID(id) {
		return fmt.Errorf("invalid user ID format: %q", id)
	}
	token, err := h.Auth(ctx)
	if err != nil {
		return fmt.Errorf("auth failed: %w", err)
	}
	userData, err := h.Client.Get(ctx, "/Users/"+id, token)
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
		libIDs, err := h.libraryIDs(ctx, token)
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

	if _, err := h.Client.Post(ctx, "/Users/"+id+"/Policy", token, policy); err != nil {
		return fmt.Errorf("post policy: %w", err)
	}
	slog.Info("updated library access", "component", "jellyfin", "userId", id, "movies", movies, "tv", tv)
	return nil
}

// GetSessions returns active/recent Jellyfin sessions for the now-playing card.
func (h *Handler) GetSessions(ctx context.Context) ([]Session, error) {
	token, err := h.Auth(ctx)
	if err != nil {
		return nil, fmt.Errorf("auth failed: %w", err)
	}
	data, err := h.Client.Get(ctx, "/Sessions", token)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse sessions: %w", err)
	}
	sessions := make([]Session, 0, len(raw))
	for _, r := range raw {
		userName, _ := r["UserName"].(string)
		if userName == "" {
			continue // skip system/device sessions with no user
		}
		deviceName, _ := r["DeviceName"].(string)
		client, _ := r["Client"].(string)
		lastActivity, _ := r["LastActivityDate"].(string)
		sess := Session{
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

// PromoteAdmin promotes userID to Jellyfin administrator using GET-merge-POST
// to avoid zeroing out other policy fields.
func PromoteAdmin(ctx context.Context, client *jfclient.Client, token, userID, username string) {
	userData, getErr := client.Get(ctx, "/Users/"+userID, token)
	if getErr != nil {
		slog.Warn("could not fetch user for admin promotion", "component", "autowire", "username", username, "error", getErr)
		return
	}
	var user map[string]any
	if jsonErr := json.Unmarshal(userData, &user); jsonErr != nil {
		slog.Warn("could not parse user data for admin promotion", "component", "autowire", "username", username, "error", jsonErr)
		return
	}
	policy, _ := user["Policy"].(map[string]any)
	if policy == nil {
		policy = map[string]any{}
	}
	policy["IsAdministrator"] = true
	if _, polErr := client.Post(ctx, "/Users/"+userID+"/Policy", token, policy); polErr != nil {
		slog.Warn("could not promote operator admin to Jellyfin admin", "component", "autowire", "username", username, "error", polErr)
		return
	}
	slog.Info("operator admin promoted to Jellyfin administrator", "component", "autowire", "username", username)
}

// SetAudioPref sets the user's preferred audio language preference in Jellyfin.
// Uses GET-merge-POST to avoid zeroing out other configuration fields.
func SetAudioPref(ctx context.Context, client *jfclient.Client, token, userID, lang string) {
	userData, err := client.Get(ctx, "/Users/"+userID, token)
	if err != nil {
		slog.Warn("could not fetch user for audio pref", "component", "autowire", "userId", userID, "error", err)
		return
	}
	var user map[string]any
	if jsonErr := json.Unmarshal(userData, &user); jsonErr != nil {
		slog.Warn("could not parse user data for audio pref", "component", "autowire", "userId", userID, "error", jsonErr)
		return
	}
	config, _ := user["Configuration"].(map[string]any)
	if config == nil {
		config = map[string]any{}
	}
	config["AudioLanguagePreference"] = lang
	config["PlayDefaultAudioTrack"] = false // honour AudioLanguagePreference, not just "first track"
	if _, cfgErr := client.Post(ctx, "/Users/"+userID+"/Configuration", token, config); cfgErr != nil {
		slog.Warn("could not set Jellyfin audio language preference", "component", "autowire", "userId", userID, "lang", lang, "error", cfgErr)
		return
	}
	slog.Info("Jellyfin audio language preference set", "component", "autowire", "userId", userID, "lang", lang)
}

// WireLibrary creates a Jellyfin virtual library if it doesn't already exist,
// or repairs its path if the library exists but points at a stale location.
// A library whose name matches is checked for path consistency: if the expected
// path is missing it is added via /Library/VirtualFolders/Paths, and any
// previously-registered paths that differ are removed.
func WireLibrary(ctx context.Context, client *jfclient.Client, token, name, collectionType, path string) {
	data, err := client.Get(ctx, "/Library/VirtualFolders", token)
	if err != nil {
		slog.Error("failed to list Jellyfin libraries", "component", "autowire", "error", err)
		return
	}
	var libraries []struct {
		Name      string   `json:"Name"`
		Locations []string `json:"Locations"`
	}
	if json.Unmarshal(data, &libraries) == nil {
		for _, lib := range libraries {
			if lib.Name != name {
				continue
			}
			// Library exists — check whether the correct path is already present.
			for _, loc := range lib.Locations {
				if loc == path {
					slog.Info("Jellyfin library path is correct, skipping", "component", "autowire", "library", name)
					return
				}
			}
			// Correct path is absent — add it first, then clean up stale paths.
			addEndpoint := fmt.Sprintf("/Library/VirtualFolders/Paths?name=%s&refreshLibrary=false", url.QueryEscape(name))
			addBody := map[string]any{
				"Name":     name,
				"PathInfo": map[string]any{"Path": path},
			}
			if _, addErr := client.Post(ctx, addEndpoint, token, addBody); addErr != nil {
				slog.Error("failed to add path to Jellyfin library", "component", "autowire", "library", name, "path", path, "error", addErr)
				return
			}
			slog.Info("added correct path to Jellyfin library", "component", "autowire", "library", name, "path", path)
			for _, stale := range lib.Locations {
				delEndpoint := fmt.Sprintf("/Library/VirtualFolders/Paths?name=%s&path=%s&refreshLibrary=false",
					url.QueryEscape(name), url.QueryEscape(stale))
				if _, delErr := client.Delete(ctx, delEndpoint, token); delErr != nil {
					slog.Warn("failed to remove stale path from Jellyfin library", "component", "autowire", "library", name, "stale", stale, "error", delErr)
				} else {
					slog.Info("removed stale path from Jellyfin library", "component", "autowire", "library", name, "stale", stale)
				}
			}
			return
		}
	}

	endpoint := fmt.Sprintf("/Library/VirtualFolders?name=%s&collectionType=%s&refreshLibrary=false",
		url.QueryEscape(name), url.QueryEscape(collectionType))
	body := map[string]any{
		"LibraryOptions": map[string]any{
			"PathInfos": []map[string]any{
				{"Path": path},
			},
		},
	}
	_, err = client.Post(ctx, endpoint, token, body)
	if err != nil {
		slog.Error("failed to create Jellyfin library", "component", "autowire", "library", name, "error", err)
		return
	}
	slog.Info("added Jellyfin library", "component", "autowire", "library", name, "path", path)
}

// libraryIDs returns a map of library name → Jellyfin folder ID.
func (h *Handler) libraryIDs(ctx context.Context, token string) (map[string]string, error) {
	data, err := h.Client.Get(ctx, "/Library/VirtualFolders", token)
	if err != nil {
		return nil, err
	}
	var folders []struct {
		Name   string `json:"Name"`
		ItemId string `json:"ItemId"`
	}
	if err := json.Unmarshal(data, &folders); err != nil {
		return nil, err
	}
	ids := make(map[string]string, len(folders))
	for _, f := range folders {
		ids[f.Name] = f.ItemId
	}
	return ids, nil
}
