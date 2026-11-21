package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// prowlarr talks to Prowlarr's v1 API. One connection covers every indexer
// configured in Prowlarr.
type prowlarr struct {
	cfg Config
	hc  *http.Client
}

func (p *prowlarr) Test(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.BaseURL+"/api/v1/system/status", nil)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("X-Api-Key", p.cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	resp, err := do(p.hc, req)
	if err != nil {
		return err
	}
	body, err := readBody(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Prowlarr answered HTTP %d", resp.StatusCode)
	}
	var status struct {
		AppName string `json:"appName"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &status); err != nil || status.Version == "" {
		return fmt.Errorf("the response does not look like Prowlarr: check the URL")
	}
	return nil
}

// SearchText is the text query for q, e.g. "Severance S01E02",
// "Severance S01" for a season pack, or "Dune Part Two 2024" for a movie.
func SearchText(q Query) string {
	switch {
	case q.MediaType == "movie" && q.Year > 0:
		return fmt.Sprintf("%s %d", q.Text, q.Year)
	case q.Season > 0 && q.Episode > 0:
		return fmt.Sprintf("%s S%02dE%02d", q.Text, q.Season, q.Episode)
	case q.Season > 0:
		return fmt.Sprintf("%s S%02d", q.Text, q.Season)
	}
	return q.Text
}

func (p *prowlarr) Search(ctx context.Context, q Query) ([]Release, error) {
	_, cats := searchParams(q)
	v := url.Values{"query": {SearchText(q)}, "type": {"search"}, "categories": cats, "limit": {"100"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.BaseURL+"/api/v1/search?"+v.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("X-Api-Key", p.cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	resp, err := do(p.hc, req)
	if err != nil {
		return nil, err
	}
	body, err := readBody(resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Prowlarr answered HTTP %d", resp.StatusCode)
	}
	var results []struct {
		Title       string    `json:"title"`
		GUID        string    `json:"guid"`
		DownloadURL string    `json:"downloadUrl"`
		MagnetURL   string    `json:"magnetUrl"`
		InfoHash    string    `json:"infoHash"`
		Size        int64     `json:"size"`
		Seeders     int       `json:"seeders"`
		Protocol    string    `json:"protocol"`
		Indexer     string    `json:"indexer"`
		PublishDate time.Time `json:"publishDate"`
	}
	if err := json.Unmarshal(body, &results); err != nil {
		return nil, fmt.Errorf("could not read Prowlarr's search results: %w", err)
	}
	out := make([]Release, 0, len(results))
	for _, r := range results {
		rel := Release{Title: r.Title, GUID: r.GUID, DownloadURL: r.DownloadURL, InfoHash: strings.ToLower(r.InfoHash),
			SizeBytes: r.Size, Seeders: r.Seeders, Indexer: r.Indexer, PublishedAt: r.PublishDate, Protocol: Protocol(r.Protocol)}
		if rel.DownloadURL == "" {
			rel.DownloadURL = r.MagnetURL
		}
		if rel.Protocol != Usenet && rel.Protocol != Torrent || rel.DownloadURL == "" {
			continue
		}
		out = append(out, rel)
	}
	return out, nil
}
