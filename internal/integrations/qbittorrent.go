package integrations

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
)

// qbittorrent talks to qBittorrent's Web API v2.
type qbittorrent struct {
	cfg Config
	hc  *http.Client
}

// session logs in and returns a client that carries the SID cookie.
func (q *qbittorrent) session(ctx context.Context) (*http.Client, error) {
	jar, _ := cookiejar.New(nil)
	hc := *q.hc
	hc.Jar = jar

	form := url.Values{"username": {q.cfg.Username}, "password": {q.cfg.Password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, q.cfg.BaseURL+"/api/v2/auth/login", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// qBittorrent's CSRF check compares Referer/Origin with its own host.
	req.Header.Set("Referer", q.cfg.BaseURL)
	resp, err := do(&hc, req)
	if err != nil {
		return nil, err
	}
	body, err := readBody(resp)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("qBittorrent has banned this IP after too many failed logins; wait or restart qBittorrent")
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("qBittorrent answered HTTP %d", resp.StatusCode)
	case strings.TrimSpace(string(body)) == "Fails.":
		return nil, ErrUnauthorized
	}
	return &hc, nil
}

func (q *qbittorrent) Test(ctx context.Context) error {
	hc, err := q.session(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, q.cfg.BaseURL+"/api/v2/app/version", nil)
	if err != nil {
		return err
	}
	resp, err := do(hc, req)
	if err != nil {
		return err
	}
	body, err := readBody(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusForbidden {
		return ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(strings.TrimSpace(string(body)), "v") {
		return fmt.Errorf("the response does not look like qBittorrent: check the URL")
	}
	return nil
}

func (q *qbittorrent) Add(ctx context.Context, r Release) (string, error) {
	return "", ErrNotImplemented
}

func (q *qbittorrent) Status(ctx context.Context, jobIDs []string) ([]Job, error) {
	return nil, ErrNotImplemented
}

func (q *qbittorrent) Remove(ctx context.Context, jobID string, deleteFiles bool) error {
	return ErrNotImplemented
}
