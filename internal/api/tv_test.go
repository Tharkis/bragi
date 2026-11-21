package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tharkis/bragi/internal/db"
	"github.com/Tharkis/bragi/internal/events"
	"github.com/Tharkis/bragi/internal/fakes"
	"github.com/Tharkis/bragi/internal/movies"
	"github.com/Tharkis/bragi/internal/store"
	"github.com/Tharkis/bragi/internal/tv"
)

type tvEnv struct {
	*client
	tv        *tv.Service
	movies    *movies.Service
	library   string
	movieLib  string
	downloads string
	refreshes *atomic.Int32
	sabURL    string
}

// newTVEnv starts Bragi with fake TMDB, Prowlarr, NZBGet, qBittorrent and
// Jellyfin, an admin signed in, and everything set up as the wizard would.
func newTVEnv(t *testing.T) *tvEnv {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "bragi.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	library := filepath.Join(root, "media", "tv")
	movieLib := filepath.Join(root, "media", "movies")
	downloads := filepath.Join(root, "downloads")
	os.MkdirAll(library, 0o755)
	os.MkdirAll(movieLib, 0o755)
	os.MkdirAll(downloads, 0o755)

	tmdb := httptest.NewServer(fakes.TMDB())
	t.Cleanup(tmdb.Close)
	var prowlarrURL string
	prowlarr := httptest.NewServer(fakes.Prowlarr(func() string { return prowlarrURL }))
	prowlarrURL = prowlarr.URL
	t.Cleanup(prowlarr.Close)
	dl := &fakes.Downloader{Dir: downloads, Duration: time.Millisecond}
	nzbget := httptest.NewServer(dl.NZBGet())
	t.Cleanup(nzbget.Close)
	qbit := httptest.NewServer(dl.QBittorrent())
	t.Cleanup(qbit.Close)
	sab := httptest.NewServer(dl.SABnzbd())
	t.Cleanup(sab.Close)
	refreshes := &atomic.Int32{}
	jellyfin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != `MediaBrowser Token="jf-key"` {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/System/Info":
			fmt.Fprint(w, `{"ServerName":"Living room","Version":"10.11.0"}`)
		case "/Library/Refresh":
			refreshes.Add(1)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(jellyfin.Close)

	st := store.New(d)
	hub := events.NewHub()
	hc := &http.Client{Timeout: 5 * time.Second}
	svc := tv.New(st, hc, nil, hub)
	svc.TMDBBaseURL = tmdb.URL + "/3"
	mv := movies.New(st, hc, nil, hub, svc.TMDB)
	mv.RefreshJellyfin = svc.RefreshJellyfin
	srv := httptest.NewServer(New(Options{Store: st, Version: "test", HTTPClient: hc, TV: svc, Movies: mv, Events: hub}))
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, url: srv.URL, hc: &http.Client{Jar: jar}}

	must := func(code, want int, what string) {
		t.Helper()
		if code != want {
			t.Fatalf("%s: HTTP %d, want %d", what, code, want)
		}
	}
	must(c.do("POST", "/api/v1/setup", credentials{"joe", "correct horse"}, nil), 201, "setup")
	must(c.do("POST", "/api/v1/libraries", map[string]any{"mediaType": "tv", "path": library, "qualityPresetId": 1}, nil), 201, "library")
	must(c.do("POST", "/api/v1/indexers", map[string]any{"kind": "prowlarr", "baseUrl": prowlarr.URL, "apiKey": "k", "enabled": true}, nil), 201, "indexer")
	must(c.do("POST", "/api/v1/downloadclients", map[string]any{"kind": "nzbget", "baseUrl": nzbget.URL, "category": "tv", "enabled": true}, nil), 201, "nzbget")
	must(c.do("POST", "/api/v1/downloadclients", map[string]any{"kind": "qbittorrent", "baseUrl": qbit.URL, "category": "tv", "enabled": true}, nil), 201, "qbittorrent")
	must(c.do("PUT", "/api/v1/settings", map[string]string{"tmdb_api_key": "good", "jellyfin_url": jellyfin.URL + "/", "jellyfin_api_key": "jf-key"}, nil), 200, "settings")
	return &tvEnv{client: c, tv: svc, movies: mv, library: library, movieLib: movieLib, downloads: downloads, refreshes: refreshes, sabURL: sab.URL}
}

func listFiles(t *testing.T, root string) []string {
	var out []string
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func TestRequestToImport(t *testing.T) {
	e := newTVEnv(t)
	ctx := context.Background()

	var settings map[string]string
	e.do("GET", "/api/v1/settings", nil, &settings)
	if settings["tmdb_api_key"] != secretMask || settings["jellyfin_url"] == "" || strings.HasSuffix(settings["jellyfin_url"], "/") {
		t.Fatalf("settings not saved or not masked: %v", settings)
	}
	var test testResult
	e.do("POST", "/api/v1/settings/test/jellyfin", map[string]string{"url": settings["jellyfin_url"], "apiKey": secretMask}, &test)
	if !test.OK {
		t.Fatalf("Jellyfin test with saved key: %+v", test)
	}

	var found []discoverResult
	if code := e.do("GET", "/api/v1/discover/search?type=tv&q=severance", nil, &found); code != 200 || len(found) != 1 {
		t.Fatalf("discover: %d %+v", code, found)
	}

	// An admin's request is approved at once and the show is added.
	var rq requestResponse
	if code := e.do("POST", "/api/v1/requests", map[string]any{"type": "tv", "tmdbId": found[0].TMDBID, "seasons": []int{1}}, &rq); code != 201 {
		t.Fatalf("create request: %d", code)
	}
	if rq.Status != "approved" || rq.Progress == nil || rq.Progress.Episodes != 9 {
		t.Fatalf("request after create: %+v progress %+v", rq.Request, rq.Progress)
	}

	// First pass: season 1 has fully aired and is all wanted, so Bragi grabs
	// a season pack. The fake Usenet 1080p WEB-DL is the best fit for the
	// "Best up to 1080p" preset; the remux is over the size cap.
	e.tv.Pass(ctx)
	var queue []downloadResponse
	e.do("GET", "/api/v1/activity/queue", nil, &queue)
	if len(queue) != 1 || !strings.Contains(queue[0].ReleaseTitle, "S01.1080p.WEB-DL") || queue[0].Protocol != "usenet" || queue[0].Label != "Season 1" {
		t.Fatalf("queue after first pass: %+v", queue)
	}

	// Second pass: the download is done; Bragi imports and renames it.
	e.tv.Pass(ctx)
	files := listFiles(t, e.library)
	if len(files) != 9 {
		t.Fatalf("library has %d files, want 9: %v", len(files), files)
	}
	want := filepath.Join("Severance (2022)", "Season 01", "Severance (2022) - S01E08 - What's for Dinner [WEBDL-1080p].mkv")
	if files[7] != want {
		t.Fatalf("file name\n got %s\nwant %s", files[7], want)
	}
	var history []downloadResponse
	e.do("GET", "/api/v1/activity/history", nil, &history)
	if len(history) != 1 || history[0].Status != "imported" {
		t.Fatalf("history: %+v", history)
	}
	e.do("GET", fmt.Sprintf("/api/v1/requests/%d", rq.ID), nil, &rq)
	if rq.Status != "available" || len(rq.Episodes) != 9 || rq.Episodes[0].Status != "imported" {
		t.Fatalf("request after import: %s, %d episodes", rq.Status, len(rq.Episodes))
	}
	if e.refreshes.Load() == 0 {
		t.Fatal("Jellyfin was not asked to refresh")
	}

	// Manual pick for one episode of another show: a torrent, hardlinked so
	// it keeps seeding.
	if code := e.do("POST", "/api/v1/requests", map[string]any{"tmdbId": 95480, "seasons": "future"}, &rq); code != 201 {
		t.Fatalf("second request: %d", code)
	}
	sid := rq.Progress.SeriesID
	var rels []releaseResponse
	if code := e.do("GET", fmt.Sprintf("/api/v1/series/%d/releases?season=1&episode=2", sid), nil, &rels); code != 200 || len(rels) != 5 {
		t.Fatalf("releases: %d, %d results", code, len(rels))
	}
	var torrent *releaseResponse
	for i := range rels {
		if rels[i].Protocol == "torrent" && rels[i].Rejected == "" {
			torrent = &rels[i]
			break
		}
	}
	if torrent == nil {
		t.Fatalf("no acceptable torrent in %+v", rels)
	}
	if rels[len(rels)-1].Rejected == "" {
		t.Fatalf("expected the last release to be rejected: %+v", rels[len(rels)-1])
	}
	if code := e.do("POST", fmt.Sprintf("/api/v1/series/%d/grab", sid), map[string]any{"season": 1, "episode": 2, "release": torrent.Release}, nil); code != 200 {
		t.Fatalf("grab: %d", code)
	}
	e.tv.Pass(ctx)
	e.tv.Pass(ctx)
	var show seriesResponse
	e.do("GET", fmt.Sprintf("/api/v1/series/%d", sid), nil, &show)
	var ep store.Episode
	for _, sn := range show.Seasons {
		for _, x := range sn.Episodes {
			if sn.Number == 1 && x.Number == 2 {
				ep = x
			}
		}
	}
	if ep.Status != "imported" || !strings.HasSuffix(ep.FilePath, "Slow Horses (2022) - S01E02 - Work Drinks [WEBDL-1080p].mkv") {
		t.Fatalf("episode after manual grab: %+v", ep)
	}
	src, _ := os.Stat(filepath.Join(e.downloads, torrent.Title, torrent.Title+".mkv"))
	dst, _ := os.Stat(ep.FilePath)
	if src == nil || dst == nil || !os.SameFile(src, dst) {
		t.Fatal("torrent import should hardlink and leave the download in place")
	}
}

func TestRequesterNeedsApproval(t *testing.T) {
	e := newTVEnv(t)
	var admin userResponse
	e.do("GET", "/api/v1/auth/me", nil, &admin)

	// The Jellyfin plugin acts for a Jellyfin user with the admin's key.
	plugin := &client{t: t, url: e.url, hc: &http.Client{}, key: admin.APIKey}
	req, _ := http.NewRequest("POST", e.url+"/api/v1/requests", strings.NewReader(`{"tmdbId":82728,"seasons":"all"}`))
	req.Header.Set("X-Api-Key", admin.APIKey)
	req.Header.Set("X-Bragi-User", "jf-user-1")
	req.Header.Set("X-Bragi-User-Name", "kid")
	resp, err := plugin.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("plugin request: %d", resp.StatusCode)
	}

	var list []requestResponse
	e.do("GET", "/api/v1/requests?status=pending", nil, &list)
	if len(list) != 1 || list[0].Username != "kid" || list[0].Status != "pending" {
		t.Fatalf("pending requests: %+v", list)
	}
	var shows []seriesResponse
	e.do("GET", "/api/v1/series", nil, &shows)
	if len(shows) != 0 {
		t.Fatal("a requester's request added the show before approval")
	}
	var out requestResponse
	if code := e.do("POST", fmt.Sprintf("/api/v1/requests/%d/approve", list[0].ID), nil, &out); code != 200 || out.Status != "approved" {
		t.Fatalf("approve: %d %+v", code, out.Request)
	}
	e.do("GET", "/api/v1/series", nil, &shows)
	if len(shows) != 1 || shows[0].Title != "Bluey" {
		t.Fatalf("shows after approval: %+v", shows)
	}
}

func TestAdoptExistingLibrary(t *testing.T) {
	e := newTVEnv(t)
	dir := filepath.Join(e.library, "The Bear (2022)", "Season 1")
	os.MkdirAll(dir, 0o755)
	for _, n := range []string{"The.Bear.S01E01.1080p.WEB.mkv", "The.Bear.S01E02.1080p.WEB.mkv", "notes.txt"} {
		os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644)
	}
	os.MkdirAll(filepath.Join(e.library, "Home Videos"), 0o755)

	if code := e.do("POST", "/api/v1/libraries/scan", nil, nil); code != 202 {
		t.Fatalf("scan: %d", code)
	}
	var st tv.ScanStatus
	for i := 0; i < 100; i++ {
		e.do("GET", "/api/v1/libraries/scan", nil, &st)
		if !st.Running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(st.Added) != 1 || st.Added[0] != "The Bear (2022)" || st.Files != 2 || len(st.Unmatched) != 1 {
		t.Fatalf("scan result: %+v", st)
	}
	var shows []seriesResponse
	e.do("GET", "/api/v1/series", nil, &shows)
	if len(shows) != 1 || shows[0].Imported != 2 {
		t.Fatalf("adopted show: %+v", shows)
	}
	// Existing episodes are not monitored, so nothing is downloaded.
	e.tv.Pass(context.Background())
	var queue []downloadResponse
	e.do("GET", "/api/v1/activity/queue", nil, &queue)
	if len(queue) != 0 {
		t.Fatalf("adopting a library started downloads: %+v", queue)
	}
}
