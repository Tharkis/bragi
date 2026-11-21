package integrations

import (
	"context"
	"crypto/sha1"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// torrentSource is what Bragi hands to a torrent client: a magnet link or
// the .torrent file's bytes, plus the info hash that names the job.
type torrentSource struct {
	Magnet string
	File   []byte
	Hash   string
}

// resolveTorrent turns a release's download link into a magnet or a
// .torrent file. Indexers (Prowlarr and Jackett included) often answer a
// download link with a redirect to a magnet, so redirects are read by hand.
func resolveTorrent(ctx context.Context, hc *http.Client, r Release) (*torrentSource, error) {
	link := r.DownloadURL
	for range 5 {
		if strings.HasPrefix(link, "magnet:") {
			h, err := magnetHash(link)
			if err != nil {
				return nil, err
			}
			return &torrentSource{Magnet: link, Hash: h}, nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
		if err != nil {
			return nil, err
		}
		nc := *hc
		nc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := do(&nc, req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			resp.Body.Close()
			loc, err := req.URL.Parse(resp.Header.Get("Location"))
			if err != nil {
				return nil, fmt.Errorf("bad redirect from %s", req.URL.Host)
			}
			link = loc.String()
			continue
		}
		body, err := readBody(resp)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s answered HTTP %d", req.URL.Host, resp.StatusCode)
		}
		h, err := infoHash(body)
		if err != nil {
			return nil, err
		}
		return &torrentSource{File: body, Hash: h}, nil
	}
	return nil, errors.New("too many redirects fetching the torrent")
}

// magnetHash reads the v1 info hash from a magnet link, as lower-case hex.
func magnetHash(magnet string) (string, error) {
	u, err := url.Parse(magnet)
	if err != nil {
		return "", fmt.Errorf("invalid magnet link")
	}
	for _, xt := range u.Query()["xt"] {
		h, ok := strings.CutPrefix(strings.ToLower(xt), "urn:btih:")
		if !ok {
			continue
		}
		switch len(h) {
		case 40:
			return h, nil
		case 32:
			b, err := base32.StdEncoding.DecodeString(strings.ToUpper(h))
			if err != nil {
				return "", fmt.Errorf("invalid magnet hash")
			}
			return hex.EncodeToString(b), nil
		}
	}
	return "", fmt.Errorf("the magnet link has no info hash")
}

// infoHash returns the SHA-1 of a .torrent file's bencoded info dictionary.
func infoHash(torrent []byte) (string, error) {
	if len(torrent) == 0 || torrent[0] != 'd' {
		return "", errors.New("the indexer did not return a .torrent file")
	}
	pos := 1
	for pos < len(torrent) && torrent[pos] != 'e' {
		key, next, err := bstring(torrent, pos)
		if err != nil {
			return "", err
		}
		end, err := bskip(torrent, next)
		if err != nil {
			return "", err
		}
		if key == "info" {
			sum := sha1.Sum(torrent[next:end])
			return hex.EncodeToString(sum[:]), nil
		}
		pos = end
	}
	return "", errors.New("the .torrent file has no info section")
}

var errBencode = errors.New("the .torrent file is damaged")

// bstring reads a bencoded string at pos and returns it and the next offset.
func bstring(b []byte, pos int) (string, int, error) {
	colon := pos
	for colon < len(b) && b[colon] != ':' {
		colon++
	}
	if colon >= len(b) {
		return "", 0, errBencode
	}
	n, err := strconv.Atoi(string(b[pos:colon]))
	if err != nil || n < 0 || colon+1+n > len(b) {
		return "", 0, errBencode
	}
	return string(b[colon+1 : colon+1+n]), colon + 1 + n, nil
}

// bskip returns the offset just past the bencoded value at pos.
func bskip(b []byte, pos int) (int, error) {
	if pos >= len(b) {
		return 0, errBencode
	}
	switch c := b[pos]; {
	case c == 'i':
		for i := pos + 1; i < len(b); i++ {
			if b[i] == 'e' {
				return i + 1, nil
			}
		}
		return 0, errBencode
	case c == 'l' || c == 'd':
		pos++
		for pos < len(b) && b[pos] != 'e' {
			next, err := bskip(b, pos)
			if err != nil {
				return 0, err
			}
			pos = next
		}
		if pos >= len(b) {
			return 0, errBencode
		}
		return pos + 1, nil
	case c >= '0' && c <= '9':
		_, next, err := bstring(b, pos)
		return next, err
	}
	return 0, errBencode
}
