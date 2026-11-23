package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/movies"
	"github.com/Tharkis/bragi/internal/release"
	"github.com/Tharkis/bragi/internal/store"
	"github.com/Tharkis/bragi/internal/tv"
)

func posterURL(path, size string) string {
	if path == "" {
		return ""
	}
	return metadata.ImageBase + size + path
}

// metadataError turns TMDB failures into a status and a message for the UI.
func (s *Server) metadataError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, metadata.ErrNoKey):
		writeError(w, http.StatusFailedDependency, sentence(err.Error()))
	case errors.Is(err, metadata.ErrBadKey):
		writeError(w, http.StatusFailedDependency, "TMDB rejected the API key; check it under Settings, Connections")
	case errors.Is(err, tv.ErrNoLibrary), errors.Is(err, movies.ErrNoLibrary):
		writeError(w, http.StatusFailedDependency, sentence(err.Error()))
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	default:
		s.log.Warn("metadata error", "err", err)
		writeError(w, http.StatusBadGateway, sentence(err.Error()))
	}
}

// ---- Discover ----

type discoverResult struct {
	TMDBID    int    `json:"tmdbId"`
	Title     string `json:"title"`
	Year      int    `json:"year,omitempty"`
	Overview  string `json:"overview"`
	PosterURL string `json:"posterUrl,omitempty"`
	// SeriesID is set when the show is already in the library.
	SeriesID int64 `json:"seriesId,omitempty"`
	// Requested is the status of the newest open request for it, if any.
	Requested string `json:"requested,omitempty"`
	Seasons   []int  `json:"seasons,omitempty"`
}

func (s *Server) handleDiscoverSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	switch t := r.URL.Query().Get("type"); t {
	case "", "tv":
	case "movie":
		s.handleDiscoverMovies(w, r)
		return
	case "book", "audiobook":
		s.handleDiscoverBooks(w, r, t)
		return
	case "music":
		s.handleDiscoverMusic(w, r)
		return
	default:
		writeError(w, http.StatusBadRequest, "type must be tv, movie, music, book or audiobook")
		return
	}
	if q == "" {
		writeJSON(w, http.StatusOK, []discoverResult{})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	shows, err := s.tv.TMDB(ctx).SearchTV(ctx, q, 0)
	if err != nil {
		s.metadataError(w, err)
		return
	}
	out := make([]discoverResult, 0, len(shows))
	for _, sh := range shows {
		d := discoverResult{TMDBID: sh.TMDBID, Title: sh.Title, Year: sh.Year, Overview: sh.Overview, PosterURL: posterURL(sh.PosterPath, "w342")}
		if sr, err := s.store.GetSeriesByTMDB(ctx, int64(sh.TMDBID)); err == nil {
			d.SeriesID = sr.ID
		}
		if reqs, err := s.store.ListRequests(ctx, store.RequestFilter{TMDBID: int64(sh.TMDBID), MediaType: "tv"}); err == nil {
			for _, rq := range reqs {
				if rq.Status == "pending" || rq.Status == "approved" || rq.Status == "available" {
					d.Requested = rq.Status
					break
				}
			}
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDiscoverShow returns one show's seasons, for the season picker.
func (s *Server) handleDiscoverShow(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	sh, err := s.tv.TMDB(ctx).Series(ctx, int(id), false)
	if err != nil {
		s.metadataError(w, err)
		return
	}
	d := discoverResult{TMDBID: sh.TMDBID, Title: sh.Title, Year: sh.Year, Overview: sh.Overview, PosterURL: posterURL(sh.PosterPath, "w342")}
	for _, sn := range sh.Seasons {
		if sn.Number > 0 {
			d.Seasons = append(d.Seasons, sn.Number)
		}
	}
	if sr, err := s.store.GetSeriesByTMDB(ctx, id); err == nil {
		d.SeriesID = sr.ID
	}
	writeJSON(w, http.StatusOK, d)
}

// ---- Requests ----

type requestBody struct {
	Type   string          `json:"type"`
	TMDBID int64           `json:"tmdbId"`
	Scope  json.RawMessage `json:"seasons"`
	// BookID is the Open Library work, for type book or audiobook.
	BookID string `json:"bookId"`
	// Music: an artist to follow (with what to monitor) or one album, by
	// MusicBrainz id.
	ArtistID string   `json:"artistId"`
	Monitor  string   `json:"monitor"`
	Albums   []string `json:"albums"`
	AlbumID  string   `json:"albumId"`
}

type requestProgress struct {
	SeriesID   int64 `json:"seriesId,omitempty"`
	MovieID    int64 `json:"movieId,omitempty"`
	ArtistID   int64 `json:"artistId,omitempty"`
	AlbumID    int64 `json:"albumId,omitempty"`
	Episodes   int   `json:"episodes"`
	Imported   int   `json:"imported"`
	Missing    int   `json:"missing"`
	Activities int   `json:"downloading"`
}

type requestResponse struct {
	store.Request
	Seasons   json.RawMessage  `json:"seasons"`
	PosterURL string           `json:"posterUrl,omitempty"`
	Progress  *requestProgress `json:"progress,omitempty"`
	// Music says what a music request asks for.
	Music *musicScope `json:"music,omitempty"`
	// Episodes is set on GET /requests/{id}: every monitored episode's status.
	Episodes []store.Episode `json:"episodes,omitempty"`
}

func (s *Server) toRequestResponse(ctx context.Context, rq store.Request, withEpisodes bool) requestResponse {
	if store.IsBookType(rq.MediaType) {
		return s.bookRequestResponse(ctx, rq)
	}
	if rq.MediaType == "music" {
		return s.musicRequestResponse(ctx, rq)
	}
	out := requestResponse{Request: rq, PosterURL: posterURL(rq.PosterPath, "w185")}
	if rq.MediaType == "movie" {
		return s.movieRequestResponse(ctx, out)
	}
	var scope tv.Scope
	if json.Unmarshal([]byte(rq.Scope), &scope) == nil && len(scope.Seasons) > 0 {
		out.Seasons = scope.Seasons
	} else {
		out.Seasons = json.RawMessage(`"all"`)
	}
	if rq.MediaItemID != 0 {
		if sr, err := s.store.GetSeries(ctx, rq.MediaItemID); err == nil {
			out.Progress = &requestProgress{SeriesID: sr.ID, Episodes: sr.Episodes, Imported: sr.Imported, Missing: sr.Missing, Activities: sr.Activities}
			if withEpisodes {
				eps, _ := s.store.EpisodesBySeries(ctx, sr.ID)
				for _, e := range eps {
					if e.Monitored || e.Status == "imported" {
						out.Episodes = append(out.Episodes, e)
					}
				}
			}
		}
	}
	return out
}

func (s *Server) handleCreateRequest(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	var in requestBody
	if !readJSON(w, r, &in) {
		return
	}
	if in.Type == "" {
		in.Type = "tv"
	}
	if store.IsBookType(in.Type) {
		s.createBookRequest(w, r, in)
		return
	}
	if in.Type == "movie" {
		s.createMovieRequest(w, r, in)
		return
	}
	if in.Type == "music" {
		s.createMusicRequest(w, r, in)
		return
	}
	if in.Type != "tv" {
		writeError(w, http.StatusBadRequest, "type must be tv, movie, music, book or audiobook")
		return
	}
	if in.TMDBID <= 0 {
		writeError(w, http.StatusBadRequest, "tmdbId is required")
		return
	}
	if _, _, err := tv.ParseScope(in.Scope); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// One open request per user and show.
	if mine, err := s.store.ListRequests(r.Context(), store.RequestFilter{UserID: u.ID, TMDBID: in.TMDBID, MediaType: "tv"}); err == nil {
		for _, rq := range mine {
			if rq.Status == "pending" {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "you already asked for this show", "request": s.toRequestResponse(r.Context(), rq, false)})
				return
			}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	sh, err := s.tv.TMDB(ctx).Series(ctx, int(in.TMDBID), false)
	if err != nil {
		s.metadataError(w, err)
		return
	}
	scope, _ := json.Marshal(tv.Scope{Seasons: in.Scope})
	if len(in.Scope) == 0 {
		scope = []byte(`{"seasons":"all"}`)
	}
	rq, err := s.store.CreateRequest(r.Context(), store.Request{UserID: u.ID, MediaType: "tv", TMDBID: in.TMDBID, Title: sh.Title,
		Year: sh.Year, PosterPath: sh.PosterPath, Scope: string(scope), Status: "pending"})
	if err != nil {
		s.storeError(w, err, "request")
		return
	}
	s.log.Info("request created", "user", u.Username, "title", sh.Title)
	if u.Role == "admin" || u.AutoApprove {
		rq, _ = s.approve(r.Context(), rq, "")
	} else {
		s.log.Info("request waits for an admin to approve it", "request", rq.ID, "user", u.Username)
	}
	s.events.Publish(eventRequest(rq))
	writeJSON(w, http.StatusCreated, s.toRequestResponse(r.Context(), *rq, false))
}

// approve adds the show and marks the request approved. If adding fails the
// request stays pending with the reason, so an admin can fix the cause and
// approve again.
func (s *Server) approve(ctx context.Context, rq *store.Request, reason string) (*store.Request, error) {
	if store.IsBookType(rq.MediaType) {
		return s.approveBook(ctx, rq, reason)
	}
	if rq.MediaType == "movie" {
		return s.approveMovie(ctx, rq, reason)
	}
	if rq.MediaType == "music" {
		return s.approveMusic(ctx, rq, reason)
	}
	var scope tv.Scope
	json.Unmarshal([]byte(rq.Scope), &scope)
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	sr, err := s.tv.AddSeries(actx, rq.TMDBID, 0, scope.Seasons)
	if err != nil {
		s.log.Warn("request approved but could not be added; it stays pending", "request", rq.ID, "type", rq.MediaType, "title", rq.Title, "err", err)
		s.store.SetRequestStatus(ctx, rq.ID, "pending", "Could not add the show: "+sentence(err.Error()), 0)
		out, _ := s.store.GetRequest(ctx, rq.ID)
		if out == nil {
			out = rq
		}
		return out, err
	}
	s.log.Info("request approved", "request", rq.ID, "type", rq.MediaType, "title", rq.Title)
	s.store.SetRequestStatus(ctx, rq.ID, "approved", reason, sr.ID)
	s.store.MarkRequestsAvailable(ctx, sr.ID)
	out, err := s.store.GetRequest(ctx, rq.ID)
	if err != nil {
		return rq, err
	}
	return out, nil
}

func eventRequest(rq *store.Request) eventT {
	return eventT{Type: "request", Data: map[string]any{"id": rq.ID, "status": rq.Status}}
}

func (s *Server) handleListRequests(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	f := store.RequestFilter{Status: r.URL.Query().Get("status")}
	if u.Role != "admin" || r.URL.Query().Get("mine") == "true" {
		f.UserID = u.ID
	}
	reqs, err := s.store.ListRequests(r.Context(), f)
	if err != nil {
		s.storeError(w, err, "requests")
		return
	}
	out := make([]requestResponse, 0, len(reqs))
	for _, rq := range reqs {
		out = append(out, s.toRequestResponse(r.Context(), rq, false))
	}
	writeJSON(w, http.StatusOK, out)
}

// ownRequest loads a request the signed-in user may see.
func (s *Server) ownRequest(w http.ResponseWriter, r *http.Request) (*store.Request, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return nil, false
	}
	rq, err := s.store.GetRequest(r.Context(), id)
	if err != nil {
		s.storeError(w, err, "request")
		return nil, false
	}
	u := userFrom(r.Context())
	if u.Role != "admin" && rq.UserID != u.ID {
		writeError(w, http.StatusNotFound, "request not found")
		return nil, false
	}
	return rq, true
}

func (s *Server) handleGetRequest(w http.ResponseWriter, r *http.Request) {
	rq, ok := s.ownRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.toRequestResponse(r.Context(), *rq, true))
}

type reasonBody struct {
	Reason string `json:"reason"`
}

func (s *Server) handleApproveRequest(w http.ResponseWriter, r *http.Request) {
	rq, ok := s.ownRequest(w, r)
	if !ok {
		return
	}
	var in reasonBody
	if r.ContentLength > 0 && !readJSON(w, r, &in) {
		return
	}
	if rq.Status != "pending" && rq.Status != "denied" {
		writeError(w, http.StatusConflict, "this request is already "+rq.Status)
		return
	}
	out, err := s.approve(r.Context(), rq, in.Reason)
	s.events.Publish(eventRequest(out))
	if err != nil {
		if store.IsBookType(rq.MediaType) {
			s.bookError(w, err)
			return
		}
		if rq.MediaType == "music" {
			s.musicError(w, err)
			return
		}
		s.metadataError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toRequestResponse(r.Context(), *out, false))
}

func (s *Server) handleDenyRequest(w http.ResponseWriter, r *http.Request) {
	rq, ok := s.ownRequest(w, r)
	if !ok {
		return
	}
	var in reasonBody
	if r.ContentLength > 0 && !readJSON(w, r, &in) {
		return
	}
	if rq.Status != "pending" {
		writeError(w, http.StatusConflict, "only pending requests can be denied")
		return
	}
	s.log.Info("request denied", "request", rq.ID, "title", rq.Title, "reason", in.Reason)
	if err := s.store.SetRequestStatus(r.Context(), rq.ID, "denied", in.Reason, 0); err != nil {
		s.storeError(w, err, "request")
		return
	}
	rq, _ = s.store.GetRequest(r.Context(), rq.ID)
	s.events.Publish(eventRequest(rq))
	writeJSON(w, http.StatusOK, s.toRequestResponse(r.Context(), *rq, false))
}

func (s *Server) handleDeleteRequest(w http.ResponseWriter, r *http.Request) {
	rq, ok := s.ownRequest(w, r)
	if !ok {
		return
	}
	if userFrom(r.Context()).Role != "admin" && rq.Status != "pending" {
		writeError(w, http.StatusConflict, "only pending requests can be cancelled")
		return
	}
	if err := s.store.DeleteRequest(r.Context(), rq.ID); err != nil {
		s.storeError(w, err, "request")
		return
	}
	s.events.Publish(eventT{Type: "request", Data: map[string]any{"id": rq.ID, "status": "deleted"}})
	w.WriteHeader(http.StatusNoContent)
}

// ---- Library ----

type seriesResponse struct {
	store.Series
	PosterURL string         `json:"posterUrl,omitempty"`
	Path      string         `json:"path,omitempty"`
	Seasons   []store.Season `json:"seasons,omitempty"`
}

func (s *Server) handleListSeries(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListSeries(r.Context())
	if err != nil {
		s.storeError(w, err, "shows")
		return
	}
	out := make([]seriesResponse, 0, len(list))
	for _, sr := range list {
		out = append(out, seriesResponse{Series: sr, PosterURL: posterURL(sr.PosterPath, "w342")})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) seriesFromPath(w http.ResponseWriter, r *http.Request) (*store.Series, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return nil, false
	}
	sr, err := s.store.GetSeries(r.Context(), id)
	if err != nil {
		s.storeError(w, err, "show")
		return nil, false
	}
	return sr, true
}

func (s *Server) handleGetSeries(w http.ResponseWriter, r *http.Request) {
	sr, ok := s.seriesFromPath(w, r)
	if !ok {
		return
	}
	seasons, err := s.store.Seasons(r.Context(), sr.ID)
	if err != nil {
		s.storeError(w, err, "show")
		return
	}
	out := seriesResponse{Series: *sr, PosterURL: posterURL(sr.PosterPath, "w342"), Seasons: seasons}
	if lib, err := s.store.GetLibrary(r.Context(), sr.LibraryID); err == nil {
		out.Path = lib.Path + "/" + sr.Folder
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteSeries(w http.ResponseWriter, r *http.Request) {
	sr, ok := s.seriesFromPath(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteSeries(r.Context(), sr.ID); err != nil {
		s.storeError(w, err, "show")
		return
	}
	s.events.Publish(eventT{Type: "series", Data: map[string]any{"id": sr.ID}})
	w.WriteHeader(http.StatusNoContent)
}

type episodeRef struct {
	Season  int `json:"season"`
	Episode int `json:"episode"`
}

func (s *Server) handleSearchSeries(w http.ResponseWriter, r *http.Request) {
	sr, ok := s.seriesFromPath(w, r)
	if !ok {
		return
	}
	var in episodeRef
	if r.ContentLength > 0 && !readJSON(w, r, &in) {
		return
	}
	n, err := s.tv.SearchNow(r.Context(), sr.ID, in.Season, in.Episode)
	if err != nil {
		s.storeError(w, err, "show")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"queued": n})
}

func (s *Server) handleRefreshSeries(w http.ResponseWriter, r *http.Request) {
	sr, ok := s.seriesFromPath(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := s.tv.RefreshSeries(ctx, sr.ID); err != nil {
		s.metadataError(w, err)
		return
	}
	s.handleGetSeries(w, r)
}

type monitorBody struct {
	EpisodeIDs []int64 `json:"episodeIds"`
	Monitored  bool    `json:"monitored"`
}

func (s *Server) handleMonitor(w http.ResponseWriter, r *http.Request) {
	sr, ok := s.seriesFromPath(w, r)
	if !ok {
		return
	}
	var in monitorBody
	if !readJSON(w, r, &in) {
		return
	}
	eps, err := s.store.EpisodesBySeries(r.Context(), sr.ID)
	if err != nil {
		s.storeError(w, err, "show")
		return
	}
	mine := map[int64]bool{}
	for _, e := range eps {
		mine[e.ID] = true
	}
	for _, id := range in.EpisodeIDs {
		if !mine[id] {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("episode %d is not part of this show", id))
			return
		}
	}
	if err := s.store.SetMonitored(r.Context(), in.EpisodeIDs, in.Monitored); err != nil {
		s.storeError(w, err, "episode")
		return
	}
	s.tv.Kick()
	s.handleGetSeries(w, r)
}

type releaseResponse struct {
	release.Candidate
	Size string `json:"size"`
	Age  string `json:"age,omitempty"`
}

func age(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	}
	return fmt.Sprintf("%d d", int(d.Hours()/24))
}

func queryInt(r *http.Request, key string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(key))
	return n
}

// handleReleases is the manual pick: every release the indexers return for
// an episode (or a season with episode=0), best first, with the reason any
// was rejected.
func (s *Server) handleReleases(w http.ResponseWriter, r *http.Request) {
	sr, ok := s.seriesFromPath(w, r)
	if !ok {
		return
	}
	t, err := s.tv.TargetFor(r.Context(), sr.ID, queryInt(r, "season"), queryInt(r, "episode"))
	if err != nil {
		s.storeError(w, err, "episode")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	cands, _, err := s.tv.Search(ctx, t)
	if err != nil {
		writeError(w, http.StatusBadGateway, sentence(err.Error()))
		return
	}
	out := make([]releaseResponse, 0, len(cands))
	for _, c := range cands {
		out = append(out, releaseResponse{Candidate: c, Size: release.HumanSize(c.SizeBytes), Age: age(c.PublishedAt)})
	}
	writeJSON(w, http.StatusOK, out)
}

type grabBody struct {
	episodeRef
	Release integrations.Release `json:"release"`
}

func (s *Server) handleGrab(w http.ResponseWriter, r *http.Request) {
	sr, ok := s.seriesFromPath(w, r)
	if !ok {
		return
	}
	var in grabBody
	if !readJSON(w, r, &in) {
		return
	}
	if in.Release.DownloadURL == "" || in.Release.Title == "" {
		writeError(w, http.StatusBadRequest, "release title and downloadUrl are required")
		return
	}
	if in.Release.Protocol != integrations.Usenet && in.Release.Protocol != integrations.Torrent {
		writeError(w, http.StatusBadRequest, "release protocol must be usenet or torrent")
		return
	}
	t, err := s.tv.TargetFor(r.Context(), sr.ID, in.Season, in.Episode)
	if err != nil {
		s.storeError(w, err, "episode")
		return
	}
	c := release.Candidate{Release: in.Release, Info: release.Parse(in.Release.Title)}
	c.Quality = c.Info.Quality()
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.tv.Grab(ctx, t, c); err != nil {
		writeError(w, http.StatusBadGateway, sentence(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- Activity ----

type downloadResponse struct {
	store.Download
	Size      string `json:"size"`
	PosterURL string `json:"posterUrl,omitempty"`
}

func toDownloads(ds []store.Download) []downloadResponse {
	out := make([]downloadResponse, 0, len(ds))
	for _, d := range ds {
		p := posterURL(d.PosterPath, "w92")
		if store.IsBookType(d.MediaType) {
			p = coverURL(d.PosterPath, "S")
		}
		if d.MediaType == "music" {
			p = metadata.AlbumCoverURL(d.AlbumMBID, 250)
		}
		out = append(out, downloadResponse{Download: d, Size: release.HumanSize(d.SizeBytes), PosterURL: p})
	}
	return out
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	ds, err := s.store.ActiveDownloads(r.Context())
	if err != nil {
		s.storeError(w, err, "downloads")
		return
	}
	writeJSON(w, http.StatusOK, toDownloads(ds))
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	ds, err := s.store.DownloadHistory(r.Context(), 200)
	if err != nil {
		s.storeError(w, err, "downloads")
		return
	}
	writeJSON(w, http.StatusOK, toDownloads(ds))
}

// handleRemoveDownload cancels a download, removes it from the client and
// blocklists the release so a new search picks something else.
func (s *Server) handleRemoveDownload(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	cancel := s.tv.Cancel
	if d, err := s.store.GetDownload(r.Context(), id); err == nil {
		switch {
		case store.IsBookType(d.MediaType):
			cancel = func(ctx context.Context, _ int64) error { return s.books.Cancel(ctx, d) }
		case d.MediaType == "movie":
			cancel = s.movies.Cancel
		case d.MediaType == "music":
			cancel = func(ctx context.Context, _ int64) error { return s.music.Cancel(ctx, d) }
		}
	}
	if err := cancel(r.Context(), id); err != nil {
		s.storeError(w, err, "download")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
