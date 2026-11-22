package api

import "testing"

func TestTrendingMovies(t *testing.T) {
	e := newMovieEnv(t)
	var found []discoverMovie
	e.do("GET", "/api/v1/discover/search?type=movie&q=dune", nil, &found)
	if len(found) != 1 {
		t.Fatalf("search: %+v", found)
	}
	if code := e.do("POST", "/api/v1/requests", map[string]any{"type": "movie", "tmdbId": found[0].TMDBID}, nil); code != 201 {
		t.Fatalf("request: %d", code)
	}

	var trending []discoverMovie
	if code := e.do("GET", "/api/v1/discover/trending?type=movie", nil, &trending); code != 200 || len(trending) < 2 {
		t.Fatalf("trending movies: %d %+v", code, trending)
	}
	marked := false
	for _, m := range trending {
		if m.Type != "movie" || m.PosterURL == "" {
			t.Fatalf("trending movie: %+v", m)
		}
		if m.TMDBID == found[0].TMDBID {
			marked = m.MovieID != 0 && m.Requested == "approved"
		}
	}
	if !marked {
		t.Fatalf("requested movie not marked in trending: %+v", trending)
	}

	// Jellyseerr's movie discover list is the same trending list.
	var admin userResponse
	e.do("GET", "/api/v1/auth/me", nil, &admin)
	var page struct {
		Results []map[string]any `json:"results"`
	}
	if code := e.seerr("GET", "/discover/movies", admin.APIKey, 0, nil, &page); code != 200 || len(page.Results) != len(trending) {
		t.Fatalf("seerr discover movies: %d %d results, want %d", code, len(page.Results), len(trending))
	}
	if page.Results[0]["mediaType"] != "movie" {
		t.Fatalf("seerr movie: %+v", page.Results[0])
	}
}

func TestTrendingBooks(t *testing.T) {
	e := newBookEnv(t, false)
	if code := e.do("POST", "/api/v1/requests", map[string]any{"type": "book", "bookId": "OL1000002W"}, nil); code != 201 {
		t.Fatalf("request: %d", code)
	}
	for _, typ := range []string{"book", "audiobook"} {
		var list []bookResult
		if code := e.do("GET", "/api/v1/discover/trending?type="+typ, nil, &list); code != 200 || len(list) < 5 {
			t.Fatalf("trending %s: %d %+v", typ, code, list)
		}
		for _, b := range list {
			if b.Type != typ || b.Author == "" || b.CoverURL == "" {
				t.Fatalf("trending %s: %+v", typ, b)
			}
			if b.BookID != "OL1000002W" {
				continue
			}
			// Piranesi was asked for as an ebook only.
			if typ == "book" && (b.ItemID == 0 || b.Requested != "approved") {
				t.Fatalf("ebook not marked: %+v", b)
			}
			if typ == "audiobook" && (b.ItemID != 0 || b.Requested != "") {
				t.Fatalf("audiobook marked: %+v", b)
			}
		}
	}
	var types []string
	if code := e.do("GET", "/api/v1/discover/types", nil, &types); code != 200 || len(types) != 2 || types[0] != "book" || types[1] != "audiobook" {
		t.Fatalf("types: %d %v", code, types)
	}
	if code := e.do("GET", "/api/v1/discover/trending?type=comics", nil, nil); code != 400 {
		t.Fatalf("unknown type: %d, want 400", code)
	}
}
