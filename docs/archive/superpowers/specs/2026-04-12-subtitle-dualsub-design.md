# Subtitle consolidation + dual subtitle profile system

**Date:** 2026-04-12
**Status:** Approved for implementation

## Overview

Two related changes:

1. **Subtitle search consolidation** — merge the "Refresh subtitles" and "Request subtitles" context menu items into a single "Search subtitles…" dialog.
2. **Dualsub profile system** — replace the bare "Regenerate dual subtitles" action with a full dialog that supports named profiles (font, layout, margin), per-item track selection, and multiple output pairs per run.

---

## 1. Subtitle search consolidation

### Context menu

Remove: "Refresh subtitles", "Request subtitles"
Add: "Search subtitles…" — always opens the dialog, pre-populated with the item's `sub_langs`.

### Dialog

- **Header**: "Search subtitles" + item title
- **Languages**: pill-toggle chips for each available language. Pre-selected from the item's `sub_langs` setting. User can toggle any chip on or off.
- **Options** (with inline descriptions):
  - *Hearing impaired* — "Includes sound-effect descriptions and speaker labels alongside dialogue"
  - *Forced only* — "Only foreign-language lines within otherwise native-audio content"
- **Actions**: Cancel / Search

Submitting fires the same backend job as the current `subtitle_request` action, passing the selected languages and flags. The separate instant-fire `subtitle_refresh` behavior is dropped; the dialog replaces both paths.

---

## 2. Dualsub profile system

### Context menu

Rename: "Regenerate dual subtitles" → "Dual subtitles…" — always opens the dialog.

### Data model

Profiles are stored in the procula settings DB.

```go
type DualSubProfile struct {
    Name     string  `json:"name"`
    Builtin  bool    `json:"builtin,omitempty"`
    Layout   string  `json:"layout"`      // "stacked_bottom" | "stacked_top" | "split"
    FontSize int     `json:"font_size"`   // points, in ASS PlayRes coordinate space
    FontName string  `json:"font_name"`
    Outline  float64 `json:"outline"`
    MarginV  int     `json:"margin_v"`    // pixels from edge
    Gap      int     `json:"gap"`         // stacked layouts only: space between top and bottom line
}
```

Track selection is per-generation, not per-profile:

```go
type DualSubJob struct {
    Profile string      `json:"profile"`
    Pairs   []TrackPair `json:"pairs"`
}

type TrackPair struct {
    TopFile    string `json:"top_file"`    // subtitle filename, e.g. "Movie.en.srt"
    BottomFile string `json:"bottom_file"` // e.g. "Movie.es.forced.srt"
}
```

### Built-in profiles

| Name | Layout | Font size | Gap |
|------|--------|-----------|-----|
| Default | stacked_bottom | 52pt | 10px |
| Large split | split | 64pt | — |
| Stacked top | stacked_top | 52pt | 10px |

All built-ins: Arial, outline 2, MarginV 40. Built-ins cannot be deleted or renamed; "Update" on a built-in is disabled (only "Save as new" is available).

### New API endpoints

**Subtitle track discovery**
```
GET /api/procula/subtitle-tracks?path=<media_path>
→ {
    "tracks": [
      { "file": "Movie.en.srt",        "lang": "en", "variant": "regular" },
      { "file": "Movie.en.hi.srt",     "lang": "en", "variant": "hi" },
      { "file": "Movie.es.srt",        "lang": "es", "variant": "regular" },
      { "file": "Movie.es.forced.srt", "lang": "es", "variant": "forced" }
    ]
  }
```

Variant is detected from filename conventions: `.hi.`, `.sdh.` → `hi`; `.forced.` → `forced`; otherwise `regular`.

**Profile CRUD**
```
GET    /api/procula/profiles          → list all profiles (built-ins + user)
POST   /api/procula/profiles          → create user profile
PUT    /api/procula/profiles/:name    → update user profile
DELETE /api/procula/profiles/:name    → delete user profile (rejects built-ins)
```

**Dualsub action**

The existing `dualsub` registered action now accepts a `DualSubJob` payload. It deletes any existing sidecars for the requested pairs, then generates new ones. Responds synchronously (existing `Sync: true` behavior).

The `dualsub` action response includes an `existing` field listing on-disk sidecar filenames for that item, so the dialog can populate "On disk" on open.

### ASS generation

PlayResX: 1920, PlayResY: 1080 (already set by prior fix).

Two named styles per output file: `Top` and `Bottom`. Both inherit font/size/outline from the profile. Positioning by layout:

| Layout | Top style | Bottom style |
|--------|-----------|--------------|
| `stacked_bottom` | Alignment=2, MarginV=`margin_v + font_size + gap` | Alignment=2, MarginV=`margin_v` |
| `stacked_top` | Alignment=8, MarginV=`margin_v` | Alignment=8, MarginV=`margin_v + font_size + gap` |
| `split` | Alignment=8, MarginV=`margin_v` | Alignment=2, MarginV=`margin_v` |

`TopFile` in the pair always renders with the `Top` style; `BottomFile` with `Bottom`. Swapping which language appears on top is done by swapping `TopFile`/`BottomFile` in the pair — the ⇅ button in the dialog handles this.

Cue merging is timestamp-driven. For each subtitle event from either track, emit a dialogue line with the matching style. When one track has no cue at a given time that slot is absent; the other renders alone in its position. HI and forced tracks need no special handling at the ASS layer.

Output filename convention: `<basename>.<top_lang>-<bottom_lang>.ass`
Example: `Hercules.1997.BluRay.en-es.ass`

### Dialog — collapsed (profile picker)

1. **Header**: "Dual subtitles" + item title
2. **On disk**: list of existing `.ass` sidecars for this item, each with a ✕ delete button
3. **Profile**: dropdown of all profiles + "Manage ▾" toggle
4. **Track pairs**: one card per pair
   - Top dropdown: all available tracks, labelled `Language — variant` (e.g. "English — hearing impaired")
   - Bottom dropdown: same set
   - ⇅ button: swaps top/bottom selections in that card
   - ✕ button: removes the pair card
5. **+ Add pair**: appends a new blank pair card
6. **Actions**: Cancel / Generate

On open, the dialog pre-populates one pair card: top = first non-HI track for the item's primary language, bottom = first track for the secondary language (if any on disk).

### Dialog — expanded (inline profile editor)

Toggled by "Manage ▾". Inserts an inline editor panel between the profile dropdown and the track pairs section.

Fields (2-column grid): Name, Font, Font size (pt), Outline, Margin (px), Gap — stacked (px)

Layout toggle: "Stacked ↓" | "Stacked ↑" | "Split" (segmented button, one active at a time)

Actions row:
- "+ Save as new" — creates a new user profile from current field values
- "Update" — saves changes to the selected profile (disabled for built-ins)
- "Delete" (right-aligned, red) — deletes the selected user profile (disabled for built-ins)

---

## Out of scope

- Side-by-side layout: technically possible in ASS but breaks on longer subtitle lines; excluded.
- Color customization per track: deferred.
- Font file upload: deferred.
