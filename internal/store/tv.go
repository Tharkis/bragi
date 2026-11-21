package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ---- Series, seasons and episodes ----
//
// A series is a media_items row with level 'work', a season is a 'unit'
// under it, and an episode is a 'leaf' under the season.

// Series is a TV show Bragi tracks.
type Series struct {
	ID         int64  `json:"id"`
	TMDBID     int64  `json:"tmdbId"`
	TVDBID     int64  `json:"tvdbId,omitempty"`
	IMDBID     string `json:"imdbId,omitempty"`
	Title      string `json:"title"`
	Year       int    `json:"year,omitempty"`
	Overview   string `json:"overview"`
	PosterPath string `json:"posterPath,omitempty"`
	Runtime    int    `json:"runtime,omitempty"`
	LibraryID  int64  `json:"libraryId"`
	Folder     string `json:"folder"`
	Monitored  bool   `json:"monitored"`
	CreatedAt  string `json:"createdAt"`

	// Episode counts, filled by ListSeries and GetSeries.
	Episodes   int `json:"episodes"`   // episodes that are wanted (monitored and aired) or on disk
	Imported   int `json:"imported"`   // episodes with a file
	Missing    int `json:"missing"`    // monitored, aired, no file
	Activities int `json:"activities"` // downloads in progress
}

// Season is a season with its episodes.
type Season struct {
	ID        int64     `json:"id"`
	Number    int       `json:"number"`
	Monitored bool      `json:"monitored"`
	Episodes  []Episode `json:"episodes"`
}

// Episode is one episode and its state.
type Episode struct {
	ID         int64  `json:"id"`
	SeasonID   int64  `json:"seasonId"`
	SeriesID   int64  `json:"seriesId"`
	Season     int    `json:"season"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	AirDate    string `json:"airDate,omitempty"`
	Runtime    int    `json:"runtime,omitempty"`
	Monitored  bool   `json:"monitored"`
	Status     string `json:"status"`
	SearchNote string `json:"searchNote,omitempty"`
	FilePath   string `json:"filePath,omitempty"`
	Quality    string `json:"quality,omitempty"`
}

// Today is the date episodes must have aired by to be searched for.
func Today() string { return time.Now().UTC().Format("2006-01-02") }

const seriesCols = `w.id, COALESCE(w.tmdb_id, 0), COALESCE(w.tvdb_id, 0), COALESCE(w.imdb_id, ''), w.title, COALESCE(w.year, 0),
	w.overview, w.poster_path, w.runtime, COALESCE(w.library_id, 0), w.folder, w.monitored, w.created_at,
	(SELECT COUNT(*) FROM media_items e JOIN media_items s ON s.id = e.parent_id
	   WHERE s.parent_id = w.id AND (e.status = 'imported' OR e.monitored = 1 AND e.air_date IS NOT NULL AND e.air_date <= ?)),
	(SELECT COUNT(*) FROM media_items e JOIN media_items s ON s.id = e.parent_id
	   WHERE s.parent_id = w.id AND e.status = 'imported'),
	(SELECT COUNT(*) FROM media_items e JOIN media_items s ON s.id = e.parent_id
	   WHERE s.parent_id = w.id AND e.monitored = 1 AND e.air_date IS NOT NULL AND e.air_date <= ? AND e.status != 'imported'),
	(SELECT COUNT(*) FROM downloads d WHERE d.status IN ('queued', 'downloading', 'completed', 'importing') AND d.media_item_id IN
	   (SELECT w.id UNION SELECT id FROM media_items WHERE parent_id = w.id
	    UNION SELECT e.id FROM media_items e JOIN media_items s ON s.id = e.parent_id WHERE s.parent_id = w.id))`

func scanSeries(row interface{ Scan(...any) error }) (*Series, error) {
	var s Series
	if err := row.Scan(&s.ID, &s.TMDBID, &s.TVDBID, &s.IMDBID, &s.Title, &s.Year, &s.Overview, &s.PosterPath, &s.Runtime,
		&s.LibraryID, &s.Folder, &s.Monitored, &s.CreatedAt, &s.Episodes, &s.Imported, &s.Missing, &s.Activities); err != nil {
		return nil, mapErr(err)
	}
	return &s, nil
}

// ListSeries returns every tracked show, by title.
func (s *Store) ListSeries(ctx context.Context) ([]Series, error) {
	t := Today()
	rows, err := s.DB.QueryContext(ctx, `SELECT `+seriesCols+` FROM media_items w
		WHERE w.media_type = 'tv' AND w.level = 'work' ORDER BY w.title COLLATE NOCASE`, t, t)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Series{}
	for rows.Next() {
		sr, err := scanSeries(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sr)
	}
	return out, rows.Err()
}

// GetSeries loads one show.
func (s *Store) GetSeries(ctx context.Context, id int64) (*Series, error) {
	t := Today()
	return scanSeries(s.DB.QueryRowContext(ctx, `SELECT `+seriesCols+` FROM media_items w
		WHERE w.id = ? AND w.media_type = 'tv' AND w.level = 'work'`, t, t, id))
}

// GetSeriesByTMDB loads a show by its TMDB id.
func (s *Store) GetSeriesByTMDB(ctx context.Context, tmdbID int64) (*Series, error) {
	t := Today()
	return scanSeries(s.DB.QueryRowContext(ctx, `SELECT `+seriesCols+` FROM media_items w
		WHERE w.tmdb_id = ? AND w.media_type = 'tv' AND w.level = 'work'`, t, t, tmdbID))
}

// SeriesInput is what UpsertSeries writes.
type SeriesInput struct {
	TMDBID     int64
	TVDBID     int64
	IMDBID     string
	Title      string
	Year       int
	Overview   string
	PosterPath string
	Runtime    int
	LibraryID  int64
	Folder     string
	Seasons    []SeasonInput
}

// SeasonInput is one season's episodes from metadata.
type SeasonInput struct {
	Number   int
	Episodes []EpisodeInput
}

// EpisodeInput is one episode from metadata.
type EpisodeInput struct {
	Number  int
	Title   string
	AirDate string
	Runtime int
}

// MonitorFunc decides whether a newly seen episode is monitored.
type MonitorFunc func(season, episode int, airDate string) bool

// UpsertSeries creates a show or refreshes its metadata. Episodes already
// known keep their status and monitored flag; new ones are monitored as
// monitor decides and queued for search when they are.
func (s *Store) UpsertSeries(ctx context.Context, in SeriesInput, monitor MonitorFunc) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	ts := now()
	nullInt := func(n int64) any {
		if n == 0 {
			return nil
		}
		return n
	}
	nullStr := func(v string) any {
		if v == "" {
			return nil
		}
		return v
	}
	var seriesID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM media_items WHERE media_type = 'tv' AND level = 'work' AND tmdb_id = ?`, in.TMDBID).Scan(&seriesID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := tx.ExecContext(ctx, `INSERT INTO media_items (media_type, level, library_id, title, year, tmdb_id, tvdb_id, imdb_id,
			overview, poster_path, runtime, folder, status, refreshed_at) VALUES ('tv', 'work', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'wanted', ?)`,
			nullInt(in.LibraryID), in.Title, in.Year, in.TMDBID, nullInt(in.TVDBID), nullStr(in.IMDBID), in.Overview, in.PosterPath, in.Runtime, in.Folder, ts)
		if err != nil {
			return 0, mapErr(err)
		}
		seriesID, _ = res.LastInsertId()
	case err != nil:
		return 0, err
	default:
		if _, err := tx.ExecContext(ctx, `UPDATE media_items SET title = ?, year = ?, tvdb_id = ?, imdb_id = ?, overview = ?,
			poster_path = ?, runtime = ?, refreshed_at = ?, updated_at = ? WHERE id = ?`,
			in.Title, in.Year, nullInt(in.TVDBID), nullStr(in.IMDBID), in.Overview, in.PosterPath, in.Runtime, ts, ts, seriesID); err != nil {
			return 0, err
		}
	}

	for _, sn := range in.Seasons {
		var seasonID int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM media_items WHERE parent_id = ? AND level = 'unit' AND number = ?`, seriesID, sn.Number).Scan(&seasonID)
		if errors.Is(err, sql.ErrNoRows) {
			res, err := tx.ExecContext(ctx, `INSERT INTO media_items (media_type, level, parent_id, title, number, monitored)
				VALUES ('tv', 'unit', ?, ?, ?, ?)`, seriesID, seasonTitle(sn.Number), sn.Number, sn.Number > 0)
			if err != nil {
				return 0, err
			}
			seasonID, _ = res.LastInsertId()
		} else if err != nil {
			return 0, err
		}
		for _, ep := range sn.Episodes {
			var epID int64
			err := tx.QueryRowContext(ctx, `SELECT id FROM media_items WHERE parent_id = ? AND level = 'leaf' AND number = ?`, seasonID, ep.Number).Scan(&epID)
			if errors.Is(err, sql.ErrNoRows) {
				mon := monitor(sn.Number, ep.Number, ep.AirDate)
				var next any
				if mon {
					next = ts
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO media_items (media_type, level, parent_id, title, number, air_date, runtime,
					monitored, status, next_search_at) VALUES ('tv', 'leaf', ?, ?, ?, ?, ?, ?, 'wanted', ?)`,
					seasonID, ep.Title, ep.Number, nullStr(ep.AirDate), ep.Runtime, mon, next); err != nil {
					return 0, err
				}
				continue
			} else if err != nil {
				return 0, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE media_items SET title = ?, air_date = ?, runtime = ?, updated_at = ? WHERE id = ?`,
				ep.Title, nullStr(ep.AirDate), ep.Runtime, ts, epID); err != nil {
				return 0, err
			}
		}
	}
	return seriesID, tx.Commit()
}

func seasonTitle(n int) string {
	if n == 0 {
		return "Specials"
	}
	return "Season " + itoa(n)
}

func itoa(n int) string { return strconv.Itoa(n) }

const episodeCols = `e.id, s.id, s.parent_id, s.number, e.number, e.title, COALESCE(e.air_date, ''), e.runtime, e.monitored, e.status,
	e.search_note, COALESCE((SELECT path FROM files f WHERE f.media_item_id = e.id ORDER BY f.id DESC LIMIT 1), ''),
	COALESCE((SELECT quality FROM files f WHERE f.media_item_id = e.id ORDER BY f.id DESC LIMIT 1), '')`

func scanEpisode(row interface{ Scan(...any) error }) (*Episode, error) {
	var e Episode
	if err := row.Scan(&e.ID, &e.SeasonID, &e.SeriesID, &e.Season, &e.Number, &e.Title, &e.AirDate, &e.Runtime, &e.Monitored,
		&e.Status, &e.SearchNote, &e.FilePath, &e.Quality); err != nil {
		return nil, mapErr(err)
	}
	return &e, nil
}

func (s *Store) queryEpisodes(ctx context.Context, where string, args ...any) ([]Episode, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+episodeCols+` FROM media_items e JOIN media_items s ON s.id = e.parent_id
		WHERE e.level = 'leaf' AND e.media_type = 'tv' AND `+where+` ORDER BY s.parent_id, s.number, e.number`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Episode{}
	for rows.Next() {
		e, err := scanEpisode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// Seasons returns a show's seasons with their episodes, newest season first.
func (s *Store) Seasons(ctx context.Context, seriesID int64) ([]Season, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, number, monitored FROM media_items WHERE parent_id = ? AND level = 'unit' ORDER BY number DESC`, seriesID)
	if err != nil {
		return nil, err
	}
	var seasons []Season
	idx := map[int64]int{}
	for rows.Next() {
		var sn Season
		if err := rows.Scan(&sn.ID, &sn.Number, &sn.Monitored); err != nil {
			rows.Close()
			return nil, err
		}
		sn.Episodes = []Episode{}
		idx[sn.ID] = len(seasons)
		seasons = append(seasons, sn)
	}
	rows.Close()
	eps, err := s.queryEpisodes(ctx, `s.parent_id = ?`, seriesID)
	if err != nil {
		return nil, err
	}
	for _, e := range eps {
		if i, ok := idx[e.SeasonID]; ok {
			seasons[i].Episodes = append(seasons[i].Episodes, e)
		}
	}
	if seasons == nil {
		seasons = []Season{}
	}
	return seasons, nil
}

// GetEpisode loads one episode.
func (s *Store) GetEpisode(ctx context.Context, id int64) (*Episode, error) {
	eps, err := s.queryEpisodes(ctx, `e.id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(eps) == 0 {
		return nil, ErrNotFound
	}
	return &eps[0], nil
}

// EpisodesBySeries returns every episode of a show.
func (s *Store) EpisodesBySeries(ctx context.Context, seriesID int64) ([]Episode, error) {
	return s.queryEpisodes(ctx, `s.parent_id = ?`, seriesID)
}

// DueEpisodes returns monitored, aired episodes without a file whose search
// time has come and that are not being downloaded.
func (s *Store) DueEpisodes(ctx context.Context) ([]Episode, error) {
	return s.queryEpisodes(ctx, `e.monitored = 1 AND e.status IN ('wanted', 'failed', 'searching')
		AND e.next_search_at IS NOT NULL AND e.next_search_at <= ? AND e.air_date IS NOT NULL AND e.air_date <= ?`, now(), Today())
}

// QueueSearch marks episodes to be searched for at the next scheduler pass.
func (s *Store) QueueSearch(ctx context.Context, episodeIDs ...int64) error {
	ts := now()
	for _, id := range episodeIDs {
		if _, err := s.DB.ExecContext(ctx, `UPDATE media_items SET next_search_at = ?, updated_at = ?
			WHERE id = ? AND status != 'imported' AND status != 'downloading'`, ts, ts, id); err != nil {
			return err
		}
	}
	return nil
}

// SetEpisodeState sets status, search note and the next search time (zero
// time clears it) for episodes.
func (s *Store) SetEpisodeState(ctx context.Context, ids []int64, status, note string, next time.Time) error {
	var nextVal any
	if !next.IsZero() {
		nextVal = next.UTC().Format(timeFormat)
	}
	ts := now()
	for _, id := range ids {
		if _, err := s.DB.ExecContext(ctx, `UPDATE media_items SET status = ?, search_note = ?, next_search_at = ?, updated_at = ? WHERE id = ?`,
			status, note, nextVal, ts, id); err != nil {
			return err
		}
	}
	return nil
}

// SetMonitored turns monitoring on or off for episodes. Turning it on queues
// a search.
func (s *Store) SetMonitored(ctx context.Context, ids []int64, on bool) error {
	ts := now()
	for _, id := range ids {
		q := `UPDATE media_items SET monitored = 0, next_search_at = NULL, updated_at = ? WHERE id = ?`
		if on {
			q = `UPDATE media_items SET monitored = 1, next_search_at = CASE WHEN status = 'imported' THEN NULL ELSE ? END, updated_at = ? WHERE id = ?`
			if _, err := s.DB.ExecContext(ctx, q, ts, ts, id); err != nil {
				return err
			}
			continue
		}
		if _, err := s.DB.ExecContext(ctx, q, ts, id); err != nil {
			return err
		}
	}
	return nil
}

// SeriesNeedingRefresh returns ids of shows whose metadata is older than age.
func (s *Store) SeriesNeedingRefresh(ctx context.Context, age time.Duration) ([]int64, error) {
	cutoff := time.Now().Add(-age).UTC().Format(timeFormat)
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM media_items WHERE media_type = 'tv' AND level = 'work'
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

// DeleteSeries forgets a show. Files on disk are not touched.
func (s *Store) DeleteSeries(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM media_items WHERE id = ? AND level = 'work'`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- Files ----

// AddFile records an imported file and marks the episode imported.
func (s *Store) AddFile(ctx context.Context, episodeID int64, path string, size int64, quality, releaseTitle string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// A replaced file (an upgrade, or a re-import) drops the old row.
	if _, err := tx.ExecContext(ctx, `DELETE FROM files WHERE media_item_id = ? OR path = ?`, episodeID, path); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO files (media_item_id, path, size_bytes, quality, release_title) VALUES (?, ?, ?, ?, ?)`,
		episodeID, path, size, quality, releaseTitle); err != nil {
		return mapErr(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE media_items SET status = 'imported', search_note = '', next_search_at = NULL, updated_at = ? WHERE id = ?`,
		now(), episodeID); err != nil {
		return err
	}
	return tx.Commit()
}

// ---- Blocklist ----

// Blocklist adds a release that failed so it is never grabbed again.
func (s *Store) Blocklist(ctx context.Context, mediaItemID int64, releaseTitle, reason string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO blocklist (media_item_id, release_title, reason) VALUES (?, ?, ?)`,
		nullable(mediaItemID), releaseTitle, reason)
	return err
}

// BlockedTitles returns every blocklisted release title for a show.
func (s *Store) BlockedTitles(ctx context.Context, seriesID int64) (map[string]bool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT release_title FROM blocklist WHERE media_item_id IN
		(SELECT ? UNION SELECT id FROM media_items WHERE parent_id = ?
		 UNION SELECT e.id FROM media_items e JOIN media_items sn ON sn.id = e.parent_id WHERE sn.parent_id = ?)`, seriesID, seriesID, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out[t] = true
	}
	return out, rows.Err()
}

func nullable(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// ---- Downloads ----

// Download is a grabbed release and its client job.
type Download struct {
	ID               int64   `json:"id"`
	MediaItemID      int64   `json:"mediaItemId"`
	DownloadClientID int64   `json:"downloadClientId"`
	ClientJobID      string  `json:"clientJobId"`
	ReleaseTitle     string  `json:"releaseTitle"`
	Protocol         string  `json:"protocol"`
	Indexer          string  `json:"indexer"`
	SizeBytes        int64   `json:"sizeBytes"`
	Quality          string  `json:"quality"`
	Status           string  `json:"status"`
	Progress         float64 `json:"progress"`
	OutputPath       string  `json:"outputPath,omitempty"`
	Error            string  `json:"error,omitempty"`
	CreatedAt        string  `json:"createdAt"`
	UpdatedAt        string  `json:"updatedAt"`

	// Filled for display: the show and what the download covers.
	// MediaType is the type of the item it is for (tv, book, ...).
	MediaType   string `json:"mediaType"`
	SeriesID    int64  `json:"seriesId"`
	SeriesTitle string `json:"seriesTitle"`
	PosterPath  string `json:"posterPath,omitempty"`
	Label       string `json:"label"` // "S01E02", "Season 1"
	ClientName  string `json:"clientName"`
}

const downloadCols = `d.id, COALESCE(d.media_item_id, 0), COALESCE(d.download_client_id, 0), d.client_job_id, d.release_title, d.protocol,
	d.indexer, d.size_bytes, d.quality, d.status, d.progress, d.output_path, d.error, d.created_at, d.updated_at,
	COALESCE(m.level, ''), COALESCE(m.number, 0), COALESCE(p.number, 0),
	COALESCE(CASE m.level WHEN 'work' THEN m.id WHEN 'unit' THEN p.id ELSE g.id END, 0),
	COALESCE(CASE m.level WHEN 'work' THEN m.title WHEN 'unit' THEN p.title ELSE g.title END, ''),
	COALESCE(CASE m.level WHEN 'work' THEN m.poster_path WHEN 'unit' THEN p.poster_path ELSE g.poster_path END, ''),
	COALESCE(c.name, ''), COALESCE(m.media_type, '')`

const downloadFrom = ` FROM downloads d
	LEFT JOIN media_items m ON m.id = d.media_item_id
	LEFT JOIN media_items p ON p.id = m.parent_id
	LEFT JOIN media_items g ON g.id = p.parent_id
	LEFT JOIN download_clients c ON c.id = d.download_client_id`

func scanDownload(row interface{ Scan(...any) error }) (*Download, error) {
	var d Download
	var level string
	var num, parentNum int
	if err := row.Scan(&d.ID, &d.MediaItemID, &d.DownloadClientID, &d.ClientJobID, &d.ReleaseTitle, &d.Protocol, &d.Indexer,
		&d.SizeBytes, &d.Quality, &d.Status, &d.Progress, &d.OutputPath, &d.Error, &d.CreatedAt, &d.UpdatedAt,
		&level, &num, &parentNum, &d.SeriesID, &d.SeriesTitle, &d.PosterPath, &d.ClientName, &d.MediaType); err != nil {
		return nil, mapErr(err)
	}
	switch level {
	case "work":
		switch d.MediaType {
		case "book":
			d.Label = "Ebook"
		case "audiobook":
			d.Label = "Audiobook"
		}
	case "unit":
		d.Label = seasonTitle(num)
	case "leaf":
		d.Label = "S" + pad2(parentNum) + "E" + pad2(num)
	}
	return &d, nil
}

func pad2(n int) string {
	if n < 10 && n >= 0 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

func (s *Store) queryDownloads(ctx context.Context, where string, args ...any) ([]Download, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+downloadCols+downloadFrom+` WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Download{}
	for rows.Next() {
		d, err := scanDownload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// CreateDownload records a grab.
func (s *Store) CreateDownload(ctx context.Context, d Download) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO downloads (media_item_id, download_client_id, client_job_id, release_title, protocol,
		indexer, size_bytes, quality, status) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullable(d.MediaItemID), nullable(d.DownloadClientID), d.ClientJobID, d.ReleaseTitle, d.Protocol, d.Indexer, d.SizeBytes, d.Quality, d.Status)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetDownload loads one download.
func (s *Store) GetDownload(ctx context.Context, id int64) (*Download, error) {
	ds, err := s.queryDownloads(ctx, `d.id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(ds) == 0 {
		return nil, ErrNotFound
	}
	return &ds[0], nil
}

// ActiveDownloads returns downloads that are not finished, oldest first.
func (s *Store) ActiveDownloads(ctx context.Context) ([]Download, error) {
	return s.queryDownloads(ctx, `d.status IN ('queued', 'downloading', 'completed', 'importing') ORDER BY d.id`)
}

// ActiveDownloadsOf returns the unfinished downloads for one media type.
// Each media type's pipeline polls only its own.
func (s *Store) ActiveDownloadsOf(ctx context.Context, mediaType string) ([]Download, error) {
	return s.queryDownloads(ctx, `d.status IN ('queued', 'downloading', 'completed', 'importing') AND m.media_type = ? ORDER BY d.id`, mediaType)
}

// DownloadHistory returns finished downloads, newest first.
func (s *Store) DownloadHistory(ctx context.Context, limit int) ([]Download, error) {
	return s.queryDownloads(ctx, `d.status IN ('imported', 'failed') ORDER BY d.updated_at DESC, d.id DESC LIMIT ?`, limit)
}

// UpdateDownload sets a download's progress and state.
func (s *Store) UpdateDownload(ctx context.Context, id int64, status string, progress float64, outputPath, errMsg string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE downloads SET status = ?, progress = ?, output_path = ?, error = ?, updated_at = ? WHERE id = ?`,
		status, progress, outputPath, errMsg, now(), id)
	return err
}

// ---- Requests ----

// Request is someone asking for a show.
type Request struct {
	ID        int64  `json:"id"`
	UserID    int64  `json:"userId"`
	Username  string `json:"username"`
	MediaType string `json:"type"`
	TMDBID    int64  `json:"tmdbId"`
	// ForeignID is the Open Library work for books and audiobooks.
	ForeignID   string `json:"bookId,omitempty"`
	Author      string `json:"author,omitempty"`
	Title       string `json:"title"`
	Year        int    `json:"year,omitempty"`
	PosterPath  string `json:"posterPath,omitempty"`
	Scope       string `json:"-"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	MediaItemID int64  `json:"mediaItemId,omitempty"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

const requestCols = `r.id, r.user_id, COALESCE(u.username, ''), r.media_type, r.tmdb_id, r.foreign_id, r.author, r.title, r.year, r.poster_path, r.scope, r.status, r.reason,
	COALESCE(r.media_item_id, 0), r.created_at, r.updated_at`

func scanRequest(row interface{ Scan(...any) error }) (*Request, error) {
	var r Request
	if err := row.Scan(&r.ID, &r.UserID, &r.Username, &r.MediaType, &r.TMDBID, &r.ForeignID, &r.Author, &r.Title, &r.Year, &r.PosterPath, &r.Scope, &r.Status, &r.Reason,
		&r.MediaItemID, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, mapErr(err)
	}
	return &r, nil
}

// CreateRequest stores a request.
func (s *Store) CreateRequest(ctx context.Context, r Request) (*Request, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO requests (user_id, media_type, tmdb_id, foreign_id, author, title, year, poster_path, scope, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.UserID, r.MediaType, r.TMDBID, r.ForeignID, r.Author, r.Title, r.Year, r.PosterPath, r.Scope, r.Status)
	if err != nil {
		return nil, mapErr(err)
	}
	id, _ := res.LastInsertId()
	return s.GetRequest(ctx, id)
}

// GetRequest loads one request.
func (s *Store) GetRequest(ctx context.Context, id int64) (*Request, error) {
	return scanRequest(s.DB.QueryRowContext(ctx, `SELECT `+requestCols+` FROM requests r LEFT JOIN users u ON u.id = r.user_id WHERE r.id = ?`, id))
}

// RequestFilter narrows ListRequests.
type RequestFilter struct {
	UserID int64 // 0 for everyone
	Status string
	TMDBID int64
	// MediaType and ForeignID find requests for a book or audiobook.
	MediaType string
	ForeignID string
}

// ListRequests returns requests, newest first.
func (s *Store) ListRequests(ctx context.Context, f RequestFilter) ([]Request, error) {
	where := []string{"1 = 1"}
	var args []any
	if f.UserID != 0 {
		where = append(where, "r.user_id = ?")
		args = append(args, f.UserID)
	}
	if f.Status != "" {
		where = append(where, "r.status = ?")
		args = append(args, f.Status)
	}
	if f.TMDBID != 0 {
		where = append(where, "r.tmdb_id = ?")
		args = append(args, f.TMDBID)
	}
	if f.MediaType != "" {
		where = append(where, "r.media_type = ?")
		args = append(args, f.MediaType)
	}
	if f.ForeignID != "" {
		where = append(where, "r.foreign_id = ?")
		args = append(args, f.ForeignID)
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT `+requestCols+` FROM requests r LEFT JOIN users u ON u.id = r.user_id
		WHERE `+strings.Join(where, " AND ")+` ORDER BY r.id DESC LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Request{}
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// SetRequestStatus changes a request's status, reason and linked show.
func (s *Store) SetRequestStatus(ctx context.Context, id int64, status, reason string, mediaItemID int64) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE requests SET status = ?, reason = ?, media_item_id = COALESCE(?, media_item_id), updated_at = ? WHERE id = ?`,
		status, reason, nullable(mediaItemID), now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkRequestsAvailable sets approved requests for a show to available
// once every monitored, aired episode has a file.
func (s *Store) MarkRequestsAvailable(ctx context.Context, seriesID int64) (bool, error) {
	sr, err := s.GetSeries(ctx, seriesID)
	if err != nil {
		return false, err
	}
	if sr.Missing > 0 || sr.Imported == 0 {
		return false, nil
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE requests SET status = 'available', updated_at = ? WHERE media_item_id = ? AND status = 'approved'`, now(), seriesID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteRequest removes a request.
func (s *Store) DeleteRequest(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM requests WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- Users (Jellyfin plugin identity) ----

// UserByJellyfinID finds the Bragi user mapped to a Jellyfin user, creating
// a requester account on first use.
func (s *Store) UserByJellyfinID(ctx context.Context, jellyfinID, name, apiKey string) (*User, error) {
	u, err := scanUser(s.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE jellyfin_user_id = ?`, jellyfinID))
	if !errors.Is(err, ErrNotFound) {
		return u, err
	}
	if name == "" {
		name = "jellyfin-" + jellyfinID
	}
	for i := 0; i < 5; i++ {
		candidate := name
		if i > 0 {
			candidate = name + "-" + itoa(i+1)
		}
		res, err := s.DB.ExecContext(ctx, `INSERT INTO users (username, role, api_key, jellyfin_user_id) VALUES (?, 'requester', ?, ?)`,
			candidate, apiKey, jellyfinID)
		if err != nil {
			if errors.Is(mapErr(err), ErrConflict) {
				continue
			}
			return nil, err
		}
		id, _ := res.LastInsertId()
		return s.GetUser(ctx, id)
	}
	return nil, ErrConflict
}
