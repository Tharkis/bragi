package api

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tharkis/bragi/internal/store"
)

// onlyTorrentClient turns off the env's other download clients and adds
// kind, served by the env's fake downloader, in its own container: it sees
// Bragi's downloads folder as /downloads.
func onlyTorrentClient(t *testing.T, e *tvEnv, kind string) store.Integration {
	t.Helper()
	var clients []store.Integration
	e.do("GET", "/api/v1/downloadclients", nil, &clients)
	for _, dc := range clients {
		dc.Enabled = false
		if code := e.do("PUT", fmt.Sprintf("/api/v1/downloadclients/%d", dc.ID), dc, nil); code != 200 {
			t.Fatalf("turn off %s: %d", dc.Kind, code)
		}
	}
	e.dl.TorrentRoot = "/downloads"
	var srv *httptest.Server
	in := store.Integration{Kind: kind, Category: "tv", Enabled: true,
		Categories: map[string]string{"movie": "movies"}, RemotePath: "/downloads", LocalPath: e.downloads}
	switch kind {
	case "transmission":
		srv = httptest.NewServer(e.dl.Transmission())
		in.Username, in.Password = "admin", "secret"
	case "deluge":
		srv = httptest.NewServer(e.dl.Deluge())
		in.Password = "deluge"
	}
	t.Cleanup(srv.Close)
	in.BaseURL = srv.URL
	var test testResult
	e.do("POST", "/api/v1/downloadclients/test", in, &test)
	if !test.OK || len(test.Warnings) != 0 {
		t.Fatalf("test %s: %+v", kind, test)
	}
	var out store.Integration
	if code := e.do("POST", "/api/v1/downloadclients", in, &out); code != 201 {
		t.Fatalf("save %s: %d", kind, code)
	}
	if code := e.do("POST", "/api/v1/libraries", map[string]any{"mediaType": "movie", "path": e.movieLib, "qualityPresetId": 1}, nil); code != 201 {
		t.Fatalf("movie library: %d", code)
	}
	return out
}

// TestTorrentClientsMovie runs a movie through Transmission and Deluge,
// each in its own container with a movies category.
func TestTorrentClientsMovie(t *testing.T) {
	for _, kind := range []string{"transmission", "deluge"} {
		t.Run(kind, func(t *testing.T) {
			e := newTVEnv(t)
			onlyTorrentClient(t, e, kind)
			var rq requestResponse
			if code := e.do("POST", "/api/v1/requests", map[string]any{"type": "movie", "tmdbId": 693134}, &rq); code != 201 {
				t.Fatalf("request: %d", code)
			}
			e.pass() // search and grab
			grabs := e.dl.Grabs()
			if len(grabs) != 1 || grabs[0].Category != "movies" {
				t.Fatalf("grabs: %+v", grabs)
			}
			time.Sleep(5 * time.Millisecond)
			e.pass() // finished under /downloads, which Bragi maps back
			files := listFiles(t, e.movieLib)
			if len(files) != 1 || !strings.HasSuffix(files[0], ".mkv") {
				var history []downloadResponse
				e.do("GET", "/api/v1/activity/history", nil, &history)
				t.Fatalf("movie library: %v; history %+v", files, history)
			}
		})
	}
}

// TestTorrentClientsFailedDownload checks a client error fails the
// download, removes it from the client and blocklists the release. The
// fake indexer has one torrent, so nothing else is grabbed.
func TestTorrentClientsFailedDownload(t *testing.T) {
	for _, kind := range []string{"transmission", "deluge"} {
		t.Run(kind, func(t *testing.T) {
			e := newTVEnv(t)
			onlyTorrentClient(t, e, kind)
			e.dl.Duration = 20 * time.Millisecond
			first := ""
			e.dl.TorrentError = func(name string) string {
				if first == "" || name == first {
					first = name
					return "No space left on device"
				}
				return ""
			}
			e.do("POST", "/api/v1/requests", map[string]any{"type": "movie", "tmdbId": 693134}, nil)
			e.pass()
			time.Sleep(12 * time.Millisecond)
			e.pass() // half done: the client reports the error
			var history []downloadResponse
			e.do("GET", "/api/v1/activity/history", nil, &history)
			if len(history) == 0 || history[0].Status != "failed" || !strings.Contains(history[0].Error, "No space left on device") {
				t.Fatalf("history: %+v", history)
			}
			e.movies.Pass(context.Background())
			grabs := e.dl.Grabs()
			if len(grabs) != 1 || !grabs[0].Removed {
				t.Fatalf("grabs after failure: %+v", grabs)
			}
		})
	}
}

// TestDelugeWithoutLabelPlugin warns but still downloads.
func TestDelugeWithoutLabelPlugin(t *testing.T) {
	e := newTVEnv(t)
	e.dl.DelugeNoLabel = true
	srv := httptest.NewServer(e.dl.Deluge())
	defer srv.Close()
	var test testResult
	e.do("POST", "/api/v1/downloadclients/test", map[string]any{"kind": "deluge", "baseUrl": srv.URL, "password": "deluge", "category": "tv"}, &test)
	if !test.OK || len(test.Warnings) != 1 || !strings.Contains(test.Warnings[0], "Label plugin") {
		t.Fatalf("test: %+v", test)
	}
	e.do("POST", "/api/v1/downloadclients/test", map[string]any{"kind": "deluge", "baseUrl": srv.URL, "password": "nope", "category": "tv"}, &test)
	if test.OK || !strings.Contains(test.Error, "rejected") {
		t.Fatalf("bad password: %+v", test)
	}
}
