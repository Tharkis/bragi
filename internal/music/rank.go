package music

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/release"
)

// DefaultQualities is the "Lossless first" list, used when a library has
// no music quality preset.
var DefaultQualities = []string{"FLAC", "MP3-320", "MP3-V0", "MP3-256", "AAC-256"}

// Candidate is a search result judged against a wanted album.
type Candidate struct {
	integrations.Release
	// Quality is what the release carries: FLAC, MP3-320, MP3-V0, ... or
	// "" when its name does not say.
	Quality string `json:"quality"`
	// Rejected says why the release will not be grabbed automatically.
	Rejected string `json:"rejected,omitempty"`
	rank     int
	exact    bool
}

// Want is what a search is for.
type Want struct {
	Artist string
	Album  string
	// Tracks is the album's track count, 0 when not known yet.
	Tracks    int
	Blocklist map[string]bool
	// Order is the quality list, best first.
	Order []string
	// Have is the quality already in the library; a release must beat it.
	Have string
}

var (
	reWord     = regexp.MustCompile(`[a-z0-9]+`)
	reAudiobk  = regexp.MustCompile(`(?i)\b(audio ?books?|unabridged|abridged|narrated)\b`)
	reDiscog   = regexp.MustCompile(`(?i)\b(discography|discografia|complete (albums|works|collection)|collection|anthology|\d+ albums)\b`)
	stopWords  = map[string]bool{"the": true, "a": true, "an": true, "of": true, "and": true, "to": true, "in": true, "on": true}
	reVersions = regexp.MustCompile(`\s*[\(\[][^\)\]]*(edition|remaster|deluxe|version|expanded|anniversary|bonus)[^\)\]]*[\)\]]`)
)

// words lowers s and splits it into letters-and-digits words, dropping
// apostrophes and reading "&" as "and" the way release names do.
func words(s string) []string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "&", " and ")
	s = strings.ReplaceAll(s, "'", "")
	s = strings.ReplaceAll(s, "’", "")
	return reWord.FindAllString(foldAccents(s), -1)
}

var accents = strings.NewReplacer("á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a", "å", "a", "é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i", "ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o", "ø", "o", "ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ñ", "n", "ç", "c", "ý", "y", "ÿ", "y", "æ", "ae", "œ", "oe", "ß", "ss")

func foldAccents(s string) string { return accents.Replace(s) }

// mainTitle drops an edition note: "OK Computer (Collector's Edition)" is
// searched as "OK Computer".
func mainTitle(t string) string {
	if s := strings.TrimSpace(reVersions.ReplaceAllString(t, "")); s != "" {
		return s
	}
	return t
}

// significant returns the words of a name that must appear in a release.
func significant(name string) []string {
	var out []string
	for _, w := range words(mainTitle(name)) {
		if !stopWords[w] {
			out = append(out, w)
		}
	}
	if len(out) == 0 {
		out = words(name)
	}
	return out
}

// QualityOf reads the quality from a release or file name: "FLAC",
// "MP3-320", "MP3-V0", "MP3-256", "MP3-192", "MP3", "AAC-256", "AAC",
// "OGG", "OPUS", or "" when the name does not say.
func QualityOf(name string) string {
	ws := words(name)
	has := func(xs ...string) bool {
		for _, x := range xs {
			if slices.Contains(ws, x) {
				return true
			}
		}
		return false
	}
	switch {
	case has("flac", "lossless", "alac", "wav", "ape", "24bit", "16bit"):
		return "FLAC"
	case has("mp3"):
		switch {
		case has("v0"):
			return "MP3-V0"
		case has("320", "320kbps", "cbr320"):
			return "MP3-320"
		case has("256", "256kbps"):
			return "MP3-256"
		case has("192", "192kbps"):
			return "MP3-192"
		case has("v2"):
			return "MP3-V2"
		case has("128", "128kbps"):
			return "MP3-128"
		}
		return "MP3"
	case has("v0"):
		return "MP3-V0"
	case has("aac", "m4a"):
		if has("256", "256kbps") {
			return "AAC-256"
		}
		return "AAC"
	case has("ogg", "vorbis"):
		return "OGG"
	case has("opus"):
		return "OPUS"
	}
	return ""
}

// rankOf places a quality in the list. A plain "MP3" or "AAC" sits just
// after the last listed one of its kind; an unknown quality sits at the
// end. -1 means the list does not want it at all.
func rankOf(order []string, q string) int {
	if i := slices.Index(order, q); i >= 0 {
		return i
	}
	switch q {
	case "":
		return len(order)
	case "MP3", "AAC":
		last := -1
		for i, x := range order {
			if strings.HasPrefix(x, q) {
				last = i
			}
		}
		if last >= 0 {
			return last
		}
	}
	return -1
}

// Evaluate judges releases for w and returns them best first, rejected
// ones last.
func Evaluate(rs []integrations.Release, w Want) []Candidate {
	order := w.Order
	if len(order) == 0 {
		order = DefaultQualities
	}
	needAlbum := significant(w.Album)
	needArtist := significant(w.Artist)
	if strings.EqualFold(w.Artist, "Various Artists") {
		needArtist = nil
	}
	haveRank := -1
	if w.Have != "" {
		haveRank = rankOf(order, w.Have)
		if haveRank < 0 {
			haveRank = len(order)
		}
	}
	albumWords := map[string]bool{}
	for _, x := range words(w.Album) {
		albumWords[x] = true
	}
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
		c := Candidate{Release: r, Quality: QualityOf(r.Title)}
		text := r.Title
		if r.Protocol == integrations.Soulseek {
			// Soulseek matched the search against the whole shared path,
			// which names the artist a folder or two up.
			if _, p, ok := strings.Cut(r.GUID, ":"); ok {
				text += " " + p
			}
		}
		have := map[string]bool{}
		for _, x := range words(text) {
			have[x] = true
		}
		missing := func(need []string) bool {
			for _, x := range need {
				if !have[x] {
					return true
				}
			}
			return false
		}
		c.rank = rankOf(order, c.Quality)
		c.exact = w.Tracks > 0 && r.Files == w.Tracks
		discog := reDiscog.FindString(r.Title)
		switch {
		case w.Blocklist[r.Title]:
			c.Rejected = "Blocklisted after an earlier failure"
		case discog != "" && !albumWords[strings.ToLower(strings.Fields(discog)[0])]:
			c.Rejected = "Looks like more than one album"
		case missing(needAlbum):
			c.Rejected = "Album not in the name"
		case missing(needArtist):
			c.Rejected = "Artist not in the name"
		case reAudiobk.MatchString(r.Title):
			c.Rejected = "This is an audiobook"
		case c.rank < 0:
			c.Rejected = fmt.Sprintf("%s is not in your quality list", c.Quality)
		case r.Protocol == integrations.Soulseek && w.Tracks > 0 && r.Files > 0 && r.Files < w.Tracks:
			c.Rejected = fmt.Sprintf("Missing tracks (%d of %d)", r.Files, w.Tracks)
		case r.Protocol == integrations.Soulseek && w.Tracks > 0 && r.Files > w.Tracks*2+2:
			c.Rejected = fmt.Sprintf("Looks like more than one album (%d files)", r.Files)
		case r.SizeBytes > 6<<30:
			c.Rejected = "Too big for one album (" + release.HumanSize(r.SizeBytes) + ")"
		case r.SizeBytes > 0 && r.SizeBytes < 3<<20:
			c.Rejected = "Too small for an album (" + release.HumanSize(r.SizeBytes) + ")"
		case haveRank >= 0 && c.rank >= haveRank:
			c.Rejected = "Not better than what you have (" + Label(w.Have) + ")"
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return better(out[i], out[j]) })
	return out
}

// Label shows a quality the way people write it: "MP3 320", "FLAC".
func Label(q string) string { return strings.ReplaceAll(q, "-", " ") }

// Soulseek first, as asked; Usenet before torrents as elsewhere.
var protocolOrder = map[integrations.Protocol]int{integrations.Soulseek: 0, integrations.Usenet: 1, integrations.Torrent: 2}

func better(a, b Candidate) bool {
	if (a.Rejected == "") != (b.Rejected == "") {
		return a.Rejected == ""
	}
	if a.rank != b.rank {
		return a.rank < b.rank
	}
	if a.exact != b.exact {
		return a.exact
	}
	if a.Protocol != b.Protocol {
		return protocolOrder[a.Protocol] < protocolOrder[b.Protocol]
	}
	if a.Seeders != b.Seeders {
		return a.Seeders > b.Seeders
	}
	return a.PublishedAt.After(b.PublishedAt)
}
