package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ---- Books and audiobooks ----
//
// A book is a media_items row with level 'work', media_type 'book' or
// 'audiobook', and no children. An ebook and an audiobook of the same title
// are two rows, since they live in different libraries and download apart.

// Book is an ebook or audiobook Bragi tracks.
type Book struct {
	ID         int64  `json:"id"`
	MediaType  string `json:"type"`
	ForeignID  string `json:"bookId"`
	Title      string `json:"title"`
	Author     string `json:"author"`
	Year       int    `json:"year,omitempty"`
	Overview   string `json:"overview"`
	CoverID    string `json:"-"`
	LibraryID  int64  `json:"libraryId"`
	Folder     string `json:"folder"`
	Monitored  bool   `json:"monitored"`
	Status     string `json:"status"`
	SearchNote string `json:"searchNote,omitempty"`
	CreatedAt  string `json:"createdAt"`

	// Files are the imported files, filled by GetBook.
	Files []BookFile `json:"files,omitempty"`
	// Activities counts downloads in progress.
	Activities int `json:"downloading"`
}

// BookFile is one imported file of a book.
type BookFile struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	Format    string `json:"format"`
}

const bookCols = `w.id, w.media_type, COALESCE(w.foreign_id, ''), w.title, w.author, COALESCE(w.year, 0), w.overview, w.poster_path,
	COALESCE(w.library_id, 0), w.folder, w.monitored, w.status, w.search_note, w.created_at,
	(SELECT COUNT(*) FROM downloads d WHERE d.media_item_id = w.id AND d.status IN ('queued', 'downloading', 'completed', 'importing'))`

func scanBook(row interface{ Scan(...any) error }) (*Book, error) {
	var b Book
	if err := row.Scan(&b.ID, &b.MediaType, &b.ForeignID, &b.Title, &b.Author, &b.Year, &b.Overview, &b.CoverID,
		&b.LibraryID, &b.Folder, &b.Monitored, &b.Status, &b.SearchNote, &b.CreatedAt, &b.Activities); err != nil {
		return nil, mapErr(err)
	}
	return &b, nil
}

// IsBookType reports whether t is a book media type.
func IsBookType(t string) bool { return t == "book" || t == "audiobook" }

// ListBooks returns every tracked book of a type ("" for both), by author
// then title.
func (s *Store) ListBooks(ctx context.Context, mediaType string) ([]Book, error) {
	where, args := `w.media_type IN ('book', 'audiobook')`, []any{}
	if mediaType != "" {
		where, args = `w.media_type = ?`, []any{mediaType}
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+bookCols+` FROM media_items w WHERE `+where+` AND w.level = 'work'
		ORDER BY w.author COLLATE NOCASE, w.title COLLATE NOCASE`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Book{}
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// GetBook loads one book with its files.
func (s *Store) GetBook(ctx context.Context, id int64) (*Book, error) {
	b, err := scanBook(s.DB.QueryRowContext(ctx, `SELECT `+bookCols+` FROM media_items w
		WHERE w.id = ? AND w.media_type IN ('book', 'audiobook') AND w.level = 'work'`, id))
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT path, size_bytes, quality FROM files WHERE media_item_id = ? ORDER BY path`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f BookFile
		if err := rows.Scan(&f.Path, &f.SizeBytes, &f.Format); err != nil {
			return nil, err
		}
		b.Files = append(b.Files, f)
	}
	return b, rows.Err()
}

// GetBookByForeignID finds a tracked book by its Open Library work.
func (s *Store) GetBookByForeignID(ctx context.Context, mediaType, foreignID string) (*Book, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM media_items WHERE media_type = ? AND foreign_id = ? AND level = 'work'`,
		mediaType, foreignID).Scan(&id)
	if err != nil {
		return nil, mapErr(err)
	}
	return s.GetBook(ctx, id)
}

// BookInput is what AddBook writes.
type BookInput struct {
	MediaType string
	ForeignID string
	Title     string
	Author    string
	Year      int
	Overview  string
	CoverID   string
	LibraryID int64
	Folder    string
}

// AddBook starts tracking a book, monitored and due for a search now. If it
// is already tracked it is monitored again and returned as is.
func (s *Store) AddBook(ctx context.Context, in BookInput) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	ts := now()
	var id int64
	var status string
	err = tx.QueryRowContext(ctx, `SELECT id, status FROM media_items WHERE media_type = ? AND foreign_id = ? AND level = 'work'`,
		in.MediaType, in.ForeignID).Scan(&id, &status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := tx.ExecContext(ctx, `INSERT INTO media_items (media_type, level, library_id, title, author, year, foreign_id,
			overview, poster_path, folder, status, monitored, next_search_at, refreshed_at) VALUES (?, 'work', ?, ?, ?, ?, ?, ?, ?, ?, 'wanted', 1, ?, ?)`,
			in.MediaType, nullable(in.LibraryID), in.Title, in.Author, in.Year, in.ForeignID, in.Overview, in.CoverID, in.Folder, ts, ts)
		if err != nil {
			return 0, mapErr(err)
		}
		id, _ = res.LastInsertId()
	case err != nil:
		return 0, err
	default:
		next := any(nil)
		if status != "imported" && status != "downloading" {
			next = ts
		}
		if _, err := tx.ExecContext(ctx, `UPDATE media_items SET monitored = 1, next_search_at = COALESCE(?, next_search_at), updated_at = ? WHERE id = ?`,
			next, ts, id); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// DueBooks returns monitored books whose search time has come.
func (s *Store) DueBooks(ctx context.Context) ([]Book, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+bookCols+` FROM media_items w
		WHERE w.media_type IN ('book', 'audiobook') AND w.level = 'work' AND w.monitored = 1
		AND w.status NOT IN ('imported', 'downloading') AND w.next_search_at IS NOT NULL AND w.next_search_at <= ?
		ORDER BY w.next_search_at`, now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Book{}
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// SetBookState records a book's status, search note and next search time
// (zero for none).
func (s *Store) SetBookState(ctx context.Context, id int64, status, note string, next time.Time) error {
	var n any
	if !next.IsZero() {
		n = next.UTC().Format(timeFormat)
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE media_items SET status = ?, search_note = ?, next_search_at = ?, updated_at = ? WHERE id = ?`,
		status, note, n, now(), id)
	return err
}

// SetBookMonitored turns automatic searching for a book on or off.
func (s *Store) SetBookMonitored(ctx context.Context, id int64, on bool) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE media_items SET monitored = ?, updated_at = ? WHERE id = ?`, on, now(), id)
	return err
}

// ReplaceBookFiles records the files of an import, dropping the book's
// earlier files, and marks it imported.
func (s *Store) ReplaceBookFiles(ctx context.Context, id int64, files []BookFile, releaseTitle string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE media_item_id = ?`, id); err != nil {
		return err
	}
	for _, f := range files {
		if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE path = ?`, f.Path); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO files (media_item_id, path, size_bytes, quality, release_title) VALUES (?, ?, ?, ?, ?)`,
			id, f.Path, f.SizeBytes, f.Format, releaseTitle); err != nil {
			return mapErr(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media_items SET status = 'imported', search_note = '', next_search_at = NULL, updated_at = ? WHERE id = ?`,
		now(), id); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteBook forgets a book. Files on disk are not touched.
func (s *Store) DeleteBook(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM media_items WHERE id = ? AND level = 'work' AND media_type IN ('book', 'audiobook')`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkBookRequestsAvailable sets approved requests for a book to available.
func (s *Store) MarkBookRequestsAvailable(ctx context.Context, id int64) (bool, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE requests SET status = 'available', updated_at = ? WHERE media_item_id = ? AND status = 'approved'`, now(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
