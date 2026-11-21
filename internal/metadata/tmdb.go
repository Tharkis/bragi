// Package metadata looks up shows and movies on TMDB.
package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ErrNoKey means no TMDB API key is configured.
var ErrNoKey = errors.New("add a TMDB API key under Settings, Connections to search for shows")

// ErrBadKey means TMDB rejected the key.
var ErrBadKey = errors.New("TMDB rejected the API key")

// DefaultBaseURL is TMDB's v3 API.
const DefaultBaseURL = "https://api.themoviedb.org/3"

// ImageBase is where poster paths resolve. A variable so tests and demos
// can serve their own images.
var ImageBase = "https://image.tmdb.org/t/p/"

// TMDB is a small client for the parts of the TMDB API Bragi uses.
type TMDB struct {
	BaseURL string
	// Key is a v3 API key or a v4 read access token; both work.
	Key string
	HC  *http.Client
}

// Show is a search result or a series' details.
type Show struct {
	TMDBID     int      `json:"tmdbId"`
	TVDBID     int      `json:"tvdbId,omitempty"`
	IMDBID     string   `json:"imdbId,omitempty"`
	Title      string   `json:"title"`
	Year       int      `json:"year,omitempty"`
	FirstAired string   `json:"firstAired,omitempty"`
	Overview   string   `json:"overview"`
	PosterPath string   `json:"posterPath,omitempty"`
	Status     string   `json:"status,omitempty"` // Returning Series, Ended, ...
	Runtime    int      `json:"runtime,omitempty"`
	Seasons    []Season `json:"seasons,omitempty"`
}

// Season is one season of a show. Specials are season 0.
type Season struct {
	Number   int       `json:"number"`
	Title    string    `json:"title"`
	AirDate  string    `json:"airDate,omitempty"`
	Episodes []Episode `json:"episodes,omitempty"`
}

// Episode is one episode.
type Episode struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	AirDate string `json:"airDate,omitempty"`
	Runtime int    `json:"runtime,omitempty"`
}

func (t *TMDB) get(ctx context.Context, path string, q url.Values, out any) error {
	if strings.TrimSpace(t.Key) == "" {
		return ErrNoKey
	}
	base := t.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	if q == nil {
		q = url.Values{}
	}
	bearer := strings.Count(t.Key, ".") == 2 // v4 read access tokens are JWTs
	if !bearer {
		q.Set("api_key", t.Key)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	if bearer {
		req.Header.Set("Authorization", "Bearer "+t.Key)
	}
	req.Header.Set("Accept", "application/json")
	hc := t.HC
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach TMDB: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return ErrBadKey
	case http.StatusNotFound:
		return fmt.Errorf("TMDB has no such title")
	default:
		return fmt.Errorf("TMDB answered HTTP %d", resp.StatusCode)
	}
	return json.Unmarshal(body, out)
}

// Test checks the key works.
func (t *TMDB) Test(ctx context.Context) error {
	var out struct{}
	return t.get(ctx, "/configuration", nil, &out)
}

type tvResult struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	FirstAirDate string `json:"first_air_date"`
	Overview     string `json:"overview"`
	PosterPath   string `json:"poster_path"`
}

func yearOf(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, _ := strconv.Atoi(date[:4])
	return y
}

// SearchTV finds shows by name. year narrows the search when non-zero.
func (t *TMDB) SearchTV(ctx context.Context, query string, year int) ([]Show, error) {
	q := url.Values{"query": {query}, "include_adult": {"false"}}
	if year > 0 {
		q.Set("first_air_date_year", strconv.Itoa(year))
	}
	var res struct {
		Results []tvResult `json:"results"`
	}
	if err := t.get(ctx, "/search/tv", q, &res); err != nil {
		return nil, err
	}
	out := make([]Show, 0, len(res.Results))
	for _, r := range res.Results {
		out = append(out, Show{TMDBID: r.ID, Title: r.Name, Year: yearOf(r.FirstAirDate), FirstAired: r.FirstAirDate,
			Overview: r.Overview, PosterPath: r.PosterPath})
	}
	return out, nil
}

// Series returns a show. withEpisodes also loads every season's episodes,
// which costs one request per season.
func (t *TMDB) Series(ctx context.Context, tmdbID int, withEpisodes bool) (*Show, error) {
	var d struct {
		tvResult
		Status         string `json:"status"`
		EpisodeRunTime []int  `json:"episode_run_time"`
		Seasons        []struct {
			SeasonNumber int    `json:"season_number"`
			Name         string `json:"name"`
			AirDate      string `json:"air_date"`
		} `json:"seasons"`
		ExternalIDs struct {
			TVDBID int    `json:"tvdb_id"`
			IMDBID string `json:"imdb_id"`
		} `json:"external_ids"`
	}
	if err := t.get(ctx, "/tv/"+strconv.Itoa(tmdbID), url.Values{"append_to_response": {"external_ids"}}, &d); err != nil {
		return nil, err
	}
	s := &Show{TMDBID: d.ID, TVDBID: d.ExternalIDs.TVDBID, IMDBID: d.ExternalIDs.IMDBID, Title: d.Name,
		Year: yearOf(d.FirstAirDate), FirstAired: d.FirstAirDate, Overview: d.Overview, PosterPath: d.PosterPath, Status: d.Status}
	if len(d.EpisodeRunTime) > 0 {
		s.Runtime = d.EpisodeRunTime[0]
	}
	for _, sn := range d.Seasons {
		if !withEpisodes {
			s.Seasons = append(s.Seasons, Season{Number: sn.SeasonNumber, Title: sn.Name, AirDate: sn.AirDate})
			continue
		}
		var sd struct {
			Episodes []struct {
				EpisodeNumber int    `json:"episode_number"`
				Name          string `json:"name"`
				AirDate       string `json:"air_date"`
				Runtime       int    `json:"runtime"`
			} `json:"episodes"`
		}
		path := fmt.Sprintf("/tv/%d/season/%d", tmdbID, sn.SeasonNumber)
		if err := t.get(ctx, path, nil, &sd); err != nil {
			return nil, fmt.Errorf("season %d: %w", sn.SeasonNumber, err)
		}
		season := Season{Number: sn.SeasonNumber, Title: sn.Name, AirDate: sn.AirDate}
		for _, e := range sd.Episodes {
			season.Episodes = append(season.Episodes, Episode{Number: e.EpisodeNumber, Title: e.Name, AirDate: e.AirDate, Runtime: e.Runtime})
			if s.Runtime == 0 && e.Runtime > 0 {
				s.Runtime = e.Runtime
			}
		}
		s.Seasons = append(s.Seasons, season)
	}
	return s, nil
}
