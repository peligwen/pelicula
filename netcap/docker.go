package main

// docker.go — container IP map built from docker-proxy API calls.
//
// Calls:
//   GET /containers/json?all=0          → list running containers
//   GET /containers/{id}/json           → extract NetworkSettings.Networks.*.IPAddress
//
// qbittorrent and prowlarr run in gluetun's network namespace, so their own
// inspect responses have empty Networks maps. Their traffic appears on gluetun's
// IP. We map gluetun's IP to "gluetun/vpn"; port-based heuristics handle
// sub-service disambiguation (port 6881 = qbittorrent peers).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// containerMap maps IP address → container service name (project prefix stripped).
type containerMap struct {
	mu          sync.RWMutex
	ipToService map[string]string
	gluetunIP   string
	lastFetch   time.Time
}

func newContainerMap() *containerMap {
	return &containerMap{
		ipToService: make(map[string]string),
	}
}

// lookup returns the service name for a given IP. Returns "" if unknown.
// Uses the stale cache if a refresh is in progress.
func (cm *containerMap) lookup(ip string) string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.ipToService[ip]
}

// gluetunIPAddr returns the last known IP of the gluetun container.
func (cm *containerMap) gluetunIPAddr() string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.gluetunIP
}

// refresh queries docker-proxy and rebuilds the map. Safe to call from a
// goroutine. On error, leaves the existing cache intact (stale-on-error).
func (cm *containerMap) refresh(dockerHost, projectName string, client *http.Client) error {
	newMap, gluetunIP, err := fetchContainerMap(dockerHost, projectName, client)
	if err != nil {
		return err
	}

	cm.mu.Lock()
	cm.ipToService = newMap
	cm.gluetunIP = gluetunIP
	cm.lastFetch = time.Now()
	cm.mu.Unlock()
	return nil
}

// containerListItem is a single entry from GET /containers/json.
type containerListItem struct {
	ID    string   `json:"Id"`
	Names []string `json:"Names"`
}

// containerInspect holds the fields we need from GET /containers/{id}/json.
type containerInspect struct {
	Name            string `json:"Name"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

// fetchContainerMap calls docker-proxy and builds the ip→serviceName map.
func fetchContainerMap(dockerHost, projectName string, client *http.Client) (map[string]string, string, error) {
	// Step 1: list running containers.
	listURL := dockerHost + "/containers/json?all=0"
	resp, err := client.Get(listURL)
	if err != nil {
		return nil, "", fmt.Errorf("container list: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, "", fmt.Errorf("container list read: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("container list HTTP %d", resp.StatusCode)
	}

	var list []containerListItem
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, "", fmt.Errorf("container list decode: %w", err)
	}

	result := make(map[string]string, len(list))
	var gluetunIP string

	prefix := projectName + "-"

	for _, item := range list {
		id := item.ID
		// Step 2: inspect each container for its IPs.
		insp, err := inspectContainer(dockerHost, id, client)
		if err != nil {
			// Non-fatal — skip this container.
			continue
		}

		// Derive service name from the container's Name field.
		// Docker Compose names containers "<project>-<service>-<replica>".
		// We strip the prefix and trailing "-N" replica suffix.
		svcName := extractServiceName(insp.Name, prefix)

		// Collect all non-empty IPs from all network attachments.
		for _, netInfo := range insp.NetworkSettings.Networks {
			ip := netInfo.IPAddress
			if ip == "" {
				continue
			}
			result[ip] = svcName
			if svcName == "gluetun" {
				gluetunIP = ip
			}
		}
	}

	// Gluetun's IP covers qbittorrent/prowlarr traffic too. Mark it specially
	// so the classification logic can do port-based disambiguation.
	if gluetunIP != "" {
		result[gluetunIP] = "gluetun/vpn"
	}

	return result, gluetunIP, nil
}

// inspectContainer fetches /containers/{id}/json from docker-proxy.
func inspectContainer(dockerHost, id string, client *http.Client) (*containerInspect, error) {
	url := dockerHost + "/containers/" + id + "/json"
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("inspect HTTP %d", resp.StatusCode)
	}
	var insp containerInspect
	if err := json.Unmarshal(body, &insp); err != nil {
		return nil, err
	}
	return &insp, nil
}

// extractServiceName strips the compose project prefix and replica suffix.
// "/pelicula-sonarr-1" → "sonarr"
// "/pelicula-docker-proxy-1" → "docker-proxy"
func extractServiceName(rawName, prefix string) string {
	name := strings.TrimPrefix(rawName, "/")
	name = strings.TrimPrefix(name, prefix)
	// Trim trailing replica index: "-1", "-2", etc.
	if idx := strings.LastIndex(name, "-"); idx >= 0 {
		suffix := name[idx+1:]
		allDigits := len(suffix) > 0
		for _, c := range suffix {
			if c < '0' || c > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			name = name[:idx]
		}
	}
	return name
}
