package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tharkis/bragi/internal/scan"
	"github.com/Tharkis/bragi/internal/store"
)

// newMovieEnv is the TV environment plus a Movies library, with SABnzbd as
// the only Usenet client (NZBGet removed) so movie grabs go through it.
func newMovieEnv(t *testing.T) *tvEnv {
	t.Helper()
	e := newTVEnv(t)
	if code := e.do("POST", "/api/v1/libraries", map[string]any{"mediaType": "movie", "path": e.movieLib, "qualityPresetId": 1}, nil); code != 201 {
		t.Fatalf("movie library: %d", code)
	}
	var test testResult
	e.do("POST", "/api/v1/downloadclients/test", map[string]any{"kind": "sabnzbd", "baseUrl": e.sabURL, "apiKey": "wrong"}, &test)
	if test.OK {
		t.Fatal("SABnzbd test passed with a wrong API key")
	}
	e.do("POST", "/api/v1/downloadclients/test", map[string]any{"kind": "sabnzbd", "baseUrl": e.sabURL, "apiKey": "sab-key"}, &test)
	if !test.OK {
		t.Fatalf("SABnzbd test: %+v", test)
	}
	var clients []store.Integration
	e.do("GET", "/api/v1/downloadclients", nil, &clients)
	for _, c := range clients {
		if c.Kind == "nzbget" {
			e.do("DELETE", fmt.Sprintf("/api/v1/downloadclients/%d", c.ID), nil, nil)
		}
	}
	if code := e.do("POST", "/api/v1/downloadclients", map[string]any{"kind": "sabnzbd", "baseUrl": e.sabURL, "apiKey": "sab-key",
		"category": "movies", "enabled": true}, nil); code != 201 {
		t.Fatalf("add SABnzbd: %d", code)
	}
	return e
}

// pass runs both pipelines, TV first, so a TV pass that touched movie
// downloads would show up as a broken import.
func (e *tvEnv) pass() {
	ctx := context.Background()
	e.tv.Pass(ctx)
	e.movies.Pass(ctx)
}

func TestMovieRequestToImport(t *testing.T) {
	e := newMovieEnv(t)

	var found []discoverMovie
	if code := e.do("GET", "/api/v1/discover/search?type=movie&q=dune", nil, &found); code != 200 || len(found) != 1 || found[0].Type != "movie" {
		t.Fatalf("discover movies: %d %+v", code, found)
	}

	var rq requestResponse
	if code := e.do("POST", "/api/v1/requests", map[string]any{"type": "movie", "tmdbId": found[0].TMDBID}, &rq); code != 201 {
		t.Fatalf("create request: %d", code)
	}
	if rq.Status != "approved" || rq.MediaType != "movie" || rq.Progress == nil || rq.Progress.MovieID == 0 || rq.Progress.Missing != 1 {
		t.Fatalf("request after create: %+v progress %+v", rq.Request, rq.Progress)
	}

	// First pass: the Usenet 1080p WEB-DL fits "Best up to 1080p"; the
	// remux is over the size cap and the cam has no known quality.
	e.pass()
	var queue []downloadResponse
	e.do("GET", "/api/v1/activity/queue", nil, &queue)
	if len(queue) != 1 || !strings.Contains(queue[0].ReleaseTitle, "1080p.WEB-DL") || queue[0].ClientName != "SABnzbd" ||
		queue[0].MediaType != "movie" || queue[0].SeriesTitle != "Dune: Part Two" {
		t.Fatalf("queue after first pass: %+v", queue)
	}

	// Second pass: done; imported and renamed.
	e.pass()
	files := listFiles(t, e.movieLib)
	want := filepath.Join("Dune - Part Two (2024)", "Dune - Part Two (2024) [WEBDL-1080p].mkv")
	if len(files) != 1 || files[0] != want {
		t.Fatalf("movie library: %v, want %s", files, want)
	}
	var m movieResponse
	e.do("GET", fmt.Sprintf("/api/v1/movies/%d", rq.Progress.MovieID), nil, &m)
	if m.Status != "imported" || m.Quality != "WEBDL-1080p" || m.Path == "" || m.Downloading != 0 {
		t.Fatalf("movie after import: %+v", m.Movie)
	}
	e.do("GET", fmt.Sprintf("/api/v1/requests/%d", rq.ID), nil, &rq)
	if rq.Status != "available" || rq.Progress.Imported != 1 {
		t.Fatalf("request after import: %s %+v", rq.Status, rq.Progress)
	}
	if e.refreshes.Load() == 0 {
		t.Fatal("Jellyfin was not asked to refresh")
	}

	// A TV discover search does not mistake the movie's TMDB id for a show's.
	var shows []discoverResult
	e.do("GET", "/api/v1/discover/search?type=tv&q=severance", nil, &shows)
	if len(shows) != 1 || shows[0].Requested != "" {
		t.Fatalf("TV discover after a movie request: %+v", shows)
	}
}

func TestMovieNotOutYet(t *testing.T) {
	e := newMovieEnv(t)
	var rq requestResponse
	if code := e.do("POST", "/api/v1/requests", map[string]any{"type": "movie", "tmdbId": 990001}, &rq); code != 201 {
		t.Fatalf("request: %d", code)
	}
	e.pass()
	var queue []downloadResponse
	e.do("GET", "/api/v1/activity/queue", nil, &queue)
	if len(queue) != 0 {
		t.Fatalf("an unreleased movie was downloaded: %+v", queue)
	}
	var out struct {
		Queued  bool   `json:"queued"`
		Message string `json:"message"`
	}
	e.do("POST", fmt.Sprintf("/api/v1/movies/%d/search", rq.Progress.MovieID), nil, &out)
	if out.Queued || !strings.Contains(out.Message, "not out yet") {
		t.Fatalf("search now on an unreleased movie: %+v", out)
	}
	if rq.Progress.Missing != 0 {
		t.Fatalf("an unreleased movie counts as missing: %+v", rq.Progress)
	}
}

func TestMovieManualPickAndRemove(t *testing.T) {
	e := newMovieEnv(t)
	var rq requestResponse
	e.do("POST", "/api/v1/requests", map[string]any{"type": "movie", "tmdbId": 666277}, &rq)
	id := rq.Progress.MovieID

	var rels []releaseResponse
	if code := e.do("GET", fmt.Sprintf("/api/v1/movies/%d/releases", id), nil, &rels); code != 200 || len(rels) != 5 {
		t.Fatalf("releases: %d, %d results", code, len(rels))
	}
	reasons := map[string]bool{}
	for _, r := range rels {
		reasons[r.Rejected] = true
	}
	if !reasons["Unknown quality"] || !reasons[""] {
		t.Fatalf("expected a mix of accepted and rejected releases: %+v", rels)
	}
	var torrent *releaseResponse
	for _, r := range rels {
		if r.Protocol == "torrent" && r.Rejected == "" && r.Quality == "WEBRip-720p" {
			torrent = &r
		}
	}
	if torrent == nil {
		t.Fatalf("no acceptable 720p torrent in %+v", rels)
	}
	if code := e.do("POST", fmt.Sprintf("/api/v1/movies/%d/grab", id), map[string]any{"release": torrent.Release}, nil); code != 200 {
		t.Fatalf("grab: %d", code)
	}

	// Removing it from the queue blocklists it and puts the movie back to wanted.
	var queue []downloadResponse
	e.do("GET", "/api/v1/activity/queue", nil, &queue)
	if len(queue) != 1 {
		t.Fatalf("queue after grab: %+v", queue)
	}
	if code := e.do("DELETE", fmt.Sprintf("/api/v1/activity/queue/%d", queue[0].ID), nil, nil); code != 204 {
		t.Fatalf("remove: %d", code)
	}
	var m movieResponse
	e.do("GET", fmt.Sprintf("/api/v1/movies/%d", id), nil, &m)
	if m.Status != "wanted" || !strings.Contains(m.SearchNote, "Removed by you") {
		t.Fatalf("movie after remove: %+v", m.Movie)
	}
	e.do("GET", fmt.Sprintf("/api/v1/movies/%d/releases", id), nil, &rels)
	for _, r := range rels {
		if r.Title == torrent.Title && r.Rejected != "Failed before" {
			t.Fatalf("removed release not blocklisted: %+v", r)
		}
	}

	// Grab it again by hand and let it finish: torrents are hardlinked.
	e.do("POST", fmt.Sprintf("/api/v1/movies/%d/grab", id), map[string]any{"release": torrent.Release}, nil)
	e.pass()
	e.pass()
	e.do("GET", fmt.Sprintf("/api/v1/movies/%d", id), nil, &m)
	if m.Status != "imported" || !strings.HasSuffix(m.FilePath, filepath.Join("Past Lives (2023)", "Past Lives (2023) [WEBRip-720p].mkv")) {
		t.Fatalf("movie after manual grab: %+v", m.Movie)
	}
	src, _ := os.Stat(filepath.Join(e.downloads, torrent.Title, torrent.Title+".mkv"))
	dst, _ := os.Stat(m.FilePath)
	if src == nil || dst == nil || !os.SameFile(src, dst) {
		t.Fatal("torrent import should hardlink and leave the download in place")
	}
}

func TestAdoptExistingMovies(t *testing.T) {
	e := newMovieEnv(t)
	dir := filepath.Join(e.movieLib, "Heat (1995)")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "Heat.1995.1080p.BluRay.x264.mkv"), []byte("movie"), 0o644)
	os.WriteFile(filepath.Join(dir, "Heat.1995.sample.mkv"), []byte("s"), 0o644)
	os.MkdirAll(filepath.Join(e.movieLib, "Holiday Videos"), 0o755)

	if code := e.do("POST", "/api/v1/libraries/scan?type=movie", nil, nil); code != 202 {
		t.Fatalf("scan: %d", code)
	}
	var st scan.Status
	for i := 0; i < 100; i++ {
		e.do("GET", "/api/v1/libraries/scan?type=movie", nil, &st)
		if !st.Running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(st.Added) != 1 || st.Added[0] != "Heat (1995)" || st.Files != 1 || len(st.Unmatched) != 1 || st.MediaType != "movie" {
		t.Fatalf("scan result: %+v", st)
	}
	var list []movieResponse
	e.do("GET", "/api/v1/movies", nil, &list)
	if len(list) != 1 || list[0].Status != "imported" || list[0].Quality != "Bluray-1080p" || list[0].Monitored {
		t.Fatalf("adopted movie: %+v", list)
	}
	e.pass()
	var queue []downloadResponse
	e.do("GET", "/api/v1/activity/queue", nil, &queue)
	if len(queue) != 0 {
		t.Fatalf("adopting a library started downloads: %+v", queue)
	}
}
