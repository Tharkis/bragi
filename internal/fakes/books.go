package fakes

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FakeBook is a fake Open Library work.
type FakeBook struct {
	ID       string // work key, e.g. "OL1000001W"
	Title    string
	Author   string
	Year     int
	Cover    int
	Color    string
	Overview string
}

// Books is the fake Open Library catalogue.
var Books = []FakeBook{
	{ID: "OL1000001W", Title: "Project Hail Mary", Author: "Andy Weir", Year: 2021, Cover: 900001, Color: "#1f5e7a",
		Overview: "Ryland Grace wakes up alone on a spaceship with no memory of who he is, and the fate of humanity on his shoulders."},
	{ID: "OL1000002W", Title: "Piranesi", Author: "Susanna Clarke", Year: 2020, Cover: 900002, Color: "#5b6e4a",
		Overview: "Piranesi lives in the House, a labyrinth of endless halls lined with statues, where the tides thunder up the staircases."},
	{ID: "OL1000003W", Title: "The Hobbit", Author: "J.R.R. Tolkien", Year: 1937, Cover: 900003, Color: "#7a4b1f",
		Overview: "Bilbo Baggins is swept into a quest to reclaim the dwarves' treasure from the dragon Smaug."},
	{ID: "OL1000004W", Title: "Dune", Author: "Frank Herbert", Year: 1965, Cover: 900004, Color: "#b0742a",
		Overview: "On the desert planet Arrakis, Paul Atreides is drawn into a struggle for the most valuable substance in the universe."},
	{ID: "OL1000005W", Title: "The Martian", Author: "Andy Weir", Year: 2011, Cover: 900005, Color: "#a3402a",
		Overview: "Stranded on Mars, astronaut Mark Watney has to science his way to survival."},
	{ID: "OL1000006W", Title: "Tomorrow, and Tomorrow, and Tomorrow", Author: "Gabrielle Zevin", Year: 2022, Cover: 900006, Color: "#3c4f8f",
		Overview: "Two friends meet as children in a hospital game room and go on to build video games together over thirty years."},
	{ID: "OL1000007W", Title: "The Way of Kings", Author: "Brandon Sanderson", Year: 2010, Cover: 900007, Color: "#2d4a6b",
		Overview: "The first book of the Stormlight Archive, set on a world of stone and storms."},
	{ID: "OL1000008W", Title: "Klara and the Sun", Author: "Kazuo Ishiguro", Year: 2021, Cover: 900008, Color: "#c79a2e",
		Overview: "An Artificial Friend watches the world from a store window, waiting to be chosen."},
}

func findBook(id string) *FakeBook {
	for i := range Books {
		if Books[i].ID == id {
			return &Books[i]
		}
	}
	return nil
}

func bookForQuery(q string) *FakeBook {
	q = strings.ToLower(q)
	for i := range Books {
		b := &Books[i]
		if strings.Contains(q, strings.ToLower(b.Title)) || strings.Contains(q, strings.ToLower(strings.SplitN(b.Title, ",", 2)[0])) {
			return b
		}
	}
	return nil
}

// ---- Open Library ----

// OpenLibrary serves the fake Open Library search, work and cover APIs.
func OpenLibrary() http.Handler {
	m := http.NewServeMux()
	doc := func(b FakeBook) map[string]any {
		return map[string]any{"key": "/works/" + b.ID, "title": b.Title, "author_name": []string{b.Author},
			"first_publish_year": b.Year, "cover_i": b.Cover}
	}
	m.HandleFunc("/search.json", func(w http.ResponseWriter, r *http.Request) {
		q := strings.ToLower(r.URL.Query().Get("q"))
		docs := []map[string]any{}
		if key, ok := strings.CutPrefix(q, "key:/works/"); ok {
			if b := findBook(strings.ToUpper(key)); b != nil {
				docs = append(docs, doc(*b))
			}
		} else {
			for _, b := range Books {
				hay := strings.ToLower(b.Title + " " + b.Author)
				all := true
				for _, w := range strings.Fields(q) {
					if !strings.Contains(hay, w) {
						all = false
					}
				}
				if all && q != "" {
					docs = append(docs, doc(b))
				}
			}
		}
		writeJSON(w, map[string]any{"numFound": len(docs), "docs": docs})
	})
	m.HandleFunc("/trending/weekly.json", func(w http.ResponseWriter, r *http.Request) {
		works := []map[string]any{}
		for _, b := range Books {
			works = append(works, doc(b))
		}
		writeJSON(w, map[string]any{"query": "/trending/weekly", "works": works, "days": 7, "hours": 0})
	})
	m.HandleFunc("/works/", func(w http.ResponseWriter, r *http.Request) {
		b := findBook(strings.TrimSuffix(filepath.Base(r.URL.Path), ".json"))
		if b == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{"key": "/works/" + b.ID, "title": b.Title, "covers": []int{b.Cover},
			"description": map[string]string{"type": "/type/text", "value": b.Overview + "\r\n----------\r\nAlso contained in: ..."}})
	})
	reCover := regexp.MustCompile(`^/b/id/(\d+)-[SML]\.jpg$`)
	m.HandleFunc("/b/id/", func(w http.ResponseWriter, r *http.Request) {
		mm := reCover.FindStringSubmatch(r.URL.Path)
		if mm == nil {
			http.NotFound(w, r)
			return
		}
		n, _ := strconv.Atoi(mm[1])
		var b *FakeBook
		for i := range Books {
			if Books[i].Cover == n {
				b = &Books[i]
			}
		}
		if b == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		var lines strings.Builder
		for i, word := range strings.Fields(b.Title) {
			fmt.Fprintf(&lines, `<text x="24" y="%d" font-family="Georgia, serif" font-size="32" fill="#fff">%s</text>`, 90+i*38, html.EscapeString(word))
		}
		fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 300 450"><rect width="300" height="450" fill="%s"/>`+
			`<rect x="12" y="12" width="276" height="426" fill="none" stroke="#fff" stroke-opacity="0.35" stroke-width="2"/>%s`+
			`<text x="24" y="410" font-family="Helvetica, Arial, sans-serif" font-size="18" fill="#fff" opacity="0.85">%s</text></svg>`,
			b.Color, lines.String(), html.EscapeString(strings.ToUpper(b.Author)))
	})
	return m
}

// bookReleases is what the fake Prowlarr returns for a book search.
func bookReleases(q string, cats []string, base string) []map[string]any {
	out := []map[string]any{}
	b := bookForQuery(q)
	if b == nil {
		return out
	}
	audio := false
	for _, c := range cats {
		if strings.HasPrefix(c, "30") {
			audio = true
		}
	}
	mb := int64(1 << 20)
	add := func(title, protocol string, size int64, seeders int, age time.Duration) {
		out = append(out, map[string]any{"title": title, "guid": title, "size": size, "seeders": seeders, "protocol": protocol,
			"indexer":     map[string]string{"usenet": "NZBgeek", "torrent": "MyAnonamouse"}[protocol],
			"publishDate": time.Now().Add(-age).UTC().Format(time.RFC3339),
			"downloadUrl": base + "/dl/" + protocol + "/" + url.PathEscape(title)})
	}
	dotted := strings.ReplaceAll(b.Author+" - "+b.Title, " ", ".")
	if audio {
		add(fmt.Sprintf("%s - %s (Unabridged) [M4B]", b.Author, b.Title), "usenet", 820*mb, 0, 30*24*time.Hour)
		add(fmt.Sprintf("%s - %s [MP3 64kbps]", b.Author, b.Title), "torrent", 460*mb, 37, 90*24*time.Hour)
		add(fmt.Sprintf("%s - %s (%d) [EPUB]", b.Author, b.Title, b.Year), "torrent", 3*mb, 120, 200*24*time.Hour)
		return out
	}
	add(fmt.Sprintf("%s - %s (%d) [EPUB]", b.Author, b.Title, b.Year), "usenet", 3*mb, 0, 40*24*time.Hour)
	add(fmt.Sprintf("%s.%d.RETAIL.EPUB.eBook-BitBook", dotted, b.Year), "torrent", 4*mb, 64, 300*24*time.Hour)
	add(fmt.Sprintf("%s - %s (%d) [PDF]", b.Author, b.Title, b.Year), "torrent", 11*mb, 22, 500*24*time.Hour)
	add(fmt.Sprintf("%s - %s (Unabridged) [M4B]", b.Author, b.Title), "usenet", 820*mb, 0, 30*24*time.Hour)
	if b.Title != "The Martian" {
		add(fmt.Sprintf("%s - The Martian (2014) [EPUB]", b.Author), "usenet", 2*mb, 0, 400*24*time.Hour)
	}
	return out
}

// bookFiles is what a finished fake download of a book release contains,
// or nil if name is not a book release.
func bookFiles(name string) map[string]string {
	l := strings.ToLower(name)
	switch {
	case strings.Contains(l, "epub"):
		return map[string]string{name + ".epub": "fake epub", name + ".nfo": "nfo"}
	case strings.Contains(l, "pdf"):
		return map[string]string{name + ".pdf": "fake pdf"}
	case strings.Contains(l, "m4b"):
		return map[string]string{name + ".m4b": "fake m4b", "cover.jpg": "jpg"}
	case strings.Contains(l, "mp3"):
		files := map[string]string{}
		for i := 1; i <= 6; i++ {
			files[fmt.Sprintf("CD%d/%02d - Chapter %d.mp3", (i-1)/3+1, i, i)] = "fake mp3"
		}
		return files
	}
	return nil
}

// ---- slskd ----

// Slskd is a fake slskd. Every search finds a few peers sharing the books
// in Books; a download finishes after Duration and writes its files under
// Dir/<remote folder name>/.
type Slskd struct {
	Dir      string
	Duration time.Duration
	// Key is the API key it accepts.
	Key string

	mu        sync.Mutex
	searches  map[string]string // id → text
	transfers []*slskTransfer
	next      int
}

type slskTransfer struct {
	id       string
	user     string
	filename string
	size     int64
	started  time.Time
	removed  bool
	written  bool
}

func (s *Slskd) peers(text string) []map[string]any {
	if music := musicPeers(text); music != nil {
		return music
	}
	b := bookForQuery(text)
	if b == nil {
		return []map[string]any{}
	}
	mb := int64(1 << 20)
	file := func(name string, size int64) map[string]any {
		return map[string]any{"filename": name, "size": size, "extension": strings.TrimPrefix(filepath.Ext(name), ".")}
	}
	base := `@@books\` + b.Author + `\` + b.Title + ` (` + strconv.Itoa(b.Year) + `)\`
	audio := `@@audio\Audiobooks\` + b.Author + `\` + b.Title + `\`
	var tracks []map[string]any
	for i := 1; i <= 12; i++ {
		tracks = append(tracks, file(fmt.Sprintf("%s%02d - %s.mp3", audio, i, b.Title), 38*mb))
	}
	return []map[string]any{
		{"username": "bookworm42", "hasFreeUploadSlot": true, "uploadSpeed": 2400000, "queueLength": 0,
			"files": []map[string]any{file(base+b.Title+" - "+b.Author+".epub", 2*mb), file(base+b.Title+" - "+b.Author+".mobi", 3*mb), file(base+"cover.jpg", mb/4)}},
		{"username": "readinglamp", "hasFreeUploadSlot": false, "uploadSpeed": 900000, "queueLength": 14,
			"files": append(tracks, file(audio+"cover.jpg", mb/4))},
		{"username": "nightowl", "hasFreeUploadSlot": true, "uploadSpeed": 600000, "queueLength": 0,
			"files": []map[string]any{file(`@@share\Audiobooks\`+b.Author+` - `+b.Title+`\`+b.Title+`.m4b`, 780*mb)}},
	}
}

func (s *Slskd) progress(t *slskTransfer) float64 {
	p := float64(time.Since(t.started)) / float64(s.Duration)
	if p < 1 {
		return p
	}
	if !t.written {
		t.written = true
		parts := strings.Split(t.filename, `\`)
		dir := filepath.Join(s.Dir, parts[len(parts)-2])
		os.MkdirAll(dir, 0o755)
		body := []byte("fake " + parts[len(parts)-1])
		if strings.HasSuffix(t.filename, ".flac") {
			body = FakeFLAC()
		}
		os.WriteFile(filepath.Join(dir, parts[len(parts)-1]), body, 0o644)
	}
	return 1
}

// Handler serves the fake slskd API.
func (s *Slskd) Handler() http.Handler {
	m := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if s.Key != "" && r.Header.Get("X-API-Key") != s.Key {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	m.HandleFunc("GET /api/v0/application", auth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"version": map[string]any{"full": "0.22.2"}, "server": map[string]any{"isConnected": true, "state": "Connected, LoggedIn"}})
	}))
	m.HandleFunc("GET /api/v0/options", auth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"directories": map[string]any{"downloads": s.Dir, "incomplete": s.Dir + "/incomplete"}})
	}))
	m.HandleFunc("POST /api/v0/searches", auth(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID         string `json:"id"`
			SearchText string `json:"searchText"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		s.mu.Lock()
		if s.searches == nil {
			s.searches = map[string]string{}
		}
		s.searches[in.ID] = in.SearchText
		s.mu.Unlock()
		writeJSON(w, map[string]any{"id": in.ID, "searchText": in.SearchText, "isComplete": false})
	}))
	m.HandleFunc("GET /api/v0/searches/{id}", auth(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"id": r.PathValue("id"), "isComplete": true, "state": "Completed, TimedOut"})
	}))
	m.HandleFunc("GET /api/v0/searches/{id}/responses", auth(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		text := s.searches[r.PathValue("id")]
		s.mu.Unlock()
		writeJSON(w, s.peers(text))
	}))
	m.HandleFunc("DELETE /api/v0/searches/{id}", auth(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	m.HandleFunc("POST /api/v0/transfers/downloads/{user}", auth(func(w http.ResponseWriter, r *http.Request) {
		var files []struct {
			Filename string `json:"filename"`
			Size     int64  `json:"size"`
		}
		json.NewDecoder(r.Body).Decode(&files)
		s.mu.Lock()
		for _, f := range files {
			s.next++
			s.transfers = append(s.transfers, &slskTransfer{id: fmt.Sprintf("00000000-0000-4000-a000-%012d", s.next), user: r.PathValue("user"),
				filename: f.Filename, size: f.Size, started: time.Now()})
		}
		s.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	}))
	m.HandleFunc("GET /api/v0/transfers/downloads", auth(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		users := map[string]map[string][]map[string]any{}
		var order []string
		for _, t := range s.transfers {
			if t.removed {
				continue
			}
			if users[t.user] == nil {
				users[t.user] = map[string][]map[string]any{}
				order = append(order, t.user)
			}
			dir := t.filename[:strings.LastIndex(t.filename, `\`)]
			p := s.progress(t)
			state := "InProgress"
			if p >= 1 {
				state, p = "Completed, Succeeded", 1
			}
			users[t.user][dir] = append(users[t.user][dir], map[string]any{"id": t.id, "username": t.user, "filename": t.filename,
				"size": t.size, "state": state, "percentComplete": p * 100, "bytesTransferred": int64(float64(t.size) * p)})
		}
		out := []map[string]any{}
		for _, u := range order {
			var dirs []map[string]any
			for d, files := range users[u] {
				dirs = append(dirs, map[string]any{"directory": d, "fileCount": len(files), "files": files})
			}
			out = append(out, map[string]any{"username": u, "directories": dirs})
		}
		writeJSON(w, out)
	}))
	m.HandleFunc("DELETE /api/v0/transfers/downloads/{user}/{id}", auth(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		for _, t := range s.transfers {
			if t.id == r.PathValue("id") && r.URL.Query().Get("remove") == "true" {
				t.removed = true
			}
		}
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	return m
}
