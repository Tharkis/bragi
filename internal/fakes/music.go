package fakes

import (
	"encoding/binary"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// FakeArtist is a fake MusicBrainz artist.
type FakeArtist struct {
	ID       string
	Name     string
	SortName string
	Type     string
	Country  string
	Genres   []string
	Photo    string // colour of the fake photo; "" for none
	Albums   []FakeAlbum
}

// FakeAlbum is a fake release group with one release.
type FakeAlbum struct {
	ID        string // release group
	ReleaseID string
	Title     string
	Type      string // Album, EP, Single
	Secondary []string
	Date      string
	Color     string
	Tracks    []string
	// Discs splits Tracks into discs of these sizes; nil means one disc.
	Discs []int
}

// mbid makes a stable MusicBrainz-shaped id from a name.
func mbid(name string) string {
	h1, h2 := hashString(name), hashString(name+"#")
	s := fmt.Sprintf("%016x%016x", h1, h2)
	return s[0:8] + "-" + s[8:12] + "-4" + s[13:16] + "-a" + s[17:20] + "-" + s[20:32]
}

func album(artist, title, typ, date, color string, tracks ...string) FakeAlbum {
	return FakeAlbum{ID: mbid("rg:" + artist + title), ReleaseID: mbid("rel:" + artist + title), Title: title, Type: typ, Date: date,
		Color: color, Tracks: tracks}
}

func numbered(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("Track %d", i+1)
	}
	return out
}

// Artists is the fake MusicBrainz catalogue.
var Artists = []FakeArtist{
	{ID: mbid("Radiohead"), Name: "Radiohead", SortName: "Radiohead", Type: "Group", Country: "GB", Genres: []string{"alternative rock", "art rock"},
		Photo: "#33415c", Albums: []FakeAlbum{
			album("Radiohead", "Creep", "Single", "1992-09-21", "#6b2737", "Creep", "Lurgee", "Inside My Head", "Million Dollar Question"),
			album("Radiohead", "Pablo Honey", "Album", "1993-02-22", "#d17a22", numbered(12)...),
			album("Radiohead", "My Iron Lung", "EP", "1994-09-26", "#3d5a80", numbered(8)...),
			album("Radiohead", "The Bends", "Album", "1995-03-13", "#9a8c98", numbered(12)...),
			album("Radiohead", "OK Computer", "Album", "1997-05-21", "#4a7c9b", "Airbag", "Paranoid Android", "Subterranean Homesick Alien",
				"Exit Music (For a Film)", "Let Down", "Karma Police", "Fitter Happier", "Electioneering", "Climbing Up the Walls",
				"No Surprises", "Lucky", "The Tourist"),
			album("Radiohead", "Kid A", "Album", "2000-10-02", "#b23a48", "Everything in Its Right Place", "Kid A", "The National Anthem",
				"How to Disappear Completely", "Treefingers", "Optimistic", "In Limbo", "Idioteque", "Morning Bell", "Motion Picture Soundtrack"),
			{ID: mbid("rg:RadioheadI Might Be Wrong"), ReleaseID: mbid("rel:RadioheadI Might Be Wrong"), Title: "I Might Be Wrong: Live Recordings",
				Type: "Album", Secondary: []string{"Live"}, Date: "2001-11-12", Color: "#2a2a2a", Tracks: numbered(8)},
			album("Radiohead", "In Rainbows", "Album", "2007-10-10", "#e09f3e", "15 Step", "Bodysnatchers", "Nude", "Weird Fishes/Arpeggi",
				"All I Need", "Faust Arp", "Reckoner", "House of Cards", "Jigsaw Falling into Place", "Videotape"),
			album("Radiohead", "A Moon Shaped Pool", "Album", "2016-05-08", "#c9c9c9", numbered(11)...),
		}},
	{ID: mbid("Daft Punk"), Name: "Daft Punk", SortName: "Daft Punk", Type: "Group", Country: "FR", Genres: []string{"house", "electronic"},
		Photo: "#7b2cbf", Albums: []FakeAlbum{
			album("Daft Punk", "Homework", "Album", "1997-01-20", "#111111", numbered(16)...),
			album("Daft Punk", "Discovery", "Album", "2001-03-12", "#5a189a", "One More Time", "Aerodynamic", "Digital Love",
				"Harder, Better, Faster, Stronger", "Crescendolls", "Nightvision", "Superheroes", "High Life", "Something About Us",
				"Voyager", "Veridis Quo", "Short Circuit", "Face to Face", "Too Long"),
			{ID: mbid("rg:Daft PunkAlive 2007"), ReleaseID: mbid("rel:Daft PunkAlive 2007"), Title: "Alive 2007", Type: "Album",
				Secondary: []string{"Live"}, Date: "2007-11-19", Color: "#ff006e", Tracks: numbered(12)},
			album("Daft Punk", "Random Access Memories", "Album", "2013-05-17", "#bfa181", "Give Life Back to Music", "The Game of Love",
				"Giorgio by Moroder", "Within", "Instant Crush", "Lose Yourself to Dance", "Touch", "Get Lucky", "Beyond", "Motherboard",
				"Fragments of Time", "Doin' It Right", "Contact"),
		}},
	{ID: mbid("Fleetwood Mac"), Name: "Fleetwood Mac", SortName: "Fleetwood Mac", Type: "Group", Country: "GB", Genres: []string{"rock", "soft rock"},
		Albums: []FakeAlbum{
			album("Fleetwood Mac", "Rumours", "Album", "1977-02-04", "#8d6e63", "Second Hand News", "Dreams", "Never Going Back Again",
				"Don't Stop", "Go Your Own Way", "Songbird", "The Chain", "You Make Loving Fun", "I Don't Want to Know", "Oh Daddy", "Gold Dust Woman"),
			album("Fleetwood Mac", "Tusk", "Album", "1979-10-12", "#455a64", numbered(20)...),
		}},
	{ID: mbid("Phoebe Bridgers"), Name: "Phoebe Bridgers", SortName: "Bridgers, Phoebe", Type: "Person", Country: "US", Genres: []string{"indie folk"},
		Photo: "#264653", Albums: []FakeAlbum{
			album("Phoebe Bridgers", "Stranger in the Alps", "Album", "2017-09-22", "#e9c46a", numbered(11)...),
			album("Phoebe Bridgers", "Punisher", "Album", "2020-06-18", "#2a9d8f", "DVD Menu", "Garden Song", "Kyoto", "Punisher",
				"Halloween", "Chinese Satellite", "Moon Song", "Savior Complex", "ICU", "Graceland Too", "I Know the End"),
			album("Phoebe Bridgers", "Copycat Killer", "EP", "2020-11-20", "#e76f51", numbered(4)...),
		}},
}

func init() {
	// The Bends... is fine on one disc; give one album two discs so the
	// disc folders are exercised.
	for i := range Artists {
		for j := range Artists[i].Albums {
			if Artists[i].Albums[j].Title == "Tusk" {
				Artists[i].Albums[j].Discs = []int{10, 10}
			}
		}
	}
}

func findArtist(id string) *FakeArtist {
	for i := range Artists {
		if Artists[i].ID == id {
			return &Artists[i]
		}
	}
	return nil
}

// FindAlbum returns a fake album by release group or release id, with its
// artist.
func FindAlbum(id string) (*FakeArtist, *FakeAlbum) {
	for i := range Artists {
		for j := range Artists[i].Albums {
			al := &Artists[i].Albums[j]
			if al.ID == id || al.ReleaseID == id {
				return &Artists[i], al
			}
		}
	}
	return nil, nil
}

// AlbumForQuery finds the fake album a search text names, like
// "Radiohead OK Computer".
func AlbumForQuery(q string) (*FakeArtist, *FakeAlbum) {
	q = strings.ToLower(q)
	var bestAr *FakeArtist
	var best *FakeAlbum
	for i := range Artists {
		ar := &Artists[i]
		if !strings.Contains(q, strings.ToLower(ar.Name)) {
			continue
		}
		for j := range ar.Albums {
			al := &ar.Albums[j]
			t := strings.ToLower(strings.SplitN(al.Title, ":", 2)[0])
			if strings.Contains(q, t) && (best == nil || len(al.Title) > len(best.Title)) {
				bestAr, best = ar, al
			}
		}
	}
	return bestAr, best
}

func credit(ar *FakeArtist) []map[string]any {
	return []map[string]any{{"name": ar.Name, "joinphrase": "", "artist": map[string]any{"id": ar.ID, "name": ar.Name, "sort-name": ar.SortName}}}
}

func groupJSON(ar *FakeArtist, al FakeAlbum, score int) map[string]any {
	sec := al.Secondary
	if sec == nil {
		sec = []string{}
	}
	return map[string]any{"id": al.ID, "title": al.Title, "primary-type": al.Type, "secondary-types": sec,
		"first-release-date": al.Date, "artist-credit": credit(ar), "score": score}
}

var (
	reField = regexp.MustCompile(`(releasegroup|artist):\(([^)]*)\)`)
	reUnesc = regexp.MustCompile(`\\(.)`)
)

// MusicBrainz serves the fake MusicBrainz API under /ws/2, the Cover Art
// Archive under /release-group, ListenBrainz under /1 and artist photos
// under /wiki.
func MusicBrainz() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /ws/2/artist", func(w http.ResponseWriter, r *http.Request) {
		q := strings.ToLower(reUnesc.ReplaceAllString(r.URL.Query().Get("query"), "$1"))
		out := []map[string]any{}
		for _, ar := range Artists {
			name := strings.ToLower(ar.Name)
			if q != "" && (strings.Contains(name, q) || strings.Contains(q, name)) {
				out = append(out, map[string]any{"id": ar.ID, "name": ar.Name, "sort-name": ar.SortName, "type": ar.Type,
					"country": ar.Country, "score": 100})
			}
		}
		writeJSON(w, map[string]any{"artists": out, "count": len(out)})
	})
	m.HandleFunc("GET /ws/2/artist/{id}", func(w http.ResponseWriter, r *http.Request) {
		ar := findArtist(r.PathValue("id"))
		if ar == nil {
			http.Error(w, `{"error":"Not Found"}`, http.StatusNotFound)
			return
		}
		rels := []map[string]any{}
		if ar.Photo != "" {
			rels = append(rels, map[string]any{"type": "image", "url": map[string]any{"resource": "https://commons.wikimedia.org/wiki/File:" + url.PathEscape(ar.Name) + ".jpg"}})
		}
		genres := []map[string]any{}
		for i, g := range ar.Genres {
			genres = append(genres, map[string]any{"name": g, "count": 10 - i})
		}
		writeJSON(w, map[string]any{"id": ar.ID, "name": ar.Name, "sort-name": ar.SortName, "type": ar.Type, "country": ar.Country,
			"relations": rels, "genres": genres})
	})
	m.HandleFunc("GET /ws/2/release-group", func(w http.ResponseWriter, r *http.Request) {
		if id := r.URL.Query().Get("artist"); id != "" {
			ar := findArtist(id)
			out := []map[string]any{}
			if ar != nil {
				for _, al := range ar.Albums {
					out = append(out, groupJSON(ar, al, 0))
				}
			}
			writeJSON(w, map[string]any{"release-groups": out, "release-group-count": len(out)})
			return
		}
		q := reUnesc.ReplaceAllString(r.URL.Query().Get("query"), "$1")
		title, artist := strings.ToLower(q), ""
		if fs := reField.FindAllStringSubmatch(q, -1); fs != nil {
			title = ""
			for _, f := range fs {
				if f[1] == "artist" {
					artist = strings.ToLower(f[2])
				} else {
					title = strings.ToLower(f[2])
				}
			}
		}
		out := []map[string]any{}
		for i := range Artists {
			ar := &Artists[i]
			if artist != "" && !strings.Contains(strings.ToLower(ar.Name), artist) {
				continue
			}
			for _, al := range ar.Albums {
				t := strings.ToLower(al.Title)
				if title != "" && (strings.Contains(t, title) || strings.Contains(title, t)) {
					out = append(out, groupJSON(ar, al, 100))
				}
			}
		}
		writeJSON(w, map[string]any{"release-groups": out})
	})
	m.HandleFunc("GET /ws/2/release-group/{id}", func(w http.ResponseWriter, r *http.Request) {
		ar, al := FindAlbum(r.PathValue("id"))
		if al == nil || al.ID != r.PathValue("id") {
			http.Error(w, `{"error":"Not Found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, groupJSON(ar, *al, 0))
	})
	m.HandleFunc("GET /ws/2/release", func(w http.ResponseWriter, r *http.Request) {
		_, al := FindAlbum(r.URL.Query().Get("release-group"))
		out := []map[string]any{}
		if al != nil {
			out = append(out, map[string]any{"id": al.ReleaseID, "title": al.Title, "status": "Official", "date": al.Date, "country": "XW",
				"media": []map[string]any{{"position": 1, "format": "Digital Media", "track-count": len(al.Tracks)}}})
			// A bootleg with fewer tracks, which must not be picked.
			out = append(out, map[string]any{"id": mbid("boot:" + al.ID), "title": al.Title, "status": "Bootleg", "date": al.Date,
				"media": []map[string]any{{"position": 1, "format": "CD-R", "track-count": 3}}})
		}
		writeJSON(w, map[string]any{"releases": out, "release-count": len(out)})
	})
	m.HandleFunc("GET /ws/2/release/{id}", func(w http.ResponseWriter, r *http.Request) {
		ar, al := FindAlbum(r.PathValue("id"))
		if al == nil || al.ReleaseID != r.PathValue("id") {
			http.Error(w, `{"error":"Not Found"}`, http.StatusNotFound)
			return
		}
		writeJSON(w, releaseJSON(ar, al))
	})
	m.HandleFunc("GET /release-group/{id}/{size}", func(w http.ResponseWriter, r *http.Request) {
		ar, al := FindAlbum(r.PathValue("id"))
		if al == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		fmt.Fprint(w, albumSVG(ar, al))
	})
	m.HandleFunc("GET /wiki/Special:FilePath/{name}", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSuffix(r.PathValue("name"), ".jpg")
		for _, ar := range Artists {
			if ar.Name == name {
				w.Header().Set("Content-Type", "image/svg+xml")
				fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 400"><rect width="400" height="400" fill="%s"/>`+
					`<circle cx="200" cy="160" r="70" fill="#fff" fill-opacity="0.25"/><path d="M80 400 C 90 270, 310 270, 320 400 Z" fill="#fff" fill-opacity="0.25"/>`+
					`<text x="200" y="370" text-anchor="middle" font-family="Helvetica, Arial, sans-serif" font-size="26" fill="#fff">%s</text></svg>`,
					ar.Photo, html.EscapeString(ar.Name))
				return
			}
		}
		http.NotFound(w, r)
	})
	m.HandleFunc("GET /1/stats/sitewide/release-groups", func(w http.ResponseWriter, r *http.Request) {
		var out []map[string]any
		for _, pick := range []string{"In Rainbows", "Random Access Memories", "Rumours", "Punisher", "OK Computer", "Discovery", "Kid A"} {
			for i := range Artists {
				ar := &Artists[i]
				for _, al := range ar.Albums {
					if al.Title == pick {
						out = append(out, map[string]any{"artist_name": ar.Name, "artist_mbids": []string{ar.ID}, "release_group_mbid": al.ID,
							"release_group_name": al.Title, "listen_count": 1000})
					}
				}
			}
		}
		writeJSON(w, map[string]any{"payload": map[string]any{"release_groups": out}})
	})
	return m
}

func releaseJSON(ar *FakeArtist, al *FakeAlbum) map[string]any {
	discs := al.Discs
	if discs == nil {
		discs = []int{len(al.Tracks)}
	}
	var media []map[string]any
	n := 0
	for d, size := range discs {
		var tracks []map[string]any
		for i := 0; i < size; i++ {
			title := al.Tracks[n]
			tracks = append(tracks, map[string]any{"position": i + 1, "number": strconv.Itoa(i + 1), "title": title,
				"length": 180000 + 13000*((n*7)%11), "recording": map[string]any{"id": mbid("rec:" + al.ID + title), "title": title}})
			n++
		}
		media = append(media, map[string]any{"position": d + 1, "format": "Digital Media", "track-count": size, "tracks": tracks})
	}
	return map[string]any{"id": al.ReleaseID, "title": al.Title, "status": "Official", "date": al.Date, "artist-credit": credit(ar),
		"release-group": map[string]any{"id": al.ID}, "media": media}
}

func albumSVG(ar *FakeArtist, al *FakeAlbum) string {
	var lines strings.Builder
	for i, word := range strings.Fields(al.Title) {
		if i == 4 {
			break
		}
		fmt.Fprintf(&lines, `<text x="28" y="%d" font-family="Georgia, serif" font-size="40" fill="#fff">%s</text>`, 70+i*46, html.EscapeString(word))
	}
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 400"><rect width="400" height="400" fill="%s"/>`+
		`<circle cx="300" cy="290" r="140" fill="#fff" fill-opacity="0.12"/><circle cx="300" cy="290" r="60" fill="#000" fill-opacity="0.15"/>%s`+
		`<text x="28" y="372" font-family="Helvetica, Arial, sans-serif" font-size="20" fill="#fff" opacity="0.85">%s</text></svg>`,
		al.Color, lines.String(), html.EscapeString(strings.ToUpper(ar.Name)))
}

// FakeFLAC is the smallest file the FLAC tag reader and writer accept: the
// "fLaC" marker, an empty STREAMINFO block and a frame sync code.
func FakeFLAC() []byte {
	b := []byte("fLaC")
	hdr := make([]byte, 4)
	binary.BigEndian.PutUint32(hdr, 34)
	hdr[0] = 0x80 // last block, type STREAMINFO
	b = append(b, hdr...)
	b = append(b, make([]byte, 34)...)
	// The start of an audio frame: its sync code.
	return append(b, 0xFF, 0xF8, 0x69, 0x08, 0x00, 0x00)
}

// musicReleases is what the fake Prowlarr returns for a music search.
func musicReleases(q, base string) []map[string]any {
	out := []map[string]any{}
	ar, al := AlbumForQuery(q)
	if al == nil {
		return out
	}
	mb := int64(1 << 20)
	year := al.Date[:4]
	add := func(title, protocol string, size int64, seeders int, age time.Duration) {
		out = append(out, map[string]any{"title": title, "guid": title, "size": size, "seeders": seeders, "protocol": protocol,
			"indexer":     map[string]string{"usenet": "NZBgeek", "torrent": "RuTracker"}[protocol],
			"publishDate": time.Now().Add(-age).UTC().Format(time.RFC3339),
			"downloadUrl": base + "/dl/" + protocol + "/" + url.PathEscape(title)})
	}
	add(fmt.Sprintf("%s - %s (%s) [FLAC] {WEB}", ar.Name, al.Title, year), "usenet", 380*mb, 0, 60*24*time.Hour)
	add(fmt.Sprintf("%s - %s (%s) [MP3 320]", ar.Name, al.Title, year), "torrent", 120*mb, 54, 400*24*time.Hour)
	add(fmt.Sprintf("%s - Discography (%s-2016) [FLAC]", ar.Name, ar.Albums[0].Date[:4]), "torrent", 9*1024*mb, 12, 900*24*time.Hour)
	add(fmt.Sprintf("%s - %s (%s) [MP3 128]", ar.Name, al.Title, year), "usenet", 50*mb, 0, 2000*24*time.Hour)
	return out
}

// musicFiles is what a finished fake download of a music release
// contains, or nil if name is not one.
func musicFiles(name string) map[string][]byte {
	_, al := AlbumForQuery(strings.ReplaceAll(name, ".", " "))
	if al == nil {
		return nil
	}
	ext := ".mp3"
	if strings.Contains(strings.ToLower(name), "flac") {
		ext = ".flac"
	}
	files := map[string][]byte{"cover.jpg": []byte("jpg")}
	for i, t := range al.Tracks {
		body := []byte("fake mp3")
		if ext == ".flac" {
			body = FakeFLAC()
		}
		files[fmt.Sprintf("%02d. %s%s", i+1, strings.ReplaceAll(t, "/", "-"), ext)] = body
	}
	return files
}

// musicPeers is what the fake slskd finds for an album search.
func musicPeers(text string) []map[string]any {
	ar, al := AlbumForQuery(text)
	if al == nil {
		return nil
	}
	mb := int64(1 << 20)
	file := func(name string, size int64, bitrate int, vbr bool) map[string]any {
		ext := name[strings.LastIndex(name, ".")+1:]
		f := map[string]any{"filename": name, "size": size, "extension": ext}
		if bitrate > 0 {
			f["bitRate"], f["isVariableBitRate"] = bitrate, vbr
		}
		return f
	}
	folder := func(root, ext string, size int64, bitrate int, vbr bool, skip int) []map[string]any {
		var fs []map[string]any
		for i, t := range al.Tracks {
			if i < skip {
				continue
			}
			fs = append(fs, file(fmt.Sprintf(`%s%02d - %s.%s`, root, i+1, strings.ReplaceAll(t, "/", "-"), ext), size, bitrate, vbr))
		}
		return append(fs, file(root+"cover.jpg", mb/2, 0, false))
	}
	year := al.Date[:4]
	return []map[string]any{
		{"username": "losslesslibrary", "hasFreeUploadSlot": true, "uploadSpeed": 4800000, "queueLength": 0,
			"files": folder(`@@music\`+ar.Name+`\`+year+` - `+al.Title+`\`, "flac", 32*mb, 0, false, 0)},
		{"username": "vinylcrate", "hasFreeUploadSlot": false, "uploadSpeed": 1200000, "queueLength": 6,
			"files": folder(`@@share\Music\`+ar.Name+`\`+al.Title+` (`+year+`)\`, "mp3", 9*mb, 320, false, 0)},
		{"username": "halfcollection", "hasFreeUploadSlot": true, "uploadSpeed": 900000, "queueLength": 0,
			"files": folder(`@@mp3\`+ar.Name+` - `+al.Title+`\`, "mp3", 7*mb, 245, true, 2)},
	}
}
