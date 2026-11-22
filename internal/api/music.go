package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/music"
	"github.com/Tharkis/bragi/internal/release"
	"github.com/Tharkis/bragi/internal/store"
)

// Music: discover artists and albums, request them, the music library and
// the manual pick. Requests go through the shared request endpoints, which
// hand music requests to the functions here.

// musicError turns MusicBrainz and pipeline failures into a status and a
// message for the UI.
func (s *Server) musicError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, music.ErrNoLibrary):
		writeError(w, http.StatusFailedDependency, err.Error())
	case errors.Is(err, metadata.ErrNoSuchMusic), errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "artist or album not found")
	default:
		s.log.Warn("music error", "err", err)
		writeError(w, http.StatusBadGateway, sentence(err.Error()))
	}
}

func albumCover(albumID string) string { return metadata.AlbumCoverURL(albumID, 250) }

// artistResult is an artist in Discover.
type artistResult struct {
	metadata.Artist
	// ItemID is set when the artist is in the library.
	ItemID int64 `json:"itemId,omitempty"`
	// Requested is the status of the newest open request for the artist.
	Requested string `json:"requested,omitempty"`
}

// albumResult is an album in Discover or on an artist's page.
type albumResult struct {
	metadata.Album
	CoverURL string `json:"coverUrl,omitempty"`
	// ItemID is set when the album is in the library; Status and
	// Monitored then say how it stands.
	ItemID    int64  `json:"itemId,omitempty"`
	Status    string `json:"status,omitempty"`
	Monitored bool   `json:"monitored,omitempty"`
	Requested string `json:"requested,omitempty"`
}

func (s *Server) openMusicRequest(ctx context.Context, foreignID string) string {
	reqs, err := s.store.ListRequests(ctx, store.RequestFilter{MediaType: "music", ForeignID: foreignID})
	if err != nil {
		return ""
	}
	for _, rq := range reqs {
		if rq.Status == "pending" || rq.Status == "approved" || rq.Status == "available" {
			return rq.Status
		}
	}
	return ""
}

func (s *Server) toArtistResult(ctx context.Context, a metadata.Artist) artistResult {
	out := artistResult{Artist: a}
	out.Albums = nil
	if ar, err := s.store.GetArtistByForeignID(ctx, a.ID); err == nil {
		out.ItemID = ar.ID
	}
	out.Requested = s.openMusicRequest(ctx, a.ID)
	return out
}

func (s *Server) toAlbumResult(ctx context.Context, a metadata.Album) albumResult {
	out := albumResult{Album: a, CoverURL: albumCover(a.ID)}
	if al, err := s.store.GetAlbumByForeignID(ctx, a.ID); err == nil {
		out.ItemID, out.Status, out.Monitored = al.ID, al.Status, al.Monitored
	}
	out.Requested = s.openMusicRequest(ctx, a.ID)
	return out
}

// handleDiscoverMusic is GET /discover/search?type=music: artists and
// albums matching the words.
func (s *Server) handleDiscoverMusic(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	out := struct {
		Artists []artistResult `json:"artists"`
		Albums  []albumResult  `json:"albums"`
	}{Artists: []artistResult{}, Albums: []albumResult{}}
	if q == "" {
		writeJSON(w, http.StatusOK, out)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	mb := s.music.MB()
	artists, err := mb.SearchArtists(ctx, q)
	if err != nil {
		s.musicError(w, err)
		return
	}
	albums, err := mb.SearchAlbums(ctx, q)
	if err != nil {
		s.musicError(w, err)
		return
	}
	for i, a := range artists {
		if i == 8 {
			break
		}
		out.Artists = append(out.Artists, s.toArtistResult(ctx, a))
	}
	for _, a := range albums {
		out.Albums = append(out.Albums, s.toAlbumResult(ctx, a))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDiscoverArtist is GET /discover/artist/{id}: an artist and every
// album, for the request dialog.
func (s *Server) handleDiscoverArtist(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	a, err := s.music.MB().Artist(ctx, r.PathValue("id"))
	if err != nil {
		s.musicError(w, err)
		return
	}
	out := struct {
		artistResult
		Albums []albumResult `json:"albums"`
		// Types are the album types a followed artist monitors.
		Types []string `json:"monitorTypes"`
	}{artistResult: s.toArtistResult(ctx, *a), Albums: []albumResult{}, Types: s.music.AlbumTypes(ctx)}
	for _, al := range a.Albums {
		out.Albums = append(out.Albums, s.toAlbumResult(ctx, al))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDiscoverAlbum is GET /discover/album/{id}: one album and its
// track list.
func (s *Server) handleDiscoverAlbum(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	mb := s.music.MB()
	a, err := mb.Album(ctx, r.PathValue("id"))
	if err != nil {
		s.musicError(w, err)
		return
	}
	out := struct {
		albumResult
		Tracks []metadata.Track `json:"tracks"`
	}{albumResult: s.toAlbumResult(ctx, *a), Tracks: []metadata.Track{}}
	if rel, err := mb.BestRelease(ctx, a.ID); err == nil {
		out.Tracks = rel.Tracks
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTrendingMusic is GET /discover/trending?type=music.
func (s *Server) handleTrendingMusic(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	albums, err := s.music.MB().TrendingAlbums(ctx, 20)
	if err != nil {
		s.log.Warn("trending music", "err", err)
		writeJSON(w, http.StatusOK, []albumResult{})
		return
	}
	out := make([]albumResult, 0, len(albums))
	for _, a := range albums {
		out = append(out, s.toAlbumResult(ctx, a))
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- Requests ----

// musicScope is what a music request asks for, kept in requests.scope.
type musicScope struct {
	// Kind is "artist" (follow an artist) or "album" (one album).
	Kind string `json:"kind"`
	// Artist requests: what to monitor, and albums picked by hand.
	Monitor string   `json:"monitor,omitempty"`
	Albums  []string `json:"albums,omitempty"`
	// Album requests: whose album it is.
	ArtistID string `json:"artistId,omitempty"`
}

// createMusicRequest handles POST /requests for type music: either
// {"artistId", "monitor", "albums"} or {"albumId"}.
func (s *Server) createMusicRequest(w http.ResponseWriter, r *http.Request, in requestBody) {
	u := userFrom(r.Context())
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	var rq store.Request
	switch {
	case in.AlbumID != "":
		if !metadata.ValidMBID(in.AlbumID) {
			writeError(w, http.StatusBadRequest, "albumId must be a MusicBrainz release group id")
			return
		}
		a, err := s.music.MB().Album(ctx, in.AlbumID)
		if err != nil {
			s.musicError(w, err)
			return
		}
		scope, _ := json.Marshal(musicScope{Kind: "album", ArtistID: a.ArtistID})
		rq = store.Request{UserID: u.ID, MediaType: "music", ForeignID: a.ID, Author: a.Artist, Title: a.Title, Year: a.Year,
			PosterPath: albumCover(a.ID), Scope: string(scope), Status: "pending"}
	case in.ArtistID != "":
		if !metadata.ValidMBID(in.ArtistID) {
			writeError(w, http.StatusBadRequest, "artistId must be a MusicBrainz artist id")
			return
		}
		mode := in.Monitor
		if mode == "" {
			mode = music.MonitorAll
		}
		if !slices.Contains([]string{music.MonitorAll, music.MonitorFuture, music.MonitorNone}, mode) {
			writeError(w, http.StatusBadRequest, "monitor must be all, future or none")
			return
		}
		for _, id := range in.Albums {
			if !metadata.ValidMBID(id) {
				writeError(w, http.StatusBadRequest, "albums must be MusicBrainz release group ids")
				return
			}
		}
		if mode == music.MonitorNone && len(in.Albums) == 0 {
			writeError(w, http.StatusBadRequest, "pick at least one album, or ask for all of them")
			return
		}
		a, err := s.music.MB().Artist(ctx, in.ArtistID)
		if err != nil {
			s.musicError(w, err)
			return
		}
		scope, _ := json.Marshal(musicScope{Kind: "artist", Monitor: mode, Albums: in.Albums})
		poster := a.ImageURL
		if poster == "" && len(a.Albums) > 0 {
			poster = albumCover(a.Albums[len(a.Albums)-1].ID)
		}
		rq = store.Request{UserID: u.ID, MediaType: "music", ForeignID: a.ID, Title: a.Name, PosterPath: poster, Scope: string(scope), Status: "pending"}
	default:
		writeError(w, http.StatusBadRequest, "artistId or albumId is required")
		return
	}
	if mine, err := s.store.ListRequests(r.Context(), store.RequestFilter{UserID: u.ID, MediaType: "music", ForeignID: rq.ForeignID}); err == nil {
		for _, old := range mine {
			if old.Status == "pending" {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "you already asked for this", "request": s.toRequestResponse(r.Context(), old, false)})
				return
			}
		}
	}
	out, err := s.store.CreateRequest(r.Context(), rq)
	if err != nil {
		s.storeError(w, err, "request")
		return
	}
	s.log.Info("request created", "user", u.Username, "type", "music", "title", out.Title)
	if u.Role == "admin" || u.AutoApprove {
		out, _ = s.approve(r.Context(), out, "")
	}
	s.events.Publish(eventRequest(out))
	writeJSON(w, http.StatusCreated, s.toRequestResponse(r.Context(), *out, false))
}

// approveMusic follows the artist or monitors the album, and marks the
// request approved, or leaves it pending with the reason.
func (s *Server) approveMusic(ctx context.Context, rq *store.Request, reason string) (*store.Request, error) {
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	defer cancel()
	var scope musicScope
	json.Unmarshal([]byte(rq.Scope), &scope)
	var itemID int64
	var err error
	if scope.Kind == "album" {
		var al *store.Album
		if al, err = s.music.AddAlbum(actx, rq.ForeignID, 0); err == nil {
			itemID = al.ID
		}
	} else {
		var ar *store.Artist
		if ar, err = s.music.AddArtist(actx, rq.ForeignID, music.AddOptions{Monitor: scope.Monitor, Albums: scope.Albums}); err == nil {
			itemID = ar.ID
		}
	}
	if err != nil {
		s.store.SetRequestStatus(ctx, rq.ID, "pending", "Could not add it: "+sentence(err.Error()), 0)
		out, _ := s.store.GetRequest(ctx, rq.ID)
		if out == nil {
			out = rq
		}
		return out, err
	}
	s.store.SetRequestStatus(ctx, rq.ID, "approved", reason, itemID)
	if scope.Kind == "album" {
		s.store.MarkMusicRequestsAvailable(ctx, itemID)
	}
	out, err := s.store.GetRequest(ctx, rq.ID)
	if err != nil {
		return rq, err
	}
	return out, nil
}

// musicRequestResponse fills a music request's picture and progress.
func (s *Server) musicRequestResponse(ctx context.Context, rq store.Request) requestResponse {
	out := requestResponse{Request: rq, PosterURL: rq.PosterPath}
	var scope musicScope
	json.Unmarshal([]byte(rq.Scope), &scope)
	out.Music = &scope
	if rq.MediaItemID == 0 {
		return out
	}
	if scope.Kind == "album" {
		if al, err := s.store.GetAlbum(ctx, rq.MediaItemID); err == nil {
			p := &requestProgress{AlbumID: al.ID, ArtistID: al.ArtistID, Episodes: al.Tracks, Imported: al.Imported, Activities: al.Activities}
			if al.Status != "imported" && al.Status != "upgrade_wanted" {
				p.Missing = max(al.Tracks-al.Imported, 1)
			}
			out.Progress = p
		}
		return out
	}
	if ar, err := s.store.GetArtist(ctx, rq.MediaItemID); err == nil {
		out.Progress = &requestProgress{ArtistID: ar.ID, Episodes: ar.Monitored, Imported: ar.Imported, Missing: ar.Wanted, Activities: ar.Activities}
	}
	return out
}

// ---- Library ----

type artistResponse struct {
	store.Artist
	PictureURL string `json:"pictureUrl,omitempty"`
}

func toArtist(a store.Artist) artistResponse {
	out := artistResponse{Artist: a, PictureURL: a.ImageURL}
	if out.PictureURL == "" && a.CoverAlbumID != "" {
		out.PictureURL = metadata.AlbumCoverURL(a.CoverAlbumID, 500)
	}
	return out
}

type albumResponse struct {
	store.Album
	CoverURL string `json:"coverUrl,omitempty"`
	Path     string `json:"path,omitempty"`
}

func toAlbum(a store.Album, size int) albumResponse {
	return albumResponse{Album: a, CoverURL: metadata.AlbumCoverURL(a.ForeignID, size)}
}

func (s *Server) handleListArtists(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListArtists(r.Context())
	if err != nil {
		s.storeError(w, err, "artists")
		return
	}
	out := make([]artistResponse, 0, len(list))
	for _, a := range list {
		out = append(out, toArtist(a))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) artistFromPath(w http.ResponseWriter, r *http.Request) (*store.Artist, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return nil, false
	}
	a, err := s.store.GetArtist(r.Context(), id)
	if err != nil {
		s.storeError(w, err, "artist")
		return nil, false
	}
	return a, true
}

func (s *Server) handleGetArtist(w http.ResponseWriter, r *http.Request) {
	a, ok := s.artistFromPath(w, r)
	if !ok {
		return
	}
	albums, err := s.store.AlbumsOf(r.Context(), a.ID)
	if err != nil {
		s.storeError(w, err, "albums")
		return
	}
	out := struct {
		artistResponse
		Path   string          `json:"path,omitempty"`
		Albums []albumResponse `json:"albumList"`
	}{artistResponse: toArtist(*a), Albums: make([]albumResponse, 0, len(albums))}
	if lib, err := s.store.GetLibrary(r.Context(), a.LibraryID); err == nil {
		out.Path = lib.Path + "/" + a.Folder
	}
	for _, al := range albums {
		out.Albums = append(out.Albums, toAlbum(al, 250))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteArtist(w http.ResponseWriter, r *http.Request) {
	a, ok := s.artistFromPath(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteArtist(r.Context(), a.ID); err != nil {
		s.storeError(w, err, "artist")
		return
	}
	s.events.Publish(eventT{Type: "artist", Data: map[string]any{"id": a.ID}})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRefreshArtist(w http.ResponseWriter, r *http.Request) {
	a, ok := s.artistFromPath(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if err := s.music.Refresh(ctx, a.ID); err != nil {
		s.musicError(w, err)
		return
	}
	s.handleGetArtist(w, r)
}

// handleMonitorArtist is PUT /artists/{id}/monitor: {"monitorNew": bool},
// and optionally {"albums": "all" | "none"} to set every album at once.
func (s *Server) handleMonitorArtist(w http.ResponseWriter, r *http.Request) {
	a, ok := s.artistFromPath(w, r)
	if !ok {
		return
	}
	var in struct {
		MonitorNew *bool  `json:"monitorNew"`
		Albums     string `json:"albums"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.MonitorNew != nil {
		if err := s.store.SetArtistMonitorNew(r.Context(), a.ID, *in.MonitorNew); err != nil {
			s.storeError(w, err, "artist")
			return
		}
	}
	if in.Albums == "all" || in.Albums == "none" {
		albums, err := s.store.AlbumsOf(r.Context(), a.ID)
		if err != nil {
			s.storeError(w, err, "albums")
			return
		}
		types := s.music.AlbumTypes(r.Context())
		var ids []int64
		for _, al := range albums {
			if in.Albums == "none" || slices.Contains(types, al.Type) {
				ids = append(ids, al.ID)
			}
		}
		if err := s.store.SetAlbumMonitored(r.Context(), ids, in.Albums == "all"); err != nil {
			s.storeError(w, err, "albums")
			return
		}
		s.music.Kick()
	}
	s.events.Publish(eventT{Type: "artist", Data: map[string]any{"id": a.ID}})
	s.handleGetArtist(w, r)
}

// handleSearchArtist queues a search for every monitored album missing.
func (s *Server) handleSearchArtist(w http.ResponseWriter, r *http.Request) {
	a, ok := s.artistFromPath(w, r)
	if !ok {
		return
	}
	albums, err := s.store.AlbumsOf(r.Context(), a.ID)
	if err != nil {
		s.storeError(w, err, "albums")
		return
	}
	n := 0
	today := store.Today()
	for _, al := range albums {
		if al.Monitored && al.Status != "imported" && al.Status != "downloading" && al.ReleaseDate != "" && al.ReleaseDate <= today {
			if s.music.SearchNow(r.Context(), al.ID) == nil {
				n++
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"queued": n})
}

func (s *Server) albumFromPath(w http.ResponseWriter, r *http.Request) (*store.Album, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return nil, false
	}
	a, err := s.store.GetAlbum(r.Context(), id)
	if err != nil {
		s.storeError(w, err, "album")
		return nil, false
	}
	return a, true
}

func (s *Server) handleGetAlbum(w http.ResponseWriter, r *http.Request) {
	a, ok := s.albumFromPath(w, r)
	if !ok {
		return
	}
	if a.Tracks == 0 {
		// Track lists are fetched when an album is first wanted; fetch it
		// now for the page.
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		if s.music.EnsureTracks(ctx, a) == nil {
			if fresh, err := s.store.GetAlbum(r.Context(), a.ID); err == nil {
				a = fresh
			}
		}
		cancel()
	}
	out := toAlbum(*a, 500)
	if ar, err := s.store.GetArtist(r.Context(), a.ArtistID); err == nil {
		if lib, err := s.store.GetLibrary(r.Context(), ar.LibraryID); err == nil {
			if strings.HasPrefix(a.Folder, "/") {
				out.Path = lib.Path + a.Folder
			} else {
				out.Path = lib.Path + "/" + ar.Folder + "/" + a.Folder
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleMonitorAlbum(w http.ResponseWriter, r *http.Request) {
	a, ok := s.albumFromPath(w, r)
	if !ok {
		return
	}
	var in struct {
		Monitored bool `json:"monitored"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if err := s.store.SetAlbumMonitored(r.Context(), []int64{a.ID}, in.Monitored); err != nil {
		s.storeError(w, err, "album")
		return
	}
	if in.Monitored {
		s.music.Kick()
	}
	s.events.Publish(eventT{Type: "album", Data: map[string]any{"id": a.ID, "artistId": a.ArtistID}})
	s.handleGetAlbum(w, r)
}

func (s *Server) handleSearchAlbum(w http.ResponseWriter, r *http.Request) {
	a, ok := s.albumFromPath(w, r)
	if !ok {
		return
	}
	if err := s.music.SearchNow(r.Context(), a.ID); err != nil {
		writeError(w, http.StatusConflict, sentence(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"queued": 1})
}

type albumReleaseResponse struct {
	music.Candidate
	QualityLabel string `json:"qualityLabel"`
	Size         string `json:"size"`
	Age          string `json:"age,omitempty"`
}

// handleAlbumReleases is the manual pick: every release Soulseek and the
// indexers return for an album, best first, with the reason any was
// rejected.
func (s *Server) handleAlbumReleases(w http.ResponseWriter, r *http.Request) {
	a, ok := s.albumFromPath(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	if err := s.music.EnsureTracks(ctx, a); err != nil {
		s.musicError(w, err)
		return
	}
	cands, _, err := s.music.Search(ctx, a, music.SourcesAll)
	if err != nil {
		writeError(w, http.StatusBadGateway, sentence(err.Error()))
		return
	}
	out := make([]albumReleaseResponse, 0, len(cands))
	for _, c := range cands {
		ag := age(c.PublishedAt)
		if c.Protocol == integrations.Soulseek {
			ag = ""
		}
		out = append(out, albumReleaseResponse{Candidate: c, QualityLabel: music.Label(c.Quality), Size: release.HumanSize(c.SizeBytes), Age: ag})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGrabAlbum(w http.ResponseWriter, r *http.Request) {
	a, ok := s.albumFromPath(w, r)
	if !ok {
		return
	}
	var in struct {
		Release integrations.Release `json:"release"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Release.DownloadURL == "" || in.Release.Title == "" {
		writeError(w, http.StatusBadRequest, "release title and downloadUrl are required")
		return
	}
	switch in.Release.Protocol {
	case integrations.Usenet, integrations.Torrent, integrations.Soulseek:
	default:
		writeError(w, http.StatusBadRequest, "release protocol must be usenet, torrent or soulseek")
		return
	}
	c := music.Candidate{Release: in.Release, Quality: music.QualityOf(in.Release.Title)}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.music.Grab(ctx, a, c); err != nil {
		writeError(w, http.StatusBadGateway, sentence(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleImportAnyway is POST /activity/queue/{id}/import: import a music
// download that waits for a decision.
func (s *Server) handleImportAnyway(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	if err := s.music.ImportAnyway(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "download not found")
			return
		}
		writeError(w, http.StatusConflict, sentence(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleTestNavidrome(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL      string `json:"url"`
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	ctx, cancel := withTimeout(r, testTimeout)
	defer cancel()
	n := music.Navidrome{URL: strings.TrimRight(strings.TrimSpace(in.URL), "/"), User: strings.TrimSpace(in.User),
		Password: s.secretOrSaved(r, music.SettingNavidromePassword, in.Password)}
	if err := s.music.TestNavidrome(ctx, n); err != nil {
		writeJSON(w, http.StatusOK, testResult{OK: false, Error: sentence(err.Error())})
		return
	}
	writeJSON(w, http.StatusOK, testResult{OK: true})
}
