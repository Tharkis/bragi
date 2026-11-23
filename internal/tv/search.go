package tv

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Tharkis/bragi/internal/grabs"
	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/release"
	"github.com/Tharkis/bragi/internal/store"
)

// ErrNoIndexers means no enabled indexer is set up.
var ErrNoIndexers = errors.New("no indexer is set up")

// Target is what one search looks for: one episode, or a full season.
type Target struct {
	Series   *store.Series
	Season   int
	Episodes []store.Episode // the wanted episodes this search covers
	// FullSeason searches for a season pack.
	FullSeason bool
	// MediaItemID is the episode (or the season, for a pack) a grab links to.
	MediaItemID int64
}

func (t Target) label() string {
	if t.FullSeason {
		return fmt.Sprintf("%s season %d", t.Series.Title, t.Season)
	}
	return fmt.Sprintf("%s S%02dE%02d", t.Series.Title, t.Season, t.Episodes[0].Number)
}

func (t Target) episodeIDs() []int64 {
	ids := make([]int64, len(t.Episodes))
	for i, e := range t.Episodes {
		ids[i] = e.ID
	}
	return ids
}

// SearchDue searches for every episode whose search time has come.
func (s *Service) SearchDue(ctx context.Context) error {
	due, err := s.Store.DueEpisodes(ctx)
	if err != nil {
		return err
	}
	if len(due) == 0 {
		return nil
	}
	targets, err := s.plan(ctx, due)
	if err != nil {
		return err
	}
	for i, t := range targets {
		if i >= searchesPerPass || ctx.Err() != nil {
			break
		}
		s.searchAndGrab(ctx, t)
	}
	return nil
}

// plan groups due episodes into searches. A season whose episodes have all
// aired and are all wanted is searched as a pack first; if no pack is found
// it falls back to one search per episode on the next pass.
func (s *Service) plan(ctx context.Context, due []store.Episode) ([]Target, error) {
	type key struct {
		series int64
		season int
	}
	groups := map[key][]store.Episode{}
	var order []key
	for _, e := range due {
		k := key{e.SeriesID, e.Season}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], e)
	}
	seriesCache := map[int64]*store.Series{}
	var out []Target
	for _, k := range order {
		sr, ok := seriesCache[k.series]
		if !ok {
			var err error
			if sr, err = s.Store.GetSeries(ctx, k.series); err != nil {
				return nil, err
			}
			seriesCache[k.series] = sr
		}
		eps := groups[k]
		if pack, ok := s.packTarget(ctx, sr, k.season, eps); ok {
			out = append(out, pack)
			continue
		}
		for _, e := range eps {
			out = append(out, Target{Series: sr, Season: k.season, Episodes: []store.Episode{e}, MediaItemID: e.ID})
		}
	}
	return out, nil
}

// packTarget returns a season-pack search if every episode of the season
// has aired, all of them are wanted, and no pack search failed recently
// (a failed pack search leaves "No season pack" as the note).
func (s *Service) packTarget(ctx context.Context, sr *store.Series, season int, due []store.Episode) (Target, bool) {
	if len(due) < 2 {
		return Target{}, false
	}
	for _, e := range due {
		if e.SearchNote == noPackNote {
			return Target{}, false
		}
	}
	all, err := s.Store.EpisodesBySeries(ctx, sr.ID)
	if err != nil {
		return Target{}, false
	}
	today := store.Today()
	var seasonID int64
	count := 0
	for _, e := range all {
		if e.Season != season {
			continue
		}
		seasonID = e.SeasonID
		count++
		if e.AirDate == "" || e.AirDate > today {
			return Target{}, false
		}
	}
	if count != len(due) {
		return Target{}, false
	}
	return Target{Series: sr, Season: season, Episodes: due, FullSeason: true, MediaItemID: seasonID}, true
}

const noPackNote = "No season pack found; searching episode by episode"

// searchAndGrab runs one automatic search and grabs the best release.
func (s *Service) searchAndGrab(ctx context.Context, t Target) {
	ids := t.episodeIDs()
	// If Bragi stops mid-search, the episodes come due again in 10 minutes.
	s.Store.SetEpisodeState(ctx, ids, "searching", "", time.Now().Add(10*time.Minute))
	s.publish("episodes", map[string]any{"seriesId": t.Series.ID})

	cands, searched, err := s.Search(ctx, t)
	if err != nil {
		note, retry := "Search failed: "+err.Error(), RetryAfter
		if errors.Is(err, ErrNoIndexers) {
			note, retry = "No indexer is set up", NoIndexerRetry
		}
		s.Log.Warn("search failed", "for", t.label(), "err", err, "nextSearch", retry)
		s.Store.SetEpisodeState(ctx, ids, "wanted", note, time.Now().Add(retry))
		s.publish("episodes", map[string]any{"seriesId": t.Series.ID})
		return
	}
	out := grabs.Try(s.Log, t.label(), cands,
		grabs.Video,
		func(c release.Candidate) error { return s.Grab(ctx, t, c) },
		func(c release.Candidate, reason string) { s.Store.Blocklist(ctx, t.MediaItemID, c.Title, reason) },
		errNoClient)
	if out.Grabbed {
		return
	}
	if note, retry, ok := out.Note(); ok {
		if retry == 0 {
			retry = RetryAfter
		}
		s.Store.SetEpisodeState(ctx, ids, "wanted", note, time.Now().Add(retry))
		s.publish("episodes", map[string]any{"seriesId": t.Series.ID})
		return
	}
	if t.FullSeason {
		// Try episode by episode right away.
		s.Log.Info("no season pack found, searching episode by episode", "for", t.label())
		s.Store.SetEpisodeState(ctx, ids, "wanted", noPackNote, time.Now())
		s.publish("episodes", map[string]any{"seriesId": t.Series.ID})
		s.Kick()
		return
	}
	note := "No releases found"
	if len(cands) > 0 {
		note = fmt.Sprintf("Nothing suitable in %d results (%s)", len(cands), topReasons(cands))
	}
	if searched == 0 {
		note = "Every indexer failed to answer"
	}
	s.Log.Info("nothing grabbed", "for", t.label(), "reason", note, "nextSearch", time.Now().Add(RetryAfter).Format(time.RFC3339))
	s.Store.SetEpisodeState(ctx, ids, "wanted", note, time.Now().Add(RetryAfter))
	s.publish("episodes", map[string]any{"seriesId": t.Series.ID})
}

// topReasons summarizes why releases were rejected, most common first.
func topReasons(cands []release.Candidate) string {
	counts := map[string]int{}
	for _, c := range cands {
		r := c.Rejected
		if r == "" {
			continue
		}
		if strings.HasPrefix(r, "Too big") {
			r = "too big"
		} else if strings.HasSuffix(r, "is not in the quality preset") {
			r = "quality not in preset"
		}
		counts[strings.ToLower(r)]++
	}
	type kv struct {
		k string
		v int
	}
	var list []kv
	for k, v := range counts {
		list = append(list, kv{k, v})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v || list[i].v == list[j].v && list[i].k < list[j].k })
	var parts []string
	for i, x := range list {
		if i == 2 {
			break
		}
		parts = append(parts, fmt.Sprintf("%d %s", x.v, x.k))
	}
	return strings.Join(parts, ", ")
}

// Search asks every enabled indexer and returns the releases ranked for t,
// rejected ones last. searched counts the indexers that answered.
func (s *Service) Search(ctx context.Context, t Target) ([]release.Candidate, int, error) {
	idx, err := s.Store.ListIntegrations(ctx, store.Indexers)
	if err != nil {
		return nil, 0, err
	}
	q := integrations.Query{MediaType: "tv", Text: t.Series.Title, TMDBID: int(t.Series.TMDBID), TVDBID: int(t.Series.TVDBID), Season: t.Season}
	if !t.FullSeason {
		q.Episode = t.Episodes[0].Number
	}
	s.Log.Info("searching", "for", t.label(), "query", integrations.SearchText(q))
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
			s.Log.Warn("indexer skipped", "indexer", ix.Name, "err", err)
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
				s.Log.Warn("indexer search failed", "indexer", name, "query", t.label(), "err", err)
				return
			}
			searched++
			s.Log.Debug("indexer answered", "indexer", name, "for", t.label(), "results", len(rs))
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
	preset, err := s.presetFor(ctx, t.Series.LibraryID)
	if err != nil {
		return nil, searched, err
	}
	blocked, err := s.Store.BlockedTitles(ctx, t.Series.ID)
	if err != nil {
		return nil, searched, err
	}
	w := release.Want{SeriesTitle: t.Series.Title, Year: t.Series.Year, Season: t.Season, FullSeason: t.FullSeason,
		Runtime: t.Series.Runtime, Blocklist: blocked}
	for _, e := range t.Episodes {
		w.Episodes = append(w.Episodes, e.Number)
		if e.Runtime > 0 {
			w.Runtime = e.Runtime
		}
	}
	cands := release.Evaluate(all, w, preset)
	grabs.LogResults(s.Log, t.label(), searched, cands, grabs.Video, grabs.VideoDetail)
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

var errNoClient = errors.New("no download client is set up for this kind of release")

// Grab sends a release to the first enabled download client that handles
// its protocol and records the download.
func (s *Service) Grab(ctx context.Context, t Target, c release.Candidate) error {
	clients, err := s.Store.ListIntegrations(ctx, store.DownloadClients)
	if err != nil {
		return err
	}
	for _, dc := range clients {
		if !dc.Enabled {
			continue
		}
		kind, ok := integrations.KindByID(dc.Kind)
		if !ok || !containsProtocol(kind.Protocols, c.Protocol) {
			continue
		}
		a, err := integrations.NewDownloadClient(dc.Kind, configOf(dc, "tv"), s.HC)
		if err != nil {
			return err
		}
		jobID, err := a.Add(ctx, c.Release)
		if err != nil {
			return fmt.Errorf("%s: %w", dc.Name, err)
		}
		id, err := s.Store.CreateDownload(ctx, store.Download{MediaItemID: t.MediaItemID, DownloadClientID: dc.ID, ClientJobID: jobID,
			ReleaseTitle: c.Title, Protocol: string(c.Protocol), Indexer: c.Indexer, SizeBytes: c.SizeBytes, Quality: c.Quality, Status: "queued"})
		if err != nil {
			return err
		}
		s.Store.SetEpisodeState(ctx, t.episodeIDs(), "downloading", "", time.Time{})
		s.Log.Info("grabbed", "release", c.Title, "client", dc.Name, "for", t.label())
		s.publish("download", map[string]any{"id": id, "seriesId": t.Series.ID})
		return nil
	}
	return errNoClient
}

func containsProtocol(ps []integrations.Protocol, p integrations.Protocol) bool {
	for _, x := range ps {
		if x == p {
			return true
		}
	}
	return false
}

// TargetFor builds a search target for a show's episode, or a whole season
// when episode is 0.
func (s *Service) TargetFor(ctx context.Context, seriesID int64, season, episode int) (Target, error) {
	sr, err := s.Store.GetSeries(ctx, seriesID)
	if err != nil {
		return Target{}, err
	}
	eps, err := s.Store.EpisodesBySeries(ctx, seriesID)
	if err != nil {
		return Target{}, err
	}
	t := Target{Series: sr, Season: season, FullSeason: episode == 0}
	for _, e := range eps {
		if e.Season != season {
			continue
		}
		if episode == 0 || e.Number == episode {
			t.Episodes = append(t.Episodes, e)
			t.MediaItemID = e.ID
			if episode == 0 {
				t.MediaItemID = e.SeasonID
			}
		}
	}
	if len(t.Episodes) == 0 {
		return Target{}, store.ErrNotFound
	}
	return t, nil
}

// SearchNow queues an immediate automatic search for a show, a season or
// an episode (season and episode 0 mean every wanted episode).
func (s *Service) SearchNow(ctx context.Context, seriesID int64, season, episode int) (int, error) {
	eps, err := s.Store.EpisodesBySeries(ctx, seriesID)
	if err != nil {
		return 0, err
	}
	today := store.Today()
	var ids []int64
	for _, e := range eps {
		if (season > 0 || episode > 0) && e.Season != season || episode > 0 && e.Number != episode {
			continue
		}
		if e.Status == "imported" || e.Status == "downloading" || e.AirDate == "" || e.AirDate > today {
			continue
		}
		if episode == 0 && !e.Monitored {
			continue
		}
		if !e.Monitored {
			s.Store.SetMonitored(ctx, []int64{e.ID}, true)
		}
		ids = append(ids, e.ID)
	}
	if err := s.Store.SetEpisodeState(ctx, ids, "wanted", "", time.Now()); err != nil {
		return 0, err
	}
	s.Kick()
	return len(ids), nil
}
