# Drawer Synopsis & Artwork Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface synopsis and artwork in the catalog detail drawer, sourced from the catalog DB (populated by Jellyfin sync).

**Architecture:** Add `GetCatalogItemByFilePath` to catalog_db.go, then walk up the parent chain (episode → season → series) to find the item that carries synopsis/artwork. Wire it into `handleCatalogDetail` so the response includes `synopsis` and `artwork_url`. Render them in the drawer above the existing technical sections.

**Tech Stack:** Go (stdlib + `modernc.org/sqlite`), vanilla JS, CSS custom properties

---

## File Map

| File | Change |
|---|---|
| `middleware/catalog_db.go` | Add `GetCatalogItemByFilePath` |
| `middleware/catalog_db_test.go` | Test `GetCatalogItemByFilePath` |
| `middleware/catalog.go` | Update `handleCatalogDetail` to include `synopsis` + `artwork_url` |
| `nginx/catalog.js` | Update `renderDetailHtml` to render hero block |
| `nginx/styles.css` | Add `.cat-drawer-hero` styles |

---

### Task 1: Add `GetCatalogItemByFilePath` to catalog DB

**Files:**
- Modify: `middleware/catalog_db.go`
- Test: `middleware/catalog_db_test.go`

The existing lookups are by ID or natural key. Episodes and movies both have a `file_path` column with an index. We also need a helper that walks the parent chain to find the item that carries `synopsis`/`artwork_url` (series for TV content, the movie itself for films).

- [ ] **Step 1: Write the failing tests**

Add to `middleware/catalog_db_test.go`:

```go
func TestGetCatalogItemByFilePath(t *testing.T) {
	db := testCatalogDB(t)

	// Insert a movie with a file path, synopsis, and artwork.
	_, err := UpsertCatalogItem(db, CatalogItem{
		Type:       "movie",
		TmdbID:     101,
		ArrType:    "radarr",
		ArrID:      1,
		Title:      "Test Movie",
		Year:       2020,
		Tier:       "library",
		FilePath:   "/media/movies/test.mkv",
		Synopsis:   "A test film.",
		ArtworkURL: "http://jellyfin/Items/abc/Images/Primary",
	})
	if err != nil {
		t.Fatalf("UpsertCatalogItem: %v", err)
	}

	item, err := GetCatalogItemByFilePath(db, "/media/movies/test.mkv")
	if err != nil {
		t.Fatalf("GetCatalogItemByFilePath: %v", err)
	}
	if item == nil {
		t.Fatal("expected item, got nil")
	}
	if item.Title != "Test Movie" {
		t.Errorf("title: got %q, want %q", item.Title, "Test Movie")
	}
	if item.Synopsis != "A test film." {
		t.Errorf("synopsis: got %q, want %q", item.Synopsis, "A test film.")
	}
	if item.ArtworkURL != "http://jellyfin/Items/abc/Images/Primary" {
		t.Errorf("artwork_url: got %q", item.ArtworkURL)
	}
}

func TestGetCatalogItemByFilePath_NotFound(t *testing.T) {
	db := testCatalogDB(t)
	item, err := GetCatalogItemByFilePath(db, "/media/movies/missing.mkv")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if item != nil {
		t.Errorf("expected nil, got item with title %q", item.Title)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/gwen/workspace/pelicula/middleware && go test ./... -run 'TestGetCatalogItemByFilePath' -v
```

Expected: compile error — `GetCatalogItemByFilePath` undefined.

- [ ] **Step 3: Implement `GetCatalogItemByFilePath`**

Add after `GetCatalogItemByID` in `middleware/catalog_db.go`:

```go
// GetCatalogItemByFilePath fetches a catalog item by its file_path.
// Returns (nil, nil) if no item matches.
func GetCatalogItemByFilePath(db *sql.DB, filePath string) (*CatalogItem, error) {
	row := db.QueryRow(selectCatalogItem+` WHERE file_path=?`, filePath)
	it, err := scanCatalogRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return it, err
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/gwen/workspace/pelicula/middleware && go test ./... -run 'TestGetCatalogItemByFilePath' -v
```

Expected: PASS (both subtests).

- [ ] **Step 5: Commit**

```bash
cd /Users/gwen/workspace/pelicula/middleware && git add catalog_db.go catalog_db_test.go
git commit -m "feat(catalog): add GetCatalogItemByFilePath lookup"
```

---

### Task 2: Wire synopsis + artwork into `handleCatalogDetail`

**Files:**
- Modify: `middleware/catalog.go`

For movies, the item found by file path carries `synopsis` and `artwork_url` directly. For TV episodes the item itself has empty fields — synopsis/artwork live on the series (grandparent). Walk up: episode → season (via `ParentID`) → series (via `ParentID`).

- [ ] **Step 1: Update `handleCatalogDetail` to resolve synopsis and artwork**

In `middleware/catalog.go`, replace the final `httputil.WriteJSON` call and add the lookup above it. The full block from `// handleCatalogDetail` through its closing brace becomes:

```go
// handleCatalogDetail returns {path, flags, job, synopsis, artwork_url} for a specific media path.
// It fetches the flag row and the newest matching job from procula, plus catalog metadata.
func handleCatalogDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		httputil.WriteError(w, "path required", http.StatusBadRequest)
		return
	}

	type flagsWrap struct {
		Rows []map[string]any `json:"rows"`
	}
	var fw flagsWrap
	if resp, err := services.client.Get(proculaURL + "/api/procula/catalog/flags"); err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		_ = json.Unmarshal(body, &fw)
	}

	flags := []map[string]any{}
	for _, row := range fw.Rows {
		if p, _ := row["path"].(string); p == path {
			if f, ok := row["flags"].([]any); ok {
				for _, item := range f {
					if m, ok := item.(map[string]any); ok {
						flags = append(flags, m)
					}
				}
			}
			break
		}
	}

	var matched map[string]any
	if resp, err := services.client.Get(proculaURL + "/api/procula/jobs"); err == nil {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var all []map[string]any
		_ = json.Unmarshal(body, &all)
		for _, j := range all {
			src, _ := j["source"].(map[string]any)
			if src == nil {
				continue
			}
			if p, _ := src["path"].(string); p == path {
				matched = j
			}
		}
	}

	// Resolve synopsis and artwork from the catalog DB.
	// For episodes: walk up episode → season → series to find the item that carries them.
	synopsis, artworkURL := "", ""
	if item, err := GetCatalogItemByFilePath(catalogDB, path); err == nil && item != nil {
		synopsis = item.Synopsis
		artworkURL = item.ArtworkURL
		if synopsis == "" && artworkURL == "" && item.Type == "episode" {
			// Walk up to season then series.
			if season, err := GetCatalogItemByID(catalogDB, item.ParentID); err == nil && season != nil {
				if series, err := GetCatalogItemByID(catalogDB, season.ParentID); err == nil && series != nil {
					synopsis = series.Synopsis
					artworkURL = series.ArtworkURL
				}
			}
		}
	}

	httputil.WriteJSON(w, map[string]any{
		"path":        path,
		"flags":       flags,
		"job":         matched,
		"synopsis":    synopsis,
		"artwork_url": artworkURL,
	})
}
```

- [ ] **Step 2: Verify the package compiles**

```bash
cd /Users/gwen/workspace/pelicula/middleware && go build ./...
```

Expected: no errors.

- [ ] **Step 3: Run full middleware test suite**

```bash
cd /Users/gwen/workspace/pelicula/middleware && go test ./... -v 2>&1 | tail -20
```

Expected: all tests pass (or pre-existing failures only — no new failures introduced).

- [ ] **Step 4: Commit**

```bash
cd /Users/gwen/workspace/pelicula/middleware && git add catalog.go
git commit -m "feat(catalog): include synopsis and artwork_url in detail endpoint"
```

---

### Task 3: Render synopsis and artwork in the drawer

**Files:**
- Modify: `nginx/catalog.js`
- Modify: `nginx/styles.css`

The drawer body is populated by `renderDetailHtml(data, mediaInfo)`. Add a hero block at the top: poster image on the left (if `artwork_url` is set), synopsis text on the right (if `synopsis` is set). If neither is present, render nothing — the existing Flags section continues to be first.

- [ ] **Step 1: Add hero styles to `styles.css`**

Add after `.drawer-error` (around line 797):

```css
.cat-drawer-hero { display: flex; gap: 1rem; margin-bottom: 1.25rem; align-items: flex-start; }
.cat-drawer-poster { width: 72px; flex-shrink: 0; border-radius: 6px; object-fit: cover; }
.cat-drawer-synopsis { font-size: 0.82rem; line-height: 1.5; color: var(--ink); }
```

- [ ] **Step 2: Update `renderDetailHtml` to render the hero block**

In `nginx/catalog.js`, replace the opening lines of `renderDetailHtml` (the function signature through the first `if (Array.isArray...`) so it reads:

```js
    function renderDetailHtml(data, mediaInfo) {
        const job = data.job || {};
        const val = job.validation || null;
        const codecs = (val && val.checks && val.checks.codecs) || radarrCodecFallback(mediaInfo);
        const parts = [];

        const hasSynopsis = typeof data.synopsis === 'string' && data.synopsis.trim();
        const hasArtwork = typeof data.artwork_url === 'string' && data.artwork_url.trim();
        if (hasSynopsis || hasArtwork) {
            parts.push(html`<div class="cat-drawer-hero">
                ${hasArtwork ? html`<img class="cat-drawer-poster" src="${data.artwork_url}" alt="" loading="lazy">` : raw('')}
                ${hasSynopsis ? html`<div class="cat-drawer-synopsis">${data.synopsis}</div>` : raw('')}
            </div>`);
        }

        if (Array.isArray(data.flags) && data.flags.length) {
```

The rest of the function is unchanged.

- [ ] **Step 3: Verify no syntax errors**

```bash
node --check /Users/gwen/workspace/pelicula/nginx/catalog.js && echo "OK"
```

Expected: `OK`

- [ ] **Step 4: Commit**

```bash
git add nginx/catalog.js nginx/styles.css
git commit -m "feat(drawer): show synopsis and artwork in catalog detail drawer"
```

---

## Self-Review

**Spec coverage:**
- ✓ Backend: `GetCatalogItemByFilePath` added with tests
- ✓ Backend: `handleCatalogDetail` returns `synopsis` + `artwork_url`
- ✓ TV episodes: walk up parent chain to series for synopsis/artwork
- ✓ Frontend: hero block rendered above technical sections
- ✓ Empty states: hero block omitted entirely when neither field is populated
- ✓ Poster image uses `loading="lazy"` to avoid blocking drawer open

**Edge cases handled:**
- `GetCatalogItemByFilePath` returns `(nil, nil)` on miss — handler treats it as empty strings
- Parent chain walk is guarded at each step with nil checks
- `artwork_url` proxied directly from Jellyfin — no auth header needed (Jellyfin's primary image endpoint is public within the LAN)

**No placeholders:** all code is complete and specific.
