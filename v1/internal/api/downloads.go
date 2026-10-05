package api

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// Torrent hashes are 40 (v1) or 64 (v2) hex characters. Anything else is
// refused: qBittorrent treats "all" and "a|b" as multi-torrent selectors, and
// the delete endpoint also removes files.
var hashRe = regexp.MustCompile(`^[0-9a-fA-F]{1,64}$`)

func (s *Server) handleListDownloads(w http.ResponseWriter, r *http.Request) {
	if s.QBT == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"vpn":       false,
			"downloads": []Torrent{},
			"transfer":  TransferInfo{},
		})
		return
	}
	torrents, err := s.QBT.ListTorrents(r.Context())
	if err != nil {
		s.log().Warn("list torrents", "err", err)
		writeError(w, http.StatusBadGateway, "qBittorrent is unreachable")
		return
	}
	if torrents == nil {
		torrents = []Torrent{}
	}
	var transfer TransferInfo
	if ti, err := s.QBT.GetTransferInfo(r.Context()); err != nil {
		s.log().Warn("transfer info", "err", err)
	} else if ti != nil {
		transfer = *ti
	}
	writeJSON(w, http.StatusOK, map[string]any{"vpn": true, "downloads": torrents, "transfer": transfer})
}

func (s *Server) handlePauseDownload(w http.ResponseWriter, r *http.Request) {
	s.torrentAction(w, r, func(ctx context.Context, hash string) error { return s.QBT.StopTorrent(ctx, hash) })
}

func (s *Server) handleResumeDownload(w http.ResponseWriter, r *http.Request) {
	s.torrentAction(w, r, func(ctx context.Context, hash string) error { return s.QBT.StartTorrent(ctx, hash) })
}

func (s *Server) torrentAction(w http.ResponseWriter, r *http.Request, act func(ctx context.Context, hash string) error) {
	hash, ok := s.downloadHash(w, r)
	if !ok {
		return
	}
	if s.QBT == nil {
		writeError(w, http.StatusServiceUnavailable, "downloads are unavailable without the VPN profile")
		return
	}
	if err := act(r.Context(), hash); err != nil {
		s.log().Warn("torrent action", "hash", hash, "err", err)
		writeError(w, http.StatusBadGateway, "qBittorrent request failed")
		return
	}
	noContent(w)
}

// handleDeleteDownload removes a download. When Radarr or Sonarr is tracking
// it, the removal goes through their queue so the release can be blocklisted
// and the *arr state stays consistent; otherwise the torrent and its files are
// deleted straight from qBittorrent.
func (s *Server) handleDeleteDownload(w http.ResponseWriter, r *http.Request) {
	hash, ok := s.downloadHash(w, r)
	if !ok {
		return
	}
	blocklist, _ := strconv.ParseBool(r.URL.Query().Get("blocklist"))
	ctx := r.Context()

	for _, c := range []struct {
		name   string
		client ArrClient
	}{{"radarr", s.Radarr}, {"sonarr", s.Sonarr}} {
		if c.client == nil {
			continue
		}
		records, err := c.client.GetAllQueueRecords(ctx)
		if err != nil {
			s.log().Warn("queue lookup failed", "service", c.name, "err", err)
			continue
		}
		for _, rec := range records {
			if !strings.EqualFold(str(rec, "downloadId"), hash) {
				continue
			}
			if err := c.client.DeleteQueueItem(ctx, num(rec, "id"), true, blocklist); err != nil {
				s.log().Warn("delete queue item", "service", c.name, "err", err)
				writeError(w, http.StatusBadGateway, c.name+" request failed")
				return
			}
			noContent(w)
			return
		}
	}

	if s.QBT == nil {
		writeError(w, http.StatusNotFound, "download not found")
		return
	}
	if err := s.QBT.DeleteTorrent(ctx, hash, true); err != nil {
		s.log().Warn("delete torrent", "hash", hash, "err", err)
		writeError(w, http.StatusBadGateway, "qBittorrent request failed")
		return
	}
	noContent(w)
}

func (s *Server) downloadHash(w http.ResponseWriter, r *http.Request) (string, bool) {
	hash := r.PathValue("hash")
	if !hashRe.MatchString(hash) {
		writeError(w, http.StatusBadRequest, "invalid download hash")
		return "", false
	}
	return hash, true
}
