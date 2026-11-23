package api

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestLogsPageAndLevel(t *testing.T) {
	c := signedIn(t)

	var logs logsResponse
	if code := c.do("GET", "/api/v1/system/logs", nil, &logs); code != 200 {
		t.Fatalf("logs: %d", code)
	}
	if logs.Level != "info" || logs.EnvLevel != "info" {
		t.Fatalf("levels %q %q", logs.Level, logs.EnvLevel)
	}

	if code := c.do("PUT", "/api/v1/settings", map[string]string{"log_level": "loud"}, nil); code != 400 {
		t.Fatalf("bad level accepted: %d", code)
	}
	var settings map[string]string
	if code := c.do("PUT", "/api/v1/settings", map[string]string{"log_level": "Debug"}, &settings); code != 200 || settings["log_level"] != "debug" {
		t.Fatalf("set level: %d %v", code, settings)
	}
	c.do("GET", "/api/v1/system/logs?level=info&q=log+level", nil, &logs)
	if logs.Level != "debug" || len(logs.Entries) != 1 || logs.Entries[0].Message != "log level changed" {
		t.Fatalf("after change: level %q entries %+v", logs.Level, logs.Entries)
	}
	last := logs.Last
	c.do("GET", "/api/v1/system/logs?after="+itoa(last), nil, &logs)
	for _, e := range logs.Entries {
		if e.ID <= last {
			t.Errorf("entry %d is not after %d", e.ID, last)
		}
	}

	resp, err := c.hc.Get(c.url + "/api/v1/system/logs/download")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("download is not a zip: %v", err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "about.txt,bragi.log" {
		t.Errorf("zip holds %v", names)
	}

	// Users who are not admins cannot read logs.
	anon := newTestServer(t)
	if code := anon.do("GET", "/api/v1/system/logs", nil, nil); code != 401 {
		t.Errorf("anonymous logs: %d", code)
	}
}
