package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ---- Movies ----
//
// A movie is a media_items row with media_type 'movie' and level 'work'.
// It has no children: its file, status and search state sit on the row.

// Movie is a movie Bragi tracks.
type Movie struct {
	ID          int64  `json:"id"`
	TMDBID      int64  `json:"tmdbId"`
	IMDBID      string `json:"imdbId,omitempty"`
	Title       string `json:"title"`
	Year        int    `json:"year,omitempty"`
	Overview    string `json:"overview"`
	PosterPath  string `json:"posterPath,omitempty"`
	Runtime     int    `json:"runtime,omitempty"`
	LibraryID   int64  `json:"libraryId"`
	Folder      string `json:"folder"`
	Monitored   bool   `json:"monitored"`
	Status      string `json:"status"`
	SearchNote  string `json:"searchNote,omitempty"`
	InCinemas   string `json:"inCinemas,omitempty"`
	Available   string `json:"available,omitempty"` // when Bragi starts searching
	FilePath    string `json:"filePath,omitempty"`
	Quality     string `json:"quality,omitempty"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
	Downloading int    `json:"downloading"` // downloads in progress
	CreatedAt   string `json:"createdAt"`
}

// IsReleased reports whether the movie can be searched for today.
func (m *Movie) IsReleased() bool { return m.Available != "" && m.Available <= Today() }

const movieCols = `w.id, COALESCE(w.tmdb_id, 0), COALESCE(w.imdb_id, ''), w.title, COALESCE(w.year, 0), w.overview, w.poster_path,
	w.runtime, COALESCE(w.library_id, 0), w.folder, w.monitored, w.status, w.search_note, COALESCE(w.in_cinemas, ''),
	COALESCE(w.air_date, ''),
	COALESCE((SELECT path FROM files f WHERE f.media_item_id = w.id ORDER BY f.id DESC LIMIT 1), ''),
	COALESCE((SELECT quality FROM files f WHERE f.media_item_id = w.id ORDER BY f.id DESC LIMIT 1), ''),
	COALESCE((SELECT size_bytes FROM files f WHERE f.media_item_id = w.id ORDER BY f.id DESC LIMIT 1), 0),
	(SELECT COUNT(*) FROM downloads d WHERE d.media_item_id = w.id AND d.status IN ('queued', 'downloading', 'completed', 'importing')),
	w.created_at`

func scanMovie(row interface{ Scan(...any) error }) (*Movie, error) {
	var m Movie
	if err := row.Scan(&m.ID, &m.TMDBID, &m.IMDBID, &m.Title, &m.Year, &m.Overview, &m.PosterPath, &m.Runtime, &m.LibraryID,
		&m.Folder, &m.Monitored, &m.Status, &m.SearchNote, &m.InCinemas, &m.Available, &m.FilePath, &m.Quality, &m.SizeBytes,
		&m.Downloading, &m.CreatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &m, nil
}

func (s *Store) queryMovies(ctx context.Context, where string, args ...any) ([]Movie, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+movieCols+` FROM media_items w
		WHERE w.media_type = 'movie' AND w.level = 'work' AND `+where+` ORDER BY w.title COLLATE NOCASE, w.year`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Movie{}
	for rows.Next() {
		m, err := scanMovie(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (s *Store) oneMovie(ctx context.Context, where string, args ...any) (*Movie, error) {
	ms, err := s.queryMovies(ctx, where, args...)
	if err != nil {
		return nil, err
	}
	if len(ms) == 0 {
		return nil, ErrNotFound
	}
	return &ms[0], nil
}

// ListMovies returns every tracked movie, by title.
func (s *Store) ListMovies(ctx context.Context) ([]Movie, error) {
	return s.queryMovies(ctx, `1 = 1`)
}

// GetMovie loads one movie.
func (s *Store) GetMovie(ctx context.Context, id int64) (*Movie, error) {
	return s.oneMovie(ctx, `w.id = ?`, id)
}

// GetMovieByTMDB loads a movie by its TMDB id.
func (s *Store) GetMovieByTMDB(ctx context.Context, tmdbID int64) (*Movie, error) {
	return s.oneMovie(ctx, `w.tmdb_id = ?`, tmdbID)
}

// DueMovies returns monitored, released movies without a file whose search
// time has come and that are not being downloaded.
func (s *Store) DueMovies(ctx context.Context) ([]Movie, error) {
	return s.queryMovies(ctx, `w.monitored = 1 AND w.status IN ('wanted', 'failed', 'searching')
		AND w.next_search_at IS NOT NULL AND w.next_search_at <= ? AND w.air_date IS NOT NULL AND w.air_date <= ?`, now(), Today())
}

// MovieInput is what UpsertMovie writes.
type MovieInput struct {
	TMDBID     int64
	IMDBID     string
	Title      string
	Year       int
	Overview   string
	PosterPath string
	Runtime    int
	InCinemas  string
	Available  string
	LibraryID  int64
	Folder     string
}

// UpsertMovie creates a movie or refreshes its metadata. A new movie is
// monitored (and queued for search) when monitor is true; an existing one
// keeps its status and monitored flag.
func (s *Store) UpsertMovie(ctx context.Context, in MovieInput, monitor bool) (int64, error) {
	ts := now()
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM media_items WHERE media_type = 'movie' AND level = 'work' AND tmdb_id = ?`, in.TMDBID).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		var next any
		if monitor {
			next = ts
		}
		res, err := s.DB.ExecContext(ctx, `INSERT INTO media_items (media_type, level, library_id, title, year, tmdb_id, imdb_id, overview,
			poster_path, runtime, folder, air_date, in_cinemas, monitored, status, next_search_at, refreshed_at)
			VALUES ('movie', 'work', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'wanted', ?, ?)`,
			nullable(in.LibraryID), in.Title, in.Year, in.TMDBID, orNull(in.IMDBID), in.Overview, in.PosterPath, in.Runtime, in.Folder,
			orNull(in.Available), orNull(in.InCinemas), monitor, next, ts)
		if err != nil {
			return 0, mapErr(err)
		}
		return res.LastInsertId()
	case err != nil:
		return 0, err
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE media_items SET title = ?, year = ?, imdb_id = ?, overview = ?, poster_path = ?, runtime = ?,
		air_date = ?, in_cinemas = ?, refreshed_at = ?, updated_at = ? WHERE id = ?`,
		in.Title, in.Year, orNull(in.IMDBID), in.Overview, in.PosterPath, in.Runtime, orNull(in.Available), orNull(in.InCinemas), ts, ts, id)
	return id, err
}

func orNull(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// MoviesNeedingRefresh returns ids of movies whose metadata is older than
// age and that have no file yet (a movie on disk rarely changes).
func (s *Store) MoviesNeedingRefresh(ctx context.Context, age time.Duration) ([]int64, error) {
	cutoff := time.Now().Add(-age).UTC().Format(timeFormat)
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM media_items WHERE media_type = 'movie' AND level = 'work'
		AND status != 'imported' AND (refreshed_at IS NULL OR refreshed_at < ?)`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeleteMovie forgets a movie. Files on disk are not touched.
func (s *Store) DeleteMovie(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM media_items WHERE id = ? AND media_type = 'movie' AND level = 'work'`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkMovieRequestsAvailable sets approved requests for a movie to
// available once it has a file.
func (s *Store) MarkMovieRequestsAvailable(ctx context.Context, movieID int64) (bool, error) {
	m, err := s.GetMovie(ctx, movieID)
	if err != nil {
		return false, err
	}
	if m.FilePath == "" {
		return false, nil
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE requests SET status = 'available', updated_at = ? WHERE media_item_id = ? AND status = 'approved'`, now(), movieID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
