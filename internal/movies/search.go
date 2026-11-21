package movies

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/release"
	"github.com/Tharkis/bragi/internal/store"
)

// ErrNoIndexers means no enabled indexer is set up.
var ErrNoIndexers = errors.New("no indexer is set up")

var errNoClient = errors.New("no download client is set up for this kind of release")

func label(m *store.Movie) string {
	if m.Year > 0 {
		return fmt.Sprintf("%s (%d)", m.Title, m.Year)
	}
	return m.Title
}

// SearchDue searches for every movie whose search time has come.
func (s *Service) SearchDue(ctx context.Context) error {
	due, err := s.Store.DueMovies(ctx)
	if err != nil {
		return err
	}
	for i := range due {
		if i >= searchesPerPass || ctx.Err() != nil {
			break
		}
		s.searchAndGrab(ctx, &due[i])
	}
	return nil
}

func (s *Service) setState(ctx context.Context, m *store.Movie, status, note string, next time.Time) {
	s.Store.SetEpisodeState(ctx, []int64{m.ID}, status, note, next)
	s.publish("movie", map[string]any{"id": m.ID})
}

// searchAndGrab runs one automatic search and grabs the best release.
func (s *Service) searchAndGrab(ctx context.Context, m *store.Movie) {
	// If Bragi stops mid-search, the movie comes due again in 10 minutes.
	s.setState(ctx, m, "searching", "", time.Now().Add(10*time.Minute))

	cands, searched, err := s.Search(ctx, m)
	if err != nil {
		note, retry := "Search failed: "+err.Error(), RetryAfter
		if errors.Is(err, ErrNoIndexers) {
			note, retry = "No indexer is set up", NoIndexerRetry
		}
		s.setState(ctx, m, "wanted", note, time.Now().Add(retry))
		return
	}
	for _, c := range cands {
		if c.Rejected != "" {
			break
		}
		err := s.Grab(ctx, m, c)
		if err == nil {
			return
		}
		s.Log.Warn("grab failed, trying the next release", "release", c.Title, "err", err)
		if errors.Is(err, errNoClient) {
			continue
		}
		s.Store.Blocklist(ctx, m.ID, c.Title, err.Error())
	}
	note := "No releases found"
	if len(cands) > 0 {
		note = fmt.Sprintf("Nothing suitable in %d results (%s)", len(cands), topReasons(cands))
	}
	if searched == 0 {
		note = "Every indexer failed to answer"
	}
	s.setState(ctx, m, "wanted", note, time.Now().Add(RetryAfter))
}

// topReasons summarizes why releases were rejected, most common first.
func topReasons(cands []release.Candidate) string {
	counts := map[string]int{}
	for _, c := range cands {
		r := c.Rejected
		if strings.HasPrefix(r, "Too big") {
			r = "too big"
		} else if strings.HasSuffix(r, "is not in the quality preset") {
			r = "quality not in preset"
		}
		counts[strings.ToLower(r)]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return counts[keys[i]] > counts[keys[j]] || counts[keys[i]] == counts[keys[j]] && keys[i] < keys[j]
	})
	var parts []string
	for i, k := range keys {
		if i == 2 {
			break
		}
		parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
	}
	return strings.Join(parts, ", ")
}

// Search asks every enabled indexer for the movie and returns the releases
// ranked, rejected ones last. searched counts the indexers that answered.
func (s *Service) Search(ctx context.Context, m *store.Movie) ([]release.Candidate, int, error) {
	idx, err := s.Store.ListIntegrations(ctx, store.Indexers)
	if err != nil {
		return nil, 0, err
	}
	q := integrations.Query{MediaType: "movie", Text: m.Title, Year: m.Year, TMDBID: int(m.TMDBID), IMDBID: m.IMDBID}
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		all      []integrations.Release
		searched int
		enabled  int
	)
	for _, ix := range idx {
		if !ix.Enabled {
			continue
		}
		a, err := integrations.NewIndexer(ix.Kind, configOf(ix), s.HC)
		if err != nil {
			continue
		}
		enabled++
		wg.Add(1)
		go func(name string, a integrations.Indexer) {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			rs, err := a.Search(sctx, q)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				s.Log.Warn("indexer search failed", "indexer", name, "query", label(m), "err", err)
				return
			}
			searched++
			for i := range rs {
				if rs[i].Indexer == "" {
					rs[i].Indexer = name
				}
			}
			all = append(all, rs...)
		}(ix.Name, a)
	}
	wg.Wait()
	if enabled == 0 {
		return nil, 0, ErrNoIndexers
	}
	preset, err := s.presetFor(ctx, m.LibraryID)
	if err != nil {
		return nil, searched, err
	}
	blocked, err := s.Store.BlockedTitles(ctx, m.ID)
	if err != nil {
		return nil, searched, err
	}
	runtime := m.Runtime
	if runtime == 0 {
		runtime = 120 // a typical feature, so the size cap still means something
	}
	w := release.Want{Movie: true, SeriesTitle: m.Title, Year: m.Year, Runtime: runtime, Blocklist: blocked}
	cands := release.Evaluate(all, w, preset)
	s.Log.Info("searched", "query", label(m), "indexers", searched, "results", len(all))
	return cands, searched, nil
}

func (s *Service) presetFor(ctx context.Context, libraryID int64) (release.Preset, error) {
	presets, err := s.Store.ListQualityPresets(ctx)
	if err != nil {
		return release.Preset{}, err
	}
	if len(presets) == 0 {
		return release.Preset{}, errors.New("no quality presets")
	}
	pick := presets[0]
	if lib, err := s.Store.GetLibrary(ctx, libraryID); err == nil && lib.QualityPresetID != nil {
		for _, p := range presets {
			if p.ID == *lib.QualityPresetID {
				pick = p
			}
		}
	}
	return release.Preset{Qualities: pick.Qualities, MaxMBPerMin: pick.MaxMBPerMin}, nil
}

// configOf builds an adapter's settings. mediaType picks the category a new
// download goes under; it is empty when nothing is being added.
func configOf(i store.Integration, mediaType ...string) integrations.Config {
	cat := i.Category
	if len(mediaType) > 0 {
		cat = i.CategoryFor(mediaType[0])
	}
	return integrations.Config{BaseURL: i.BaseURL, APIKey: i.APIKey, Username: i.Username, Password: i.Password, Category: cat,
		RemotePath: i.RemotePath, LocalPath: i.LocalPath}
}

// Grab sends a release to the first enabled download client that handles
// its protocol and records the download.
func (s *Service) Grab(ctx context.Context, m *store.Movie, c release.Candidate) error {
	clients, err := s.Store.ListIntegrations(ctx, store.DownloadClients)
	if err != nil {
		return err
	}
	for _, dc := range clients {
		if !dc.Enabled {
			continue
		}
		kind, ok := integrations.KindByID(dc.Kind)
		if !ok || !slices.Contains(kind.Protocols, c.Protocol) {
			continue
		}
		a, err := integrations.NewDownloadClient(dc.Kind, configOf(dc, "movie"), s.HC)
		if err != nil {
			return err
		}
		jobID, err := a.Add(ctx, c.Release)
		if err != nil {
			return fmt.Errorf("%s: %w", dc.Name, err)
		}
		id, err := s.Store.CreateDownload(ctx, store.Download{MediaItemID: m.ID, DownloadClientID: dc.ID, ClientJobID: jobID,
			ReleaseTitle: c.Title, Protocol: string(c.Protocol), Indexer: c.Indexer, SizeBytes: c.SizeBytes, Quality: c.Quality, Status: "queued"})
		if err != nil {
			return err
		}
		s.Store.SetEpisodeState(ctx, []int64{m.ID}, "downloading", "", time.Time{})
		s.Log.Info("grabbed", "release", c.Title, "client", dc.Name, "for", label(m))
		s.publish("download", map[string]any{"id": id, "movieId": m.ID})
		s.publish("movie", map[string]any{"id": m.ID})
		return nil
	}
	return errNoClient
}

// SearchNow queues an immediate automatic search for a movie, turning
// monitoring on. It returns false when there is nothing to search for:
// the movie has a file, is downloading, or is not out yet.
func (s *Service) SearchNow(ctx context.Context, id int64) (bool, string, error) {
	m, err := s.Store.GetMovie(ctx, id)
	if err != nil {
		return false, "", err
	}
	switch {
	case m.Status == "downloading":
		return false, "It is already downloading.", nil
	case m.FilePath != "":
		return false, "It is already in the library. Use Pick a release to replace the file.", nil
	case !m.IsReleased():
		return false, "It is not out yet. Bragi searches once it is.", nil
	}
	if !m.Monitored {
		s.Store.SetMonitored(ctx, []int64{m.ID}, true)
	}
	if err := s.Store.SetEpisodeState(ctx, []int64{m.ID}, "wanted", "", time.Now()); err != nil {
		return false, "", err
	}
	s.Kick()
	return true, "", nil
}
