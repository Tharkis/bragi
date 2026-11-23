package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/release"
	"github.com/Tharkis/bragi/internal/store"
)

// ---- Discover ----

type discoverMovie struct {
	TMDBID    int    `json:"tmdbId"`
	Title     string `json:"title"`
	Year      int    `json:"year,omitempty"`
	Overview  string `json:"overview"`
	PosterURL string `json:"posterUrl,omitempty"`
	Type      string `json:"type"`
	// MovieID is set when the movie is already in the library.
	MovieID int64 `json:"movieId,omitempty"`
	// Requested is the status of the newest open request for it, if any.
	Requested   string `json:"requested,omitempty"`
	InCinemas   string `json:"inCinemas,omitempty"`
	HomeRelease string `json:"homeRelease,omitempty"`
	Runtime     int    `json:"runtime,omitempty"`
}

func (s *Server) toDiscoverMovie(ctx context.Context, m metadata.Movie) discoverMovie {
	d := discoverMovie{TMDBID: m.TMDBID, Title: m.Title, Year: m.Year, Overview: m.Overview, PosterURL: posterURL(m.PosterPath, "w342"),
		Type: "movie", InCinemas: m.InCinemas, HomeRelease: m.HomeRelease, Runtime: m.Runtime}
	if mv, err := s.store.GetMovieByTMDB(ctx, int64(m.TMDBID)); err == nil {
		d.MovieID = mv.ID
	}
	if reqs, err := s.store.ListRequests(ctx, store.RequestFilter{TMDBID: int64(m.TMDBID), MediaType: "movie"}); err == nil {
		for _, rq := range reqs {
			if rq.Status == "pending" || rq.Status == "approved" || rq.Status == "available" {
				d.Requested = rq.Status
				break
			}
		}
	}
	return d
}

func (s *Server) handleDiscoverMovies(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, []discoverMovie{})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	found, err := s.tv.TMDB(ctx).SearchMovies(ctx, q, 0)
	if err != nil {
		s.metadataError(w, err)
		return
	}
	out := make([]discoverMovie, 0, len(found))
	for _, m := range found {
		out = append(out, s.toDiscoverMovie(ctx, m))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDiscoverMovie returns one movie with its release dates.
func (s *Server) handleDiscoverMovie(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	m, err := s.tv.TMDB(ctx).Movie(ctx, int(id))
	if err != nil {
		s.metadataError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toDiscoverMovie(ctx, *m))
}

// ---- Requests ----

func (s *Server) createMovieRequest(w http.ResponseWriter, r *http.Request, in requestBody) {
	u := userFrom(r.Context())
	if in.TMDBID <= 0 {
		writeError(w, http.StatusBadRequest, "tmdbId is required")
		return
	}
	// One open request per user and movie.
	if mine, err := s.store.ListRequests(r.Context(), store.RequestFilter{UserID: u.ID, TMDBID: in.TMDBID, MediaType: "movie"}); err == nil {
		for _, rq := range mine {
			if rq.Status == "pending" {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "you already asked for this movie", "request": s.toRequestResponse(r.Context(), rq, false)})
				return
			}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	m, err := s.tv.TMDB(ctx).Movie(ctx, int(in.TMDBID))
	if err != nil {
		s.metadataError(w, err)
		return
	}
	rq, err := s.store.CreateRequest(r.Context(), store.Request{UserID: u.ID, MediaType: "movie", TMDBID: in.TMDBID, Title: m.Title,
		Year: m.Year, PosterPath: m.PosterPath, Scope: "{}", Status: "pending"})
	if err != nil {
		s.storeError(w, err, "request")
		return
	}
	s.log.Info("request created", "user", u.Username, "title", m.Title, "type", "movie")
	if u.Role == "admin" || u.AutoApprove {
		rq, _ = s.approve(r.Context(), rq, "")
	} else {
		s.log.Info("request waits for an admin to approve it", "request", rq.ID, "user", u.Username)
	}
	s.events.Publish(eventRequest(rq))
	writeJSON(w, http.StatusCreated, s.toRequestResponse(r.Context(), *rq, false))
}

// approveMovie adds the movie and marks the request approved. If adding
// fails the request stays pending with the reason.
func (s *Server) approveMovie(ctx context.Context, rq *store.Request, reason string) (*store.Request, error) {
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	m, err := s.movies.AddMovie(actx, rq.TMDBID, 0)
	if err != nil {
		s.log.Warn("request approved but could not be added; it stays pending", "request", rq.ID, "type", rq.MediaType, "title", rq.Title, "err", err)
		s.store.SetRequestStatus(ctx, rq.ID, "pending", "Could not add the movie: "+sentence(err.Error()), 0)
		out, _ := s.store.GetRequest(ctx, rq.ID)
		if out == nil {
			out = rq
		}
		return out, err
	}
	s.log.Info("request approved", "request", rq.ID, "type", rq.MediaType, "title", rq.Title)
	s.store.SetRequestStatus(ctx, rq.ID, "approved", reason, m.ID)
	s.store.MarkMovieRequestsAvailable(ctx, m.ID)
	out, err := s.store.GetRequest(ctx, rq.ID)
	if err != nil {
		return rq, err
	}
	return out, nil
}

// movieRequestResponse fills a movie request's progress: one item that is
// either imported or not.
func (s *Server) movieRequestResponse(ctx context.Context, out requestResponse) requestResponse {
	if out.MediaItemID == 0 {
		return out
	}
	m, err := s.store.GetMovie(ctx, out.MediaItemID)
	if err != nil {
		return out
	}
	p := &requestProgress{MovieID: m.ID, Episodes: 1, Activities: m.Downloading}
	if m.FilePath != "" {
		p.Imported = 1
	} else if m.IsReleased() {
		p.Missing = 1
	}
	out.Progress = p
	return out
}

// ---- Library ----

type movieResponse struct {
	store.Movie
	PosterURL string `json:"posterUrl,omitempty"`
	Path      string `json:"path,omitempty"`
	Size      string `json:"size,omitempty"`
}

func (s *Server) toMovieResponse(ctx context.Context, m store.Movie, withPath bool) movieResponse {
	out := movieResponse{Movie: m, PosterURL: posterURL(m.PosterPath, "w342")}
	if m.SizeBytes > 0 {
		out.Size = release.HumanSize(m.SizeBytes)
	}
	if withPath {
		if lib, err := s.store.GetLibrary(ctx, m.LibraryID); err == nil {
			out.Path = lib.Path + "/" + m.Folder
		}
	}
	return out
}

func (s *Server) handleListMovies(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListMovies(r.Context())
	if err != nil {
		s.storeError(w, err, "movies")
		return
	}
	out := make([]movieResponse, 0, len(list))
	for _, m := range list {
		out = append(out, s.toMovieResponse(r.Context(), m, false))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) movieFromPath(w http.ResponseWriter, r *http.Request) (*store.Movie, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return nil, false
	}
	m, err := s.store.GetMovie(r.Context(), id)
	if err != nil {
		s.storeError(w, err, "movie")
		return nil, false
	}
	return m, true
}

func (s *Server) handleGetMovie(w http.ResponseWriter, r *http.Request) {
	m, ok := s.movieFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.toMovieResponse(r.Context(), *m, true))
}

func (s *Server) handleDeleteMovie(w http.ResponseWriter, r *http.Request) {
	m, ok := s.movieFromPath(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteMovie(r.Context(), m.ID); err != nil {
		s.storeError(w, err, "movie")
		return
	}
	s.events.Publish(eventT{Type: "movie", Data: map[string]any{"id": m.ID}})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSearchMovie(w http.ResponseWriter, r *http.Request) {
	m, ok := s.movieFromPath(w, r)
	if !ok {
		return
	}
	queued, why, err := s.movies.SearchNow(r.Context(), m.ID)
	if err != nil {
		s.storeError(w, err, "movie")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queued": queued, "message": why})
}

func (s *Server) handleRefreshMovie(w http.ResponseWriter, r *http.Request) {
	m, ok := s.movieFromPath(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	if err := s.movies.RefreshMovie(ctx, m.ID); err != nil {
		s.metadataError(w, err)
		return
	}
	s.handleGetMovie(w, r)
}

func (s *Server) handleMonitorMovie(w http.ResponseWriter, r *http.Request) {
	m, ok := s.movieFromPath(w, r)
	if !ok {
		return
	}
	var in struct {
		Monitored bool `json:"monitored"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if err := s.movies.SetMonitored(r.Context(), m.ID, in.Monitored); err != nil {
		s.storeError(w, err, "movie")
		return
	}
	s.handleGetMovie(w, r)
}

// handleMovieReleases is the manual pick: every release the indexers
// return for the movie, best first, with the reason any was rejected.
func (s *Server) handleMovieReleases(w http.ResponseWriter, r *http.Request) {
	m, ok := s.movieFromPath(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	cands, _, err := s.movies.Search(ctx, m)
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

func (s *Server) handleGrabMovie(w http.ResponseWriter, r *http.Request) {
	m, ok := s.movieFromPath(w, r)
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
	if in.Release.Protocol != integrations.Usenet && in.Release.Protocol != integrations.Torrent {
		writeError(w, http.StatusBadRequest, "release protocol must be usenet or torrent")
		return
	}
	c := release.Candidate{Release: in.Release, Info: release.Parse(in.Release.Title)}
	c.Quality = c.Info.Quality()
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.movies.Grab(ctx, m, c); err != nil {
		writeError(w, http.StatusBadGateway, sentence(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTrendingMovies is GET /discover/trending?type=movie: this week's
// trending movies, marked with what is in the library or requested.
func (s *Server) handleTrendingMovies(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	found, _, err := s.tv.TMDB(ctx).TrendingMovies(ctx, 1)
	if err != nil {
		s.metadataError(w, err)
		return
	}
	out := make([]discoverMovie, 0, len(found))
	for _, m := range found {
		out = append(out, s.toDiscoverMovie(ctx, m))
	}
	writeJSON(w, http.StatusOK, out)
}
