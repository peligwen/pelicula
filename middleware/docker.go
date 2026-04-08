package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// dockerHost returns the Docker Engine API base URL.
// Reads DOCKER_HOST env; defaults to the docker-socket-proxy sidecar.
func dockerHost() string {
	if h := strings.TrimSpace(os.Getenv("DOCKER_HOST")); h != "" {
		return h
	}
	return "http://docker-proxy:2375"
}

var dockerClient = &http.Client{Timeout: 30 * time.Second}

// allowedContainers is the explicit whitelist of container names the admin
// endpoints may act on.  Defense-in-depth on top of the docker-socket-proxy
// allowlist.
var allowedContainers = map[string]bool{
	"nginx":        true,
	"pelicula-api": true,
	"procula":      true,
	"sonarr":       true,
	"radarr":       true,
	"prowlarr":     true,
	"qbittorrent":  true,
	"jellyfin":     true,
	"bazarr":       true,
	"gluetun":      true,
}

// isAllowedContainer reports whether name is in the allowlist.
func isAllowedContainer(name string) bool {
	return allowedContainers[name]
}

// dockerRestart sends a restart request to the Docker Engine API for the
// named container (POST /containers/{name}/restart?t=5).
func dockerRestart(name string) error {
	url := dockerHost() + "/containers/" + name + "/restart?t=5"
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		return err
	}
	resp, err := dockerClient.Do(req)
	if err != nil {
		return fmt.Errorf("docker restart %s: %w (is the Docker socket proxy reachable?)", name, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("docker restart %s: HTTP %d", name, resp.StatusCode)
	}
	return nil
}

// dockerLogs fetches the last tail lines of stdout+stderr for a container.
// Docker multiplexes streams using an 8-byte framing header when the container
// has no TTY (our case); we strip those headers and return raw log bytes.
func dockerLogs(name string, tail int) ([]byte, error) {
	if tail <= 0 || tail > 500 {
		tail = 200
	}
	url := fmt.Sprintf("%s/containers/%s/logs?stdout=1&stderr=1&tail=%d&timestamps=0",
		dockerHost(), name, tail)
	resp, err := dockerClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("docker logs %s: %w (is the Docker socket proxy reachable?)", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("docker logs %s: HTTP %d", name, resp.StatusCode)
	}
	return demuxDockerLogs(resp.Body)
}

// demuxDockerLogs strips the Docker stream multiplexing headers and returns
// the concatenated log payload.
//
// Frame format: [stream(1), 0,0,0, size(4 BE)] followed by `size` payload bytes.
// stream: 1=stdout, 2=stderr (we include both).
//
// Total output is capped at maxLogBytes to bound memory use against a
// misbehaving proxy or unexpectedly large frames.
const maxLogBytes = 5 << 20 // 5 MiB

func demuxDockerLogs(r io.Reader) ([]byte, error) {
	var out []byte
	hdr := make([]byte, 8)
	for {
		_, err := io.ReadFull(r, hdr)
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return out, err
		}
		size := binary.BigEndian.Uint32(hdr[4:8])
		if size == 0 {
			continue
		}
		if len(out)+int(size) > maxLogBytes {
			break
		}
		chunk := make([]byte, size)
		if _, err := io.ReadFull(r, chunk); err != nil {
			break
		}
		out = append(out, chunk...)
	}
	return out, nil
}
