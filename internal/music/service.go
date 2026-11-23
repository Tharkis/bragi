// Package music runs the music pipeline: follow an artist or pick albums
// from MusicBrainz, search Soulseek and then the indexers for each wanted
// album, send the best release to a download client, and import and tag
// the files into "Library/Artist/Album (Year)/".
package music

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Tharkis/bragi/internal/events"
	"github.com/Tharkis/bragi/internal/grabs"
	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/library"
	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/release"
	"github.com/Tharkis/bragi/internal/store"
)

// Settings the pipeline reads.
const (
	// SettingAlbumTypes lists the album types followed artists monitor,
	// comma separated. Empty means DefaultAlbumTypes.
	SettingAlbumTypes = "music_album_types"
	// SettingUpgrades is "false" to stop looking for better copies.
	SettingUpgrades = "music_upgrades"
)

// DefaultAlbumTypes is what a followed artist monitors unless set otherwise.
var DefaultAlbumTypes = []string{"Album", "EP", "Single"}

// Timing of the scheduler. Variables so tests can shorten them.
var (
	PollInterval    = 30 * time.Second
	RetryAfter      = 12 * time.Hour
	UpgradeAfter    = 24 * time.Hour
	NoIndexerRetry  = 1 * time.Hour
	RefreshAge      = 24 * time.Hour
	searchesPerPass = 5
)

// ErrNoIndexers means neither an indexer nor slskd is set up.
var ErrNoIndexers = errors.New("no indexer or Soulseek client is set up")

// ErrNoLibrary means no music library is set up.
var ErrNoLibrary = errors.New("add a Music library under Settings, Libraries first")

// Monitor modes for a followed artist.
const (
	MonitorAll    = "all"    // every album of the wanted types, and new ones
	MonitorFuture = "future" // only albums that come out from now on
	MonitorNone   = "none"   // only the albums picked by hand
)

// Service holds the pipeline's dependencies.
type Service struct {
	Store  *store.Store
	HC     *http.Client
	Log    *slog.Logger
	Events *events.Hub
	// OnImported runs after an album is imported, to tell media servers.
	OnImported func(ctx context.Context) error

	kick     chan struct{}
	importMu sync.Mutex
	forceMu  sync.Mutex
	force    map[int64]bool
}

// New returns a Service.
func New(st *store.Store, hc *http.Client, log *slog.Logger, hub *events.Hub) *Service {
	if log == nil {
		log = slog.Default()
	}
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	if hub == nil {
		hub = events.NewHub()
	}
	return &Service{Store: st, HC: hc, Log: log, Events: hub, kick: make(chan struct{}, 1), force: map[int64]bool{}}
}

// MB returns the MusicBrainz client, caching lookups in the database.
func (s *Service) MB() *metadata.MusicBrainz { return &metadata.MusicBrainz{HC: s.HC, Cache: s.Store} }

// Kick asks the scheduler to run a pass now.
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *Service) publish(typ string, data any) {
	s.Events.Publish(events.Event{Type: typ, Data: data})
}

// AlbumTypes returns the album types followed artists monitor.
func (s *Service) AlbumTypes(ctx context.Context) []string {
	v, _ := s.Store.GetSetting(ctx, SettingAlbumTypes)
	var out []string
	for _, t := range strings.Split(v, ",") {
		if t = strings.TrimSpace(t); slices.Contains(metadata.AlbumTypes, t) {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return DefaultAlbumTypes
	}
	return out
}

func (s *Service) upgrades(ctx context.Context) bool {
	v, _ := s.Store.GetSetting(ctx, SettingUpgrades)
	return v != "false"
}

// AlbumFolder is "Album (Year)", inside the artist's folder.
func AlbumFolder(title string, year int) string {
	t := library.CleanName(title)
	if t == "" {
		t = "Unknown Album"
	}
	if year > 0 {
		t = fmt.Sprintf("%s (%d)", t, year)
	}
	return t
}

// ArtistFolder is the artist's name, safe for a folder.
func ArtistFolder(name string) string {
	if n := library.CleanName(name); n != "" {
		return n
	}
	return "Unknown Artist"
}

func (s *Service) library(ctx context.Context, id int64) (*store.Library, error) {
	if id != 0 {
		l, err := s.Store.GetLibrary(ctx, id)
		if err != nil {
			return nil, err
		}
		if l.MediaType != "music" {
			return nil, errors.New("that library is not for music")
		}
		return l, nil
	}
	libs, err := s.Store.ListLibraries(ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range libs {
		if l.MediaType == "music" {
			return &l, nil
		}
	}
	return nil, ErrNoLibrary
}

func overviewOf(a *metadata.Artist) string {
	var parts []string
	if a.Disambiguation != "" {
		parts = append(parts, a.Disambiguation)
	}
	kind := a.Type
	if kind == "" {
		kind = "Artist"
	}
	if a.Country != "" {
		kind += " from " + countryName(a.Country)
	}
	parts = append(parts, kind)
	if len(a.Genres) > 0 {
		parts = append(parts, strings.Join(a.Genres, ", "))
	}
	return strings.Join(parts, " · ")
}

func countryName(code string) string {
	names := map[string]string{"US": "the US", "GB": "the UK", "CA": "Canada", "AU": "Australia", "IE": "Ireland", "SE": "Sweden",
		"NO": "Norway", "IS": "Iceland", "DE": "Germany", "FR": "France", "JP": "Japan", "NZ": "New Zealand", "NL": "the Netherlands",
		"FI": "Finland", "DK": "Denmark", "BE": "Belgium", "ES": "Spain", "IT": "Italy", "BR": "Brazil", "MX": "Mexico", "KR": "South Korea"}
	if n, ok := names[code]; ok {
		return n
	}
	return code
}

// ---- Adding and refreshing ----

// AddOptions says what to monitor when following an artist.
type AddOptions struct {
	// Monitor is MonitorAll, MonitorFuture or MonitorNone.
	Monitor string
	// Albums are release groups to monitor whatever their type.
	Albums    []string
	LibraryID int64
}

// AddArtist starts tracking an artist (or updates one already tracked) and
// monitors albums as opts says. Monitoring only ever grows here; nothing
// already monitored is turned off.
func (s *Service) AddArtist(ctx context.Context, mbid string, opts AddOptions) (*store.Artist, error) {
	lib, err := s.library(ctx, opts.LibraryID)
	if err != nil {
		return nil, err
	}
	a, err := s.MB().Artist(ctx, mbid)
	if err != nil {
		return nil, err
	}
	existing, _ := s.Store.GetArtistByForeignID(ctx, mbid)
	id, err := s.Store.UpsertArtist(ctx, store.ArtistInput{ForeignID: a.ID, Name: a.Name, SortName: a.SortName, Overview: overviewOf(a),
		ImageURL: a.ImageURL, LibraryID: lib.ID, Folder: ArtistFolder(a.Name)})
	if err != nil {
		return nil, err
	}
	types := s.AlbumTypes(ctx)
	today := store.Today()
	var monitor []int64
	for _, al := range a.Albums {
		aid, _, err := s.Store.UpsertAlbum(ctx, id, store.AlbumInput{ForeignID: al.ID, Title: al.Title, Year: al.Year, ReleaseDate: al.ReleaseDate,
			Type: al.Type, Folder: AlbumFolder(al.Title, al.Year)})
		if err != nil {
			return nil, err
		}
		wantType := slices.Contains(types, al.Type)
		upcoming := al.ReleaseDate == "" || al.ReleaseDate > today
		switch {
		case slices.Contains(opts.Albums, al.ID):
			monitor = append(monitor, aid)
		case opts.Monitor == MonitorAll && wantType:
			monitor = append(monitor, aid)
		case opts.Monitor == MonitorFuture && wantType && upcoming && al.ReleaseDate != "":
			monitor = append(monitor, aid)
		}
	}
	if err := s.Store.SetAlbumMonitored(ctx, monitor, true); err != nil {
		return nil, err
	}
	switch {
	case opts.Monitor == MonitorAll || opts.Monitor == MonitorFuture:
		s.Store.SetArtistMonitorNew(ctx, id, true)
	case existing == nil:
		s.Store.SetArtistMonitorNew(ctx, id, false)
	}
	s.Log.Info("artist added", "artist", a.Name, "albums", len(a.Albums), "monitored", len(monitor))
	s.publish("artist", map[string]any{"id": id})
	s.Kick()
	return s.Store.GetArtist(ctx, id)
}

// AddAlbum monitors one album, adding its artist (without following it)
// if needed.
func (s *Service) AddAlbum(ctx context.Context, albumID string, libraryID int64) (*store.Album, error) {
	if al, err := s.Store.GetAlbumByForeignID(ctx, albumID); err == nil {
		if err := s.Store.SetAlbumMonitored(ctx, []int64{al.ID}, true); err != nil {
			return nil, err
		}
		s.Kick()
		return s.Store.GetAlbum(ctx, al.ID)
	}
	g, err := s.MB().Album(ctx, albumID)
	if err != nil {
		return nil, err
	}
	if g.ArtistID == "" {
		return nil, errors.New("MusicBrainz does not say whose album this is")
	}
	if _, err := s.AddArtist(ctx, g.ArtistID, AddOptions{Monitor: MonitorNone, Albums: []string{albumID}, LibraryID: libraryID}); err != nil {
		return nil, err
	}
	al, err := s.Store.GetAlbumByForeignID(ctx, albumID)
	if err != nil {
		// The album is filed under another artist on MusicBrainz's side
		// (a collaboration): track it there.
		ar, err2 := s.Store.GetArtistByForeignID(ctx, g.ArtistID)
		if err2 != nil {
			return nil, err
		}
		aid, _, err := s.Store.UpsertAlbum(ctx, ar.ID, store.AlbumInput{ForeignID: g.ID, Title: g.Title, Year: g.Year, ReleaseDate: g.ReleaseDate,
			Type: g.Type, Folder: AlbumFolder(g.Title, g.Year)})
		if err != nil {
			return nil, err
		}
		s.Store.SetAlbumMonitored(ctx, []int64{aid}, true)
		al, err = s.Store.GetAlbum(ctx, aid)
		if err != nil {
			return nil, err
		}
	}
	return al, nil
}

// EnsureTracks fetches an album's track list if it has none yet.
func (s *Service) EnsureTracks(ctx context.Context, al *store.Album) error {
	if al.Tracks > 0 {
		return nil
	}
	rel, err := s.MB().BestRelease(ctx, al.ForeignID)
	if err != nil {
		return err
	}
	tracks := make([]store.TrackInput, len(rel.Tracks))
	for i, t := range rel.Tracks {
		tracks[i] = store.TrackInput{RecordingID: t.RecordingID, Disc: t.Disc, Number: t.Number, Title: t.Title, Artist: t.Artist, Seconds: t.Seconds}
	}
	if err := s.Store.SetAlbumTracks(ctx, al.ID, rel.ID, tracks); err != nil {
		return err
	}
	al.Tracks, al.ReleaseID = len(tracks), rel.ID
	return nil
}

// Refresh reloads an artist from MusicBrainz. Albums that are new to Bragi
// are monitored if the artist follows new releases.
func (s *Service) Refresh(ctx context.Context, artistID int64) error {
	ar, err := s.Store.GetArtist(ctx, artistID)
	if err != nil {
		return err
	}
	a, err := s.MB().Artist(ctx, ar.ForeignID)
	if err != nil {
		return err
	}
	if _, err := s.Store.UpsertArtist(ctx, store.ArtistInput{ForeignID: a.ID, Name: a.Name, SortName: a.SortName, Overview: overviewOf(a),
		ImageURL: a.ImageURL}); err != nil {
		return err
	}
	types := s.AlbumTypes(ctx)
	recent := time.Now().AddDate(0, 0, -90).Format("2006-01-02")
	var monitor []int64
	for _, al := range a.Albums {
		aid, isNew, err := s.Store.UpsertAlbum(ctx, artistID, store.AlbumInput{ForeignID: al.ID, Title: al.Title, Year: al.Year,
			ReleaseDate: al.ReleaseDate, Type: al.Type, Folder: AlbumFolder(al.Title, al.Year)})
		if err != nil {
			return err
		}
		if isNew && ar.MonitorNew && slices.Contains(types, al.Type) && (al.ReleaseDate == "" || al.ReleaseDate >= recent) {
			monitor = append(monitor, aid)
			s.Log.Info("new album", "artist", a.Name, "album", al.Title)
		}
	}
	if len(monitor) > 0 {
		s.Store.SetAlbumMonitored(ctx, monitor, true)
		s.Kick()
	}
	s.publish("artist", map[string]any{"id": artistID})
	return nil
}

// ---- Scheduler ----

// Run drives the pipeline until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(PollInterval)
	defer t.Stop()
	for {
		s.Pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

// Pass runs one round of download polling, refreshing and searching.
func (s *Service) Pass(ctx context.Context) {
	if err := s.PollDownloads(ctx); err != nil && ctx.Err() == nil {
		s.Log.Error("polling music downloads failed", "err", err)
	}
	if ids, err := s.Store.ArtistsNeedingRefresh(ctx, RefreshAge); err == nil {
		for i, id := range ids {
			if i >= 5 || ctx.Err() != nil {
				break
			}
			if err := s.Refresh(ctx, id); err != nil {
				s.Log.Warn("artist refresh failed", "id", id, "err", err)
			}
		}
	}
	if err := s.SearchDue(ctx); err != nil && ctx.Err() == nil {
		s.Log.Error("searching for albums failed", "err", err)
	}
}

// SearchDue searches for every album whose search time has come.
func (s *Service) SearchDue(ctx context.Context) error {
	due, err := s.Store.DueAlbums(ctx)
	if err != nil {
		return err
	}
	for i, al := range due {
		if i >= searchesPerPass || ctx.Err() != nil {
			break
		}
		s.searchAndGrab(ctx, &al)
	}
	return nil
}

// SearchNow queues an immediate automatic search for an album, monitoring
// it if it was not.
func (s *Service) SearchNow(ctx context.Context, id int64) error {
	al, err := s.Store.GetAlbum(ctx, id)
	if err != nil {
		return err
	}
	if al.Status == "downloading" {
		return errors.New("it is downloading already")
	}
	if !al.Monitored {
		s.Store.SetAlbumMonitored(ctx, []int64{id}, true)
	}
	status := "wanted"
	if al.Status == "imported" || al.Status == "upgrade_wanted" {
		status = "upgrade_wanted"
	}
	if err := s.Store.SetAlbumState(ctx, id, status, "", time.Now()); err != nil {
		return err
	}
	s.Kick()
	return nil
}

func (s *Service) searchAndGrab(ctx context.Context, al *store.Album) {
	keep := al.Status
	if keep != "upgrade_wanted" {
		keep = "wanted"
	}
	s.Store.SetAlbumState(ctx, al.ID, "searching", "", time.Now().Add(10*time.Minute))
	s.publish("album", map[string]any{"id": al.ID, "artistId": al.ArtistID})
	retry := RetryAfter
	if keep == "upgrade_wanted" {
		retry = UpgradeAfter
	}
	if err := s.EnsureTracks(ctx, al); err != nil {
		s.Store.SetAlbumState(ctx, al.ID, keep, "Could not load the track list: "+err.Error(), time.Now().Add(time.Hour))
		s.publish("album", map[string]any{"id": al.ID, "artistId": al.ArtistID})
		return
	}
	cands, searched, err := s.Search(ctx, al, SourcesAuto)
	if err != nil {
		note := "Search failed: " + err.Error()
		if errors.Is(err, ErrNoIndexers) {
			note, retry = "No indexer or Soulseek client is set up", NoIndexerRetry
		}
		s.Log.Warn("search failed", "for", al.Artist+" - "+al.Title, "err", err, "nextSearch", retry)
		s.Store.SetAlbumState(ctx, al.ID, keep, note, time.Now().Add(retry))
		s.publish("album", map[string]any{"id": al.ID, "artistId": al.ArtistID})
		return
	}
	what := al.Artist + " - " + al.Title
	grabs.LogResults(s.Log, what, searched, cands, describe, detail)
	out := grabs.Try(s.Log, what, cands, describe,
		func(c Candidate) error { return s.Grab(ctx, al, c) },
		func(c Candidate, reason string) { s.Store.Blocklist(ctx, al.ID, c.Title, reason) },
		errNoClient)
	if out.Grabbed {
		return
	}
	if note, r, ok := out.Note(); ok {
		if r == 0 {
			r = retry
		}
		s.Store.SetAlbumState(ctx, al.ID, keep, note, time.Now().Add(r))
		s.publish("album", map[string]any{"id": al.ID, "artistId": al.ArtistID})
		return
	}
	note := "No releases found"
	switch {
	case searched == 0:
		note = "Every indexer failed to answer"
	case len(cands) > 0 && keep == "upgrade_wanted":
		note = "No better copy found yet"
	case len(cands) > 0:
		note = fmt.Sprintf("Nothing suitable in %d results (%s)", len(cands), topReasons(cands))
	}
	s.Log.Info("nothing grabbed", "for", what, "reason", note, "nextSearch", time.Now().Add(retry).Format(time.RFC3339))
	s.Store.SetAlbumState(ctx, al.ID, keep, note, time.Now().Add(retry))
	s.publish("album", map[string]any{"id": al.ID, "artistId": al.ArtistID})
}

func describe(c Candidate) grabs.Release {
	return grabs.Release{Title: c.Title, Protocol: c.Protocol, Rejected: c.Rejected}
}

func detail(c Candidate) []any {
	return []any{"quality", c.Quality, "size", release.HumanSize(c.SizeBytes), "seeders", c.Seeders, "source", c.Indexer}
}

func topReasons(cands []Candidate) string {
	counts := map[string]int{}
	var order []string
	for _, c := range cands {
		if c.Rejected == "" {
			continue
		}
		r := strings.ToLower(c.Rejected)
		if i := strings.Index(r, " ("); i > 0 {
			r = r[:i]
		}
		if counts[r] == 0 {
			order = append(order, r)
		}
		counts[r]++
	}
	var parts []string
	for len(parts) < 2 && len(order) > 0 {
		best := 0
		for i, k := range order {
			if counts[k] > counts[order[best]] {
				best = i
			}
		}
		parts = append(parts, fmt.Sprintf("%d %s", counts[order[best]], order[best]))
		order = append(order[:best], order[best+1:]...)
	}
	return strings.Join(parts, ", ")
}

// SearchText is what Bragi types into indexers and Soulseek for an album.
func SearchText(al *store.Album) string {
	artist := al.Artist
	if strings.EqualFold(artist, "Various Artists") {
		artist = ""
	}
	return strings.TrimSpace(artist + " " + mainTitle(al.Title))
}

// Which sources a search asks.
const (
	// SourcesAuto asks Soulseek first and the indexers only if Soulseek
	// has nothing usable.
	SourcesAuto = "auto"
	// SourcesAll asks everything at once, for the manual pick.
	SourcesAll = "all"
)

type source struct {
	name     string
	a        integrations.Indexer
	soulseek bool
}

// Search asks Soulseek and the indexers for an album and returns the
// releases ranked for it. searched counts the sources that answered.
func (s *Service) Search(ctx context.Context, al *store.Album, which string) ([]Candidate, int, error) {
	idx, err := s.Store.ListIntegrations(ctx, store.Indexers)
	if err != nil {
		return nil, 0, err
	}
	clients, err := s.Store.ListIntegrations(ctx, store.DownloadClients)
	if err != nil {
		return nil, 0, err
	}
	var slsk, indexers []source
	for _, dc := range clients {
		if !dc.Enabled {
			continue
		}
		if a, ok := integrations.Searcher(dc.Kind, configOf(dc), s.HC); ok {
			slsk = append(slsk, source{dc.Name, a, true})
		}
	}
	for _, ix := range idx {
		if !ix.Enabled {
			continue
		}
		if a, err := integrations.NewIndexer(ix.Kind, configOf(ix), s.HC); err == nil {
			indexers = append(indexers, source{ix.Name, a, false})
		} else {
			s.Log.Warn("indexer skipped", "indexer", ix.Name, "err", err)
		}
	}
	if len(slsk)+len(indexers) == 0 {
		return nil, 0, ErrNoIndexers
	}
	want, err := s.want(ctx, al)
	if err != nil {
		return nil, 0, err
	}
	if which == SourcesAll {
		rs, n := s.ask(ctx, al, append(slsk, indexers...))
		return Evaluate(rs, want), n, nil
	}
	var all []integrations.Release
	searched := 0
	if len(slsk) > 0 {
		rs, n := s.ask(ctx, al, slsk)
		all, searched = rs, n
		cands := Evaluate(all, want)
		if len(cands) > 0 && cands[0].Rejected == "" {
			return cands, searched, nil
		}
	}
	if len(indexers) > 0 {
		rs, n := s.ask(ctx, al, indexers)
		all = append(all, rs...)
		searched += n
	}
	return Evaluate(all, want), searched, nil
}

func (s *Service) ask(ctx context.Context, al *store.Album, sources []source) ([]integrations.Release, int) {
	q := integrations.Query{MediaType: "music", Text: SearchText(al)}
	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		all      []integrations.Release
		searched int
	)
	for _, src := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			rs, err := src.a.Search(sctx, q)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				s.Log.Warn("music search failed", "source", src.name, "query", q.Text, "err", err)
				return
			}
			searched++
			s.Log.Debug("source answered", "source", src.name, "query", q.Text, "results", len(rs))
			for i := range rs {
				if rs[i].Indexer == "" {
					rs[i].Indexer = src.name
				}
			}
			all = append(all, rs...)
		}()
	}
	wg.Wait()
	s.Log.Info("searched", "query", q.Text, "sources", searched, "results", len(all))
	return all, searched
}

// want is what a search for al looks for: the library's quality list and,
// for an upgrade, the quality it must beat.
func (s *Service) want(ctx context.Context, al *store.Album) (Want, error) {
	blocked, err := s.Store.BlockedTitles(ctx, al.ID)
	if err != nil {
		return Want{}, err
	}
	w := Want{Artist: al.Artist, Album: al.Title, Tracks: al.Tracks, Blocklist: blocked, Order: s.qualities(ctx, al.LibraryID)}
	if al.Status == "upgrade_wanted" || al.Status == "imported" {
		w.Have = al.Quality
	}
	return w, nil
}

// qualities is the library's quality list, best first.
func (s *Service) qualities(ctx context.Context, libraryID int64) []string {
	lib, err := s.Store.GetLibrary(ctx, libraryID)
	if err != nil || lib.QualityPresetID == nil {
		return DefaultQualities
	}
	presets, err := s.Store.ListQualityPresets(ctx)
	if err != nil {
		return DefaultQualities
	}
	for _, p := range presets {
		if p.ID == *lib.QualityPresetID && p.MediaType == "music" && len(p.Qualities) > 0 {
			return p.Qualities
		}
	}
	return DefaultQualities
}

// configOf builds an adapter's settings, with the music category when a
// download is being added.
func configOf(i store.Integration, forDownload ...bool) integrations.Config {
	cat := i.Category
	if len(forDownload) > 0 && forDownload[0] {
		cat = i.CategoryFor("music")
	}
	return integrations.Config{BaseURL: i.BaseURL, APIKey: i.APIKey, Username: i.Username, Password: i.Password, Category: cat,
		RemotePath: i.RemotePath, LocalPath: i.LocalPath}
}

var errNoClient = errors.New("no download client is set up for this kind of release")

// Grab sends a release to the first enabled download client that handles
// its protocol and records the download.
func (s *Service) Grab(ctx context.Context, al *store.Album, c Candidate) error {
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
		a, err := integrations.NewDownloadClient(dc.Kind, configOf(dc, true), s.HC)
		if err != nil {
			return err
		}
		jobID, err := a.Add(ctx, c.Release)
		if err != nil {
			return fmt.Errorf("%s: %w", dc.Name, err)
		}
		id, err := s.Store.CreateDownload(ctx, store.Download{MediaItemID: al.ID, DownloadClientID: dc.ID, ClientJobID: jobID,
			ReleaseTitle: c.Title, Protocol: string(c.Protocol), Indexer: c.Indexer, SizeBytes: c.SizeBytes, Quality: c.Quality, Status: "queued"})
		if err != nil {
			return err
		}
		s.Store.SetAlbumState(ctx, al.ID, "downloading", "", time.Time{})
		s.Log.Info("grabbed", "release", c.Title, "client", dc.Name, "for", al.Artist+" - "+al.Title)
		s.publish("download", map[string]any{"id": id, "albumId": al.ID})
		s.publish("album", map[string]any{"id": al.ID, "artistId": al.ArtistID})
		return nil
	}
	return errNoClient
}
