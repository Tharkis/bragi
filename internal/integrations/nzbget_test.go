package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeNZBGet is a JSON-RPC NZBGet with two logins: nzbget (control) and
// adder (add only, as NZBGet's AddUsername).
type fakeNZBGet struct {
	t        *testing.T
	version  string
	queue    []map[string]any
	history  []map[string]any
	appended []any // params of the last append
	removed  []string
}

func (f *fakeNZBGet) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/jsonrpc" {
		http.NotFound(w, r)
		return
	}
	user, pass, _ := r.BasicAuth()
	if !(user == "nzbget" && pass == "tegbzn6789") && !(user == "adder" && pass == "add") {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var call struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	json.NewDecoder(r.Body).Decode(&call)
	reply := func(v any) { json.NewEncoder(w).Encode(map[string]any{"version": "1.1", "id": 1, "result": v}) }
	if user == "adder" && call.Method != "version" && call.Method != "append" {
		json.NewEncoder(w).Encode(map[string]any{"version": "1.1", "id": 1, "error": map[string]any{"name": "JSONRPCError", "code": 401, "message": "Access denied"}})
		return
	}
	switch call.Method {
	case "version":
		reply(f.version)
	case "config":
		reply([]map[string]string{
			{"Name": "MainDir", "Value": "/downloads"},
			{"Name": "Category1.Name", "Value": "Movies"},
			{"Name": "Category1.DestDir", "Value": ""},
			{"Name": "Category2.Name", "Value": "tv"},
		})
	case "append":
		f.appended = make([]any, len(call.Params))
		for i, p := range call.Params {
			json.Unmarshal(p, &f.appended[i])
		}
		reply(42)
	case "listgroups":
		reply(f.queue)
	case "history":
		reply(f.history)
	case "editqueue":
		var cmd string
		json.Unmarshal(call.Params[0], &cmd)
		f.removed = append(f.removed, cmd+" "+string(call.Params[2]))
		reply(strings.HasPrefix(cmd, "History"))
	default:
		f.t.Errorf("unexpected method %s", call.Method)
	}
}

func TestNZBGetCheck(t *testing.T) {
	f := &fakeNZBGet{t: t, version: "24.3"}
	srv := httptest.NewServer(f)
	defer srv.Close()
	ctx := context.Background()

	client := func(user, pass string, cats ...string) Checker {
		c, _ := NewDownloadClient("nzbget", Config{BaseURL: srv.URL + "/", Username: user, Password: pass, Categories: cats}, hc)
		return c.(Checker)
	}
	if w, err := client("nzbget", "tegbzn6789", "movies", "tv").Check(ctx); err != nil || len(w) != 0 {
		t.Fatalf("good login: %v %v", w, err)
	}
	if _, err := client("nzbget", "x").Check(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad login: got %v, want ErrUnauthorized", err)
	}
	if _, err := client("adder", "add").Check(ctx); err == nil || !strings.Contains(err.Error(), "cannot see the queue") {
		t.Fatalf("add-only login: %v", err)
	}
	w, err := client("nzbget", "tegbzn6789", "tv", "books", "Audiobooks").Check(ctx)
	if err != nil || len(w) != 1 || !strings.Contains(w[0], "categories “books” or “Audiobooks”") {
		t.Fatalf("missing categories: %q %v", w, err)
	}
	f.version = "15.0"
	if _, err := client("nzbget", "tegbzn6789").Check(ctx); err == nil || !strings.Contains(err.Error(), "too old") {
		t.Fatalf("old NZBGet: %v", err)
	}
}

func TestNZBGetAddStatusRemove(t *testing.T) {
	nzb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "<nzb/>")
	}))
	defer nzb.Close()
	f := &fakeNZBGet{t: t, version: "21.1"}
	srv := httptest.NewServer(f)
	defer srv.Close()
	ctx := context.Background()
	c, _ := NewDownloadClient("nzbget", Config{BaseURL: srv.URL, Username: "nzbget", Password: "tegbzn6789", Category: "movies",
		RemotePath: "/downloads/", LocalPath: "/data/usenet"}, hc)

	id, err := c.Add(ctx, Release{Title: "Movie.2024.1080p.WEB-DL", DownloadURL: nzb.URL})
	if err != nil || id != "42" {
		t.Fatalf("add: %q %v", id, err)
	}
	content, _ := base64.StdEncoding.DecodeString(f.appended[1].(string))
	if f.appended[0] != "Movie.2024.1080p.WEB-DL.nzb" || string(content) != "<nzb/>" || f.appended[2] != "movies" || f.appended[8] != "FORCE" {
		t.Fatalf("append params: %v", f.appended)
	}

	f.queue = []map[string]any{
		{"NZBID": 1, "Status": "DOWNLOADING", "FileSizeMB": 1000, "RemainingSizeMB": 250, "DestDir": "/downloads/intermediate/a.#1"},
		{"NZBID": 2, "Status": "PAUSED", "FileSizeMB": 1000, "RemainingSizeMB": 1000},
		{"NZBID": 3, "Status": "UNPACKING", "FileSizeMB": 1000, "RemainingSizeMB": 0},
	}
	f.history = []map[string]any{
		{"NZBID": 4, "Kind": "NZB", "Status": "SUCCESS/ALL", "DestDir": "/downloads/movies/b", "FinalDir": ""},
		{"NZBID": 5, "Kind": "NZB", "Status": "WARNING/SCRIPT", "DestDir": "/downloads/x", "FinalDir": "/downloads/movies/c"},
		{"NZBID": 6, "Kind": "NZB", "Status": "WARNING/DAMAGED", "DestDir": "/downloads/d"},
		{"NZBID": 7, "Kind": "NZB", "Status": "DELETED/DUPE"},
		{"NZBID": 8, "Kind": "NZB", "Status": "FAILURE/UNPACK"},
		{"NZBID": 9, "Kind": "DUP", "Status": "SUCCESS/ALL"},
		{"NZBID": 10, "Kind": "NZB", "Status": "WARNING/PASSWORD"},
	}
	jobs, err := c.Status(ctx, []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"})
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ state, path, stage, err string }{
		{JobDownloading, "/data/usenet/intermediate/a.#1", "", ""},
		{JobQueued, "", "Paused in NZBGet", ""},
		{JobDownloading, "", "Unpacking", ""},
		{JobCompleted, "/data/usenet/movies/b", "", ""},
		{JobCompleted, "/data/usenet/movies/c", "", ""},
		{JobFailed, "/data/usenet/d", "", "NZBGet finished with damaged files that were not repaired"},
		{JobFailed, "", "", "NZBGet skipped it as a duplicate"},
		{JobFailed, "", "", "NZBGet could not unpack the download"},
		{JobMissing, "", "", ""},
		{JobFailed, "", "", "The release is password protected"},
	}
	for i, w := range want {
		j := jobs[i]
		if j.State != w.state || j.OutputPath != w.path || j.Stage != w.stage || j.Error != w.err {
			t.Errorf("job %s: got %+v, want %+v", j.ID, j, w)
		}
	}
	if jobs[0].Progress != 0.75 || jobs[2].Progress != 1 {
		t.Errorf("progress: %v %v", jobs[0].Progress, jobs[2].Progress)
	}

	if err := c.Remove(ctx, "4", false); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.removed, "|") != "GroupDelete [4]|HistoryDelete [4]" {
		t.Fatalf("remove: %v", f.removed)
	}
}

func TestNZBGetBigHistory(t *testing.T) {
	// Five thousand history entries with NZBGet's usual bulk is well over
	// the 4 MB other clients are held to.
	entry := map[string]any{"NZBID": 0, "Kind": "NZB", "Status": "SUCCESS/ALL", "DestDir": "/downloads/x",
		"Parameters": []map[string]string{{"Name": "*Unpack:", "Value": "yes"}}, "Log": strings.Repeat("x", 1200)}
	f := &fakeNZBGet{t: t, version: "24.3"}
	for i := 1; i <= 5000; i++ {
		e := map[string]any{}
		for k, v := range entry {
			e[k] = v
		}
		e["NZBID"] = i
		f.history = append(f.history, e)
	}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c, _ := NewDownloadClient("nzbget", Config{BaseURL: srv.URL, Username: "nzbget", Password: "tegbzn6789"}, hc)
	jobs, err := c.Status(context.Background(), []string{"4999"})
	if err != nil || jobs[0].State != JobCompleted {
		t.Fatalf("big history: %+v %v", jobs, err)
	}
}

func TestMapPath(t *testing.T) {
	cases := []struct{ p, from, to, want string }{
		{"/downloads/tv/Show", "/downloads", "/data/usenet", "/data/usenet/tv/Show"},
		{"/downloads", "/downloads/", "/data/usenet/", "/data/usenet"},
		{"/downloads2/tv", "/downloads", "/data", "/downloads2/tv"},
		{"/other/tv", "/downloads", "/data", "/other/tv"},
		{`D:\Usenet\complete\Movie`, `d:\usenet`, "/mnt/usenet", "/mnt/usenet/complete/Movie"},
		{"/downloads/x", "", "/data", "/downloads/x"},
	}
	for _, c := range cases {
		if got := MapPath(c.p, c.from, c.to); got != c.want {
			t.Errorf("MapPath(%q, %q, %q) = %q, want %q", c.p, c.from, c.to, got, c.want)
		}
	}
}
