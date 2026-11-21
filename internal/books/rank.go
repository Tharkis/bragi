package books

import (
	"regexp"
	"sort"
	"strings"

	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/release"
)

// Candidate is a search result judged against a wanted book.
type Candidate struct {
	integrations.Release
	// Format is the file format the release carries (EPUB, M4B, ...), or ""
	// when its name does not say.
	Format string `json:"format"`
	// AuthorMatch is true when the release names the author.
	AuthorMatch bool `json:"authorMatch"`
	// Rejected says why the release will not be grabbed automatically.
	Rejected string `json:"rejected,omitempty"`
	rank     int
}

// Want is what a search is for.
type Want struct {
	MediaType string
	Title     string
	Author    string
	Blocklist map[string]bool
}

// Format preferences, best first. Unknown formats sit between the good
// ones and the poor ones: the import checks the files anyway.
var (
	ebookOrder = []string{"EPUB", "AZW3", "MOBI", "AZW", "", "PDF", "CBZ", "CBR"}
	audioOrder = []string{"M4B", "MP3", "M4A", "FLAC", "", "AAC", "OGG", "OPUS", "WMA"}
	ebookSet   = map[string]bool{"EPUB": true, "AZW3": true, "MOBI": true, "AZW": true, "PDF": true, "CBZ": true, "CBR": true}
	audioSet   = map[string]bool{"M4B": true, "MP3": true, "M4A": true, "FLAC": true, "AAC": true, "OGG": true, "OPUS": true, "WMA": true}

	reWord       = regexp.MustCompile(`[a-z0-9]+`)
	reAudioWords = regexp.MustCompile(`(?i)\b(audio ?books?|unabridged|abridged|narrated|read by|audible)\b`)
	stopWords    = map[string]bool{"the": true, "a": true, "an": true, "of": true, "and": true, "or": true, "to": true, "in": true, "on": true}
)

// words lowers s, splits it into letters-and-digits words and drops "&"
// and apostrophes the way release names do.
func words(s string) []string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "&", " and ")
	s = strings.ReplaceAll(s, "'", "")
	s = strings.ReplaceAll(s, "’", "")
	return reWord.FindAllString(s, -1)
}

// mainTitle drops a subtitle: "Dune: Deluxe Edition" is searched as "Dune".
func mainTitle(t string) string {
	if i := strings.IndexAny(t, ":("); i > 0 {
		return strings.TrimSpace(t[:i])
	}
	return t
}

// significant returns the words of a title that must appear in a release.
func significant(title string) []string {
	var out []string
	for _, w := range words(mainTitle(title)) {
		if !stopWords[w] {
			out = append(out, w)
		}
	}
	if len(out) == 0 {
		out = words(mainTitle(title))
	}
	return out
}

// surname is the author's last name, the part releases almost always carry.
func surname(author string) string {
	ws := words(author)
	if len(ws) == 0 {
		return ""
	}
	return ws[len(ws)-1]
}

// FormatOf reads the format from a release or file name.
func FormatOf(name string) string {
	ws := words(name)
	// The extension or the last bracketed tag usually says it; look from
	// the end so "Mobi Dick EPUB" reads as EPUB.
	for i := len(ws) - 1; i >= 0; i-- {
		u := strings.ToUpper(ws[i])
		if ebookSet[u] || audioSet[u] {
			return u
		}
	}
	return ""
}

func orderOf(list []string, f string) int {
	for i, x := range list {
		if x == f {
			return i
		}
	}
	return len(list)
}

// Evaluate judges releases for w and returns them best first, rejected
// ones last.
func Evaluate(rs []integrations.Release, w Want) []Candidate {
	need := significant(w.Title)
	sur := surname(w.Author)
	out := make([]Candidate, 0, len(rs))
	seen := map[string]bool{}
	for _, r := range rs {
		key := strings.ToLower(r.Title) + "|" + string(r.Protocol)
		if r.Protocol == integrations.Soulseek {
			key = r.GUID
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		c := Candidate{Release: r, Format: FormatOf(r.Title)}
		have := map[string]bool{}
		text := r.Title
		if r.Protocol == integrations.Soulseek {
			// Soulseek matched the search against the whole shared path,
			// which often names the author a few folders up.
			if _, p, ok := strings.Cut(r.GUID, ":"); ok {
				text += " " + p
			}
		}
		for _, x := range words(text) {
			have[x] = true
		}
		c.AuthorMatch = sur != "" && have[sur]
		audioHint := audioSet[c.Format] || reAudioWords.MatchString(r.Title)
		missing := ""
		for _, x := range need {
			if !have[x] {
				missing = x
				break
			}
		}
		switch {
		case w.Blocklist[r.Title]:
			c.Rejected = "Blocklisted after an earlier failure"
		case missing != "":
			c.Rejected = "Title does not match"
		case !c.AuthorMatch && sur != "" && len(need) < 3:
			// Short titles match too much; ask for the author as well.
			c.Rejected = "Author not in the name"
		case w.MediaType == "book" && audioHint:
			c.Rejected = "This is an audiobook"
		case w.MediaType == "audiobook" && ebookSet[c.Format]:
			c.Rejected = "This is an ebook"
		case w.MediaType == "book" && r.SizeBytes > 300<<20:
			c.Rejected = "Too big for an ebook (" + release.HumanSize(r.SizeBytes) + ")"
		case w.MediaType == "audiobook" && r.SizeBytes > 0 && r.SizeBytes < 5<<20:
			c.Rejected = "Too small for an audiobook (" + release.HumanSize(r.SizeBytes) + ")"
		}
		if w.MediaType == "audiobook" {
			c.rank = orderOf(audioOrder, c.Format)
		} else {
			c.rank = orderOf(ebookOrder, c.Format)
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return better(out[i], out[j]) })
	return out
}

var protocolOrder = map[integrations.Protocol]int{integrations.Usenet: 0, integrations.Torrent: 1, integrations.Soulseek: 2}

func better(a, b Candidate) bool {
	if (a.Rejected == "") != (b.Rejected == "") {
		return a.Rejected == ""
	}
	if a.rank != b.rank {
		return a.rank < b.rank
	}
	if a.AuthorMatch != b.AuthorMatch {
		return a.AuthorMatch
	}
	if a.Protocol != b.Protocol {
		return protocolOrder[a.Protocol] < protocolOrder[b.Protocol]
	}
	if a.Seeders != b.Seeders {
		return a.Seeders > b.Seeders
	}
	return a.PublishedAt.After(b.PublishedAt)
}
