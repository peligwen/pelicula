package library

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	proculaclient "pelicula-api/internal/clients/procula"
)

// newProculaClientForTest constructs a procula.Client pointed at a test server URL.
func newProculaClientForTest(baseURL string) *proculaclient.Client {
	return proculaclient.New(baseURL, "")
}

func TestCleanFilename(t *testing.T) {
	t.Parallel()
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
		c := c
		t.Run(c.filename, func(t *testing.T) {
			t.Parallel()
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
	t.Parallel()
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
		c := c
		t.Run(c.filename, func(t *testing.T) {
			t.Parallel()
			got := extractSeason(c.filename)
			if got != c.want {
				t.Errorf("extractSeason(%q) = %d, want %d", c.filename, got, c.want)
			}
		})
	}
}

func TestNormalizeTitle(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input string
		want  string
	}{
		{"The Dark Knight", "dark knight"},
		{"A Beautiful Mind", "beautiful mind"},
		{"An Officer and a Gentleman", "officer and a gentleman"},
		{"Hello: World!", "hello world"},
		{"  multiple   spaces  ", "multiple spaces"},
		{"Schindler's List", "schindlers list"},
		{"2001: A Space Odyssey", "2001 a space odyssey"},
		{"", ""},
		{"THE MATRIX", "matrix"},
		{"a", "a"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.input, func(t *testing.T) {
			t.Parallel()
			got := normalizeTitle(c.input)
			if got != c.want {
				t.Errorf("normalizeTitle(%q) = %q, want %q", c.input, got, c.want)
			}
		})
	}
}

func TestScoreMatch(t *testing.T) {
	t.Parallel()
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
		{"exact after normalize year ok", "Dark Knight", 2008, "The Dark Knight", 2008, "high"},
		{"exact after normalize year bad", "Dark Knight", 2000, "The Dark Knight", 2008, "medium"},
		{"substring match year ok", "Dark Knight", 2012, "The Dark Knight Rises", 2012, "medium"},
		{"substring match year bad", "Dark Knight", 2000, "The Dark Knight Rises", 2012, "low"},
		{"exact after normalize year ok", "Dark Knight Rises", 2012, "The Dark Knight Rises", 2012, "high"},
		{"no overlap", "Star Wars", 1977, "Alien", 1979, "unmatched"},
		{"empty title", "", 2008, "The Dark Knight", 2008, "unmatched"},
		{"empty match title", "The Dark Knight", 2008, "", 2008, "unmatched"},
		// Same title, different year: only the matching year is high
		{"same title year match", "A Star Is Born", 1937, "A Star Is Born", 1937, "high"},
		{"same title year mismatch", "A Star Is Born", 1937, "A Star Is Born", 2018, "medium"},
		{"all quiet year match", "All Quiet on the Western Front", 2022, "All Quiet on the Western Front", 2022, "high"},
		{"all quiet year mismatch", "All Quiet on the Western Front", 1930, "All Quiet on the Western Front", 2022, "medium"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := scoreMatch(c.title, c.year, c.matchTitle, c.matchYear)
			if got != c.want {
				t.Errorf("scoreMatch(%q,%d,%q,%d) = %q, want %q",
					c.title, c.year, c.matchTitle, c.matchYear, got, c.want)
			}
		})
	}
}

func TestPickBestMatch(t *testing.T) {
	t.Parallel()

	// Simulates Radarr returning popularity-ordered results: 2018 version first,
	// then 1976, 1954, 1937. When the filename year is 1937 the matcher must
	// skip the more-popular 2018 entry and return the high-confidence 1937 one.
	starIsBorn := []map[string]any{
		{"title": "A Star Is Born", "year": float64(2018), "tmdbId": float64(332562)},
		{"title": "A Star Is Born", "year": float64(1976), "tmdbId": float64(3072)},
		{"title": "A Star Is Born", "year": float64(1954), "tmdbId": float64(3073)},
		{"title": "A Star Is Born", "year": float64(1937), "tmdbId": float64(3074)},
	}

	m := pickBestMatch(starIsBorn, "a star is born", 1937, "movie")
	if m == nil {
		t.Fatal("expected a match, got nil")
	}
	if m.Year != 1937 {
		t.Errorf("expected year 1937, got %d (title=%q confidence=%q)", m.Year, m.Title, m.Confidence)
	}
	if m.Confidence != "high" {
		t.Errorf("expected confidence high, got %q", m.Confidence)
	}
	if m.TmdbID != 3074 {
		t.Errorf("expected tmdbId 3074, got %d", m.TmdbID)
	}

	// When no year is given, the first non-unmatched (most popular) result wins.
	m2 := pickBestMatch(starIsBorn, "a star is born", 0, "movie")
	if m2 == nil {
		t.Fatal("expected a match (no year), got nil")
	}
	if m2.TmdbID != 332562 {
		t.Errorf("no-year: expected most popular (2018) tmdbId 332562, got %d", m2.TmdbID)
	}

	// All Quiet: returns two candidates; 1930 should score high against year=1930.
	allQuiet := []map[string]any{
		{"title": "All Quiet on the Western Front", "year": float64(2022), "tmdbId": float64(560050)},
		{"title": "All Quiet on the Western Front", "year": float64(1930), "tmdbId": float64(143)},
	}
	m3 := pickBestMatch(allQuiet, "all quiet on the western front", 1930, "movie")
	if m3 == nil {
		t.Fatal("expected a match for 1930, got nil")
	}
	if m3.Year != 1930 {
		t.Errorf("expected year 1930, got %d", m3.Year)
	}
}

func TestSuggestedMoviePath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		title    string
		year     int
		filename string
		edition  string
		want     string
	}{
		{"Alien", 1979, "alien.1979.mkv", "", "/media/movies/Alien (1979)/alien.1979.mkv"},
		{"Alien", 0, "alien.mkv", "", "/media/movies/Alien/alien.mkv"},
		{"The Dark Knight", 2008, "the.dark.knight.mkv", "", "/media/movies/The Dark Knight (2008)/the.dark.knight.mkv"},
		// Edition cases — Jellyfin multi-version naming.
		{"Apocalypse Now", 1979, "Apocalypse.Now.Redux.mkv", "Redux", "/media/movies/Apocalypse Now (1979)/Apocalypse Now (1979) - Redux.mkv"},
		{"Apocalypse Now", 1979, "Apocalypse.Now.Theatrical.mkv", "Theatrical Cut", "/media/movies/Apocalypse Now (1979)/Apocalypse Now (1979) - Theatrical Cut.mkv"},
		{"Blade Runner", 1982, "blade.runner.final.cut.mkv", "Final Cut", "/media/movies/Blade Runner (1982)/Blade Runner (1982) - Final Cut.mkv"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.title+"/"+c.edition, func(t *testing.T) {
			t.Parallel()
			got := suggestedMoviePath("/media/movies", c.title, c.year, c.filename, c.edition)
			if got != c.want {
				t.Errorf("suggestedMoviePath(%q,%d,%q,%q) = %q, want %q",
					c.title, c.year, c.filename, c.edition, got, c.want)
			}
		})
	}
}

func TestSuggestedTVPath(t *testing.T) {
	t.Parallel()
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
		c := c
		t.Run(c.title, func(t *testing.T) {
			t.Parallel()
			got := suggestedTVPath("/media/tv", c.title, c.season, c.filename)
			if got != c.want {
				t.Errorf("suggestedTVPath(%q,%d,%q) = %q, want %q",
					c.title, c.season, c.filename, got, c.want)
			}
		})
	}
}

// ── FS helpers ────────────────────────────────────────────────────────────────

func TestCopyFile(t *testing.T) {
	t.Parallel()
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
	if _, err := os.Stat(src); os.IsNotExist(err) {
		t.Error("copyFile should not remove src")
	}
}

func TestMoveFile(t *testing.T) {
	t.Parallel()
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
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("moveFile should remove src")
	}
}

func TestWalkVideoFiles(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

// ── resolveProfileID ──────────────────────────────────────────────────────────

func TestResolveProfileID(t *testing.T) {
	t.Parallel()

	t.Run("name found returns exact id", func(t *testing.T) {
		t.Parallel()
		got := resolveProfileID("HD-1080p", map[string]int{"HD-1080p": 3, "Any": 1})
		if got != 3 {
			t.Errorf("got %d, want 3", got)
		}
	})

	t.Run("empty name picks lowest id deterministically", func(t *testing.T) {
		t.Parallel()
		// Run several times — map iteration order is randomized, so a
		// non-deterministic implementation will eventually return a non-min id.
		m := map[string]int{"HD-1080p": 3, "Any": 1, "Bluray": 5}
		for i := 0; i < 20; i++ {
			if got := resolveProfileID("", m); got != 1 {
				t.Fatalf("iter %d: got %d, want 1 (lowest id)", i, got)
			}
		}
	})

	t.Run("missing name falls back to lowest id", func(t *testing.T) {
		t.Parallel()
		m := map[string]int{"HD-1080p": 3, "Any": 1}
		for i := 0; i < 20; i++ {
			if got := resolveProfileID("Missing", m); got != 1 {
				t.Fatalf("iter %d: got %d, want 1", i, got)
			}
		}
	})

	t.Run("empty map returns 1", func(t *testing.T) {
		t.Parallel()
		if got := resolveProfileID("", map[string]int{}); got != 1 {
			t.Errorf("got %d, want 1", got)
		}
	})
}

// ── applyFSOps ────────────────────────────────────────────────────────────────

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
	t.Parallel()
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
	applyFSOps(items, "migrate", []string{srcRoot}, []string{dstRoot}, "", "")

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
	t.Parallel()
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
	applyFSOps(items, "symlink", []string{srcRoot}, []string{dstRoot}, "", "")

	info, err := os.Lstat(dst)
	if os.IsNotExist(err) {
		t.Fatal("dst should exist after symlink")
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("dst should be a symlink")
	}
	if _, err := os.Stat(src); os.IsNotExist(err) {
		t.Error("src should still exist after symlink")
	}
	if items[0].DestPath != dst {
		t.Errorf("DestPath = %q, want %q", items[0].DestPath, dst)
	}
}

func TestApplyFSOps_SymlinkIdempotent(t *testing.T) {
	t.Parallel()
	srcRoot, dstRoot := newApplyFSOpsRoots(t)
	src := filepath.Join(srcRoot, "movie.mkv")
	dst := filepath.Join(dstRoot, "Movie (2020)", "movie.mkv")
	if err := os.WriteFile(src, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	items := []ApplyItem{{Type: "movie", Title: "Movie", Year: 2020, SourcePath: src, DestPath: dst}}
	applyFSOps(items, "symlink", []string{srcRoot}, []string{dstRoot}, "", "")
	applyFSOps(items, "symlink", []string{srcRoot}, []string{dstRoot}, "", "")

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
	// applyFSOps resolves symlinks on the source before operating, so the
	// emitted symlink points at the canonical path (e.g. /private/var/...
	// rather than /var/... on macOS). Compare against the resolved src.
	wantTarget, err := filepath.EvalSymlinks(src)
	if err != nil {
		t.Fatalf("EvalSymlinks(src): %v", err)
	}
	if target != wantTarget {
		t.Errorf("symlink target = %q, want %q", target, wantTarget)
	}
}

func TestApplyFSOps_Keep(t *testing.T) {
	t.Parallel()
	srcRoot, dstRoot := newApplyFSOpsRoots(t)
	src := filepath.Join(srcRoot, "movie.mkv")
	dst := filepath.Join(dstRoot, "Movie (2020)", "movie.mkv")
	if err := os.WriteFile(src, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	items := []ApplyItem{{Type: "movie", Title: "Movie", Year: 2020, SourcePath: src, DestPath: dst}}
	applyFSOps(items, "keep", []string{srcRoot}, []string{dstRoot}, "", "")

	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("keep strategy should not create dst")
	}
	if _, err := os.Stat(src); os.IsNotExist(err) {
		t.Error("keep strategy should leave src intact")
	}
}

func TestApplyFSOps_RejectEscapingPath(t *testing.T) {
	t.Parallel()
	_, dstRoot := newApplyFSOpsRoots(t)
	base := filepath.Dir(dstRoot)
	escapeSrc := filepath.Join(base, "outside.mkv")
	dst := filepath.Join(dstRoot, "Movie (2020)", "outside.mkv")
	if err := os.WriteFile(escapeSrc, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	items := []ApplyItem{{Type: "movie", Title: "Movie", Year: 2020, SourcePath: escapeSrc, DestPath: dst}}
	applyFSOps(items, "migrate", []string{filepath.Join(base, "downloads")}, []string{dstRoot}, "", "")

	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("path outside allowedSrcRoots should not be migrated")
	}
	if _, err := os.Stat(escapeSrc); os.IsNotExist(err) {
		t.Error("escapeSrc should be untouched")
	}
}

// TestApplyFSOps_RejectSymlinkEscape covers the case where a symlink under an
// allowed source root resolves to a path outside any allowed root. The textual
// prefix check passes; only EvalSymlinks catches it.
func TestApplyFSOps_RejectSymlinkEscape(t *testing.T) {
	t.Parallel()
	srcRoot, dstRoot := newApplyFSOpsRoots(t)
	base := filepath.Dir(dstRoot)

	// Real file lives outside the allowed src root.
	outside := filepath.Join(base, "secrets.mkv")
	if err := os.WriteFile(outside, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	// Plant a symlink inside srcRoot pointing at the outside file.
	sneaky := filepath.Join(srcRoot, "sneaky.mkv")
	if err := os.Symlink(outside, sneaky); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dstRoot, "Movie (2020)", "sneaky.mkv")
	items := []ApplyItem{{Type: "movie", Title: "Movie", Year: 2020, SourcePath: sneaky, DestPath: dst}}
	applyFSOps(items, "migrate", []string{srcRoot}, []string{dstRoot}, "", "")

	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Error("symlink escaping allowedSrcRoots must not produce a dst")
	}
	if _, err := os.Lstat(outside); err != nil {
		t.Errorf("outside target must remain untouched: %v", err)
	}
}

// TestApplyFSOps_FollowSymlinkInsideRoot confirms that a symlink whose target
// stays under an allowed root is honoured: the migrate operation moves the
// underlying file to the destination, and the original symlink path is gone.
func TestApplyFSOps_FollowSymlinkInsideRoot(t *testing.T) {
	t.Parallel()
	srcRoot, dstRoot := newApplyFSOpsRoots(t)

	// Real file inside srcRoot.
	real := filepath.Join(srcRoot, "actual.mkv")
	if err := os.WriteFile(real, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	// Symlink inside srcRoot pointing at the real file (also inside srcRoot).
	link := filepath.Join(srcRoot, "alias.mkv")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dstRoot, "Movie (2020)", "actual.mkv")
	items := []ApplyItem{{Type: "movie", Title: "Movie", Year: 2020, SourcePath: link, DestPath: dst}}
	applyFSOps(items, "migrate", []string{srcRoot}, []string{dstRoot}, "", "")

	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("dst should exist after migrate via in-root symlink: %v", err)
	}
	// The real file got renamed; both link and real should now be gone.
	if _, err := os.Stat(real); !os.IsNotExist(err) {
		t.Error("real file should have moved to dst")
	}
}

// ── extractEpisode ────────────────────────────────────────────────────────────

func TestExtractEpisode(t *testing.T) {
	t.Parallel()
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
		c := c
		t.Run(c.filename, func(t *testing.T) {
			t.Parallel()
			got := extractEpisode(c.filename)
			if got != c.want {
				t.Errorf("extractEpisode(%q) = %d, want %d", c.filename, got, c.want)
			}
		})
	}
}

// ── collapseHardlinks ─────────────────────────────────────────────────────────

func TestCollapseHardlinks_NoLinks(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	files := []ScanFile{{Path: "/nonexistent/ghost.mkv", Size: 100}}
	got := collapseHardlinks(files)
	if len(got) != 1 {
		t.Errorf("expected 1 file, got %d", len(got))
	}
}

// ── matchItemGroupKey / assignGroupKeys ───────────────────────────────────────

func TestMatchItemGroupKey(t *testing.T) {
	t.Parallel()
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
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := matchItemGroupKey(c.item)
			if got != c.want {
				t.Errorf("matchItemGroupKey = %q, want %q", got, c.want)
			}
		})
	}
}

func TestAssignGroupKeys(t *testing.T) {
	t.Parallel()
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

func newTestHandler() *Handler {
	return &Handler{
		Svc:       &stubArrClient{},
		RadarrURL: "http://radarr:7878/radarr",
		SonarrURL: "http://sonarr:8989/sonarr",
		ConfigDir: "/config/pelicula",
	}
}

// stubArrClient satisfies ArrClient returning empty/zeroed responses.
type stubArrClient struct{}

func (s *stubArrClient) Keys() (string, string, string) { return "", "", "" }
func (s *stubArrClient) ArrGet(baseURL, apiKey, path string) ([]byte, error) {
	return []byte("[]"), nil
}
func (s *stubArrClient) ArrPost(baseURL, apiKey, path string, payload any) ([]byte, error) {
	return []byte("{}"), nil
}

func TestHandleLibraryApply_DuplicateGuard(t *testing.T) {
	t.Parallel()
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

	h := newTestHandler()
	// Inject stub keys so it gets past the key check.
	h.Svc = &stubArrClientWithKeys{sonarr: "sk", radarr: "rk"}
	h.HandleLibraryApply(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for duplicate group keys, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "movie:999") {
		t.Errorf("error response should mention the duplicate group key, got: %s", w.Body.String())
	}
}

func TestHandleLibraryApply_DupEpisodeGuard(t *testing.T) {
	t.Parallel()
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

	h := newTestHandler()
	h.Svc = &stubArrClientWithKeys{sonarr: "sk", radarr: "rk"}
	h.HandleLibraryApply(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for duplicate episode group keys, got %d: %s", w.Code, w.Body.String())
	}
}

// stubArrClientWithKeys returns non-empty API keys.
type stubArrClientWithKeys struct {
	sonarr, radarr string
}

func (s *stubArrClientWithKeys) Keys() (string, string, string) { return s.sonarr, s.radarr, "" }
func (s *stubArrClientWithKeys) ArrGet(baseURL, apiKey, path string) ([]byte, error) {
	return []byte("[]"), nil
}
func (s *stubArrClientWithKeys) ArrPost(baseURL, apiKey, path string, payload any) ([]byte, error) {
	return []byte("{}"), nil
}

func TestInPlaceDetection(t *testing.T) {
	t.Parallel()
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
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			item := MatchItem{
				File:          c.file,
				Status:        c.origStatus,
				SuggestedPath: c.suggested,
			}
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
	t.Parallel()
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

func TestExtractEdition(t *testing.T) {
	t.Parallel()
	cases := []struct {
		filename string
		want     string
	}{
		{"Apocalypse.Now.Redux.1979.mkv", "Redux"},
		{"Apocalypse.Now.Final.Cut.1979.mkv", "Final Cut"},
		{"Apocalypse.Now.Theatrical.Cut.1979.mkv", "Theatrical Cut"},
		{"Apocalypse.Now.Theatrical.1979.mkv", "Theatrical Cut"},
		{"Blade.Runner.Directors.Cut.1982.mkv", "Director's Cut"},
		{"Blade.Runner.Director's.Cut.1982.mkv", "Director's Cut"},
		{"The.Dark.Knight.2008.mkv", ""},
		{"Alien.1979.mkv", ""},
		{"Film.Extended.Cut.mkv", "Extended Cut"},
		{"Film.Extended.mkv", "Extended Cut"},
		{"Film.Unrated.mkv", "Unrated"},
		{"Film.Remastered.2160p.mkv", "Remastered"},
		{"Film.Special.Edition.mkv", "Special Edition"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.filename, func(t *testing.T) {
			t.Parallel()
			got := extractEdition(c.filename)
			if got != c.want {
				t.Errorf("extractEdition(%q) = %q, want %q", c.filename, got, c.want)
			}
		})
	}
}

func TestApplyGroupKey_Editions(t *testing.T) {
	t.Parallel()

	// Different editions of the same movie → different group keys (pass duplicate guard).
	theatrical := ApplyItem{Type: "movie", TmdbID: 100, Edition: "Theatrical Cut"}
	redux := ApplyItem{Type: "movie", TmdbID: 100, Edition: "Redux"}
	if applyGroupKey(theatrical) == applyGroupKey(redux) {
		t.Error("different editions of same movie should have different group keys")
	}

	// Same movie, no edition → same group key (caught as duplicate).
	dup1 := ApplyItem{Type: "movie", TmdbID: 200}
	dup2 := ApplyItem{Type: "movie", TmdbID: 200}
	if applyGroupKey(dup1) != applyGroupKey(dup2) {
		t.Error("same movie without edition should have the same group key")
	}

	// Edition is case-folded — same edition with different casing → same key.
	lower := ApplyItem{Type: "movie", TmdbID: 300, Edition: "redux"}
	upper := ApplyItem{Type: "movie", TmdbID: 300, Edition: "Redux"}
	if applyGroupKey(lower) != applyGroupKey(upper) {
		t.Error("edition comparison should be case-insensitive")
	}
}

func TestHandleJobRetry_ProxiesToProcula(t *testing.T) {
	t.Parallel()
	var gotPath string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer fake.Close()

	proculaclient := newProculaClientForTest(fake.URL)
	h := &Handler{
		Svc:     &stubArrClient{},
		Procula: proculaclient,
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pelicula/procula/jobs/abc123/retry", nil)
	req.SetPathValue("id", "abc123")
	w := httptest.NewRecorder()
	h.HandleJobRetry(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if gotPath != "/api/procula/jobs/abc123/retry" {
		t.Errorf("proxied to %q, want /api/procula/jobs/abc123/retry", gotPath)
	}
}

func TestHandleJobRetry_MethodNotAllowed(t *testing.T) {
	t.Parallel()
	h := newTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/procula/jobs/abc123/retry", nil)
	req.SetPathValue("id", "abc123")
	w := httptest.NewRecorder()
	h.HandleJobRetry(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

// ── handleBrowse symlink escape ───────────────────────────────────────────────

func TestHandleBrowse_RejectsOutOfBoundsResolvedPath(t *testing.T) {
	t.Parallel()
	h := newTestHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/pelicula/browse?path=/etc/passwd", nil)
	w := httptest.NewRecorder()
	h.HandleBrowse(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 for path outside allowed roots", w.Code)
	}
}
