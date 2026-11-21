package api

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tharkis/bragi/internal/store"
)

// nzbgetClient returns the env's saved NZBGet client.
func nzbgetClient(t *testing.T, c *client) store.Integration {
	t.Helper()
	var clients []store.Integration
	c.do("GET", "/api/v1/downloadclients", nil, &clients)
	for _, dc := range clients {
		if dc.Kind == "nzbget" {
			return dc
		}
	}
	t.Fatal("no NZBGet client")
	return store.Integration{}
}

// TestNZBGetMovie runs a movie through NZBGet in its own container: a
// movies category, a path mapping, and a round of unpacking.
func TestNZBGetMovie(t *testing.T) {
	e := newTVEnv(t)
	if code := e.do("POST", "/api/v1/libraries", map[string]any{"mediaType": "movie", "path": e.movieLib, "qualityPresetId": 1}, nil); code != 201 {
		t.Fatalf("movie library: %d", code)
	}
	e.dl.NZBGetRoot = "/downloads"
	e.dl.PostProcess = true
	e.dl.NZBGetCategories = []string{"tv", "Movies"}

	nz := nzbgetClient(t, e.client)
	nz.Categories = map[string]string{"movie": "movies", "audiobook": "audiobooks"}
	nz.RemotePath = "/downloads/"

	// Half a mapping is refused.
	if code := e.do("PUT", fmt.Sprintf("/api/v1/downloadclients/%d", nz.ID), nz, nil); code != 400 {
		t.Fatalf("half a path mapping: HTTP %d, want 400", code)
	}
	nz.LocalPath = filepath.Join(e.downloads, "nope")
	var test testResult
	e.do("POST", "/api/v1/downloadclients/test", nz, &test)
	if !test.OK || len(test.Warnings) != 2 || !strings.Contains(test.Warnings[0], "“audiobooks”") || !strings.Contains(test.Warnings[1], "cannot see the folder") {
		t.Fatalf("test with a missing category and folder: %+v", test)
	}
	nz.LocalPath = e.downloads
	if code := e.do("PUT", fmt.Sprintf("/api/v1/downloadclients/%d", nz.ID), nz, nil); code != 200 {
		t.Fatalf("save NZBGet: %d", code)
	}
	e.do("POST", fmt.Sprintf("/api/v1/downloadclients/%d/test", nz.ID), nil, &test)
	if !test.OK || len(test.Warnings) != 1 {
		t.Fatalf("saved test: %+v", test)
	}
	saved := nzbgetClient(t, e.client)
	if saved.Categories["movie"] != "movies" || saved.LocalPath != e.downloads || saved.Password != "" {
		t.Fatalf("saved client: %+v", saved)
	}

	var rq requestResponse
	if code := e.do("POST", "/api/v1/requests", map[string]any{"type": "movie", "tmdbId": 693134}, &rq); code != 201 {
		t.Fatalf("request: %d", code)
	}
	e.pass() // search and grab
	grabs := e.dl.Grabs()
	if len(grabs) != 1 || grabs[0].Category != "movies" || !strings.Contains(grabs[0].Name, "1080p.WEB-DL") {
		t.Fatalf("grabs: %+v", grabs)
	}
	time.Sleep(5 * time.Millisecond)
	e.pass() // downloaded; NZBGet is unpacking
	var queue []downloadResponse
	e.do("GET", "/api/v1/activity/queue", nil, &queue)
	if len(queue) != 1 || queue[0].Stage != "Unpacking" || queue[0].Progress != 1 || queue[0].Status != "downloading" || queue[0].Error != "" {
		t.Fatalf("queue while unpacking: %+v", queue)
	}
	e.pass() // in history under /downloads, which Bragi maps back
	files := listFiles(t, e.movieLib)
	if len(files) != 1 || !strings.HasSuffix(files[0], "[WEBDL-1080p].mkv") {
		t.Fatalf("movie library: %v", files)
	}
	if g := e.dl.Grabs(); !g[0].Removed {
		t.Fatal("the imported job was not cleared from NZBGet's history")
	}

	// TV still goes under the main category.
	e.do("POST", "/api/v1/requests", map[string]any{"type": "tv", "tmdbId": 95396, "seasons": []int{1}}, nil)
	e.pass()
	var tvCats []string
	for _, g := range e.dl.Grabs()[1:] {
		if len(g.ID) < 12 { // NZBGet ids are small numbers; torrents are hashes
			tvCats = append(tvCats, g.Category)
		}
	}
	if len(tvCats) == 0 || strings.Join(tvCats, ",") != strings.TrimSuffix(strings.Repeat("tv,", len(tvCats)), ",") {
		t.Fatalf("TV grabs through NZBGet went under %v, want tv", tvCats)
	}
}

// TestNZBGetDamagedRelease checks a WARNING/DAMAGED result is a failure:
// the release is blocklisted and Bragi grabs another one.
func TestNZBGetDamagedRelease(t *testing.T) {
	e := newTVEnv(t)
	if code := e.do("POST", "/api/v1/libraries", map[string]any{"mediaType": "movie", "path": e.movieLib, "qualityPresetId": 1}, nil); code != 201 {
		t.Fatalf("movie library: %d", code)
	}
	e.dl.NZBGetResult = func(name string) string {
		if strings.Contains(name, "WEB-DL") {
			return "WARNING/DAMAGED"
		}
		return ""
	}
	var rq requestResponse
	e.do("POST", "/api/v1/requests", map[string]any{"type": "movie", "tmdbId": 693134}, &rq)
	e.pass()
	time.Sleep(5 * time.Millisecond)
	e.pass() // damaged: failed, blocklisted, searched again
	var history []downloadResponse
	e.do("GET", "/api/v1/activity/history", nil, &history)
	if len(history) == 0 || history[0].Status != "failed" || history[0].Error != "NZBGet finished with damaged files that were not repaired" {
		t.Fatalf("history: %+v", history)
	}
	ctx := context.Background()
	e.movies.Pass(ctx)
	grabs := e.dl.Grabs()
	if len(grabs) < 2 || strings.Contains(grabs[len(grabs)-1].Name, "WEB-DL") {
		t.Fatalf("no other release was grabbed: %+v", grabs)
	}
	if !grabs[0].Removed {
		t.Fatal("the damaged job was not removed from NZBGet")
	}
}

// TestNZBGetAudiobook sends an audiobook through NZBGet under its own
// category and imports it.
func TestNZBGetAudiobook(t *testing.T) {
	e := newBookEnv(t, true)
	nz := nzbgetClient(t, e.client)
	nz.Categories = map[string]string{"audiobook": "audiobooks"}
	if code := e.do("PUT", fmt.Sprintf("/api/v1/downloadclients/%d", nz.ID), nz, nil); code != 200 {
		t.Fatalf("save NZBGet: %d", code)
	}
	var rq requestResponse
	if code := e.do("POST", "/api/v1/requests", map[string]any{"type": "audiobook", "bookId": "OL1000001W"}, &rq); code != 201 {
		t.Fatalf("request: %d", code)
	}
	ctx := context.Background()
	e.books.Pass(ctx)
	var nzb []string
	for _, g := range e.dl.Grabs() {
		if len(g.ID) < 12 { // NZBGet ids are small numbers; torrents are hashes
			nzb = append(nzb, g.Category+" "+g.Name)
		}
	}
	if len(nzb) != 1 || !strings.HasPrefix(nzb[0], "audiobooks ") || !strings.Contains(nzb[0], "[M4B]") {
		t.Fatalf("NZBGet grabs: %v", nzb)
	}
	time.Sleep(5 * time.Millisecond)
	e.books.Pass(ctx)
	files := listFiles(t, e.audio)
	if len(files) == 0 || !strings.HasSuffix(files[0], ".m4b") {
		t.Fatalf("audiobook library: %v", files)
	}
}
