package books

import (
	"testing"

	"github.com/Tharkis/bragi/internal/store"
)

func TestCleanName(t *testing.T) {
	for in, want := range map[string]string{
		"01. Dune":                   "Dune",
		"03 - The Well of Ascension": "The Well of Ascension",
		"Project Hail Mary (2021)":   "Project Hail Mary",
		"The Hobbit (123)":           "The Hobbit",
		"1984":                       "1984",
		"Book 2 - The Dark Forest":   "The Dark Forest",
		"Piranesi [retail] (2020)":   "Piranesi",
		"the_name_of_the_wind":       "the name of the wind",
	} {
		if got, _ := cleanName(in); got != want {
			t.Errorf("cleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGuessFromPath(t *testing.T) {
	lib := &store.Library{Path: t.TempDir(), MediaType: "book"}
	for unit, want := range map[string][2]string{
		"Andy Weir/Project Hail Mary.epub":                                 {"Project Hail Mary", "Andy Weir"},
		"Brandon Sanderson/Mistborn/02. The Well of Ascension.epub":        {"The Well of Ascension", "Brandon Sanderson"},
		"Andy Weir - The Martian.epub":                                     {"The Martian", "Andy Weir"},
		"Tolkien, J.R.R./The Hobbit (12)/The Hobbit - J.R.R. Tolkien.epub": {"The Hobbit", "Tolkien, J.R.R."},
	} {
		g := guessUnit(lib, unit, nil)
		if len(g.titles) == 0 || g.titles[0] != want[0] || g.author != want[1] {
			t.Errorf("guess(%q) = %+v, want %v", unit, g, want)
		}
	}
	if !sameAuthor("Tolkien, J.R.R.", "J. R. R. Tolkien") || sameAuthor("Andy Weir", "Frank Herbert") {
		t.Error("sameAuthor")
	}
}
