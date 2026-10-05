package autowire

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"pelicula/internal/config"
)

// The *arr APIs return resources as JSON objects whose "fields" member is a
// list of {"name": ..., "value": ...}. The helpers below read and patch such a
// decoded resource in place so an update can PUT back exactly what the app sent
// us, with only the drifted fields changed.

// clone deep-copies a decoded resource through JSON. It also normalises
// whatever shape the client handed back (typed slices, ints) to plain decoded
// JSON, and keeps us from mutating the caller's data.
func clone(m map[string]any) (map[string]any, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// jsonEqual compares two values by their JSON encoding, so 8080 equals
// float64(8080) and []map[string]string equals the decoded []any.
func jsonEqual(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

func fieldValue(res map[string]any, name string) (any, bool) {
	fields, _ := res["fields"].([]any)
	for _, f := range fields {
		if fm, ok := f.(map[string]any); ok && fm["name"] == name {
			return fm["value"], true
		}
	}
	return nil, false
}

func setField(res map[string]any, name string, v any) {
	fields, _ := res["fields"].([]any)
	for _, f := range fields {
		if fm, ok := f.(map[string]any); ok && fm["name"] == name {
			fm["value"] = v
			return
		}
	}
	res["fields"] = append(fields, map[string]any{"name": name, "value": v})
}

type fieldSpec struct {
	name  string
	value any
	// ifPresent: only enforce the value when the resource carries the field
	// (some fields are absent when they hold their default, or have a
	// different name per app).
	ifPresent bool
}

// reconcileFields sets every spec'd field that is missing or different and
// reports whether anything changed.
func reconcileFields(res map[string]any, specs []fieldSpec) (drift bool) {
	for _, s := range specs {
		got, ok := fieldValue(res, s.name)
		if !ok && s.ifPresent {
			continue
		}
		if !ok || !jsonEqual(got, s.value) {
			setField(res, s.name, s.value)
			drift = true
		}
	}
	return drift
}

// reconcileFlag sets res[key] = want when it differs.
func reconcileFlag(res map[string]any, key string, want bool) bool {
	if got, _ := res[key].(bool); got == want {
		return false
	}
	res[key] = want
	return true
}

// resourceID extracts the numeric "id" of a listed resource.
func resourceID(res map[string]any) (int, error) {
	switch v := res["id"].(type) {
	case float64:
		return int(v), nil
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case json.Number:
		n, err := v.Int64()
		return int(n), err
	}
	return 0, errNoID
}

func fieldList(specs ...fieldSpec) []map[string]any {
	out := make([]map[string]any, 0, len(specs))
	for _, s := range specs {
		out = append(out, map[string]any{"name": s.name, "value": s.value})
	}
	return out
}

// wireRootFolder ensures the *arr app has path as a root folder.
func wireRootFolder(ctx context.Context, log *slog.Logger, name string, c ArrClient, path string) error {
	folders, err := c.ListRootFolders(ctx)
	if err != nil {
		return fmt.Errorf("list root folders: %w", err)
	}
	for _, f := range folders {
		if p, _ := f["path"].(string); samePath(p, path) {
			log.Info("root folder already configured", "service", name, "path", path)
			return nil
		}
	}
	if err := c.AddRootFolder(ctx, map[string]any{"path": path}); err != nil {
		return fmt.Errorf("add root folder %s: %w", path, err)
	}
	log.Info("added root folder", "service", name, "path", path)
	return nil
}

// wireDownloadClient ensures the *arr app has a qBittorrent download client
// pointing at gluetun:8080 with the app's category. qBittorrent needs no
// credentials: the CLI seeds a subnet whitelist.
//
// catFields lists the names under which the category may appear: the *arr
// schema calls it tvCategory (Sonarr) / movieCategory (Radarr); the legacy
// plain "category" is written too, as the old middleware did.
func wireDownloadClient(ctx context.Context, log *slog.Logger, name string, c ArrClient, category string, catFields []string) error {
	existing, err := c.ListDownloadClients(ctx)
	if err != nil {
		return fmt.Errorf("list download clients: %w", err)
	}

	want := []fieldSpec{
		{name: "host", value: qbtHost},
		{name: "port", value: qbtPort},
		{name: "useSsl", value: false, ifPresent: true}, // absent means false
	}
	for _, f := range catFields {
		want = append(want, fieldSpec{name: f, value: category, ifPresent: true})
	}

	for _, e := range existing {
		if impl, _ := e["implementation"].(string); impl != "QBittorrent" {
			continue
		}
		res, err := clone(e)
		if err != nil {
			return fmt.Errorf("copy download client: %w", err)
		}
		if !reconcileFields(res, want) {
			log.Info("qBittorrent download client already configured", "service", name)
			return nil
		}
		id, err := resourceID(res)
		if err != nil {
			return fmt.Errorf("update download client: %w", err)
		}
		if err := c.UpdateDownloadClient(ctx, id, res); err != nil {
			return fmt.Errorf("update download client: %w", err)
		}
		log.Info("updated qBittorrent download client (drift corrected)", "service", name)
		return nil
	}

	add := []fieldSpec{
		{name: "host", value: qbtHost},
		{name: "port", value: qbtPort},
		{name: "username", value: ""},
		{name: "password", value: ""},
	}
	for _, f := range catFields {
		add = append(add, fieldSpec{name: f, value: category})
	}
	payload := map[string]any{
		"name":           "qBittorrent",
		"implementation": "QBittorrent",
		"configContract": "QBittorrentSettings",
		"protocol":       "torrent",
		"enable":         true,
		"priority":       1,
		"fields":         fieldList(add...),
	}
	if err := c.AddDownloadClient(ctx, payload); err != nil {
		return fmt.Errorf("add download client: %w", err)
	}
	log.Info("added qBittorrent download client", "service", name, "category", category)
	return nil
}

// wireImportWebhook ensures the *arr app has a "Pelicula" Webhook notification
// that POSTs imports to this server. The shared secret travels in an
// X-Webhook-Secret header (the webhook's "headers" field), not in the URL, so
// it stays out of the *arr logs.
func wireImportWebhook(ctx context.Context, log *slog.Logger, name string, c ArrClient, cfg config.Config) error {
	hookURL := strings.TrimRight(cfg.SelfURL, "/") + webhookPath
	secret := cfg.WebhookSecret

	existing, err := c.ListNotifications(ctx)
	if err != nil {
		return fmt.Errorf("list notifications: %w", err)
	}

	for _, e := range existing {
		if n, _ := e["name"].(string); n != webhookName {
			continue
		}
		res, err := clone(e)
		if err != nil {
			return fmt.Errorf("copy notification: %w", err)
		}
		drift := reconcileFields(res, []fieldSpec{
			{name: "url", value: hookURL},
			{name: "method", value: webhookPOST},
		})
		if got := headerSecret(res); got != secret {
			if secret != "" {
				setField(res, "headers", secretHeaders(secret))
			} else {
				setField(res, "headers", []map[string]string{})
			}
			drift = true
		}
		if reconcileFlag(res, "onDownload", true) {
			drift = true
		}
		if reconcileFlag(res, "onUpgrade", true) {
			drift = true
		}
		if !drift {
			log.Info("import webhook already configured", "service", name)
			return nil
		}
		id, err := resourceID(res)
		if err != nil {
			return fmt.Errorf("update webhook: %w", err)
		}
		if err := c.UpdateNotification(ctx, id, res); err != nil {
			return fmt.Errorf("update webhook: %w", err)
		}
		log.Info("updated import webhook (drift corrected)", "service", name, "url", hookURL)
		return nil
	}

	fields := []fieldSpec{
		{name: "url", value: hookURL},
		{name: "method", value: webhookPOST},
		{name: "username", value: ""},
		{name: "password", value: ""},
	}
	if secret != "" {
		fields = append(fields, fieldSpec{name: "headers", value: secretHeaders(secret)})
	} else {
		log.Warn("WEBHOOK_SECRET is empty, registering webhook without a secret header", "service", name)
	}
	payload := map[string]any{
		"name":                webhookName,
		"implementation":      "Webhook",
		"configContract":      "WebhookSettings",
		"onGrab":              false,
		"onDownload":          true,
		"onUpgrade":           true,
		"onHealthIssue":       false,
		"onApplicationUpdate": false,
		"fields":              fieldList(fields...),
	}
	if err := c.AddNotification(ctx, payload); err != nil {
		return fmt.Errorf("add webhook: %w", err)
	}
	log.Info("added import webhook", "service", name, "url", hookURL)
	return nil
}

func secretHeaders(secret string) []map[string]string {
	return []map[string]string{{"key": webhookHeader, "value": secret}}
}

// headerSecret returns the X-Webhook-Secret value in a notification's headers
// field, or "" when there is none.
func headerSecret(res map[string]any) string {
	v, _ := fieldValue(res, "headers")
	headers, _ := v.([]any)
	for _, h := range headers {
		hm, _ := h.(map[string]any)
		if k, _ := hm["key"].(string); strings.EqualFold(k, webhookHeader) {
			s, _ := hm["value"].(string)
			return s
		}
	}
	return ""
}

// wireProwlarrApp ensures Prowlarr has an application entry for Sonarr or
// Radarr (appName) that syncs indexers to it.
func wireProwlarrApp(ctx context.Context, log *slog.Logger, prowlarr ArrClient, appName, prowlarrURL, appURL, appKey string) error {
	if appKey == "" {
		return fmt.Errorf("no %s API key available", appName)
	}
	existing, err := prowlarr.ListApplications(ctx)
	if err != nil {
		return fmt.Errorf("list applications: %w", err)
	}

	for _, e := range existing {
		if n, _ := e["name"].(string); n != appName {
			continue
		}
		res, err := clone(e)
		if err != nil {
			return fmt.Errorf("copy application: %w", err)
		}
		drift := false
		for _, s := range []struct {
			name, want string
			norm       func(string) string
		}{
			{"prowlarrUrl", prowlarrURL, normalizeURL},
			{"baseUrl", appURL, normalizeURL},
			{"apiKey", appKey, func(s string) string { return s }},
		} {
			got, ok := fieldValue(res, s.name)
			gs, _ := got.(string)
			if ok && s.norm(gs) == s.norm(s.want) {
				continue
			}
			setField(res, s.name, s.want)
			drift = true
		}
		if !drift {
			log.Info("Prowlarr application already connected", "app", appName)
			return nil
		}
		id, err := resourceID(res)
		if err != nil {
			return fmt.Errorf("update application %s: %w", appName, err)
		}
		if err := prowlarr.UpdateApplication(ctx, id, res); err != nil {
			return fmt.Errorf("update application %s: %w", appName, err)
		}
		log.Info("updated Prowlarr application (stale url or key)", "app", appName)
		return nil
	}

	payload := map[string]any{
		"name":           appName,
		"implementation": appName,
		"configContract": appName + "Settings",
		"syncLevel":      "fullSync",
		"fields": fieldList(
			fieldSpec{name: "prowlarrUrl", value: prowlarrURL},
			fieldSpec{name: "baseUrl", value: appURL},
			fieldSpec{name: "apiKey", value: appKey},
		),
	}
	if err := prowlarr.AddApplication(ctx, payload); err != nil {
		return fmt.Errorf("add application %s: %w", appName, err)
	}
	log.Info("connected Prowlarr application", "app", appName)
	return nil
}

// normalizeURL lowercases scheme and host and strips trailing slashes so URL
// comparisons ignore the normalisation the *arr apps apply.
func normalizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return strings.TrimRight(raw, "/")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String()
}

func samePath(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}
