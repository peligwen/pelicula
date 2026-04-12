// jobs.go — /api/pelicula/jobs: a flat, grouped view of every procula job
// across every action type. Used by the dashboard Jobs tab.
package main

import (
	"encoding/json"
	"io"
	"net/http"
	"pelicula-api/httputil"
)

// handleJobsList fetches /api/procula/jobs and groups rows by state.
// The response shape is {groups: {queued: [...], processing: [...], ...}, total}.
func handleJobsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	resp, err := services.client.Get(proculaURL + "/api/procula/jobs")
	if err != nil {
		httputil.WriteError(w, "procula unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var all []map[string]any
	if err := json.Unmarshal(body, &all); err != nil {
		httputil.WriteError(w, "invalid procula response: "+err.Error(), http.StatusBadGateway)
		return
	}
	groups := map[string][]map[string]any{
		"queued":     {},
		"processing": {},
		"completed":  {},
		"failed":     {},
		"cancelled":  {},
	}
	for _, j := range all {
		state, _ := j["state"].(string)
		if _, ok := groups[state]; !ok {
			continue
		}
		groups[state] = append(groups[state], j)
	}
	httputil.WriteJSON(w, map[string]any{
		"groups": groups,
		"total":  len(all),
	})
}
