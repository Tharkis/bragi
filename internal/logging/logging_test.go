package logging

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := map[string]string{
		`Get "http://prowlarr:9696/api?t=search&apikey=abc123&q=x"`: `Get "http://prowlarr:9696/api?t=search&apikey=***&q=x"`,
		"http://user:hunter2@host/path":                             "http://user:***@host/path",
		`{"password":"hunter2","name":"x"}`:                         `{"password":"***","name":"x"}`,
		"nothing secret here":                                       "nothing secret here",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLoggerWritesEverywhereAndRedacts(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	m, err := New(&out, dir, slog.LevelInfo)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	log := m.Logger().With("component", "tv")
	log.Debug("hidden")
	log.Info("searched", "url", "http://x/api?apikey=SECRET", "password", "pw", "err", errors.New("token=abc"))

	for _, s := range []string{out.String(), readFile(t, filepath.Join(dir, FileName))} {
		if strings.Contains(s, "hidden") || strings.Contains(s, "SECRET") || strings.Contains(s, "pw") || strings.Contains(s, "abc") {
			t.Errorf("output leaks a debug line or a secret: %s", s)
		}
		if !strings.Contains(s, "searched") || !strings.Contains(s, "component=tv") {
			t.Errorf("output misses the line: %s", s)
		}
	}
	es, last := m.Ring().Query(Filter{})
	if len(es) != 1 || last != 1 || es[0].Component != "tv" || es[0].Message != "searched" {
		t.Fatalf("ring = %+v, last %d", es, last)
	}

	m.SetLevel(slog.LevelDebug)
	log.Debug("now shown", "n", 3)
	es, _ = m.Ring().Query(Filter{After: 1})
	if len(es) != 1 || es[0].Level != "debug" {
		t.Fatalf("after SetLevel: %+v", es)
	}
	if es, _ := m.Ring().Query(Filter{MinLevel: "info"}); len(es) != 1 {
		t.Errorf("level filter kept %d lines", len(es))
	}
	if es, _ := m.Ring().Query(Filter{Text: "NOW"}); len(es) != 1 {
		t.Errorf("text filter kept %d lines", len(es))
	}
}

func TestRingWraps(t *testing.T) {
	r := NewRing(3)
	for i := 0; i < 5; i++ {
		r.Add(Entry{Message: "m"})
	}
	es, last := r.Query(Filter{})
	if len(es) != 3 || es[0].ID != 3 || es[2].ID != 5 || last != 5 {
		t.Fatalf("got %+v last %d", es, last)
	}
}

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.log")
	f, err := openRotating(path, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("a", 59) + "\n")
	for i := 0; i < 10; i++ {
		f.Write(line)
	}
	f.Close()
	for _, p := range []string{path, path + ".1", path + ".2"} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s missing", p)
		}
	}
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Error("kept too many files")
	}
}

func readFile(t *testing.T, p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
