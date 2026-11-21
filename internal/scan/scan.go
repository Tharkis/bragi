// Package scan adopts media already on disk: it walks each library, asks the
// media type's pipeline to match every folder or file it finds, and keeps
// what it could not match so it can be matched by hand. Nothing on disk is
// moved, renamed or downloaded.
package scan

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Tharkis/bragi/internal/events"
	"github.com/Tharkis/bragi/internal/store"
)

// Adopter is what each media type's pipeline provides.
type Adopter interface {
	// Units lists what a scan looks at in a library, as paths relative to
	// its root: show or movie folders, ebook files, audiobook folders.
	Units(ctx context.Context, lib *store.Library) ([]string, error)
	// Adopt matches one unit and links its files. With foreignID set (a
	// TMDB id or an Open Library work) it skips matching and uses that.
	// It returns the item's name and how many files it newly linked.
	Adopt(ctx context.Context, lib *store.Library, unit, foreignID string) (Result, error)
}

// Result is what adopting one unit did.
type Result struct {
	Name  string
	Files int
	// New is true when the item was not tracked before.
	New bool
}

// NoMatchError means the unit could not be matched. Guess is what was read
// from it, to prefill a search.
type NoMatchError struct {
	Reason string
	Guess  string
}

func (e *NoMatchError) Error() string { return e.Reason }

// Status reports a library scan's progress. One is kept per media type.
type Status struct {
	Running   bool     `json:"running"`
	Queued    bool     `json:"queued,omitempty"`
	Library   string   `json:"library,omitempty"`
	MediaType string   `json:"mediaType"`
	Total     int      `json:"total"`
	Done      int      `json:"done"`
	Added     []string `json:"added"`
	Unmatched []string `json:"unmatched"`
	Files     int      `json:"files"`
	Error     string   `json:"error,omitempty"`
	Finished  string   `json:"finished,omitempty"`
	Nightly   bool     `json:"nightly,omitempty"`
}

// ErrRunning means that library is already being scanned or waiting to be.
var ErrRunning = errors.New("a library scan is already running")

// ErrNoLibrary means there is no library of that type to scan.
var ErrNoLibrary = errors.New("there is no library of that type to scan")

// Settings the manager reads.
const (
	SettingNightly  = "nightly_scan"   // "false" turns the nightly scan off
	SettingLastFull = "last_full_scan" // RFC 3339 time of the last scan of every library
)

// NightlyHour is the local hour the nightly scan starts.
var NightlyHour = 3

type job struct {
	lib         store.Library
	incremental bool
}

// Manager runs scans one library at a time.
type Manager struct {
	Store  *store.Store
	Log    *slog.Logger
	Events *events.Hub
	// Nightly turns on the nightly scan of every library.
	Nightly bool
	// Pace is a pause between units, so a big first scan does not hammer
	// the metadata services. Zero in tests.
	Pace time.Duration

	adopters map[string]Adopter
	mu       sync.Mutex
	status   map[string]*Status
	pending  map[int64]bool
	queue    []job
	wake     chan struct{}
	// After is called once a library's scan ends (the pipelines use it to
	// search for what was added and refresh lists).
	After func(mediaType string)
}

// New returns a Manager. Call Run to start it.
func New(st *store.Store, log *slog.Logger, hub *events.Hub) *Manager {
	if log == nil {
		log = slog.Default()
	}
	if hub == nil {
		hub = events.NewHub()
	}
	return &Manager{Store: st, Log: log, Events: hub, adopters: map[string]Adopter{}, status: map[string]*Status{},
		pending: map[int64]bool{}, wake: make(chan struct{}, 1)}
}

// Register sets the adopter for a media type.
func (m *Manager) Register(mediaType string, a Adopter) { m.adopters[mediaType] = a }

// Adopter returns the adopter of a media type.
func (m *Manager) Adopter(mediaType string) Adopter { return m.adopters[mediaType] }

// State returns the current or last scan of a media type.
func (m *Manager) State(mediaType string) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{MediaType: mediaType, Added: []string{}, Unmatched: []string{}}
	if s := m.status[mediaType]; s != nil {
		st = *s
		st.Added = append([]string{}, s.Added...)
		st.Unmatched = append([]string{}, s.Unmatched...)
	}
	for _, j := range m.queue {
		if j.lib.MediaType == mediaType && !st.Running {
			st.Queued = true
		}
	}
	return st
}

// Busy reports whether any scan is running or waiting.
func (m *Manager) Busy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending) > 0
}

func (m *Manager) publish() { m.Events.Publish(events.Event{Type: "scan"}) }

// Start queues a scan of one library: the given one, or the first of the
// media type when libraryID is 0. A scan started by hand also retries what
// earlier scans could not match.
func (m *Manager) Start(ctx context.Context, mediaType string, libraryID int64) (Status, error) {
	var lib *store.Library
	if libraryID != 0 {
		l, err := m.Store.GetLibrary(ctx, libraryID)
		if err != nil {
			return Status{}, err
		}
		lib = l
	} else {
		libs, err := m.Store.ListLibraries(ctx)
		if err != nil {
			return Status{}, err
		}
		for _, l := range libs {
			if l.MediaType == mediaType {
				lib = &l
				break
			}
		}
	}
	if lib == nil {
		return Status{}, ErrNoLibrary
	}
	if m.adopters[lib.MediaType] == nil {
		return Status{}, fmt.Errorf("Bragi cannot scan %s libraries yet", lib.MediaType)
	}
	if !m.enqueue(job{lib: *lib}) {
		return m.State(lib.MediaType), ErrRunning
	}
	return m.State(lib.MediaType), nil
}

// StartAll queues a scan of every library. incremental (the nightly scan)
// skips what earlier scans could not match.
func (m *Manager) StartAll(ctx context.Context, incremental bool) (int, error) {
	libs, err := m.Store.ListLibraries(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, l := range libs {
		if m.adopters[l.MediaType] != nil && m.enqueue(job{lib: l, incremental: incremental}) {
			n++
		}
	}
	m.Store.SetSetting(ctx, SettingLastFull, time.Now().UTC().Format(time.RFC3339))
	return n, nil
}

func (m *Manager) enqueue(j job) bool {
	m.mu.Lock()
	if m.pending[j.lib.ID] {
		m.mu.Unlock()
		return false
	}
	m.pending[j.lib.ID] = true
	m.queue = append(m.queue, j)
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
	m.publish()
	return true
}

// Run works through queued scans and starts the nightly scan, until ctx
// ends.
func (m *Manager) Run(ctx context.Context) {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		for m.next(ctx) {
		}
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		case now := <-tick.C:
			m.maybeNightly(ctx, now)
		}
	}
}

// maybeNightly starts the nightly scan once a day, from NightlyHour local
// time, unless it is turned off.
func (m *Manager) maybeNightly(ctx context.Context, now time.Time) {
	if !m.Nightly || now.Hour() < NightlyHour {
		return
	}
	if v, _ := m.Store.GetSetting(ctx, SettingNightly); v == "false" {
		return
	}
	last, _ := m.Store.GetSetting(ctx, SettingLastFull)
	if t, err := time.Parse(time.RFC3339, last); err == nil {
		t = t.In(now.Location())
		today := time.Date(now.Year(), now.Month(), now.Day(), NightlyHour, 0, 0, 0, now.Location())
		if !t.Before(today) {
			return
		}
	}
	n, err := m.StartAll(ctx, true)
	if err != nil {
		m.Log.Error("nightly library scan failed to start", "err", err)
		return
	}
	m.Log.Info("nightly library scan started", "libraries", n)
}

// next runs the next queued scan, if any.
func (m *Manager) next(ctx context.Context) bool {
	m.mu.Lock()
	if len(m.queue) == 0 {
		m.mu.Unlock()
		return false
	}
	j := m.queue[0]
	m.queue = m.queue[1:]
	m.mu.Unlock()
	m.runJob(ctx, j)
	m.mu.Lock()
	delete(m.pending, j.lib.ID)
	m.mu.Unlock()
	m.publish()
	if m.After != nil {
		m.After(j.lib.MediaType)
	}
	return ctx.Err() == nil
}

func (m *Manager) runJob(ctx context.Context, j job) {
	lib := j.lib
	a := m.adopters[lib.MediaType]
	st := &Status{Running: true, Library: lib.Path, MediaType: lib.MediaType, Added: []string{}, Unmatched: []string{}, Nightly: j.incremental}
	m.mu.Lock()
	m.status[lib.MediaType] = st
	m.mu.Unlock()
	finish := func(err error) {
		m.mu.Lock()
		st.Running = false
		st.Finished = time.Now().UTC().Format(time.RFC3339)
		if err != nil {
			st.Error = err.Error()
		}
		m.mu.Unlock()
	}

	units, err := a.Units(ctx, &lib)
	if err != nil {
		m.Log.Error("library scan failed", "library", lib.Path, "err", err)
		finish(err)
		return
	}
	known, err := m.Store.UnmatchedPaths(ctx, lib.ID)
	if err != nil {
		finish(err)
		return
	}
	m.mu.Lock()
	st.Total = len(units)
	m.mu.Unlock()
	m.publish()
	m.Log.Info("library scan started", "library", lib.Path, "type", lib.MediaType, "items", len(units), "nightly", j.incremental)

	seen := map[string]bool{}
	for _, u := range units {
		if ctx.Err() != nil {
			finish(ctx.Err())
			return
		}
		seen[u] = true
		ignored, wasUnmatched := known[u]
		if ignored || (j.incremental && wasUnmatched) {
			m.step(st, func() {})
			continue
		}
		res, err := a.Adopt(ctx, &lib, u, "")
		var nm *NoMatchError
		switch {
		case errors.As(err, &nm):
			m.Store.SetUnmatched(ctx, lib.ID, u, nm.Reason, nm.Guess)
			m.step(st, func() { st.Unmatched = append(st.Unmatched, u+": "+nm.Reason) })
		case err != nil:
			// A metadata service being down is not the folder's fault: keep
			// it out of the unmatched list so the next scan tries again.
			m.Log.Warn("could not adopt", "path", u, "err", err)
			m.step(st, func() { st.Unmatched = append(st.Unmatched, u+": "+err.Error()) })
		default:
			if wasUnmatched {
				m.Store.ClearUnmatched(ctx, lib.ID, u)
			}
			m.step(st, func() {
				st.Files += res.Files
				if res.New {
					st.Added = append(st.Added, res.Name)
				}
			})
		}
		if m.Pace > 0 && !(ignored || (j.incremental && wasUnmatched)) {
			select {
			case <-ctx.Done():
			case <-time.After(m.Pace):
			}
		}
	}
	m.Store.PruneUnmatched(ctx, lib.ID, seen)
	finish(nil)
	m.Log.Info("library scan finished", "library", lib.Path, "added", len(st.Added), "files", st.Files, "unmatched", len(st.Unmatched))
}

func (m *Manager) step(st *Status, f func()) {
	m.mu.Lock()
	st.Done++
	f()
	m.mu.Unlock()
	m.publish()
}

// Match adopts an unmatched item as the given TMDB id or Open Library work.
func (m *Manager) Match(ctx context.Context, unmatchedID int64, foreignID string) (Result, error) {
	u, err := m.Store.GetUnmatched(ctx, unmatchedID)
	if err != nil {
		return Result{}, err
	}
	lib, err := m.Store.GetLibrary(ctx, u.LibraryID)
	if err != nil {
		return Result{}, err
	}
	a := m.adopters[lib.MediaType]
	if a == nil {
		return Result{}, fmt.Errorf("Bragi cannot match %s items", lib.MediaType)
	}
	res, err := a.Adopt(ctx, lib, u.Path, foreignID)
	if err != nil {
		return res, err
	}
	m.Store.ClearUnmatched(ctx, lib.ID, u.Path)
	m.publish()
	if m.After != nil {
		m.After(lib.MediaType)
	}
	return res, nil
}
