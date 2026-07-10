# Catalog Missing Section Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a collapsible "Missing / Searching" section at the top of the catalog tab for monitored-but-undownloaded items, with a *arr-metadata drawer and context-aware actions (Force Search, Unmonitor).

**Architecture:** Client-side split of the existing `/api/pelicula/catalog` response (no new list endpoints needed — Radarr/Sonarr already return `hasFile` and `statistics.episodeFileCount`). Two new backend endpoints: `POST /api/pelicula/catalog/command` for force-search and unmonitor mutations, and `GET /api/pelicula/catalog/qualityprofiles` to resolve profile IDs to names. Partially-downloaded series stay in the main list; only fully-undownloaded items go in the missing section.

**Tech Stack:** Go (middleware), vanilla JS (nginx/catalog.js), CSS (nginx/catalog.css), httptest for backend tests.

---

## File Map

**Create:**
- Nothing new — all changes extend existing files

**Modify:**
- `middleware/catalog.go` — add `handleCatalogCommand`, `handleCatalogQualityProfiles`
- `middleware/catalog_test.go` — tests for both new handlers
- `middleware/main.go` — register two new routes
- `nginx/catalog.js` — `isMissing()`, updated `renderCatalog()`, `buildMissingSection()`, `buildMissingRow()`, `openMissingDetail()`, `renderMissingDetailHtml()`, `openMissingContextMenu()`, `runArrCommand()`, store init additions
- `nginx/catalog.css` — `.cat-missing-*` styles

---

## Task 1: Backend — `handleCatalogCommand`

Force search and unmonitor mutations proxied to Radarr/Sonarr.

**Files:**
- Modify: `middleware/catalog.go` (append after `handleCatalogBackfill`)
- Modify: `middleware/catalog_test.go` (append tests)
- Modify: `middleware/main.go:201` (add route after backfill route)

- [ ] **Step 1: Write the failing tests**

Append to `middleware/catalog_test.go`:

```go
func TestHandleCatalogCommandSearch_Radarr(t *testing.T) {
	var gotBody map[string]any
	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/command" && r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.Write([]byte(`{"id":1}`))
		}
	}))
	defer radarr.Close()

	origR := radarrURL
	radarrURL = radarr.URL
	services = &ServiceClients{RadarrKey: "k"}
	services.client = &http.Client{}
	t.Cleanup(func() { radarrURL = origR })

	body := `{"arr_type":"radarr","arr_id":42,"command":"search"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/catalog/command", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleCatalogCommand(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	if gotBody["name"] != "MoviesSearch" {
		t.Errorf("command name = %v, want MoviesSearch", gotBody["name"])
	}
	ids, _ := gotBody["movieIds"].([]any)
	if len(ids) != 1 || ids[0].(float64) != 42 {
		t.Errorf("movieIds = %v, want [42]", gotBody["movieIds"])
	}
}

func TestHandleCatalogCommandSearch_Sonarr(t *testing.T) {
	var gotBody map[string]any
	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/command" && r.Method == http.MethodPost {
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.Write([]byte(`{"id":1}`))
		}
	}))
	defer sonarr.Close()

	origS := sonarrURL
	sonarrURL = sonarr.URL
	services = &ServiceClients{SonarrKey: "k"}
	services.client = &http.Client{}
	t.Cleanup(func() { sonarrURL = origS })

	body := `{"arr_type":"sonarr","arr_id":7,"command":"search"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/catalog/command", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleCatalogCommand(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	if gotBody["name"] != "SeriesSearch" {
		t.Errorf("command name = %v, want SeriesSearch", gotBody["name"])
	}
	if gotBody["seriesId"].(float64) != 7 {
		t.Errorf("seriesId = %v, want 7", gotBody["seriesId"])
	}
}

func TestHandleCatalogCommandUnmonitor_Radarr(t *testing.T) {
	var putBody map[string]any
	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/movie/42" && r.Method == http.MethodGet:
			w.Write([]byte(`{"id":42,"title":"Foo","monitored":true}`))
		case r.URL.Path == "/api/v3/movie/42" && r.Method == http.MethodPut:
			_ = json.NewDecoder(r.Body).Decode(&putBody)
			w.Write([]byte(`{"id":42}`))
		}
	}))
	defer radarr.Close()

	origR := radarrURL
	radarrURL = radarr.URL
	services = &ServiceClients{RadarrKey: "k"}
	services.client = &http.Client{}
	t.Cleanup(func() { radarrURL = origR })

	body := `{"arr_type":"radarr","arr_id":42,"command":"unmonitor"}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/catalog/command", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handleCatalogCommand(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	if monitored, _ := putBody["monitored"].(bool); monitored {
		t.Errorf("monitored = true after unmonitor, want false")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/gwen/workspace/pelicula/middleware && go test -run 'TestHandleCatalogCommand' ./...
```

Expected: `undefined: handleCatalogCommand`

- [ ] **Step 3: Implement `handleCatalogCommand`**

Add to `middleware/catalog.go`. First add `"fmt"` to the imports block:

```go
import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"pelicula-api/httputil"
	"strings"
)
```

Then append this function after `handleCatalogBackfill`:

```go
// handleCatalogCommand proxies force-search and unmonitor commands to Radarr/Sonarr.
// POST /api/pelicula/catalog/command
// Body: {"arr_type":"radarr"|"sonarr","arr_id":N,"command":"search"|"unmonitor"}
func handleCatalogCommand(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ArrType string `json:"arr_type"`
		ArrID   int    `json:"arr_id"`
		Command string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ArrID == 0 {
		httputil.WriteError(w, "invalid request", http.StatusBadRequest)
		return
	}
	sonarrKey, radarrKey, _ := services.Keys()

	switch req.Command {
	case "search":
		if req.ArrType == "radarr" {
			if _, err := services.ArrPost(radarrURL, radarrKey, "/api/v3/command", map[string]any{
				"name": "MoviesSearch", "movieIds": []int{req.ArrID},
			}); err != nil {
				httputil.WriteError(w, "radarr search failed", http.StatusBadGateway)
				return
			}
		} else {
			if _, err := services.ArrPost(sonarrURL, sonarrKey, "/api/v3/command", map[string]any{
				"name": "SeriesSearch", "seriesId": req.ArrID,
			}); err != nil {
				httputil.WriteError(w, "sonarr search failed", http.StatusBadGateway)
				return
			}
		}
	case "unmonitor":
		if req.ArrType == "radarr" {
			body, err := services.ArrGet(radarrURL, radarrKey, fmt.Sprintf("/api/v3/movie/%d", req.ArrID))
			if err != nil {
				httputil.WriteError(w, "radarr fetch failed", http.StatusBadGateway)
				return
			}
			var movie map[string]any
			if err := json.Unmarshal(body, &movie); err != nil {
				httputil.WriteError(w, "invalid radarr response", http.StatusBadGateway)
				return
			}
			movie["monitored"] = false
			if _, err := services.ArrPut(radarrURL, radarrKey, fmt.Sprintf("/api/v3/movie/%d", req.ArrID), movie); err != nil {
				httputil.WriteError(w, "radarr update failed", http.StatusBadGateway)
				return
			}
		} else {
			body, err := services.ArrGet(sonarrURL, sonarrKey, fmt.Sprintf("/api/v3/series/%d", req.ArrID))
			if err != nil {
				httputil.WriteError(w, "sonarr fetch failed", http.StatusBadGateway)
				return
			}
			var series map[string]any
			if err := json.Unmarshal(body, &series); err != nil {
				httputil.WriteError(w, "invalid sonarr response", http.StatusBadGateway)
				return
			}
			series["monitored"] = false
			if _, err := services.ArrPut(sonarrURL, sonarrKey, fmt.Sprintf("/api/v3/series/%d", req.ArrID), series); err != nil {
				httputil.WriteError(w, "sonarr update failed", http.StatusBadGateway)
				return
			}
		}
	default:
		httputil.WriteError(w, "unknown command", http.StatusBadRequest)
		return
	}
	httputil.WriteJSON(w, map[string]string{"status": "ok"})
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd /Users/gwen/workspace/pelicula/middleware && go test -run 'TestHandleCatalogCommand' ./...
```

Expected: `PASS`

- [ ] **Step 5: Register route in main.go**

In `middleware/main.go`, after line 201 (`mux.Handle("/api/pelicula/catalog/backfill", ...)`), add:

```go
	mux.Handle("/api/pelicula/catalog/command", auth.GuardAdmin(http.HandlerFunc(handleCatalogCommand)))
```

- [ ] **Step 6: Compile check**

```bash
cd /Users/gwen/workspace/pelicula/middleware && go build ./...
```

Expected: no errors

- [ ] **Step 7: Commit**

```bash
cd /Users/gwen/workspace/pelicula && git add middleware/catalog.go middleware/catalog_test.go middleware/main.go
git commit -m "feat(catalog): add command endpoint for force search and unmonitor"
```

---

## Task 2: Backend — `handleCatalogQualityProfiles`

Returns quality profile id→name maps for both Radarr and Sonarr in one call, to be cached by the frontend.

**Files:**
- Modify: `middleware/catalog.go` (append)
- Modify: `middleware/catalog_test.go` (append test)
- Modify: `middleware/main.go` (add route)

- [ ] **Step 1: Write the failing test**

Append to `middleware/catalog_test.go`:

```go
func TestHandleCatalogQualityProfiles(t *testing.T) {
	radarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/qualityprofile" {
			w.Write([]byte(`[{"id":1,"name":"HD-1080p"},{"id":2,"name":"Any"}]`))
		}
	}))
	defer radarr.Close()

	sonarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/qualityprofile" {
			w.Write([]byte(`[{"id":4,"name":"HD TV"}]`))
		}
	}))
	defer sonarr.Close()

	origR, origS := radarrURL, sonarrURL
	radarrURL, sonarrURL = radarr.URL, sonarr.URL
	services = &ServiceClients{RadarrKey: "k", SonarrKey: "k"}
	services.client = &http.Client{}
	t.Cleanup(func() { radarrURL, sonarrURL = origR, origS })

	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/catalog/qualityprofiles", nil)
	w := httptest.NewRecorder()
	handleCatalogQualityProfiles(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Radarr map[string]string `json:"radarr"`
		Sonarr map[string]string `json:"sonarr"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Radarr["1"] != "HD-1080p" {
		t.Errorf("radarr profile 1 = %q, want HD-1080p", resp.Radarr["1"])
	}
	if resp.Sonarr["4"] != "HD TV" {
		t.Errorf("sonarr profile 4 = %q, want HD TV", resp.Sonarr["4"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/gwen/workspace/pelicula/middleware && go test -run 'TestHandleCatalogQualityProfiles' ./...
```

Expected: `undefined: handleCatalogQualityProfiles`

- [ ] **Step 3: Implement `handleCatalogQualityProfiles`**

Append to `middleware/catalog.go` (after `handleCatalogCommand`):

```go
// handleCatalogQualityProfiles returns quality profile id→name maps for Radarr and Sonarr.
// GET /api/pelicula/catalog/qualityprofiles
// Response: {"radarr":{"1":"HD-1080p",...},"sonarr":{"4":"HD TV",...}}
func handleCatalogQualityProfiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sonarrKey, radarrKey, _ := services.Keys()

	type fetch struct {
		data []byte
		err  error
	}
	rCh := make(chan fetch, 1)
	sCh := make(chan fetch, 1)
	go func() {
		body, err := services.ArrGet(radarrURL, radarrKey, "/api/v3/qualityprofile")
		rCh <- fetch{body, err}
	}()
	go func() {
		body, err := services.ArrGet(sonarrURL, sonarrKey, "/api/v3/qualityprofile")
		sCh <- fetch{body, err}
	}()

	buildMap := func(data []byte) map[string]string {
		var profiles []map[string]any
		m := map[string]string{}
		if json.Unmarshal(data, &profiles) != nil {
			return m
		}
		for _, p := range profiles {
			if id, ok := p["id"].(float64); ok {
				if name, ok := p["name"].(string); ok {
					m[fmt.Sprintf("%.0f", id)] = name
				}
			}
		}
		return m
	}

	radarrMap := map[string]string{}
	sonarrMap := map[string]string{}
	if rr := <-rCh; rr.err == nil {
		radarrMap = buildMap(rr.data)
	}
	if sr := <-sCh; sr.err == nil {
		sonarrMap = buildMap(sr.data)
	}

	httputil.WriteJSON(w, map[string]any{
		"radarr": radarrMap,
		"sonarr": sonarrMap,
	})
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/gwen/workspace/pelicula/middleware && go test -run 'TestHandleCatalogQualityProfiles' ./...
```

Expected: `PASS`

- [ ] **Step 5: Register route in main.go**

After the `handleCatalogCommand` route added in Task 1, add:

```go
	mux.Handle("/api/pelicula/catalog/qualityprofiles", auth.Guard(http.HandlerFunc(handleCatalogQualityProfiles)))
```

- [ ] **Step 6: Compile check and run all catalog tests**

```bash
cd /Users/gwen/workspace/pelicula/middleware && go build ./... && go test -run 'TestHandleCatalog' ./...
```

Expected: all `PASS`

- [ ] **Step 7: Commit**

```bash
cd /Users/gwen/workspace/pelicula && git add middleware/catalog.go middleware/catalog_test.go middleware/main.go
git commit -m "feat(catalog): add quality profiles endpoint for missing item drawer"
```

---

## Task 3: Frontend — item classification and missing section

Split the catalog render into present vs. missing, build the collapsible section.

**Files:**
- Modify: `nginx/catalog.js` — `isMissing()`, `renderCatalog()`, `buildMissingSection()`, `buildMissingRow()`
- Modify: `nginx/catalog.css` — `.cat-missing-*` styles

- [ ] **Step 1: Add store keys for quality profiles and missing section**

In `catalog.js`, in the store initialisation block (around line 37, after the existing `store.set` calls), add:

```js
    store.set('catalog.qualityProfiles', null);
```

- [ ] **Step 2: Add `isMissing()` helper**

In `catalog.js`, add after the `fmtSize` function (around line 19):

```js
    // isMissing returns true for items with no downloaded content.
    // Movies: hasFile === false. Series: episodeFileCount === 0.
    // Partially-downloaded series (episodeFileCount > 0) stay in the main list.
    function isMissing(item) {
        if (Array.isArray(item.seasons)) {
            return !(item.statistics && item.statistics.episodeFileCount > 0);
        }
        return !item.hasFile;
    }
```

- [ ] **Step 3: Update `renderCatalog()` to split items**

Replace the existing `renderCatalog` function body (lines 139–152 in the original file):

```js
    function renderCatalog() {
        const list = document.getElementById('cat-list');
        if (!list) return;
        const items = store.get('catalog.items');
        if (!items.length) {
            setHTML(list, html`<div class="no-items">No items found.</div>`);
            return;
        }
        const missingItems = items.filter(isMissing);
        const presentItems = items.filter(item => !isMissing(item));
        const frag = document.createDocumentFragment();
        const missingSection = buildMissingSection(missingItems);
        if (missingSection) frag.appendChild(missingSection);
        for (const item of presentItems) {
            frag.appendChild(Array.isArray(item.seasons) ? buildSeriesRow(item) : buildMovieRow(item));
        }
        list.replaceChildren(frag);
    }
```

- [ ] **Step 4: Add `buildMissingSection()` and `buildMissingRow()`**

Add these two functions in `catalog.js` before the `buildMovieRow` function (around line 154):

```js
    function buildMissingSection(items) {
        if (!items.length) return null;
        const details = document.createElement('details');
        details.className = 'cat-missing-section';
        // collapsed by default — open attribute intentionally omitted

        const summary = document.createElement('summary');
        summary.className = 'cat-missing-header';
        const titleSpan = document.createElement('span');
        titleSpan.className = 'cat-missing-title';
        titleSpan.textContent = 'Missing / Searching';
        const countSpan = document.createElement('span');
        countSpan.className = 'cat-missing-count';
        countSpan.textContent = String(items.length);
        summary.appendChild(titleSpan);
        summary.appendChild(countSpan);
        details.appendChild(summary);

        const inner = document.createElement('div');
        inner.className = 'cat-missing-list';
        for (const item of items) inner.appendChild(buildMissingRow(item));
        details.appendChild(inner);
        return details;
    }

    function buildMissingRow(item) {
        const isSeries = Array.isArray(item.seasons);
        const metaParts = [];
        if (item.year) metaParts.push(item.year);
        if (isSeries && item.statistics) {
            metaParts.push(item.statistics.episodeFileCount + '/' + item.statistics.totalEpisodeCount + ' ep');
        }
        const div = document.createElement('div');
        div.className = 'cat-row cat-row-missing';
        setHTML(div, html`
            <span class="cat-row-title" title="${item.title || ''}">${item.title || '(untitled)'}</span>
            <span class="cat-row-meta">${metaParts.join(' \u00b7 ')}</span>
            <div class="cat-row-actions"><button class="cat-ctx-btn" title="Actions">\u22ef</button></div>`);
        div.addEventListener('click', (e) => {
            if (e.target.closest('.cat-ctx-btn')) return;
            openMissingDetail(item);
        });
        div.addEventListener('contextmenu', (e) => {
            e.preventDefault();
            openMissingContextMenu(e, item);
        });
        div.querySelector('.cat-ctx-btn').addEventListener('click', (e) => {
            e.stopPropagation();
            openMissingContextMenu(e, item);
        });
        return div;
    }
```

- [ ] **Step 5: Add CSS for the missing section**

In `nginx/catalog.css`, append after the `.cat-attention-row:hover` rule (after line 266):

```css
/* ── Missing / Searching section ────────────────────────────────────────── */
.cat-missing-section {
    margin: 0.5rem 0 1rem 0;
    border: 1px solid rgba(255, 178, 102, 0.35);
    border-radius: 8px;
    background: rgba(255, 178, 102, 0.05);
}
.cat-missing-header {
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.5rem 0.75rem;
    font-weight: 600;
    cursor: pointer;
    list-style: none;
}
.cat-missing-header::-webkit-details-marker { display: none; }
.cat-missing-count {
    display: inline-block;
    min-width: 1.25rem;
    padding: 0 0.4rem;
    border-radius: 999px;
    background: rgba(255, 178, 102, 0.7);
    color: #000;
    font-size: 0.75rem;
    text-align: center;
}
.cat-missing-list { padding: 0 0.25rem 0.5rem; }
.cat-row-missing { cursor: pointer; }
```

- [ ] **Step 6: Verify in browser**

Start the dev stack (`pelicula up`) and open the catalog tab. Confirm:
- Monitored-but-undownloaded items no longer appear in the main list
- A "Missing / Searching" section appears at the top, collapsed by default, with item count
- Clicking the summary opens/closes it
- Missing rows show title, year, episode count for series

- [ ] **Step 7: Commit**

```bash
cd /Users/gwen/workspace/pelicula && git add nginx/catalog.js nginx/catalog.css
git commit -m "feat(catalog): split missing items into collapsible Missing / Searching section"
```

---

## Task 4: Frontend — missing item drawer

Clicking a missing item opens the existing `cat-drawer` with *arr metadata instead of file details.

**Files:**
- Modify: `nginx/catalog.js` — `openMissingDetail()`, `renderMissingDetailHtml()`

- [ ] **Step 1: Add `openMissingDetail()` and `renderMissingDetailHtml()`**

In `catalog.js`, add these functions after the existing `openDetail` function (after line ~413):

```js
    async function openMissingDetail(item) {
        const isSeries = Array.isArray(item.seasons);
        const arrType = isSeries ? 'sonarr' : 'radarr';
        const backdrop = document.getElementById('cat-drawer-backdrop');
        const drawer = document.getElementById('cat-drawer');
        const titleEl = document.getElementById('cat-drawer-title');
        const sub = document.getElementById('cat-drawer-sub');
        const body = document.getElementById('cat-drawer-body');
        if (!drawer) return;
        PeliculaFW.openDrawer(drawer, backdrop);
        titleEl.textContent = item.title || '(untitled)';
        titleEl.title = item.title || '';
        sub.textContent = item.monitored ? 'Monitored \u2014 not downloaded' : 'Not monitored';
        sub.title = '';
        setHTML(body, html`<div style="color:var(--muted);padding:1rem 0">Loading\u2026</div>`);

        // Fetch and cache quality profiles
        let profilesData = store.get('catalog.qualityProfiles');
        if (!profilesData) {
            try {
                const res = await catFetch('/api/pelicula/catalog/qualityprofiles');
                if (res.ok) {
                    profilesData = await res.json();
                    store.set('catalog.qualityProfiles', profilesData);
                }
            } catch (e) { /* non-critical — will show profile ID instead of name */ }
        }

        setHTML(body, renderMissingDetailHtml(item, arrType, profilesData));
    }

    function renderMissingDetailHtml(item, arrType, profilesData) {
        const isSeries = Array.isArray(item.seasons);
        const stats = item.statistics || {};
        const profileId = item.qualityProfileId;
        const profileMap = profilesData && profilesData[arrType] ? profilesData[arrType] : {};
        const profileName = profileId
            ? (profileMap[String(profileId)] || 'Profile #' + profileId)
            : '\u2014';

        const parts = [];

        // Poster (Radarr/Sonarr both return images array with coverType)
        const poster = (item.images || []).find(img => img.coverType === 'poster');
        if (poster) {
            parts.push(html`<div class="cat-drawer-hero">
                <img class="cat-drawer-poster" src="${poster.remoteUrl || poster.url || ''}" alt="" loading="lazy">
            </div>`);
        }

        // Overview / synopsis
        if (item.overview) {
            parts.push(html`<div class="cat-drawer-synopsis">${item.overview}</div>`);
        }

        parts.push(html`<div class="drawer-section-title">Status</div>`);
        parts.push(html`<div>
            <span class="cat-pill">${item.monitored ? 'monitored' : 'unmonitored'}</span>
            <span class="cat-pill">${isSeries ? 'series' : 'movie'}</span>
            ${item.status ? html`<span class="cat-pill">${item.status}</span>` : raw('')}
            ${item.network ? html`<span class="cat-pill">${item.network}</span>` : raw('')}
        </div>`);

        parts.push(html`<div class="drawer-section-title">Quality Profile</div>`);
        parts.push(html`<div><span class="cat-pill cat-pill-encoding">${profileName}</span></div>`);

        if (isSeries) {
            const downloaded = stats.episodeFileCount || 0;
            const total = stats.totalEpisodeCount || 0;
            const monitored = stats.monitoredEpisodeCount || 0;
            parts.push(html`<div class="drawer-section-title">Episodes</div>`);
            parts.push(html`<div>
                <span class="cat-pill">${downloaded}/${total} downloaded</span>
                <span class="cat-pill">${monitored} monitored</span>
            </div>`);
        }

        if (Array.isArray(item.genres) && item.genres.length) {
            parts.push(html`<div class="drawer-section-title">Genres</div>`);
            parts.push(html`<div>${item.genres.map(g => html`<span class="cat-pill">${g}</span>`)}</div>`);
        }

        return html`<div>${parts}</div>`;
    }
```

- [ ] **Step 2: Verify in browser**

Click a missing item row. Confirm the drawer opens and shows:
- Title and "Monitored — not downloaded" subtitle
- Poster image (if available)
- Overview text
- Status pills (monitored, type, status, network for series)
- Quality profile name (resolved from profiles cache)
- Episodes stats for series

- [ ] **Step 3: Commit**

```bash
cd /Users/gwen/workspace/pelicula && git add nginx/catalog.js
git commit -m "feat(catalog): missing item drawer with arr metadata and quality profile"
```

---

## Task 5: Frontend — missing context menu and arr command runner

Right-click or ⋯ on a missing item shows Force Search and Unmonitor actions.

**Files:**
- Modify: `nginx/catalog.js` — `openMissingContextMenu()`, `runArrCommand()`

- [ ] **Step 1: Add `openMissingContextMenu()` and `runArrCommand()`**

In `catalog.js`, add these functions after `openMissingDetail` (after `renderMissingDetailHtml`):

```js
    function openMissingContextMenu(event, item) {
        if (_openMenu) { _openMenu.remove(); _openMenu = null; }
        const menu = document.createElement('div');
        menu.className = 'cat-ctx-menu';
        menu.addEventListener('click', (e) => e.stopPropagation());
        _openMenu = menu;

        const isSeries = Array.isArray(item.seasons);
        const arrType = isSeries ? 'sonarr' : 'radarr';

        const actions = [
            {
                label: 'Force Search',
                fn: () => { closeMenu(); runArrCommand('search', arrType, item.id, item.title); },
            },
            {
                label: 'Unmonitor',
                fn: () => { closeMenu(); runArrCommand('unmonitor', arrType, item.id, item.title); },
            },
        ];

        for (const action of actions) {
            const btn = document.createElement('button');
            btn.className = 'cat-ctx-item';
            btn.textContent = action.label;
            btn.addEventListener('click', action.fn);
            menu.appendChild(btn);
        }

        document.body.appendChild(menu);
        positionMenu(menu, event);
        function closeMenu() { if (_openMenu === menu) { menu.remove(); _openMenu = null; } }
    }

    async function runArrCommand(command, arrType, arrId, title) {
        const label = command === 'search' ? 'Force Search' : 'Unmonitor';
        try {
            const res = await catFetch('/api/pelicula/catalog/command', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ arr_type: arrType, arr_id: arrId, command }),
            });
            if (!res.ok) {
                const data = await res.json().catch(() => ({}));
                toast(label + ' failed: ' + (data.error || 'unknown'), { error: true });
                return;
            }
            const successMsg = command === 'search'
                ? (title || 'Item') + ': search triggered'
                : (title || 'Item') + ': unmonitored';
            toast(successMsg);
        } catch (e) {
            toast(label + ' error: ' + e.message, { error: true });
        }
    }
```

- [ ] **Step 2: Verify in browser**

Right-click a missing item (or click ⋯). Confirm:
- Menu shows "Force Search" and "Unmonitor" (not the procula action registry items)
- "Force Search" shows a toast confirming search triggered
- "Unmonitor" shows a toast confirming unmonitored, and the item moves to unmonitored state in Radarr/Sonarr (verify in the *arr UI or re-load catalog)

Also confirm that right-clicking a downloaded (present) item still shows the original procula actions (re-verify, etc.) — the `openContextMenu` path is unchanged.

- [ ] **Step 3: Commit**

```bash
cd /Users/gwen/workspace/pelicula && git add nginx/catalog.js
git commit -m "feat(catalog): context menu for missing items — Force Search and Unmonitor"
```

---

## Self-Review

**Spec coverage check:**

| Requirement | Task |
|---|---|
| Missing items in separate collapsible section, top of catalog, collapsed by default | Task 3 |
| Fully undownloaded movies: `hasFile === false` | Task 3 (`isMissing`) |
| Fully undownloaded series: `episodeFileCount === 0` | Task 3 (`isMissing`) |
| Partially downloaded series stay in main list | Task 3 (filter logic) |
| Clicking missing item opens drawer with *arr metadata | Task 4 |
| Drawer shows: title, monitored status, quality profile name, statistics, overview, poster | Task 4 |
| Quality profile resolved by name, not just ID | Task 2 + Task 4 |
| Context menu for missing items: Force Search, Unmonitor | Task 5 |
| Force search hits *arr API directly (not procula) | Task 1 + Task 5 |
| Unmonitor: GET item → set monitored:false → PUT | Task 1 |
| Downloaded items retain their existing context menu | Task 5 (unchanged `openContextMenu` path) |
| Architecture leaves room for expansion | `openMissingContextMenu` and `openMissingDetail` are separate functions, not entangled with the existing paths |

**Placeholder scan:** None found.

**Type consistency check:**
- `isMissing(item)` called in `renderCatalog()` (Task 3) — consistent
- `buildMissingRow(item)` calls `openMissingDetail(item)` and `openMissingContextMenu(e, item)` — defined in Tasks 4 and 5
- `runArrCommand('search', arrType, item.id, item.title)` — `arrType` is always `'radarr'` or `'sonarr'`, matching backend `arr_type` field
- `store.get('catalog.qualityProfiles')` initialized in Task 3 Step 1, used in Task 4
- Backend `handleCatalogQualityProfiles` returns `{"radarr":{"1":"name"},...}` — matches `profilesData[arrType]` access in Task 4
