// Package fakes provides in-process stand-ins for TMDB, Prowlarr, NZBGet
// and qBittorrent. Tests use them to run the whole TV pipeline without a
// network, and `go run ./internal/fakes/cmd` serves them for UI work.
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

// Show is a fake TMDB show.
type Show struct {
	ID       int
	Name     string
	First    string // first air date
	Overview string
	Color    string // poster colour
	TVDB     int
	Seasons  []FakeSeason
}

// FakeSeason is a season with episode air dates.
type FakeSeason struct {
	Number   int
	Episodes []FakeEpisode
}

// FakeEpisode is one episode.
type FakeEpisode struct {
	Title string
	Air   string
}

// eps builds n episodes airing weekly from start.
func eps(start string, titles ...string) []FakeEpisode {
	t, _ := time.Parse("2006-01-02", start)
	out := make([]FakeEpisode, len(titles))
	for i, title := range titles {
		out[i] = FakeEpisode{Title: title, Air: t.AddDate(0, 0, 7*i).Format("2006-01-02")}
	}
	return out
}

// Shows is the fake catalogue.
var Shows = []Show{
	{ID: 95396, Name: "Severance", First: "2022-02-17", TVDB: 371980, Color: "#2f6f8f",
		Overview: "Mark leads a team of office workers whose memories have been surgically divided between their work and personal lives.",
		Seasons: []FakeSeason{
			{1, eps("2022-02-17", "Good News About Hell", "Half Loop", "In Perpetuity", "The You You Are", "The Grim Barbarity of Optics and Design", "Hide and Seek", "Defiant Jazz", "What's for Dinner?", "The We We Are")},
			{2, eps("2025-01-17", "Hello, Ms. Cobel", "Goodbye, Mrs. Selvig", "Who Is Alive?", "Woe's Hollow", "Trojan's Horse", "Attila", "Chikhai Bardo", "Sweet Vitriol", "The After Hours", "Cold Harbor")},
		}},
	{ID: 95480, Name: "Slow Horses", First: "2022-04-01", TVDB: 401206, Color: "#6b4f2a",
		Overview: "A dysfunctional team of MI5 agents who have been banished to Slough House.",
		Seasons: []FakeSeason{
			{1, eps("2022-04-01", "Failure's Contagious", "Work Drinks", "Visiting Hours", "Bad Tradecraft", "Uppers", "Follies")},
			{2, eps("2022-12-02", "Last Stop", "From Upshott With Love", "Cicada", "Drinking Games", "Grave Danger", "Old Bones")},
		}},
	{ID: 82728, Name: "Bluey", First: "2018-10-01", TVDB: 353546, Color: "#3a86c8",
		Overview: "Bluey is an inexhaustible six year-old Blue Heeler dog who loves to play.",
		Seasons: []FakeSeason{
			{1, eps("2018-10-01", "Magic Xylophone", "Hospital", "Keepy Uppy", "Daddy Robot")},
		}},
	{ID: 136315, Name: "The Bear", First: "2022-06-23", TVDB: 403245, Color: "#a23b2a",
		Overview: "A young chef from the fine dining world returns to Chicago to run his family's sandwich shop.",
		Seasons: []FakeSeason{
			{1, eps("2022-06-23", "System", "Hands", "Brigade", "Dogs", "Sheridan", "Ceres", "Review", "Braciole")},
		}},
	{ID: 202411, Name: "Night Shift Diaries", First: "2026-10-30", TVDB: 500001, Color: "#4b3f72",
		Overview: "Paramedics on the late shift in a city that never sleeps.",
		Seasons: []FakeSeason{
			{1, eps("2026-10-30", "Pilot", "Crosstown", "Code Three", "Overtime", "Daybreak", "Signal")},
		}},
}

func findShow(id int) *Show {
	for i := range Shows {
		if Shows[i].ID == id {
			return &Shows[i]
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// ---- TMDB ----

// TMDB serves the fake TMDB API and SVG posters under /t/p/.
func TMDB() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/3/configuration", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") == "bad" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"images": map[string]any{}})
	})
	m.HandleFunc("/3/search/tv", func(w http.ResponseWriter, r *http.Request) {
		q := strings.ToLower(r.URL.Query().Get("query"))
		results := []map[string]any{}
		for _, s := range Shows {
			if strings.Contains(strings.ToLower(s.Name), q) || strings.Contains(q, strings.ToLower(s.Name)) {
				results = append(results, map[string]any{"id": s.ID, "name": s.Name, "first_air_date": s.First,
					"overview": s.Overview, "poster_path": fmt.Sprintf("/%d.svg", s.ID)})
			}
		}
		writeJSON(w, map[string]any{"results": results})
	})
	reSeason := regexp.MustCompile(`^/3/tv/(\d+)/season/(\d+)$`)
	reShow := regexp.MustCompile(`^/3/tv/(\d+)$`)
	m.HandleFunc("/3/tv/", func(w http.ResponseWriter, r *http.Request) {
		if m := reSeason.FindStringSubmatch(r.URL.Path); m != nil {
			id, _ := strconv.Atoi(m[1])
			n, _ := strconv.Atoi(m[2])
			s := findShow(id)
			if s == nil {
				http.NotFound(w, r)
				return
			}
			for _, sn := range s.Seasons {
				if sn.Number == n {
					list := []map[string]any{}
					for i, e := range sn.Episodes {
						list = append(list, map[string]any{"episode_number": i + 1, "name": e.Title, "air_date": e.Air, "runtime": 50})
					}
					writeJSON(w, map[string]any{"episodes": list})
					return
				}
			}
			http.NotFound(w, r)
			return
		}
		if m := reShow.FindStringSubmatch(r.URL.Path); m != nil {
			id, _ := strconv.Atoi(m[1])
			s := findShow(id)
			if s == nil {
				http.NotFound(w, r)
				return
			}
			seasons := []map[string]any{}
			for _, sn := range s.Seasons {
				seasons = append(seasons, map[string]any{"season_number": sn.Number, "name": fmt.Sprintf("Season %d", sn.Number), "air_date": sn.Episodes[0].Air})
			}
			writeJSON(w, map[string]any{"id": s.ID, "name": s.Name, "first_air_date": s.First, "overview": s.Overview,
				"poster_path": fmt.Sprintf("/%d.svg", s.ID), "status": "Returning Series", "episode_run_time": []int{50},
				"seasons": seasons, "external_ids": map[string]any{"tvdb_id": s.TVDB, "imdb_id": fmt.Sprintf("tt%d", s.ID)}})
			return
		}
		http.NotFound(w, r)
	})
	m.HandleFunc("/t/p/", func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.Atoi(strings.TrimSuffix(filepath.Base(r.URL.Path), ".svg"))
		s := findShow(id)
		if s == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		words := strings.Fields(s.Name)
		var lines strings.Builder
		for i, word := range words {
			fmt.Fprintf(&lines, `<text x="20" y="%d" font-family="Georgia, serif" font-size="34" fill="#fff">%s</text>`, 330+i*40, html.EscapeString(word))
		}
		fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 300 450"><defs><linearGradient id="g" x1="0" y1="0" x2="0" y2="1">`+
			`<stop offset="0" stop-color="%s"/><stop offset="1" stop-color="#111"/></linearGradient></defs>`+
			`<rect width="300" height="450" fill="url(#g)"/><circle cx="220" cy="110" r="60" fill="#fff" opacity="0.12"/>%s</svg>`,
			s.Color, lines.String())
	})
	return m
}

// ---- Prowlarr ----

var reQuery = regexp.MustCompile(`(?i)^(.*?) S(\d\d)(?:E(\d\d))?$`)

// Prowlarr serves a fake Prowlarr whose search returns a few releases for
// any "Title S01E02" or "Title S01" query. Download links point at base.
func Prowlarr(base func() string) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/api/v1/system/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"appName": "Prowlarr", "version": "1.30.0"})
	})
	m.HandleFunc("/api/v1/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		if cats := r.URL.Query()["categories"]; len(cats) > 0 && (strings.HasPrefix(cats[0], "70") || strings.HasPrefix(cats[0], "30")) {
			writeJSON(w, bookReleases(q, cats, base()))
			return
		}
		mm := reQuery.FindStringSubmatch(q)
		out := []map[string]any{}
		if mm == nil {
			writeJSON(w, out)
			return
		}
		name := strings.ReplaceAll(mm[1], " ", ".")
		code := "S" + mm[2]
		if mm[3] != "" {
			code += "E" + mm[3]
		}
		gb := int64(1 << 30)
		add := func(title, protocol string, size int64, seeders int, age time.Duration) {
			out = append(out, map[string]any{"title": title, "guid": title, "size": size, "seeders": seeders, "protocol": protocol,
				"indexer":     map[string]string{"usenet": "NZBgeek", "torrent": "TorrentLeech"}[protocol],
				"publishDate": time.Now().Add(-age).UTC().Format(time.RFC3339),
				"downloadUrl": base() + "/dl/" + protocol + "/" + url.PathEscape(title)})
		}
		scale := int64(1)
		if mm[3] == "" {
			scale = 8
		}
		add(name+"."+code+".1080p.WEB-DL.DDP5.1.H.264-NTb", "usenet", 3*gb/2*scale, 0, 3*time.Hour)
		add(name+"."+code+".1080p.WEB.h264-ETHEL", "torrent", 2*gb*scale, 41, 5*time.Hour)
		add(name+"."+code+".2160p.WEB-DL.DV.HDR.H.265-FLUX", "torrent", 9*gb*scale, 88, 4*time.Hour)
		add(name+"."+code+".720p.HDTV.x264-SYNCOPY", "usenet", gb/2*scale, 0, 26*time.Hour)
		add(name+"."+code+".1080p.BluRay.REMUX.AVC-EPSiLON", "torrent", 30*gb*scale, 12, 400*time.Hour)
		writeJSON(w, out)
	})
	m.HandleFunc("/dl/usenet/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-nzb")
		fmt.Fprintf(w, `<?xml version="1.0"?><nzb><head><meta type="name">%s</meta></head></nzb>`, html.EscapeString(filepath.Base(r.URL.Path)))
	})
	m.HandleFunc("/dl/torrent/", func(w http.ResponseWriter, r *http.Request) {
		title, _ := url.PathUnescape(filepath.Base(r.URL.Path))
		// Like Prowlarr often does: redirect to a magnet.
		hash := fmt.Sprintf("%040x", hashString(title))
		http.Redirect(w, r, "magnet:?xt=urn:btih:"+hash+"&dn="+url.QueryEscape(title), http.StatusFound)
	})
	return m
}

func hashString(s string) uint64 {
	var h uint64 = 1469598103934665603
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

// ---- Download clients ----

// job is a download in a fake client.
type job struct {
	id      string
	name    string
	started time.Time
	done    bool
	removed bool
}

// Downloader is a fake NZBGet and qBittorrent in one. A job finishes after
// Duration, at which point a small video file appears under Dir/<name>/.
type Downloader struct {
	Dir      string
	Duration time.Duration
	// Seasons lists episode counts so a season pack gets one file per episode.
	mu   sync.Mutex
	jobs []*job
	next int
}

func (d *Downloader) add(id, name string) *job {
	d.mu.Lock()
	defer d.mu.Unlock()
	j := &job{id: id, name: name, started: time.Now()}
	d.jobs = append(d.jobs, j)
	return j
}

var rePack = regexp.MustCompile(`(?i)^(.*)\.S(\d\d)\.(.*)$`)

func (d *Downloader) progress(j *job) float64 {
	if j.done {
		return 1
	}
	p := float64(time.Since(j.started)) / float64(d.Duration)
	if p >= 1 {
		j.done = true
		dir := filepath.Join(d.Dir, j.name)
		os.MkdirAll(dir, 0o755)
		if files := bookFiles(j.name); files != nil {
			for name, body := range files {
				os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
				os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
			}
		} else if m := rePack.FindStringSubmatch(j.name); m != nil {
			for e := 1; e <= 10; e++ {
				os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s.S%sE%02d.%s.mkv", m[1], m[2], e, m[3])), []byte("fake video"), 0o644)
			}
		} else {
			os.WriteFile(filepath.Join(dir, j.name+".mkv"), []byte("fake video"), 0o644)
		}
		return 1
	}
	return p
}

// NZBGet serves the fake NZBGet JSON-RPC API.
func (d *Downloader) NZBGet() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&call)
		reply := func(v any) { writeJSON(w, map[string]any{"version": "1.1", "id": 1, "result": v}) }
		switch call.Method {
		case "version":
			reply("24.5")
		case "append":
			var name string
			json.Unmarshal(call.Params[0], &name)
			d.mu.Lock()
			d.next++
			id := d.next
			d.mu.Unlock()
			d.add(strconv.Itoa(id), strings.TrimSuffix(name, ".nzb"))
			reply(id)
		case "listgroups":
			d.mu.Lock()
			defer d.mu.Unlock()
			out := []map[string]any{}
			for _, j := range d.jobs {
				if j.removed || len(j.id) > 12 {
					continue
				}
				if p := d.progress(j); p < 1 {
					id, _ := strconv.Atoi(j.id)
					out = append(out, map[string]any{"NZBID": id, "Status": "DOWNLOADING", "FileSizeMB": 1000, "RemainingSizeMB": int(1000 * (1 - p)),
						"DestDir": filepath.Join(d.Dir, "intermediate", j.name)})
				}
			}
			reply(out)
		case "history":
			d.mu.Lock()
			defer d.mu.Unlock()
			out := []map[string]any{}
			for _, j := range d.jobs {
				if j.removed || len(j.id) > 12 || !j.done {
					continue
				}
				id, _ := strconv.Atoi(j.id)
				out = append(out, map[string]any{"NZBID": id, "Status": "SUCCESS/ALL", "DestDir": filepath.Join(d.Dir, j.name), "FinalDir": ""})
			}
			reply(out)
		case "editqueue":
			var ids []int
			json.Unmarshal(call.Params[2], &ids)
			d.mu.Lock()
			for _, j := range d.jobs {
				for _, id := range ids {
					if j.id == strconv.Itoa(id) {
						j.removed = true
					}
				}
			}
			d.mu.Unlock()
			reply(true)
		default:
			writeJSON(w, map[string]any{"error": map[string]string{"message": "unknown method " + call.Method}})
		}
	})
}

// QBittorrent serves the fake qBittorrent Web API.
func (d *Downloader) QBittorrent() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "fake", Path: "/"})
		fmt.Fprint(w, "Ok.")
	})
	m.HandleFunc("/api/v2/app/version", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "v5.0.2") })
	m.HandleFunc("/api/v2/torrents/add", func(w http.ResponseWriter, r *http.Request) {
		r.ParseMultipartForm(1 << 20)
		magnet := r.FormValue("urls")
		u, _ := url.Parse(magnet)
		hash := strings.TrimPrefix(u.Query().Get("xt"), "urn:btih:")
		d.add(strings.ToLower(hash), u.Query().Get("dn"))
		fmt.Fprint(w, "Ok.")
	})
	m.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		want := map[string]bool{}
		for _, h := range strings.Split(r.URL.Query().Get("hashes"), "|") {
			want[h] = true
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		out := []map[string]any{}
		for _, j := range d.jobs {
			if j.removed || !want[j.id] {
				continue
			}
			p := d.progress(j)
			state := "downloading"
			if p >= 1 {
				state = "uploading"
			}
			out = append(out, map[string]any{"hash": j.id, "state": state, "progress": p, "name": j.name,
				"save_path": d.Dir, "content_path": filepath.Join(d.Dir, j.name)})
		}
		writeJSON(w, out)
	})
	m.HandleFunc("/api/v2/torrents/delete", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		d.mu.Lock()
		for _, j := range d.jobs {
			if j.id == r.Form.Get("hashes") {
				j.removed = true
			}
		}
		d.mu.Unlock()
	})
	return m
}
