package release

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Tharkis/bragi/internal/integrations"
)

// Preset is the part of a quality preset ranking needs.
type Preset struct {
	Qualities   []string
	MaxMBPerMin float64
}

// Want describes what a search is for.
type Want struct {
	SeriesTitle string
	Year        int
	Season      int
	// Episodes wanted. Empty with FullSeason means a season pack.
	Episodes   []int
	FullSeason bool
	// Runtime in minutes of one episode, for the size cap.
	Runtime int
	// Blocklist holds release titles that failed before.
	Blocklist map[string]bool
}

// Candidate is a search result with Bragi's verdict on it.
type Candidate struct {
	integrations.Release
	Info     Info   `json:"info"`
	Quality  string `json:"quality"`
	Rank     int    `json:"rank"` // position of Quality in the preset, 0 is best
	Rejected string `json:"rejected,omitempty"`
}

// Evaluate parses every release, rejects the ones that do not fit, and sorts
// the rest best first. Rejected releases come last, with a reason, so the
// manual pick screen can show why.
func Evaluate(releases []integrations.Release, w Want, p Preset) []Candidate {
	out := make([]Candidate, 0, len(releases))
	seen := map[string]bool{}
	for _, r := range releases {
		key := strings.ToLower(r.Title) + "|" + string(r.Protocol)
		if seen[key] {
			continue
		}
		seen[key] = true
		c := Candidate{Release: r, Info: Parse(r.Title)}
		c.Quality = c.Info.Quality()
		c.Rank = -1
		for i, q := range p.Qualities {
			if strings.EqualFold(q, c.Quality) {
				c.Rank = i
				break
			}
		}
		c.Rejected = reject(c, w, p)
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool { return better(out[i], out[j]) })
	return out
}

func reject(c Candidate, w Want, p Preset) string {
	switch {
	case w.Blocklist[c.Title]:
		return "Failed before"
	case !MatchesSeries(c.Info, w.SeriesTitle, w.Year):
		return "Different show"
	case c.Info.Season != w.Season || !c.Info.IsTV():
		return "Different season"
	}
	if w.FullSeason {
		if !c.Info.FullSeason {
			return "Not a full season"
		}
	} else {
		if c.Info.FullSeason {
			return "Full season, only some episodes wanted"
		}
		if !coversWanted(c.Info.Episodes, w.Episodes) {
			return "Different episode"
		}
	}
	if c.Quality == "" {
		return "Unknown quality"
	}
	if c.Rank < 0 {
		return c.Quality + " is not in the quality preset"
	}
	if c.Protocol == integrations.Torrent && c.Seeders == 0 {
		return "No seeders"
	}
	if p.MaxMBPerMin > 0 && w.Runtime > 0 && c.SizeBytes > 0 {
		eps := len(c.Info.Episodes)
		if w.FullSeason || eps == 0 {
			eps = len(w.Episodes)
		}
		if eps == 0 {
			eps = 1
		}
		limit := p.MaxMBPerMin * float64(w.Runtime) * float64(eps) * 1024 * 1024
		if float64(c.SizeBytes) > limit {
			return fmt.Sprintf("Too big (%s, limit %s)", HumanSize(c.SizeBytes), HumanSize(int64(limit)))
		}
	}
	return ""
}

// coversWanted reports whether a release holds at least one wanted episode
// and no episode outside the wanted set.
func coversWanted(have, want []int) bool {
	if len(have) == 0 {
		return false
	}
	ws := map[int]bool{}
	for _, e := range want {
		ws[e] = true
	}
	for _, e := range have {
		if !ws[e] {
			return false
		}
	}
	return true
}

func better(a, b Candidate) bool {
	if (a.Rejected == "") != (b.Rejected == "") {
		return a.Rejected == ""
	}
	if a.Rank != b.Rank {
		if a.Rank < 0 || b.Rank < 0 {
			return a.Rank >= 0
		}
		return a.Rank < b.Rank
	}
	if a.Info.Proper != b.Info.Proper {
		return a.Info.Proper
	}
	if a.Protocol != b.Protocol {
		// Usenet first: it finishes faster and needs no seeding.
		return a.Protocol == integrations.Usenet
	}
	if a.Seeders != b.Seeders {
		return a.Seeders > b.Seeders
	}
	return a.PublishedAt.After(b.PublishedAt)
}

// HumanSize formats bytes as "1.4 GB".
func HumanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
