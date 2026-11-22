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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MusicBrainz needs no API key but asks for one request a second and a
// User-Agent that names the app. These are variables so tests and demos can
// point them at a fake.
var (
	MusicBrainzURL  = "https://musicbrainz.org/ws/2"
	CoverArtURL     = "https://coverartarchive.org"
	ListenBrainzURL = "https://api.listenbrainz.org"
	CommonsURL      = "https://commons.wikimedia.org"
	// MusicBrainzInterval is the gap kept between requests.
	MusicBrainzInterval = 1100 * time.Millisecond
)

// ErrNoSuchMusic means MusicBrainz has no such artist, album or release.
var ErrNoSuchMusic = errors.New("MusicBrainz has no such artist or album")

// Cache keeps metadata lookups. The store implements it.
type Cache interface {
	GetCache(ctx context.Context, key string, maxAge time.Duration) (string, bool)
	SetCache(ctx context.Context, key, body string)
}

// MusicBrainz looks up artists, albums and track lists.
type MusicBrainz struct {
	HC    *http.Client
	Cache Cache
}

var (
	mbMu   sync.Mutex
	mbNext time.Time
)

// mbWait holds a request back until MusicBrainz's rate limit allows it.
func mbWait(ctx context.Context) error {
	mbMu.Lock()
	wait := time.Until(mbNext)
	if wait < 0 {
		wait = 0
	}
	mbNext = time.Now().Add(wait + MusicBrainzInterval)
	mbMu.Unlock()
	if wait == 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

var reMBID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// VariousArtists is MusicBrainz's artist for compilations. It has far too
// many albums to list, so Bragi only tracks the ones it is given.
const VariousArtists = "89ad4ac3-39f7-470e-963a-56509c546377"

// ValidMBID reports whether id looks like a MusicBrainz id.
func ValidMBID(id string) bool { return reMBID.MatchString(id) }

// get fetches a MusicBrainz path. maxAge > 0 serves and fills the cache.
func (m *MusicBrainz) get(ctx context.Context, path string, q url.Values, maxAge time.Duration, out any) error {
	if q == nil {
		q = url.Values{}
	}
	q.Set("fmt", "json")
	u := MusicBrainzURL + path + "?" + q.Encode()
	if maxAge > 0 && m.Cache != nil {
		if body, ok := m.Cache.GetCache(ctx, "mb:"+path+"?"+q.Encode(), maxAge); ok {
			if json.Unmarshal([]byte(body), out) == nil {
				return nil
			}
		}
	}
	body, err := m.fetch(ctx, u)
	if errors.Is(err, errBusy) {
		// MusicBrainz answers 503 when it is busy; one more try.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
		body, err = m.fetch(ctx, u)
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("MusicBrainz sent something unexpected: %w", err)
	}
	if maxAge > 0 && m.Cache != nil {
		m.Cache.SetCache(ctx, "mb:"+path+"?"+q.Encode(), string(body))
	}
	return nil
}

var errBusy = errors.New("MusicBrainz is busy; try again in a minute")

// UserAgent is what Bragi calls itself to MusicBrainz and ListenBrainz.
const UserAgent = "Bragi/1.0 ( https://github.com/Tharkis/bragi )"

func (m *MusicBrainz) fetch(ctx context.Context, u string) ([]byte, error) {
	if err := mbWait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	hc := m.HC
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach MusicBrainz: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest:
		return nil, ErrNoSuchMusic
	case resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusTooManyRequests:
		return nil, errBusy
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("MusicBrainz answered HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// ---- Types ----

// Artist is a MusicBrainz artist.
type Artist struct {
	ID             string   `json:"artistId"`
	Name           string   `json:"name"`
	SortName       string   `json:"sortName,omitempty"`
	Disambiguation string   `json:"disambiguation,omitempty"`
	Type           string   `json:"type,omitempty"`
	Country        string   `json:"country,omitempty"`
	Genres         []string `json:"genres,omitempty"`
	ImageURL       string   `json:"imageUrl,omitempty"`
	Score          int      `json:"-"`
	// Albums is filled by Artist: every release group, oldest first.
	Albums []Album `json:"albums,omitempty"`
}

// Album is a MusicBrainz release group: an album, EP or single across all
// its editions.
type Album struct {
	ID       string `json:"albumId"`
	Title    string `json:"title"`
	ArtistID string `json:"artistId"`
	Artist   string `json:"artist"`
	// Type is Album, EP, Single, Live, Compilation or Other.
	Type        string `json:"albumType"`
	ReleaseDate string `json:"releaseDate,omitempty"`
	Year        int    `json:"year,omitempty"`
	Score       int    `json:"-"`
}

// Track is one track of a release.
type Track struct {
	RecordingID string `json:"recordingId"`
	Disc        int    `json:"disc"`
	Number      int    `json:"number"`
	Title       string `json:"title"`
	// Seconds is the track's length, 0 when MusicBrainz does not know it.
	Seconds int    `json:"seconds,omitempty"`
	Artist  string `json:"artist,omitempty"`
}

// Release is one edition of an album, with its track list.
type Release struct {
	ID     string  `json:"releaseId"`
	Title  string  `json:"title"`
	Date   string  `json:"date,omitempty"`
	Discs  int     `json:"discs"`
	Tracks []Track `json:"tracks"`
	// AlbumID is the release group it belongs to.
	AlbumID string `json:"albumId,omitempty"`
}

// AlbumTypes are the album types, in the order the UI lists them.
var AlbumTypes = []string{"Album", "EP", "Single", "Live", "Compilation", "Other"}

// albumType folds MusicBrainz's primary and secondary types into one.
func albumType(primary string, secondary []string) string {
	for _, s := range secondary {
		switch s {
		case "Live":
			return "Live"
		case "Compilation":
			return "Compilation"
		case "Soundtrack", "Remix", "DJ-mix", "Mixtape/Street", "Demo", "Spokenword", "Interview", "Audiobook", "Audio drama":
			return "Other"
		}
	}
	switch primary {
	case "Album", "EP", "Single":
		return primary
	}
	return "Other"
}

type mbCredit struct {
	Name       string `json:"name"`
	JoinPhrase string `json:"joinphrase"`
	Artist     struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"artist"`
}

func creditName(cs []mbCredit) string {
	var b strings.Builder
	for _, c := range cs {
		b.WriteString(c.Name)
		b.WriteString(c.JoinPhrase)
	}
	return b.String()
}

type mbReleaseGroup struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	PrimaryType  string     `json:"primary-type"`
	Secondary    []string   `json:"secondary-types"`
	FirstRelease string     `json:"first-release-date"`
	Credits      []mbCredit `json:"artist-credit"`
	Score        int        `json:"score"`
}

func (g mbReleaseGroup) album() Album {
	a := Album{ID: g.ID, Title: g.Title, Type: albumType(g.PrimaryType, g.Secondary), ReleaseDate: g.FirstRelease,
		Year: yearOf(g.FirstRelease), Score: g.Score, Artist: creditName(g.Credits)}
	if len(g.Credits) > 0 {
		a.ArtistID = g.Credits[0].Artist.ID
	}
	return a
}

type mbArtist struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	SortName       string `json:"sort-name"`
	Type           string `json:"type"`
	Country        string `json:"country"`
	Disambiguation string `json:"disambiguation"`
	Score          int    `json:"score"`
	Relations      []struct {
		Type string `json:"type"`
		URL  struct {
			Resource string `json:"resource"`
		} `json:"url"`
	} `json:"relations"`
	Genres []struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	} `json:"genres"`
}

func (a mbArtist) artist() Artist {
	out := Artist{ID: a.ID, Name: a.Name, SortName: a.SortName, Type: a.Type, Country: a.Country, Disambiguation: a.Disambiguation, Score: a.Score}
	for _, r := range a.Relations {
		if r.Type == "image" {
			if img := commonsImage(r.URL.Resource); img != "" {
				out.ImageURL = img
				break
			}
		}
	}
	sort.SliceStable(a.Genres, func(i, j int) bool { return a.Genres[i].Count > a.Genres[j].Count })
	for i, g := range a.Genres {
		if i == 3 {
			break
		}
		out.Genres = append(out.Genres, g.Name)
	}
	return out
}

// commonsImage turns a Wikimedia Commons file page into a link to the
// picture itself, scaled down.
func commonsImage(page string) string {
	const prefix = "https://commons.wikimedia.org/wiki/File:"
	name, ok := strings.CutPrefix(page, prefix)
	if !ok || name == "" {
		return ""
	}
	return CommonsURL + "/wiki/Special:FilePath/" + name + "?width=500"
}

// AlbumCoverURL is the album's front cover from the Cover Art Archive.
// size is 250, 500 or 1200.
func AlbumCoverURL(albumID string, size int) string {
	if albumID == "" {
		return ""
	}
	return fmt.Sprintf("%s/release-group/%s/front-%d", CoverArtURL, albumID, size)
}

// ---- Search ----

// luceneEscape quotes the characters MusicBrainz's search treats as syntax.
func luceneEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`+-&|!(){}[]^"~*?:\/`, r) {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// SearchArtists finds artists by name, best match first.
func (m *MusicBrainz) SearchArtists(ctx context.Context, q string) ([]Artist, error) {
	var res struct {
		Artists []mbArtist `json:"artists"`
	}
	if err := m.get(ctx, "/artist", url.Values{"query": {luceneEscape(q)}, "limit": {"12"}}, 0, &res); err != nil {
		return nil, err
	}
	out := []Artist{}
	for _, a := range res.Artists {
		if a.Score < 50 {
			continue
		}
		out = append(out, a.artist())
	}
	return out, nil
}

// SearchAlbums finds albums by title. "Artist - Album" narrows it to that
// artist.
func (m *MusicBrainz) SearchAlbums(ctx context.Context, q string) ([]Album, error) {
	query := luceneEscape(q)
	if a, t, ok := strings.Cut(q, " - "); ok && strings.TrimSpace(a) != "" && strings.TrimSpace(t) != "" {
		query = fmt.Sprintf("releasegroup:(%s) AND artist:(%s)", luceneEscape(strings.TrimSpace(t)), luceneEscape(strings.TrimSpace(a)))
	}
	return m.searchAlbums(ctx, query, 15)
}

// FindAlbum looks for an album by its artist and title, for a library scan.
func (m *MusicBrainz) FindAlbum(ctx context.Context, artist, title string) ([]Album, error) {
	query := fmt.Sprintf(`releasegroup:(%s)`, luceneEscape(title))
	if artist != "" {
		query += fmt.Sprintf(` AND artist:(%s)`, luceneEscape(artist))
	}
	return m.searchAlbums(ctx, query, 10)
}

func (m *MusicBrainz) searchAlbums(ctx context.Context, query string, limit int) ([]Album, error) {
	var res struct {
		Groups []mbReleaseGroup `json:"release-groups"`
	}
	if err := m.get(ctx, "/release-group", url.Values{"query": {query}, "limit": {strconv.Itoa(limit)}}, 0, &res); err != nil {
		return nil, err
	}
	out := []Album{}
	for _, g := range res.Groups {
		if g.Score < 50 {
			continue
		}
		out = append(out, g.album())
	}
	return out, nil
}

// ---- Lookups ----

// Artist returns an artist with every album, EP and single, oldest first.
func (m *MusicBrainz) Artist(ctx context.Context, id string) (*Artist, error) {
	if !ValidMBID(id) {
		return nil, ErrNoSuchMusic
	}
	var a mbArtist
	if err := m.get(ctx, "/artist/"+id, url.Values{"inc": {"url-rels genres"}}, 20*time.Hour, &a); err != nil {
		return nil, err
	}
	out := a.artist()
	for offset := 0; offset < 1000 && id != VariousArtists; {
		var page struct {
			Groups []mbReleaseGroup `json:"release-groups"`
			Count  int              `json:"release-group-count"`
		}
		q := url.Values{"artist": {id}, "limit": {"100"}, "offset": {strconv.Itoa(offset)}}
		if err := m.get(ctx, "/release-group", q, 20*time.Hour, &page); err != nil {
			return nil, err
		}
		for _, g := range page.Groups {
			al := g.album()
			al.ArtistID, al.Artist = out.ID, out.Name
			out.Albums = append(out.Albums, al)
		}
		offset += len(page.Groups)
		if len(page.Groups) == 0 || offset >= page.Count {
			break
		}
	}
	sort.SliceStable(out.Albums, func(i, j int) bool {
		a, b := out.Albums[i].ReleaseDate, out.Albums[j].ReleaseDate
		if (a == "") != (b == "") {
			return b == "" // undated last
		}
		return a < b
	})
	return &out, nil
}

// Album returns one release group.
func (m *MusicBrainz) Album(ctx context.Context, id string) (*Album, error) {
	if !ValidMBID(id) {
		return nil, ErrNoSuchMusic
	}
	var g mbReleaseGroup
	if err := m.get(ctx, "/release-group/"+id, url.Values{"inc": {"artist-credits"}}, 20*time.Hour, &g); err != nil {
		return nil, err
	}
	a := g.album()
	return &a, nil
}

type mbRelease struct {
	ID      string     `json:"id"`
	Title   string     `json:"title"`
	Status  string     `json:"status"`
	Date    string     `json:"date"`
	Country string     `json:"country"`
	Credits []mbCredit `json:"artist-credit"`
	Group   *struct {
		ID string `json:"id"`
	} `json:"release-group"`
	Media []struct {
		Position   int    `json:"position"`
		Format     string `json:"format"`
		TrackCount int    `json:"track-count"`
		Tracks     []struct {
			Position int        `json:"position"`
			Title    string     `json:"title"`
			Length   int        `json:"length"`
			Credits  []mbCredit `json:"artist-credit"`
			Rec      struct {
				ID     string `json:"id"`
				Length int    `json:"length"`
			} `json:"recording"`
		} `json:"tracks"`
	} `json:"media"`
}

// BestRelease picks the edition of an album whose track list Bragi uses:
// an official release with the track count most editions share, on CD or
// digital, preferring worldwide, US and UK editions and then the earliest.
func (m *MusicBrainz) BestRelease(ctx context.Context, albumID string) (*Release, error) {
	if !ValidMBID(albumID) {
		return nil, ErrNoSuchMusic
	}
	var res struct {
		Releases []mbRelease `json:"releases"`
	}
	if err := m.get(ctx, "/release", url.Values{"release-group": {albumID}, "inc": {"media"}, "limit": {"100"}}, 20*time.Hour, &res); err != nil {
		return nil, err
	}
	if len(res.Releases) == 0 {
		return nil, ErrNoSuchMusic
	}
	count := func(r mbRelease) int {
		n := 0
		for _, md := range r.Media {
			n += md.TrackCount
		}
		return n
	}
	common := map[int]int{}
	for _, r := range res.Releases {
		if r.Status == "Official" || r.Status == "" {
			common[count(r)]++
		}
	}
	mode, best := 0, 0
	for n, c := range common {
		if c > best || c == best && n < mode {
			mode, best = n, c
		}
	}
	score := func(r mbRelease) int {
		s := 0
		if r.Status == "Official" {
			s += 100
		}
		if count(r) == mode {
			s += 40
		}
		good := len(r.Media) > 0
		for _, md := range r.Media {
			if md.Format != "CD" && md.Format != "Digital Media" && md.Format != "" {
				good = false
			}
		}
		if good {
			s += 20
		}
		switch r.Country {
		case "XW":
			s += 12
		case "US", "GB", "XE":
			s += 10
		}
		if r.Date != "" {
			s += 5
		}
		return s
	}
	sort.SliceStable(res.Releases, func(i, j int) bool {
		a, b := res.Releases[i], res.Releases[j]
		if sa, sb := score(a), score(b); sa != sb {
			return sa > sb
		}
		return a.Date != "" && (b.Date == "" || a.Date < b.Date)
	})
	return m.Release(ctx, res.Releases[0].ID)
}

// Release returns one edition's track list.
func (m *MusicBrainz) Release(ctx context.Context, id string) (*Release, error) {
	if !ValidMBID(id) {
		return nil, ErrNoSuchMusic
	}
	var r mbRelease
	if err := m.get(ctx, "/release/"+id, url.Values{"inc": {"recordings artist-credits release-groups"}}, 30*24*time.Hour, &r); err != nil {
		return nil, err
	}
	out := &Release{ID: r.ID, Title: r.Title, Date: r.Date}
	if r.Group != nil {
		out.AlbumID = r.Group.ID
	}
	album := creditName(r.Credits)
	for i, md := range r.Media {
		disc := md.Position
		if disc == 0 {
			disc = i + 1
		}
		if len(md.Tracks) > 0 {
			out.Discs++
		}
		for _, t := range md.Tracks {
			length := t.Length
			if length == 0 {
				length = t.Rec.Length
			}
			tr := Track{RecordingID: t.Rec.ID, Disc: disc, Number: t.Position, Title: t.Title, Seconds: (length + 500) / 1000}
			if a := creditName(t.Credits); a != "" && a != album {
				tr.Artist = a
			}
			out.Tracks = append(out.Tracks, tr)
		}
	}
	return out, nil
}

// ---- Trending ----

// TrendingAlbums returns the albums most listened to on ListenBrainz this
// week. ListenBrainz needs no key either.
func (m *MusicBrainz) TrendingAlbums(ctx context.Context, n int) ([]Album, error) {
	u := ListenBrainzURL + "/1/stats/sitewide/release-groups?range=week&count=" + strconv.Itoa(n*2)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", UserAgent)
	hc := m.HC
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach ListenBrainz: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ListenBrainz answered HTTP %d", resp.StatusCode)
	}
	var res struct {
		Payload struct {
			Groups []struct {
				Artist  string   `json:"artist_name"`
				Artists []string `json:"artist_mbids"`
				ID      string   `json:"release_group_mbid"`
				Title   string   `json:"release_group_name"`
			} `json:"release_groups"`
		} `json:"payload"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&res); err != nil {
		return nil, fmt.Errorf("ListenBrainz sent something unexpected: %w", err)
	}
	out := []Album{}
	for _, g := range res.Payload.Groups {
		if g.ID == "" || len(g.Artists) == 0 || len(out) >= n {
			continue
		}
		out = append(out, Album{ID: g.ID, Title: g.Title, Artist: g.Artist, ArtistID: g.Artists[0], Type: "Album"})
	}
	return out, nil
}
