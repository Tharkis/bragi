// Package books runs the ebook and audiobook pipeline: add a book from Open
// Library, search indexers and Soulseek for it, send the best release to a
// download client, track it, and import the files into the library.
package books

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Tharkis/bragi/internal/events"
	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/library"
	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/store"
)

// Timing of the scheduler. Variables so tests can shorten them.
var (
	PollInterval    = 30 * time.Second
	RetryAfter      = 12 * time.Hour // books rarely appear overnight
	NoIndexerRetry  = 1 * time.Hour
	searchesPerPass = 10
)

// ErrNoIndexers means neither an indexer nor slskd is set up.
var ErrNoIndexers = errors.New("no indexer or Soulseek client is set up")

// NoLibraryError means no library of the media type is set up.
type NoLibraryError struct{ MediaType string }

func (e NoLibraryError) Error() string {
	return fmt.Sprintf("add %s library under Settings, Libraries first", map[string]string{"book": "a Books", "audiobook": "an Audiobooks"}[e.MediaType])
}

// Service holds the pipeline's dependencies.
type Service struct {
	Store  *store.Store
	HC     *http.Client
	Log    *slog.Logger
	Events *events.Hub
	// OnImported runs after a book is imported, to tell the media server.
	OnImported func(ctx context.Context) error

	kick     chan struct{}
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
	if hub == nil {
		hub = events.NewHub()
	}
	return &Service{Store: st, HC: hc, Log: log, Events: hub, kick: make(chan struct{}, 1)}
}

// OpenLibrary returns the metadata client.
func (s *Service) OpenLibrary() *metadata.OpenLibrary { return &metadata.OpenLibrary{HC: s.HC} }

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

// ---- Adding books ----

// bookFolder is "Author/Title (Year)", the layout Audiobookshelf, Calibre-
// style tools and Jellyfin all read.
func bookFolder(author, title string, year int) string {
	a := library.CleanName(author)
	if a == "" {
		a = "Unknown Author"
	}
	t := library.CleanName(title)
	if year > 0 {
		t = fmt.Sprintf("%s (%d)", t, year)
	}
	return a + "/" + t
}

func (s *Service) library(ctx context.Context, mediaType string, id int64) (*store.Library, error) {
	if id != 0 {
		l, err := s.Store.GetLibrary(ctx, id)
		if err != nil {
			return nil, err
		}
		if l.MediaType != mediaType {
			return nil, fmt.Errorf("that library is not for %ss", mediaType)
		}
		return l, nil
	}
	libs, err := s.Store.ListLibraries(ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range libs {
		if l.MediaType == mediaType {
			return &l, nil
		}
	}
	return nil, NoLibraryError{mediaType}
}

// AddBook starts tracking a book (mediaType "book" or "audiobook") and
// queues a search for it. A book already tracked is monitored again.
func (s *Service) AddBook(ctx context.Context, mediaType, workID string, libraryID int64) (*store.Book, error) {
	if !store.IsBookType(mediaType) {
		return nil, fmt.Errorf("unknown book type %q", mediaType)
	}
	if b, err := s.Store.GetBookByForeignID(ctx, mediaType, workID); err == nil {
		if _, err := s.Store.AddBook(ctx, store.BookInput{MediaType: mediaType, ForeignID: workID}); err != nil {
			return nil, err
		}
		s.Kick()
		return s.Store.GetBook(ctx, b.ID)
	}
	lib, err := s.library(ctx, mediaType, libraryID)
	if err != nil {
		return nil, err
	}
	w, err := s.OpenLibrary().Work(ctx, workID)
	if err != nil {
		return nil, err
	}
	id, err := s.Store.AddBook(ctx, store.BookInput{MediaType: mediaType, ForeignID: w.ID, Title: w.Title, Author: w.Author,
		Year: w.Year, Overview: w.Overview, CoverID: metadata.CoverIDString(w.CoverID), LibraryID: lib.ID,
		Folder: bookFolder(w.Author, w.Title, w.Year)})
	if err != nil {
		return nil, err
	}
	s.Log.Info("book added", "type", mediaType, "title", w.Title, "author", w.Author)
	s.publish("book", map[string]any{"id": id})
	s.Kick()
	return s.Store.GetBook(ctx, id)
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

// Pass runs one round of download polling and searching.
func (s *Service) Pass(ctx context.Context) {
	if err := s.PollDownloads(ctx); err != nil && ctx.Err() == nil {
		s.Log.Error("polling book downloads failed", "err", err)
	}
	if err := s.SearchDue(ctx); err != nil && ctx.Err() == nil {
		s.Log.Error("searching for books failed", "err", err)
	}
}

// SearchDue searches for every book whose search time has come.
func (s *Service) SearchDue(ctx context.Context) error {
	due, err := s.Store.DueBooks(ctx)
	if err != nil {
		return err
	}
	for i, b := range due {
		if i >= searchesPerPass || ctx.Err() != nil {
			break
		}
		s.searchAndGrab(ctx, &b)
	}
	return nil
}

// SearchNow queues an immediate automatic search for a book.
func (s *Service) SearchNow(ctx context.Context, id int64) error {
	b, err := s.Store.GetBook(ctx, id)
	if err != nil {
		return err
	}
	if b.Status == "downloading" {
		return errors.New("it is downloading already")
	}
	if !b.Monitored {
		s.Store.SetBookMonitored(ctx, id, true)
	}
	if err := s.Store.SetBookState(ctx, id, "wanted", "", time.Now()); err != nil {
		return err
	}
	s.Kick()
	return nil
}

func (s *Service) searchAndGrab(ctx context.Context, b *store.Book) {
	keep := b.Status
	if keep != "upgrade_wanted" {
		keep = "wanted"
	}
	s.Store.SetBookState(ctx, b.ID, "searching", "", time.Now().Add(10*time.Minute))
	s.publish("book", map[string]any{"id": b.ID})

	cands, searched, err := s.Search(ctx, b)
	if err != nil {
		note, retry := "Search failed: "+err.Error(), RetryAfter
		if errors.Is(err, ErrNoIndexers) {
			note, retry = "No indexer or Soulseek client is set up", NoIndexerRetry
		}
		s.Store.SetBookState(ctx, b.ID, keep, note, time.Now().Add(retry))
		s.publish("book", map[string]any{"id": b.ID})
		return
	}
	for _, c := range cands {
		if c.Rejected != "" {
			break
		}
		err := s.Grab(ctx, b, c)
		if err == nil {
			return
		}
		s.Log.Warn("grab failed, trying the next release", "release", c.Title, "err", err)
		if errors.Is(err, errNoClient) {
			continue
		}
		s.Store.Blocklist(ctx, b.ID, c.Title, err.Error())
	}
	note := "No releases found"
	switch {
	case searched == 0:
		note = "Every indexer failed to answer"
	case len(cands) > 0:
		note = fmt.Sprintf("Nothing suitable in %d results (%s)", len(cands), topReasons(cands))
	}
	s.Store.SetBookState(ctx, b.ID, keep, note, time.Now().Add(RetryAfter))
	s.publish("book", map[string]any{"id": b.ID})
}

func topReasons(cands []Candidate) string {
	counts := map[string]int{}
	var order []string
	for _, c := range cands {
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

// SearchText is what Bragi types into indexers for a book: the author and
// the title without its subtitle.
func SearchText(b *store.Book) string {
	return strings.TrimSpace(b.Author + " " + mainTitle(b.Title))
}

// Search asks every enabled indexer and every slskd client, and returns the
// releases ranked for b. searched counts the sources that answered.
func (s *Service) Search(ctx context.Context, b *store.Book) ([]Candidate, int, error) {
	idx, err := s.Store.ListIntegrations(ctx, store.Indexers)
	if err != nil {
		return nil, 0, err
	}
	clients, err := s.Store.ListIntegrations(ctx, store.DownloadClients)
	if err != nil {
		return nil, 0, err
	}
	type source struct {
		name string
		a    integrations.Indexer
	}
	var sources []source
	for _, ix := range idx {
		if !ix.Enabled {
			continue
		}
		if a, err := integrations.NewIndexer(ix.Kind, configOf(ix), s.HC); err == nil {
			sources = append(sources, source{ix.Name, a})
		}
	}
	for _, dc := range clients {
		if !dc.Enabled {
			continue
		}
		if a, ok := integrations.Searcher(dc.Kind, configOf(dc), s.HC); ok {
			sources = append(sources, source{dc.Name, a})
		}
	}
	if len(sources) == 0 {
		return nil, 0, ErrNoIndexers
	}
	q := integrations.Query{MediaType: b.MediaType, Text: SearchText(b)}
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
				s.Log.Warn("book search failed", "source", src.name, "query", q.Text, "err", err)
				return
			}
			searched++
			for i := range rs {
				if rs[i].Indexer == "" {
					rs[i].Indexer = src.name
				}
			}
			all = append(all, rs...)
		}()
	}
	wg.Wait()
	blocked, err := s.Store.BlockedTitles(ctx, b.ID)
	if err != nil {
		return nil, searched, err
	}
	cands := Evaluate(all, Want{MediaType: b.MediaType, Title: b.Title, Author: b.Author, Blocklist: blocked})
	s.Log.Info("searched", "query", q.Text, "type", b.MediaType, "sources", searched, "results", len(all))
	return cands, searched, nil
}

func configOf(i store.Integration) integrations.Config {
	return integrations.Config{BaseURL: i.BaseURL, APIKey: i.APIKey, Username: i.Username, Password: i.Password, Category: i.Category}
}

var errNoClient = errors.New("no download client is set up for this kind of release")

// Grab sends a release to the first enabled download client that handles
// its protocol and records the download.
func (s *Service) Grab(ctx context.Context, b *store.Book, c Candidate) error {
	clients, err := s.Store.ListIntegrations(ctx, store.DownloadClients)
	if err != nil {
		return err
	}
	for _, dc := range clients {
		if !dc.Enabled {
			continue
		}
		kind, ok := integrations.KindByID(dc.Kind)
		if !ok || !hasProtocol(kind.Protocols, c.Protocol) {
			continue
		}
		a, err := integrations.NewDownloadClient(dc.Kind, configOf(dc), s.HC)
		if err != nil {
			return err
		}
		jobID, err := a.Add(ctx, c.Release)
		if err != nil {
			return fmt.Errorf("%s: %w", dc.Name, err)
		}
		id, err := s.Store.CreateDownload(ctx, store.Download{MediaItemID: b.ID, DownloadClientID: dc.ID, ClientJobID: jobID,
			ReleaseTitle: c.Title, Protocol: string(c.Protocol), Indexer: c.Indexer, SizeBytes: c.SizeBytes, Quality: c.Format, Status: "queued"})
		if err != nil {
			return err
		}
		s.Store.SetBookState(ctx, b.ID, "downloading", "", time.Time{})
		s.Log.Info("grabbed", "release", c.Title, "client", dc.Name, "for", b.Title)
		s.publish("download", map[string]any{"id": id, "bookId": b.ID})
		s.publish("book", map[string]any{"id": b.ID})
		return nil
	}
	return errNoClient
}

func hasProtocol(ps []integrations.Protocol, p integrations.Protocol) bool {
	for _, x := range ps {
		if x == p {
			return true
		}
	}
	return false
}
