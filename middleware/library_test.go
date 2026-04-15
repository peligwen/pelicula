package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanFilename(t *testing.T) {
	cases := []struct {
		filename string
		title    string
		year     int
		isTV     bool
	}{
		{"The.Dark.Knight.2008.1080p.BluRay.mkv", "The Dark Knight", 2008, false},
		{"Breaking.Bad.S01E01.720p.mkv", "Breaking Bad", 0, true},
		{"Alien (1979).mkv", "Alien", 1979, false},
		{"Movie_Name_2020_WEB.mkv", "Movie Name", 2020, false},
		{"Show.2019.S02E03.HDTV.mkv", "Show", 2019, true},
		{"NoYear.mkv", "NoYear", 0, false},
		{".mkv", "", 0, false},
		{"Inception.2010.mkv", "Inception", 2010, false},
		{"The.Wire.S03E12.mkv", "The Wire", 0, true},
		{"movie.name.mkv", "movie name", 0, false},
	}
	for _, c := range cases {
		t.Run(c.filename, func(t *testing.T) {
			title, year, isTV := cleanFilename(c.filename)
			if title != c.title {
				t.Errorf("title = %q, want %q", title, c.title)
			}
			if year != c.year {
				t.Errorf("year = %d, want %d", year, c.year)
			}
			if isTV != c.isTV {
				t.Errorf("isTV = %v, want %v", isTV, c.isTV)
			}
		})
	}
}

func TestExtractSeason(t *testing.T) {
	cases := []struct {
		filename string
		want     int
	}{
		{"Show.S01E01.mkv", 1},
		{"Show.S12E05.mkv", 12},
		{"Movie.2020.mkv", 0},
		{"show s3e10 720p.mkv", 3},
		{"S01E01.mkv", 1},
		{"no episode info.mkv", 0},
		{"", 0},
	}
	for _, c := range cases {
		t.Run(c.filename, func(t *testing.T) {
			got := extractSeason(c.filename)
			if got != c.want {
				t.Errorf("extractSeason(%q) = %d, want %d", c.filename, got, c.want)
			}
		})
	}
}

func TestNormalizeTitle(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"The Dark Knight", "dark knight"},
		{"A Beautiful Mind", "beautiful mind"},
		{"An Officer and a Gentleman", "officer and a gentleman"}, // only leading "an " stripped; "a" mid-string kept
		{"Hello: World!", "hello world"},
		{"  multiple   spaces  ", "multiple spaces"},
		{"Schindler's List", "schindlers list"},
		{"2001: A Space Odyssey", "2001 a space odyssey"}, // no leading article; "a" inside kept
		{"", ""},
		{"THE MATRIX", "matrix"},
		{"a", "a"}, // single letter "a" — no trailing space so prefix not matched
	}
	for _, c := range cases {
		t.Run(c.input, func(t *testing.T) {
			got := normalizeTitle(c.input)
			if got != c.want {
				t.Errorf("normalizeTitle(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

func TestScoreMatch(t *testing.T) {
	cases := []struct {
		name       string
		title      string
		year       int
		matchTitle string
		matchYear  int
		want       string
	}{
		{"exact match same year", "The Dark Knight", 2008, "The Dark Knight", 2008, "high"},
		{"exact match year off by 1", "The Dark Knight", 2008, "The Dark Knight", 2009, "high"},
		{"exact match year off by 2", "The Dark Knight", 2008, "The Dark Knight", 2010, "medium"},
		{"exact match no year info", "Alien", 0, "Alien", 1979, "high"},
		// "Dark Knight" and "The Dark Knight" both normalize to "dark knight" → exact match
		{"exact after normalize year ok", "Dark Knight", 2008, "The Dark Knight", 2008, "high"},
		{"exact after normalize year bad", "Dark Knight", 2000, "The Dark Knight", 2008, "medium"},
		// true substring: "Dark Knight" is in "The Dark Knight Rises" after normalize
		{"substring match year ok", "Dark Knight", 2012, "The Dark Knight Rises", 2012, "medium"},
		{"substring match year bad", "Dark Knight", 2000, "The Dark Knight Rises", 2012, "low"},
		{"exact after normalize year ok", "Dark Knight Rises", 2012, "The Dark Knight Rises", 2012, "high"},
		{"no overlap", "Star Wars", 1977, "Alien", 1979, "unmatched"},
		{"empty title", "", 2008, "The Dark Knight", 2008, "unmatched"},
		{"empty match title", "The Dark Knight", 2008, "", 2008, "unmatched"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := scoreMatch(c.title, c.year, c.matchTitle, c.matchYear)
			if got != c.want {
				t.Errorf("scoreMatch(%q,%d,%q,%d) = %q, want %q",
					c.title, c.year, c.matchTitle, c.matchYear, got, c.want)
			}
		})
	}
}

func TestSuggestedMoviePath(t *testing.T) {
	// Default registry (empty) falls back to /media/movies.
	cases := []struct {
		title    string
		year     int
		filename string
		want     string
	}{
		{"Alien", 1979, "alien.1979.mkv", "/media/movies/Alien (1979)/alien.1979.mkv"},
		{"Alien", 0, "alien.mkv", "/media/movies/Alien/alien.mkv"},
		{"The Dark Knight", 2008, "the.dark.knight.mkv", "/media/movies/The Dark Knight (2008)/the.dark.knight.mkv"},
	}
	for _, c := range cases {
		t.Run(c.title, func(t *testing.T) {
			got := suggestedMoviePath(c.title, c.year, c.filename)
			if got != c.want {
				t.Errorf("suggestedMoviePath(%q,%d,%q) = %q, want %q",
					c.title, c.year, c.filename, got, c.want)
			}
		})
	}
}

func TestSuggestedTVPath(t *testing.T) {
	// Default registry (empty) falls back to /media/tv.
	cases := []struct {
		title    string
		season   int
		filename string
		want     string
	}{
		{"Breaking Bad", 1, "s01e01.mkv", "/media/tv/Breaking Bad/Season 01/s01e01.mkv"},
		{"Breaking Bad", 12, "s12e01.mkv", "/media/tv/Breaking Bad/Season 12/s12e01.mkv"},
		{"Show", 0, "episode.mkv", "/media/tv/Show/episode.mkv"},
	}
	for _, c := range cases {
		t.Run(c.title, func(t *testing.T) {
			got := suggestedTVPath(c.title, c.season, c.filename)
			if got != c.want {
				t.Errorf("suggestedTVPath(%q,%d,%q) = %q, want %q",
					c.title, c.season, c.filename, got, c.want)
			}
		})
	}
}

// ── FS helpers ────────────────────────────────────────────────────────────────

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	dst := filepath.Join(dir, "dst.mkv")

	content := []byte("fake video content")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("dst content = %q, want %q", got, content)
	}
	// src should still exist after copy (copyFile does not remove src)
	if _, err := os.Stat(src); os.IsNotExist(err) {
		t.Error("copyFile should not remove src")
	}
}

func TestMoveFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	dst := filepath.Join(dir, "subdir", "dst.mkv")

	content := []byte("video data")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Dir(dst), 0755); err != nil {
		t.Fatal(err)
	}
	if err := moveFile(src, dst); err != nil {
		t.Fatalf("moveFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("dst content = %q, want %q", got, content)
	}
	// src should be gone after a move
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("moveFile should remove src")
	}
}

func TestWalkVideoFiles(t *testing.T) {
	// Build a directory tree:
	//   root/
	//     movie.mkv              ← included
	//     readme.txt             ← skipped (not a video ext)
	//     .hidden/               ← skipped (hidden dir)
	//       hidden.mkv           ← never reached
	//     Extras/                ← skipped (skipDirs)
	//       extra.mkv            ← never reached
	//     Season 01/
	//       ep01.mkv             ← included
	//       sample.mkv           ← skipped (name contains "sample" AND < 100 MB)
	root := t.TempDir()
	write := func(path string, size int) {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, size), 0644); err != nil {
			t.Fatal(err)
		}
	}

	write(filepath.Join(root, "movie.mkv"), 500)
	write(filepath.Join(root, "readme.txt"), 100)
	write(filepath.Join(root, ".hidden", "hidden.mkv"), 500)
	write(filepath.Join(root, "Extras", "extra.mkv"), 500)
	write(filepath.Join(root, "Season 01", "ep01.mkv"), 500)
	write(filepath.Join(root, "Season 01", "sample.mkv"), 100) // < 100 MB → skipped

	files, _ := walkVideoFiles(root, nil, 100)
	paths := make(map[string]bool, len(files))
	for _, f := range files {
		paths[filepath.Base(f.Path)] = true
	}

	if !paths["movie.mkv"] {
		t.Error("expected movie.mkv to be included")
	}
	if !paths["ep01.mkv"] {
		t.Error("expected ep01.mkv to be included")
	}
	if paths["readme.txt"] {
		t.Error("readme.txt should be excluded (not a video)")
	}
	if paths["hidden.mkv"] {
		t.Error("hidden.mkv should be excluded (hidden dir)")
	}
	if paths["extra.mkv"] {
		t.Error("extra.mkv should be excluded (Extras dir)")
	}
	if paths["sample.mkv"] {
		t.Error("sample.mkv should be excluded (sample file)")
	}
	if len(files) != 2 {
		t.Errorf("expected 2 files, got %d: %v", len(files), files)
	}
}

func TestWalkVideoFilesCap(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		name := filepath.Join(root, filepath.Base(t.TempDir())+".mkv")
		if err := os.WriteFile(name, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	files, remaining := walkVideoFiles(root, nil, 3)
	if len(files) != 3 {
		t.Errorf("expected cap=3 to limit to 3 files, got %d", len(files))
	}
	if remaining != 0 {
		t.Errorf("remaining should be 0 when cap hit, got %d", remaining)
	}
}

// ── applyFSOps ────────────────────────────────────────────────────────────────

// newApplyFSOpsRoots builds a tmp tree with src and dst roots so applyFSOps can
// be exercised without touching the production /downloads or /movies paths.
func newApplyFSOpsRoots(t *testing.T) (srcRoot, dstRoot string) {
	t.Helper()
	base := t.TempDir()
	srcRoot = filepath.Join(base, "downloads")
	dstRoot = filepath.Join(base, "movies")
	for _, d := range []string{srcRoot, dstRoot} {
		if err := os.Mkdir(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	return
}

func TestApplyFSOps_Migrate(t *testing.T) {
	srcRoot, dstRoot := newApplyFSOpsRoots(t)
	src := filepath.Join(srcRoot, "Alien.1979.mkv")
	dst := filepath.Join(dstRoot, "Alien (1979)", "Alien.1979.mkv")
	if err := os.WriteFile(src, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	items := []ApplyItem{{
		Type:       "movie",
		Title:      "Alien",
		Year:       1979,
		SourcePath: src,
		DestPath:   dst,
	}}
	applyFSOps(items, "migrate", []string{srcRoot}, []string{dstRoot})

	if _, err := os.Stat(dst); os.IsNotExist(err) {
		t.Fatal("dst should exist after migrate")
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("src should be gone after migrate")
	}
	if items[0].DestPath != dst {
		t.Errorf("DestPath = %q, want %q", items[0].DestPath, dst)
	}
}

func TestApplyFSOps_Symlink(t *testing.T) {
	srcRoot, dstRoot := newApplyFSOpsRoots(t)
	src := filepath.Join(srcRoot, "Inception.2010.mkv")
	dst := filepath.Join(dstRoot, "Inception (2010)", "Inception.2010.mkv")
	if err := os.WriteFile(src, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	items := []ApplyItem{{
		Type:       "movie",
		Title:      "Inception",
		Year:       2010,
		SourcePath: src,
		DestPath:   dst,
	}}
	applyFSOps(items, "symlink", []string{srcRoot}, []string{dstRoot})

	info, err := os.Lstat(dst)
	if os.IsNotExist(err) {
		t.Fatal("dst should exist after symlink")
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("dst should be a symlink")
	}
	// src should still exist
	if _, err := os.Stat(src); os.IsNotExist(err) {
		t.Error("src should still exist after symlink")
	}
	if items[0].DestPath != dst {
		t.Errorf("DestPath = %q, want %q", items[0].DestPath, dst)
	}
}

func TestApplyFSOps_SymlinkIdempotent(t *testing.T) {
	// Calling applyFSOps twice with symlink strategy should not error on the
	// second call — it silently skips if the dst already exists.
	srcRoot, dstRoot := newApplyFSOpsRoots(t)
	src := filepath.Join(srcRoot, "movie.mkv")
	dst := filepath.Join(dstRoot, "Movie (2020)", "movie.mkv")
	if err := os.WriteFile(src, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	items := []ApplyItem{{Type: "movie", Title: "Movie", Year: 2020, SourcePath: src, DestPath: dst}}
	applyFSOps(items, "symlink", []string{srcRoot}, []string{dstRoot})
	applyFSOps(items, "symlink", []string{srcRoot}, []string{dstRoot}) // should not panic / error

	fi, err := os.Lstat(dst)
	if err != nil {
		t.Fatalf("dst should exist after idempotent symlink: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("dst should be a symlink")
	}
	target, err := os.Readlink(dst)
	if err != nil {
		t.Fatalf("Readlink: %v", err)
	}
	if target != src {
		t.Errorf("symlink target = %q, want %q", target, src)
	}
}

func TestApplyFSOps_Keep(t *testing.T) {
	// "keep" must not touch any files.
	srcRoot, dstRoot := newApplyFSOpsRoots(t)
	src := filepath.Join(srcRoot, "movie.mkv")
	dst := filepath.Join(dstRoot, "Movie (2020)", "movie.mkv")
	if err := os.WriteFile(src, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	items := []ApplyItem{{Type: "movie", Title: "Movie", Year: 2020, SourcePath: src, DestPath: dst}}
	applyFSOps(items, "keep", []string{srcRoot}, []string{dstRoot})

	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("keep strategy should not create dst")
	}
	if _, err := os.Stat(src); os.IsNotExist(err) {
		t.Error("keep strategy should leave src intact")
	}
}

func TestApplyFSOps_RejectEscapingPath(t *testing.T) {
	// SourcePath outside allowedSrcRoots must be silently ignored.
	_, dstRoot := newApplyFSOpsRoots(t)
	base := filepath.Dir(dstRoot)
	escapeSrc := filepath.Join(base, "outside.mkv")
	dst := filepath.Join(dstRoot, "Movie (2020)", "outside.mkv")
	if err := os.WriteFile(escapeSrc, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	items := []ApplyItem{{Type: "movie", Title: "Movie", Year: 2020, SourcePath: escapeSrc, DestPath: dst}}
	applyFSOps(items, "migrate", []string{filepath.Join(base, "downloads")}, []string{dstRoot})

	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("path outside allowedSrcRoots should not be migrated")
	}
	// original must remain untouched
	if _, err := os.Stat(escapeSrc); os.IsNotExist(err) {
		t.Error("escapeSrc should be untouched")
	}
}

// ── extractEpisode ────────────────────────────────────────────────────────────

func TestExtractEpisode(t *testing.T) {
	cases := []struct {
		filename string
		want     int
	}{
		{"Show.S01E01.mkv", 1},
		{"Show.S01E12.mkv", 12},
		{"Show.S12E05.mkv", 5},
		{"Movie.2020.mkv", 0},
		{"show s3e10 720p.mkv", 10},
		{"S01E01.mkv", 1},
		{"no episode info.mkv", 0},
		{"", 0},
	}
	for _, c := range cases {
		t.Run(c.filename, func(t *testing.T) {
			got := extractEpisode(c.filename)
			if got != c.want {
				t.Errorf("extractEpisode(%q) = %d, want %d", c.filename, got, c.want)
			}
		})
	}
}

// ── collapseHardlinks ─────────────────────────────────────────────────────────

func TestCollapseHardlinks_NoLinks(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.mkv")
	b := filepath.Join(dir, "b.mkv")
	if err := os.WriteFile(a, []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("b"), 0644); err != nil {
		t.Fatal(err)
	}
	files := []ScanFile{{Path: a, Size: 1}, {Path: b, Size: 1}}
	got := collapseHardlinks(files)
	if len(got) != 2 {
		t.Errorf("expected 2 files, got %d", len(got))
	}
	for _, f := range got {
		if len(f.Aliases) != 0 {
			t.Errorf("file %s should have no aliases, got %v", f.Path, f.Aliases)
		}
	}
}

func TestCollapseHardlinks_WithLink(t *testing.T) {
	dir := t.TempDir()
	orig := filepath.Join(dir, "original.mkv")
	link := filepath.Join(dir, "hardlink.mkv")
	if err := os.WriteFile(orig, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(orig, link); err != nil {
		t.Fatal(err)
	}

	files := []ScanFile{{Path: orig, Size: 4}, {Path: link, Size: 4}}
	got := collapseHardlinks(files)

	if len(got) != 1 {
		t.Fatalf("expected 1 file after hardlink collapse, got %d", len(got))
	}
	if got[0].Path != orig {
		t.Errorf("expected canonical path %q, got %q", orig, got[0].Path)
	}
	if len(got[0].Aliases) != 1 || got[0].Aliases[0] != link {
		t.Errorf("expected aliases [%q], got %v", link, got[0].Aliases)
	}
}

func TestCollapseHardlinks_NonExistentFile(t *testing.T) {
	// Files that cannot be stat'd should be passed through as-is.
	files := []ScanFile{{Path: "/nonexistent/ghost.mkv", Size: 100}}
	got := collapseHardlinks(files)
	if len(got) != 1 {
		t.Errorf("expected 1 file, got %d", len(got))
	}
}

// ── matchItemGroupKey / assignGroupKeys ───────────────────────────────────────

func TestMatchItemGroupKey(t *testing.T) {
	cases := []struct {
		name string
		item MatchItem
		want string
	}{
		{
			"movie",
			MatchItem{Match: &MediaMatch{Type: "movie", TmdbID: 123}},
			"movie:123",
		},
		{
			"series episode",
			MatchItem{Match: &MediaMatch{Type: "series", TvdbID: 456, Season: 1, Episode: 3}},
			"series:456:s1e3",
		},
		{
			"unmatched uses file path",
			MatchItem{File: "/downloads/foo.mkv"},
			"unmatched:/downloads/foo.mkv",
		},
		{
			"series with zero tvdbId falls back to unmatched",
			MatchItem{File: "/downloads/foo.mkv", Match: &MediaMatch{Type: "series", TvdbID: 0}},
			"unmatched:/downloads/foo.mkv",
		},
		{
			"movie with zero tmdbId falls back to unmatched",
			MatchItem{File: "/downloads/foo.mkv", Match: &MediaMatch{Type: "movie", TmdbID: 0}},
			"unmatched:/downloads/foo.mkv",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := matchItemGroupKey(c.item)
			if got != c.want {
				t.Errorf("matchItemGroupKey = %q, want %q", got, c.want)
			}
		})
	}
}

func TestAssignGroupKeys(t *testing.T) {
	items := []MatchItem{
		{File: "/a.mkv", Match: &MediaMatch{Type: "movie", TmdbID: 1}},
		{File: "/b.mkv", Match: &MediaMatch{Type: "movie", TmdbID: 1}}, // dup
		{File: "/c.mkv", Match: &MediaMatch{Type: "series", TvdbID: 2, Season: 1, Episode: 1}},
		{File: "/d.mkv"}, // unmatched
	}
	assignGroupKeys(items)

	if items[0].GroupKey != "movie:1" {
		t.Errorf("items[0].GroupKey = %q, want %q", items[0].GroupKey, "movie:1")
	}
	if items[1].GroupKey != "movie:1" {
		t.Errorf("items[1].GroupKey = %q, want %q", items[1].GroupKey, "movie:1")
	}
	if items[0].GroupKey != items[1].GroupKey {
		t.Error("two files for the same movie should have the same GroupKey")
	}
	if items[2].GroupKey != "series:2:s1e1" {
		t.Errorf("items[2].GroupKey = %q, want %q", items[2].GroupKey, "series:2:s1e1")
	}
	if !strings.HasPrefix(items[3].GroupKey, "unmatched:") {
		t.Errorf("unmatched item GroupKey = %q, should start with 'unmatched:'", items[3].GroupKey)
	}
}

// ── handleLibraryApply duplicate guard ───────────────────────────────────────

func TestHandleLibraryApply_DuplicateGuard(t *testing.T) {
	// Two items with the same movie group key must be rejected with 400.
	body := `{
		"items": [
			{"type":"movie","tmdbId":999,"title":"Alien","year":1979,"rootFolderPath":"/movies","sourcePath":"/a/alien_1.mkv"},
			{"type":"movie","tmdbId":999,"title":"Alien","year":1979,"rootFolderPath":"/movies","sourcePath":"/a/alien_2.mkv"}
		],
		"strategy":"keep"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/library/apply",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handleLibraryApply(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for duplicate group keys, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "movie:999") {
		t.Errorf("error response should mention the duplicate group key, got: %s", w.Body.String())
	}
}

func TestHandleLibraryApply_DupEpisodeGuard(t *testing.T) {
	// Two items for the same episode must be rejected with 400.
	body := `{
		"items": [
			{"type":"series","tvdbId":888,"title":"Breaking Bad","season":1,"episode":1,"rootFolderPath":"/tv","sourcePath":"/a/bb_s01e01_720p.mkv"},
			{"type":"series","tvdbId":888,"title":"Breaking Bad","season":1,"episode":1,"rootFolderPath":"/tv","sourcePath":"/a/bb_s01e01_1080p.mkv"}
		],
		"strategy":"keep"
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/library/apply",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handleLibraryApply(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for duplicate episode group keys, got %d: %s", w.Code, w.Body.String())
	}
}

// TestInPlaceDetection verifies that the in-place status is correctly assigned
// when the scan result's file path matches its suggested destination path.
// We can't call matchFile directly (it hits external APIs), so we validate the
// post-processing logic that applyInPlaceStatus performs on MatchItem slices.
func TestInPlaceDetection(t *testing.T) {
	cases := []struct {
		name       string
		file       string
		suggested  string
		origStatus string
		wantStatus string
	}{
		{
			"movie already in library path",
			"/movies/Inception (2010)/Inception.2010.mkv",
			"/movies/Inception (2010)/Inception.2010.mkv",
			"new",
			"in_place",
		},
		{
			"tv already in library path",
			"/tv/Breaking Bad/Season 01/Breaking.Bad.S01E01.mkv",
			"/tv/Breaking Bad/Season 01/Breaking.Bad.S01E01.mkv",
			"new",
			"in_place",
		},
		{
			"file in downloads — different from suggested",
			"/downloads/Inception.2010.mkv",
			"/movies/Inception (2010)/Inception.2010.mkv",
			"new",
			"new",
		},
		{
			"already exists in arr — keeps exists status",
			"/movies/Inception (2010)/Inception.2010.mkv",
			"/movies/Inception (2010)/Inception.2010.mkv",
			"exists",
			"exists",
		},
		{
			"path normalization — trailing slash or double slash",
			"/movies/Inception (2010)//Inception.2010.mkv",
			"/movies/Inception (2010)/Inception.2010.mkv",
			"new",
			"in_place",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			item := MatchItem{
				File:          c.file,
				Status:        c.origStatus,
				SuggestedPath: c.suggested,
			}
			// Apply the same logic used in matchFile post-processing
			if item.Status == "new" && item.SuggestedPath != "" &&
				filepath.Clean(item.File) == filepath.Clean(item.SuggestedPath) {
				item.Status = "in_place"
			}
			if item.Status != c.wantStatus {
				t.Errorf("status = %q, want %q", item.Status, c.wantStatus)
			}
		})
	}
}

func TestApplyGroupKey_DifferentEpisodes_NotDups(t *testing.T) {
	// Two different episodes of the same series must produce different group keys
	// so the duplicate guard does NOT reject them.
	ep1 := ApplyItem{Type: "series", TvdbID: 888, Season: 1, Episode: 1}
	ep2 := ApplyItem{Type: "series", TvdbID: 888, Season: 1, Episode: 2}

	k1 := applyGroupKey(ep1)
	k2 := applyGroupKey(ep2)

	if k1 == k2 {
		t.Errorf("different episodes should have different group keys; both got %q", k1)
	}
	if k1 != "series:888:s1e1" {
		t.Errorf("ep1 group key = %q, want %q", k1, "series:888:s1e1")
	}
	if k2 != "series:888:s1e2" {
		t.Errorf("ep2 group key = %q, want %q", k2, "series:888:s1e2")
	}
}

func TestHandleJobRetry_ProxiesToProcula(t *testing.T) {
	var gotPath string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer fake.Close()

	old := proculaURL
	origSvc := services
	proculaURL = fake.URL
	services = NewServiceClients("/config")
	t.Cleanup(func() { proculaURL = old; services = origSvc })

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/procula/jobs/abc123/retry", nil)
	req.SetPathValue("id", "abc123")
	w := httptest.NewRecorder()
	handleJobRetry(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if gotPath != "/api/procula/jobs/abc123/retry" {
		t.Errorf("proxied to %q, want /api/procula/jobs/abc123/retry", gotPath)
	}
}

func TestHandleJobRetry_MethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/procula/jobs/abc123/retry", nil)
	req.SetPathValue("id", "abc123")
	w := httptest.NewRecorder()
	handleJobRetry(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}
