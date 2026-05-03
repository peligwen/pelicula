package catalog

// reconcile.go — orphan reconciler for catalog.db.
//
// Invariant enforced: every Jellyfin Movie whose file path falls under a
// Radarr root folder must either have a catalog_items row OR be traceable
// to a Radarr movie with hasFile=true. Items satisfying neither condition
// are "orphaned" and get a new catalog_items row with source='reconcile'.
//
// TV/Sonarr reconcile is deferred; only movies are handled here.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
)

// reconcileBatchLimit caps the number of Jellyfin movie items scanned per run.
// Prevents runaway memory use on large libraries. Raise if needed.
const reconcileBatchLimit = 500

// ReconcileResult summarises one reconcile run.
type ReconcileResult struct {
	Added   int `json:"added"`
	Updated int `json:"updated"` // reserved; always 0 today
	Scanned int `json:"scanned"`
}

// jellyfinMovieItem is a Jellyfin item with the Path field needed for reconcile.
type jellyfinMovieItem struct {
	ID             string            `json:"Id"`
	Name           string            `json:"Name"`
	ProductionYear int               `json:"ProductionYear"`
	Path           string            `json:"Path"`
	ProviderIDs    map[string]string `json:"ProviderIds"`
}

// ReconcileOrphans identifies Jellyfin Movie items that fall under a Radarr
// root folder but have no catalog_items row and are not traceable to a Radarr
// movie with hasFile=true. For each such item a new row is inserted with
// source='reconcile'.
//
// The reconciler is read-only on Jellyfin and Radarr; it only writes to
// catalog.db. Running it twice in a row produces zero additional rows the
// second time (idempotent).
func ReconcileOrphans(ctx context.Context, db *sql.DB, jf JellyfinMetaClient, radarr ArrClient) (ReconcileResult, error) {
	var result ReconcileResult

	_, radarrKey, _ := radarr.Keys()
	if radarrKey == "" {
		slog.Info("reconcile: radarr not configured, skipping", "component", "catalog_reconcile")
		return result, nil
	}

	radarrClient := radarr.RadarrClient()

	// 1. Fetch Radarr root folder paths.
	rootFolders, err := radarrClient.ListRootFolders(ctx, "/api/v3")
	if err != nil {
		return result, fmt.Errorf("fetch radarr root folders: %w", err)
	}
	if len(rootFolders) == 0 {
		slog.Info("reconcile: no radarr root folders configured", "component", "catalog_reconcile")
		return result, nil
	}

	rootPaths := make([]string, 0, len(rootFolders))
	for _, rf := range rootFolders {
		p, _ := rf["path"].(string)
		if p != "" {
			// Ensure trailing slash for prefix matching
			if !strings.HasSuffix(p, "/") {
				p += "/"
			}
			rootPaths = append(rootPaths, p)
		}
	}

	// 2. Fetch Jellyfin Movies with Path field, bounded by reconcileBatchLimit.
	uid := jf.GetJellyfinUserID()
	if uid == "" {
		// Resolve user ID from Jellyfin API.
		body, err := jf.JellyfinGet(ctx, "/Users", jf.GetJellyfinAPIKey())
		if err != nil {
			return result, fmt.Errorf("fetch jellyfin users: %w", err)
		}
		var users []struct {
			ID   string `json:"Id"`
			Name string `json:"Name"`
		}
		if err := json.Unmarshal(body, &users); err != nil {
			return result, fmt.Errorf("parse jellyfin users: %w", err)
		}
		for _, u := range users {
			if u.Name == jellyfinServiceUser {
				uid = u.ID
				break
			}
		}
		if uid == "" && len(users) > 0 {
			uid = users[0].ID
		}
		if uid == "" {
			return result, fmt.Errorf("no jellyfin users found")
		}
		jf.SetJellyfinUserID(uid)
	}

	jfPath := fmt.Sprintf(
		"/Users/%s/Items?IncludeItemTypes=Movie&Fields=Path,ProviderIds&Recursive=true&Limit=%d",
		uid, reconcileBatchLimit,
	)
	jfBody, err := jf.JellyfinGet(ctx, jfPath, jf.GetJellyfinAPIKey())
	if err != nil {
		return result, fmt.Errorf("fetch jellyfin movies: %w", err)
	}
	var jfResp struct {
		Items []jellyfinMovieItem `json:"Items"`
	}
	if err := json.Unmarshal(jfBody, &jfResp); err != nil {
		return result, fmt.Errorf("parse jellyfin movies: %w", err)
	}

	// Filter to items under a Radarr root folder.
	var candidates []jellyfinMovieItem
	for _, item := range jfResp.Items {
		if item.Path == "" {
			continue
		}
		for _, root := range rootPaths {
			if strings.HasPrefix(item.Path, root) {
				candidates = append(candidates, item)
				break
			}
		}
	}

	if len(candidates) == 0 {
		slog.Info("reconcile: no jellyfin movies under radarr root folders", "component", "catalog_reconcile")
		return result, nil
	}

	// 3. Build Radarr index keyed by tmdb_id and file_path (hasFile=true only).
	radarrMovies, err := radarrClient.GetMovies(ctx, "/api/v3")
	if err != nil {
		return result, fmt.Errorf("fetch radarr movies: %w", err)
	}

	type radarrEntry struct {
		tmdbID   int
		filePath string
		title    string
		year     int
		arrID    int
	}
	radarrByTmdb := make(map[int]radarrEntry, len(radarrMovies))
	radarrByFilePath := make(map[string]radarrEntry, len(radarrMovies))

	for _, m := range radarrMovies {
		hasFile, _ := m["hasFile"].(bool)
		if !hasFile {
			continue
		}
		tmdbID := int(floatVal(m, "tmdbId"))
		arrID := int(floatVal(m, "id"))
		title, _ := m["title"].(string)
		year := int(floatVal(m, "year"))
		fp := ""
		if mf, ok := m["movieFile"].(map[string]any); ok {
			fp, _ = mf["path"].(string)
		}
		entry := radarrEntry{tmdbID: tmdbID, filePath: fp, title: title, year: year, arrID: arrID}
		if tmdbID != 0 {
			radarrByTmdb[tmdbID] = entry
		}
		if fp != "" {
			radarrByFilePath[fp] = entry
		}
	}

	// 4. For each candidate: check catalog.db, then Radarr index.
	for _, jfItem := range candidates {
		result.Scanned++

		// (a) Catalog check: is there already a row for this file_path?
		existing, err := GetCatalogItemByFilePath(ctx, db, jfItem.Path)
		if err != nil {
			slog.Warn("reconcile: catalog lookup error",
				"component", "catalog_reconcile", "path", jfItem.Path, "error", err)
			continue
		}
		if existing != nil {
			// Already cataloged — skip.
			continue
		}

		// Also check by tmdb_id if available.
		jfTmdbStr, _ := jfItem.ProviderIDs["Tmdb"]
		jfTmdbID := 0
		if jfTmdbStr != "" {
			fmt.Sscanf(jfTmdbStr, "%d", &jfTmdbID) //nolint:errcheck
		}

		if jfTmdbID != 0 {
			// Check if a catalog row exists by tmdb_id
			rows, err := db.QueryContext(ctx,
				`SELECT id FROM catalog_items WHERE type='movie' AND tmdb_id=? LIMIT 1`,
				jfTmdbID)
			if err == nil {
				found := rows.Next()
				rows.Close()
				if found {
					continue
				}
			}
		}

		// (b) Radarr traceability check: skip if Radarr already has this file.
		if _, ok := radarrByFilePath[jfItem.Path]; ok {
			// Radarr knows about this file; BackfillFromArr will handle it.
			continue
		}
		if jfTmdbID != 0 {
			if _, ok := radarrByTmdb[jfTmdbID]; ok {
				// Radarr has this movie with hasFile=true.
				continue
			}
		}

		// (c) Item is an orphan — insert with source='reconcile'.
		title := jfItem.Name
		if title == "" {
			title = filepath.Base(jfItem.Path)
		}
		year := jfItem.ProductionYear

		_, err = InsertReconciledItem(ctx, db, CatalogItem{
			Type:     "movie",
			TmdbID:   jfTmdbID,
			ArrType:  "radarr",
			Title:    title,
			Year:     year,
			Tier:     "library",
			FilePath: jfItem.Path,
		})
		if err != nil {
			slog.Error("reconcile: insert orphan",
				"component", "catalog_reconcile", "path", jfItem.Path, "error", err)
			continue
		}

		result.Added++
		slog.Info("reconcile: inserted orphan",
			"component", "catalog_reconcile",
			"title", title,
			"path", jfItem.Path,
		)
	}

	slog.Info("reconcile run complete",
		"component", "catalog_reconcile",
		"scanned", result.Scanned,
		"added", result.Added,
	)
	return result, nil
}
