package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Tharkis/bragi/internal/db"
	"github.com/Tharkis/bragi/internal/store"
)

type client struct {
	t   *testing.T
	url string
	hc  *http.Client
	key string
}

func newTestServer(t *testing.T) *client {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "bragi.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if err := db.Migrate(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(Options{Store: store.New(d), Version: "test"}))
	t.Cleanup(srv.Close)
	jar, _ := cookiejar.New(nil)
	return &client{t: t, url: srv.URL, hc: &http.Client{Jar: jar}}
}

func (c *client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.url+path, r)
	req.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		req.Header.Set("X-Api-Key", c.key)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestSetupLoginAndAuth(t *testing.T) {
	c := newTestServer(t)

	var health map[string]string
	if code := c.do("GET", "/api/v1/health", nil, &health); code != 200 || health["status"] != "ok" {
		t.Fatalf("health: %d %v", code, health)
	}

	var status map[string]any
	c.do("GET", "/api/v1/setup", nil, &status)
	if status["needsAdmin"] != true {
		t.Fatalf("fresh install should need an admin: %v", status)
	}

	if code := c.do("GET", "/api/v1/indexers", nil, nil); code != 401 {
		t.Fatalf("indexers before sign-in: %d, want 401", code)
	}
	if code := c.do("POST", "/api/v1/setup", credentials{"joe", "short"}, nil); code != 400 {
		t.Fatalf("short password: %d, want 400", code)
	}

	var me userResponse
	if code := c.do("POST", "/api/v1/setup", credentials{"joe", "correct horse"}, &me); code != 201 {
		t.Fatalf("setup: %d", code)
	}
	if me.Role != "admin" || len(me.APIKey) != 32 {
		t.Fatalf("setup user: %+v", me)
	}
	if code := c.do("POST", "/api/v1/setup", credentials{"eve", "password123"}, nil); code != 409 {
		t.Fatalf("second setup: %d, want 409", code)
	}

	// Session cookie from setup works.
	if code := c.do("GET", "/api/v1/auth/me", nil, nil); code != 200 {
		t.Fatalf("me with session: %d", code)
	}
	c.do("POST", "/api/v1/auth/logout", nil, nil)
	if code := c.do("GET", "/api/v1/auth/me", nil, nil); code != 401 {
		t.Fatalf("me after logout: %d, want 401", code)
	}
	if code := c.do("POST", "/api/v1/auth/login", credentials{"JOE", "wrong password"}, nil); code != 401 {
		t.Fatalf("wrong password: %d, want 401", code)
	}
	if code := c.do("POST", "/api/v1/auth/login", credentials{"JOE", "correct horse"}, nil); code != 200 {
		t.Fatalf("login (case-insensitive name): %d", code)
	}

	// API key works without a cookie.
	jar, _ := cookiejar.New(nil)
	c.hc.Jar = jar
	c.key = me.APIKey
	if code := c.do("GET", "/api/v1/auth/me", nil, nil); code != 200 {
		t.Fatalf("me with API key: %d", code)
	}
	c.key = "not-a-key"
	if code := c.do("GET", "/api/v1/auth/me", nil, nil); code != 401 {
		t.Fatalf("me with bad API key: %d, want 401", code)
	}
}

func signedIn(t *testing.T) *client {
	c := newTestServer(t)
	if code := c.do("POST", "/api/v1/setup", credentials{"joe", "correct horse"}, nil); code != 201 {
		t.Fatalf("setup: %d", code)
	}
	return c
}

func TestIndexerCRUDAndTest(t *testing.T) {
	c := signedIn(t)

	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "secret-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, `{"appName":"Prowlarr","version":"1.20.0"}`)
	}))
	defer prowlarr.Close()

	var res testResult
	c.do("POST", "/api/v1/indexers/test", store.Integration{Kind: "prowlarr", BaseURL: prowlarr.URL, APIKey: "secret-key"}, &res)
	if !res.OK {
		t.Fatalf("unsaved test failed: %s", res.Error)
	}

	var created store.Integration
	if code := c.do("POST", "/api/v1/indexers", store.Integration{Kind: "prowlarr", BaseURL: prowlarr.URL + "/", APIKey: "secret-key", Enabled: true}, &created); code != 201 {
		t.Fatalf("create: %d", code)
	}
	if created.APIKey != secretMask || created.Name != "Prowlarr" || created.BaseURL != prowlarr.URL {
		t.Fatalf("created: %+v", created)
	}

	// Saving with the mask keeps the real key.
	created.Name = "My Prowlarr"
	var updated store.Integration
	if code := c.do("PUT", "/api/v1/indexers/"+itoa(created.ID), created, &updated); code != 200 || updated.Name != "My Prowlarr" {
		t.Fatalf("update: %d %+v", code, updated)
	}
	res = testResult{}
	c.do("POST", "/api/v1/indexers/"+itoa(created.ID)+"/test", nil, &res)
	if !res.OK {
		t.Fatalf("saved test failed after masked update: %s", res.Error)
	}

	// A download-client kind is rejected on the indexers endpoint.
	if code := c.do("POST", "/api/v1/indexers", store.Integration{Kind: "nzbget", BaseURL: "http://x"}, nil); code != 400 {
		t.Fatalf("wrong kind: %d, want 400", code)
	}
	if code := c.do("POST", "/api/v1/indexers", store.Integration{Kind: "prowlarr", BaseURL: "ftp://x", APIKey: "k"}, nil); code != 400 {
		t.Fatalf("bad URL: %d, want 400", code)
	}

	var list []store.Integration
	c.do("GET", "/api/v1/indexers", nil, &list)
	if len(list) != 1 {
		t.Fatalf("list: %d items", len(list))
	}
	if code := c.do("DELETE", "/api/v1/indexers/"+itoa(created.ID), nil, nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code := c.do("GET", "/api/v1/indexers/"+itoa(created.ID), nil, nil); code != 404 {
		t.Fatalf("get after delete: %d, want 404", code)
	}
}

func TestLibraries(t *testing.T) {
	c := signedIn(t)
	dir := t.TempDir()

	if code := c.do("POST", "/api/v1/libraries", store.Library{MediaType: "tv", Path: filepath.Join(dir, "missing")}, nil); code != 400 {
		t.Fatalf("missing folder: %d, want 400", code)
	}
	var lib store.Library
	if code := c.do("POST", "/api/v1/libraries", store.Library{MediaType: "tv", Path: dir}, &lib); code != 201 {
		t.Fatalf("create: %d", code)
	}
	if lib.Name != filepath.Base(dir) {
		t.Fatalf("default name: %q", lib.Name)
	}
	if code := c.do("POST", "/api/v1/libraries", store.Library{MediaType: "tv", Path: dir}, nil); code != 409 {
		t.Fatalf("duplicate path: %d, want 409", code)
	}

	var presets []store.QualityPreset
	c.do("GET", "/api/v1/qualitypresets", nil, &presets)
	if len(presets) != 3 {
		t.Fatalf("presets: %d, want 3", len(presets))
	}
}

func TestSettingsAndUI(t *testing.T) {
	c := signedIn(t)
	if code := c.do("PUT", "/api/v1/settings", map[string]string{"setup_complete": "true"}, nil); code != 200 {
		t.Fatalf("put settings: %d", code)
	}
	if code := c.do("PUT", "/api/v1/settings", map[string]string{"evil": "1"}, nil); code != 400 {
		t.Fatalf("unknown setting: %d, want 400", code)
	}
	var status map[string]any
	c.do("GET", "/api/v1/setup", nil, &status)
	if status["setupComplete"] != true {
		t.Fatalf("setup status: %v", status)
	}
	if code := c.do("GET", "/api/v1/nope", nil, nil); code != 404 {
		t.Fatalf("unknown API path: %d, want 404", code)
	}
	if code := c.do("GET", "/settings/indexers", nil, nil); code != 200 {
		t.Fatalf("SPA fallback: %d, want 200", code)
	}
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
