package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// DualSubProfile controls font, layout, and positioning for a dual-subtitle ASS output.
type DualSubProfile struct {
	Name     string  `json:"name"`
	Builtin  bool    `json:"builtin,omitempty"`
	Layout   string  `json:"layout"`    // "stacked_bottom" | "stacked_top" | "split"
	FontSize int     `json:"font_size"` // points in ASS PlayRes coordinate space
	FontName string  `json:"font_name"`
	Outline  float64 `json:"outline"`
	MarginV  int     `json:"margin_v"` // pixels from edge
	Gap      int     `json:"gap"`      // stacked layouts only: space between top and bottom line
}

// TrackPair identifies the two subtitle sources to combine for one output sidecar.
// Each side is either a sidecar file path or an embedded stream index.
// A SubIndex of -1 means "use the file path"; >= 0 means "use embedded stream N".
type TrackPair struct {
	TopFile        string `json:"top_file"`         // sidecar file path (empty if using embedded)
	BottomFile     string `json:"bottom_file"`      // sidecar file path (empty if using embedded)
	TopSubIndex    int    `json:"top_sub_index"`    // embedded stream index (-1 = not used)
	BottomSubIndex int    `json:"bottom_sub_index"` // embedded stream index (-1 = not used)
}

func builtinDualSubProfiles() []DualSubProfile {
	return []DualSubProfile{
		{
			Name: "Default", Builtin: true,
			Layout: "stacked_bottom", FontSize: 52, FontName: "Arial",
			Outline: 2, MarginV: 40, Gap: 10,
		},
		{
			Name: "Large split", Builtin: true,
			Layout: "split", FontSize: 64, FontName: "Arial",
			Outline: 2, MarginV: 40,
		},
		{
			Name: "Stacked top", Builtin: true,
			Layout: "stacked_top", FontSize: 52, FontName: "Arial",
			Outline: 2, MarginV: 40, Gap: 10,
		},
	}
}

// ListDualSubProfiles returns built-ins followed by user-defined profiles from DB.
func ListDualSubProfiles(db *sql.DB) ([]DualSubProfile, error) {
	profiles := builtinDualSubProfiles()
	if db == nil {
		return profiles, nil
	}
	rows, err := db.Query(`SELECT name, data FROM dualsub_profiles ORDER BY rowid`)
	if err != nil {
		return profiles, nil // table may not exist yet on old DBs
	}
	defer rows.Close()
	for rows.Next() {
		var name, data string
		if err := rows.Scan(&name, &data); err != nil {
			continue
		}
		var p DualSubProfile
		if json.Unmarshal([]byte(data), &p) == nil {
			p.Name = name // DB key is authoritative
			p.Builtin = false
			profiles = append(profiles, p)
		}
	}
	return profiles, rows.Err()
}

// SaveDualSubProfile creates or replaces a user-defined profile. Returns an error
// if the name matches a built-in.
func SaveDualSubProfile(db *sql.DB, p DualSubProfile) error {
	for _, b := range builtinDualSubProfiles() {
		if b.Name == p.Name {
			return fmt.Errorf("cannot overwrite built-in profile %q", p.Name)
		}
	}
	p.Builtin = false
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = db.Exec(
		`INSERT INTO dualsub_profiles (name, data) VALUES (?, ?)
		 ON CONFLICT(name) DO UPDATE SET data=excluded.data`,
		p.Name, string(data),
	)
	return err
}

// DeleteDualSubProfile removes a user-defined profile. Returns an error if the
// name matches a built-in.
func DeleteDualSubProfile(db *sql.DB, name string) error {
	for _, b := range builtinDualSubProfiles() {
		if b.Name == name {
			return fmt.Errorf("cannot delete built-in profile %q", name)
		}
	}
	_, err := db.Exec(`DELETE FROM dualsub_profiles WHERE name=?`, name)
	return err
}

// FindDualSubProfile looks up a profile by name (built-ins first, then DB).
// Returns the Default built-in if name is empty or not found.
func FindDualSubProfile(db *sql.DB, name string) DualSubProfile {
	all, _ := ListDualSubProfiles(db)
	for _, p := range all {
		if p.Name == name {
			return p
		}
	}
	// fall back to Default
	for _, p := range all {
		if p.Name == "Default" {
			return p
		}
	}
	return builtinDualSubProfiles()[0]
}
