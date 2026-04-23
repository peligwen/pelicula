package autowire

import (
	"encoding/json"
	"log/slog"
)

// defaultIndexers is a curated list of legal, free, publicly accessible
// torrent indexers that ship as Cardigann definitions in the stock Prowlarr
// linuxserver.io Docker image. We use only "1337x" and "yts" as the minimal
// verified-safe set. Additional names (eztv, thepiratebay, kickasstorrents-ws)
// may work on some Prowlarr versions but their definition names vary across
// releases. Extend this list only after verifying the exact definitionName
// in the running image.
var defaultIndexers = []string{
	"1337x",
	"yts",
}

// seedDefaultIndexers adds any missing default indexers to Prowlarr.
// It is idempotent: it lists existing indexers and only POSTs the ones not
// already present by name. Partial failures are non-fatal; the function logs
// a warning and continues. If Prowlarr is unreachable the function returns
// immediately without error.
func (a *Autowirer) seedDefaultIndexers() {
	if !a.seedIndexers {
		return
	}

	prowlarrKey := a.svc.GetProwlarrKey()

	// List existing indexers.
	data, err := a.svc.ArrGet(a.urls.Prowlarr, prowlarrKey, "/api/v1/indexer")
	if err != nil {
		slog.Warn("seedDefaultIndexers: could not reach Prowlarr — skipping", "component", "autowire", "error", err)
		return
	}

	var existing []map[string]any
	if err := json.Unmarshal(data, &existing); err != nil {
		slog.Warn("seedDefaultIndexers: failed to parse indexer list — skipping", "component", "autowire", "error", err)
		return
	}

	// Build a set of names that are already configured.
	present := make(map[string]bool, len(existing))
	for _, idx := range existing {
		if name, _ := idx["name"].(string); name != "" {
			present[name] = true
		}
	}

	added := 0
	for _, defName := range defaultIndexers {
		// Match against the "name" field Prowlarr returns. Prowlarr typically
		// sets the indexer name equal to the definitionName on first add, so
		// this check is sufficient for idempotency in the common case.
		if present[defName] {
			slog.Debug("seedDefaultIndexers: indexer already present, skipping",
				"component", "autowire", "indexer", defName)
			continue
		}

		// Fetch the Cardigann schema for this definition.
		schemaPath := "/api/v1/indexer/schema?type=Cardigann&definitionName=" + defName
		schemaData, err := a.svc.ArrGet(a.urls.Prowlarr, prowlarrKey, schemaPath)
		if err != nil {
			slog.Warn("seedDefaultIndexers: failed to fetch schema, skipping",
				"component", "autowire", "indexer", defName, "error", err)
			continue
		}

		var schema map[string]any
		if err := json.Unmarshal(schemaData, &schema); err != nil {
			slog.Warn("seedDefaultIndexers: failed to parse schema, skipping",
				"component", "autowire", "indexer", defName, "error", err)
			continue
		}

		// POST the schema as-is to create the indexer.
		_, err = a.svc.ArrPost(a.urls.Prowlarr, prowlarrKey, "/api/v1/indexer", schema)
		if err != nil {
			slog.Warn("seedDefaultIndexers: failed to add indexer, skipping",
				"component", "autowire", "indexer", defName, "error", err)
			continue
		}

		slog.Info("seedDefaultIndexers: added indexer",
			"component", "autowire", "indexer", defName)
		added++
	}

	if added > 0 {
		// Invalidate the indexer count cache so the dashboard reflects the new
		// indexers immediately rather than waiting for the TTL to expire.
		a.invalidateIdx()
	}
}
