package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Open Library needs no API key. These are variables so tests and demos can
// point them at a fake.
var (
	OpenLibraryURL = "https://openlibrary.org"
	CoversURL      = "https://covers.openlibrary.org"
)

// ErrNotFound means Open Library has no such work.
var ErrNotFound = errors.New("Open Library has no such book")

// OpenLibrary looks up books on openlibrary.org.
type OpenLibrary struct {
	HC *http.Client
}

// Book is a search result or a work's details.
type Book struct {
	// ID is the Open Library work key without its prefix, e.g. "OL27448W".
	ID       string `json:"id"`
	Title    string `json:"title"`
	Author   string `json:"author"`
	Year     int    `json:"year,omitempty"`
	CoverID  int    `json:"coverId,omitempty"`
	Overview string `json:"overview"`
}

// CoverURL returns the cover image for a cover id. size is S, M or L.
func CoverURL(coverID string, size string) string {
	if coverID == "" || coverID == "0" {
		return ""
	}
	return CoversURL + "/b/id/" + coverID + "-" + size + ".jpg"
}

var reWorkID = regexp.MustCompile(`^OL\d+W$`)

// ValidWorkID reports whether id looks like an Open Library work key.
func ValidWorkID(id string) bool { return reWorkID.MatchString(id) }

func (o *OpenLibrary) get(ctx context.Context, path string, q url.Values, out any) error {
	u := OpenLibraryURL + path
	if q != nil {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	// Open Library asks API users to identify themselves.
	req.Header.Set("User-Agent", "Bragi media manager (https://github.com/Tharkis/bragi)")
	hc := o.HC
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach Open Library: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("Open Library answered HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("could not read Open Library's answer: %w", err)
	}
	return nil
}

type olDoc struct {
	Key        string   `json:"key"`
	Title      string   `json:"title"`
	AuthorName []string `json:"author_name"`
	FirstYear  int      `json:"first_publish_year"`
	CoverI     int      `json:"cover_i"`
}

func (d olDoc) book() Book {
	b := Book{ID: strings.TrimPrefix(d.Key, "/works/"), Title: d.Title, Year: d.FirstYear, CoverID: d.CoverI}
	if len(d.AuthorName) > 0 {
		b.Author = d.AuthorName[0]
	}
	return b
}

const olFields = "key,title,author_name,first_publish_year,cover_i"

// SearchBooks finds works by title, author or ISBN.
func (o *OpenLibrary) SearchBooks(ctx context.Context, query string) ([]Book, error) {
	var res struct {
		Docs []olDoc `json:"docs"`
	}
	q := url.Values{"q": {query}, "fields": {olFields}, "limit": {"20"}}
	if err := o.get(ctx, "/search.json", q, &res); err != nil {
		return nil, err
	}
	out := make([]Book, 0, len(res.Docs))
	for _, d := range res.Docs {
		if !strings.HasPrefix(d.Key, "/works/") || d.Title == "" {
			continue
		}
		out = append(out, d.book())
	}
	return out, nil
}

// Work loads one work: title, first author, first publication year, cover
// and description.
func (o *OpenLibrary) Work(ctx context.Context, id string) (*Book, error) {
	if !ValidWorkID(id) {
		return nil, ErrNotFound
	}
	// The search index has the author's name and the first publication
	// year, which the work record itself only links to.
	var res struct {
		Docs []olDoc `json:"docs"`
	}
	if err := o.get(ctx, "/search.json", url.Values{"q": {"key:/works/" + id}, "fields": {olFields}}, &res); err != nil {
		return nil, err
	}
	var work struct {
		Title       string          `json:"title"`
		Description json.RawMessage `json:"description"`
		Covers      []int           `json:"covers"`
	}
	if err := o.get(ctx, "/works/"+id+".json", nil, &work); err != nil {
		return nil, err
	}
	b := Book{ID: id, Title: work.Title}
	if len(res.Docs) > 0 {
		b = res.Docs[0].book()
		b.ID = id
	}
	if b.Title == "" {
		b.Title = work.Title
	}
	if b.CoverID == 0 && len(work.Covers) > 0 && work.Covers[0] > 0 {
		b.CoverID = work.Covers[0]
	}
	b.Overview = description(work.Description)
	return &b, nil
}

// description reads a work's description, which is either a string or
// {"type": "/type/text", "value": "..."}, and drops the trailing source
// notes and link lists many entries carry.
func description(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		var obj struct {
			Value string `json:"value"`
		}
		json.Unmarshal(raw, &obj)
		s = obj.Value
	}
	for _, cut := range []string{"\r\n----------", "\n----------", "\n([source]", "\n[source]"} {
		if i := strings.Index(s, cut); i > 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}

// CoverIDString formats a cover id for storage ("" for none).
func CoverIDString(id int) string {
	if id <= 0 {
		return ""
	}
	return strconv.Itoa(id)
}
