package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path"
	"strconv"
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

// post sends a form or multipart body on a logged-in session.
func (q *qbittorrent) post(ctx context.Context, hc *http.Client, path, contentType string, body io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, q.cfg.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Referer", q.cfg.BaseURL)
	resp, err := do(hc, req)
	if err != nil {
		return err
	}
	b, _ := readBody(resp)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("qBittorrent answered HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if strings.TrimSpace(string(b)) == "Fails." {
		return releaseErr(fmt.Errorf("qBittorrent refused the torrent (it may already have it, or the file is invalid)"))
	}
	return nil
}

func (q *qbittorrent) Add(ctx context.Context, r Release) (string, error) {
	src, err := resolveTorrent(ctx, q.hc, r)
	if err != nil {
		return "", releaseErr(fmt.Errorf("could not fetch the torrent: %w", err))
	}
	hc, err := q.session(ctx)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if src.Magnet != "" {
		mw.WriteField("urls", src.Magnet)
	} else {
		fw, _ := mw.CreateFormFile("torrents", "bragi.torrent")
		fw.Write(src.File)
	}
	if q.cfg.Category != "" {
		mw.WriteField("category", q.cfg.Category)
	}
	mw.WriteField("tags", "bragi")
	mw.Close()
	if err := q.post(ctx, hc, "/api/v2/torrents/add", mw.FormDataContentType(), &buf); err != nil {
		return "", err
	}
	return src.Hash, nil
}

func (q *qbittorrent) Status(ctx context.Context, jobIDs []string) ([]Job, error) {
	hc, err := q.session(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		q.cfg.BaseURL+"/api/v2/torrents/info?"+url.Values{"hashes": {strings.Join(jobIDs, "|")}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := do(hc, req)
	if err != nil {
		return nil, err
	}
	body, err := readBody(resp)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("qBittorrent answered HTTP %d", resp.StatusCode)
	}
	var torrents []struct {
		Hash        string  `json:"hash"`
		State       string  `json:"state"`
		Progress    float64 `json:"progress"`
		ContentPath string  `json:"content_path"`
		SavePath    string  `json:"save_path"`
		Name        string  `json:"name"`
	}
	if err := json.Unmarshal(body, &torrents); err != nil {
		return nil, fmt.Errorf("could not read qBittorrent's torrent list")
	}
	found := map[string]Job{}
	for _, t := range torrents {
		j := Job{ID: strings.ToLower(t.Hash), Progress: t.Progress, OutputPath: t.ContentPath}
		if j.OutputPath == "" {
			j.OutputPath = path.Join(t.SavePath, t.Name)
		}
		switch t.State {
		case "error", "missingFiles":
			j.State, j.Error = JobFailed, "qBittorrent reports "+t.State
		case "uploading", "stalledUP", "pausedUP", "stoppedUP", "queuedUP", "forcedUP", "checkingUP":
			j.State = JobCompleted
		case "queuedDL", "pausedDL", "stoppedDL", "metaDL", "forcedMetaDL", "allocating", "checkingResumeData":
			j.State = JobQueued
		default:
			j.State = JobDownloading
		}
		if j.State != JobCompleted && t.Progress >= 1 && t.State != "error" && t.State != "missingFiles" {
			j.State = JobCompleted
		}
		found[j.ID] = j
	}
	out := make([]Job, 0, len(jobIDs))
	for _, id := range jobIDs {
		j, ok := found[strings.ToLower(id)]
		if !ok {
			j = Job{ID: id, State: JobMissing}
		}
		j.ID = id
		out = append(out, j)
	}
	return out, nil
}

func (q *qbittorrent) Remove(ctx context.Context, jobID string, deleteFiles bool) error {
	hc, err := q.session(ctx)
	if err != nil {
		return err
	}
	form := url.Values{"hashes": {jobID}, "deleteFiles": {strconv.FormatBool(deleteFiles)}}
	return q.post(ctx, hc, "/api/v2/torrents/delete", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
}
