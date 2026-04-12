# Subtitle consolidation + dual subtitle profile system — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Consolidate subtitle search into one dialog, and replace the bare dualsub action with a profile-based system that lets operators pick fonts, layouts, and exact subtitle tracks per generation.

**Architecture:** Backend changes are all in `procula/` — new `DualSubProfile` type + CRUD stored in the settings DB, a `subtitle-tracks` discovery endpoint, and updated `writeASS` that accepts a profile and per-file track paths. Frontend replaces two context menu items with one subtitle-search dialog and rewrites the dualsub dialog to expose track pickers and an inline profile editor.

**Tech Stack:** Go (stdlib + modernc.org/sqlite), vanilla JS, ASS subtitle format

---

## File map

| File | Change |
|------|--------|
| `procula/dualsub_profiles.go` | **Create** — `DualSubProfile` type, built-in profiles, DB CRUD (`ListDualSubProfiles`, `SaveDualSubProfile`, `DeleteDualSubProfile`) |
| `procula/dualsub_profiles_test.go` | **Create** — unit tests for profile CRUD and built-in list |
| `procula/dualsub.go` | **Modify** — update `writeASS`/`writeASSContent` to accept `DualSubProfile`; add `subtitleTracksForPath` discovery func; add `DeleteDualSubSidecars` helper |
| `procula/dualsub_test.go` | **Modify** — add tests for layout margin formulas and sidecar discovery |
| `procula/actions.go` | **Modify** — update `runDualSubAction` to accept `DualSubJob` payload (profile + pairs); remove `subtitle_refresh` and `subtitle_request` actions; add `subtitle_search` action |
| `procula/main.go` | **Modify** — add routes: `GET /api/procula/dualsub-profiles`, `POST /api/procula/dualsub-profiles`, `PUT /api/procula/dualsub-profiles/{name}`, `DELETE /api/procula/dualsub-profiles/{name}`, `GET /api/procula/subtitle-tracks` |
| `procula/db.go` | **Modify** — add `dualsub_profiles` table migration |
| `nginx/index.html` | **Modify** — replace `sub-req-dialog` + `dualsub-dialog` with new `subtitle-search-dialog` and `dualsub-dialog` (full redesign) |
| `nginx/catalog.js` | **Modify** — replace `subtitle_refresh`/`subtitle_request` menu handling with `subtitle_search`; rewrite dualsub dialog open/submit logic with profile + track picker |

---

## Task 1: `DualSubProfile` type and built-in profiles

**Files:**
- Create: `procula/dualsub_profiles.go`
- Create: `procula/dualsub_profiles_test.go`

- [ ] **Step 1: Write the failing test**

```go
// procula/dualsub_profiles_test.go
package main

import (
	"testing"
)

func TestBuiltinDualSubProfiles(t *testing.T) {
	profiles := builtinDualSubProfiles()
	if len(profiles) != 3 {
		t.Fatalf("expected 3 built-in profiles, got %d", len(profiles))
	}
	names := map[string]bool{}
	for _, p := range profiles {
		names[p.Name] = true
		if !p.Builtin {
			t.Errorf("profile %q: Builtin should be true", p.Name)
		}
		if p.FontSize <= 0 {
			t.Errorf("profile %q: FontSize must be positive", p.Name)
		}
		if p.FontName == "" {
			t.Errorf("profile %q: FontName must not be empty", p.Name)
		}
		switch p.Layout {
		case "stacked_bottom", "stacked_top", "split":
		default:
			t.Errorf("profile %q: unknown layout %q", p.Name, p.Layout)
		}
	}
	if !names["Default"] {
		t.Error("missing built-in profile 'Default'")
	}
	// Default must be stacked_bottom
	for _, p := range profiles {
		if p.Name == "Default" && p.Layout != "stacked_bottom" {
			t.Errorf("Default profile layout = %q, want stacked_bottom", p.Layout)
		}
	}
}
```

- [ ] **Step 2: Run test to confirm it fails**

```bash
cd /Users/gwen/workspace/pelicula/procula && go test -run TestBuiltinDualSubProfiles -v
```
Expected: compile error — `builtinDualSubProfiles` undefined.

- [ ] **Step 3: Create `procula/dualsub_profiles.go`**

```go
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

// DualSubJob is the payload sent with the dualsub action.
type DualSubJob struct {
	Profile string      `json:"profile"`
	Pairs   []TrackPair `json:"pairs"`
}

// TrackPair identifies the two subtitle files to combine for one output sidecar.
type TrackPair struct {
	TopFile    string `json:"top_file"`    // e.g. "Movie.en.srt"
	BottomFile string `json:"bottom_file"` // e.g. "Movie.es.srt"
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
```

- [ ] **Step 4: Run test to confirm it passes**

```bash
cd /Users/gwen/workspace/pelicula/procula && go test -run TestBuiltinDualSubProfiles -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/gwen/workspace/pelicula
git add procula/dualsub_profiles.go procula/dualsub_profiles_test.go
git commit -m "feat(dualsub): DualSubProfile type, built-ins, DB CRUD"
```

---

## Task 2: DB migration for `dualsub_profiles` table

**Files:**
- Modify: `procula/db.go`

- [ ] **Step 1: Find the migration list**

```bash
grep -n "CREATE TABLE\|migrations\|migration" /Users/gwen/workspace/pelicula/procula/db.go | head -30
```

- [ ] **Step 2: Add the migration**

Open `procula/db.go`. Find the migrations slice (a `[]string` of `CREATE TABLE` or `ALTER TABLE` statements). Append as the last entry:

```go
`CREATE TABLE IF NOT EXISTS dualsub_profiles (
    name TEXT PRIMARY KEY,
    data TEXT NOT NULL
)`,
```

Do not renumber or reorder existing entries.

- [ ] **Step 3: Build to confirm no compile errors**

```bash
cd /Users/gwen/workspace/pelicula/procula && go build ./...
```
Expected: no output (success).

- [ ] **Step 4: Commit**

```bash
cd /Users/gwen/workspace/pelicula
git add procula/db.go
git commit -m "feat(dualsub): add dualsub_profiles table migration"
```

---

## Task 3: Subtitle track discovery

**Files:**
- Modify: `procula/dualsub.go`
- Modify: `procula/dualsub_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `procula/dualsub_test.go`:

```go
func TestSubtitleTracksForPath(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "Movie.mkv")
	files := []string{
		"Movie.en.srt",
		"Movie.en.hi.srt",
		"Movie.es.srt",
		"Movie.es.forced.srt",
		"Movie.fr.sdh.srt",
	}
	for _, f := range files {
		os.WriteFile(filepath.Join(dir, f), []byte(""), 0644)
	}
	tracks := subtitleTracksForPath(base)
	if len(tracks) != len(files) {
		t.Fatalf("expected %d tracks, got %d", len(files), len(tracks))
	}
	byFile := map[string]SubtitleTrack{}
	for _, tr := range tracks {
		byFile[filepath.Base(tr.File)] = tr
	}
	if byFile["Movie.en.srt"].Variant != "regular" {
		t.Errorf("Movie.en.srt variant = %q, want regular", byFile["Movie.en.srt"].Variant)
	}
	if byFile["Movie.en.hi.srt"].Variant != "hi" {
		t.Errorf("Movie.en.hi.srt variant = %q, want hi", byFile["Movie.en.hi.srt"].Variant)
	}
	if byFile["Movie.es.forced.srt"].Variant != "forced" {
		t.Errorf("Movie.es.forced.srt variant = %q, want forced", byFile["Movie.es.forced.srt"].Variant)
	}
	if byFile["Movie.fr.sdh.srt"].Variant != "hi" {
		t.Errorf("Movie.fr.sdh.srt variant = %q, want hi (sdh maps to hi)", byFile["Movie.fr.sdh.srt"].Variant)
	}
	if byFile["Movie.en.srt"].Lang != "en" {
		t.Errorf("Movie.en.srt lang = %q, want en", byFile["Movie.en.srt"].Lang)
	}
}

func TestDetectSubVariant(t *testing.T) {
	cases := []struct{ file, want string }{
		{"Movie.en.srt", "regular"},
		{"Movie.en.hi.srt", "hi"},
		{"Movie.en.HI.srt", "hi"},
		{"Movie.es.forced.srt", "forced"},
		{"Movie.fr.sdh.srt", "hi"},
		{"Movie.de.srt", "regular"},
	}
	for _, c := range cases {
		got := detectSubVariant(c.file)
		if got != c.want {
			t.Errorf("detectSubVariant(%q) = %q, want %q", c.file, got, c.want)
		}
	}
}
```

Ensure `"path/filepath"` and `"os"` are in the import block (they already are).

- [ ] **Step 2: Run tests to confirm they fail**

```bash
cd /Users/gwen/workspace/pelicula/procula && go test -run "TestSubtitleTracksForPath|TestDetectSubVariant" -v
```
Expected: compile error — `subtitleTracksForPath`, `SubtitleTrack`, `detectSubVariant` undefined.

- [ ] **Step 3: Add to `procula/dualsub.go` after the `subStream` type**

```go
// SubtitleTrack describes one subtitle sidecar file alongside a media item.
type SubtitleTrack struct {
	File    string `json:"file"`    // full path
	Lang    string `json:"lang"`    // 2-letter code, e.g. "en"
	Variant string `json:"variant"` // "regular" | "hi" | "forced"
}

// subtitleTracksForPath returns all subtitle sidecar files alongside mediaPath.
// Detects lang and variant from filename conventions:
//   Movie.en.srt       → {lang:"en", variant:"regular"}
//   Movie.en.hi.srt    → {lang:"en", variant:"hi"}
//   Movie.es.forced.srt → {lang:"es", variant:"forced"}
// Dual-sub sidecars (e.g. Movie.en-es.ass) are excluded.
func subtitleTracksForPath(mediaPath string) []SubtitleTrack {
	dir := filepath.Dir(mediaPath)
	base := strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var tracks []SubtitleTrack
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".srt" && ext != ".ass" {
			continue
		}
		if !strings.HasPrefix(name, base+".") {
			continue
		}
		// Strip base. and extension to get the tag portion: e.g. "en", "en.hi", "es.forced"
		inner := name[len(base)+1 : len(name)-len(ext)]
		// Exclude dualsub sidecars (e.g. "en-es")
		if strings.ContainsRune(inner, '-') {
			continue
		}
		parts := strings.Split(strings.ToLower(inner), ".")
		if len(parts) == 0 || parts[0] == "" {
			continue
		}
		lang := normalizeLangCode(parts[0])
		variant := "regular"
		for _, tag := range parts[1:] {
			v := detectSubVariant("." + tag + ".")
			if v != "regular" {
				variant = v
				break
			}
		}
		tracks = append(tracks, SubtitleTrack{
			File:    filepath.Join(dir, name),
			Lang:    lang,
			Variant: variant,
		})
	}
	return tracks
}

// detectSubVariant returns "hi", "forced", or "regular" based on filename tags.
func detectSubVariant(filename string) string {
	lower := strings.ToLower(filename)
	if strings.Contains(lower, ".hi.") || strings.Contains(lower, ".sdh.") {
		return "hi"
	}
	if strings.Contains(lower, ".forced.") {
		return "forced"
	}
	return "regular"
}

// DeleteDualSubSidecars removes all dual-sub ASS sidecars alongside mediaPath
// (those matching base.<lang>-<lang>.ass). Returns the count deleted.
func DeleteDualSubSidecars(mediaPath string) int {
	dir := filepath.Dir(mediaPath)
	base := strings.TrimSuffix(filepath.Base(mediaPath), filepath.Ext(mediaPath))
	entries, _ := os.ReadDir(dir)
	count := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base+".") || !strings.HasSuffix(name, ".ass") {
			continue
		}
		inner := name[len(base)+1 : len(name)-4]
		if strings.ContainsRune(inner, '-') {
			os.Remove(filepath.Join(dir, name)) //nolint:errcheck
			count++
		}
	}
	return count
}
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
cd /Users/gwen/workspace/pelicula/procula && go test -run "TestSubtitleTracksForPath|TestDetectSubVariant" -v
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/gwen/workspace/pelicula
git add procula/dualsub.go procula/dualsub_test.go
git commit -m "feat(dualsub): subtitle track discovery and sidecar cleanup helper"
```

---

## Task 4: Profile-aware `writeASS`

**Files:**
- Modify: `procula/dualsub.go`
- Modify: `procula/dualsub_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `procula/dualsub_test.go`:

```go
func TestWriteASSLayout_StackedBottom(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ass")
	prof := DualSubProfile{
		Layout: "stacked_bottom", FontSize: 52, FontName: "Arial",
		Outline: 2, MarginV: 40, Gap: 10,
	}
	cues := []SubtitleCue{{Start: 0, End: time.Second, Text: "Hello"}}
	sec := []string{"Hola"}
	if err := writeASS(out, prof, cues, sec); err != nil {
		t.Fatalf("writeASS: %v", err)
	}
	data, _ := os.ReadFile(out)
	content := string(data)
	// Bottom style: Alignment=2, MarginV=40
	if !strings.Contains(content, "Style: Bottom,Arial,52,") {
		t.Error("missing Bottom style with Arial,52")
	}
	// Top style: MarginV = margin_v + font_size + gap = 40+52+10 = 102
	if !strings.Contains(content, ",8,10,10,102,") {
		t.Errorf("Top style MarginV should be 102 (40+52+10); content:\n%s", content)
	}
}

func TestWriteASSLayout_Split(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ass")
	prof := DualSubProfile{
		Layout: "split", FontSize: 64, FontName: "Arial",
		Outline: 2, MarginV: 40,
	}
	cues := []SubtitleCue{{Start: 0, End: time.Second, Text: "Hello"}}
	sec := []string{"Hola"}
	if err := writeASS(out, prof, cues, sec); err != nil {
		t.Fatalf("writeASS: %v", err)
	}
	data, _ := os.ReadFile(out)
	content := string(data)
	// Top style: Alignment=8, MarginV=40
	if !strings.Contains(content, ",8,10,10,40,") {
		t.Errorf("Top style should have Alignment=8 MarginV=40; content:\n%s", content)
	}
	// Bottom style: Alignment=2, MarginV=40
	if !strings.Contains(content, ",2,10,10,40,") {
		t.Errorf("Bottom style should have Alignment=2 MarginV=40; content:\n%s", content)
	}
}

func TestWriteASSLayout_StackedTop(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.ass")
	prof := DualSubProfile{
		Layout: "stacked_top", FontSize: 52, FontName: "Arial",
		Outline: 2, MarginV: 40, Gap: 10,
	}
	cues := []SubtitleCue{{Start: 0, End: time.Second, Text: "Hello"}}
	sec := []string{"Hola"}
	if err := writeASS(out, prof, cues, sec); err != nil {
		t.Fatalf("writeASS: %v", err)
	}
	data, _ := os.ReadFile(out)
	content := string(data)
	// Top style: Alignment=8, MarginV=40
	if !strings.Contains(content, ",8,10,10,40,") {
		t.Errorf("Top style should have Alignment=8 MarginV=40; content:\n%s", content)
	}
	// Bottom/secondary for stacked_top: MarginV = 40+52+10 = 102, also Alignment=8
	if !strings.Contains(content, ",8,10,10,102,") {
		t.Errorf("Secondary style MarginV should be 102; content:\n%s", content)
	}
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
cd /Users/gwen/workspace/pelicula/procula && go test -run "TestWriteASSLayout" -v
```
Expected: compile error — `writeASS` signature mismatch (old signature takes `baseLang, secLang string, baseCues, secTexts`).

- [ ] **Step 3: Replace `writeASS` and `writeASSContent` in `procula/dualsub.go`**

Find the existing `writeASS` (line ~410) and `writeASSContent` (line ~419) functions and replace both with:

```go
// writeASS writes a dual-language ASS subtitle file using the given profile.
// topCues are rendered at the top position; bottomTexts (parallel to topCues) at the bottom.
// Uses atomic write (partial → rename).
func writeASS(outPath string, prof DualSubProfile, topCues []SubtitleCue, bottomTexts []string) error {
	tmp := outPath + ".partial"
	if err := writeASSContent(tmp, prof, topCues, bottomTexts); err != nil {
		os.Remove(tmp) //nolint:errcheck
		return err
	}
	return os.Rename(tmp, outPath)
}

func writeASSContent(path string, prof DualSubProfile, topCues []SubtitleCue, bottomTexts []string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)

	// Per-layout alignment (ASS: 2=bottom-center, 8=top-center) and margins
	type styleParams struct {
		alignment int
		marginV   int
	}
	var topStyle, bottomStyle styleParams
	switch prof.Layout {
	case "stacked_top":
		topStyle = styleParams{8, prof.MarginV}
		bottomStyle = styleParams{8, prof.MarginV + prof.FontSize + prof.Gap}
	case "split":
		topStyle = styleParams{8, prof.MarginV}
		bottomStyle = styleParams{2, prof.MarginV}
	default: // "stacked_bottom"
		topStyle = styleParams{8, prof.MarginV + prof.FontSize + prof.Gap}
		bottomStyle = styleParams{2, prof.MarginV}
	}

	outlineStr := fmt.Sprintf("%.0f", prof.Outline)
	if prof.Outline != float64(int(prof.Outline)) {
		outlineStr = fmt.Sprintf("%.1f", prof.Outline)
	}

	fmt.Fprintf(w, "[Script Info]\n")
	fmt.Fprintf(w, "; Generated by Procula (dual subtitles)\n")
	fmt.Fprintf(w, "ScriptType: v4.00+\n")
	fmt.Fprintf(w, "Collisions: Normal\n")
	fmt.Fprintf(w, "PlayResX: 1920\n")
	fmt.Fprintf(w, "PlayResY: 1080\n\n")

	// Colour: White=&H00FFFFFF&, Yellow=&H0000FFFF&, Black=&H00000000&
	fmt.Fprintf(w, "[V4+ Styles]\n")
	fmt.Fprintf(w, "Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	fmt.Fprintf(w, "Style: Top,%s,%d,&H0000FFFF&,&H000000FF&,&H00000000&,&H80000000&,-1,0,0,0,100,100,0,0,1,%s,0,%d,10,10,%d,1\n",
		prof.FontName, prof.FontSize, outlineStr, topStyle.alignment, topStyle.marginV)
	fmt.Fprintf(w, "Style: Bottom,%s,%d,&H00FFFFFF&,&H000000FF&,&H00000000&,&H80000000&,-1,0,0,0,100,100,0,0,1,%s,0,%d,10,10,%d,1\n",
		prof.FontName, prof.FontSize, outlineStr, bottomStyle.alignment, bottomStyle.marginV)
	fmt.Fprintf(w, "\n")

	fmt.Fprintf(w, "[Events]\n")
	fmt.Fprintf(w, "Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")

	anTop := fmt.Sprintf("{\\an%d}", topStyle.alignment)
	anBottom := fmt.Sprintf("{\\an%d}", bottomStyle.alignment)

	for i, cue := range topCues {
		start := fmtASS(cue.Start)
		end := fmtASS(cue.End)
		if tt := toASSText(cue.Text); tt != "" {
			fmt.Fprintf(w, "Dialogue: 0,%s,%s,Top,,0,0,0,,%s%s\n", start, end, anTop, tt)
		}
		if i < len(bottomTexts) {
			if bt := toASSText(bottomTexts[i]); bt != "" {
				fmt.Fprintf(w, "Dialogue: 0,%s,%s,Bottom,,0,0,0,,%s%s\n", start, end, anBottom, bt)
			}
		}
	}
	return w.Flush()
}
```

- [ ] **Step 4: Update `generatePair` caller to pass a profile**

The existing `generatePair` function calls the old `writeASS`. Update its signature to accept a `DualSubProfile` and forward it:

```go
func generatePair(ctx context.Context, job *Job, baseLang, secLang, outPath string, settings PipelineSettings, configDir string, prof DualSubProfile) (string, error) {
    // ... existing probe/cue logic unchanged ...

    // Align secondary cues to base cue timing
    secTexts := alignCues(baseCues, secCues)

    if err := writeASS(outPath, prof, baseCues, secTexts); err != nil {
        return "", fmt.Errorf("write ASS: %w", err)
    }
    return outPath, nil
}
```

Update `GenerateDualSubs` to pass the Default profile:

```go
prof := FindDualSubProfile(nil, "Default")
path, err := generatePair(ctx, job, baseLang, secLang, outPath, settings, configDir, prof)
```

- [ ] **Step 5: Run all dualsub tests**

```bash
cd /Users/gwen/workspace/pelicula/procula && go test -run "TestWriteASS|TestParseSRT|TestAlignCues|TestFmtASS" -v
```
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
cd /Users/gwen/workspace/pelicula
git add procula/dualsub.go procula/dualsub_test.go
git commit -m "feat(dualsub): profile-aware writeASS with layout margin formulas"
```

---

## Task 5: Updated `dualsub` action + new `subtitle_search` action

**Files:**
- Modify: `procula/actions.go`

- [ ] **Step 1: Write a failing test**

Check if `procula/actions_test.go` exists: `ls procula/actions_test.go`. If it doesn't exist, create it. Add:

```go
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunDualSubAction_MissingPath(t *testing.T) {
	job := &Job{Params: map[string]any{}}
	_, err := runDualSubAction(context.Background(), nil, job)
	if err == nil || err.Error() != "dualsub: path required" {
		t.Errorf("expected 'dualsub: path required', got %v", err)
	}
}

func TestRunDualSubAction_MissingPairs(t *testing.T) {
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "Movie.mkv")
	os.WriteFile(mediaPath, []byte(""), 0644)
	job := &Job{Params: map[string]any{"path": mediaPath}}
	_, err := runDualSubAction(context.Background(), nil, job)
	if err == nil || err.Error() != "dualsub: at least one track pair required" {
		t.Errorf("expected pairs error, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to confirm it fails**

```bash
cd /Users/gwen/workspace/pelicula/procula && go test -run "TestRunDualSubAction" -v
```
Expected: test failure (existing handler doesn't check for pairs).

- [ ] **Step 3: Rewrite `runDualSubAction` in `procula/actions.go`**

Replace the existing `runDualSubAction` function (starts around line 267):

```go
// runDualSubAction accepts a DualSubJob payload via job.Params:
//   "path"    string        (required) — media file path
//   "profile" string        (optional) — profile name; defaults to "Default"
//   "pairs"   []interface{} (required) — each entry: {"top_file":"…","bottom_file":"…"}
func runDualSubAction(ctx context.Context, q *Queue, job *Job) (map[string]any, error) {
	path, _ := job.Params["path"].(string)
	if path == "" {
		return nil, fmt.Errorf("dualsub: path required")
	}

	rawPairs, _ := job.Params["pairs"].([]any)
	var pairs []TrackPair
	for _, rp := range rawPairs {
		m, ok := rp.(map[string]any)
		if !ok {
			continue
		}
		top, _ := m["top_file"].(string)
		bottom, _ := m["bottom_file"].(string)
		if top != "" && bottom != "" {
			pairs = append(pairs, TrackPair{TopFile: top, BottomFile: bottom})
		}
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("dualsub: at least one track pair required")
	}

	profileName, _ := job.Params["profile"].(string)
	prof := FindDualSubProfile(appDB, profileName)

	var outputs []string
	var warnings []string
	for _, pair := range pairs {
		topBase := strings.TrimSuffix(filepath.Base(pair.TopFile), filepath.Ext(pair.TopFile))
		botBase := strings.TrimSuffix(filepath.Base(pair.BottomFile), filepath.Ext(pair.BottomFile))
		topLang := langTagFromBase(topBase)
		botLang := langTagFromBase(botBase)
		mediaBase := strings.TrimSuffix(path, filepath.Ext(path))
		outPath := mediaBase + "." + topLang + "-" + botLang + ".ass"

		os.Remove(outPath) //nolint:errcheck

		topCues, err := parseSidecarFile(pair.TopFile)
		if err != nil || len(topCues) == 0 {
			warnings = append(warnings, fmt.Sprintf("top track %s: %v", filepath.Base(pair.TopFile), err))
			continue
		}
		botCues, err := parseSidecarFile(pair.BottomFile)
		if err != nil || len(botCues) == 0 {
			warnings = append(warnings, fmt.Sprintf("bottom track %s: %v", filepath.Base(pair.BottomFile), err))
			continue
		}

		bottomTexts := alignCues(topCues, botCues)
		if err := writeASS(outPath, prof, topCues, bottomTexts); err != nil {
			warnings = append(warnings, fmt.Sprintf("write %s: %v", filepath.Base(outPath), err))
			continue
		}
		outputs = append(outputs, outPath)
		slog.Info("dual sub generated", "component", "dualsub", "output", outPath, "profile", prof.Name)
	}

	result := map[string]any{"outputs": outputs}
	if len(warnings) > 0 {
		result["warnings"] = warnings
	}
	return result, nil
}

// langTagFromBase extracts the primary language tag from a subtitle base name.
// "Movie.en" → "en", "Movie.en.hi" → "en"
func langTagFromBase(base string) string {
	parts := strings.Split(base, ".")
	if len(parts) >= 2 {
		return normalizeLangCode(parts[len(parts)-1])
	}
	return base
}

// parseSidecarFile reads SubtitleCues from a .srt or .ass file.
func parseSidecarFile(path string) ([]SubtitleCue, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if strings.ToLower(filepath.Ext(path)) == ".ass" {
		return parseASSCues(data)
	}
	return parseSRT(data)
}

// parseASSCues extracts SubtitleCues from an ASS file by reading [Events] Dialogue lines.
func parseASSCues(data []byte) ([]SubtitleCue, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	var cues []SubtitleCue
	inEvents := false
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "[Events]" {
			inEvents = true
			continue
		}
		if !inEvents || !strings.HasPrefix(line, "Dialogue:") {
			continue
		}
		// Format: Layer,Start,End,Style,Name,MarginL,MarginR,MarginV,Effect,Text
		fields := strings.SplitN(line[len("Dialogue:"):], ",", 10)
		if len(fields) < 10 {
			continue
		}
		start, err1 := parseASSTime(strings.TrimSpace(fields[1]))
		end, err2 := parseASSTime(strings.TrimSpace(fields[2]))
		if err1 != nil || err2 != nil {
			continue
		}
		rawText := strings.TrimSpace(fields[9])
		rawText = assTagRE.ReplaceAllString(rawText, "")
		rawText = strings.ReplaceAll(rawText, "\\N", "\n")
		if rawText != "" {
			cues = append(cues, SubtitleCue{Start: start, End: end, Text: rawText})
		}
	}
	return cues, nil
}

// parseASSTime parses an ASS timestamp "H:MM:SS.cs" into a Duration.
func parseASSTime(s string) (time.Duration, error) {
	var h, m, sec, cs int
	if _, err := fmt.Sscanf(s, "%d:%d:%d.%d", &h, &m, &sec, &cs); err != nil {
		return 0, fmt.Errorf("parse ASS time %q: %w", s, err)
	}
	return time.Duration(h)*time.Hour +
		time.Duration(m)*time.Minute +
		time.Duration(sec)*time.Second +
		time.Duration(cs)*10*time.Millisecond, nil
}
```

- [ ] **Step 4: Update `registerBuiltinActions` in `procula/actions.go`**

In `registerBuiltinActions`, replace the three subtitle-related registrations:

Remove:
```go
Register(&ActionDef{Name: "subtitle_refresh", ...})
Register(&ActionDef{Name: "subtitle_request", ...})
```

Replace the `dualsub` registration with the updated label and description:
```go
Register(&ActionDef{
    Name:        "subtitle_search",
    Label:       "Search subtitles\u2026",
    AppliesTo:   []string{"movie", "episode"},
    Sync:        true,
    Description: "Search Bazarr for subtitles with explicit languages, HI, and forced flags.",
    Handler:     runSubtitleSearchAction,
})
Register(&ActionDef{
    Name:        "dualsub",
    Label:       "Dual subtitles\u2026",
    AppliesTo:   []string{"movie", "episode"},
    Sync:        true,
    Description: "Generate dual-language ASS subtitle sidecars with a chosen profile and track pair.",
    Handler:     runDualSubAction,
})
```

Rename `runSubtitleRequestAction` to `runSubtitleSearchAction` (update the function definition and remove `runSubtitleRefreshAction`). The `runSubtitleSearchAction` body is identical to the old `runSubtitleRequestAction`.

- [ ] **Step 5: Run tests**

```bash
cd /Users/gwen/workspace/pelicula/procula && go test -run "TestRunDualSubAction" -v
```
Expected: PASS.

- [ ] **Step 6: Full build**

```bash
cd /Users/gwen/workspace/pelicula/procula && go build ./...
```
Expected: no errors.

- [ ] **Step 7: Commit**

```bash
cd /Users/gwen/workspace/pelicula
git add procula/actions.go procula/actions_test.go
git commit -m "feat(dualsub): profile+pair action handler; rename subtitle_request to subtitle_search"
```

---

## Task 6: New API endpoints in `procula/main.go`

**Files:**
- Modify: `procula/main.go`

- [ ] **Step 1: Add routes to the `mux` block in `main()`**

After the existing `/api/procula/profiles` routes, add:

```go
mux.HandleFunc("GET /api/procula/dualsub-profiles", srv.handleListDualSubProfiles)
mux.HandleFunc("POST /api/procula/dualsub-profiles", requireAPIKey(srv.handleSaveDualSubProfile))
mux.HandleFunc("PUT /api/procula/dualsub-profiles/{name}", requireAPIKey(srv.handleSaveDualSubProfile))
mux.HandleFunc("DELETE /api/procula/dualsub-profiles/{name}", requireAPIKey(srv.handleDeleteDualSubProfile))
mux.HandleFunc("GET /api/procula/subtitle-tracks", srv.handleSubtitleTracks)
```

- [ ] **Step 2: Add handler functions at the bottom of `procula/main.go`**

```go
func (s *Server) handleListDualSubProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := ListDualSubProfiles(s.db)
	if err != nil {
		writeError(w, "failed to list profiles: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, profiles)
}

func (s *Server) handleSaveDualSubProfile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var p DualSubProfile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, "invalid request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if p.Name == "" {
		writeError(w, "name is required", http.StatusBadRequest)
		return
	}
	if err := SaveDualSubProfile(s.db, p); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, p)
}

func (s *Server) handleDeleteDualSubProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := DeleteDualSubProfile(s.db, name); err != nil {
		writeError(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSubtitleTracks(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, "path is required", http.StatusBadRequest)
		return
	}
	clean := filepath.Clean(path)
	if !isLibraryPath(clean) {
		writeError(w, "path must be under /movies or /tv", http.StatusBadRequest)
		return
	}
	tracks := subtitleTracksForPath(clean)
	if tracks == nil {
		tracks = []SubtitleTrack{}
	}
	writeJSON(w, map[string]any{"tracks": tracks})
}
```

- [ ] **Step 3: Build and run full test suite**

```bash
cd /Users/gwen/workspace/pelicula/procula && go build ./... && go test ./... -count=1
```
Expected: build succeeds; all tests pass (or only pre-existing failures).

- [ ] **Step 4: Commit**

```bash
cd /Users/gwen/workspace/pelicula
git add procula/main.go
git commit -m "feat(dualsub): add dualsub-profiles and subtitle-tracks API endpoints"
```

---

## Task 7: Frontend — dialog HTML

**Files:**
- Modify: `nginx/index.html`

- [ ] **Step 1: Replace the subtitle request dialog HTML**

Find the block starting with `<!-- Subtitle request dialog -->` (around line 946) through `</div>` at line ~968 and replace it with:

```html
<!-- Subtitle search dialog -->
<div class="modal-backdrop hidden" id="sub-search-backdrop" onclick="subSearchClose()"></div>
<div class="cat-modal hidden" id="sub-search-dialog" role="dialog" aria-label="Search subtitles">
    <div class="drawer-header">
        <div>
            <div class="drawer-title">Search subtitles</div>
            <div class="drawer-sub" id="sub-search-sub"></div>
        </div>
        <button class="drawer-close" onclick="subSearchClose()">&times;</button>
    </div>
    <div class="drawer-body">
        <div class="drawer-section-title">Languages</div>
        <div id="sub-search-langs" class="sub-req-langs"></div>
        <div class="drawer-section-title" style="margin-top:.75rem">Options</div>
        <div style="display:flex;flex-direction:column;gap:.55rem;margin-bottom:1rem">
            <div>
                <label style="display:flex;align-items:center;gap:.45rem;font-size:.82rem;cursor:pointer">
                    <input type="checkbox" id="sub-search-hi"> <span>Hearing impaired</span>
                </label>
                <div style="font-size:.72rem;color:var(--muted);margin-left:1.35rem;margin-top:.15rem">Includes sound-effect descriptions and speaker labels alongside dialogue</div>
            </div>
            <div>
                <label style="display:flex;align-items:center;gap:.45rem;font-size:.82rem;cursor:pointer">
                    <input type="checkbox" id="sub-search-forced"> <span>Forced only</span>
                </label>
                <div style="font-size:.72rem;color:var(--muted);margin-left:1.35rem;margin-top:.15rem">Only foreign-language lines within otherwise native-audio content</div>
            </div>
        </div>
        <div class="modal-buttons">
            <button class="modal-btn-cancel" onclick="subSearchClose()">Cancel</button>
            <button class="modal-btn-confirm" onclick="subSearchSubmit()">Search</button>
        </div>
        <div id="sub-search-status" class="no-items"></div>
    </div>
</div>
```

- [ ] **Step 2: Replace the dualsub dialog HTML**

Find the block starting with `<!-- Dual subtitle regeneration dialog -->` (around line 970) through its closing `</div>` and replace it with:

```html
<!-- Dual subtitle dialog -->
<div class="modal-backdrop hidden" id="dualsub-backdrop" onclick="dualsubClose()"></div>
<div class="cat-modal hidden" id="dualsub-dialog" role="dialog" aria-label="Dual subtitles" style="max-width:480px">
    <div class="drawer-header">
        <div>
            <div class="drawer-title">Dual subtitles</div>
            <div class="drawer-sub" id="dualsub-sub"></div>
        </div>
        <button class="drawer-close" onclick="dualsubClose()">&times;</button>
    </div>
    <div class="drawer-body">
        <div class="drawer-section-title">On disk</div>
        <div id="dualsub-ondisk" style="margin-bottom:1rem"></div>

        <div class="drawer-section-title">Profile</div>
        <div style="display:flex;gap:.5rem;align-items:center;margin-bottom:1rem">
            <select id="dualsub-profile-select" style="flex:1;background:var(--bg);border:1px solid var(--border);border-radius:4px;padding:.35rem .6rem;font-size:.82rem;color:var(--text)" onchange="dualsubProfileChanged()"></select>
            <button id="dualsub-manage-btn" style="padding:.35rem .6rem;border-radius:4px;border:1px solid var(--border);background:transparent;color:var(--muted);font-size:.75rem;cursor:pointer;white-space:nowrap" onclick="dualsubToggleManage()">Manage &#9660;</button>
        </div>

        <div id="dualsub-profile-editor" style="display:none;border:1px solid var(--border);border-radius:8px;padding:.85rem;background:var(--bg);margin-bottom:1rem">
            <div class="drawer-section-title" style="margin-bottom:.6rem">Edit profile</div>
            <div style="display:grid;grid-template-columns:1fr 1fr;gap:.5rem .75rem;margin-bottom:.75rem">
                <div>
                    <div style="font-size:.7rem;color:var(--muted);margin-bottom:.2rem">Name</div>
                    <input id="dsp-name" style="width:100%;box-sizing:border-box;background:var(--surface);border:1px solid var(--border);border-radius:4px;padding:.3rem .5rem;font-size:.8rem;color:var(--text)">
                </div>
                <div>
                    <div style="font-size:.7rem;color:var(--muted);margin-bottom:.2rem">Font</div>
                    <input id="dsp-font" style="width:100%;box-sizing:border-box;background:var(--surface);border:1px solid var(--border);border-radius:4px;padding:.3rem .5rem;font-size:.8rem;color:var(--text)">
                </div>
                <div>
                    <div style="font-size:.7rem;color:var(--muted);margin-bottom:.2rem">Font size (pt)</div>
                    <input type="number" id="dsp-size" style="width:100%;box-sizing:border-box;background:var(--surface);border:1px solid var(--border);border-radius:4px;padding:.3rem .5rem;font-size:.8rem;color:var(--text)">
                </div>
                <div>
                    <div style="font-size:.7rem;color:var(--muted);margin-bottom:.2rem">Outline</div>
                    <input type="number" step="0.5" id="dsp-outline" style="width:100%;box-sizing:border-box;background:var(--surface);border:1px solid var(--border);border-radius:4px;padding:.3rem .5rem;font-size:.8rem;color:var(--text)">
                </div>
                <div>
                    <div style="font-size:.7rem;color:var(--muted);margin-bottom:.2rem">Margin (px)</div>
                    <input type="number" id="dsp-margin" style="width:100%;box-sizing:border-box;background:var(--surface);border:1px solid var(--border);border-radius:4px;padding:.3rem .5rem;font-size:.8rem;color:var(--text)">
                </div>
                <div>
                    <div style="font-size:.7rem;color:var(--muted);margin-bottom:.2rem">Gap &#8212; stacked (px)</div>
                    <input type="number" id="dsp-gap" style="width:100%;box-sizing:border-box;background:var(--surface);border:1px solid var(--border);border-radius:4px;padding:.3rem .5rem;font-size:.8rem;color:var(--text)">
                </div>
            </div>
            <div style="font-size:.7rem;color:var(--muted);margin-bottom:.4rem">Layout</div>
            <div id="dsp-layout-btns" style="display:flex;gap:.4rem;margin-bottom:.75rem">
                <button data-layout="stacked_bottom" onclick="dualsubSetLayout(this)" style="flex:1;padding:.3rem .4rem;border-radius:4px;border:1px solid var(--border);background:transparent;color:var(--muted);font-size:.72rem;cursor:pointer">Stacked &#8595;</button>
                <button data-layout="stacked_top" onclick="dualsubSetLayout(this)" style="flex:1;padding:.3rem .4rem;border-radius:4px;border:1px solid var(--border);background:transparent;color:var(--muted);font-size:.72rem;cursor:pointer">Stacked &#8593;</button>
                <button data-layout="split" onclick="dualsubSetLayout(this)" style="flex:1;padding:.3rem .4rem;border-radius:4px;border:1px solid var(--border);background:transparent;color:var(--muted);font-size:.72rem;cursor:pointer">Split</button>
            </div>
            <div style="display:flex;gap:.4rem">
                <button onclick="dualsubSaveAsNew()" style="padding:.28rem .6rem;border-radius:4px;border:1px solid var(--border);background:transparent;color:var(--muted);font-size:.72rem;cursor:pointer">+ Save as new</button>
                <button id="dsp-update-btn" onclick="dualsubUpdateProfile()" style="padding:.28rem .6rem;border-radius:4px;border:none;background:var(--accent);color:#000;font-size:.72rem;font-weight:600;cursor:pointer">Update</button>
                <button id="dsp-delete-btn" onclick="dualsubDeleteProfile()" style="margin-left:auto;padding:.28rem .6rem;border-radius:4px;border:1px solid #c0392b;background:transparent;color:#c0392b;font-size:.72rem;cursor:pointer">Delete</button>
            </div>
        </div>

        <div class="drawer-section-title">Track pairs</div>
        <div id="dualsub-pairs"></div>
        <button onclick="dualsubAddPair()" style="font-size:.75rem;color:var(--muted);background:transparent;border:1px dashed var(--border);border-radius:4px;padding:.25rem .65rem;cursor:pointer;margin:0 0 1rem">+ Add pair</button>

        <div class="modal-buttons">
            <button class="modal-btn-cancel" onclick="dualsubClose()">Cancel</button>
            <button class="modal-btn-confirm" onclick="dualsubSubmit()">Generate</button>
        </div>
        <div id="dualsub-status" class="no-items"></div>
    </div>
</div>
```

- [ ] **Step 3: Commit**

```bash
cd /Users/gwen/workspace/pelicula
git add nginx/index.html
git commit -m "feat(frontend): replace subtitle/dualsub dialogs with new designs"
```

---

## Task 8: Frontend — catalog.js rewrites

**Files:**
- Modify: `nginx/catalog.js`

- [ ] **Step 1: Update context menu dispatch**

Find the block that handles `dualsub` in the context menu click handler (around line 326). Replace the entire condition so it handles both dialogs:

```js
if (def.name === 'dualsub') {
    openDualsubDialog(item, level);
    return;
}
if (def.name === 'subtitle_search') {
    openSubSearchDialog(item, level);
    return;
}
isFanout ? runFanout(item, level, def) : runAction(def, item, level);
```

- [ ] **Step 2: Update `buildParams`**

Remove the `dualsub` case from `buildParams` (the dialog now handles its own payload). The function should only handle `validate` and `transcode`:

```js
function buildParams(def, item, level) {
    if (def.name === 'validate' || def.name === 'transcode') {
        return { path: level === 'movie' ? (item.movieFile ? item.movieFile.path : '') : (item.path || '') };
    }
    return {};
}
```

- [ ] **Step 3: Replace subtitle dialog JS**

Find the existing subtitle request dialog functions (`openSubReqDialog`, `subReqClose`, `subReqSubmit`, and any related helpers around line 583+) and replace them with:

```js
// ── Subtitle search dialog ────────────────────────────────────────────────────

function openSubSearchDialog(item, level) {
    store.set('catalog.subsearch.item', item);
    store.set('catalog.subsearch.level', level);
    document.getElementById('sub-search-sub').textContent = item.title || item.seriesTitle || '';
    document.getElementById('sub-search-hi').checked = false;
    document.getElementById('sub-search-forced').checked = false;
    document.getElementById('sub-search-status').textContent = '';

    const langs = (item.subLangs || ['en']).slice();
    const allLangs = Array.from(new Set([...langs, 'en', 'es', 'fr', 'de']));
    const container = document.getElementById('sub-search-langs');
    container.replaceChildren();
    allLangs.forEach(lang => {
        const chip = document.createElement('span');
        chip.textContent = lang;
        chip.className = 'sub-req-chip';
        chip.dataset.lang = lang;
        chip.dataset.active = langs.includes(lang) ? '1' : '0';
        chip.style.cssText = 'padding:.2rem .6rem;border-radius:4px;font-size:.8rem;cursor:pointer;margin:.15rem';
        const updateChip = () => {
            const on = chip.dataset.active === '1';
            chip.style.background = on ? 'var(--accent)' : 'var(--border)';
            chip.style.color = on ? '#000' : 'var(--muted)';
        };
        updateChip();
        chip.addEventListener('click', () => {
            chip.dataset.active = chip.dataset.active === '1' ? '0' : '1';
            updateChip();
        });
        container.appendChild(chip);
    });

    showModal(document.getElementById('sub-search-dialog'), document.getElementById('sub-search-backdrop'));
}

window.subSearchClose = function () {
    hideModal(document.getElementById('sub-search-dialog'), document.getElementById('sub-search-backdrop'));
};

window.subSearchSubmit = async function () {
    const item = store.get('catalog.subsearch.item');
    const level = store.get('catalog.subsearch.level');
    const statusEl = document.getElementById('sub-search-status');
    const langs = Array.from(document.querySelectorAll('#sub-search-langs [data-active="1"]'))
        .map(c => c.dataset.lang);
    if (!langs.length) { statusEl.textContent = 'Select at least one language.'; return; }
    statusEl.textContent = 'Searching\u2026';
    try {
        const res = await procFetch('/api/procula/actions?wait=10', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                action: 'subtitle_search',
                target: { arr_type: level === 'movie' ? 'radarr' : 'sonarr', arr_id: item.id, episode_id: item.episodeId || 0 },
                params: {
                    languages: langs,
                    hi: document.getElementById('sub-search-hi').checked,
                    forced: document.getElementById('sub-search-forced').checked,
                },
            }),
        });
        const data = await res.json();
        if (!res.ok) { statusEl.textContent = data.error || 'Error'; return; }
        statusEl.textContent = 'Search triggered.';
        setTimeout(window.subSearchClose, 1500);
    } catch (e) {
        statusEl.textContent = 'Request failed.';
    }
};
```

- [ ] **Step 4: Replace dualsub dialog JS**

Find and replace the existing `openDualsubDialog`, `dualsubClose`, and `dualsubSubmit` functions with:

```js
// ── Dual subtitle dialog ──────────────────────────────────────────────────────

let _dualsubTracks = [];
let _dualsubProfiles = [];
let _dualsubManageOpen = false;
let _dualsubCurrentLayout = 'stacked_bottom';

async function openDualsubDialog(item, level) {
    store.set('catalog.dualsub.item', item);
    store.set('catalog.dualsub.level', level);
    const path = level === 'movie' ? (item.movieFile ? item.movieFile.path : '') : (item.path || '');
    store.set('catalog.dualsub.path', path);

    document.getElementById('dualsub-sub').textContent = item.title || item.seriesTitle || '';
    document.getElementById('dualsub-status').textContent = '';
    document.getElementById('dualsub-profile-editor').style.display = 'none';
    _dualsubManageOpen = false;
    document.getElementById('dualsub-manage-btn').textContent = 'Manage \u25be';

    const [profRes, trackRes] = await Promise.all([
        procFetch('/api/procula/dualsub-profiles'),
        path ? procFetch('/api/procula/subtitle-tracks?path=' + encodeURIComponent(path)) : Promise.resolve(null),
    ]);
    _dualsubProfiles = profRes.ok ? await profRes.json() : [];
    _dualsubTracks = (trackRes && trackRes.ok) ? (await trackRes.json()).tracks || [] : [];

    dualsubRenderProfiles();
    dualsubRenderOnDisk();
    dualsubRenderPairs();

    showModal(document.getElementById('dualsub-dialog'), document.getElementById('dualsub-backdrop'));
}

function dualsubRenderProfiles() {
    const sel = document.getElementById('dualsub-profile-select');
    sel.replaceChildren();
    _dualsubProfiles.forEach(p => {
        const opt = document.createElement('option');
        opt.value = p.name;
        opt.textContent = p.name + (p.layout ? ' (' + p.layout.replace('_', ' ') + ', ' + p.font_size + 'pt)' : '');
        sel.appendChild(opt);
    });
}

function dualsubRenderOnDisk() {
    const container = document.getElementById('dualsub-ondisk');
    container.replaceChildren();
    const msg = document.createElement('div');
    msg.style.cssText = 'color:var(--muted);font-size:.78rem';
    msg.textContent = '\u2014';
    container.appendChild(msg);
}

function dualsubRenderPairs() {
    const container = document.getElementById('dualsub-pairs');
    container.replaceChildren();
    if (_dualsubTracks.length === 0) {
        const msg = document.createElement('div');
        msg.style.cssText = 'color:var(--muted);font-size:.8rem;margin-bottom:.75rem';
        msg.textContent = 'No subtitle sidecar files found alongside this media file.';
        container.appendChild(msg);
        return;
    }
    dualsubAddPair();
}

function dualsubMakePairCard(topFile, bottomFile) {
    const card = document.createElement('div');
    card.className = 'dualsub-pair-card';
    card.style.cssText = 'background:var(--bg);border-radius:6px;padding:.55rem .65rem;margin-bottom:.4rem;display:flex;align-items:center;gap:.4rem';

    function makeSelect(selectedFile) {
        const sel = document.createElement('select');
        sel.style.cssText = 'flex:1;background:var(--surface);border:1px solid var(--border);border-radius:4px;padding:.25rem .4rem;font-size:.78rem;color:var(--text)';
        _dualsubTracks.forEach(tr => {
            const opt = document.createElement('option');
            opt.value = tr.file;
            const varLabel = tr.variant === 'hi' ? ' \u2014 hearing impaired' : tr.variant === 'forced' ? ' \u2014 forced' : '';
            opt.textContent = tr.lang.toUpperCase() + varLabel;
            if (tr.file === selectedFile) opt.selected = true;
            sel.appendChild(opt);
        });
        return sel;
    }

    function makeRow(labelText, selectedFile) {
        const row = document.createElement('div');
        row.style.cssText = 'display:flex;align-items:center;gap:.4rem';
        const lbl = document.createElement('span');
        lbl.style.cssText = 'font-size:.65rem;color:var(--muted);width:2.6rem;flex-shrink:0';
        lbl.textContent = labelText;
        const sel = makeSelect(selectedFile);
        row.appendChild(lbl);
        row.appendChild(sel);
        return { row, sel };
    }

    const rows = document.createElement('div');
    rows.style.cssText = 'display:flex;flex-direction:column;gap:.25rem;flex:1';
    const { row: topRow, sel: topSel } = makeRow('Top', topFile);
    const { row: botRow, sel: botSel } = makeRow('Bottom', bottomFile);
    rows.appendChild(topRow);
    rows.appendChild(botRow);
    card.appendChild(rows);

    const btns = document.createElement('div');
    btns.style.cssText = 'display:flex;flex-direction:column;gap:.2rem;align-items:center';

    const swapBtn = document.createElement('button');
    swapBtn.title = 'Swap top/bottom';
    swapBtn.textContent = '\u21c5';
    swapBtn.style.cssText = 'width:1.6rem;height:1.6rem;border-radius:4px;border:1px solid var(--border);background:transparent;color:var(--muted);font-size:.8rem;cursor:pointer';
    swapBtn.addEventListener('click', () => { const t = topSel.value; topSel.value = botSel.value; botSel.value = t; });

    const removeBtn = document.createElement('button');
    removeBtn.title = 'Remove pair';
    removeBtn.textContent = '\u00d7';
    removeBtn.style.cssText = 'width:1.6rem;height:1.6rem;border-radius:4px;border:1px solid var(--border);background:transparent;color:var(--muted);font-size:.75rem;cursor:pointer';
    removeBtn.addEventListener('click', () => card.remove());

    btns.appendChild(swapBtn);
    btns.appendChild(removeBtn);
    card.appendChild(btns);

    card._getTopFile = () => topSel.value;
    card._getBotFile = () => botSel.value;
    return card;
}

window.dualsubAddPair = function () {
    const container = document.getElementById('dualsub-pairs');
    const topFile = _dualsubTracks[0] ? _dualsubTracks[0].file : '';
    const botFile = _dualsubTracks[1] ? _dualsubTracks[1].file : (_dualsubTracks[0] ? _dualsubTracks[0].file : '');
    container.appendChild(dualsubMakePairCard(topFile, botFile));
};

window.dualsubClose = function () {
    hideModal(document.getElementById('dualsub-dialog'), document.getElementById('dualsub-backdrop'));
};

window.dualsubToggleManage = function () {
    _dualsubManageOpen = !_dualsubManageOpen;
    document.getElementById('dualsub-profile-editor').style.display = _dualsubManageOpen ? 'block' : 'none';
    document.getElementById('dualsub-manage-btn').textContent = _dualsubManageOpen ? 'Manage \u25b4' : 'Manage \u25be';
    if (_dualsubManageOpen) dualsubLoadProfileEditor();
};

function dualsubLoadProfileEditor() {
    const sel = document.getElementById('dualsub-profile-select');
    const prof = _dualsubProfiles.find(p => p.name === sel.value) || _dualsubProfiles[0];
    if (!prof) return;
    document.getElementById('dsp-name').value = prof.name;
    document.getElementById('dsp-font').value = prof.font_name || 'Arial';
    document.getElementById('dsp-size').value = prof.font_size || 52;
    document.getElementById('dsp-outline').value = prof.outline || 2;
    document.getElementById('dsp-margin').value = prof.margin_v || 40;
    document.getElementById('dsp-gap').value = prof.gap || 10;
    _dualsubCurrentLayout = prof.layout || 'stacked_bottom';
    dualsubHighlightLayout(_dualsubCurrentLayout);
    const isBuiltin = !!prof.builtin;
    document.getElementById('dsp-update-btn').disabled = isBuiltin;
    document.getElementById('dsp-delete-btn').disabled = isBuiltin;
}

window.dualsubProfileChanged = function () {
    if (_dualsubManageOpen) dualsubLoadProfileEditor();
};

window.dualsubSetLayout = function (btn) {
    _dualsubCurrentLayout = btn.dataset.layout;
    dualsubHighlightLayout(_dualsubCurrentLayout);
};

function dualsubHighlightLayout(layout) {
    document.querySelectorAll('#dsp-layout-btns button').forEach(b => {
        const active = b.dataset.layout === layout;
        b.style.borderColor = active ? 'var(--accent)' : 'var(--border)';
        b.style.background = active ? 'rgba(255,200,0,.12)' : 'transparent';
        b.style.color = active ? 'var(--accent)' : 'var(--muted)';
    });
}

function dualsubReadEditorFields() {
    return {
        name: document.getElementById('dsp-name').value.trim(),
        font_name: document.getElementById('dsp-font').value.trim() || 'Arial',
        font_size: parseInt(document.getElementById('dsp-size').value) || 52,
        outline: parseFloat(document.getElementById('dsp-outline').value) || 2,
        margin_v: parseInt(document.getElementById('dsp-margin').value) || 40,
        gap: parseInt(document.getElementById('dsp-gap').value) || 10,
        layout: _dualsubCurrentLayout,
    };
}

window.dualsubSaveAsNew = async function () {
    const fields = dualsubReadEditorFields();
    if (!fields.name) { toast('Profile name required'); return; }
    const res = await procFetch('/api/procula/dualsub-profiles', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(fields),
    });
    if (!res.ok) { const d = await res.json(); toast(d.error || 'Save failed'); return; }
    const saved = await res.json();
    _dualsubProfiles.push(saved);
    dualsubRenderProfiles();
    document.getElementById('dualsub-profile-select').value = saved.name;
    toast('Profile saved');
};

window.dualsubUpdateProfile = async function () {
    const fields = dualsubReadEditorFields();
    if (!fields.name) return;
    const res = await procFetch('/api/procula/dualsub-profiles/' + encodeURIComponent(fields.name), {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(fields),
    });
    if (!res.ok) { const d = await res.json(); toast(d.error || 'Update failed'); return; }
    const idx = _dualsubProfiles.findIndex(p => p.name === fields.name);
    if (idx !== -1) _dualsubProfiles[idx] = fields;
    dualsubRenderProfiles();
    document.getElementById('dualsub-profile-select').value = fields.name;
    toast('Profile updated');
};

window.dualsubDeleteProfile = async function () {
    const name = document.getElementById('dsp-name').value.trim();
    if (!name) return;
    const res = await procFetch('/api/procula/dualsub-profiles/' + encodeURIComponent(name), { method: 'DELETE' });
    if (!res.ok && res.status !== 204) { const d = await res.json(); toast(d.error || 'Delete failed'); return; }
    _dualsubProfiles = _dualsubProfiles.filter(p => p.name !== name);
    dualsubRenderProfiles();
    document.getElementById('dualsub-profile-editor').style.display = 'none';
    _dualsubManageOpen = false;
    document.getElementById('dualsub-manage-btn').textContent = 'Manage \u25be';
    toast('Profile deleted');
};

window.dualsubSubmit = async function () {
    const path = store.get('catalog.dualsub.path');
    const statusEl = document.getElementById('dualsub-status');
    if (!path) { statusEl.textContent = 'No file path available.'; return; }

    const profileName = document.getElementById('dualsub-profile-select').value;
    const pairCards = document.querySelectorAll('.dualsub-pair-card');
    const pairs = Array.from(pairCards).map(c => ({
        top_file: c._getTopFile(),
        bottom_file: c._getBotFile(),
    })).filter(p => p.top_file && p.bottom_file);

    if (!pairs.length) { statusEl.textContent = 'Add at least one track pair.'; return; }

    statusEl.textContent = 'Generating\u2026';
    try {
        const res = await procFetch('/api/procula/actions?wait=10', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                action: 'dualsub',
                target: { path },
                params: { profile: profileName, pairs },
            }),
        });
        const data = await res.json();
        if (!res.ok) { statusEl.textContent = data.error || 'Error'; return; }
        const outputs = (data.result && data.result.outputs) || [];
        statusEl.textContent = outputs.length
            ? 'Generated: ' + outputs.map(o => o.split('/').pop()).join(', ')
            : 'No output produced.';
        if (outputs.length) setTimeout(window.dualsubClose, 2000);
    } catch (e) {
        statusEl.textContent = 'Request failed.';
    }
};
```

- [ ] **Step 5: Check JS syntax**

```bash
node --check /Users/gwen/workspace/pelicula/nginx/catalog.js 2>&1 || echo "node not available"
```
Expected: no output (clean), or "node not available".

- [ ] **Step 6: Commit**

```bash
cd /Users/gwen/workspace/pelicula
git add nginx/catalog.js
git commit -m "feat(frontend): subtitle search + dualsub profile+track dialogs"
```

---

## Task 9: Rebuild and smoke-test

- [ ] **Step 1: Rebuild procula**

```bash
cd /Users/gwen/workspace/pelicula && pelicula rebuild
```
Expected: build succeeds, container restarts.

- [ ] **Step 2: Check logs**

```bash
pelicula logs procula | tail -20
```
Expected: `"listening"` log line, no startup errors.

- [ ] **Step 3: Verify endpoints**

```bash
curl -s http://localhost:7354/api/procula/dualsub-profiles | python3 -m json.tool | grep '"name"'
```
Expected: three entries: `"Default"`, `"Large split"`, `"Stacked top"`.

```bash
curl -s "http://localhost:7354/api/procula/subtitle-tracks?path=/movies/x" -o /dev/null -w "%{http_code}"
```
Expected: `400` (path not in library, as `/movies/x` doesn't exist but the path check runs first — adjust to a real path for a 200).

- [ ] **Step 4: Verify action registry**

```bash
curl -s http://localhost:7354/api/procula/actions/registry | python3 -m json.tool | grep '"name"'
```
Expected: `"subtitle_search"` and `"dualsub"` present; `"subtitle_refresh"` and `"subtitle_request"` absent.

- [ ] **Step 5: Open catalog in browser and verify dialogs**

Open `http://localhost:7354`. Right-click a movie. Confirm:
- "Search subtitles..." replaces the old two subtitle items
- "Dual subtitles..." replaces "Regenerate dual subtitles"
- Clicking "Search subtitles..." shows the new dialog with language chips and HI/forced descriptions
- Clicking "Dual subtitles..." shows profile dropdown, track pairs, and Manage toggle

---

## Notes

- `dualsubRenderOnDisk` currently shows "—" as a placeholder. A follow-up task can add a dedicated sidecar-listing endpoint or reuse `subtitleTracksForPath` output filtered for dualsub sidecars.
- `langTagFromBase` (Task 5) extracts the language from the second-to-last dot-segment of a filename base. For standard Bazarr/manual naming (`Movie.en.hi`) this is correct. Unusual names may mismatch.
- `parseASSCues` and `parseASSTime` are placed in `actions.go` alongside their only caller. If they grow more callers, move them to `dualsub.go`.
