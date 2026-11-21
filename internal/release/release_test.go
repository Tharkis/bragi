package release

import (
	"reflect"
	"testing"

	"github.com/Tharkis/bragi/internal/integrations"
)

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Info
		q    string
	}{
		{"Severance.S02E03.1080p.ATVP.WEB-DL.DDP5.1.H.264-NTb",
			Info{Title: "Severance", Season: 2, Episodes: []int{3}, Resolution: "1080p", Source: "WEBDL"}, "WEBDL-1080p"},
		{"The.Office.US.S05E01-E02.720p.HDTV.x264-GRP",
			Info{Title: "The Office US", Season: 5, Episodes: []int{1, 2}, Resolution: "720p", Source: "HDTV"}, "HDTV-720p"},
		{"Doctor.Who.2005.S13E01.2160p.WEBRip.x265",
			Info{Title: "Doctor Who", Year: 2005, Season: 13, Episodes: []int{1}, Resolution: "2160p", Source: "WEBRip"}, "WEBRip-2160p"},
		{"Shogun (2024) Season 1 1080p BluRay x265",
			Info{Title: "Shogun", Year: 2024, Season: 1, FullSeason: true, Resolution: "1080p", Source: "Bluray"}, "Bluray-1080p"},
		{"Slow.Horses.S04.1080p.WEB.h264-ETHEL",
			Info{Title: "Slow Horses", Season: 4, FullSeason: true, Resolution: "1080p", Source: "WEBDL"}, "WEBDL-1080p"},
		{"Bluey - 3x05 - Rain.mkv",
			Info{Title: "Bluey", Season: 3, Episodes: []int{5}}, ""},
		{"Andor.S01E01.REPACK.1080p.DSNP.WEB-DL.mkv",
			Info{Title: "Andor", Season: 1, Episodes: []int{1}, Resolution: "1080p", Source: "WEBDL", Proper: true}, "WEBDL-1080p"},
	}
	for _, c := range cases {
		got := Parse(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Parse(%q)\n got %+v\nwant %+v", c.in, got, c.want)
		}
		if got.Quality() != c.q {
			t.Errorf("Parse(%q).Quality() = %q, want %q", c.in, got.Quality(), c.q)
		}
	}
}

func TestMatchesSeries(t *testing.T) {
	if !MatchesSeries(Parse("The.Office.US.S01E01"), "The Office (US)", 2005) {
		t.Error("The Office (US)")
	}
	if !MatchesSeries(Parse("Marvels.Agents.of.S.H.I.E.L.D.S01E01"), "Marvel's Agents of S.H.I.E.L.D.", 2013) {
		t.Error("SHIELD")
	}
	if !MatchesSeries(Parse("Law.and.Order.S01E01"), "Law & Order", 1990) {
		t.Error("ampersand")
	}
	if MatchesSeries(Parse("Doctor.Who.1963.S01E01"), "Doctor Who", 2005) {
		t.Error("wrong year accepted")
	}
	if MatchesSeries(Parse("Severance.Pay.S01E01"), "Severance", 2022) {
		t.Error("different show accepted")
	}
}

func TestEvaluate(t *testing.T) {
	p := Preset{Qualities: []string{"WEBDL-1080p", "HDTV-1080p", "WEBDL-720p"}, MaxMBPerMin: 100}
	w := Want{SeriesTitle: "Severance", Year: 2022, Season: 1, Episodes: []int{2}, Runtime: 55,
		Blocklist: map[string]bool{"Severance.S01E02.1080p.WEB-DL-BAD": true}}
	gb := int64(1 << 30)
	rs := []integrations.Release{
		{Title: "Severance.S01E02.720p.WEB-DL-A", Protocol: integrations.Usenet, SizeBytes: gb},
		{Title: "Severance.S01E02.1080p.WEB-DL-BAD", Protocol: integrations.Usenet, SizeBytes: gb},
		{Title: "Severance.S01E02.1080p.WEB-DL-T", Protocol: integrations.Torrent, SizeBytes: 2 * gb, Seeders: 30},
		{Title: "Severance.S01E02.1080p.WEB-DL-U", Protocol: integrations.Usenet, SizeBytes: 2 * gb},
		{Title: "Severance.S01E02.1080p.WEB-DL-HUGE", Protocol: integrations.Usenet, SizeBytes: 20 * gb},
		{Title: "Severance.S01E03.1080p.WEB-DL-U", Protocol: integrations.Usenet, SizeBytes: gb},
		{Title: "Severance.S01E02.2160p.WEB-DL-U", Protocol: integrations.Usenet, SizeBytes: gb},
		{Title: "Severance.S01E02.1080p.HDTV-Dead", Protocol: integrations.Torrent, SizeBytes: gb},
	}
	got := Evaluate(rs, w, p)
	order := []string{}
	for _, c := range got {
		if c.Rejected == "" {
			order = append(order, c.Title)
		}
	}
	want := []string{"Severance.S01E02.1080p.WEB-DL-U", "Severance.S01E02.1080p.WEB-DL-T", "Severance.S01E02.720p.WEB-DL-A"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("accepted order\n got %v\nwant %v", order, want)
	}
	reasons := map[string]string{}
	for _, c := range got {
		reasons[c.Title] = c.Rejected
	}
	for title, want := range map[string]string{
		"Severance.S01E02.1080p.WEB-DL-BAD":  "Failed before",
		"Severance.S01E03.1080p.WEB-DL-U":    "Different episode",
		"Severance.S01E02.2160p.WEB-DL-U":    "WEBDL-2160p is not in the quality preset",
		"Severance.S01E02.1080p.HDTV-Dead":   "No seeders",
		"Severance.S01E02.1080p.WEB-DL-HUGE": "Too big (20.0 GB, limit 5.4 GB)",
	} {
		if reasons[title] != want {
			t.Errorf("%s: rejected %q, want %q", title, reasons[title], want)
		}
	}
}
