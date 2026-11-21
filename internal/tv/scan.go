package tv

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Tharkis/bragi/internal/library"
	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/release"
	"github.com/Tharkis/bragi/internal/store"
)

// ScanStatus reports a library scan's progress.
type ScanStatus struct {
	Running   bool     `json:"running"`
	Library   string   `json:"library,omitempty"`
	Total     int      `json:"total"`
	Done      int      `json:"done"`
	Added     []string `json:"added"`
	Unmatched []string `json:"unmatched"`
	Files     int      `json:"files"`
	Error     string   `json:"error,omitempty"`
	Finished  string   `json:"finished,omitempty"`
}

// ScanState returns the current or last scan.
func (s *Service) ScanState() ScanStatus {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	st := s.scan
	st.Added = append([]string{}, st.Added...)
	st.Unmatched = append([]string{}, st.Unmatched...)
	return st
}

// ErrScanRunning means a scan is already going.
var ErrScanRunning = errors.New("a library scan is already running")

// StartScan adopts an existing TV folder in the background: each show
// folder is matched on TMDB by its name ("Title (Year)"), added to Bragi
// with new episodes monitored, and its episode files linked in place.
// Nothing on disk is moved or renamed.
func (s *Service) StartScan(ctx context.Context, libraryID int64) error {
	lib, err := s.tvLibrary(ctx, libraryID)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(lib.Path)
	if err != nil {
		return err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && e.Name()[0] != '.' {
			dirs = append(dirs, e.Name())
		}
	}
	s.scanMu.Lock()
	if s.scan.Running {
		s.scanMu.Unlock()
		return ErrScanRunning
	}
	s.scan = ScanStatus{Running: true, Library: lib.Path, Total: len(dirs), Added: []string{}, Unmatched: []string{}}
	s.scanMu.Unlock()

	go func() {
		// The scan outlives the HTTP request that started it.
		bg := context.WithoutCancel(ctx)
		for _, d := range dirs {
			added, files, err := s.adopt(bg, lib, d)
			s.scanMu.Lock()
			s.scan.Done++
			s.scan.Files += files
			switch {
			case err != nil:
				s.scan.Unmatched = append(s.scan.Unmatched, d+": "+err.Error())
			case added != "":
				s.scan.Added = append(s.scan.Added, added)
			}
			s.scanMu.Unlock()
			s.publish("scan", nil)
		}
		s.scanMu.Lock()
		s.scan.Running = false
		s.scan.Finished = time.Now().UTC().Format(time.RFC3339)
		s.scanMu.Unlock()
		s.publish("scan", nil)
		s.Kick()
	}()
	return nil
}

var errNoMatch = errors.New("no match on TMDB")

// adopt matches one folder and links its files. It returns the show's name
// if it was added and how many files were linked.
func (s *Service) adopt(ctx context.Context, lib *store.Library, folder string) (string, int, error) {
	info := release.Parse(folder)
	title := info.Title
	if title == "" {
		title = folder
	}
	tmdb := s.TMDB(ctx)
	results, err := tmdb.SearchTV(ctx, title, info.Year)
	if err != nil {
		return "", 0, err
	}
	var match *metadata.Show
	for i, r := range results {
		if release.Normalize(r.Title) == release.Normalize(title) && (info.Year == 0 || r.Year == info.Year) {
			match = &results[i]
			break
		}
	}
	if match == nil {
		return "", 0, errNoMatch
	}
	sr, err := s.Store.GetSeriesByTMDB(ctx, int64(match.TMDBID))
	if errors.Is(err, store.ErrNotFound) {
		show, err := tmdb.Series(ctx, match.TMDBID, true)
		if err != nil {
			return "", 0, err
		}
		in := toInput(show)
		in.LibraryID = lib.ID
		in.Folder = folder
		scope, _ := json.Marshal(ScopeFuture)
		mode, seasons, _ := ParseScope(scope)
		id, err := s.Store.UpsertSeries(ctx, in, monitorFunc(mode, seasons, show))
		if err != nil {
			return "", 0, err
		}
		if sr, err = s.Store.GetSeries(ctx, id); err != nil {
			return "", 0, err
		}
	} else if err != nil {
		return "", 0, err
	} else if sr.Folder != folder || sr.LibraryID != lib.ID {
		return "", 0, errors.New("already tracked in another folder")
	}

	eps, err := s.Store.EpisodesBySeries(ctx, sr.ID)
	if err != nil {
		return "", 0, err
	}
	files := library.VideoFiles(filepath.Join(lib.Path, folder))
	sort.Strings(files)
	linked := 0
	for _, m := range matchFiles(files, eps, eps) {
		st, err := os.Stat(m.path)
		if err != nil {
			continue
		}
		q := release.Parse(filepath.Base(m.path)).Quality()
		for _, e := range m.eps {
			if e.FilePath == m.path {
				continue
			}
			if err := s.Store.AddFile(ctx, e.ID, m.path, st.Size(), q, ""); err == nil {
				linked++
			}
		}
	}
	name := sr.Title
	if sr.Year > 0 {
		name += " (" + itoa(sr.Year) + ")"
	}
	return name, linked, nil
}
