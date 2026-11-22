package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ---- Music ----
//
// An artist is a media_items 'work' (media_type 'music'), its albums are
// 'unit' rows under it and each album's tracks are 'leaf' rows under the
// album. foreign_id is the MusicBrainz id at every level. Searching and
// downloading happen per album; files belong to tracks.

// Artist is an artist Bragi tracks.
type Artist struct {
	ID         int64  `json:"id"`
	ForeignID  string `json:"artistId"`
	Name       string `json:"name"`
	SortName   string `json:"sortName,omitempty"`
	Overview   string `json:"overview,omitempty"`
	ImageURL   string `json:"imageUrl,omitempty"`
	LibraryID  int64  `json:"libraryId"`
	Folder     string `json:"folder"`
	MonitorNew bool   `json:"monitorNew"`
	CreatedAt  string `json:"createdAt"`
	// Counts over the artist's albums. Wanted counts monitored albums that
	// are out and not in the library yet.
	Albums     int `json:"albums"`
	Monitored  int `json:"monitoredAlbums"`
	Imported   int `json:"importedAlbums"`
	Wanted     int `json:"wantedAlbums"`
	Activities int `json:"downloading"`
	// CoverAlbumID is an album to show when the artist has no photo.
	CoverAlbumID string `json:"coverAlbumId,omitempty"`
}

// Album is one album (MusicBrainz release group) of an artist.
type Album struct {
	ID          int64  `json:"id"`
	ArtistID    int64  `json:"artistDbId"`
	Artist      string `json:"artist"`
	ArtistMBID  string `json:"artistId"`
	ForeignID   string `json:"albumId"`
	Title       string `json:"title"`
	Year        int    `json:"year,omitempty"`
	ReleaseDate string `json:"releaseDate,omitempty"`
	Type        string `json:"albumType"`
	ReleaseID   string `json:"releaseId,omitempty"`
	LibraryID   int64  `json:"libraryId"`
	Folder      string `json:"folder"`
	Monitored   bool   `json:"monitored"`
	Status      string `json:"status"`
	SearchNote  string `json:"searchNote,omitempty"`
	Tracks      int    `json:"tracks"`
	Imported    int    `json:"importedTracks"`
	// Quality is the lowest quality among the album's files.
	Quality    string `json:"quality,omitempty"`
	Activities int    `json:"downloading"`

	// TrackList is filled by GetAlbum.
	TrackList []Track `json:"trackList,omitempty"`
}

// Track is one track of an album.
type Track struct {
	ID          int64  `json:"id"`
	RecordingID string `json:"recordingId,omitempty"`
	Disc        int    `json:"disc"`
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Artist      string `json:"artist,omitempty"`
	Seconds     int    `json:"seconds,omitempty"`
	Status      string `json:"status"`
	FilePath    string `json:"filePath,omitempty"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
	Quality     string `json:"quality,omitempty"`
}

const artistCols = `w.id, COALESCE(w.foreign_id, ''), w.title, w.author, w.overview, w.poster_path, COALESCE(w.library_id, 0), w.folder,
	w.monitor_new, w.created_at,
	(SELECT COUNT(*) FROM media_items a WHERE a.parent_id = w.id),
	(SELECT COUNT(*) FROM media_items a WHERE a.parent_id = w.id AND a.monitored = 1),
	(SELECT COUNT(*) FROM media_items a WHERE a.parent_id = w.id AND a.status IN ('imported', 'upgrade_wanted')),
	(SELECT COUNT(*) FROM media_items a WHERE a.parent_id = w.id AND a.monitored = 1 AND a.status NOT IN ('imported', 'upgrade_wanted')
	   AND a.air_date IS NOT NULL AND a.air_date <= ?),
	(SELECT COUNT(*) FROM downloads d JOIN media_items a ON a.id = d.media_item_id
	   WHERE a.parent_id = w.id AND d.status IN ('queued', 'downloading', 'completed', 'importing')),
	COALESCE((SELECT a.foreign_id FROM media_items a WHERE a.parent_id = w.id
	   ORDER BY a.status IN ('imported', 'upgrade_wanted') DESC, a.album_type = 'Album' DESC, a.air_date DESC LIMIT 1), '')`

func scanArtist(row interface{ Scan(...any) error }) (*Artist, error) {
	var a Artist
	if err := row.Scan(&a.ID, &a.ForeignID, &a.Name, &a.SortName, &a.Overview, &a.ImageURL, &a.LibraryID, &a.Folder,
		&a.MonitorNew, &a.CreatedAt, &a.Albums, &a.Monitored, &a.Imported, &a.Wanted, &a.Activities, &a.CoverAlbumID); err != nil {
		return nil, mapErr(err)
	}
	return &a, nil
}

// ListArtists returns every tracked artist, by sort name.
func (s *Store) ListArtists(ctx context.Context) ([]Artist, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+artistCols+` FROM media_items w WHERE w.media_type = 'music' AND w.level = 'work'
		ORDER BY CASE w.author WHEN '' THEN w.title ELSE w.author END COLLATE NOCASE`, Today())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Artist{}
	for rows.Next() {
		a, err := scanArtist(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// GetArtist loads one artist.
func (s *Store) GetArtist(ctx context.Context, id int64) (*Artist, error) {
	return scanArtist(s.DB.QueryRowContext(ctx, `SELECT `+artistCols+` FROM media_items w
		WHERE w.id = ? AND w.media_type = 'music' AND w.level = 'work'`, Today(), id))
}

// GetArtistByForeignID finds an artist by MusicBrainz id.
func (s *Store) GetArtistByForeignID(ctx context.Context, mbid string) (*Artist, error) {
	return scanArtist(s.DB.QueryRowContext(ctx, `SELECT `+artistCols+` FROM media_items w
		WHERE w.foreign_id = ? AND w.media_type = 'music' AND w.level = 'work'`, Today(), mbid))
}

// ArtistInput is what UpsertArtist writes.
type ArtistInput struct {
	ForeignID string
	Name      string
	SortName  string
	Overview  string
	ImageURL  string
	LibraryID int64
	Folder    string
}

// UpsertArtist adds an artist or refreshes its metadata, and returns its
// id. A new artist does not monitor new albums until told to.
func (s *Store) UpsertArtist(ctx context.Context, in ArtistInput) (int64, error) {
	ts := now()
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM media_items WHERE media_type = 'music' AND level = 'work' AND foreign_id = ?`, in.ForeignID).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := s.DB.ExecContext(ctx, `INSERT INTO media_items (media_type, level, library_id, title, author, foreign_id, overview,
			poster_path, folder, status, monitored, refreshed_at) VALUES ('music', 'work', ?, ?, ?, ?, ?, ?, ?, 'wanted', 1, ?)`,
			nullable(in.LibraryID), in.Name, in.SortName, in.ForeignID, in.Overview, in.ImageURL, in.Folder, ts)
		if err != nil {
			return 0, mapErr(err)
		}
		return res.LastInsertId()
	case err != nil:
		return 0, err
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE media_items SET title = ?, author = ?, overview = ?, poster_path = ?, refreshed_at = ?, updated_at = ?,
		library_id = COALESCE(library_id, ?) WHERE id = ?`, in.Name, in.SortName, in.Overview, in.ImageURL, ts, ts, nullable(in.LibraryID), id)
	return id, err
}

// SetArtistFolder records where an artist's albums go inside the library.
func (s *Store) SetArtistFolder(ctx context.Context, id int64, folder string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE media_items SET folder = ?, updated_at = ? WHERE id = ?`, folder, now(), id)
	return err
}

// SetArtistMonitorNew turns monitoring of an artist's future albums on or off.
func (s *Store) SetArtistMonitorNew(ctx context.Context, id int64, on bool) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE media_items SET monitor_new = ?, updated_at = ? WHERE id = ? AND media_type = 'music' AND level = 'work'`, on, now(), id)
	return err
}

// ArtistsNeedingRefresh returns ids of artists whose metadata is older than age.
func (s *Store) ArtistsNeedingRefresh(ctx context.Context, age time.Duration) ([]int64, error) {
	cutoff := time.Now().Add(-age).UTC().Format(timeFormat)
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM media_items WHERE media_type = 'music' AND level = 'work'
		AND (refreshed_at IS NULL OR refreshed_at < ?)`, cutoff)
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

// DeleteArtist forgets an artist and its albums. Files on disk stay.
func (s *Store) DeleteArtist(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM media_items WHERE id = ? AND level = 'work' AND media_type = 'music'`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- Albums ----

const albumCols = `a.id, w.id, w.title, COALESCE(w.foreign_id, ''), COALESCE(a.foreign_id, ''), a.title, COALESCE(a.year, 0),
	COALESCE(a.air_date, ''), a.album_type, a.release_id, COALESCE(w.library_id, 0), a.folder, a.monitored, a.status, a.search_note,
	(SELECT COUNT(*) FROM media_items t WHERE t.parent_id = a.id),
	(SELECT COUNT(*) FROM media_items t WHERE t.parent_id = a.id AND t.status = 'imported'),
	COALESCE((SELECT f.quality FROM files f JOIN media_items t ON t.id = f.media_item_id WHERE t.parent_id = a.id
	   ORDER BY f.quality = 'FLAC', f.quality LIMIT 1), ''),
	(SELECT COUNT(*) FROM downloads d WHERE d.media_item_id = a.id AND d.status IN ('queued', 'downloading', 'completed', 'importing'))`

const albumFrom = ` FROM media_items a JOIN media_items w ON w.id = a.parent_id`

func scanAlbum(row interface{ Scan(...any) error }) (*Album, error) {
	var a Album
	if err := row.Scan(&a.ID, &a.ArtistID, &a.Artist, &a.ArtistMBID, &a.ForeignID, &a.Title, &a.Year, &a.ReleaseDate, &a.Type,
		&a.ReleaseID, &a.LibraryID, &a.Folder, &a.Monitored, &a.Status, &a.SearchNote, &a.Tracks, &a.Imported, &a.Quality, &a.Activities); err != nil {
		return nil, mapErr(err)
	}
	return &a, nil
}

func (s *Store) queryAlbums(ctx context.Context, where string, args ...any) ([]Album, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+albumCols+albumFrom+` WHERE a.media_type = 'music' AND a.level = 'unit' AND `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Album{}
	for rows.Next() {
		a, err := scanAlbum(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// AlbumsOf returns an artist's albums, oldest first.
func (s *Store) AlbumsOf(ctx context.Context, artistID int64) ([]Album, error) {
	return s.queryAlbums(ctx, `a.parent_id = ? ORDER BY a.air_date IS NULL, a.air_date, a.title COLLATE NOCASE`, artistID)
}

// GetAlbum loads one album with its tracks.
func (s *Store) GetAlbum(ctx context.Context, id int64) (*Album, error) {
	as, err := s.queryAlbums(ctx, `a.id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(as) == 0 {
		return nil, ErrNotFound
	}
	a := &as[0]
	rows, err := s.DB.QueryContext(ctx, `SELECT t.id, COALESCE(t.foreign_id, ''), t.disc, COALESCE(t.number, 0), t.title, t.author, t.runtime, t.status,
		COALESCE(f.path, ''), COALESCE(f.size_bytes, 0), COALESCE(f.quality, '')
		FROM media_items t LEFT JOIN files f ON f.media_item_id = t.id WHERE t.parent_id = ? ORDER BY t.disc, t.number`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t Track
		if err := rows.Scan(&t.ID, &t.RecordingID, &t.Disc, &t.Number, &t.Title, &t.Artist, &t.Seconds, &t.Status,
			&t.FilePath, &t.SizeBytes, &t.Quality); err != nil {
			return nil, err
		}
		a.TrackList = append(a.TrackList, t)
	}
	return a, rows.Err()
}

// GetAlbumByForeignID finds an album by its MusicBrainz release group.
func (s *Store) GetAlbumByForeignID(ctx context.Context, mbid string) (*Album, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM media_items WHERE media_type = 'music' AND level = 'unit' AND foreign_id = ?`, mbid).Scan(&id)
	if err != nil {
		return nil, mapErr(err)
	}
	return s.GetAlbum(ctx, id)
}

// AlbumInput is what UpsertAlbum writes.
type AlbumInput struct {
	ForeignID   string
	Title       string
	Year        int
	ReleaseDate string
	Type        string
	Folder      string
}

// UpsertAlbum adds an album to an artist or refreshes its metadata. A new
// album starts unmonitored. isNew says whether it was added.
func (s *Store) UpsertAlbum(ctx context.Context, artistID int64, in AlbumInput) (id int64, isNew bool, err error) {
	var date any
	if in.ReleaseDate != "" {
		date = fullDate(in.ReleaseDate)
	}
	err = s.DB.QueryRowContext(ctx, `SELECT id FROM media_items WHERE media_type = 'music' AND level = 'unit' AND foreign_id = ?`, in.ForeignID).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := s.DB.ExecContext(ctx, `INSERT INTO media_items (media_type, level, parent_id, title, year, air_date, foreign_id, album_type,
			folder, status, monitored) VALUES ('music', 'unit', ?, ?, ?, ?, ?, ?, ?, 'wanted', 0)`,
			artistID, in.Title, in.Year, date, in.ForeignID, in.Type, in.Folder)
		if err != nil {
			return 0, false, mapErr(err)
		}
		id, _ = res.LastInsertId()
		return id, true, nil
	case err != nil:
		return 0, false, err
	}
	_, err = s.DB.ExecContext(ctx, `UPDATE media_items SET title = ?, year = ?, air_date = ?, album_type = ?, updated_at = ? WHERE id = ?`,
		in.Title, in.Year, date, in.Type, now(), id)
	return id, false, err
}

// fullDate pads a MusicBrainz date ("2016" or "2016-05") to a day, so it
// compares with today: a year alone counts from its last day.
func fullDate(d string) string {
	switch len(d) {
	case 4:
		return d + "-12-31"
	case 7:
		return d + "-28"
	}
	return d
}

// TrackInput is one track from metadata.
type TrackInput struct {
	RecordingID string
	Disc        int
	Number      int
	Title       string
	Artist      string
	Seconds     int
}

// SetAlbumTracks stores an album's track list from a release. It changes
// nothing once the album has files, so linked files stay linked.
func (s *Store) SetAlbumTracks(ctx context.Context, albumID int64, releaseID string, tracks []TrackInput) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var files int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM files f JOIN media_items t ON t.id = f.media_item_id WHERE t.parent_id = ?`, albumID).Scan(&files); err != nil {
		return err
	}
	if files > 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM media_items WHERE parent_id = ?`, albumID); err != nil {
		return err
	}
	for _, t := range tracks {
		if _, err := tx.ExecContext(ctx, `INSERT INTO media_items (media_type, level, parent_id, title, author, foreign_id, disc, number, runtime,
			status, monitored) VALUES ('music', 'leaf', ?, ?, ?, ?, ?, ?, ?, 'wanted', 1)`,
			albumID, t.Title, t.Artist, t.RecordingID, t.Disc, t.Number, t.Seconds); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media_items SET release_id = ?, updated_at = ? WHERE id = ?`, releaseID, now(), albumID); err != nil {
		return err
	}
	return tx.Commit()
}

// SetAlbumMonitored turns automatic searching for albums on or off. An
// album turned on that is not in the library is queued for a search.
func (s *Store) SetAlbumMonitored(ctx context.Context, ids []int64, on bool) error {
	ts := now()
	for _, id := range ids {
		var err error
		if on {
			_, err = s.DB.ExecContext(ctx, `UPDATE media_items SET monitored = 1, updated_at = ?,
				next_search_at = CASE WHEN status IN ('imported', 'downloading') THEN next_search_at ELSE ? END WHERE id = ? AND level = 'unit'`, ts, ts, id)
		} else {
			_, err = s.DB.ExecContext(ctx, `UPDATE media_items SET monitored = 0, updated_at = ? WHERE id = ? AND level = 'unit'`, ts, id)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// DueAlbums returns monitored albums that are out and whose search time has
// come.
func (s *Store) DueAlbums(ctx context.Context) ([]Album, error) {
	return s.queryAlbums(ctx, `a.monitored = 1 AND a.status NOT IN ('imported', 'downloading') AND a.next_search_at IS NOT NULL
		AND a.next_search_at <= ? AND (a.air_date IS NULL OR a.air_date <= ?) ORDER BY a.next_search_at`, now(), Today())
}

// SetAlbumState records an album's status, search note and next search
// time (zero for none).
func (s *Store) SetAlbumState(ctx context.Context, id int64, status, note string, next time.Time) error {
	var n any
	if !next.IsZero() {
		n = next.UTC().Format(timeFormat)
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE media_items SET status = ?, search_note = ?, next_search_at = ?, updated_at = ? WHERE id = ?`,
		status, note, n, now(), id)
	return err
}

// SetAlbumFolder records where an album lives inside its library.
func (s *Store) SetAlbumFolder(ctx context.Context, id int64, folder string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE media_items SET folder = ?, updated_at = ? WHERE id = ?`, folder, now(), id)
	return err
}

// ClearAlbumFiles forgets the files of an album's tracks, before an upgrade
// records new ones.
func (s *Store) ClearAlbumFiles(ctx context.Context, albumID int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE media_item_id IN (SELECT id FROM media_items WHERE parent_id = ?)`, albumID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media_items SET status = 'wanted' WHERE parent_id = ?`, albumID); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkMusicRequestsAvailable sets approved requests to available: those
// for the album, and those for its artist once no monitored album is
// missing.
func (s *Store) MarkMusicRequestsAvailable(ctx context.Context, albumID int64) (bool, error) {
	a, err := s.GetAlbum(ctx, albumID)
	if err != nil {
		return false, err
	}
	var n int64
	if a.Status == "imported" || a.Status == "upgrade_wanted" {
		res, err := s.DB.ExecContext(ctx, `UPDATE requests SET status = 'available', updated_at = ? WHERE media_item_id = ? AND status = 'approved'`, now(), albumID)
		if err != nil {
			return false, err
		}
		n, _ = res.RowsAffected()
	}
	if ar, err := s.GetArtist(ctx, a.ArtistID); err == nil && ar.Wanted == 0 && ar.Imported > 0 {
		res, err := s.DB.ExecContext(ctx, `UPDATE requests SET status = 'available', updated_at = ? WHERE media_item_id = ? AND status = 'approved'`, now(), ar.ID)
		if err != nil {
			return false, err
		}
		m, _ := res.RowsAffected()
		n += m
	}
	return n > 0, nil
}

// ---- Metadata cache ----

// GetCache returns a cached body no older than maxAge.
func (s *Store) GetCache(ctx context.Context, key string, maxAge time.Duration) (string, bool) {
	var body, at string
	if err := s.DB.QueryRowContext(ctx, `SELECT body, fetched_at FROM metadata_cache WHERE key = ?`, key).Scan(&body, &at); err != nil {
		return "", false
	}
	t, err := time.Parse(timeFormat, at)
	if err != nil || time.Since(t) > maxAge {
		return "", false
	}
	return body, true
}

// SetCache stores a body.
func (s *Store) SetCache(ctx context.Context, key, body string) {
	s.DB.ExecContext(ctx, `INSERT INTO metadata_cache (key, body, fetched_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET body = excluded.body, fetched_at = excluded.fetched_at`, key, body, now())
}

// PruneCache drops cache entries older than age.
func (s *Store) PruneCache(ctx context.Context, age time.Duration) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM metadata_cache WHERE fetched_at < ?`, time.Now().Add(-age).UTC().Format(timeFormat))
	return err
}
