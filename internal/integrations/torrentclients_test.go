package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTransmissionRPCURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://nas:9091":                  "http://nas:9091/transmission/rpc",
		"http://nas:9091/transmission":     "http://nas:9091/transmission/rpc",
		"http://nas:9091/transmission/web": "http://nas:9091/transmission/rpc",
		"http://nas:9091/transmission/rpc": "http://nas:9091/transmission/rpc",
		"https://box/tr/rpc":               "https://box/tr/rpc",
		"https://box/tr":                   "https://box/tr/transmission/rpc",
	} {
		if got := (&transmission{cfg: Config{BaseURL: in}}).rpcURL(); got != want {
			t.Errorf("%s: got %s, want %s", in, got, want)
		}
	}
}

// TestTransmissionSession checks the 409 session-id handshake, basic auth
// and the category folder and label on add.
func TestTransmissionSession(t *testing.T) {
	var added map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "admin" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-Transmission-Session-Id") != "s1" {
			w.Header().Set("X-Transmission-Session-Id", "s1")
			w.WriteHeader(http.StatusConflict)
			return
		}
		var call struct {
			Method    string         `json:"method"`
			Arguments map[string]any `json:"arguments"`
		}
		json.NewDecoder(r.Body).Decode(&call)
		switch call.Method {
		case "session-get":
			io.WriteString(w, `{"result":"success","arguments":{"version":"4.0.6","rpc-version":17,"download-dir":"/downloads/complete"}}`)
		case "torrent-add":
			added = call.Arguments
			io.WriteString(w, `{"result":"success","arguments":{"torrent-duplicate":{"hashString":"ABCDEF0123456789ABCDEF0123456789ABCDEF01"}}}`)
		}
	}))
	defer srv.Close()

	c, _ := NewDownloadClient("transmission", Config{BaseURL: srv.URL, Username: "admin", Password: "secret", Category: "movies"}, hc)
	if err := c.Test(context.Background()); err != nil {
		t.Fatalf("test: %v", err)
	}
	id, err := c.Add(context.Background(), Release{DownloadURL: "magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01&dn=x"})
	if err != nil || id != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Fatalf("add: %q %v", id, err)
	}
	if added["download-dir"] != "/downloads/complete/movies" || added["labels"].([]any)[0] != "movies" {
		t.Fatalf("added with %v", added)
	}
	bad, _ := NewDownloadClient("transmission", Config{BaseURL: srv.URL, Username: "admin", Password: "wrong"}, hc)
	if err := bad.Test(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad login: %v", err)
	}
}

func TestTransmissionJobStates(t *testing.T) {
	base := transmissionTorrent{HashString: "AB", Name: "Show.S01", DownloadDir: "/dl/tv", SizeWhenDone: 100, LeftUntilDone: 50, PercentDone: 0.5, MetadataPercent: 1, Status: trDownload}
	cases := []struct {
		name  string
		edit  func(*transmissionTorrent)
		state string
		stage string
	}{
		{"downloading", func(*transmissionTorrent) {}, JobDownloading, ""},
		{"paused", func(t *transmissionTorrent) { t.Status = trStopped }, JobQueued, "Paused in Transmission"},
		{"queued", func(t *transmissionTorrent) { t.Status = trDownloadWait }, JobQueued, ""},
		{"magnet without metadata", func(t *transmissionTorrent) { t.MetadataPercent = 0.2; t.SizeWhenDone = 0 }, JobQueued, "Fetching metadata"},
		{"checking", func(t *transmissionTorrent) { t.Status = trCheck }, JobDownloading, "Checking files"},
		{"tracker error is not fatal", func(t *transmissionTorrent) { t.Error, t.ErrorString = 2, "Tracker gave HTTP 502" }, JobDownloading, ""},
		{"local error", func(t *transmissionTorrent) { t.Error, t.ErrorString = 3, "No data found" }, JobFailed, ""},
		{"seeding", func(t *transmissionTorrent) { t.LeftUntilDone, t.PercentDone, t.Status = 0, 1, trSeed }, JobCompleted, ""},
		{"stopped after seeding", func(t *transmissionTorrent) { t.LeftUntilDone, t.PercentDone, t.Status = 0, 1, trStopped }, JobCompleted, ""},
	}
	for _, c := range cases {
		tt := base
		c.edit(&tt)
		j := tt.job()
		if j.State != c.state || j.Stage != c.stage || j.OutputPath != "/dl/tv/Show.S01" || j.ID != "ab" {
			t.Errorf("%s: got %+v", c.name, j)
		}
	}
}

// TestDelugeConnect checks login, connecting a disconnected web UI to its
// daemon, a duplicate add and the label.
func TestDelugeConnect(t *testing.T) {
	var calls []string
	connected := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
			ID     int    `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&call)
		calls = append(calls, call.Method)
		res := func(v any) { json.NewEncoder(w).Encode(map[string]any{"result": v, "error": nil, "id": call.ID}) }
		switch call.Method {
		case "auth.login":
			res(call.Params[0] == "pw")
		case "web.connected":
			res(connected)
		case "web.get_hosts":
			res([][]any{{"h1", "127.0.0.1", 58846, "Offline"}})
		case "web.connect":
			connected = call.Params[0] == "h1"
			res(nil)
		case "core.add_torrent_magnet":
			json.NewEncoder(w).Encode(map[string]any{"result": nil, "id": call.ID,
				"error": map[string]any{"message": "Torrent already in session (abcdef0123456789abcdef0123456789abcdef01).", "code": 4}})
		case "core.get_enabled_plugins":
			res([]string{"Label"})
		case "label.get_labels":
			res([]string{"tv"})
		default:
			res(nil)
		}
	}))
	defer srv.Close()

	c, _ := NewDownloadClient("deluge", Config{BaseURL: srv.URL, Password: "pw", Category: "Movies"}, hc)
	id, err := c.Add(context.Background(), Release{DownloadURL: "magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01"})
	if err != nil || id != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Fatalf("add: %q %v", id, err)
	}
	got := strings.Join(calls, " ")
	if !strings.Contains(got, "web.connect ") || !strings.HasSuffix(got, "label.get_labels label.add label.set_torrent") {
		t.Fatalf("calls: %s", got)
	}
	bad, _ := NewDownloadClient("deluge", Config{BaseURL: srv.URL, Password: "nope"}, hc)
	if err := bad.Test(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad password: %v", err)
	}
}

func TestDelugeJobStates(t *testing.T) {
	base := delugeTorrent{State: "Downloading", Progress: 40, SavePath: "/dl", Name: "Movie", TotalWanted: 100, TotalDone: 40, Message: "OK"}
	cases := []struct {
		name  string
		edit  func(*delugeTorrent)
		state string
		stage string
	}{
		{"downloading", func(*delugeTorrent) {}, JobDownloading, ""},
		{"paused", func(t *delugeTorrent) { t.State = "Paused" }, JobQueued, "Paused in Deluge"},
		{"queued", func(t *delugeTorrent) { t.State = "Queued" }, JobQueued, ""},
		{"metadata", func(t *delugeTorrent) { t.TotalWanted, t.TotalDone = 0, 0 }, JobQueued, "Fetching metadata"},
		{"checking", func(t *delugeTorrent) { t.State = "Checking" }, JobDownloading, "Checking files"},
		{"error", func(t *delugeTorrent) { t.State, t.Message = "Error", "Disk full" }, JobFailed, ""},
		{"seeding", func(t *delugeTorrent) { t.State, t.IsFinished, t.Progress = "Seeding", true, 100 }, JobCompleted, ""},
		{"paused when done", func(t *delugeTorrent) { t.State, t.IsFinished = "Paused", true }, JobCompleted, ""},
		{"waiting to move", func(t *delugeTorrent) {
			t.State, t.IsFinished, t.MoveOnDone, t.MoveOnPath = "Seeding", true, true, "/done"
		}, JobDownloading, "Moving files"},
		{"moved", func(t *delugeTorrent) {
			t.State, t.IsFinished, t.MoveOnDone, t.MoveOnPath, t.SavePath = "Seeding", true, true, "/dl/", "/dl"
		}, JobCompleted, ""},
	}
	for _, c := range cases {
		dt := base
		c.edit(&dt)
		j := dt.job("AB")
		if j.State != c.state || j.Stage != c.stage || j.OutputPath != "/dl/Movie" {
			t.Errorf("%s: got %+v", c.name, j)
		}
	}
}
