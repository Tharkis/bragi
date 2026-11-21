package movies

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Tharkis/bragi/internal/library"
	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/release"
	"github.com/Tharkis/bragi/internal/store"
)

// ScanStatus reports a library scan's progress. It has the same shape as
// the TV scan's, so the UI shows both the same way.
type ScanStatus struct {
	Running   bool     `json:"running"`
	Library   string   `json:"library,omitempty"`
	MediaType string   `json:"mediaType"`
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
	st.MediaType = "movie"
	st.Added = append([]string{}, st.Added...)
	st.Unmatched = append([]string{}, st.Unmatched...)
	return st
}

// ErrScanRunning means a scan is already going.
var ErrScanRunning = errors.New("a library scan is already running")

// StartScan adopts an existing movie folder in the background: each
// "Title (Year)" folder is matched on TMDB, added to Bragi, and its movie
// file linked in place. Nothing on disk is moved or renamed, and nothing is
// downloaded: folders without a video file are added unmonitored.
func (s *Service) StartScan(ctx context.Context, libraryID int64) error {
	lib, err := s.movieLibrary(ctx, libraryID)
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
		s.publish("movie", nil)
	}()
	return nil
}

var errNoMatch = errors.New("no match on TMDB")

// adopt matches one folder and links its movie file. It returns the
// movie's name if it was added, and how many files were linked.
func (s *Service) adopt(ctx context.Context, lib *store.Library, folder string) (string, int, error) {
	info := release.Parse(folder)
	title := info.Title
	if title == "" {
		title = folder
	}
	tmdb := s.TMDB(ctx)
	results, err := tmdb.SearchMovies(ctx, title, info.Year)
	if err != nil {
		return "", 0, err
	}
	var match *metadata.Movie
	for i, r := range results {
		if release.Normalize(r.Title) == release.Normalize(title) && (info.Year == 0 || r.Year == info.Year) {
			match = &results[i]
			break
		}
	}
	if match == nil {
		return "", 0, errNoMatch
	}
	m, err := s.Store.GetMovieByTMDB(ctx, int64(match.TMDBID))
	if errors.Is(err, store.ErrNotFound) {
		md, err := tmdb.Movie(ctx, match.TMDBID)
		if err != nil {
			return "", 0, err
		}
		in := toInput(md)
		in.LibraryID = lib.ID
		in.Folder = folder
		id, err := s.Store.UpsertMovie(ctx, in, false)
		if err != nil {
			return "", 0, err
		}
		if m, err = s.Store.GetMovie(ctx, id); err != nil {
			return "", 0, err
		}
	} else if err != nil {
		return "", 0, err
	} else if m.Folder != folder || m.LibraryID != lib.ID {
		return "", 0, errors.New("already tracked in another folder")
	}

	name := label(m)
	file := mainVideo(library.VideoFiles(filepath.Join(lib.Path, folder)))
	if file == "" || file == m.FilePath {
		return name, 0, nil
	}
	st, err := os.Stat(file)
	if err != nil {
		return name, 0, nil
	}
	q := release.Parse(filepath.Base(file)).Quality()
	if err := s.Store.AddFile(ctx, m.ID, file, st.Size(), q, ""); err != nil {
		return "", 0, fmt.Errorf("could not link %s: %w", filepath.Base(file), err)
	}
	return name, 1, nil
}
