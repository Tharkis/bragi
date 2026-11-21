// Package tv runs the TV pipeline: add a show, search for wanted episodes,
// send the best release to a download client, track it, and import the
// finished files into the library.
package tv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Tharkis/bragi/internal/events"
	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/store"
)

// Setting keys this package reads.
const (
	SettingTMDBKey        = "tmdb_api_key"
	SettingJellyfinURL    = "jellyfin_url"
	SettingJellyfinAPIKey = "jellyfin_api_key"
)

// Timing of the scheduler. Variables so tests can shorten them.
var (
	PollInterval    = 30 * time.Second
	RetryAfter      = 6 * time.Hour  // next search after nothing suitable was found
	NoIndexerRetry  = 1 * time.Hour  // next search when no indexer is set up
	RefreshInterval = 12 * time.Hour // metadata refresh per show
	searchesPerPass = 25
)

// ErrNoLibrary means no TV library is set up.
var ErrNoLibrary = errors.New("add a TV library under Settings, Libraries first")

// Service holds the pipeline's dependencies.
type Service struct {
	Store  *store.Store
	HC     *http.Client
	Log    *slog.Logger
	Events *events.Hub
	// TMDBKey, when set (from BRAGI_TMDB_API_KEY), is used if the settings
	// page has none.
	TMDBKey string
	// TMDBBaseURL overrides the TMDB API address, for tests.
	TMDBBaseURL string

	kick     chan struct{}
	scanMu   sync.Mutex
	scan     ScanStatus
	importMu sync.Mutex
}

// New returns a Service.
func New(st *store.Store, hc *http.Client, log *slog.Logger, hub *events.Hub) *Service {
	if log == nil {
		log = slog.Default()
	}
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Service{Store: st, HC: hc, Log: log, Events: hub, kick: make(chan struct{}, 1)}
}

// TMDB returns a metadata client with the configured key.
func (s *Service) TMDB(ctx context.Context) *metadata.TMDB {
	key, _ := s.Store.GetSetting(ctx, SettingTMDBKey)
	if key == "" {
		key = s.TMDBKey
	}
	return &metadata.TMDB{BaseURL: s.TMDBBaseURL, Key: key, HC: s.HC}
}

// Kick asks the scheduler to run a pass now instead of waiting.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *Service) publish(typ string, data any) {
	s.Events.Publish(events.Event{Type: typ, Data: data})
}

// ---- Adding shows ----

// Scope says which episodes a request wants.
type Scope struct {
	// Seasons is "all", "latest", "future", or a list of season numbers.
	Seasons json.RawMessage `json:"seasons"`
}

// ScopeAll, ScopeLatest and ScopeFuture are the three simple choices.
const (
	ScopeAll    = "all"
	ScopeLatest = "latest"
	ScopeFuture = "future"
)

// ParseScope validates a scope and returns its mode and season list.
func ParseScope(raw json.RawMessage) (mode string, seasons []int, err error) {
	if len(raw) == 0 || string(raw) == "null" {
		return ScopeAll, nil, nil
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		switch str {
		case ScopeAll, ScopeLatest, ScopeFuture:
			return str, nil, nil
		}
		return "", nil, fmt.Errorf(`seasons must be "all", "latest", "future" or a list of season numbers`)
	}
	// Also accept ["all"], the shape the design doc sketches.
	var list []any
	if err := json.Unmarshal(raw, &list); err != nil {
		return "", nil, fmt.Errorf(`seasons must be "all", "latest", "future" or a list of season numbers`)
	}
	for _, v := range list {
		switch x := v.(type) {
		case string:
			if x == ScopeAll || x == ScopeLatest || x == ScopeFuture {
				return x, nil, nil
			}
		case float64:
			if x >= 0 && x == float64(int(x)) {
				seasons = append(seasons, int(x))
				continue
			}
		}
		return "", nil, fmt.Errorf("invalid season %v", v)
	}
	if len(seasons) == 0 {
		return "", nil, errors.New("pick at least one season")
	}
	return "seasons", seasons, nil
}

func monitorFunc(mode string, seasons []int, show *metadata.Show) store.MonitorFunc {
	today := store.Today()
	latest := 0
	for _, sn := range show.Seasons {
		if sn.Number > latest && len(sn.Episodes) > 0 && sn.Episodes[0].AirDate != "" && sn.Episodes[0].AirDate <= today {
			latest = sn.Number
		}
	}
	future := func(air string) bool { return air == "" || air > today }
	return func(season, episode int, air string) bool {
		if season == 0 {
			return false // specials only on request, from the show page
		}
		switch mode {
		case ScopeAll:
			return true
		case ScopeLatest:
			return season >= latest || future(air)
		case ScopeFuture:
			return future(air)
		}
		return slices.Contains(seasons, season)
	}
}

var reBadPath = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

// cleanName makes a title safe as a file or folder name on every OS.
func cleanName(s string) string {
	s = strings.ReplaceAll(s, ": ", " - ")
	s = reBadPath.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimRight(s, ". ")
}

func seriesFolder(title string, year int) string {
	if year > 0 {
		return cleanName(fmt.Sprintf("%s (%d)", title, year))
	}
	return cleanName(title)
}

// tvLibrary returns the library to add shows to: the one given, or the
// first TV library.
func (s *Service) tvLibrary(ctx context.Context, id int64) (*store.Library, error) {
	if id != 0 {
		l, err := s.Store.GetLibrary(ctx, id)
		if err != nil {
			return nil, err
		}
		if l.MediaType != "tv" {
			return nil, errors.New("that library is not a TV library")
		}
		return l, nil
	}
	libs, err := s.Store.ListLibraries(ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range libs {
		if l.MediaType == "tv" {
			return &l, nil
		}
	}
	return nil, ErrNoLibrary
}

// AddSeries starts tracking a show, or widens what is monitored if it is
// already tracked, and queues searches for the wanted episodes.
func (s *Service) AddSeries(ctx context.Context, tmdbID int64, libraryID int64, scope json.RawMessage) (*store.Series, error) {
	mode, seasons, err := ParseScope(scope)
	if err != nil {
		return nil, err
	}
	show, err := s.TMDB(ctx).Series(ctx, int(tmdbID), true)
	if err != nil {
		return nil, err
	}
	monitor := monitorFunc(mode, seasons, show)

	existing, err := s.Store.GetSeriesByTMDB(ctx, tmdbID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	in := toInput(show)
	if existing == nil {
		lib, err := s.tvLibrary(ctx, libraryID)
		if err != nil {
			return nil, err
		}
		in.LibraryID = lib.ID
		in.Folder = seriesFolder(show.Title, show.Year)
	}
	id, err := s.Store.UpsertSeries(ctx, in, monitor)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		// Already tracked: monitor what this request adds.
		eps, err := s.Store.EpisodesBySeries(ctx, id)
		if err != nil {
			return nil, err
		}
		var on []int64
		for _, e := range eps {
			if !e.Monitored && monitor(e.Season, e.Number, e.AirDate) {
				on = append(on, e.ID)
			}
		}
		if err := s.Store.SetMonitored(ctx, on, true); err != nil {
			return nil, err
		}
	}
	s.Log.Info("show added", "title", show.Title, "tmdb", tmdbID, "scope", mode)
	s.publish("series", map[string]any{"id": id})
	s.Kick()
	return s.Store.GetSeries(ctx, id)
}

func toInput(show *metadata.Show) store.SeriesInput {
	in := store.SeriesInput{TMDBID: int64(show.TMDBID), TVDBID: int64(show.TVDBID), IMDBID: show.IMDBID, Title: show.Title,
		Year: show.Year, Overview: show.Overview, PosterPath: show.PosterPath, Runtime: show.Runtime}
	for _, sn := range show.Seasons {
		si := store.SeasonInput{Number: sn.Number}
		for _, e := range sn.Episodes {
			si.Episodes = append(si.Episodes, store.EpisodeInput{Number: e.Number, Title: e.Title, AirDate: e.AirDate, Runtime: e.Runtime})
		}
		in.Seasons = append(in.Seasons, si)
	}
	return in
}

// RefreshSeries reloads a show's metadata. New episodes that have not aired
// yet are monitored if the show is.
func (s *Service) RefreshSeries(ctx context.Context, id int64) error {
	sr, err := s.Store.GetSeries(ctx, id)
	if err != nil {
		return err
	}
	show, err := s.TMDB(ctx).Series(ctx, int(sr.TMDBID), true)
	if err != nil {
		return err
	}
	today := store.Today()
	_, err = s.Store.UpsertSeries(ctx, toInput(show), func(season, _ int, air string) bool {
		return sr.Monitored && season > 0 && (air == "" || air >= today)
	})
	return err
}

// ---- Scheduler ----

// Run drives the pipeline until ctx ends: poll downloads, search for due
// episodes, refresh metadata.
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
		s.Log.Error("polling downloads failed", "err", err)
	}
	if err := s.SearchDue(ctx); err != nil && ctx.Err() == nil {
		s.Log.Error("searching failed", "err", err)
	}
}

func (s *Service) refreshStale(ctx context.Context) {
	ids, err := s.Store.SeriesNeedingRefresh(ctx, RefreshInterval)
	if err != nil {
		s.Log.Error("listing shows to refresh failed", "err", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if err := s.RefreshSeries(ctx, id); err != nil {
			s.Log.Warn("metadata refresh failed", "series", id, "err", err)
		}
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func itoa(n int) string { return fmt.Sprint(n) }
