package tv

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/Tharkis/bragi/internal/library"
	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/release"
	"github.com/Tharkis/bragi/internal/scan"
	"github.com/Tharkis/bragi/internal/store"
)

// Units lists the show folders at the top of a TV library.
func (s *Service) Units(ctx context.Context, lib *store.Library) ([]string, error) {
	return library.Folders(lib.Path)
}

// Adopt matches a show folder ("Title (Year)") on TMDB, or uses the TMDB id
// given, adds the show with new episodes monitored, and links its episode
// files in place. A folder already tracked only gets new files linked.
func (s *Service) Adopt(ctx context.Context, lib *store.Library, folder, tmdbID string) (scan.Result, error) {
	sr, err := s.Store.SeriesInFolder(ctx, lib.ID, folder)
	isNew := false
	switch {
	case err == nil && (tmdbID == "" || strconv.FormatInt(sr.TMDBID, 10) == tmdbID):
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return scan.Result{}, err
	default:
		id, err := s.matchFolder(ctx, folder, tmdbID)
		if err != nil {
			return scan.Result{}, err
		}
		sr, err = s.Store.GetSeriesByTMDB(ctx, int64(id))
		switch {
		case errors.Is(err, store.ErrNotFound):
			show, err := s.TMDB(ctx).Series(ctx, id, true)
			if err != nil {
				return scan.Result{}, err
			}
			in := toInput(show)
			in.LibraryID = lib.ID
			in.Folder = folder
			scope, _ := json.Marshal(ScopeFuture)
			mode, seasons, _ := ParseScope(scope)
			sid, err := s.Store.UpsertSeries(ctx, in, monitorFunc(mode, seasons, show))
			if err != nil {
				return scan.Result{}, err
			}
			if sr, err = s.Store.GetSeries(ctx, sid); err != nil {
				return scan.Result{}, err
			}
			isNew = true
		case err != nil:
			return scan.Result{}, err
		case sr.Folder != folder || sr.LibraryID != lib.ID:
			return scan.Result{}, &scan.NoMatchError{Reason: sr.Title + " is already in Bragi in another folder", Guess: folderGuess(folder)}
		}
	}

	eps, err := s.Store.EpisodesBySeries(ctx, sr.ID)
	if err != nil {
		return scan.Result{}, err
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
	if linked > 0 || isNew {
		s.publish("series", map[string]any{"id": sr.ID})
	}
	name := sr.Title
	if sr.Year > 0 {
		name += " (" + itoa(sr.Year) + ")"
	}
	return scan.Result{Name: name, Files: linked, New: isNew}, nil
}

func folderGuess(folder string) string {
	info := release.Parse(folder)
	if info.Title == "" {
		return folder
	}
	if info.Year > 0 {
		return info.Title + " " + itoa(info.Year)
	}
	return info.Title
}

// matchFolder returns the TMDB id of a show folder: the one given, or the
// search result whose title (and year, if the folder has one) matches.
func (s *Service) matchFolder(ctx context.Context, folder, tmdbID string) (int, error) {
	if tmdbID != "" {
		id, err := strconv.Atoi(tmdbID)
		if err != nil || id <= 0 {
			return 0, errors.New("that is not a TMDB id")
		}
		return id, nil
	}
	info := release.Parse(folder)
	title := info.Title
	if title == "" {
		title = folder
	}
	results, err := s.TMDB(ctx).SearchTV(ctx, title, info.Year)
	if err != nil {
		return 0, err
	}
	var match *metadata.Show
	for i, r := range results {
		if release.Normalize(r.Title) == release.Normalize(title) && (info.Year == 0 || r.Year == info.Year) {
			match = &results[i]
			break
		}
	}
	if match == nil {
		return 0, &scan.NoMatchError{Reason: "no match on TMDB", Guess: folderGuess(folder)}
	}
	return match.TMDBID, nil
}
