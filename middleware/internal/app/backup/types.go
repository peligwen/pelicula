package backup

import "pelicula-api/internal/peligrosa"

// currentBackupVersion is the latest supported backup format version.
const currentBackupVersion = 2

// BackupExport is the top-level JSON structure written by HandleExport and
// consumed by HandleImportBackup. The format is stable across versions: v1
// omits the roles/invites/requests fields; v2 includes them.
type BackupExport struct {
	Version         int                       `json:"version"`
	PeliculaVersion string                    `json:"pelicula_version,omitempty"`
	Exported        string                    `json:"exported"`
	Movies          []MovieExport             `json:"movies"`
	Series          []SeriesExport            `json:"series"`
	Roles           []peligrosa.RolesEntry    `json:"roles,omitempty"`
	Invites         []peligrosa.InviteExport  `json:"invites,omitempty"`
	Requests        []peligrosa.RequestExport `json:"requests,omitempty"`
}

// MovieExport holds a single Radarr movie entry.
type MovieExport struct {
	Title          string          `json:"title"`
	Year           int             `json:"year"`
	TmdbID         int             `json:"tmdbId"`
	ImdbID         string          `json:"imdbId,omitempty"`
	Path           string          `json:"path"`
	QualityProfile string          `json:"qualityProfile"`
	Monitored      bool            `json:"monitored"`
	HasFile        bool            `json:"hasFile"`
	Tags           []string        `json:"tags"`
	FileInfo       *FileInfoExport `json:"fileInfo,omitempty"`
}

// SeriesExport holds a single Sonarr series entry.
type SeriesExport struct {
	Title          string         `json:"title"`
	Year           int            `json:"year"`
	TvdbID         int            `json:"tvdbId"`
	TmdbID         int            `json:"tmdbId,omitempty"`
	ImdbID         string         `json:"imdbId,omitempty"`
	Path           string         `json:"path"`
	QualityProfile string         `json:"qualityProfile"`
	Monitored      bool           `json:"monitored"`
	HasFile        bool           `json:"hasFile"`
	Tags           []string       `json:"tags"`
	Seasons        []SeasonExport `json:"seasons"`
}

// SeasonExport holds per-season monitoring state.
type SeasonExport struct {
	SeasonNumber int  `json:"seasonNumber"`
	Monitored    bool `json:"monitored"`
}

// FileInfoExport holds file metadata for a movie.
type FileInfoExport struct {
	RelativePath string `json:"relativePath"`
	Size         int64  `json:"size"`
	Quality      string `json:"quality"`
}

// ImportResult is returned by HandleImportBackup with counts of added/skipped/failed items.
type ImportResult struct {
	MoviesAdded   int      `json:"moviesAdded"`
	MoviesSkipped int      `json:"moviesSkipped"`
	MoviesFailed  int      `json:"moviesFailed"`
	SeriesAdded   int      `json:"seriesAdded"`
	SeriesSkipped int      `json:"seriesSkipped"`
	SeriesFailed  int      `json:"seriesFailed"`
	Errors        []string `json:"errors,omitempty"`
}
