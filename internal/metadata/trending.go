package metadata

import (
	"context"
	"net/url"
	"strconv"
)

// TrendingTV returns one page of this week's trending shows and the number
// of pages TMDB has. Pages start at 1.
func (t *TMDB) TrendingTV(ctx context.Context, page int) ([]Show, int, error) {
	if page < 1 {
		page = 1
	}
	var res struct {
		Results    []tvResult `json:"results"`
		TotalPages int        `json:"total_pages"`
	}
	if err := t.get(ctx, "/trending/tv/week", url.Values{"page": {strconv.Itoa(page)}}, &res); err != nil {
		return nil, 0, err
	}
	out := make([]Show, 0, len(res.Results))
	for _, r := range res.Results {
		out = append(out, Show{TMDBID: r.ID, Title: r.Name, Year: yearOf(r.FirstAirDate), FirstAired: r.FirstAirDate,
			Overview: r.Overview, PosterPath: r.PosterPath})
	}
	return out, res.TotalPages, nil
}

// TrendingMovies returns one page of this week's trending movies and the
// number of pages TMDB has. Pages start at 1.
func (t *TMDB) TrendingMovies(ctx context.Context, page int) ([]Movie, int, error) {
	if page < 1 {
		page = 1
	}
	var res struct {
		Results    []movieResult `json:"results"`
		TotalPages int           `json:"total_pages"`
	}
	if err := t.get(ctx, "/trending/movie/week", url.Values{"page": {strconv.Itoa(page)}}, &res); err != nil {
		return nil, 0, err
	}
	out := make([]Movie, 0, len(res.Results))
	for _, r := range res.Results {
		out = append(out, Movie{TMDBID: r.ID, Title: r.Title, Year: yearOf(r.ReleaseDate), InCinemas: r.ReleaseDate,
			Overview: r.Overview, PosterPath: r.PosterPath})
	}
	return out, res.TotalPages, nil
}
