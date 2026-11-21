package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// seerr calls the Jellyseerr-compatible API the way Jellyfin Enhanced does:
// an admin key, and X-Api-User to act for a user.
func (e *tvEnv) seerr(method, path, key string, asUser int64, body, out any) int {
	e.t.Helper()
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req, _ := http.NewRequest(method, e.url+"/seerr/api/v1"+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", key)
	if asUser > 0 {
		req.Header.Set("X-Api-User", fmt.Sprint(asUser))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestSeerrRequestFlow(t *testing.T) {
	e := newTVEnv(t)
	var admin userResponse
	e.do("GET", "/api/v1/auth/me", nil, &admin)
	key := admin.APIKey

	var status map[string]any
	if code := e.seerr("GET", "/status", "", 0, nil, &status); code != 200 || status["version"] == "" {
		t.Fatalf("status: %d %v", code, status)
	}
	if code := e.seerr("GET", "/user", "wrong", 0, nil, nil); code != 403 {
		t.Fatalf("users with a bad key: %d, want 403", code)
	}

	// Jellyfin Enhanced imports a Jellyfin user, then finds it by id.
	var imported []seerrUser
	if code := e.seerr("POST", "/user/import-from-jellyfin", key, 0, map[string]any{"jellyfinUserIds": []string{"6A1F0C2D-1111-2222-3333-444455556666"}}, &imported); code != 201 || len(imported) != 1 {
		t.Fatalf("import: %d %+v", code, imported)
	}
	kid := imported[0]
	if kid.JellyfinUserID != "6a1f0c2d111122223333444455556666" || kid.Permissions != seerrPermRequest {
		t.Fatalf("imported user: %+v", kid)
	}
	var users struct {
		PageInfo seerrPageInfo `json:"pageInfo"`
		Results  []seerrUser   `json:"results"`
	}
	e.seerr("GET", "/user?take=1000", key, 0, nil, &users)
	if users.PageInfo.Results != 2 || len(users.Results) != 2 {
		t.Fatalf("users: %+v", users)
	}
	// The same Jellyfin user through Bragi's own plugin header maps to the
	// same account, whatever the dashes.
	req, _ := http.NewRequest("GET", e.url+"/api/v1/auth/me", nil)
	req.Header.Set("X-Api-Key", key)
	req.Header.Set("X-Bragi-User", "6a1f0c2d-1111-2222-3333-444455556666")
	resp, _ := http.DefaultClient.Do(req)
	var me userResponse
	json.NewDecoder(resp.Body).Decode(&me)
	resp.Body.Close()
	if me.ID != kid.ID {
		t.Fatalf("plugin header mapped to user %d, want %d", me.ID, kid.ID)
	}

	// Search as the kid.
	var search struct {
		Results []map[string]any `json:"results"`
	}
	if code := e.seerr("GET", "/search?query=bluey&page=1", key, kid.ID, nil, &search); code != 200 || len(search.Results) != 1 {
		t.Fatalf("search: %d %+v", code, search)
	}
	if r := search.Results[0]; r["mediaType"] != "tv" || r["name"] != "Bluey" || r["posterPath"] != "/82728.svg" || r["mediaInfo"] != nil {
		t.Fatalf("search result: %+v", r)
	}

	// Request season 1; it waits for approval.
	var rq seerrRequest
	if code := e.seerr("POST", "/request", key, kid.ID, map[string]any{"mediaType": "tv", "mediaId": 82728, "seasons": []int{1}}, &rq); code != 201 {
		t.Fatalf("request: %d", code)
	}
	if rq.Status != seerrRequestPending || rq.RequestedBy == nil || rq.RequestedBy.ID != kid.ID || len(rq.Seasons) != 1 || rq.Media.Status != seerrMediaPending {
		t.Fatalf("new request: %+v", rq)
	}
	if code := e.seerr("POST", "/request", key, kid.ID, map[string]any{"mediaType": "tv", "mediaId": 82728, "seasons": []int{1}}, nil); code != 409 {
		t.Fatalf("duplicate request: %d, want 409", code)
	}
	var details map[string]any
	e.seerr("GET", "/tv/82728", key, kid.ID, nil, &details)
	if mi, _ := details["mediaInfo"].(map[string]any); mi == nil || mi["status"] != float64(seerrMediaPending) {
		t.Fatalf("tv details mediaInfo: %+v", details["mediaInfo"])
	}

	// The kid may not approve; the admin may.
	if code := e.seerr("POST", fmt.Sprintf("/request/%d/approve", rq.ID), key, kid.ID, nil, nil); code != 403 {
		t.Fatalf("requester approve: %d, want 403", code)
	}
	var approved seerrRequest
	if code := e.seerr("POST", fmt.Sprintf("/request/%d/approve", rq.ID), key, 0, nil, &approved); code != 200 || approved.Status != seerrRequestApproved {
		t.Fatalf("approve: %d %+v", code, approved)
	}

	// The kid's request list shows only their request.
	var list struct {
		PageInfo seerrPageInfo  `json:"pageInfo"`
		Results  []seerrRequest `json:"results"`
	}
	e.seerr("GET", "/request?take=10&skip=0&filter=all", key, kid.ID, nil, &list)
	if list.PageInfo.Results != 1 || list.Results[0].Media.TMDBID != 82728 {
		t.Fatalf("request list: %+v", list)
	}
	e.seerr("GET", "/request?filter=pending", key, kid.ID, nil, &list)
	if list.PageInfo.Results != 0 {
		t.Fatalf("pending filter: %+v", list)
	}

	// Trending shows come with what Bragi already has.
	var trending struct {
		Results []map[string]any `json:"results"`
	}
	e.seerr("GET", "/discover/trending", key, kid.ID, nil, &trending)
	found := false
	for _, r := range trending.Results {
		if r["id"] == float64(82728) {
			found = r["mediaInfo"] != nil
		}
	}
	if !found {
		t.Fatalf("Bluey missing or without mediaInfo in trending: %+v", trending.Results)
	}

	if code := e.seerr("GET", "/issue", key, 0, nil, nil); code != 404 {
		t.Fatalf("unsupported endpoint: %d, want 404", code)
	}
}

func TestDiscoverTrending(t *testing.T) {
	e := newTVEnv(t)
	e.do("POST", "/api/v1/requests", map[string]any{"tmdbId": 95396, "seasons": "all"}, nil)
	var page discoverPage
	if code := e.do("GET", "/api/v1/discover/trending?type=tv", nil, &page); code != 200 || len(page.Results) == 0 {
		t.Fatalf("trending: %d %+v", code, page)
	}
	for _, r := range page.Results {
		if r.TMDBID == 95396 && (r.SeriesID == 0 || r.Requested == "") {
			t.Fatalf("Severance not marked: %+v", r)
		}
	}
}
