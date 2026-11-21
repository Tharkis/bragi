package books

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dhowden/tag"

	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/scan"
	"github.com/Tharkis/bragi/internal/store"
)

// Adopting books already on disk.
//
// Book libraries come in many layouts. Grimmory and Booklore let people pick
// their own naming pattern ("{authors}/<{series}/><{seriesIndex}. >{title}"
// and the like), Calibre writes "Author/Title (123)/Title - Author.epub",
// Audiobookshelf reads "Author/Series/Title/…", and plenty of folders are
// just "Author - Title.epub". So a scan does not trust one layout: it finds
// the books first (ebook files, audiobook folders), reads the title and
// author from the file's own metadata where there is some, falls back to
// the names on the path, and checks every guess against Open Library.

// A unit is one book on disk. For ebooks its key is the path of one of its
// files (the same book in several formats sits side by side with the same
// name); for audiobooks it is the folder holding the parts, or the file
// itself when several single-file audiobooks share a folder.

var reDisc = regexp.MustCompile(`(?i)^(cd|dis[ck]|part|pt|vol(ume)?)[\s._-]*\d+\b`)

// Units lists the books in a library.
func (s *Service) Units(ctx context.Context, lib *store.Library) ([]string, error) {
	if _, err := os.Stat(lib.Path); err != nil {
		return nil, err
	}
	var units []string
	walkDirs(lib.Path, func(dir string, files []string) {
		rel := func(p string) string { r, _ := filepath.Rel(lib.Path, p); return filepath.ToSlash(r) }
		if lib.MediaType == "book" {
			seen := map[string]bool{}
			for _, f := range files {
				if !integrations.IsEbookFile(f) {
					continue
				}
				stem := strings.TrimSuffix(f, filepath.Ext(f))
				if !seen[stem] {
					seen[stem] = true
					units = append(units, rel(f))
				}
			}
			return
		}
		if reDisc.MatchString(filepath.Base(dir)) && dir != lib.Path {
			return // parts of the folder above
		}
		groups := audioGroups(dir, files)
		if len(groups) == 0 {
			return
		}
		if len(groups) == 1 && len(groups[0]) > 1 && dir != lib.Path {
			units = append(units, rel(dir))
			return
		}
		for _, g := range groups {
			units = append(units, rel(g[0]))
		}
	})
	sort.Strings(units)
	return units, nil
}

// walkDirs calls fn for every folder under root with its files, skipping
// hidden entries.
func walkDirs(root string, fn func(dir string, files []string)) {
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") && p != root {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		entries, err := os.ReadDir(p)
		if err != nil {
			return nil
		}
		var files []string
		for _, e := range entries {
			if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				files = append(files, filepath.Join(p, e.Name()))
			}
		}
		fn(p, files)
		return nil
	})
}

// audioGroups splits a folder's audio files into books. Parts of one book
// share an album tag (or have none); several .m4b files with different
// albums, or different names and no tags, are separate books.
func audioGroups(dir string, files []string) [][]string {
	var audio []string
	for _, f := range files {
		if integrations.IsAudioFile(f) {
			audio = append(audio, f)
		}
	}
	// Disc sub-folders belong to this folder's book.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() && reDisc.MatchString(e.Name()) {
			walkDirs(filepath.Join(dir, e.Name()), func(_ string, fs []string) {
				for _, f := range fs {
					if integrations.IsAudioFile(f) {
						audio = append(audio, f)
					}
				}
			})
		}
	}
	sort.Strings(audio)
	if len(audio) <= 1 {
		if len(audio) == 1 {
			return [][]string{audio}
		}
		return nil
	}
	byAlbum := map[string][]string{}
	var order []string
	allWhole := true
	for _, f := range audio {
		if ext := strings.ToLower(filepath.Ext(f)); ext != ".m4b" {
			allWhole = false
		}
		key := ""
		if t := readTags(f); t != nil {
			key = strings.ToLower(strings.TrimSpace(t.album))
		}
		if _, ok := byAlbum[key]; !ok {
			order = append(order, key)
		}
		byAlbum[key] = append(byAlbum[key], f)
	}
	if len(order) == 1 && order[0] == "" && allWhole && !looksLikeParts(audio) {
		// Untagged .m4b files with unrelated names: one book each.
		out := make([][]string, 0, len(audio))
		for _, f := range audio {
			out = append(out, []string{f})
		}
		return out
	}
	if len(order) == 1 {
		return [][]string{audio}
	}
	out := make([][]string, 0, len(order))
	for _, k := range order {
		out = append(out, byAlbum[k])
	}
	return out
}

var reTrackNo = regexp.MustCompile(`(?i)(^|[\s._-])(\d{1,3}|part[\s._-]*\d+|chapter[\s._-]*\d+)([\s._-]|$)`)

// looksLikeParts reports whether files are numbered parts of one book.
func looksLikeParts(files []string) bool {
	n := 0
	for _, f := range files {
		if reTrackNo.MatchString(strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))) {
			n++
		}
	}
	return n == len(files)
}

// unitFiles returns the files of a unit, absolute.
func unitFiles(lib *store.Library, unit string) ([]string, error) {
	p := filepath.Join(lib.Path, filepath.FromSlash(unit))
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		entries, _ := os.ReadDir(p)
		var files []string
		for _, e := range entries {
			if !e.IsDir() {
				files = append(files, filepath.Join(p, e.Name()))
			}
		}
		for _, g := range audioGroups(p, files) {
			return g, nil
		}
		return nil, errors.New("no audio files")
	}
	dir := filepath.Dir(p)
	if lib.MediaType == "book" {
		stem := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))
		entries, _ := os.ReadDir(dir)
		var out []string
		for _, e := range entries {
			n := e.Name()
			if !e.IsDir() && integrations.IsEbookFile(n) && strings.TrimSuffix(n, filepath.Ext(n)) == stem {
				out = append(out, filepath.Join(dir, n))
			}
		}
		return out, nil
	}
	return []string{p}, nil
}

// guess is what a unit says about itself.
type guess struct {
	titles []string // best first
	author string
	year   int
}

func (g guess) String() string {
	t := ""
	if len(g.titles) > 0 {
		t = g.titles[0]
	}
	if g.author != "" {
		return strings.TrimSpace(t + " " + g.author)
	}
	return t
}

var (
	reYear      = regexp.MustCompile(`\s*[\(\[]((?:1[4-9]|20)\d\d)[\)\]]\s*`)
	reCalibreID = regexp.MustCompile(`\s*\(\d+\)\s*$`)
	reBrackets  = regexp.MustCompile(`\s*\[[^\]]*\]\s*`)
	reIndex     = regexp.MustCompile(`^(?i)(book|vol(ume)?|#)?\s*\d{1,3}(\.\d+)?\s*([.\-–:)]|\s-\s)\s*`)
	reSpaces    = regexp.MustCompile(`\s+`)
)

// cleanName turns a file or folder name into a title, returning any year
// it carried.
func cleanName(n string) (string, int) {
	n = strings.ReplaceAll(n, "_", " ")
	year := 0
	if m := reYear.FindStringSubmatch(n); m != nil {
		year, _ = strconv.Atoi(m[1])
		n = reYear.ReplaceAllString(n, " ")
	}
	n = reCalibreID.ReplaceAllString(n, "")
	n = reBrackets.ReplaceAllString(n, " ")
	n = reIndex.ReplaceAllString(strings.TrimSpace(n), "")
	n = reSpaces.ReplaceAllString(n, " ")
	return strings.Trim(n, " -,"), year
}

// guessUnit reads a unit's metadata and path.
func guessUnit(lib *store.Library, unit string, files []string) guess {
	var g guess
	add := func(t string) {
		t = strings.TrimSpace(t)
		if t == "" {
			return
		}
		for _, x := range g.titles {
			if strings.EqualFold(x, t) {
				return
			}
		}
		g.titles = append(g.titles, t)
	}
	// The file's own metadata, which Grimmory, Calibre and taggers write.
	for _, f := range files {
		if strings.EqualFold(filepath.Ext(f), ".epub") {
			if t, a := readEPUB(f); t != "" {
				add(t)
				if g.author == "" {
					g.author = a
				}
				break
			}
		}
		if integrations.IsAudioFile(f) {
			if t := readTags(f); t != nil && (t.album != "" || t.title != "") {
				if t.album != "" {
					add(t.album)
				} else if len(files) == 1 {
					add(t.title)
				}
				if g.author == "" {
					g.author = t.author
				}
				if g.year == 0 {
					g.year = t.year
				}
				break
			}
		}
	}

	// The path: "Author/…/Title" with the name cleaned up.
	parts := strings.Split(unit, "/")
	last := parts[len(parts)-1]
	if integrations.IsEbookFile(last) || integrations.IsAudioFile(last) {
		last = strings.TrimSuffix(last, filepath.Ext(last))
	}
	name, year := cleanName(last)
	if g.year == 0 {
		g.year = year
	}
	folderAuthor := ""
	if len(parts) >= 2 {
		folderAuthor, _ = cleanName(parts[0])
	}
	parentTitle := ""
	if len(parts) >= 3 && last != parts[len(parts)-1] {
		// A file in "Author/Title/…": Calibre's "Author/Title (123)/Title -
		// Author.epub", or an audiobook kept under its original file name.
		// The folder above the file may be the title.
		parentTitle, _ = cleanName(parts[len(parts)-2])
		if strings.HasPrefix(strings.ToLower(name), strings.ToLower(parentTitle)) {
			add(parentTitle)
			parentTitle = ""
		}
	}

	if a, t, ok := strings.Cut(name, " - "); ok {
		// "Author - Title" or "Title - Author": the side that is the folder
		// or tagged author is the author.
		known := g.author
		if known == "" {
			known = folderAuthor
		}
		switch {
		case known != "" && sameAuthor(known, t):
			add(a)
		case known != "" && sameAuthor(known, a):
			add(t)
		default:
			add(t)
			add(a)
			if folderAuthor == "" && g.author == "" {
				folderAuthor = a
			}
		}
	} else {
		add(name)
	}
	if parentTitle != "" {
		add(parentTitle)
	}
	if g.author == "" {
		g.author = folderAuthor
	}
	return g
}

// sameAuthor reports whether two spellings name the same author: "J.R.R.
// Tolkien", "Tolkien, J. R. R." and "JRR Tolkien" all do.
func sameAuthor(a, b string) bool {
	sa, sb := authorSurname(a), authorSurname(b)
	if sa == "" || sb == "" {
		return false
	}
	return sa == sb || contains(words(b), sa) || contains(words(a), sb)
}

func authorSurname(a string) string {
	if i := strings.Index(a, ","); i > 0 && !strings.Contains(a[i:], "&") {
		a = a[:i] // "Last, First"
	} else if i := strings.IndexAny(a, "&;"); i > 0 {
		a = a[:i] // first of several
	}
	return surname(a)
}

func contains(ws []string, w string) bool {
	for _, x := range ws {
		if x == w {
			return true
		}
	}
	return false
}

func sameTitle(a, b string) bool {
	na, nb := strings.Join(words(mainTitle(a)), " "), strings.Join(words(mainTitle(b)), " ")
	return na != "" && (na == nb || strings.Join(words(a), " ") == strings.Join(words(b), " "))
}

// Adopt matches a book on disk with Open Library, or uses the work given,
// and records it as imported with its files where they are.
func (s *Service) Adopt(ctx context.Context, lib *store.Library, unit, workID string) (scan.Result, error) {
	files, err := unitFiles(lib, unit)
	if err != nil || len(files) == 0 {
		return scan.Result{}, &scan.NoMatchError{Reason: "no book files found"}
	}
	// Already known: nothing to look up.
	owner, fresh := int64(0), 0
	for _, f := range files {
		id, err := s.Store.FileOwner(ctx, f)
		if err != nil {
			return scan.Result{}, err
		}
		if id == 0 {
			fresh++
		} else {
			owner = id
		}
	}
	if fresh > 0 && fresh < len(files) && workID == "" {
		// A new file next to known ones (another format, another part):
		// it is the same book.
		if b, err := s.Store.GetBook(ctx, owner); err == nil {
			workID = b.ForeignID
		}
	}
	if fresh > 0 {
		owner = 0
	}
	if owner != 0 && workID == "" {
		b, err := s.Store.GetBook(ctx, owner)
		if err == nil {
			return scan.Result{Name: bookLabel(b)}, nil
		}
	}

	g := guessUnit(lib, unit, files)
	var w *metadata.Book
	if workID != "" {
		if w, err = s.OpenLibrary().Work(ctx, workID); err != nil {
			return scan.Result{}, err
		}
	} else {
		if w, err = s.matchBook(ctx, g); err != nil {
			return scan.Result{}, err
		}
		// The search result has no description; the work record does.
		if full, err := s.OpenLibrary().Work(ctx, w.ID); err == nil {
			full.Author, full.Year = firstNonEmpty(full.Author, w.Author), firstNonZero(full.Year, w.Year)
			w = full
		}
	}

	var recorded []store.BookFile
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			continue
		}
		recorded = append(recorded, store.BookFile{Path: f, SizeBytes: st.Size(), Format: FormatOf(filepath.Base(f))})
	}
	folder, _ := filepath.Rel(lib.Path, filepath.Dir(files[0]))
	if fi, err := os.Stat(filepath.Join(lib.Path, filepath.FromSlash(unit))); err == nil && fi.IsDir() {
		folder = unit
	}
	_, lookupErr := s.Store.GetBookByForeignID(ctx, lib.MediaType, w.ID)
	isNew := errors.Is(lookupErr, store.ErrNotFound)
	id, err := s.Store.AdoptBook(ctx, store.BookInput{MediaType: lib.MediaType, ForeignID: w.ID, Title: w.Title, Author: w.Author,
		Year: w.Year, Overview: w.Overview, CoverID: metadata.CoverIDString(w.CoverID), LibraryID: lib.ID,
		Folder: filepath.ToSlash(folder)}, recorded)
	if errors.Is(err, store.ErrBookHasFiles) {
		return scan.Result{}, &scan.NoMatchError{Reason: fmt.Sprintf("%s is already in Bragi with other files", w.Title), Guess: g.String()}
	}
	if err != nil {
		return scan.Result{}, err
	}
	s.publish("book", map[string]any{"id": id})
	if done, err := s.Store.MarkBookRequestsAvailable(ctx, id); err == nil && done {
		s.publish("request", map[string]any{"bookId": id, "status": "available"})
	}
	b, _ := s.Store.GetBook(ctx, id)
	name := w.Title
	if b != nil {
		name = bookLabel(b)
	}
	return scan.Result{Name: name, Files: fresh, New: isNew}, nil
}

func bookLabel(b *store.Book) string {
	if b.Author != "" {
		return b.Title + " by " + b.Author
	}
	return b.Title
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func firstNonZero(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

// matchBook searches Open Library for each title guess (with the author,
// then without) and takes the first result whose title and author agree.
func (s *Service) matchBook(ctx context.Context, g guess) (*metadata.Book, error) {
	if len(g.titles) == 0 {
		return nil, &scan.NoMatchError{Reason: "could not read a title", Guess: g.String()}
	}
	ol := s.OpenLibrary()
	tries := 0
	for _, t := range g.titles {
		queries := []string{t}
		if g.author != "" {
			queries = []string{t + " " + g.author, t}
		}
		for _, q := range queries {
			if tries >= 3 {
				break
			}
			tries++
			results, err := ol.SearchBooks(ctx, q)
			if err != nil {
				return nil, err
			}
			for i, r := range results {
				titleOK := sameTitle(r.Title, t)
				authorOK := g.author == "" || sameAuthor(r.Author, g.author)
				if titleOK && authorOK {
					return &results[i], nil
				}
			}
			if g.author == "" {
				break
			}
		}
	}
	return nil, &scan.NoMatchError{Reason: "no match on Open Library", Guess: g.String()}
}

// ---- Reading metadata from files ----

type audioTags struct {
	album, title, author string
	year                 int
}

// readTags reads an audio file's tags (ID3, MP4, FLAC, Ogg), or nil.
func readTags(path string) *audioTags {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil {
		return nil
	}
	t := &audioTags{album: m.Album(), title: m.Title(), author: m.AlbumArtist(), year: m.Year()}
	if t.author == "" {
		t.author = m.Artist()
	}
	if t.author == "" {
		t.author = m.Composer()
	}
	return t
}

// readEPUB reads an EPUB's title and first author from its package file.
func readEPUB(path string) (title, author string) {
	z, err := zip.OpenReader(path)
	if err != nil {
		return "", ""
	}
	defer z.Close()
	open := func(name string) []byte {
		for _, f := range z.File {
			if f.Name == name {
				r, err := f.Open()
				if err != nil {
					return nil
				}
				defer r.Close()
				b, _ := io.ReadAll(io.LimitReader(r, 1<<20))
				return b
			}
		}
		return nil
	}
	var container struct {
		Rootfiles []struct {
			Path string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if xml.Unmarshal(open("META-INF/container.xml"), &container) != nil || len(container.Rootfiles) == 0 {
		return "", ""
	}
	var pkg struct {
		Titles   []string `xml:"metadata>title"`
		Creators []string `xml:"metadata>creator"`
	}
	if xml.Unmarshal(open(container.Rootfiles[0].Path), &pkg) != nil {
		return "", ""
	}
	if len(pkg.Titles) > 0 {
		title = strings.TrimSpace(pkg.Titles[0])
	}
	if len(pkg.Creators) > 0 {
		author = strings.TrimSpace(pkg.Creators[0])
	}
	return title, author
}
