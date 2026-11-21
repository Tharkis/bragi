package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tharkis/bragi/internal/books"
	"github.com/Tharkis/bragi/internal/db"
	"github.com/Tharkis/bragi/internal/events"
	"github.com/Tharkis/bragi/internal/fakes"
	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/store"
)

type bookEnv struct {
	*client
	books     *books.Service
	ebooks    string
	audio     string
	downloads string
	slskdDir  string
	dl        *fakes.Downloader
}

// newBookEnv starts Bragi with fake Open Library, Prowlarr, NZBGet,
// qBittorrent and slskd, an admin signed in, and a Books and an Audiobooks
// library. withIndexer adds Prowlarr; without it only Soulseek searches.
func newBookEnv(t *testing.T, withIndexer bool) *bookEnv {
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
	e := &bookEnv{ebooks: filepath.Join(root, "media", "books"), audio: filepath.Join(root, "media", "audiobooks"),
		downloads: filepath.Join(root, "downloads"), slskdDir: filepath.Join(root, "downloads", "soulseek")}
	for _, p := range []string{e.ebooks, e.audio, e.downloads, e.slskdDir} {
		os.MkdirAll(p, 0o755)
	}

	ol := httptest.NewServer(fakes.OpenLibrary())
	t.Cleanup(ol.Close)
	oldOL, oldCovers := metadata.OpenLibraryURL, metadata.CoversURL
	metadata.OpenLibraryURL, metadata.CoversURL = ol.URL, ol.URL
	t.Cleanup(func() { metadata.OpenLibraryURL, metadata.CoversURL = oldOL, oldCovers })

	var prowlarrURL string
	prowlarr := httptest.NewServer(fakes.Prowlarr(func() string { return prowlarrURL }))
	prowlarrURL = prowlarr.URL
	t.Cleanup(prowlarr.Close)
	dl := &fakes.Downloader{Dir: e.downloads, Duration: time.Millisecond}
	e.dl = dl
	nzbget := httptest.NewServer(dl.NZBGet())
	t.Cleanup(nzbget.Close)
	qbit := httptest.NewServer(dl.QBittorrent())
	t.Cleanup(qbit.Close)
	sl := &fakes.Slskd{Dir: e.slskdDir, Key: "slsk-key"} // finishes at once
	slsrv := httptest.NewServer(sl.Handler())
	t.Cleanup(slsrv.Close)

	st := store.New(d)
	hub := events.NewHub()
	hc := &http.Client{Timeout: 5 * time.Second}
	e.books = books.New(st, hc, nil, hub)
	srv := httptest.NewServer(New(Options{Store: st, Version: "test", HTTPClient: hc, Books: e.books, Events: hub}))
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	e.client = &client{t: t, url: srv.URL, hc: &http.Client{Jar: jar}}

	must := func(code, want int, what string) {
		t.Helper()
		if code != want {
			t.Fatalf("%s: HTTP %d, want %d", what, code, want)
		}
	}
	must(e.do("POST", "/api/v1/setup", credentials{"joe", "correct horse"}, nil), 201, "setup")
	must(e.do("POST", "/api/v1/libraries", map[string]any{"mediaType": "book", "path": e.ebooks}, nil), 201, "books library")
	must(e.do("POST", "/api/v1/libraries", map[string]any{"mediaType": "audiobook", "path": e.audio}, nil), 201, "audiobooks library")
	if withIndexer {
		must(e.do("POST", "/api/v1/indexers", map[string]any{"kind": "prowlarr", "baseUrl": prowlarr.URL, "apiKey": "k", "enabled": true}, nil), 201, "indexer")
		must(e.do("POST", "/api/v1/downloadclients", map[string]any{"kind": "nzbget", "baseUrl": nzbget.URL, "category": "books", "enabled": true}, nil), 201, "nzbget")
		must(e.do("POST", "/api/v1/downloadclients", map[string]any{"kind": "qbittorrent", "baseUrl": qbit.URL, "category": "books", "enabled": true}, nil), 201, "qbittorrent")
	}
	// The downloads folder is left empty: Bragi asks slskd for it.
	must(e.do("POST", "/api/v1/downloadclients", map[string]any{"kind": "slskd", "baseUrl": slsrv.URL, "apiKey": "slsk-key", "enabled": true}, nil), 201, "slskd")
	return e
}

func TestBookRequestToImport(t *testing.T) {
	e := newBookEnv(t, true)
	ctx := context.Background()

	var found []bookResult
	if code := e.do("GET", "/api/v1/discover/search?type=book&q=hail+mary", nil, &found); code != 200 || len(found) != 1 {
		t.Fatalf("discover: %d %+v", code, found)
	}
	if found[0].Author != "Andy Weir" || found[0].Year != 2021 || !strings.HasSuffix(found[0].CoverURL, "/b/id/900001-M.jpg") {
		t.Fatalf("discover result: %+v", found[0])
	}

	var rq requestResponse
	if code := e.do("POST", "/api/v1/requests", map[string]any{"type": "book", "bookId": found[0].BookID}, &rq); code != 201 {
		t.Fatalf("create request: %d", code)
	}
	if rq.Status != "approved" || rq.Progress == nil || rq.Author != "Andy Weir" || rq.ForeignID != "OL1000001W" {
		t.Fatalf("request after create: %+v progress %+v", rq.Request, rq.Progress)
	}
	bookID := rq.Progress.SeriesID

	var b bookResponse
	e.do("GET", fmt.Sprintf("/api/v1/books/%d", bookID), nil, &b)
	if b.Folder != "Andy Weir/Project Hail Mary (2021)" || strings.Contains(b.Overview, "----") || b.Status != "wanted" {
		t.Fatalf("book: %+v", b.Book)
	}

	// The Usenet EPUB beats the torrent EPUB, the PDF, the audiobook and
	// another of the author's books.
	e.books.Pass(ctx)
	var queue []downloadResponse
	e.do("GET", "/api/v1/activity/queue", nil, &queue)
	if len(queue) != 1 || queue[0].ReleaseTitle != "Andy Weir - Project Hail Mary (2021) [EPUB]" || queue[0].MediaType != "book" ||
		queue[0].Label != "Ebook" || queue[0].Quality != "EPUB" {
		t.Fatalf("queue after first pass: %+v", queue)
	}

	time.Sleep(5 * time.Millisecond)
	e.books.Pass(ctx)
	files := listFiles(t, e.ebooks)
	want := filepath.Join("Andy Weir", "Project Hail Mary (2021)", "Project Hail Mary - Andy Weir.epub")
	if len(files) != 1 || files[0] != want {
		t.Fatalf("library files %v, want %s", files, want)
	}
	e.do("GET", fmt.Sprintf("/api/v1/requests/%d", rq.ID), nil, &rq)
	if rq.Status != "available" {
		t.Fatalf("request after import: %s", rq.Status)
	}
	e.do("GET", fmt.Sprintf("/api/v1/books/%d", bookID), nil, &b)
	if b.Status != "imported" || len(b.Files) != 1 || b.Files[0].Format != "EPUB" {
		t.Fatalf("book after import: %+v", b.Book)
	}

	// Discover now shows it as in the library.
	found = nil
	e.do("GET", "/api/v1/discover/search?type=book&q=hail+mary", nil, &found)
	if found[0].ItemID != bookID || found[0].Requested != "available" {
		t.Fatalf("discover after import: %+v", found[0])
	}
	// The same work as an audiobook is a separate item.
	found = nil
	e.do("GET", "/api/v1/discover/search?type=audiobook&q=hail+mary", nil, &found)
	if found[0].ItemID != 0 || found[0].Requested != "" {
		t.Fatalf("audiobook discover: %+v", found[0])
	}
}

func TestAudiobookTorrentHardlinksTracks(t *testing.T) {
	e := newBookEnv(t, true)
	ctx := context.Background()
	var rq requestResponse
	if code := e.do("POST", "/api/v1/requests", map[string]any{"type": "audiobook", "bookId": "OL1000002W"}, &rq); code != 201 {
		t.Fatalf("create request: %d", code)
	}
	id := rq.Progress.SeriesID

	// Manual pick: the MP3 torrent, which has two disc folders.
	var rels []bookReleaseResponse
	if code := e.do("GET", fmt.Sprintf("/api/v1/books/%d/releases", id), nil, &rels); code != 200 {
		t.Fatalf("releases: %d", code)
	}
	// Prowlarr: M4B, MP3, and an EPUB (rejected). Soulseek: a folder of
	// MP3s and an M4B; the ebook peer has no audio.
	if len(rels) != 5 || rels[0].Format != "M4B" || rels[len(rels)-1].Rejected != "This is an ebook" {
		for _, r := range rels {
			t.Logf("%s %s %q", r.Protocol, r.Title, r.Rejected)
		}
		t.Fatalf("releases: %d", len(rels))
	}
	var mp3 *bookReleaseResponse
	for i := range rels {
		if rels[i].Protocol == integrations.Torrent && rels[i].Format == "MP3" {
			mp3 = &rels[i]
		}
	}
	if mp3 == nil {
		t.Fatal("no MP3 torrent")
	}
	if code := e.do("POST", fmt.Sprintf("/api/v1/books/%d/grab", id), map[string]any{"release": mp3.Release}, nil); code != 200 {
		t.Fatalf("grab: %d", code)
	}
	time.Sleep(5 * time.Millisecond) // the fake torrent finishes after 1 ms
	e.books.Pass(ctx)                // poll: done, import
	files := listFiles(t, e.audio)
	if len(files) != 6 || files[0] != filepath.Join("Susanna Clarke", "Piranesi (2020)", "CD1 - 01 - Chapter 1.mp3") {
		t.Fatalf("audiobook files: %v", files)
	}
	// Hardlinked: the torrent's files are still there to seed.
	src := filepath.Join(e.downloads, "Susanna Clarke - Piranesi [MP3 64kbps]", "CD1", "01 - Chapter 1.mp3")
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("torrent source was moved: %v", err)
	}
	var b bookResponse
	e.do("GET", fmt.Sprintf("/api/v1/books/%d", id), nil, &b)
	if b.Status != "imported" || len(b.Files) != 6 {
		t.Fatalf("book after import: %s, %d files", b.Status, len(b.Files))
	}
}

func TestSoulseekOnly(t *testing.T) {
	e := newBookEnv(t, false)
	ctx := context.Background()

	// Settings lists slskd and its test passes.
	var test testResult
	var list []map[string]any
	e.do("GET", "/api/v1/downloadclients", nil, &list)
	e.do("POST", fmt.Sprintf("/api/v1/downloadclients/%v/test", list[0]["id"]), nil, &test)
	if !test.OK {
		t.Fatalf("slskd test: %+v", test)
	}

	// An ebook from Soulseek: the EPUB from the peer with a free slot.
	var rq requestResponse
	e.do("POST", "/api/v1/requests", map[string]any{"type": "book", "bookId": "OL1000004W"}, &rq)
	e.books.Pass(ctx)
	var queue []downloadResponse
	e.do("GET", "/api/v1/activity/queue", nil, &queue)
	if len(queue) != 1 || queue[0].Protocol != "soulseek" || !strings.HasSuffix(queue[0].ReleaseTitle, ".epub") {
		t.Fatalf("queue: %+v", queue)
	}
	e.books.Pass(ctx)
	files := listFiles(t, e.ebooks)
	if len(files) != 1 || files[0] != filepath.Join("Frank Herbert", "Dune (1965)", "Dune - Frank Herbert.epub") {
		t.Fatalf("ebook files: %v", files)
	}

	// An audiobook from Soulseek: the single M4B from the free peer beats
	// twelve MP3s from a peer with a queue.
	e.do("POST", "/api/v1/requests", map[string]any{"type": "audiobook", "bookId": "OL1000004W"}, &rq)
	e.books.Pass(ctx)
	e.books.Pass(ctx)
	files = listFiles(t, e.audio)
	if len(files) != 1 || files[0] != filepath.Join("Frank Herbert", "Dune (1965)", "Dune.m4b") {
		t.Fatalf("audiobook files: %v", files)
	}
	// Moved out of slskd's folder, not copied.
	if _, err := os.Stat(filepath.Join(e.slskdDir, "Frank Herbert - Dune", "Dune.m4b")); err == nil {
		t.Fatal("the M4B was left in slskd's downloads")
	}
	var history []downloadResponse
	e.do("GET", "/api/v1/activity/history", nil, &history)
	if len(history) != 2 || history[0].Status != "imported" || history[1].Status != "imported" {
		t.Fatalf("history: %+v", history)
	}

	// A folder of MP3s from a peer, picked by hand: every track is kept.
	e.do("POST", "/api/v1/requests", map[string]any{"type": "audiobook", "bookId": "OL1000002W"}, &rq)
	id := rq.Progress.SeriesID
	var rels []bookReleaseResponse
	e.do("GET", fmt.Sprintf("/api/v1/books/%d/releases", id), nil, &rels)
	var folder *bookReleaseResponse
	for i := range rels {
		if rels[i].Format == "MP3" {
			folder = &rels[i]
		}
	}
	if folder == nil || folder.Rejected != "" || folder.Title != "Susanna Clarke / Piranesi [MP3]" {
		t.Fatalf("no MP3 folder in %+v", rels)
	}
	if code := e.do("POST", fmt.Sprintf("/api/v1/books/%d/grab", id), map[string]any{"release": folder.Release}, nil); code != 200 {
		t.Fatalf("grab: %d", code)
	}
	e.books.Pass(ctx)
	files = listFiles(t, filepath.Join(e.audio, "Susanna Clarke"))
	if len(files) != 13 || files[0] != filepath.Join("Piranesi (2020)", "01 - Piranesi.mp3") || files[12] != filepath.Join("Piranesi (2020)", "cover.jpg") {
		t.Fatalf("MP3 audiobook files: %v", files)
	}
}

func TestBookRequestNeedsLibrary(t *testing.T) {
	e := newBookEnv(t, false)
	var libs []store.Library
	e.do("GET", "/api/v1/libraries", nil, &libs)
	for _, l := range libs {
		if l.MediaType == "audiobook" {
			e.do("DELETE", fmt.Sprintf("/api/v1/libraries/%d", l.ID), nil, nil)
		}
	}
	var rq requestResponse
	e.do("POST", "/api/v1/requests", map[string]any{"type": "audiobook", "bookId": "OL1000003W"}, &rq)
	if rq.Status != "pending" || !strings.Contains(rq.Reason, "Audiobooks library") {
		t.Fatalf("request without a library: %s %q", rq.Status, rq.Reason)
	}
	if code := e.do("POST", "/api/v1/requests", map[string]any{"type": "book", "bookId": "nope"}, nil); code != 400 {
		t.Fatalf("bad book id: %d", code)
	}
}
