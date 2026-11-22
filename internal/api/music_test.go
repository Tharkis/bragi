package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Tharkis/bragi/internal/db"
	"github.com/Tharkis/bragi/internal/events"
	"github.com/Tharkis/bragi/internal/fakes"
	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/music"
	"github.com/Tharkis/bragi/internal/store"
)

type musicEnv struct {
	*client
	music     *music.Service
	lib       string
	downloads string
	store     *store.Store
}

// newMusicEnv starts Bragi with fake MusicBrainz, slskd and (withIndexer)
// Prowlarr, NZBGet and qBittorrent, an admin signed in and a Music library.
func newMusicEnv(t *testing.T, withSlskd, withIndexer bool) *musicEnv {
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
	e := &musicEnv{lib: filepath.Join(root, "media", "music"), downloads: filepath.Join(root, "downloads")}
	slskDir := filepath.Join(e.downloads, "soulseek")
	for _, p := range []string{e.lib, e.downloads, slskDir} {
		os.MkdirAll(p, 0o755)
	}

	mb := httptest.NewServer(fakes.MusicBrainz())
	t.Cleanup(mb.Close)
	old := []string{metadata.MusicBrainzURL, metadata.CoverArtURL, metadata.ListenBrainzURL, metadata.CommonsURL}
	oldGap := metadata.MusicBrainzInterval
	metadata.MusicBrainzURL, metadata.CoverArtURL, metadata.ListenBrainzURL, metadata.CommonsURL = mb.URL+"/ws/2", mb.URL, mb.URL, mb.URL
	metadata.MusicBrainzInterval = 0
	t.Cleanup(func() {
		metadata.MusicBrainzURL, metadata.CoverArtURL, metadata.ListenBrainzURL, metadata.CommonsURL = old[0], old[1], old[2], old[3]
		metadata.MusicBrainzInterval = oldGap
	})

	var prowlarrURL string
	prowlarr := httptest.NewServer(fakes.Prowlarr(func() string { return prowlarrURL }))
	prowlarrURL = prowlarr.URL
	t.Cleanup(prowlarr.Close)
	dl := &fakes.Downloader{Dir: e.downloads, Duration: time.Millisecond}
	nzbget := httptest.NewServer(dl.NZBGet())
	t.Cleanup(nzbget.Close)
	qbit := httptest.NewServer(dl.QBittorrent())
	t.Cleanup(qbit.Close)
	sl := &fakes.Slskd{Dir: slskDir, Key: "slsk-key"}
	slsrv := httptest.NewServer(sl.Handler())
	t.Cleanup(slsrv.Close)

	e.store = store.New(d)
	hub := events.NewHub()
	hc := &http.Client{Timeout: 5 * time.Second}
	e.music = music.New(e.store, hc, nil, hub)
	srv := httptest.NewServer(New(Options{Store: e.store, Version: "test", HTTPClient: hc, Music: e.music, Events: hub}))
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
	must(e.do("POST", "/api/v1/libraries", map[string]any{"mediaType": "music", "path": e.lib}, nil), 201, "music library")
	if withIndexer {
		must(e.do("POST", "/api/v1/indexers", map[string]any{"kind": "prowlarr", "baseUrl": prowlarr.URL, "apiKey": "k", "enabled": true}, nil), 201, "indexer")
		must(e.do("POST", "/api/v1/downloadclients", map[string]any{"kind": "nzbget", "baseUrl": nzbget.URL, "category": "bragi",
			"categories": map[string]string{"music": "music"}, "enabled": true}, nil), 201, "nzbget")
		must(e.do("POST", "/api/v1/downloadclients", map[string]any{"kind": "qbittorrent", "baseUrl": qbit.URL, "category": "bragi", "enabled": true}, nil), 201, "qbittorrent")
	}
	if withSlskd {
		must(e.do("POST", "/api/v1/downloadclients", map[string]any{"kind": "slskd", "baseUrl": slsrv.URL, "apiKey": "slsk-key", "enabled": true}, nil), 201, "slskd")
	}
	return e
}

type discoverMusic struct {
	Artists []artistResult `json:"artists"`
	Albums  []albumResult  `json:"albums"`
}

type artistPage struct {
	artistResponse
	Albums []albumResponse `json:"albumList"`
}

func (e *musicEnv) artistPage(id int64) artistPage {
	var p artistPage
	if code := e.do("GET", fmt.Sprintf("/api/v1/artists/%d", id), nil, &p); code != 200 {
		e.t.Fatalf("artist %d: HTTP %d", id, code)
	}
	return p
}

func albumNamed(p artistPage, title string) albumResponse {
	for _, a := range p.Albums {
		if a.Title == title {
			return a
		}
	}
	return albumResponse{}
}

func TestMusicAlbumFromSoulseekToLibrary(t *testing.T) {
	e := newMusicEnv(t, true, false)
	ctx := context.Background()

	var found discoverMusic
	if code := e.do("GET", "/api/v1/discover/search?type=music&q=radiohead", nil, &found); code != 200 || len(found.Artists) != 1 {
		t.Fatalf("discover: %d %+v", code, found)
	}
	artist := found.Artists[0]
	if artist.Name != "Radiohead" || artist.ItemID != 0 {
		t.Fatalf("artist: %+v", artist)
	}
	var page struct {
		artistResult
		Albums []albumResult `json:"albums"`
		Types  []string      `json:"monitorTypes"`
	}
	if code := e.do("GET", "/api/v1/discover/artist/"+artist.ID, nil, &page); code != 200 {
		t.Fatalf("discover artist: %d", code)
	}
	if len(page.Albums) != 9 || page.ImageURL == "" || !slices.Equal(page.Types, []string{"Album", "EP", "Single"}) {
		t.Fatalf("discover artist: %d albums, image %q, types %v", len(page.Albums), page.ImageURL, page.Types)
	}
	var ok *albumResult
	for i := range page.Albums {
		if page.Albums[i].Title == "OK Computer" {
			ok = &page.Albums[i]
		}
		if page.Albums[i].Title == "I Might Be Wrong: Live Recordings" && page.Albums[i].Type != "Live" {
			t.Fatalf("live album type: %+v", page.Albums[i])
		}
	}

	// Asking for one album adds the artist without following it.
	var rq requestResponse
	if code := e.do("POST", "/api/v1/requests", map[string]any{"type": "music", "albumId": ok.ID}, &rq); code != 201 {
		t.Fatalf("request: %d", code)
	}
	if rq.Status != "approved" || rq.Title != "OK Computer" || rq.Author != "Radiohead" || rq.Music == nil || rq.Music.Kind != "album" ||
		rq.Progress == nil || rq.Progress.AlbumID == 0 {
		t.Fatalf("request: %+v music %+v progress %+v", rq.Request, rq.Music, rq.Progress)
	}
	p := e.artistPage(rq.Progress.ArtistID)
	if p.MonitorNew || p.Albums == nil || len(p.Albums) != 9 || p.Monitored != 1 || p.Folder != "Radiohead" {
		t.Fatalf("artist after request: %+v", p.Artist)
	}

	// Soulseek: the FLAC folder with every track and a free slot wins over
	// the MP3 320 and the V0 folder missing two tracks.
	e.music.Pass(ctx)
	var queue []downloadResponse
	e.do("GET", "/api/v1/activity/queue", nil, &queue)
	if len(queue) != 1 || queue[0].Indexer != "Soulseek: losslesslibrary" || queue[0].Quality != "FLAC" || queue[0].Label != "OK Computer" ||
		queue[0].SeriesTitle != "Radiohead" || !strings.Contains(queue[0].PosterURL, "/release-group/"+ok.ID) {
		t.Fatalf("queue: %+v", queue)
	}

	e.music.Pass(ctx)
	files := listFiles(t, e.lib)
	if len(files) != 13 || files[0] != filepath.Join("Radiohead", "OK Computer (1997)", "01 - Airbag.flac") ||
		!slices.Contains(files, filepath.Join("Radiohead", "OK Computer (1997)", "cover.jpg")) ||
		!slices.Contains(files, filepath.Join("Radiohead", "OK Computer (1997)", "04 - Exit Music (For a Film).flac")) {
		t.Fatalf("library files: %v", files)
	}
	tags := music.ReadTags(filepath.Join(e.lib, "Radiohead", "OK Computer (1997)", "02 - Paranoid Android.flac"))
	if tags.Title != "Paranoid Android" || tags.Track != 2 || tags.Album != "OK Computer" || tags.AlbumArtist != "Radiohead" ||
		tags.AlbumID != ok.ID || tags.ArtistID != artist.ID {
		t.Fatalf("tags: %+v", tags)
	}
	var al albumResponse
	e.do("GET", fmt.Sprintf("/api/v1/albums/%d", rq.Progress.AlbumID), nil, &al)
	if al.Status != "imported" || al.Imported != 12 || al.Tracks != 12 || al.Quality != "FLAC" || al.TrackList[0].FilePath == "" {
		t.Fatalf("album after import: %+v", al.Album)
	}
	e.do("GET", fmt.Sprintf("/api/v1/requests/%d", rq.ID), nil, &rq)
	if rq.Status != "available" {
		t.Fatalf("request after import: %s", rq.Status)
	}
	// Soulseek's own folder is tidied away.
	if left := listFiles(t, filepath.Join(e.downloads, "soulseek")); len(left) != 0 {
		t.Fatalf("left in downloads: %v", left)
	}
}

func TestMusicFollowArtist(t *testing.T) {
	e := newMusicEnv(t, false, true)
	ctx := context.Background()
	daft := fakes.Artists[1]

	if code := e.do("POST", "/api/v1/requests", map[string]any{"type": "music", "artistId": daft.ID, "monitor": "bogus"}, nil); code != 400 {
		t.Fatalf("bad monitor: %d", code)
	}
	var rq requestResponse
	if code := e.do("POST", "/api/v1/requests", map[string]any{"type": "music", "artistId": daft.ID, "monitor": "all"}, &rq); code != 201 {
		t.Fatalf("request: %d", code)
	}
	p := e.artistPage(rq.Progress.ArtistID)
	// Studio albums are followed; the live album is not one of the types.
	if !p.MonitorNew || p.Monitored != 3 || albumNamed(p, "Alive 2007").Monitored || !albumNamed(p, "Discovery").Monitored ||
		!strings.Contains(p.Overview, "Group from France") {
		t.Fatalf("artist: %+v", p.Artist)
	}

	// Without Soulseek the indexers are asked: the FLAC on Usenet beats the
	// MP3 torrent; the discography and the 128 kbps MP3 are turned down.
	disc := albumNamed(p, "Discovery")
	var rels []albumReleaseResponse
	if code := e.do("GET", fmt.Sprintf("/api/v1/albums/%d/releases", disc.ID), nil, &rels); code != 200 || len(rels) != 4 {
		t.Fatalf("releases: %d %d", code, len(rels))
	}
	if rels[0].Quality != "FLAC" || rels[0].Protocol != integrations.Usenet || rels[1].Quality != "MP3-320" ||
		rels[2].Rejected == "" || rels[3].Rejected == "" {
		for _, r := range rels {
			t.Logf("%s %s %s %q", r.Protocol, r.Quality, r.Title, r.Rejected)
		}
		t.Fatal("ranking")
	}
	for _, r := range rels {
		if strings.Contains(r.Title, "Discography") && r.Rejected != "Looks like more than one album" ||
			strings.Contains(r.Title, "MP3 128") && r.Rejected != "MP3-128 is not in your quality list" {
			t.Fatalf("rejection: %s %q", r.Title, r.Rejected)
		}
	}

	e.music.Pass(ctx) // searches all three albums
	var queue []downloadResponse
	e.do("GET", "/api/v1/activity/queue", nil, &queue)
	if len(queue) != 3 {
		t.Fatalf("queue: %+v", queue)
	}
	time.Sleep(5 * time.Millisecond)
	e.music.Pass(ctx)
	files := listFiles(t, e.lib)
	if !slices.Contains(files, filepath.Join("Daft Punk", "Discovery (2001)", "04 - Harder, Better, Faster, Stronger.flac")) ||
		!slices.Contains(files, filepath.Join("Daft Punk", "Random Access Memories (2013)", "13 - Contact.flac")) {
		t.Fatalf("files: %v", files)
	}
	e.do("GET", fmt.Sprintf("/api/v1/requests/%d", rq.ID), nil, &rq)
	if rq.Status != "available" || rq.Progress.Imported != 3 || rq.Progress.Missing != 0 {
		t.Fatalf("request: %s %+v", rq.Status, rq.Progress)
	}
}

func TestMusicUpgradeAndDecision(t *testing.T) {
	e := newMusicEnv(t, true, false)
	ctx := context.Background()
	ph := fakes.Artists[3]
	var rq requestResponse
	if code := e.do("POST", "/api/v1/requests", map[string]any{"type": "music", "artistId": ph.ID, "monitor": "none",
		"albums": []string{ph.Albums[1].ID}}, &rq); code != 201 {
		t.Fatalf("request: %d", code)
	}
	p := e.artistPage(rq.Progress.ArtistID)
	punisher := albumNamed(p, "Punisher")
	if p.MonitorNew || p.Monitored != 1 || !punisher.Monitored {
		t.Fatalf("artist: %+v", p.Artist)
	}
	// Pick the V0 folder that misses two tracks by hand.
	var rels []albumReleaseResponse
	e.do("GET", fmt.Sprintf("/api/v1/albums/%d/releases", punisher.ID), nil, &rels)
	var partial *albumReleaseResponse
	for i := range rels {
		if strings.HasPrefix(rels[i].Rejected, "Missing tracks (9 of 11)") {
			partial = &rels[i]
		}
	}
	if partial == nil || partial.Quality != "MP3-V0" || rels[0].Quality != "FLAC" {
		t.Fatalf("releases: %+v", rels)
	}
	if code := e.do("POST", fmt.Sprintf("/api/v1/albums/%d/grab", punisher.ID), map[string]any{"release": partial.Release}, nil); code != 200 {
		t.Fatalf("grab: %d", code)
	}
	e.music.PollDownloads(ctx)
	var queue []downloadResponse
	e.do("GET", "/api/v1/activity/queue", nil, &queue)
	if len(queue) != 1 || queue[0].Status != "completed" || !strings.HasPrefix(queue[0].Error, music.NeedsDecision+"2 of 11 tracks are missing") {
		t.Fatalf("queue: %+v", queue)
	}
	if code := e.do("POST", fmt.Sprintf("/api/v1/activity/queue/%d/import", queue[0].ID), nil, nil); code != 200 {
		t.Fatalf("import anyway: %d", code)
	}
	var al albumResponse
	e.do("GET", fmt.Sprintf("/api/v1/albums/%d", punisher.ID), nil, &al)
	if al.Imported != 9 || al.Status != "upgrade_wanted" || al.Quality != "MP3-V0" {
		t.Fatalf("album after import: %+v", al.Album)
	}
	if n := len(listFiles(t, filepath.Join(e.lib, "Phoebe Bridgers", "Punisher (2020)"))); n != 10 {
		t.Fatalf("files: %d", n)
	}

	// The upgrade search finds the FLAC and replaces the MP3s.
	if code := e.do("POST", fmt.Sprintf("/api/v1/albums/%d/search", punisher.ID), nil, nil); code != 200 {
		t.Fatalf("search: %d", code)
	}
	e.music.Pass(ctx)
	e.music.Pass(ctx)
	e.do("GET", fmt.Sprintf("/api/v1/albums/%d", punisher.ID), nil, &al)
	files := listFiles(t, filepath.Join(e.lib, "Phoebe Bridgers", "Punisher (2020)"))
	if al.Status != "imported" || al.Imported != 11 || al.Quality != "FLAC" || len(files) != 12 || !strings.HasSuffix(files[0], ".flac") {
		t.Fatalf("after upgrade: %+v files %v", al.Album, files)
	}
}

func TestAdoptMusic(t *testing.T) {
	e := newMusicEnv(t, false, false)
	rad, fm := fakes.Artists[0], fakes.Artists[2]
	_, kidA := fakes.FindAlbum(rad.Albums[5].ID)

	// Tagged by Picard, in a Navidrome-style folder that says nothing.
	for i, title := range kidA.Tracks {
		p := filepath.Join(e.lib, "Incoming", "rip-0042", fmt.Sprintf("track%02d.flac", i+1))
		writeFile(t, p, string(fakes.FakeFLAC()))
		if err := music.WriteTags(p, music.TrackTags{Artist: "Radiohead", AlbumArtist: "Radiohead", Album: "Kid A", Title: title,
			Track: i + 1, Tracks: 10, Disc: 1, Discs: 1, AlbumID: kidA.ID}); err != nil {
			t.Fatal(err)
		}
	}
	// Untagged, named the Lidarr way, with a disc folder.
	for i, title := range fm.Albums[0].Tracks {
		writeFile(t, filepath.Join(e.lib, "Fleetwood Mac", "Rumours (1977)", "CD1", fmt.Sprintf("%02d - %s.mp3", i+1, title)), "mp3")
	}
	// Unknown.
	writeFile(t, filepath.Join(e.lib, "Some Band", "Demo Tape", "01 - Song.mp3"), "mp3")

	if code := e.do("POST", "/api/v1/libraries/scan?type=music", nil, nil); code != 202 {
		t.Fatalf("scan: %d", code)
	}
	st := e.waitScan("music")
	sort.Strings(st.Added)
	if !slices.Equal(st.Added, []string{"Fleetwood Mac - Rumours", "Radiohead - Kid A"}) || st.Files != 21 || len(st.Unmatched) != 1 {
		t.Fatalf("scan: %+v", st)
	}
	var artists []artistResponse
	e.do("GET", "/api/v1/artists", nil, &artists)
	if len(artists) != 2 || artists[0].Name != "Fleetwood Mac" || artists[0].Imported != 1 || artists[0].Monitored != 0 || artists[0].MonitorNew {
		t.Fatalf("artists: %+v", artists)
	}
	// Radiohead's folder follows where Kid A was found.
	p := e.artistPage(artists[1].ID)
	kid := albumNamed(p, "Kid A")
	if p.Folder != "Incoming" || kid.Status != "imported" || kid.Folder != "rip-0042" {
		t.Fatalf("radiohead: folder %q, kid a %+v", p.Folder, kid.Album)
	}

	// Matching the unknown folder by hand.
	var un []store.Unmatched
	e.do("GET", "/api/v1/libraries/unmatched?type=music", nil, &un)
	if len(un) != 1 || un[0].Path != "Some Band/Demo Tape" || un[0].Guess != "Some Band - Demo Tape" {
		t.Fatalf("unmatched: %+v", un)
	}
	if code := e.do("POST", fmt.Sprintf("/api/v1/libraries/unmatched/%d/match", un[0].ID), map[string]string{"id": rad.Albums[0].ID}, nil); code != 200 {
		t.Fatalf("match: %d", code)
	}
	// A second scan finds nothing new and asks MusicBrainz nothing.
	e.do("POST", "/api/v1/libraries/scan?type=music", nil, nil)
	st = e.waitScan("music")
	if len(st.Added) != 0 || st.Files != 0 || len(st.Unmatched) != 0 {
		t.Fatalf("second scan: %+v", st)
	}
}
