// receive.go — inbound webhook receipt and path allowlist.
// See docs/PELIGROSA.md for the trust boundary rationale.
package hooks

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"pelicula-api/httputil"
	"pelicula-api/internal/app/catalog"
)

// HandleImportHook receives *arr import webhooks, normalizes the payload,
// and forwards a job to Procula.
func (h *Handler) HandleImportHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Verify shared secret (passed via X-Webhook-Secret header by autowire).
	// If WEBHOOK_SECRET is unset the check is skipped (backward compat with
	// existing installs that haven't been re-run through setup/reset).
	if secret := strings.TrimSpace(os.Getenv("WEBHOOK_SECRET")); secret != "" {
		provided := r.Header.Get("X-Webhook-Secret")
		if subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) == 0 {
			httputil.WriteError(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httputil.WriteError(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		httputil.WriteError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	eventType, _ := raw["eventType"].(string)
	// Only process Download (import) events; silently accept test pings.
	if strings.EqualFold(eventType, "test") {
		httputil.WriteJSON(w, map[string]string{"status": "ok"})
		return
	}
	if !strings.EqualFold(eventType, "download") {
		slog.Info("ignoring webhook event", "component", "hooks", "event_type", eventType)
		httputil.WriteJSON(w, map[string]string{"status": "ignored"})
		return
	}

	source, err := NormalizeHookPayload(raw)
	if err != nil {
		slog.Error("failed to normalize webhook", "component", "hooks", "error", err)
		httputil.WriteError(w, "invalid webhook payload: "+err.Error(), http.StatusBadRequest)
		return
	}

	slog.Info("import webhook received", "component", "hooks",
		"arr_type", source.ArrType, "title", source.Title,
		"type", source.Type, "path", source.Path, "episode_id", source.EpisodeID)

	// Forward to Procula via the typed client.
	if err := h.forwardToProcula(r.Context(), source); err != nil {
		slog.Error("failed to forward to Procula", "component", "hooks", "error", err)
		// Don't fail the webhook — *arr doesn't retry sensibly on 5xx.
		httputil.WriteJSON(w, map[string]string{"status": "queued", "warning": err.Error()})
		return
	}

	// Upsert catalog record — best-effort, non-blocking.
	if h.CatalogDB != nil {
		go func() {
			if err := catalog.UpsertFromHook(context.Background(), h.CatalogDB, source); err != nil {
				slog.Error("catalog upsert from hook failed", "component", "hooks", "error", err)
			}
		}()
	}

	// Mark any matching pending request as available now that the content has landed.
	// Webhook type is "movie" or "episode"; requests use "movie" or "series".
	if h.RequestStore != nil {
		reqType := source.Type
		if reqType == "episode" {
			reqType = "series"
		}
		go h.RequestStore.MarkAvailable(reqType, source.TmdbID, source.TvdbID, source.Title, h.Notify) //nolint:errcheck
	}

	// When SEEDING_REMOVE_ON_COMPLETE is set, delete the torrent from qBittorrent
	// immediately after *arr has imported (and hardlinked) the file. The file itself
	// is preserved; only the torrent entry is removed.
	if os.Getenv("SEEDING_REMOVE_ON_COMPLETE") == "true" && source.DownloadHash != "" && h.Qbt != nil {
		if err := h.Qbt.RemoveTorrent(r.Context(), source.DownloadHash); err != nil {
			slog.Warn("remove-on-complete: failed to delete torrent", "component", "hooks",
				"hash", shortHash(source.DownloadHash), "error", err)
		} else {
			slog.Info("remove-on-complete: torrent removed", "component", "hooks",
				"hash", shortHash(source.DownloadHash))
		}
	}

	httputil.WriteJSON(w, map[string]string{"status": "queued"})
}

// forwardToProcula creates a job in Procula for the given source.
func (h *Handler) forwardToProcula(ctx context.Context, source catalog.ProculaJobSource) error {
	if _, err := h.Procula.CreateJob(ctx, source); err != nil {
		return err
	}
	return nil
}

func shortHash(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}
