package main

// main.go — netcap: outbound TCP connection monitor for the pelicula stack.
//
// Reads /proc/net/tcp and /proc/net/tcp6 on a configurable interval, maps
// source IPs to container names via docker-proxy, resolves remote hostnames,
// and exposes a JSON API at /connections.
//
// Network topology note:
//   - netcap runs with network_mode: host so /proc/net/tcp shows the full
//     host socket table (including all Docker container connections).
//   - On Docker Desktop (macOS), network_mode: host is a no-op and /proc/net/tcp
//     reflects the VM's host namespace, not the Mac's. This is an accepted
//     limitation — netcap is designed for Linux (Synology/bare-metal) deployment.
//   - pelicula-api reaches netcap at http://host.docker.internal:9191.
//   - netcap reaches docker-proxy via the loopback-published 127.0.0.1:2375.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

// config holds all runtime configuration derived from environment variables.
type config struct {
	listen       string
	pollInterval time.Duration
	retention    time.Duration
	dockerHost   string
	projectName  string
}

func loadConfig() config {
	c := config{
		listen:       envOr("NETCAP_LISTEN", "127.0.0.1:9191"),
		dockerHost:   envOr("DOCKER_HOST", "http://127.0.0.1:2375"),
		projectName:  envOr("PELICULA_PROJECT_NAME", "pelicula"),
		pollInterval: parseDuration(envOr("NETCAP_POLL_INTERVAL", "5s"), 5*time.Second),
		retention:    parseDuration(envOr("NETCAP_RETENTION", "1h"), time.Hour),
	}
	return c
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseDuration(s string, def time.Duration) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return def
	}
	return d
}

// withTimeout returns a context with the given timeout. Used by resolveIP.
func withTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

func main() {
	cfg := loadConfig()

	store := newConnStore(cfg.retention)
	cmap := newContainerMap()
	dns := newDNSCache(time.Hour)

	httpClient := &http.Client{Timeout: 10 * time.Second}

	// DNS worker pool: 4 workers, buffered channel of IPs to resolve.
	dnsJobs := make(chan string, 256)
	for i := 0; i < 4; i++ {
		go func() {
			for ip := range dnsJobs {
				// Check cache before doing network I/O.
				if _, ok := dns.get(ip); ok {
					continue
				}
				hostname := resolveIP(ip)
				dns.set(ip, hostname)
				store.updateDestHost(ip, hostname)
			}
		}()
	}

	// Container map refresh: every 30s. Run immediately on startup.
	go func() {
		for {
			if err := cmap.refresh(cfg.dockerHost, cfg.projectName, httpClient); err != nil {
				log.Printf("netcap: container map refresh: %v", err)
			}
			time.Sleep(30 * time.Second)
		}
	}()

	// Eviction loop: every minute.
	go func() {
		for {
			time.Sleep(time.Minute)
			store.evict(time.Now())
		}
	}()

	// Poll loop: read /proc/net/tcp[6] and upsert connections.
	go func() {
		for {
			poll(store, cmap, dns, dnsJobs)
			time.Sleep(cfg.pollInterval)
		}
	}()

	// HTTP server.
	mux := http.NewServeMux()
	mux.HandleFunc("/connections", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		entries := store.snapshot(cmap.gluetunIPAddr())
		apiEntries := make([]apiConnection, 0, len(entries))
		for _, e := range entries {
			apiEntries = append(apiEntries, toAPIConnection(e))
		}
		resp := apiResponse{
			GeneratedAt:      time.Now().UTC(),
			RetentionSeconds: int(cfg.retention.Seconds()),
			Connections:      apiEntries,
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Printf("netcap: encode response: %v", err)
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	log.Printf("netcap: listening on %s (poll=%s retention=%s)", cfg.listen, cfg.pollInterval, cfg.retention)
	if err := http.ListenAndServe(cfg.listen, mux); err != nil {
		log.Fatalf("netcap: server: %v", err)
	}
}

// poll reads /proc/net/tcp and /proc/net/tcp6, upserts ESTABLISHED connections.
func poll(store *connStore, cmap *containerMap, dns *dnsCache, dnsJobs chan<- string) {
	now := time.Now().UTC()
	gluetunIP := cmap.gluetunIPAddr()

	var allEntries []tcpEntry

	// IPv4
	v4, err := parseProcNetTCP("/proc/net/tcp", false)
	if err != nil && !os.IsNotExist(err) {
		log.Printf("netcap: parse /proc/net/tcp: %v", err)
	}
	allEntries = append(allEntries, v4...)

	// IPv6
	v6, err := parseProcNetTCP("/proc/net/tcp6", true)
	if err != nil && !os.IsNotExist(err) {
		log.Printf("netcap: parse /proc/net/tcp6: %v", err)
	}
	allEntries = append(allEntries, v6...)

	for _, te := range allEntries {
		remoteIP := te.RemoteIP.String()

		if shouldFilterRemote(te.RemoteIP) {
			continue
		}

		localIP := te.LocalIP.String()
		container := cmap.lookup(localIP)
		if container == "" {
			continue
		}

		kind := "internet"
		if gluetunIP != "" && localIP == gluetunIP {
			kind = "vpn"
		}

		// Resolve hostname (non-blocking: drop if queue full).
		destHost := remoteIP
		if cached, ok := dns.get(remoteIP); ok {
			destHost = cached
		} else {
			select {
			case dnsJobs <- remoteIP:
			default:
				// Queue full; will retry next poll.
			}
		}

		e := connEntry{
			Kind:      kind,
			Container: container,
			SourceIP:  localIP,
			DestHost:  destHost,
			DestIP:    remoteIP,
			DestPort:  te.RemotePort,
			FirstSeen: now,
			LastSeen:  now,
		}
		store.upsert(e)
	}
}

// apiResponse is the JSON envelope for GET /connections.
type apiResponse struct {
	GeneratedAt      time.Time       `json:"generated_at"`
	RetentionSeconds int             `json:"retention_seconds"`
	Connections      []apiConnection `json:"connections"`
}

// apiConnection is a single entry in the /connections response.
type apiConnection struct {
	Kind      string    `json:"kind"`
	Container string    `json:"container"`
	SourceIP  string    `json:"source_ip"`
	DestHost  string    `json:"dest_host"`
	DestIP    string    `json:"dest_ip"`
	DestPort  int       `json:"dest_port"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
	PeerCount int       `json:"peer_count"`
}

func toAPIConnection(e connEntry) apiConnection {
	return apiConnection{
		Kind:      e.Kind,
		Container: e.Container,
		SourceIP:  e.SourceIP,
		DestHost:  e.DestHost,
		DestIP:    e.DestIP,
		DestPort:  int(e.DestPort),
		FirstSeen: e.FirstSeen.UTC(),
		LastSeen:  e.LastSeen.UTC(),
		PeerCount: e.PeerCount,
	}
}
