package integrations

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// newznab speaks the Newznab / Torznab API. Jackett's aggregate endpoint and
// any single Usenet or torrent indexer use it.
type newznab struct {
	apiURL   string
	cfg      Config
	hc       *http.Client
	protocol Protocol
}

func (n *newznab) Test(ctx context.Context) error {
	u, err := url.Parse(n.apiURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("invalid URL %q", n.apiURL)
	}
	q := u.Query()
	q.Set("t", "caps")
	if n.cfg.APIKey != "" {
		q.Set("apikey", n.cfg.APIKey)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	resp, err := do(n.hc, req)
	if err != nil {
		return err
	}
	body, err := readBody(resp)
	if err != nil {
		return err
	}
	root, attrs, err := firstElement(body)
	if err != nil {
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("indexer answered HTTP %d", resp.StatusCode)
		}
		return fmt.Errorf("the response is not a Newznab/Torznab capabilities document: check the URL")
	}
	switch root {
	case "caps":
		return nil
	case "error":
		// Newznab error codes 100-102 are credential problems.
		switch attrs["code"] {
		case "100", "101", "102":
			return ErrUnauthorized
		}
		if d := attrs["description"]; d != "" {
			return fmt.Errorf("indexer error: %s", d)
		}
		return fmt.Errorf("indexer returned error code %s", attrs["code"])
	}
	return fmt.Errorf("unexpected <%s> response: check the URL", root)
}

// tvCategory is the Newznab category for all TV.
const tvCategory = "5000"

func (n *newznab) Search(ctx context.Context, q Query) ([]Release, error) {
	u, err := url.Parse(n.apiURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid URL %q", n.apiURL)
	}
	v := u.Query()
	if n.cfg.APIKey != "" {
		v.Set("apikey", n.cfg.APIKey)
	}
	v.Set("t", "tvsearch")
	v.Set("cat", tvCategory)
	v.Set("limit", "100")
	v.Set("extended", "1")
	v.Set("q", q.Text)
	if q.Season > 0 {
		v.Set("season", strconv.Itoa(q.Season))
		if q.Episode > 0 {
			v.Set("ep", strconv.Itoa(q.Episode))
		}
	}
	u.RawQuery = v.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := do(n.hc, req)
	if err != nil {
		return nil, err
	}
	body, err := readBody(resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("indexer answered HTTP %d", resp.StatusCode)
	}
	return parseFeed(body, n.protocol)
}

type feedAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type feedItem struct {
	Title     string `xml:"title"`
	GUID      string `xml:"guid"`
	Link      string `xml:"link"`
	PubDate   string `xml:"pubDate"`
	Size      int64  `xml:"size"`
	JackettID string `xml:"jackettindexer"`
	Indexer   struct {
		Name string `xml:",chardata"`
	} `xml:"indexer"`
	Enclosure struct {
		URL    string `xml:"url,attr"`
		Length int64  `xml:"length,attr"`
		Type   string `xml:"type,attr"`
	} `xml:"enclosure"`
	Attrs []feedAttr `xml:"attr"`
}

// parseFeed reads a Newznab or Torznab search response.
func parseFeed(body []byte, protocol Protocol) ([]Release, error) {
	root, attrs, err := firstElement(body)
	if err != nil {
		return nil, fmt.Errorf("the indexer's answer is not a search feed")
	}
	if root == "error" {
		switch attrs["code"] {
		case "100", "101", "102":
			return nil, ErrUnauthorized
		}
		return nil, fmt.Errorf("indexer error: %s", attrs["description"])
	}
	var feed struct {
		Channel struct {
			Title string     `xml:"title"`
			Items []feedItem `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("could not read the indexer's search feed: %w", err)
	}
	out := make([]Release, 0, len(feed.Channel.Items))
	for _, it := range feed.Channel.Items {
		r := Release{Title: strings.TrimSpace(it.Title), GUID: it.GUID, DownloadURL: it.Link, Protocol: protocol,
			SizeBytes: it.Size, Indexer: strings.TrimSpace(it.Indexer.Name)}
		if r.Indexer == "" {
			r.Indexer = strings.TrimSpace(it.JackettID)
		}
		if r.Indexer == "" {
			r.Indexer = feed.Channel.Title
		}
		if it.Enclosure.URL != "" {
			r.DownloadURL = it.Enclosure.URL
		}
		if r.SizeBytes == 0 {
			r.SizeBytes = it.Enclosure.Length
		}
		if t, err := time.Parse(time.RFC1123Z, it.PubDate); err == nil {
			r.PublishedAt = t
		} else if t, err := time.Parse(time.RFC1123, it.PubDate); err == nil {
			r.PublishedAt = t
		}
		peers := -1
		for _, a := range it.Attrs {
			switch strings.ToLower(a.Name) {
			case "size":
				if n, err := strconv.ParseInt(a.Value, 10, 64); err == nil && n > 0 {
					r.SizeBytes = n
				}
			case "seeders":
				r.Seeders, _ = strconv.Atoi(a.Value)
			case "peers":
				peers, _ = strconv.Atoi(a.Value)
			case "magneturl":
				if r.DownloadURL == "" || protocol == Torrent && !strings.HasPrefix(r.DownloadURL, "http") {
					r.DownloadURL = a.Value
				}
			case "infohash":
				r.InfoHash = strings.ToLower(a.Value)
			}
		}
		if r.Seeders == 0 && peers > 0 {
			r.Seeders = peers
		}
		if r.Title == "" || r.DownloadURL == "" {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// firstElement returns the name and attributes of the XML root element.
func firstElement(body []byte) (string, map[string]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return "", nil, errors.New("empty document")
		}
		if err != nil {
			return "", nil, err
		}
		if se, ok := tok.(xml.StartElement); ok {
			attrs := map[string]string{}
			for _, a := range se.Attr {
				attrs[a.Name.Local] = a.Value
			}
			return se.Name.Local, attrs, nil
		}
	}
}
