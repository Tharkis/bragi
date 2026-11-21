package books

import (
	"testing"

	"github.com/Tharkis/bragi/internal/integrations"
)

func TestFormatOf(t *testing.T) {
	for name, want := range map[string]string{
		"Andy Weir - Project Hail Mary (2021) [EPUB]":      "EPUB",
		"Andy.Weir.-.Project.Hail.Mary.2021.RETAIL.EPUB-x": "EPUB",
		"Moby Dick (Mobi edition) epub":                    "EPUB",
		"Project Hail Mary - Andy Weir (Unabridged) [M4B]": "M4B",
		"/data/x/01 - Chapter 1.mp3":                       "MP3",
		"Andy Weir - Project Hail Mary":                    "",
		"Frank Herbert - Dune [azw3]":                      "AZW3",
	} {
		if got := FormatOf(name); got != want {
			t.Errorf("FormatOf(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestEvaluate(t *testing.T) {
	rel := func(title string, p integrations.Protocol, mb int64) integrations.Release {
		return integrations.Release{Title: title, Protocol: p, SizeBytes: mb << 20}
	}
	rs := []integrations.Release{
		rel("Andy Weir - Project Hail Mary (2021) [PDF]", integrations.Usenet, 9),
		rel("Andy Weir - The Martian [EPUB]", integrations.Usenet, 2),
		rel("Project Hail Mary [EPUB]", integrations.Torrent, 2),
		rel("Andy Weir - Project Hail Mary [M4B]", integrations.Usenet, 800),
		rel("Andy Weir - Project Hail Mary [EPUB]", integrations.Torrent, 3),
		rel("Andy Weir - Project Hail Mary [EPUB]", integrations.Usenet, 3),
		rel("Andy Weir - Project Hail Mary Audiobook", integrations.Usenet, 400),
	}
	got := Evaluate(rs, Want{MediaType: "book", Title: "Project Hail Mary", Author: "Andy Weir"})
	order := []string{"usenet EPUB", "torrent EPUB", "torrent EPUB", "usenet PDF"}
	for i, w := range order {
		if got[i].Rejected != "" || string(got[i].Protocol)+" "+got[i].Format != w {
			t.Fatalf("#%d = %s %s (%s), want %s", i, got[i].Protocol, got[i].Format, got[i].Rejected, w)
		}
	}
	// A title with three or more words matches without the author.
	if !got[1].AuthorMatch == !got[2].AuthorMatch {
		t.Fatalf("author match not used as a tie-break: %+v %+v", got[1], got[2])
	}
	for _, c := range got[4:] {
		if c.Rejected == "" {
			t.Fatalf("%s should be rejected", c.Title)
		}
	}

	// Short titles need the author.
	got = Evaluate([]integrations.Release{rel("Dune [EPUB]", integrations.Usenet, 1), rel("Herbert - Dune [EPUB]", integrations.Usenet, 1)},
		Want{MediaType: "book", Title: "Dune", Author: "Frank Herbert"})
	if got[0].Title != "Herbert - Dune [EPUB]" || got[1].Rejected != "Author not in the name" {
		t.Fatalf("short title: %+v", got)
	}

	// Audiobooks: M4B over MP3, ebooks and tiny files rejected.
	got = Evaluate([]integrations.Release{
		rel("Frank Herbert - Dune [MP3]", integrations.Torrent, 500),
		rel("Frank Herbert - Dune [EPUB]", integrations.Torrent, 2),
		rel("Frank Herbert - Dune [M4B]", integrations.Torrent, 700),
		rel("Frank Herbert - Dune sample [MP3]", integrations.Torrent, 1),
	}, Want{MediaType: "audiobook", Title: "Dune: Deluxe Edition", Author: "Frank Herbert"})
	if got[0].Format != "M4B" || got[1].Format != "MP3" || got[2].Rejected == "" || got[3].Rejected == "" {
		t.Fatalf("audiobooks: %+v", got)
	}
}
