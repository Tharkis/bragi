package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/store"
)

type discoverPage struct {
	Page       int              `json:"page"`
	TotalPages int              `json:"totalPages"`
	Results    []discoverResult `json:"results"`
}

// handleDiscoverTrending lists this week's trending shows, marked with what
// is already in the library or requested. The Jellyfin plugin fills its
// Request library from it.
func (s *Server) handleDiscoverTrending(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("type") == "music" {
		s.handleTrendingMusic(w, r)
		return
	}
	switch t := r.URL.Query().Get("type"); t {
	case "", "tv":
	case "movie":
		s.handleTrendingMovies(w, r)
		return
	case "book", "audiobook":
		s.handleTrendingBooks(w, r, t)
		return
	default:
		writeError(w, http.StatusBadRequest, "type must be tv, movie, music, book or audiobook")
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	shows, pages, err := s.tv.TMDB(ctx).TrendingTV(ctx, page)
	if err != nil {
		s.metadataError(w, err)
		return
	}
	out := discoverPage{Page: page, TotalPages: pages, Results: make([]discoverResult, 0, len(shows))}
	for _, sh := range shows {
		out.Results = append(out.Results, s.markShow(ctx, sh))
	}
	writeJSON(w, http.StatusOK, out)
}

// markShow turns a TMDB show into a discover result, marked with its
// library entry and open request.
func (s *Server) markShow(ctx context.Context, sh metadata.Show) discoverResult {
	d := discoverResult{TMDBID: sh.TMDBID, Title: sh.Title, Year: sh.Year, Overview: sh.Overview, PosterURL: posterURL(sh.PosterPath, "w342")}
	if sr, err := s.store.GetSeriesByTMDB(ctx, int64(sh.TMDBID)); err == nil {
		d.SeriesID = sr.ID
	}
	if reqs, err := s.store.ListRequests(ctx, store.RequestFilter{TMDBID: int64(sh.TMDBID)}); err == nil {
		for _, rq := range reqs {
			if rq.MediaType == "tv" && (rq.Status == "pending" || rq.Status == "approved" || rq.Status == "available") {
				d.Requested = rq.Status
				break
			}
		}
	}
	return d
}

// handleDiscoverTypes is GET /discover/types: the media types Bragi has a
// library for, in a fixed order, so request clients (the Jellyfin plugin)
// only offer what Bragi can take. Any signed-in user may ask.
func (s *Server) handleDiscoverTypes(w http.ResponseWriter, r *http.Request) {
	libs, err := s.store.ListLibraries(r.Context())
	if err != nil {
		s.storeError(w, err, "libraries")
		return
	}
	has := map[string]bool{}
	for _, l := range libs {
		has[l.MediaType] = true
	}
	out := []string{}
	for _, t := range []string{"tv", "movie", "music", "book", "audiobook"} {
		if has[t] {
			out = append(out, t)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
