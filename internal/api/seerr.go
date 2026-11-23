package api

// A Jellyseerr-compatible slice of the API, served under /seerr/api/v1, so
// tools written for Jellyseerr (such as the Jellyfin Enhanced plugin) can
// search and request through Bragi. Point them at http://bragi:8787/seerr
// with a Bragi admin API key.
//
// Only what request clients use is covered: status, users, search, show and
// movie details, and requests. Everything is translated to Bragi's own model;
// creating, approving and cancelling requests goes through Bragi's own
// /api/v1/requests handlers so both APIs behave the same.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tharkis/bragi/internal/auth"
	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/store"
	"github.com/Tharkis/bragi/internal/tv"
)

const seerrBase = "/seerr/api/v1"

// Jellyseerr's media and request status codes.
const (
	seerrMediaUnknown    = 1
	seerrMediaPending    = 2
	seerrMediaProcessing = 3
	seerrMediaPartial    = 4
	seerrMediaAvailable  = 5

	seerrRequestPending   = 1
	seerrRequestApproved  = 2
	seerrRequestDeclined  = 3
	seerrRequestCompleted = 5
)

// Jellyseerr permission bits Bragi maps its roles to.
const (
	seerrPermAdmin       = 2
	seerrPermRequest     = 32
	seerrPermAutoApprove = 128
)

// seerrUserTypeJellyfin marks users that come from Jellyfin.
const (
	seerrUserTypeLocal    = 2
	seerrUserTypeJellyfin = 3
)

func (s *Server) seerrRoutes() {
	m := s.mux
	user := func(h http.HandlerFunc) http.HandlerFunc { return s.seerrAuth(false, h) }
	admin := func(h http.HandlerFunc) http.HandlerFunc { return s.seerrAuth(true, h) }

	m.HandleFunc("GET "+seerrBase+"/status", s.seerrStatus)
	m.HandleFunc("GET "+seerrBase+"/settings/public", s.seerrPublicSettings)
	m.HandleFunc("GET "+seerrBase+"/settings/main", admin(s.seerrMainSettings))
	m.HandleFunc("GET "+seerrBase+"/auth/me", user(s.seerrMe))

	m.HandleFunc("GET "+seerrBase+"/user", admin(s.seerrListUsers))
	m.HandleFunc("POST "+seerrBase+"/user/import-from-jellyfin", admin(s.seerrImportUsers))
	m.HandleFunc("GET "+seerrBase+"/user/{id}", user(s.seerrGetUser))
	m.HandleFunc("GET "+seerrBase+"/user/{id}/quota", user(s.seerrQuota))
	m.HandleFunc("GET "+seerrBase+"/user/{id}/settings/main", user(s.seerrUserSettings))

	m.HandleFunc("GET "+seerrBase+"/search", user(s.seerrSearch))
	m.HandleFunc("GET "+seerrBase+"/discover/trending", user(s.seerrTrending))
	m.HandleFunc("GET "+seerrBase+"/discover/tv", user(s.seerrTrending))
	m.HandleFunc("GET "+seerrBase+"/discover/movies", user(s.seerrTrendingMovies))
	m.HandleFunc("GET "+seerrBase+"/tv/{id}", user(s.seerrTV))
	m.HandleFunc("GET "+seerrBase+"/tv/{id}/season/{season}", user(s.seerrSeason))
	m.HandleFunc("GET "+seerrBase+"/movie/{id}", user(s.seerrMovie))

	m.HandleFunc("GET "+seerrBase+"/request", user(s.seerrListRequests))
	m.HandleFunc("GET "+seerrBase+"/request/count", user(s.seerrRequestCount))
	m.HandleFunc("POST "+seerrBase+"/request", user(s.seerrCreateRequest))
	m.HandleFunc("GET "+seerrBase+"/request/{id}", user(s.seerrGetRequest))
	m.HandleFunc("DELETE "+seerrBase+"/request/{id}", user(s.seerrDeleteRequest))
	m.HandleFunc("POST "+seerrBase+"/request/{id}/{action}", admin(s.seerrRequestAction))

	// Bragi picks the downloader and quality itself, so there are no
	// Sonarr or Radarr servers to choose from.
	m.HandleFunc("GET "+seerrBase+"/service/sonarr", user(s.seerrEmptyList))
	m.HandleFunc("GET "+seerrBase+"/service/radarr", user(s.seerrEmptyList))

	m.HandleFunc("/seerr/", func(w http.ResponseWriter, r *http.Request) {
		seerrError(w, http.StatusNotFound, "Bragi does not support this Jellyseerr endpoint")
	})
}

// seerrAuth requires a signed-in user. Like Jellyseerr, an admin API key may
// act for another user by sending that user's id in X-Api-User.
func (s *Server) seerrAuth(adminOnly bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := userFrom(r.Context())
		if u == nil {
			seerrError(w, http.StatusForbidden, "You do not have permission to access this endpoint")
			return
		}
		if as := strings.TrimSpace(r.Header.Get("X-Api-User")); as != "" && u.Role == "admin" && r.Header.Get("X-Api-Key") != "" {
			id, err := strconv.ParseInt(as, 10, 64)
			if err != nil {
				seerrError(w, http.StatusBadRequest, "X-Api-User must be a user id")
				return
			}
			other, err := s.store.GetUser(r.Context(), id)
			if err != nil {
				seerrError(w, http.StatusNotFound, "User not found")
				return
			}
			u = other
			r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, u))
		}
		if adminOnly && u.Role != "admin" {
			seerrError(w, http.StatusForbidden, "You do not have permission to access this endpoint")
			return
		}
		h(w, r)
	}
}

func seerrError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"message": msg, "status": status})
}

// ---- Status and settings ----

func (s *Server) seerrStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"version": "bragi-" + s.version, "commitTag": s.version,
		"updateAvailable": false, "commitsBehind": 0, "restartRequired": false})
}

func (s *Server) seerrPublicSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"initialized": true, "applicationTitle": "Bragi", "applicationUrl": "", "mediaServerType": 2,
		"jellyfinServerName": "", "localLogin": true, "movie4kEnabled": false, "series4kEnabled": false,
		"partialRequestsEnabled": true, "enableSpecialEpisodes": false, "region": "", "originalLanguage": "", "locale": "en",
	})
}

func (s *Server) seerrMainSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"applicationTitle": "Bragi", "applicationUrl": "", "defaultPermissions": seerrPermRequest, "defaultQuotas": map[string]any{},
		"hideAvailable": false, "localLogin": true, "newPlexLogin": false, "region": "", "originalLanguage": "",
		"partialRequestsEnabled": true, "enableSpecialEpisodes": false, "locale": "en",
	})
}

// ---- Users ----

type seerrUser struct {
	ID               int64  `json:"id"`
	Email            string `json:"email"`
	Username         string `json:"username"`
	DisplayName      string `json:"displayName"`
	JellyfinUsername string `json:"jellyfinUsername,omitempty"`
	JellyfinUserID   string `json:"jellyfinUserId,omitempty"`
	UserType         int    `json:"userType"`
	Permissions      int    `json:"permissions"`
	Avatar           string `json:"avatar"`
	RequestCount     int    `json:"requestCount"`
	MovieQuotaLimit  *int   `json:"movieQuotaLimit"`
	MovieQuotaDays   *int   `json:"movieQuotaDays"`
	TVQuotaLimit     *int   `json:"tvQuotaLimit"`
	TVQuotaDays      *int   `json:"tvQuotaDays"`
	CreatedAt        string `json:"createdAt"`
	UpdatedAt        string `json:"updatedAt"`
}

// normalizeJellyfinID writes a Jellyfin user id the way Jellyseerr stores it:
// lower case, without dashes.
func normalizeJellyfinID(id string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(id), "-", ""))
}

func toSeerrUser(u store.User, requests int) seerrUser {
	out := seerrUser{ID: u.ID, Email: u.Username, Username: u.Username, DisplayName: u.Username, UserType: seerrUserTypeLocal,
		Permissions: seerrPermRequest, RequestCount: requests, CreatedAt: u.CreatedAt, UpdatedAt: u.CreatedAt}
	if u.JellyfinUserID != "" {
		out.UserType = seerrUserTypeJellyfin
		out.JellyfinUserID = normalizeJellyfinID(u.JellyfinUserID)
		out.JellyfinUsername = u.Username
	}
	switch {
	case u.Role == "admin":
		out.Permissions = seerrPermAdmin
	case u.AutoApprove:
		out.Permissions |= seerrPermAutoApprove
	}
	return out
}

func (s *Server) seerrMe(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	counts, _ := s.store.CountRequestsByUser(r.Context())
	writeJSON(w, http.StatusOK, toSeerrUser(*u, counts[u.ID]))
}

type seerrPageInfo struct {
	Pages    int `json:"pages"`
	PageSize int `json:"pageSize"`
	Results  int `json:"results"`
	Page     int `json:"page"`
}

// pageArgs reads Jellyseerr's take and skip parameters.
func pageArgs(r *http.Request, def int) (take, skip int) {
	take, _ = strconv.Atoi(r.URL.Query().Get("take"))
	skip, _ = strconv.Atoi(r.URL.Query().Get("skip"))
	if take <= 0 {
		take = def
	}
	if take > 1000 {
		take = 1000
	}
	if skip < 0 {
		skip = 0
	}
	return take, skip
}

func pageOf(total, take, skip int) seerrPageInfo {
	pages := (total + take - 1) / take
	return seerrPageInfo{Pages: pages, PageSize: take, Results: total, Page: skip/take + 1}
}

func (s *Server) seerrListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		s.storeError(w, err, "users")
		return
	}
	counts, _ := s.store.CountRequestsByUser(r.Context())
	take, skip := pageArgs(r, 10)
	out := []seerrUser{}
	for i := skip; i < len(users) && i < skip+take; i++ {
		out = append(out, toSeerrUser(users[i], counts[users[i].ID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"pageInfo": pageOf(len(users), take, skip), "results": out})
}

func (s *Server) seerrUserFromPath(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		seerrError(w, http.StatusBadRequest, "Invalid user id")
		return nil, false
	}
	me := userFrom(r.Context())
	if me.Role != "admin" && me.ID != id {
		seerrError(w, http.StatusForbidden, "You do not have permission to view this user")
		return nil, false
	}
	u, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		seerrError(w, http.StatusNotFound, "User not found")
		return nil, false
	}
	return u, true
}

func (s *Server) seerrGetUser(w http.ResponseWriter, r *http.Request) {
	u, ok := s.seerrUserFromPath(w, r)
	if !ok {
		return
	}
	counts, _ := s.store.CountRequestsByUser(r.Context())
	writeJSON(w, http.StatusOK, toSeerrUser(*u, counts[u.ID]))
}

// seerrQuota reports no limits: Bragi has no request quotas yet.
func (s *Server) seerrQuota(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.seerrUserFromPath(w, r); !ok {
		return
	}
	unlimited := map[string]any{"days": 0, "limit": 0, "used": 0, "remaining": nil, "restricted": false}
	writeJSON(w, http.StatusOK, map[string]any{"movie": unlimited, "tv": unlimited})
}

func (s *Server) seerrUserSettings(w http.ResponseWriter, r *http.Request) {
	u, ok := s.seerrUserFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"username": u.Username, "email": u.Username, "discordId": nil,
		"locale": "", "discoverRegion": "", "streamingRegion": "", "originalLanguage": nil, "movieQuotaLimit": nil,
		"movieQuotaDays": nil, "tvQuotaLimit": nil, "tvQuotaDays": nil, "watchlistSyncMovies": false, "watchlistSyncTv": false})
}

// seerrImportUsers creates requester accounts for Jellyfin users, named after
// their Jellyfin name when Bragi's Jellyfin connection is set up.
func (s *Server) seerrImportUsers(w http.ResponseWriter, r *http.Request) {
	var in struct {
		JellyfinUserIDs []string `json:"jellyfinUserIds"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	out := []seerrUser{}
	for _, raw := range in.JellyfinUserIDs {
		id := normalizeJellyfinID(raw)
		if id == "" {
			continue
		}
		u, err := s.userForJellyfinID(r.Context(), id, "")
		if err != nil {
			s.log.Error("importing Jellyfin user failed", "err", err)
			seerrError(w, http.StatusInternalServerError, "Could not import the Jellyfin user")
			return
		}
		out = append(out, toSeerrUser(*u, 0))
	}
	writeJSON(w, http.StatusCreated, out)
}

// userForJellyfinID finds or creates the Bragi user for a Jellyfin user. It
// matches ids with or without dashes, since the plugin and Jellyseerr
// clients write them differently.
func (s *Server) userForJellyfinID(ctx context.Context, id, name string) (*store.User, error) {
	norm := normalizeJellyfinID(id)
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	for i := range users {
		if users[i].JellyfinUserID != "" && normalizeJellyfinID(users[i].JellyfinUserID) == norm {
			return &users[i], nil
		}
	}
	if name == "" {
		lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		name, _ = s.tv.JellyfinUserName(lctx, norm)
		cancel()
	}
	return s.store.UserByJellyfinID(ctx, norm, name, auth.NewAPIKey())
}

// ---- Search and details ----

type seerrPage struct {
	Page         int   `json:"page"`
	TotalPages   int   `json:"totalPages"`
	TotalResults int   `json:"totalResults"`
	Results      []any `json:"results"`
}

type seerrTVResult struct {
	ID               int64       `json:"id"`
	MediaType        string      `json:"mediaType"`
	Name             string      `json:"name"`
	OriginalName     string      `json:"originalName"`
	FirstAirDate     string      `json:"firstAirDate"`
	Overview         string      `json:"overview"`
	PosterPath       string      `json:"posterPath,omitempty"`
	BackdropPath     *string     `json:"backdropPath"`
	GenreIDs         []int       `json:"genreIds"`
	OriginCountry    []string    `json:"originCountry"`
	OriginalLanguage string      `json:"originalLanguage"`
	Popularity       float64     `json:"popularity"`
	VoteAverage      float64     `json:"voteAverage"`
	VoteCount        int         `json:"voteCount"`
	MediaInfo        *seerrMedia `json:"mediaInfo,omitempty"`
}

type seerrMovieResult struct {
	ID               int64       `json:"id"`
	MediaType        string      `json:"mediaType"`
	Title            string      `json:"title"`
	OriginalTitle    string      `json:"originalTitle"`
	ReleaseDate      string      `json:"releaseDate"`
	Overview         string      `json:"overview"`
	PosterPath       string      `json:"posterPath,omitempty"`
	BackdropPath     *string     `json:"backdropPath"`
	GenreIDs         []int       `json:"genreIds"`
	OriginalLanguage string      `json:"originalLanguage"`
	Adult            bool        `json:"adult"`
	Video            bool        `json:"video"`
	Popularity       float64     `json:"popularity"`
	VoteAverage      float64     `json:"voteAverage"`
	VoteCount        int         `json:"voteCount"`
	MediaInfo        *seerrMedia `json:"mediaInfo,omitempty"`
}

func (s *Server) seerrTVResult(ctx context.Context, sh metadata.Show) seerrTVResult {
	return seerrTVResult{ID: int64(sh.TMDBID), MediaType: "tv", Name: sh.Title, OriginalName: sh.Title, FirstAirDate: sh.FirstAired,
		Overview: sh.Overview, PosterPath: sh.PosterPath, GenreIDs: []int{}, OriginCountry: []string{},
		MediaInfo: s.seerrTVMedia(ctx, int64(sh.TMDBID))}
}

func (s *Server) seerrSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("query"))
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	out := seerrPage{Page: page, TotalPages: 1, Results: []any{}}
	if q == "" || page > 1 {
		writeJSON(w, http.StatusOK, out)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	shows, err := s.tv.TMDB(ctx).SearchTV(ctx, q, 0)
	if err != nil {
		s.seerrMetadataError(w, err)
		return
	}
	for _, sh := range shows {
		out.Results = append(out.Results, s.seerrTVResult(ctx, sh))
	}
	// Movies come from Bragi's own discover API, once Bragi handles them.
	var movies []map[string]any
	if s.internalCall(r, http.MethodGet, "/api/v1/discover/search?type=movie&q="+url.QueryEscape(q), nil, &movies) == http.StatusOK {
		for _, mv := range movies {
			out.Results = append(out.Results, s.seerrMovieFromDiscover(ctx, mv))
		}
	}
	out.TotalResults = len(out.Results)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) seerrTrending(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	shows, pages, err := s.tv.TMDB(ctx).TrendingTV(ctx, page)
	if err != nil {
		s.seerrMetadataError(w, err)
		return
	}
	out := seerrPage{Page: page, TotalPages: pages, Results: []any{}}
	for _, sh := range shows {
		out.Results = append(out.Results, s.seerrTVResult(ctx, sh))
	}
	out.TotalResults = len(out.Results)
	writeJSON(w, http.StatusOK, out)
}

// seerrTrendingMovies answers Jellyseerr's movie discover list with this
// week's trending movies from Bragi's own discover API.
func (s *Server) seerrTrendingMovies(w http.ResponseWriter, r *http.Request) {
	out := seerrPage{Page: 1, TotalPages: 1, Results: []any{}}
	var movies []map[string]any
	if s.internalCall(r, http.MethodGet, "/api/v1/discover/trending?type=movie", nil, &movies) == http.StatusOK {
		for _, mv := range movies {
			out.Results = append(out.Results, s.seerrMovieFromDiscover(r.Context(), mv))
		}
	}
	out.TotalResults = len(out.Results)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) seerrEmptyList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, []any{})
}

func (s *Server) seerrMetadataError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, metadata.ErrNoKey), errors.Is(err, metadata.ErrBadKey):
		seerrError(w, http.StatusInternalServerError, "Bragi has no working TMDB API key; add one under Settings, Connections")
	case errors.Is(err, store.ErrNotFound):
		seerrError(w, http.StatusNotFound, "Not found")
	default:
		s.log.Warn("metadata error", "err", err)
		seerrError(w, http.StatusInternalServerError, "Could not reach TMDB")
	}
}

type seerrSeason struct {
	ID           int64  `json:"id"`
	SeasonNumber int    `json:"seasonNumber"`
	Name         string `json:"name"`
	EpisodeCount int    `json:"episodeCount"`
	AirDate      string `json:"airDate,omitempty"`
	Overview     string `json:"overview"`
	PosterPath   string `json:"posterPath,omitempty"`
}

func (s *Server) seerrTV(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < 1 {
		seerrError(w, http.StatusBadRequest, "Invalid id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	sh, err := s.tv.TMDB(ctx).Series(ctx, id, false)
	if err != nil {
		s.seerrMetadataError(w, err)
		return
	}
	// Episode counts come from the library when Bragi tracks the show.
	counts := map[int]int{}
	if sr, err := s.store.GetSeriesByTMDB(ctx, int64(id)); err == nil {
		if seasons, err := s.store.Seasons(ctx, sr.ID); err == nil {
			for _, sn := range seasons {
				counts[sn.Number] = len(sn.Episodes)
			}
		}
	}
	seasons := []seerrSeason{}
	for _, sn := range sh.Seasons {
		seasons = append(seasons, seerrSeason{ID: int64(id)*1000 + int64(sn.Number), SeasonNumber: sn.Number, Name: sn.Title,
			EpisodeCount: counts[sn.Number], AirDate: sn.AirDate})
	}
	numSeasons := 0
	for _, sn := range sh.Seasons {
		if sn.Number > 0 {
			numSeasons++
		}
	}
	var tvdb any
	if sh.TVDBID > 0 {
		tvdb = sh.TVDBID
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": sh.TMDBID, "name": sh.Title, "originalName": sh.Title, "firstAirDate": sh.FirstAired, "overview": sh.Overview,
		"posterPath": sh.PosterPath, "backdropPath": nil, "status": sh.Status, "numberOfSeasons": numSeasons,
		"episodeRunTime": []int{sh.Runtime}, "seasons": seasons, "genres": []any{}, "networks": []any{}, "credits": map[string]any{"cast": []any{}, "crew": []any{}},
		"externalIds": map[string]any{"tvdbId": tvdb, "imdbId": sh.IMDBID}, "keywords": []any{}, "relatedVideos": []any{},
		"mediaInfo": s.seerrTVMedia(ctx, int64(sh.TMDBID)),
	})
}

// seerrSeason lists a season's episodes from the library; shows Bragi does
// not track yet return the season with no episodes.
func (s *Server) seerrSeason(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	n, err2 := strconv.Atoi(r.PathValue("season"))
	if err != nil || err2 != nil {
		seerrError(w, http.StatusBadRequest, "Invalid id")
		return
	}
	episodes := []map[string]any{}
	if sr, err := s.store.GetSeriesByTMDB(r.Context(), id); err == nil {
		if seasons, err := s.store.Seasons(r.Context(), sr.ID); err == nil {
			for _, sn := range seasons {
				if sn.Number != n {
					continue
				}
				for _, e := range sn.Episodes {
					episodes = append(episodes, map[string]any{"id": e.ID, "episodeNumber": e.Number, "seasonNumber": e.Season,
						"name": e.Title, "airDate": e.AirDate, "overview": "", "stillPath": nil})
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id*1000 + int64(n), "seasonNumber": n, "name": "Season " + strconv.Itoa(n),
		"episodes": episodes})
}

func (s *Server) seerrMovie(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		seerrError(w, http.StatusBadRequest, "Invalid id")
		return
	}
	var mv map[string]any
	if code := s.internalCall(r, http.MethodGet, "/api/v1/discover/movie/"+strconv.FormatInt(id, 10), nil, &mv); code != http.StatusOK {
		seerrError(w, http.StatusNotFound, "Movies are not supported by this Bragi version")
		return
	}
	writeJSON(w, http.StatusOK, s.seerrMovieFromDiscover(r.Context(), mv))
}

// seerrMovieFromDiscover maps a movie from Bragi's discover API.
func (s *Server) seerrMovieFromDiscover(ctx context.Context, mv map[string]any) seerrMovieResult {
	num := func(k string) int64 { f, _ := mv[k].(float64); return int64(f) }
	str := func(k string) string { v, _ := mv[k].(string); return v }
	id := num("tmdbId")
	out := seerrMovieResult{ID: id, MediaType: "movie", Title: str("title"), OriginalTitle: str("title"), Overview: str("overview"),
		PosterPath: tmdbPath(str("posterUrl")), GenreIDs: []int{}}
	if y := num("year"); y > 0 {
		out.ReleaseDate = strconv.FormatInt(y, 10) + "-01-01"
	}
	if d := str("releaseDate"); d != "" {
		out.ReleaseDate = d
	}
	out.MediaInfo = s.seerrMovieMedia(ctx, id, num("movieId") > 0)
	return out
}

// tmdbPath turns a full TMDB image URL back into the path Jellyseerr
// clients expect, such as /abc.jpg.
func tmdbPath(u string) string {
	if !strings.HasPrefix(u, metadata.ImageBase) {
		return u
	}
	rest := strings.TrimPrefix(u, metadata.ImageBase)
	if i := strings.Index(rest, "/"); i >= 0 {
		return rest[i:]
	}
	return ""
}

// ---- Media status ----

type seerrSeasonStatus struct {
	ID           int64  `json:"id"`
	SeasonNumber int    `json:"seasonNumber"`
	Status       int    `json:"status"`
	Status4K     int    `json:"status4k"`
	CreatedAt    string `json:"createdAt,omitempty"`
	UpdatedAt    string `json:"updatedAt,omitempty"`
}

type seerrMedia struct {
	ID              int64               `json:"id"`
	TMDBID          int64               `json:"tmdbId"`
	TVDBID          *int64              `json:"tvdbId"`
	IMDBID          *string             `json:"imdbId"`
	MediaType       string              `json:"mediaType"`
	Status          int                 `json:"status"`
	Status4K        int                 `json:"status4k"`
	Seasons         []seerrSeasonStatus `json:"seasons"`
	Requests        []seerrRequest      `json:"requests"`
	JellyfinMediaID *string             `json:"jellyfinMediaId"`
	CreatedAt       string              `json:"createdAt"`
	UpdatedAt       string              `json:"updatedAt"`
}

// openRequests returns the requests for one title that still matter.
func (s *Server) openRequests(ctx context.Context, mediaType string, tmdbID int64) []store.Request {
	all, err := s.store.ListRequests(ctx, store.RequestFilter{TMDBID: tmdbID})
	if err != nil {
		return nil
	}
	var out []store.Request
	for _, rq := range all {
		if rq.MediaType == mediaType && rq.Status != "denied" {
			out = append(out, rq)
		}
	}
	return out
}

// seriesSeasonStatus works out each season's status from its episodes.
func seriesSeasonStatus(seasons []store.Season) ([]seerrSeasonStatus, int) {
	today := store.Today()
	out := []seerrSeasonStatus{}
	anyFile, allDone, any := false, true, false
	for _, sn := range seasons {
		if sn.Number == 0 {
			continue
		}
		aired, have := 0, 0
		for _, e := range sn.Episodes {
			if e.AirDate != "" && e.AirDate <= today {
				aired++
			}
			if e.Status == "imported" || e.FilePath != "" {
				have++
			}
		}
		st := seerrMediaUnknown
		switch {
		case have > 0 && have >= aired:
			st = seerrMediaAvailable
		case have > 0:
			st = seerrMediaPartial
		case sn.Monitored:
			st = seerrMediaProcessing
		}
		if have > 0 {
			anyFile = true
		}
		if st != seerrMediaAvailable {
			allDone = false
		}
		any = true
		out = append(out, seerrSeasonStatus{ID: sn.ID, SeasonNumber: sn.Number, Status: st, Status4K: seerrMediaUnknown})
	}
	switch {
	case any && allDone:
		return out, seerrMediaAvailable
	case anyFile:
		return out, seerrMediaPartial
	default:
		return out, seerrMediaProcessing
	}
}

// seerrTVMedia describes what Bragi has of a show, or nil when Bragi neither
// tracks it nor has a request for it.
func (s *Server) seerrTVMedia(ctx context.Context, tmdbID int64) *seerrMedia {
	reqs := s.openRequests(ctx, "tv", tmdbID)
	sr, err := s.store.GetSeriesByTMDB(ctx, tmdbID)
	if err != nil && len(reqs) == 0 {
		return nil
	}
	m := &seerrMedia{ID: tmdbID, TMDBID: tmdbID, MediaType: "tv", Status: seerrMediaPending, Status4K: seerrMediaUnknown,
		Seasons: []seerrSeasonStatus{}, Requests: []seerrRequest{}}
	if sr != nil {
		m.ID = sr.ID
		m.CreatedAt, m.UpdatedAt = sr.CreatedAt, sr.CreatedAt
		if sr.TVDBID > 0 {
			m.TVDBID = &sr.TVDBID
		}
		if sr.IMDBID != "" {
			m.IMDBID = &sr.IMDBID
		}
		if seasons, err := s.store.Seasons(ctx, sr.ID); err == nil {
			m.Seasons, m.Status = seriesSeasonStatus(seasons)
		}
	} else {
		for _, rq := range reqs {
			if rq.Status == "approved" {
				m.Status = seerrMediaProcessing
			}
		}
	}
	users := map[int64]*store.User{}
	for _, rq := range reqs {
		m.Requests = append(m.Requests, s.toSeerrRequest(ctx, rq, users, false))
	}
	return m
}

// seerrMovieMedia describes a movie from its requests. inLibrary marks a
// movie Bragi already tracks.
func (s *Server) seerrMovieMedia(ctx context.Context, tmdbID int64, inLibrary bool) *seerrMedia {
	reqs := s.openRequests(ctx, "movie", tmdbID)
	if len(reqs) == 0 && !inLibrary {
		return nil
	}
	m := &seerrMedia{ID: tmdbID, TMDBID: tmdbID, MediaType: "movie", Status: seerrMediaProcessing, Status4K: seerrMediaUnknown,
		Seasons: []seerrSeasonStatus{}, Requests: []seerrRequest{}}
	if len(reqs) > 0 {
		m.Status = movieStatus(reqs[0].Status)
	}
	users := map[int64]*store.User{}
	for _, rq := range reqs {
		m.Requests = append(m.Requests, s.toSeerrRequest(ctx, rq, users, false))
	}
	return m
}

func movieStatus(requestStatus string) int {
	switch requestStatus {
	case "pending":
		return seerrMediaPending
	case "available":
		return seerrMediaAvailable
	default:
		return seerrMediaProcessing
	}
}

// ---- Requests ----

type seerrRequest struct {
	ID          int64               `json:"id"`
	Status      int                 `json:"status"`
	Type        string              `json:"type"`
	Is4K        bool                `json:"is4k"`
	Media       *seerrMedia         `json:"media,omitempty"`
	RequestedBy *seerrUser          `json:"requestedBy"`
	ModifiedBy  *seerrUser          `json:"modifiedBy"`
	Seasons     []seerrSeasonStatus `json:"seasons"`
	SeasonCount int                 `json:"seasonCount"`
	ServerID    *int                `json:"serverId"`
	ProfileID   *int                `json:"profileId"`
	RootFolder  *string             `json:"rootFolder"`
	CreatedAt   string              `json:"createdAt"`
	UpdatedAt   string              `json:"updatedAt"`
}

func seerrRequestStatus(status string) int {
	switch status {
	case "pending":
		return seerrRequestPending
	case "denied":
		return seerrRequestDeclined
	case "available":
		return seerrRequestCompleted
	default:
		return seerrRequestApproved
	}
}

// toSeerrRequest maps a Bragi request. users caches requesters across a list.
func (s *Server) toSeerrRequest(ctx context.Context, rq store.Request, users map[int64]*store.User, withMedia bool) seerrRequest {
	out := seerrRequest{ID: rq.ID, Status: seerrRequestStatus(rq.Status), Type: rq.MediaType, Seasons: []seerrSeasonStatus{},
		CreatedAt: rq.CreatedAt, UpdatedAt: rq.UpdatedAt}
	u, ok := users[rq.UserID]
	if !ok {
		u, _ = s.store.GetUser(ctx, rq.UserID)
		users[rq.UserID] = u
	}
	if u != nil {
		su := toSeerrUser(*u, 0)
		out.RequestedBy = &su
	} else {
		out.RequestedBy = &seerrUser{ID: rq.UserID, Username: rq.Username, DisplayName: rq.Username, Email: rq.Username}
	}
	if rq.MediaType == "tv" {
		var scope tv.Scope
		var nums []int
		if json.Unmarshal([]byte(rq.Scope), &scope) == nil && json.Unmarshal(scope.Seasons, &nums) == nil {
			for _, n := range nums {
				out.Seasons = append(out.Seasons, seerrSeasonStatus{ID: rq.ID*1000 + int64(n), SeasonNumber: n, Status: out.Status,
					Status4K: seerrMediaUnknown, CreatedAt: rq.CreatedAt, UpdatedAt: rq.UpdatedAt})
			}
		} else if rq.MediaItemID != 0 {
			if seasons, err := s.store.Seasons(ctx, rq.MediaItemID); err == nil {
				for _, sn := range seasons {
					if sn.Number > 0 && sn.Monitored {
						out.Seasons = append(out.Seasons, seerrSeasonStatus{ID: rq.ID*1000 + int64(sn.Number), SeasonNumber: sn.Number,
							Status: out.Status, Status4K: seerrMediaUnknown, CreatedAt: rq.CreatedAt, UpdatedAt: rq.UpdatedAt})
					}
				}
			}
		}
		out.SeasonCount = len(out.Seasons)
	}
	if withMedia {
		var m *seerrMedia
		if rq.MediaType == "tv" {
			m = s.seerrTVMedia(ctx, rq.TMDBID)
		} else {
			m = s.seerrMovieMedia(ctx, rq.TMDBID, false)
		}
		if m != nil {
			m.Requests = []seerrRequest{}
			out.Media = m
		}
	}
	if out.Media == nil {
		out.Media = &seerrMedia{ID: rq.TMDBID, TMDBID: rq.TMDBID, MediaType: rq.MediaType, Status: seerrMediaPending,
			Status4K: seerrMediaUnknown, Seasons: []seerrSeasonStatus{}, Requests: []seerrRequest{}}
	}
	return out
}

// seerrFilter maps Jellyseerr's request filters to a test on Bragi's statuses.
func seerrFilter(f string) func(string) bool {
	switch f {
	case "pending":
		return func(st string) bool { return st == "pending" }
	case "approved", "processing":
		return func(st string) bool { return st == "approved" }
	case "available", "completed":
		return func(st string) bool { return st == "available" }
	case "declined", "failed", "unavailable":
		return func(st string) bool { return st == "denied" }
	default:
		return func(string) bool { return true }
	}
}

func (s *Server) seerrListRequests(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	q := r.URL.Query()
	f := store.RequestFilter{}
	if u.Role != "admin" {
		f.UserID = u.ID
	} else if by, err := strconv.ParseInt(q.Get("requestedBy"), 10, 64); err == nil && by > 0 {
		f.UserID = by
	}
	all, err := s.store.ListRequests(r.Context(), f)
	if err != nil {
		s.storeError(w, err, "requests")
		return
	}
	keep := seerrFilter(q.Get("filter"))
	mediaType := q.Get("mediaType")
	var list []store.Request
	for _, rq := range all {
		if keep(rq.Status) && (mediaType == "" || mediaType == "all" || mediaType == rq.MediaType) {
			list = append(list, rq)
		}
	}
	if q.Get("sort") == "modified" {
		sort.SliceStable(list, func(i, j int) bool { return list[i].UpdatedAt > list[j].UpdatedAt })
	}
	take, skip := pageArgs(r, 10)
	users := map[int64]*store.User{}
	out := []seerrRequest{}
	for i := skip; i < len(list) && i < skip+take; i++ {
		out = append(out, s.toSeerrRequest(r.Context(), list[i], users, true))
	}
	writeJSON(w, http.StatusOK, map[string]any{"pageInfo": pageOf(len(list), take, skip), "results": out})
}

func (s *Server) seerrRequestCount(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r.Context())
	f := store.RequestFilter{}
	if u.Role != "admin" {
		f.UserID = u.ID
	}
	all, err := s.store.ListRequests(r.Context(), f)
	if err != nil {
		s.storeError(w, err, "requests")
		return
	}
	c := map[string]int{"total": len(all), "movie": 0, "tv": 0, "pending": 0, "approved": 0, "declined": 0,
		"processing": 0, "available": 0, "completed": 0}
	for _, rq := range all {
		c[rq.MediaType]++
		switch rq.Status {
		case "pending":
			c["pending"]++
		case "approved":
			c["approved"]++
			c["processing"]++
		case "denied":
			c["declined"]++
		case "available":
			c["approved"]++
			c["available"]++
			c["completed"]++
		}
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) seerrCreateRequest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MediaType string          `json:"mediaType"`
		MediaID   int64           `json:"mediaId"`
		Seasons   json.RawMessage `json:"seasons"`
		Is4K      bool            `json:"is4k"`
		UserID    int64           `json:"userId"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.MediaType != "tv" && in.MediaType != "movie" {
		seerrError(w, http.StatusBadRequest, "mediaType must be tv or movie")
		return
	}
	if in.MediaID <= 0 {
		seerrError(w, http.StatusBadRequest, "mediaId is required")
		return
	}
	// An admin may request on someone else's behalf.
	if me := userFrom(r.Context()); in.UserID > 0 && in.UserID != me.ID && me.Role == "admin" {
		other, err := s.store.GetUser(r.Context(), in.UserID)
		if err != nil {
			seerrError(w, http.StatusNotFound, "User not found")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, other))
	}
	body := map[string]any{"type": in.MediaType, "tmdbId": in.MediaID}
	if in.MediaType == "tv" && len(in.Seasons) > 0 && string(in.Seasons) != "null" {
		body["seasons"] = in.Seasons
	}
	var created struct {
		ID    int64  `json:"id"`
		Error string `json:"error"`
	}
	code := s.internalCall(r, http.MethodPost, "/api/v1/requests", body, &created)
	if code != http.StatusCreated {
		if code == http.StatusConflict {
			seerrError(w, http.StatusConflict, "Request for this media already exists")
			return
		}
		seerrError(w, code, created.Error)
		return
	}
	rq, err := s.store.GetRequest(r.Context(), created.ID)
	if err != nil {
		s.storeError(w, err, "request")
		return
	}
	writeJSON(w, http.StatusCreated, s.toSeerrRequest(r.Context(), *rq, map[int64]*store.User{}, true))
}

func (s *Server) seerrRequestFromPath(w http.ResponseWriter, r *http.Request) (*store.Request, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		seerrError(w, http.StatusBadRequest, "Invalid request id")
		return nil, false
	}
	rq, err := s.store.GetRequest(r.Context(), id)
	if err != nil || (userFrom(r.Context()).Role != "admin" && rq.UserID != userFrom(r.Context()).ID) {
		seerrError(w, http.StatusNotFound, "Request not found")
		return nil, false
	}
	return rq, true
}

func (s *Server) seerrGetRequest(w http.ResponseWriter, r *http.Request) {
	rq, ok := s.seerrRequestFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.toSeerrRequest(r.Context(), *rq, map[int64]*store.User{}, true))
}

func (s *Server) seerrDeleteRequest(w http.ResponseWriter, r *http.Request) {
	rq, ok := s.seerrRequestFromPath(w, r)
	if !ok {
		return
	}
	var e errorBody
	if code := s.internalCall(r, http.MethodDelete, "/api/v1/requests/"+strconv.FormatInt(rq.ID, 10), nil, &e); code != http.StatusNoContent {
		seerrError(w, code, e.Error)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) seerrRequestAction(w http.ResponseWriter, r *http.Request) {
	rq, ok := s.seerrRequestFromPath(w, r)
	if !ok {
		return
	}
	action := map[string]string{"approve": "approve", "decline": "deny"}[r.PathValue("action")]
	if action == "" {
		seerrError(w, http.StatusBadRequest, "Use approve or decline")
		return
	}
	var e errorBody
	if code := s.internalCall(r, http.MethodPost, "/api/v1/requests/"+strconv.FormatInt(rq.ID, 10)+"/"+action, nil, &e); code != http.StatusOK {
		seerrError(w, code, e.Error)
		return
	}
	rq, _ = s.store.GetRequest(r.Context(), rq.ID)
	writeJSON(w, http.StatusOK, s.toSeerrRequest(r.Context(), *rq, map[int64]*store.User{}, true))
}

// internalCall runs one of Bragi's own API handlers as the current user and
// decodes its JSON answer into out. It returns the HTTP status.
func (s *Server) internalCall(r *http.Request, method, path string, body, out any) int {
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), method, path, &buf)
	if err != nil {
		return http.StatusInternalServerError
	}
	req.ContentLength = int64(buf.Len())
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	if out != nil && rec.Body.Len() > 0 {
		json.Unmarshal(rec.Body.Bytes(), out)
	}
	return rec.Code
}
