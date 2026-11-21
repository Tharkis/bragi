package metadata

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

// Movie is a movie search result or a movie's details.
type Movie struct {
	TMDBID     int    `json:"tmdbId"`
	IMDBID     string `json:"imdbId,omitempty"`
	Title      string `json:"title"`
	Year       int    `json:"year,omitempty"`
	Overview   string `json:"overview"`
	PosterPath string `json:"posterPath,omitempty"`
	Runtime    int    `json:"runtime,omitempty"`
	Status     string `json:"status,omitempty"` // Released, In Production, ...
	// InCinemas is the first theatrical release date.
	InCinemas string `json:"inCinemas,omitempty"`
	// HomeRelease is the first digital or physical release date, if TMDB
	// knows one. Releases usually appear on indexers around then.
	HomeRelease string `json:"homeRelease,omitempty"`
}

// Available is the date from which Bragi searches for the movie: the home
// release if known, else 90 days after the theatrical release (when good
// copies usually appear; before that indexers mostly have cam recordings).
// "" means no date is announced.
func (m *Movie) Available() string {
	if m.HomeRelease != "" {
		return m.HomeRelease
	}
	t, err := time.Parse("2006-01-02", m.InCinemas)
	if err != nil {
		return ""
	}
	return t.AddDate(0, 0, 90).Format("2006-01-02")
}

type movieResult struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	ReleaseDate string `json:"release_date"`
	Overview    string `json:"overview"`
	PosterPath  string `json:"poster_path"`
}

// SearchMovies finds movies by title. year narrows the search when non-zero.
func (t *TMDB) SearchMovies(ctx context.Context, query string, year int) ([]Movie, error) {
	q := url.Values{"query": {query}, "include_adult": {"false"}}
	if year > 0 {
		q.Set("year", strconv.Itoa(year))
	}
	var res struct {
		Results []movieResult `json:"results"`
	}
	if err := t.get(ctx, "/search/movie", q, &res); err != nil {
		return nil, err
	}
	out := make([]Movie, 0, len(res.Results))
	for _, r := range res.Results {
		out = append(out, Movie{TMDBID: r.ID, Title: r.Title, Year: yearOf(r.ReleaseDate), InCinemas: r.ReleaseDate,
			Overview: r.Overview, PosterPath: r.PosterPath})
	}
	return out, nil
}

// Movie returns one movie with its IMDb id and release dates.
func (t *TMDB) Movie(ctx context.Context, tmdbID int) (*Movie, error) {
	var d struct {
		movieResult
		IMDBID       string `json:"imdb_id"`
		Runtime      int    `json:"runtime"`
		Status       string `json:"status"`
		ReleaseDates struct {
			Results []struct {
				Country  string `json:"iso_3166_1"`
				Releases []struct {
					Date string `json:"release_date"`
					Type int    `json:"type"`
				} `json:"release_dates"`
			} `json:"results"`
		} `json:"release_dates"`
	}
	if err := t.get(ctx, "/movie/"+strconv.Itoa(tmdbID), url.Values{"append_to_response": {"release_dates"}}, &d); err != nil {
		return nil, err
	}
	m := &Movie{TMDBID: d.ID, IMDBID: d.IMDBID, Title: d.Title, Year: yearOf(d.ReleaseDate), Overview: d.Overview,
		PosterPath: d.PosterPath, Runtime: d.Runtime, Status: d.Status, InCinemas: d.ReleaseDate}
	// TMDB release types: 1 premiere, 2 limited, 3 theatrical, 4 digital,
	// 5 physical, 6 TV. Take the earliest theatrical and home dates
	// anywhere in the world.
	earliest := func(cur, date string) string {
		if len(date) >= 10 {
			date = date[:10]
		}
		if date != "" && (cur == "" || date < cur) {
			return date
		}
		return cur
	}
	theatrical := ""
	for _, c := range d.ReleaseDates.Results {
		for _, r := range c.Releases {
			switch r.Type {
			case 2, 3:
				theatrical = earliest(theatrical, r.Date)
			case 4, 5:
				m.HomeRelease = earliest(m.HomeRelease, r.Date)
			}
		}
	}
	if theatrical != "" {
		m.InCinemas = theatrical
	}
	return m, nil
}
