package main

import (
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
		{"80% word overlap year ok", "Dark Knight Rises", 2012, "The Dark Knight Rises", 2012, "high"}, // exact after normalize
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
	cases := []struct {
		title    string
		year     int
		filename string
		want     string
	}{
		{"Alien", 1979, "alien.1979.mkv", "/movies/Alien (1979)/alien.1979.mkv"},
		{"Alien", 0, "alien.mkv", "/movies/Alien/alien.mkv"},
		{"The Dark Knight", 2008, "the.dark.knight.mkv", "/movies/The Dark Knight (2008)/the.dark.knight.mkv"},
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
	cases := []struct {
		title    string
		season   int
		filename string
		want     string
	}{
		{"Breaking Bad", 1, "s01e01.mkv", "/tv/Breaking Bad/Season 01/s01e01.mkv"},
		{"Breaking Bad", 12, "s12e01.mkv", "/tv/Breaking Bad/Season 12/s12e01.mkv"},
		{"Show", 0, "episode.mkv", "/tv/Show/episode.mkv"},
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
