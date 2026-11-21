package fakes

import (
	"fmt"
	"net/http"
)

// trendingTV serves TMDB's weekly trending shows: the whole fake catalogue.
func trendingTV(w http.ResponseWriter, r *http.Request) {
	results := []map[string]any{}
	for _, s := range Shows {
		results = append(results, map[string]any{"id": s.ID, "name": s.Name, "first_air_date": s.First,
			"overview": s.Overview, "poster_path": fmt.Sprintf("/%d.svg", s.ID), "media_type": "tv"})
	}
	writeJSON(w, map[string]any{"page": 1, "total_pages": 1, "results": results})
}
