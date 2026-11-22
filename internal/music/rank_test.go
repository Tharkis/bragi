package music

import (
	"testing"

	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/store"
)

func TestQualityOf(t *testing.T) {
	for name, want := range map[string]string{
		"Radiohead - OK Computer (1997) [FLAC] {WEB}":      "FLAC",
		"Radiohead-OK_Computer-24BIT-WEB-FLAC-2016-ENRiCH": "FLAC",
		"Radiohead - OK Computer [MP3 320]":                "MP3-320",
		"Radiohead - OK Computer (1997) MP3 V0":            "MP3-V0",
		"Radiohead - OK Computer (1997) [V0]":              "MP3-V0",
		"Radiohead - OK Computer 1997 mp3 256kbps":         "MP3-256",
		"Radiohead - OK Computer (1997) [MP3]":             "MP3",
		"Radiohead - OK Computer (iTunes AAC 256)":         "AAC-256",
		"Radiohead - OK Computer (1997)":                   "",
		"Radiohead / OK Computer (1997) [MP3 192]":         "MP3-192",
	} {
		if got := QualityOf(name); got != want {
			t.Errorf("QualityOf(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestEvaluateUpgradeAndSoulseekFirst(t *testing.T) {
	rs := []integrations.Release{
		{Title: "Radiohead - Kid A (2000) [FLAC]", Protocol: integrations.Usenet, SizeBytes: 300 << 20},
		{Title: "Radiohead / Kid A [FLAC]", GUID: `peer:@@music\Radiohead\Kid A\01.flac`, Protocol: integrations.Soulseek, SizeBytes: 300 << 20, Files: 10},
		{Title: "Radiohead - Kid A (2000) [MP3 320]", Protocol: integrations.Torrent, SizeBytes: 90 << 20},
		{Title: "Radiohead - Kid A Audiobook", Protocol: integrations.Torrent, SizeBytes: 90 << 20},
	}
	c := Evaluate(rs, Want{Artist: "Radiohead", Album: "Kid A", Tracks: 10})
	if c[0].Protocol != integrations.Soulseek || c[1].Protocol != integrations.Usenet || c[3].Rejected != "This is an audiobook" {
		t.Fatalf("order: %+v", c)
	}
	c = Evaluate(rs, Want{Artist: "Radiohead", Album: "Kid A", Tracks: 10, Have: "MP3-320"})
	if c[0].Rejected != "" || c[2].Rejected != "Not better than what you have (MP3 320)" {
		t.Fatalf("upgrade: %+v", c)
	}
}

func TestMatchByNumbersTitlesAndOrder(t *testing.T) {
	tracks := []store.Track{{Disc: 1, Number: 1, Title: "Airbag"}, {Disc: 1, Number: 2, Title: "Paranoid Android"}, {Disc: 1, Number: 3, Title: "Subterranean Homesick Alien"}}
	files := []audioFile{{path: "c", title: "Subterranean Homesick Alien"}, {path: "a", track: 1, title: "Airbag"}, {path: "b", title: "Paranoid Android (Remastered)"}}
	pairs, sure, why := match(files, tracks)
	if !sure || pairs[0] != 2 || pairs[1] != 0 || pairs[2] != 1 {
		t.Fatalf("pairs %v sure %v %s", pairs, sure, why)
	}
	_, sure, why = match(files[:2], tracks)
	if sure || why != "1 of 3 tracks are missing from the download" {
		t.Fatalf("missing: %v %s", sure, why)
	}
	d, n, title := numbersFromName("/x/CD2/1-07 - Artist - Song.flac", true)
	if d != 2 || n != 7 || title != "Song" {
		t.Fatalf("numbersFromName: %d %d %q", d, n, title)
	}
	d, n, _ = numbersFromName("/x/Album/107 Song.mp3", true)
	if d != 1 || n != 7 {
		t.Fatalf("numbersFromName 107: %d %d", d, n)
	}
}
