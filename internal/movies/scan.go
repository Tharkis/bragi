package movies

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/Tharkis/bragi/internal/library"
	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/release"
	"github.com/Tharkis/bragi/internal/scan"
	"github.com/Tharkis/bragi/internal/store"
)

// Units lists the movie folders at the top of a movie library.
func (s *Service) Units(ctx context.Context, lib *store.Library) ([]string, error) {
	return library.Folders(lib.Path)
}

// Adopt matches a "Title (Year)" folder on TMDB, or uses the TMDB id given,
// adds the movie and links its movie file in place. A folder without a
// video file is added unmonitored, like the rest: nothing is downloaded.
func (s *Service) Adopt(ctx context.Context, lib *store.Library, folder, tmdbID string) (scan.Result, error) {
	m, err := s.Store.MovieInFolder(ctx, lib.ID, folder)
	isNew := false
	switch {
	case err == nil && (tmdbID == "" || strconv.FormatInt(m.TMDBID, 10) == tmdbID):
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return scan.Result{}, err
	default:
		id, err := s.matchFolder(ctx, folder, tmdbID)
		if err != nil {
			return scan.Result{}, err
		}
		m, err = s.Store.GetMovieByTMDB(ctx, int64(id))
		switch {
		case errors.Is(err, store.ErrNotFound):
			md, err := s.TMDB(ctx).Movie(ctx, id)
			if err != nil {
				return scan.Result{}, err
			}
			in := toInput(md)
			in.LibraryID = lib.ID
			in.Folder = folder
			mid, err := s.Store.UpsertMovie(ctx, in, false)
			if err != nil {
				return scan.Result{}, err
			}
			if m, err = s.Store.GetMovie(ctx, mid); err != nil {
				return scan.Result{}, err
			}
			isNew = true
		case err != nil:
			return scan.Result{}, err
		case m.Folder != folder || m.LibraryID != lib.ID:
			return scan.Result{}, &scan.NoMatchError{Reason: label(m) + " is already in Bragi in another folder", Guess: folderGuess(folder)}
		}
	}

	res := scan.Result{Name: label(m), New: isNew}
	defer func() {
		if res.New || res.Files > 0 {
			s.publish("movie", map[string]any{"id": m.ID})
		}
	}()
	file := mainVideo(library.VideoFiles(filepath.Join(lib.Path, folder)))
	if file == "" || file == m.FilePath {
		return res, nil
	}
	st, err := os.Stat(file)
	if err != nil {
		return res, nil
	}
	q := release.Parse(filepath.Base(file)).Quality()
	if err := s.Store.AddFile(ctx, m.ID, file, st.Size(), q, ""); err != nil {
		return scan.Result{}, fmt.Errorf("could not link %s: %w", filepath.Base(file), err)
	}
	res.Files = 1
	return res, nil
}

func folderGuess(folder string) string {
	info := release.Parse(folder)
	if info.Title == "" {
		return folder
	}
	if info.Year > 0 {
		return fmt.Sprintf("%s %d", info.Title, info.Year)
	}
	return info.Title
}

// matchFolder returns the TMDB id of a movie folder: the one given, or the
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
	results, err := s.TMDB(ctx).SearchMovies(ctx, title, info.Year)
	if err != nil {
		return 0, err
	}
	var match *metadata.Movie
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
