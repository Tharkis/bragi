// Package integrations defines the adapter interfaces for indexers and
// download clients, and the registry of supported kinds.
//
// Adapters are compiled in, not loaded at runtime. Adding a new client is
// one file: implement Indexer or DownloadClient, then add a Kind to kinds.
package integrations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrNotImplemented marks adapter methods that arrive in a later step.
var ErrNotImplemented = errors.New("not implemented yet")

// ErrUnauthorized means the service rejected the API key or password.
var ErrUnauthorized = errors.New("the API key, username or password was rejected")

// Protocol is how a release is transferred.
type Protocol string

// Supported protocols.
const (
	Usenet   Protocol = "usenet"
	Torrent  Protocol = "torrent"
	Soulseek Protocol = "soulseek"
)

// Role says whether a kind searches, downloads, or both.
type Role string

// Roles.
const (
	RoleIndexer    Role = "indexer"
	RoleDownloader Role = "downloader"
)

// Config is what an adapter needs to talk to its service.
type Config struct {
	BaseURL  string
	APIKey   string
	Username string
	Password string
	Category string
}

// Query is a search request. Adapters use the IDs when the indexer supports
// them and fall back to Text.
type Query struct {
	MediaType string
	Text      string
	TMDBID    int
	TVDBID    int
	Season    int
	Episode   int
}

// Release is one search result.
type Release struct {
	Title       string    `json:"title"`
	GUID        string    `json:"guid"`
	DownloadURL string    `json:"downloadUrl"`
	InfoHash    string    `json:"infoHash,omitempty"`
	Protocol    Protocol  `json:"protocol"`
	SizeBytes   int64     `json:"sizeBytes"`
	Seeders     int       `json:"seeders"`
	PublishedAt time.Time `json:"publishedAt"`
	Indexer     string    `json:"indexer"`
}

// Job states every client maps onto.
const (
	JobQueued      = "queued"
	JobDownloading = "downloading"
	JobCompleted   = "completed"
	JobFailed      = "failed"
	// JobMissing means the client no longer knows the job.
	JobMissing = "missing"
)

// Job is a download as the client reports it.
type Job struct {
	ID       string
	State    string
	Progress float64 // 0 to 1
	// OutputPath is the finished file or folder, as the client sees it.
	OutputPath string
	Error      string
}

// Indexer searches for releases.
type Indexer interface {
	Test(ctx context.Context) error
	Search(ctx context.Context, q Query) ([]Release, error)
}

// DownloadClient downloads releases.
type DownloadClient interface {
	Test(ctx context.Context) error
	Add(ctx context.Context, r Release) (jobID string, err error)
	Status(ctx context.Context, jobIDs []string) ([]Job, error)
	Remove(ctx context.Context, jobID string, deleteFiles bool) error
}

// Field describes one setting the UI asks for.
type Field struct {
	Name        string `json:"name"` // baseUrl, apiKey, username, password or category
	Label       string `json:"label"`
	Type        string `json:"type"` // text, url or password
	Placeholder string `json:"placeholder,omitempty"`
	Help        string `json:"help,omitempty"`
	Required    bool   `json:"required"`
}

// Kind is a supported integration and the form the UI shows for it.
type Kind struct {
	ID        string     `json:"id"`
	Label     string     `json:"label"`
	Role      Role       `json:"role"`
	Protocols []Protocol `json:"protocols"`
	Fields    []Field    `json:"fields"`
}

var (
	fieldBaseURL = func(placeholder string) Field {
		return Field{Name: "baseUrl", Label: "URL", Type: "url", Placeholder: placeholder, Required: true}
	}
	fieldAPIKey   = Field{Name: "apiKey", Label: "API key", Type: "password", Required: true}
	fieldUsername = Field{Name: "username", Label: "Username", Type: "text"}
	fieldPassword = Field{Name: "password", Label: "Password", Type: "password"}
	fieldCategory = Field{Name: "category", Label: "Category", Type: "text", Placeholder: "tv",
		Help: "Bragi files its downloads under this category so it never touches yours."}
)

var kinds = []Kind{
	{ID: "prowlarr", Label: "Prowlarr", Role: RoleIndexer, Protocols: []Protocol{Usenet, Torrent},
		Fields: []Field{fieldBaseURL("http://192.168.1.10:9696"), fieldAPIKey}},
	{ID: "jackett", Label: "Jackett", Role: RoleIndexer, Protocols: []Protocol{Torrent},
		Fields: []Field{fieldBaseURL("http://192.168.1.10:9117"), fieldAPIKey}},
	{ID: "newznab", Label: "Newznab (any Usenet indexer)", Role: RoleIndexer, Protocols: []Protocol{Usenet},
		Fields: []Field{{Name: "baseUrl", Label: "API URL", Type: "url", Placeholder: "https://indexer.example/api", Required: true}, fieldAPIKey}},
	{ID: "torznab", Label: "Torznab (any torrent indexer)", Role: RoleIndexer, Protocols: []Protocol{Torrent},
		Fields: []Field{{Name: "baseUrl", Label: "API URL", Type: "url", Placeholder: "http://192.168.1.10:9117/api/v2.0/indexers/x/results/torznab/api", Required: true}, fieldAPIKey}},
	{ID: "nzbget", Label: "NZBGet", Role: RoleDownloader, Protocols: []Protocol{Usenet},
		Fields: []Field{fieldBaseURL("http://192.168.1.10:6789"), fieldUsername, fieldPassword, fieldCategory}},
	{ID: "qbittorrent", Label: "qBittorrent", Role: RoleDownloader, Protocols: []Protocol{Torrent},
		Fields: []Field{fieldBaseURL("http://192.168.1.10:8080"), fieldUsername, fieldPassword, fieldCategory}},
	{ID: "slskd", Label: "slskd (Soulseek)", Role: RoleDownloader, Protocols: []Protocol{Soulseek},
		Fields: []Field{fieldBaseURL("http://192.168.1.10:5030"),
			{Name: "apiKey", Label: "API key", Type: "password", Required: true,
				Help: "An API key from slskd's settings (web.authentication.api_keys)."},
			{Name: "category", Label: "Downloads folder", Type: "text", Placeholder: "Ask slskd",
				Help: "Where slskd's finished downloads are, as Bragi sees the path. Leave empty if slskd and Bragi use the same path."}}},
}

// Kinds returns every supported integration kind.
func Kinds() []Kind { return kinds }

// KindByID finds a kind.
func KindByID(id string) (Kind, bool) {
	for _, k := range kinds {
		if k.ID == id {
			return k, true
		}
	}
	return Kind{}, false
}

// NewIndexer builds the adapter for kind.
func NewIndexer(kind string, cfg Config, hc *http.Client) (Indexer, error) {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	switch kind {
	case "prowlarr":
		return &prowlarr{cfg: cfg, hc: hc}, nil
	case "jackett":
		return &newznab{apiURL: cfg.BaseURL + "/api/v2.0/indexers/all/results/torznab/api", cfg: cfg, hc: hc, protocol: Torrent}, nil
	case "newznab":
		return &newznab{apiURL: cfg.BaseURL, cfg: cfg, hc: hc, protocol: Usenet}, nil
	case "torznab":
		return &newznab{apiURL: cfg.BaseURL, cfg: cfg, hc: hc, protocol: Torrent}, nil
	}
	return nil, fmt.Errorf("unknown indexer kind %q", kind)
}

// NewDownloadClient builds the adapter for kind.
func NewDownloadClient(kind string, cfg Config, hc *http.Client) (DownloadClient, error) {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	switch kind {
	case "nzbget":
		return &nzbget{cfg: cfg, hc: hc}, nil
	case "qbittorrent":
		return &qbittorrent{cfg: cfg, hc: hc}, nil
	case "slskd":
		return &slskd{cfg: cfg, hc: hc}, nil
	}
	return nil, fmt.Errorf("unknown download client kind %q", kind)
}

// do sends req and turns transport errors and common status codes into
// messages a user can act on.
func do(hc *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach %s: %w", req.URL.Host, err)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		resp.Body.Close()
		return nil, ErrUnauthorized
	case http.StatusNotFound:
		resp.Body.Close()
		return nil, fmt.Errorf("%s answered 404 Not Found: check the URL and any URL base", req.URL.Host)
	}
	return resp, nil
}

// readBody reads at most 4 MB.
func readBody(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// fetch downloads a small file (an NZB or a .torrent) that an indexer links.
func fetch(ctx context.Context, hc *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := do(hc, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s answered HTTP %d", req.URL.Host, resp.StatusCode)
	}
	return readBody(resp)
}
