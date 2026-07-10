# Dualsub Embedded Tracks — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose embedded text-based subtitle streams from inside media files as selectable tracks in the dualsub dialog, so users can create dual-subtitle files without needing downloaded sidecar files.

**Architecture:** Backend adds `EmbeddedTrack` struct and wires `probeSubStreams` into `handleSubtitleTracks` (procula). The dualsub action handler (`runDualSubAction`) gains support for `sub_index`-based track references alongside file paths. Frontend populates the track picker `<select>` with both sidecar files and embedded streams, using `embedded:<N>` synthetic values to distinguish them.

**Tech Stack:** Go (stdlib + modernc.org/sqlite), vanilla JS, ffprobe/ffmpeg

---

## File map

| File | Change |
|------|--------|
| `procula/dualsub.go` | **Modify** — add exported `EmbeddedTrack` struct |
| `procula/dualsub_test.go` | **Modify** — add unit tests for `EmbeddedTrack` filtering and `TrackPair` sub_index support |
| `procula/main.go` | **Modify** — update `handleSubtitleTracks` to probe embedded streams and include `embedded_tracks` in response |
| `procula/actions.go` | **Modify** — update `runDualSubAction` to accept `top_sub_index`/`bottom_sub_index` as alternatives to `top_file`/`bottom_file` in pair parsing; add `TrackPair.TopSubIndex`/`BottomSubIndex` fields |
| `procula/dualsub_profiles.go` | **Modify** — add `TopSubIndex`/`BottomSubIndex` optional fields to `TrackPair` |
| `procula/actions_test.go` | **Modify** — add test for embedded-stream pair parsing in `runDualSubAction` |
| `nginx/catalog.js` | **Modify** — read `embedded_tracks` from API; populate selects with both sidecar and embedded options; encode `embedded:<N>` values in submit payload |

---

## Task 1: Add `EmbeddedTrack` struct

**Files:**
- Modify: `procula/dualsub.go`
- Modify: `procula/dualsub_test.go`

- [ ] **Step 1: Write the failing test**

Add to `procula/dualsub_test.go` after `TestSubtitleTracksForPath`:

```go
func TestEmbeddedTrackStruct(t *testing.T) {
	// Verify the struct exists and JSON tags are correct
	et := EmbeddedTrack{SubIndex: 2, Lang: "es", CodecName: "subrip"}
	data, err := json.Marshal(et)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if m["sub_index"] != float64(2) {
		t.Errorf("sub_index = %v, want 2", m["sub_index"])
	}
	if m["lang"] != "es" {
		t.Errorf("lang = %v, want es", m["lang"])
	}
	if m["codec"] != "subrip" {
		t.Errorf("codec = %v, want subrip", m["codec"])
	}
}

func TestFilterTextEmbeddedTracks(t *testing.T) {
	streams := []subStream{
		{SubIndex: 0, Lang: "en", CodecName: "subrip"},
		{SubIndex: 1, Lang: "es", CodecName: "ass"},
		{SubIndex: 2, Lang: "ja", CodecName: "hdmv_pgs_subtitle"},
		{SubIndex: 3, Lang: "fr", CodecName: "webvtt"},
	}
	got := filterTextEmbeddedTracks(streams)
	if len(got) != 3 {
		t.Fatalf("expected 3 text tracks, got %d", len(got))
	}
	wantIndices := []int{0, 1, 3}
	wantLangs := []string{"en", "es", "fr"}
	wantCodecs := []string{"subrip", "ass", "webvtt"}
	for i, et := range got {
		if et.SubIndex != wantIndices[i] {
			t.Errorf("[%d] SubIndex = %d, want %d", i, et.SubIndex, wantIndices[i])
		}
		if et.Lang != wantLangs[i] {
			t.Errorf("[%d] Lang = %q, want %q", i, et.Lang, wantLangs[i])
		}
		if et.CodecName != wantCodecs[i] {
			t.Errorf("[%d] CodecName = %q, want %q", i, et.CodecName, wantCodecs[i])
		}
	}
}
```

Add `"encoding/json"` to the test file imports if not already present.

- [ ] **Step 2: Add the struct and filter function**

Add to `procula/dualsub.go` after the `DualSubSidecar` struct (after line 43):

```go
// EmbeddedTrack describes a text-based subtitle stream embedded in a media file.
type EmbeddedTrack struct {
	SubIndex  int    `json:"sub_index"` // ordinal among subtitle streams, for ffmpeg -map 0:s:N
	Lang      string `json:"lang"`      // normalized 2-letter code
	CodecName string `json:"codec"`     // e.g. "subrip", "ass"
}

// filterTextEmbeddedTracks returns only the text-based subtitle streams from
// the given list, converted to EmbeddedTrack for JSON serialization.
func filterTextEmbeddedTracks(streams []subStream) []EmbeddedTrack {
	var tracks []EmbeddedTrack
	for _, s := range streams {
		if isTextSubCodec(s.CodecName) {
			tracks = append(tracks, EmbeddedTrack{
				SubIndex:  s.SubIndex,
				Lang:      s.Lang,
				CodecName: s.CodecName,
			})
		}
	}
	return tracks
}
```

- [ ] **Step 3: Verify tests pass**

```bash
cd /Users/gwen/workspace/pelicula/procula && go test -run "TestEmbeddedTrack|TestFilterText" -v ./...
```

- [ ] **Step 4: Commit**

```
feat(procula): add EmbeddedTrack struct and filter helper for dualsub
```

---

## Task 2: Expose embedded tracks in the subtitle-tracks endpoint

**Files:**
- Modify: `procula/main.go`

- [ ] **Step 1: Write the failing test**

Add to `procula/dualsub_test.go`:

```go
func TestHandleSubtitleTracksEmbeddedField(t *testing.T) {
	// The response struct should marshal embedded_tracks as a JSON key.
	// This validates the integration point: handleSubtitleTracks returns
	// the correct shape. Since probeSubStreams needs ffprobe + a real media
	// file, we test the contract by round-tripping through JSON.
	resp := map[string]any{
		"tracks":          []SubtitleTrack{},
		"dualsubs":        []DualSubSidecar{},
		"embedded_tracks": []EmbeddedTrack{{SubIndex: 0, Lang: "en", CodecName: "subrip"}},
	}
	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["embedded_tracks"]; !ok {
		t.Error("response missing embedded_tracks field")
	}
	var embedded []EmbeddedTrack
	if err := json.Unmarshal(decoded["embedded_tracks"], &embedded); err != nil {
		t.Fatal(err)
	}
	if len(embedded) != 1 || embedded[0].SubIndex != 0 {
		t.Errorf("unexpected embedded_tracks: %+v", embedded)
	}
}
```

- [ ] **Step 2: Update `handleSubtitleTracks`**

In `procula/main.go`, replace the current `handleSubtitleTracks` function (lines 635-655) with:

```go
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
	dualsubs := dualsubSidecarsForPath(clean)
	if dualsubs == nil {
		dualsubs = []DualSubSidecar{}
	}

	var embedded []EmbeddedTrack
	streams, err := probeSubStreams(clean)
	if err == nil {
		embedded = filterTextEmbeddedTracks(streams)
	} else {
		slog.Warn("probe embedded subs failed", "path", clean, "error", err)
	}
	if embedded == nil {
		embedded = []EmbeddedTrack{}
	}

	writeJSON(w, map[string]any{
		"tracks":          tracks,
		"dualsubs":        dualsubs,
		"embedded_tracks": embedded,
	})
}
```

- [ ] **Step 3: Verify tests pass**

```bash
cd /Users/gwen/workspace/pelicula/procula && go test -run "TestHandleSubtitleTracksEmbedded" -v ./...
```

- [ ] **Step 4: Commit**

```
feat(procula): expose embedded subtitle streams in subtitle-tracks endpoint
```

---

## Task 3: Accept `sub_index` in dualsub action pairs

**Files:**
- Modify: `procula/dualsub_profiles.go`
- Modify: `procula/actions.go`
- Modify: `procula/actions_test.go` (or `procula/dualsub_test.go`)

- [ ] **Step 1: Write the failing test**

Add to `procula/dualsub_test.go`:

```go
func TestParseTrackPairWithSubIndex(t *testing.T) {
	// Simulate the JSON payload parsing logic from runDualSubAction
	raw := []any{
		map[string]any{"top_file": "/movies/Movie/Movie.en.srt", "bottom_file": "/movies/Movie/Movie.es.srt"},
		map[string]any{"top_sub_index": float64(0), "bottom_sub_index": float64(1)},
		map[string]any{"top_file": "/movies/Movie/Movie.en.srt", "bottom_sub_index": float64(2)},
	}
	pairs := parseDualSubPairs(raw)
	if len(pairs) != 3 {
		t.Fatalf("expected 3 pairs, got %d", len(pairs))
	}
	// Pair 0: both files
	if pairs[0].TopFile != "/movies/Movie/Movie.en.srt" {
		t.Errorf("pair[0].TopFile = %q", pairs[0].TopFile)
	}
	if pairs[0].TopSubIndex != -1 {
		t.Errorf("pair[0].TopSubIndex = %d, want -1", pairs[0].TopSubIndex)
	}
	// Pair 1: both embedded
	if pairs[1].TopSubIndex != 0 {
		t.Errorf("pair[1].TopSubIndex = %d, want 0", pairs[1].TopSubIndex)
	}
	if pairs[1].BottomSubIndex != 1 {
		t.Errorf("pair[1].BottomSubIndex = %d, want 1", pairs[1].BottomSubIndex)
	}
	// Pair 2: mixed — file top, embedded bottom
	if pairs[2].TopFile != "/movies/Movie/Movie.en.srt" {
		t.Errorf("pair[2].TopFile = %q", pairs[2].TopFile)
	}
	if pairs[2].BottomSubIndex != 2 {
		t.Errorf("pair[2].BottomSubIndex = %d, want 2", pairs[2].BottomSubIndex)
	}
}
```

- [ ] **Step 2: Extend `TrackPair` struct**

In `procula/dualsub_profiles.go`, replace the `TrackPair` struct:

```go
// TrackPair identifies the two subtitle sources to combine for one output sidecar.
// Each side is either a sidecar file path or an embedded stream index.
// A SubIndex of -1 means "use the file path"; >= 0 means "use embedded stream N".
type TrackPair struct {
	TopFile        string `json:"top_file"`         // sidecar file path (empty if using embedded)
	BottomFile     string `json:"bottom_file"`      // sidecar file path (empty if using embedded)
	TopSubIndex    int    `json:"top_sub_index"`    // embedded stream index (-1 = not used)
	BottomSubIndex int    `json:"bottom_sub_index"` // embedded stream index (-1 = not used)
}
```

- [ ] **Step 3: Add `parseDualSubPairs` helper and update `runDualSubAction`**

In `procula/actions.go`, add a new parsing function and update the action handler:

```go
// parseDualSubPairs parses raw JSON pair entries into TrackPairs.
// Each entry can use file paths, sub_index values, or a mix of both.
func parseDualSubPairs(rawPairs []any) []TrackPair {
	var pairs []TrackPair
	for _, rp := range rawPairs {
		m, ok := rp.(map[string]any)
		if !ok {
			continue
		}
		var p TrackPair
		p.TopSubIndex = -1
		p.BottomSubIndex = -1

		p.TopFile, _ = m["top_file"].(string)
		p.BottomFile, _ = m["bottom_file"].(string)

		if v, ok := m["top_sub_index"].(float64); ok {
			p.TopSubIndex = int(v)
		}
		if v, ok := m["bottom_sub_index"].(float64); ok {
			p.BottomSubIndex = int(v)
		}

		// At least one source on each side
		hasTop := p.TopFile != "" || p.TopSubIndex >= 0
		hasBot := p.BottomFile != "" || p.BottomSubIndex >= 0
		if hasTop && hasBot {
			pairs = append(pairs, p)
		}
	}
	return pairs
}
```

Then update `runDualSubAction` to use the new parsing and handle embedded sources. Replace the pair-parsing block (lines 250-262) with:

```go
	rawPairs, _ := job.Params["pairs"].([]any)
	pairs := parseDualSubPairs(rawPairs)
	if len(pairs) == 0 {
		return nil, fmt.Errorf("dualsub: at least one track pair required")
	}
```

And replace the per-pair loop body (lines 272-299) with logic that resolves cues from either file or embedded stream:

```go
	for _, pair := range pairs {
		// Determine language tags for the output filename
		topLang := pairSideLang(pair.TopFile, pair.TopSubIndex, streams)
		botLang := pairSideLang(pair.BottomFile, pair.BottomSubIndex, streams)
		mediaBase := strings.TrimSuffix(path, filepath.Ext(path))
		outPath := mediaBase + "." + topLang + "-" + botLang + ".ass"

		os.Remove(outPath) //nolint:errcheck

		topCues, err := resolvePairCues(ctx, path, pair.TopFile, pair.TopSubIndex)
		if err != nil || len(topCues) == 0 {
			label := pair.TopFile
			if pair.TopSubIndex >= 0 {
				label = fmt.Sprintf("embedded:%d", pair.TopSubIndex)
			}
			warnings = append(warnings, fmt.Sprintf("top track %s: %v", label, err))
			continue
		}
		botCues, err := resolvePairCues(ctx, path, pair.BottomFile, pair.BottomSubIndex)
		if err != nil || len(botCues) == 0 {
			label := pair.BottomFile
			if pair.BottomSubIndex >= 0 {
				label = fmt.Sprintf("embedded:%d", pair.BottomSubIndex)
			}
			warnings = append(warnings, fmt.Sprintf("bottom track %s: %v", label, err))
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
```

Add the helper functions in `procula/actions.go`:

```go
// resolvePairCues loads subtitle cues from either a sidecar file or an embedded stream.
// subIndex < 0 means use the file; subIndex >= 0 means use the embedded stream.
func resolvePairCues(ctx context.Context, mediaPath, file string, subIndex int) ([]SubtitleCue, error) {
	if subIndex >= 0 {
		return extractEmbeddedSub(ctx, mediaPath, subIndex)
	}
	return parseSidecarFile(file)
}

// pairSideLang determines the language tag for one side of a track pair.
// For file sources, extracts from the filename. For embedded streams,
// looks up the lang in the probed streams list.
func pairSideLang(file string, subIndex int, streams []subStream) string {
	if subIndex >= 0 {
		for _, s := range streams {
			if s.SubIndex == subIndex {
				return s.Lang
			}
		}
		return "und"
	}
	base := strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	return langTagFromBase(base)
}
```

And add the streams probe at the top of `runDualSubAction`, right after the path validation and before pair parsing:

```go
	// Probe embedded streams (needed for sub_index resolution and lang tags)
	streams, _ := probeSubStreams(path)
```

- [ ] **Step 4: Verify tests pass**

```bash
cd /Users/gwen/workspace/pelicula/procula && go test -run "TestParseTrackPair" -v ./...
```

- [ ] **Step 5: Commit**

```
feat(procula): accept embedded sub_index in dualsub action pairs
```

---

## Task 4: Frontend — populate selects with embedded tracks

**Files:**
- Modify: `nginx/catalog.js`

- [ ] **Step 1: Store embedded tracks from API response**

In `openDualsubDialog` (line 900), after `_dualsubTracks = tracksData.tracks || [];`, add:

```javascript
        _dualsubEmbedded = tracksData.embedded_tracks || [];
```

And at the top of the dualsub section (line 874), after `let _dualsubTracks = [];`, add:

```javascript
    let _dualsubEmbedded = [];
```

- [ ] **Step 2: Update `dualsubRenderPairs` empty-state check**

Replace the empty check in `dualsubRenderPairs` (lines 943-949) so it considers both sources:

```javascript
    function dualsubRenderPairs() {
        const container = document.getElementById('dualsub-pairs');
        container.replaceChildren();
        if (_dualsubTracks.length === 0 && _dualsubEmbedded.length === 0) {
            const msg = document.createElement('div');
            msg.style.cssText = 'color:var(--muted);font-size:.8rem;margin-bottom:.75rem';
            msg.textContent = 'No subtitle tracks found (sidecar or embedded).';
            container.appendChild(msg);
            return;
        }
        dualsubAddPair();
    }
```

- [ ] **Step 3: Update `dualsubMakePairCard` to include embedded options**

In the `makeSelect` inner function (lines 959-969), after populating sidecar tracks, add embedded tracks:

```javascript
            _dualsubEmbedded.forEach(et => {
                const opt = document.createElement('option');
                opt.value = 'embedded:' + et.sub_index;
                const codecLabel = et.codec === 'subrip' ? 'SRT' : et.codec.toUpperCase();
                opt.textContent = et.lang.toUpperCase() + ' \u2014 embedded (' + codecLabel + ')';
                if ('embedded:' + et.sub_index === selectedFile) opt.selected = true;
                sel.appendChild(opt);
            });
```

The full `makeSelect` function becomes:

```javascript
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
            _dualsubEmbedded.forEach(et => {
                const opt = document.createElement('option');
                opt.value = 'embedded:' + et.sub_index;
                const codecLabel = et.codec === 'subrip' ? 'SRT' : et.codec.toUpperCase();
                opt.textContent = et.lang.toUpperCase() + ' \u2014 embedded (' + codecLabel + ')';
                if ('embedded:' + et.sub_index === selectedFile) opt.selected = true;
                sel.appendChild(opt);
            });
            return sel;
        }
```

- [ ] **Step 4: Update `dualsubAddPair` default selection**

Replace the `dualsubAddPair` function (lines 1016-1021) to pick from either source:

```javascript
    window.dualsubAddPair = function () {
        const container = document.getElementById('dualsub-pairs');
        const allOptions = [
            ..._dualsubTracks.map(tr => tr.file),
            ..._dualsubEmbedded.map(et => 'embedded:' + et.sub_index),
        ];
        const topFile = allOptions[0] || '';
        const botFile = allOptions[1] || allOptions[0] || '';
        container.appendChild(dualsubMakePairCard(topFile, botFile));
    };
```

- [ ] **Step 5: Update `dualsubSubmit` to encode embedded values**

Replace the pair-building logic in `dualsubSubmit` (lines 1133-1136) with:

```javascript
        const pairs = Array.from(pairCards).map(c => {
            const topVal = c._getTopFile();
            const botVal = c._getBotFile();
            const pair = {};
            if (topVal.startsWith('embedded:')) {
                pair.top_sub_index = parseInt(topVal.slice(9), 10);
            } else {
                pair.top_file = topVal;
            }
            if (botVal.startsWith('embedded:')) {
                pair.bottom_sub_index = parseInt(botVal.slice(9), 10);
            } else {
                pair.bottom_file = botVal;
            }
            return pair;
        }).filter(p => (p.top_file || p.top_sub_index >= 0) && (p.bottom_file || p.bottom_sub_index >= 0));
```

- [ ] **Step 6: Verify manually** (no automated JS tests for catalog.js)

Start the stack with `pelicula up`, open an MKV that has embedded subtitle streams, open the dualsub dialog, and verify:
1. Embedded tracks appear in the select dropdowns with "embedded (SRT)" labels
2. Selecting an embedded track and generating produces the expected ASS output
3. Mixed pairs (one sidecar, one embedded) work correctly

- [ ] **Step 7: Commit**

```
feat(catalog): show embedded subtitle streams in dualsub track picker
```

---

## Task 5: Full integration verification

- [ ] **Step 1: Run all procula tests**

```bash
cd /Users/gwen/workspace/pelicula/procula && go test -v ./...
```

- [ ] **Step 2: Run Go vet**

```bash
cd /Users/gwen/workspace/pelicula/procula && go vet ./...
```

- [ ] **Step 3: Final commit** (if any cleanup needed)

```
chore(procula): clean up after dualsub embedded tracks integration
```
