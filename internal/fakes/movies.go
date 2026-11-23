package fakes

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Movie is a fake TMDB movie.
type Movie struct {
	ID       int
	Title    string
	Released string // theatrical date
	Home     string // digital release date, "" if not announced
	Runtime  int
	Overview string
	Color    string
}

// Movies is the fake movie catalogue. Starfall is not out yet.
var Movies = []Movie{
	{ID: 693134, Title: "Dune: Part Two", Released: "2024-02-27", Home: "2024-04-16", Runtime: 166, Color: "#b0783a",
		Overview: "Paul Atreides unites with Chani and the Fremen while on a warpath of revenge against the conspirators who destroyed his family."},
	{ID: 666277, Title: "Past Lives", Released: "2023-06-02", Home: "2023-07-21", Runtime: 106, Color: "#3f6f6a",
		Overview: "Nora and Hae Sung, two deeply connected childhood friends, are wrested apart, then reunited twenty years later."},
	{ID: 840430, Title: "The Holdovers", Released: "2023-10-27", Home: "2023-11-21", Runtime: 133, Color: "#7a3b2e",
		Overview: "A curmudgeonly instructor at a New England prep school is forced to remain on campus during Christmas break."},
	{ID: 949, Title: "Heat", Released: "1995-12-15", Home: "1999-06-08", Runtime: 170, Color: "#2d4a6b",
		Overview: "Obsessive master thief Neil McCauley leads a top-notch crew on various daring heists throughout Los Angeles."},
	{ID: 990001, Title: "Starfall Protocol", Released: "2026-11-06", Runtime: 128, Color: "#4a3b78",
		Overview: "A salvage crew finds a derelict station that is still broadcasting a countdown."},
}

func findMovie(id int) *Movie {
	for i := range Movies {
		if Movies[i].ID == id {
			return &Movies[i]
		}
	}
	return nil
}

func movieRoutes(m *http.ServeMux) {
	m.HandleFunc("/3/trending/movie/week", func(w http.ResponseWriter, r *http.Request) {
		results := []map[string]any{}
		for _, mv := range Movies {
			results = append(results, map[string]any{"id": mv.ID, "title": mv.Title, "release_date": mv.Released,
				"overview": mv.Overview, "poster_path": fmt.Sprintf("/m%d.svg", mv.ID), "media_type": "movie"})
		}
		writeJSON(w, map[string]any{"page": 1, "total_pages": 1, "results": results})
	})
	m.HandleFunc("/3/search/movie", func(w http.ResponseWriter, r *http.Request) {
		q := strings.ToLower(r.URL.Query().Get("query"))
		results := []map[string]any{}
		for _, mv := range Movies {
			t := strings.ToLower(mv.Title)
			if strings.Contains(t, q) || strings.Contains(q, t) || strings.Contains(strings.ReplaceAll(t, ":", ""), q) ||
				len(q) > 3 && strings.Contains(strings.ToLower(mv.Overview), " "+q+" ") {
				results = append(results, map[string]any{"id": mv.ID, "title": mv.Title, "release_date": mv.Released,
					"overview": mv.Overview, "poster_path": fmt.Sprintf("/m%d.svg", mv.ID)})
			}
		}
		writeJSON(w, map[string]any{"results": results})
	})
	reMovie := regexp.MustCompile(`^/3/movie/(\d+)$`)
	m.HandleFunc("/3/movie/", func(w http.ResponseWriter, r *http.Request) {
		mm := reMovie.FindStringSubmatch(r.URL.Path)
		if mm == nil {
			http.NotFound(w, r)
			return
		}
		id, _ := strconv.Atoi(mm[1])
		mv := findMovie(id)
		if mv == nil {
			http.NotFound(w, r)
			return
		}
		releases := []map[string]any{{"type": 3, "release_date": mv.Released + "T00:00:00.000Z"}}
		if mv.Home != "" {
			releases = append(releases, map[string]any{"type": 4, "release_date": mv.Home + "T00:00:00.000Z"})
		}
		status := "Released"
		if mv.Home == "" {
			status = "In Theaters"
		}
		writeJSON(w, map[string]any{"id": mv.ID, "title": mv.Title, "release_date": mv.Released, "overview": mv.Overview,
			"poster_path": fmt.Sprintf("/m%d.svg", mv.ID), "runtime": mv.Runtime, "status": status, "imdb_id": fmt.Sprintf("tt%07d", mv.ID),
			"release_dates": map[string]any{"results": []map[string]any{{"iso_3166_1": "US", "release_dates": releases}}}})
	})
}

var reMovieQuery = regexp.MustCompile(`^(.*) (\d{4})$`)

// movieReleases is the fake Prowlarr answer to a movie search such as
// "Dune: Part Two 2024".
func movieReleases(q, base string) []map[string]any {
	out := []map[string]any{}
	mm := reMovieQuery.FindStringSubmatch(q)
	if mm == nil {
		return out
	}
	name := strings.Join(strings.Fields(strings.NewReplacer(":", "", "'", "").Replace(mm[1])), ".") + "." + mm[2]
	gb := int64(1 << 30)
	add := func(title, protocol string, size int64, seeders int, age time.Duration) {
		out = append(out, map[string]any{"title": title, "guid": title, "size": size, "seeders": seeders, "protocol": protocol,
			"indexer":     map[string]string{"usenet": "NZBgeek", "torrent": "TorrentLeech"}[protocol],
			"publishDate": time.Now().Add(-age).UTC().Format(time.RFC3339),
			"downloadUrl": base + "/dl/" + protocol + "/" + url.PathEscape(title)})
	}
	add(name+".1080p.WEB-DL.DDP5.1.Atmos.H.264-FLUX", "usenet", 9*gb, 0, 30*time.Hour)
	add(name+".1080p.BluRay.x264-SPARKS", "torrent", 18*gb, 63, 200*time.Hour)
	add(name+".2160p.UHD.BluRay.REMUX.HDR.HEVC-FGT", "torrent", 70*gb, 120, 150*time.Hour)
	add(name+".720p.WEBRip.x264-YTS", "torrent", gb*3/2, 900, 400*time.Hour)
	add(name+".HDCAM.x264-TGx", "torrent", 2*gb, 15, 900*time.Hour)
	return out
}

// SABnzbd serves the fake SABnzbd API. The API key is "sab-key".
func (d *Downloader) SABnzbd() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		mode := q.Get("mode")
		if mode == "version" {
			writeJSON(w, map[string]string{"version": "4.4.1"})
			return
		}
		if q.Get("apikey") != "sab-key" {
			writeJSON(w, map[string]any{"status": false, "error": "API Key Incorrect"})
			return
		}
		ids := map[string]bool{}
		for _, id := range strings.Split(q.Get("nzo_ids"), ",") {
			if id != "" {
				ids[id] = true
			}
		}
		want := func(j *job) bool {
			return strings.HasPrefix(j.id, "SABnzbd_nzo_") && !j.removed && (len(ids) == 0 || ids[j.id])
		}
		switch mode {
		case "addfile":
			r.ParseMultipartForm(1 << 20)
			f, hdr, err := r.FormFile("name")
			if err != nil {
				writeJSON(w, map[string]any{"status": false, "error": "no file"})
				return
			}
			io.Copy(io.Discard, f)
			name := r.FormValue("nzbname")
			if name == "" {
				name = strings.TrimSuffix(hdr.Filename, ".nzb")
			}
			d.mu.Lock()
			d.next++
			id := fmt.Sprintf("SABnzbd_nzo_%06d", d.next)
			d.mu.Unlock()
			d.add(id, name)
			writeJSON(w, map[string]any{"status": true, "nzo_ids": []string{id}})
		case "queue":
			if q.Get("name") == "delete" {
				d.remove(q.Get("value"))
				writeJSON(w, map[string]any{"status": true})
				return
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			slots := []map[string]any{}
			for _, j := range d.jobs {
				if !want(j) {
					continue
				}
				if p := d.progress(j); p < 1 {
					slots = append(slots, map[string]any{"nzo_id": j.id, "status": "Downloading", "filename": j.name,
						"mb": "8000.00", "mbleft": fmt.Sprintf("%.2f", 8000*(1-p)), "percentage": fmt.Sprintf("%d", int(p*100))})
				}
			}
			writeJSON(w, map[string]any{"queue": map[string]any{"slots": slots}})
		case "history":
			if q.Get("name") == "delete" {
				d.remove(q.Get("value"))
				writeJSON(w, map[string]any{"status": true})
				return
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			slots := []map[string]any{}
			for _, j := range d.jobs {
				if want(j) && j.done {
					slots = append(slots, map[string]any{"nzo_id": j.id, "status": "Completed", "name": j.name,
						"storage": filepath.Join(d.Dir, j.name), "fail_message": ""})
				}
			}
			writeJSON(w, map[string]any{"history": map[string]any{"slots": slots}})
		default:
			writeJSON(w, map[string]any{"status": false, "error": "not implemented"})
		}
	})
}

func (d *Downloader) remove(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, j := range d.jobs {
		if j.id == id {
			j.removed = true
		}
	}
}
