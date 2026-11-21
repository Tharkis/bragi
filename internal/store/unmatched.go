package store

import (
	"context"
	"database/sql"
	"errors"
)

// Unmatched is a folder or file a library scan could not match.
type Unmatched struct {
	ID        int64  `json:"id"`
	LibraryID int64  `json:"libraryId"`
	MediaType string `json:"type"`
	Library   string `json:"library"`
	Path      string `json:"path"`
	Reason    string `json:"reason"`
	Guess     string `json:"guess"`
	Ignored   bool   `json:"ignored"`
	UpdatedAt string `json:"updatedAt"`
}

const unmatchedCols = `u.id, u.library_id, l.media_type, l.path, u.path, u.reason, u.guess, u.ignored, u.updated_at`

func scanUnmatched(row interface{ Scan(...any) error }) (*Unmatched, error) {
	var u Unmatched
	if err := row.Scan(&u.ID, &u.LibraryID, &u.MediaType, &u.Library, &u.Path, &u.Reason, &u.Guess, &u.Ignored, &u.UpdatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &u, nil
}

// ListUnmatched returns the unmatched items of a media type ("" for all),
// leaving out ignored ones unless asked.
func (s *Store) ListUnmatched(ctx context.Context, mediaType string, withIgnored bool) ([]Unmatched, error) {
	q := `SELECT ` + unmatchedCols + ` FROM unmatched u JOIN libraries l ON l.id = u.library_id WHERE 1 = 1`
	var args []any
	if mediaType != "" {
		q += ` AND l.media_type = ?`
		args = append(args, mediaType)
	}
	if !withIgnored {
		q += ` AND u.ignored = 0`
	}
	rows, err := s.DB.QueryContext(ctx, q+` ORDER BY u.path COLLATE NOCASE`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Unmatched{}
	for rows.Next() {
		u, err := scanUnmatched(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// UnmatchedPaths returns a library's unmatched paths, with whether each is
// ignored.
func (s *Store) UnmatchedPaths(ctx context.Context, libraryID int64) (map[string]bool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT path, ignored FROM unmatched WHERE library_id = ?`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var p string
		var ign bool
		if err := rows.Scan(&p, &ign); err != nil {
			return nil, err
		}
		out[p] = ign
	}
	return out, rows.Err()
}

// GetUnmatched loads one unmatched item.
func (s *Store) GetUnmatched(ctx context.Context, id int64) (*Unmatched, error) {
	return scanUnmatched(s.DB.QueryRowContext(ctx, `SELECT `+unmatchedCols+` FROM unmatched u JOIN libraries l ON l.id = u.library_id WHERE u.id = ?`, id))
}

// SetUnmatched records (or refreshes) an item a scan could not match. An
// ignored item stays ignored.
func (s *Store) SetUnmatched(ctx context.Context, libraryID int64, path, reason, guess string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO unmatched (library_id, path, reason, guess, updated_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (library_id, path) DO UPDATE SET reason = excluded.reason, guess = excluded.guess, updated_at = excluded.updated_at`,
		libraryID, path, reason, guess, now())
	return err
}

// ClearUnmatched forgets an item, once it is matched.
func (s *Store) ClearUnmatched(ctx context.Context, libraryID int64, path string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM unmatched WHERE library_id = ? AND path = ?`, libraryID, path)
	return err
}

// PruneUnmatched forgets a library's items that a full scan no longer saw.
func (s *Store) PruneUnmatched(ctx context.Context, libraryID int64, seen map[string]bool) error {
	paths, err := s.UnmatchedPaths(ctx, libraryID)
	if err != nil {
		return err
	}
	for p := range paths {
		if !seen[p] {
			if err := s.ClearUnmatched(ctx, libraryID, p); err != nil {
				return err
			}
		}
	}
	return nil
}

// SetUnmatchedIgnored hides an item from the list and from later scans, or
// brings it back.
func (s *Store) SetUnmatchedIgnored(ctx context.Context, id int64, ignored bool) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE unmatched SET ignored = ?, updated_at = ? WHERE id = ?`, ignored, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SeriesInFolder finds the show tracked in a library folder.
func (s *Store) SeriesInFolder(ctx context.Context, libraryID int64, folder string) (*Series, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM media_items WHERE media_type = 'tv' AND level = 'work' AND library_id = ? AND folder = ?`,
		libraryID, folder).Scan(&id)
	if err != nil {
		return nil, mapErr(err)
	}
	return s.GetSeries(ctx, id)
}

// MovieInFolder finds the movie tracked in a library folder.
func (s *Store) MovieInFolder(ctx context.Context, libraryID int64, folder string) (*Movie, error) {
	return s.oneMovie(ctx, `w.library_id = ? AND w.folder = ?`, libraryID, folder)
}

// FileOwner returns the media item a file is recorded for, or 0.
func (s *Store) FileOwner(ctx context.Context, path string) (int64, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT media_item_id FROM files WHERE path = ?`, path).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// ErrBookHasFiles means a book being adopted already has files elsewhere.
var ErrBookHasFiles = errors.New("already in Bragi with other files")

// AdoptBook records a book found on disk: it is added as imported and not
// monitored, or, if Bragi already tracks it without files (say it was
// requested), its files are linked and it is marked imported.
func (s *Store) AdoptBook(ctx context.Context, in BookInput, files []BookFile) (int64, error) {
	b, err := s.GetBookByForeignID(ctx, in.MediaType, in.ForeignID)
	switch {
	case errors.Is(err, ErrNotFound):
		res, err := s.DB.ExecContext(ctx, `INSERT INTO media_items (media_type, level, library_id, title, author, year, foreign_id,
			overview, poster_path, folder, status, monitored, refreshed_at) VALUES (?, 'work', ?, ?, ?, ?, ?, ?, ?, ?, 'wanted', 0, ?)`,
			in.MediaType, nullable(in.LibraryID), in.Title, in.Author, in.Year, in.ForeignID, in.Overview, in.CoverID, in.Folder, now())
		if err != nil {
			return 0, mapErr(err)
		}
		id, _ := res.LastInsertId()
		return id, s.ReplaceBookFiles(ctx, id, files, "")
	case err != nil:
		return 0, err
	}
	merged := append([]BookFile{}, files...)
	if len(b.Files) > 0 {
		// Files of this book exist elsewhere: leave both alone.
		if !overlaps(b.Files, files) {
			return 0, ErrBookHasFiles
		}
		have := map[string]bool{}
		for _, f := range files {
			have[f.Path] = true
		}
		added := false
		for _, f := range b.Files {
			if !have[f.Path] {
				merged = append(merged, f)
			}
		}
		for p := range have {
			if !hasPath(b.Files, p) {
				added = true
			}
		}
		if !added {
			return b.ID, nil
		}
	} else if _, err := s.DB.ExecContext(ctx, `UPDATE media_items SET folder = ?, library_id = COALESCE(library_id, ?) WHERE id = ?`,
		in.Folder, nullable(in.LibraryID), b.ID); err != nil {
		return 0, err
	}
	return b.ID, s.ReplaceBookFiles(ctx, b.ID, merged, "")
}

func hasPath(fs []BookFile, p string) bool {
	for _, f := range fs {
		if f.Path == p {
			return true
		}
	}
	return false
}

func overlaps(a, b []BookFile) bool {
	for _, x := range a {
		for _, y := range b {
			if x.Path == y.Path {
				return true
			}
		}
	}
	return false
}
