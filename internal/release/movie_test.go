package release

import (
	"testing"

	"github.com/Tharkis/bragi/internal/integrations"
)

func TestParseMovie(t *testing.T) {
	cases := []struct {
		name, title string
		year        int
		quality     string
	}{
		{"Dune.Part.Two.2024.1080p.WEB-DL.DDP5.1.Atmos.H.264-FLUX", "Dune Part Two", 2024, "WEBDL-1080p"},
		{"Heat.1995.BluRay.x264-SPARKS", "Heat", 1995, "DVD"},
		{"Blade.Runner.2049.2017.2160p.UHD.BluRay.REMUX", "Blade Runner 2049", 2017, "Bluray-2160p"},
		{"Past Lives (2023) [WEBRip-720p].mkv", "Past Lives", 2023, "WEBRip-720p"},
		{"Heat (1995)", "Heat", 1995, ""},
	}
	for _, c := range cases {
		info := Parse(c.name)
		if info.Title != c.title || info.Year != c.year || info.Quality() != c.quality || info.IsTV() {
			t.Errorf("Parse(%q) = %q %d %q tv=%v, want %q %d %q", c.name, info.Title, info.Year, info.Quality(), info.IsTV(), c.title, c.year, c.quality)
		}
	}
}

func TestMatchesMovie(t *testing.T) {
	cases := []struct {
		release, title string
		year           int
		want           bool
	}{
		{"Dune.Part.Two.2024.1080p.WEB-DL", "Dune: Part Two", 2024, true},
		{"Dune.Part.Two.2023.1080p.WEB-DL", "Dune: Part Two", 2024, true}, // a year off is common
		{"Dune.2021.1080p.WEB-DL", "Dune: Part Two", 2024, false},
		{"Dune.1984.1080p.BluRay", "Dune", 2021, false},
		{"Blade.Runner.2049.1080p.BluRay", "Blade Runner 2049", 2017, true},
		{"Blade.Runner.1982.1080p.BluRay", "Blade Runner 2049", 2017, false},
	}
	for _, c := range cases {
		if got := MatchesMovie(Parse(c.release), c.title, c.year); got != c.want {
			t.Errorf("MatchesMovie(%q, %q, %d) = %v, want %v", c.release, c.title, c.year, got, c.want)
		}
	}
}

func TestEvaluateMovie(t *testing.T) {
	gb := int64(1 << 30)
	rs := []integrations.Release{
		{Title: "Heat.1995.1080p.BluRay.x264-A", Protocol: integrations.Torrent, Seeders: 5, SizeBytes: 12 * gb},
		{Title: "Heat.1995.720p.WEB-DL-B", Protocol: integrations.Usenet, SizeBytes: 4 * gb},
		{Title: "Heat.S01E01.1080p.WEB-DL-C", Protocol: integrations.Usenet, SizeBytes: gb},
		{Title: "Heat.1995.1080p.BluRay.REMUX-D", Protocol: integrations.Torrent, Seeders: 5, SizeBytes: 40 * gb},
	}
	p := Preset{Qualities: []string{"Bluray-1080p", "WEBDL-1080p", "WEBDL-720p"}, MaxMBPerMin: 100}
	got := Evaluate(rs, Want{Movie: true, SeriesTitle: "Heat", Year: 1995, Runtime: 170}, p)
	if got[0].Title != "Heat.1995.1080p.BluRay.x264-A" || got[1].Title != "Heat.1995.720p.WEB-DL-B" {
		t.Fatalf("order: %s, %s", got[0].Title, got[1].Title)
	}
	reasons := map[string]string{}
	for _, c := range got {
		reasons[c.Title] = c.Rejected
	}
	if reasons["Heat.S01E01.1080p.WEB-DL-C"] != "TV episode, not a movie" {
		t.Errorf("episode: %q", reasons["Heat.S01E01.1080p.WEB-DL-C"])
	}
	if r := reasons["Heat.1995.1080p.BluRay.REMUX-D"]; r == "" || r[:7] != "Too big" {
		t.Errorf("remux: %q", r)
	}
}
