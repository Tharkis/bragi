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

func (n *newznab) Search(ctx context.Context, q Query) ([]Release, error) {
	return nil, ErrNotImplemented
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
