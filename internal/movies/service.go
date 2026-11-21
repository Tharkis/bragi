// Package movies runs the movie pipeline: add a movie, search for it once
// it is out, send the best release to a download client, track it, and
// import the finished file into the library.
//
// It follows the TV pipeline's shape (internal/tv) and shares its metadata
// key, Jellyfin connection, indexers and download clients, but polls only
// movie downloads.
package movies

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Tharkis/bragi/internal/events"
	"github.com/Tharkis/bragi/internal/library"
	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/store"
)

// Timing of the scheduler. Variables so tests can shorten them.
var (
	PollInterval    = 30 * time.Second
	RetryAfter      = 6 * time.Hour  // next search after nothing suitable was found
	NoIndexerRetry  = 1 * time.Hour  // next search when no indexer is set up
	RefreshInterval = 24 * time.Hour // metadata refresh per movie without a file
	searchesPerPass = 25
)

// ErrNoLibrary means no movie library is set up.
var ErrNoLibrary = errors.New("add a Movies library under Settings, Libraries first")

// Service holds the pipeline's dependencies.
type Service struct {
	Store  *store.Store
	HC     *http.Client
	Log    *slog.Logger
	Events *events.Hub
	// TMDB returns a metadata client with the configured key. The TV
	// service's TMDB method fits.
	TMDB func(ctx context.Context) *metadata.TMDB
	// RefreshJellyfin asks Jellyfin to scan for new files. Optional.
	RefreshJellyfin func(ctx context.Context) error

	kick     chan struct{}
	importMu sync.Mutex
}

// New returns a Service.
func New(st *store.Store, hc *http.Client, log *slog.Logger, hub *events.Hub, tmdb func(context.Context) *metadata.TMDB) *Service {
	if log == nil {
		log = slog.Default()
	}
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Service{Store: st, HC: hc, Log: log, Events: hub, TMDB: tmdb, kick: make(chan struct{}, 1)}
}

// Kick asks the scheduler to run a pass now instead of waiting.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *Service) publish(typ string, data any) {
	if s.Events != nil {
		s.Events.Publish(events.Event{Type: typ, Data: data})
	}
}

// movieFolder is "Title (Year)", the layout Jellyfin and Plex expect.
func movieFolder(title string, year int) string {
	if year > 0 {
		return library.CleanName(fmt.Sprintf("%s (%d)", title, year))
	}
	return library.CleanName(title)
}

// movieLibrary returns the library to add movies to: the one given, or
// the first movie library.
func (s *Service) movieLibrary(ctx context.Context, id int64) (*store.Library, error) {
	if id != 0 {
		l, err := s.Store.GetLibrary(ctx, id)
		if err != nil {
			return nil, err
		}
		if l.MediaType != "movie" {
			return nil, errors.New("that library is not a Movies library")
		}
		return l, nil
	}
	libs, err := s.Store.ListLibraries(ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range libs {
		if l.MediaType == "movie" {
			return &l, nil
		}
	}
	return nil, ErrNoLibrary
}

func toInput(m *metadata.Movie) store.MovieInput {
	return store.MovieInput{TMDBID: int64(m.TMDBID), IMDBID: m.IMDBID, Title: m.Title, Year: m.Year, Overview: m.Overview,
		PosterPath: m.PosterPath, Runtime: m.Runtime, InCinemas: m.InCinemas, Available: m.Available()}
}

// AddMovie starts tracking a movie and queues a search for it. Adding a
// movie that is already tracked turns monitoring back on if it has no file.
func (s *Service) AddMovie(ctx context.Context, tmdbID, libraryID int64) (*store.Movie, error) {
	md, err := s.TMDB(ctx).Movie(ctx, int(tmdbID))
	if err != nil {
		return nil, err
	}
	in := toInput(md)
	existing, err := s.Store.GetMovieByTMDB(ctx, tmdbID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		lib, err := s.movieLibrary(ctx, libraryID)
		if err != nil {
			return nil, err
		}
		in.LibraryID = lib.ID
		in.Folder = movieFolder(md.Title, md.Year)
	case err != nil:
		return nil, err
	}
	id, err := s.Store.UpsertMovie(ctx, in, true)
	if err != nil {
		return nil, err
	}
	if existing != nil && !existing.Monitored && existing.FilePath == "" {
		if err := s.Store.SetMonitored(ctx, []int64{id}, true); err != nil {
			return nil, err
		}
	}
	s.Log.Info("movie added", "title", md.Title, "tmdb", tmdbID)
	s.publish("movie", map[string]any{"id": id})
	s.Kick()
	return s.Store.GetMovie(ctx, id)
}

// RefreshMovie reloads a movie's metadata, which moves its search date
// when a home release is announced.
func (s *Service) RefreshMovie(ctx context.Context, id int64) error {
	m, err := s.Store.GetMovie(ctx, id)
	if err != nil {
		return err
	}
	md, err := s.TMDB(ctx).Movie(ctx, int(m.TMDBID))
	if err != nil {
		return err
	}
	_, err = s.Store.UpsertMovie(ctx, toInput(md), false)
	return err
}

// SetMonitored turns monitoring of a movie on (queuing a search) or off.
func (s *Service) SetMonitored(ctx context.Context, id int64, on bool) error {
	if err := s.Store.SetMonitored(ctx, []int64{id}, on); err != nil {
		return err
	}
	s.publish("movie", map[string]any{"id": id})
	if on {
		s.Kick()
	}
	return nil
}

// ---- Scheduler ----

// Run drives the pipeline until ctx ends: poll downloads, search for due
// movies, refresh metadata.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(PollInterval)
	defer t.Stop()
	lastRefresh := time.Time{}
	for {
		s.Pass(ctx)
		if time.Since(lastRefresh) > time.Hour {
			s.refreshStale(ctx)
			lastRefresh = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

// Pass runs one round of download polling and searching.
func (s *Service) Pass(ctx context.Context) {
	if err := s.PollDownloads(ctx); err != nil && ctx.Err() == nil {
		s.Log.Error("polling movie downloads failed", "err", err)
	}
	if err := s.SearchDue(ctx); err != nil && ctx.Err() == nil {
		s.Log.Error("searching for movies failed", "err", err)
	}
}

func (s *Service) refreshStale(ctx context.Context) {
	ids, err := s.Store.MoviesNeedingRefresh(ctx, RefreshInterval)
	if err != nil {
		s.Log.Error("listing movies to refresh failed", "err", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if err := s.RefreshMovie(ctx, id); err != nil {
			s.Log.Warn("movie metadata refresh failed", "movie", id, "err", err)
		}
	}
}
