package api

import (
	"archive/zip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tharkis/bragi/internal/scan"
	"github.com/Tharkis/bragi/internal/store"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeEPUB writes a minimal EPUB whose package file names the book.
func writeEPUB(t *testing.T, path, title, author string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	w, _ := z.Create("META-INF/container.xml")
	w.Write([]byte(`<?xml version="1.0"?><container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles>` +
		`<rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`))
	w, _ = z.Create("OEBPS/content.opf")
	w.Write([]byte(`<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/">` +
		`<dc:title>` + title + `</dc:title><dc:creator>` + author + `</dc:creator></metadata></package>`))
	z.Close()
	f.Close()
}

func (c *client) waitScan(mediaType string) scan.Status {
	c.t.Helper()
	var st scan.Status
	for i := 0; i < 200; i++ {
		st = scan.Status{}
		c.do("GET", "/api/v1/libraries/scan?type="+mediaType, nil, &st)
		if !st.Running && !st.Queued && st.Finished != "" {
			return st
		}
		time.Sleep(20 * time.Millisecond)
	}
	var all scanAllState
	c.do("GET", "/api/v1/libraries/scan/all", nil, &all)
	c.t.Fatalf("scan of %s did not finish: %+v all %+v", mediaType, st, all)
	return st
}

func TestAdoptEbooks(t *testing.T) {
	e := newBookEnv(t, false)
	lib := e.ebooks
	// Grimmory-style patterns, Calibre, a loose file, and a file whose name
	// says nothing but whose metadata does.
	writeFile(t, filepath.Join(lib, "Andy Weir", "Project Hail Mary (2021).epub"), "e")
	writeFile(t, filepath.Join(lib, "Andy Weir", "Project Hail Mary (2021).pdf"), "p")
	writeFile(t, filepath.Join(lib, "Frank Herbert", "Dune", "01. Dune.epub"), "e")
	writeFile(t, filepath.Join(lib, "Tolkien, J.R.R.", "The Hobbit (123)", "The Hobbit - J.R.R. Tolkien.mobi"), "m")
	writeFile(t, filepath.Join(lib, "Susanna Clarke - Piranesi.azw3"), "a")
	writeEPUB(t, filepath.Join(lib, "inbox", "book_8812.epub"), "Klara and the Sun", "Kazuo Ishiguro")
	writeFile(t, filepath.Join(lib, "Unknown", "Some Notes.pdf"), "n")
	writeFile(t, filepath.Join(lib, "Unknown", "cover.jpg"), "c")

	if code := e.do("POST", "/api/v1/libraries/scan?type=book", nil, nil); code != 202 {
		t.Fatalf("scan: %d", code)
	}
	st := e.waitScan("book")
	sort.Strings(st.Added)
	want := []string{"Dune by Frank Herbert", "Klara and the Sun by Kazuo Ishiguro", "Piranesi by Susanna Clarke",
		"Project Hail Mary by Andy Weir", "The Hobbit by J.R.R. Tolkien"}
	if strings.Join(st.Added, "|") != strings.Join(want, "|") || st.Files != 6 || len(st.Unmatched) != 1 {
		t.Fatalf("scan result: %+v", st)
	}

	var list []bookResponse
	e.do("GET", "/api/v1/books?type=book", nil, &list)
	if len(list) != 5 {
		t.Fatalf("books: %+v", list)
	}
	for _, b := range list {
		if b.Status != "imported" || b.Monitored {
			t.Fatalf("adopted book should be imported and not searched for: %+v", b.Book)
		}
		if b.Title == "Project Hail Mary" {
			var full bookResponse
			e.do("GET", "/api/v1/books/"+itoa64(b.ID), nil, &full)
			if len(full.Files) != 2 || full.Folder != "Andy Weir" {
				t.Fatalf("Project Hail Mary should keep both formats in place: %+v", full.Book)
			}
		}
	}

	// The unmatched file is listed with what was read from it.
	var un []store.Unmatched
	e.do("GET", "/api/v1/libraries/unmatched?type=book", nil, &un)
	if len(un) != 1 || un[0].Path != "Unknown/Some Notes.pdf" || un[0].Guess != "Some Notes Unknown" {
		t.Fatalf("unmatched: %+v", un)
	}
	// Ignored, it leaves the list and later scans skip it.
	if code := e.do("PUT", "/api/v1/libraries/unmatched/"+itoa64(un[0].ID)+"/ignore", nil, nil); code != 200 {
		t.Fatalf("ignore: %d", code)
	}
	e.do("GET", "/api/v1/libraries/unmatched?type=book", nil, &un)
	if len(un) != 0 {
		t.Fatalf("ignored item still listed: %+v", un)
	}

	// Scanning again adds nothing and links nothing twice.
	e.do("POST", "/api/v1/libraries/scan?type=book", nil, nil)
	st = e.waitScan("book")
	if len(st.Added) != 0 || st.Files != 0 || len(st.Unmatched) != 0 {
		t.Fatalf("second scan: %+v", st)
	}
	// A new format of a known book is picked up.
	writeFile(t, filepath.Join(lib, "Frank Herbert", "Dune", "01. Dune.azw3"), "k")
	e.do("POST", "/api/v1/libraries/scan?type=book", nil, nil)
	if st = e.waitScan("book"); len(st.Added) != 0 || st.Files != 1 {
		t.Fatalf("scan after a new format: %+v", st)
	}
}

func TestAdoptAudiobooksAndMatchByHand(t *testing.T) {
	e := newBookEnv(t, false)
	lib := e.audio
	writeFile(t, filepath.Join(lib, "Andy Weir", "The Martian", "CD1", "01 - Chapter 1.mp3"), "a")
	writeFile(t, filepath.Join(lib, "Andy Weir", "The Martian", "CD2", "01 - Chapter 9.mp3"), "b")
	writeFile(t, filepath.Join(lib, "Brandon Sanderson", "Stormlight", "01. The Way of Kings.m4b"), "c")
	writeFile(t, filepath.Join(lib, "Brandon Sanderson", "Mystery Tape.m4b"), "d")
	// Grimmory's "{authors}/{title}/" pattern keeps the original file name.
	writeFile(t, filepath.Join(lib, "Andy Weir", "Project Hail Mary", "phm_unabridged_final.m4b"), "e")

	e.do("POST", "/api/v1/libraries/scan?type=audiobook", nil, nil)
	st := e.waitScan("audiobook")
	sort.Strings(st.Added)
	if strings.Join(st.Added, "|") != "Project Hail Mary by Andy Weir|The Martian by Andy Weir|The Way of Kings by Brandon Sanderson" || st.Files != 4 || len(st.Unmatched) != 1 {
		t.Fatalf("scan result: %+v", st)
	}

	var un []store.Unmatched
	e.do("GET", "/api/v1/libraries/unmatched?type=audiobook", nil, &un)
	if len(un) != 1 || un[0].Path != "Brandon Sanderson/Mystery Tape.m4b" {
		t.Fatalf("unmatched: %+v", un)
	}
	var res struct {
		Name  string `json:"name"`
		Files int    `json:"files"`
	}
	if code := e.do("POST", "/api/v1/libraries/unmatched/"+itoa64(un[0].ID)+"/match", map[string]string{"id": "OL1000006W"}, &res); code != 200 {
		t.Fatalf("match: %d", code)
	}
	if res.Name != "Tomorrow, and Tomorrow, and Tomorrow by Gabrielle Zevin" || res.Files != 1 {
		t.Fatalf("match result: %+v", res)
	}
	e.do("GET", "/api/v1/libraries/unmatched?type=audiobook", nil, &un)
	if len(un) != 0 {
		t.Fatalf("matched item still listed: %+v", un)
	}
	var list []bookResponse
	e.do("GET", "/api/v1/books?type=audiobook", nil, &list)
	if len(list) != 4 {
		t.Fatalf("audiobooks: %+v", list)
	}
	for _, b := range list {
		if b.Title == "The Martian" {
			var full bookResponse
			e.do("GET", "/api/v1/books/"+itoa64(b.ID), nil, &full)
			if len(full.Files) != 2 || full.Folder != "Andy Weir/The Martian" {
				t.Fatalf("The Martian should keep both discs: %+v", full.Book)
			}
		}
	}

	// Scan all libraries covers both book libraries.
	var all scanAllState
	if code := e.do("POST", "/api/v1/libraries/scan/all", nil, &all); code != 202 || !all.Nightly {
		t.Fatalf("scan all: %d %+v", code, all)
	}
	e.waitScan("book")
	if st := e.waitScan("audiobook"); len(st.Added) != 0 || st.Files != 0 {
		t.Fatalf("rescan: %+v", st)
	}
	e.do("GET", "/api/v1/libraries/scan/all", nil, &all)
	if all.LastFull == "" || all.Running {
		t.Fatalf("scan all state: %+v", all)
	}
}

func TestMatchShowFolderByHand(t *testing.T) {
	e := newTVEnv(t)
	writeFile(t, filepath.Join(e.library, "bluey stuff", "Bluey.S01E01.mkv"), "x")
	e.do("POST", "/api/v1/libraries/scan", nil, nil)
	st := e.waitScan("tv")
	if len(st.Added) != 0 || len(st.Unmatched) != 1 {
		t.Fatalf("scan: %+v", st)
	}
	var un []store.Unmatched
	e.do("GET", "/api/v1/libraries/unmatched?type=tv", nil, &un)
	if len(un) != 1 || un[0].Path != "bluey stuff" {
		t.Fatalf("unmatched: %+v", un)
	}
	var res struct {
		Name  string `json:"name"`
		Files int    `json:"files"`
	}
	if code := e.do("POST", "/api/v1/libraries/unmatched/"+itoa64(un[0].ID)+"/match", map[string]string{"id": "82728"}, &res); code != 200 {
		t.Fatalf("match: %d", code)
	}
	if res.Name != "Bluey (2018)" || res.Files != 1 {
		t.Fatalf("match result: %+v", res)
	}
	// The folder is now the show's: a later scan finds it without TMDB
	// matching its odd name.
	e.do("POST", "/api/v1/libraries/scan", nil, nil)
	if st = e.waitScan("tv"); len(st.Unmatched) != 0 {
		t.Fatalf("rescan: %+v", st)
	}
}

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }
