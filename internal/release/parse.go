// Package release parses release names and ranks search results against a
// quality preset.
package release

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Info is what Bragi can read from a release or file name.
type Info struct {
	Title      string `json:"title"`
	Year       int    `json:"year,omitempty"`
	Season     int    `json:"season"`
	Episodes   []int  `json:"episodes,omitempty"`
	FullSeason bool   `json:"fullSeason,omitempty"`
	Resolution string `json:"resolution,omitempty"` // 2160p, 1080p, 720p, 480p
	Source     string `json:"source,omitempty"`     // Bluray, WEBDL, WEBRip, HDTV, DVD
	Proper     bool   `json:"proper,omitempty"`
}

// Quality is the preset name for this release, e.g. "WEBDL-1080p", or "" if
// Bragi cannot tell.
func (i Info) Quality() string {
	switch {
	case i.Source == "" && i.Resolution == "":
		return ""
	case i.Resolution == "480p" || i.Resolution == "":
		if i.Source == "DVD" || i.Source == "Bluray" {
			return "DVD"
		}
		return "SDTV"
	case i.Source == "":
		return "HDTV-" + i.Resolution
	case i.Source == "DVD":
		return "DVD"
	}
	return i.Source + "-" + i.Resolution
}

// IsTV reports whether a season (and usually an episode) was found.
func (i Info) IsTV() bool { return i.FullSeason || len(i.Episodes) > 0 }

var (
	reSxxEyy     = regexp.MustCompile(`(?i)\bS(\d{1,2})[ ._-]?E(\d{1,3})((?:[ ._-]?-?[ ._-]?E\d{1,3})*)`)
	reMoreEps    = regexp.MustCompile(`(?i)E(\d{1,3})`)
	reNxNN       = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{2,3})\b`)
	reSeasonPack = regexp.MustCompile(`(?i)\b(?:S(\d{1,2})|Season[ ._-]?(\d{1,2}))\b`)
	reYear       = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	reRes        = regexp.MustCompile(`(?i)\b(2160p|4k|uhd|1080[pi]|720p|576p|480p)\b`)
	reBluray     = regexp.MustCompile(`(?i)\b(blu-?ray|bdrip|brrip|bdremux|remux|bd(?:25|50))\b`)
	reWebDL      = regexp.MustCompile(`(?i)\b(web-?dl|webhd|web|amzn|nf|dsnp|hmax|atvp|hulu|pcok|max)\b`)
	reWebRip     = regexp.MustCompile(`(?i)\b(web-?rip)\b`)
	reHDTV       = regexp.MustCompile(`(?i)\b(hdtv|pdtv|dsr|tvrip)\b`)
	reDVD        = regexp.MustCompile(`(?i)\b(dvd-?rip|dvd|dvdr)\b`)
	reProper     = regexp.MustCompile(`(?i)\b(proper|repack|rerip)\b`)
	reSep        = regexp.MustCompile(`[._]+`)
	reSpaces     = regexp.MustCompile(`\s+`)
)

// Parse reads a release title or file name.
func Parse(name string) Info {
	name = strings.TrimSpace(name)
	if ext := filepath.Ext(name); len(ext) >= 3 && len(ext) <= 5 && isVideoExt(ext) {
		name = strings.TrimSuffix(name, ext)
	}
	var info Info
	titleEnd := -1

	if m := reSxxEyy.FindStringSubmatchIndex(name); m != nil {
		info.Season = atoi(name[m[2]:m[3]])
		info.Episodes = append(info.Episodes, atoi(name[m[4]:m[5]]))
		if m[6] < m[7] {
			for _, e := range reMoreEps.FindAllStringSubmatch(name[m[6]:m[7]], -1) {
				info.Episodes = append(info.Episodes, atoi(e[1]))
			}
		}
		titleEnd = m[0]
		info.Episodes = expandRange(info.Episodes, name[m[6]:m[7]])
	} else if m := reNxNN.FindStringSubmatchIndex(name); m != nil {
		info.Season = atoi(name[m[2]:m[3]])
		info.Episodes = []int{atoi(name[m[4]:m[5]])}
		titleEnd = m[0]
	} else if m := reSeasonPack.FindStringSubmatchIndex(name); m != nil {
		if m[2] >= 0 {
			info.Season = atoi(name[m[2]:m[3]])
		} else {
			info.Season = atoi(name[m[4]:m[5]])
		}
		info.FullSeason = true
		titleEnd = m[0]
	}

	rest := name
	if titleEnd >= 0 {
		rest = name[titleEnd:]
		info.Title = name[:titleEnd]
	} else if m := reRes.FindStringIndex(name); m != nil {
		info.Title = name[:m[0]]
		// A year before the resolution ends a movie title, e.g.
		// "Past Lives (2023) [WEBRip-720p]".
		if ys := reYear.FindAllStringIndex(info.Title, -1); len(ys) > 0 && ys[len(ys)-1][0] > 0 {
			info.Title = info.Title[:ys[len(ys)-1][1]]
		}
	} else if m := reYear.FindAllStringIndex(name, -1); len(m) > 0 && m[len(m)-1][0] > 0 {
		// A movie without a resolution, e.g. "Heat.1995.BluRay.x264": the
		// title runs to the last year.
		last := m[len(m)-1]
		info.Title = name[:last[1]]
		rest = name[last[1]:]
	} else {
		info.Title = name
	}
	info.Title = reSpaces.ReplaceAllString(strings.TrimSpace(reSep.ReplaceAllString(info.Title, " ")), " ")
	info.Title = strings.Trim(info.Title, " -([")
	// A trailing year belongs to the title, e.g. "Doctor Who 2005".
	if m := reYear.FindAllStringIndex(info.Title, -1); len(m) > 0 {
		last := m[len(m)-1]
		if last[1] >= len(strings.TrimRight(info.Title, " )")) && last[0] > 0 {
			info.Year = atoi(info.Title[last[0]:last[1]])
			info.Title = strings.TrimSpace(strings.Trim(strings.TrimSpace(info.Title[:last[0]]), "("))
		}
	}

	if m := reRes.FindStringSubmatch(rest); m != nil {
		switch strings.ToLower(m[1]) {
		case "2160p", "4k", "uhd":
			info.Resolution = "2160p"
		case "1080p", "1080i":
			info.Resolution = "1080p"
		case "720p":
			info.Resolution = "720p"
		default:
			info.Resolution = "480p"
		}
	}
	switch {
	case reBluray.MatchString(rest):
		info.Source = "Bluray"
	case reWebRip.MatchString(rest):
		info.Source = "WEBRip"
	case reWebDL.MatchString(rest):
		info.Source = "WEBDL"
	case reHDTV.MatchString(rest):
		info.Source = "HDTV"
	case reDVD.MatchString(rest):
		info.Source = "DVD"
	}
	info.Proper = reProper.MatchString(rest)
	return info
}

// expandRange turns S01E01-E03 into 1, 2, 3.
func expandRange(eps []int, tail string) []int {
	if len(eps) == 2 && strings.Contains(tail, "-") && eps[1] > eps[0] && eps[1]-eps[0] < 30 {
		out := []int{}
		for e := eps[0]; e <= eps[1]; e++ {
			out = append(out, e)
		}
		return out
	}
	return eps
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

var videoExts = map[string]bool{".mkv": true, ".mp4": true, ".avi": true, ".m4v": true, ".ts": true, ".wmv": true, ".mov": true, ".webm": true}

func isVideoExt(ext string) bool { return videoExts[strings.ToLower(ext)] }

// IsVideo reports whether path looks like a video file.
func IsVideo(path string) bool { return isVideoExt(filepath.Ext(path)) }

var reNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// Normalize turns a title into a comparable key: lower case, "&" as "and",
// no punctuation, no leading "the".
func Normalize(title string) string {
	t := strings.ToLower(title)
	t = strings.ReplaceAll(t, "&", " and ")
	t = strings.ReplaceAll(t, "'", "")
	t = reNonAlnum.ReplaceAllString(t, " ")
	t = strings.TrimSpace(t)
	t = strings.TrimPrefix(t, "the ")
	return strings.ReplaceAll(t, " ", "")
}

// MatchesSeries reports whether a parsed release is for the series named
// title (first aired in year).
func MatchesSeries(info Info, title string, year int) bool {
	want := Normalize(title)
	got := Normalize(info.Title)
	if got == "" {
		return false
	}
	if info.Year != 0 && year != 0 && info.Year != year {
		return false
	}
	if got == want {
		return true
	}
	// "Show (2019)" as the series title, "Show" in the release, or the reverse.
	withoutYear := Normalize(reYear.ReplaceAllString(title, ""))
	return got == withoutYear || got+strconv.Itoa(year) == want
}

// MatchesMovie reports whether a parsed release is for the movie named
// title, released in year. Release years are often a year off from TMDB's
// (festival premieres, late releases elsewhere), so one year either way is
// accepted. A title that ends in a number that looks like a year, such as
// "Blade Runner 2049", is handled too.
func MatchesMovie(info Info, title string, year int) bool {
	want := Normalize(title)
	got := Normalize(info.Title)
	if got == "" {
		return false
	}
	near := func(y int) bool { return y == 0 || year == 0 || y >= year-1 && y <= year+1 }
	if got == want && near(info.Year) {
		return true
	}
	if info.Year != 0 && got+strconv.Itoa(info.Year) == want {
		return true
	}
	// "Movie (2019)" as the stored title.
	return got == Normalize(reYear.ReplaceAllString(title, "")) && near(info.Year)
}
