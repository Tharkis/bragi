package integrations

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewznabMovieSearch(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.RawQuery)
		io.WriteString(w, `<rss><channel><title>x</title></channel></rss>`)
	}))
	defer srv.Close()
	ix, _ := NewIndexer("newznab", Config{BaseURL: srv.URL, APIKey: "k"}, hc)
	ix.Search(context.Background(), Query{MediaType: "movie", Text: "Heat", Year: 1995, IMDBID: "tt0113277"})
	ix.Search(context.Background(), Query{MediaType: "movie", Text: "Heat", Year: 1995})
	want := []string{
		"apikey=k&cat=2000&extended=1&imdbid=0113277&limit=100&t=movie",
		"apikey=k&cat=2000&extended=1&limit=100&q=Heat+1995&t=movie",
	}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Errorf("query %d = %v, want %s", i, got, want[i])
		}
	}
}

// fakeSAB is a minimal SABnzbd: one job that finishes on the second poll.
func fakeSAB(t *testing.T, dir string) *httptest.Server {
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("mode") == "version" {
			io.WriteString(w, `{"version":"4.4.1"}`)
			return
		}
		if q.Get("apikey") != "good" {
			io.WriteString(w, `{"status":false,"error":"API Key Incorrect"}`)
			return
		}
		switch q.Get("mode") {
		case "addfile":
			if err := r.ParseMultipartForm(1 << 20); err != nil || r.FormValue("cat") != "movies" || r.FormValue("nzbname") != "Heat.1995.1080p" {
				t.Errorf("addfile form: %v %v", err, r.MultipartForm)
			}
			io.WriteString(w, `{"status":true,"nzo_ids":["SABnzbd_nzo_abc"]}`)
		case "queue":
			if q.Get("name") == "delete" {
				io.WriteString(w, `{"status":true}`)
				return
			}
			if q.Get("nzo_ids") == "" {
				io.WriteString(w, `{"queue":{"slots":[]}}`) // the key check in Test
				return
			}
			polls++
			if polls == 1 {
				io.WriteString(w, `{"queue":{"slots":[{"nzo_id":"SABnzbd_nzo_abc","status":"Downloading","mb":"1000.0","mbleft":"250.0","percentage":"75"}]}}`)
				return
			}
			io.WriteString(w, `{"queue":{"slots":[]}}`)
		case "history":
			if polls == 1 {
				io.WriteString(w, `{"history":{"slots":[]}}`)
				return
			}
			io.WriteString(w, `{"history":{"slots":[{"nzo_id":"SABnzbd_nzo_abc","status":"Completed","storage":"`+dir+`"},`+
				`{"nzo_id":"SABnzbd_nzo_bad","status":"Failed","fail_message":"Out of retention"}]}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSABnzbd(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Heat.1995.1080p")
	os.MkdirAll(dir, 0o755)
	nzb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "<nzb/>") }))
	defer nzb.Close()
	sab := fakeSAB(t, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bad, _ := NewDownloadClient("sabnzbd", Config{BaseURL: sab.URL, APIKey: "nope"}, hc)
	if err := bad.Test(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad key: %v", err)
	}
	c, _ := NewDownloadClient("sabnzbd", Config{BaseURL: sab.URL + "/", APIKey: "good", Category: "movies"}, hc)
	if err := c.Test(ctx); err != nil {
		t.Fatalf("test: %v", err)
	}
	id, err := c.Add(ctx, Release{Title: "Heat.1995.1080p", DownloadURL: nzb.URL + "/x.nzb", Protocol: Usenet})
	if err != nil || id != "SABnzbd_nzo_abc" {
		t.Fatalf("add: %q %v", id, err)
	}
	jobs, err := c.Status(ctx, []string{id, "SABnzbd_nzo_bad"})
	if err != nil || jobs[0].State != JobDownloading || jobs[0].Progress != 0.75 || jobs[1].State != JobMissing {
		t.Fatalf("first poll: %+v %v", jobs, err)
	}
	jobs, err = c.Status(ctx, []string{id, "SABnzbd_nzo_bad"})
	if err != nil || jobs[0].State != JobCompleted || jobs[0].OutputPath != dir || jobs[1].State != JobFailed || jobs[1].Error != "SABnzbd reports failed: Out of retention" {
		t.Fatalf("second poll: %+v %v", jobs, err)
	}
	if err := c.Remove(ctx, id, false); err != nil {
		t.Fatalf("remove: %v", err)
	}
}
