package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// sabnzbd talks to SABnzbd's HTTP API (/api?mode=...&output=json).
type sabnzbd struct {
	cfg Config
	hc  *http.Client
}

// sabError is the shape of an API failure: {"status": false, "error": "..."}.
type sabError struct {
	Status *bool  `json:"status"`
	Error  string `json:"error"`
}

func (s *sabnzbd) url(mode string, extra url.Values) string {
	v := url.Values{"mode": {mode}, "output": {"json"}, "apikey": {s.cfg.APIKey}}
	for k, vs := range extra {
		v[k] = vs
	}
	return s.cfg.BaseURL + "/api?" + v.Encode()
}

// send runs req and decodes the JSON answer into out.
func (s *sabnzbd) send(req *http.Request, out any) error {
	resp, err := do(s.hc, req)
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
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("SABnzbd answered HTTP %d", resp.StatusCode)
	}
	var e sabError
	if err := json.Unmarshal(body, &e); err != nil {
		// SABnzbd answers plain text for some errors, e.g. a missing key.
		text := strings.TrimSpace(string(body))
		if strings.Contains(strings.ToLower(text), "api key") {
			return ErrUnauthorized
		}
		return fmt.Errorf("the response does not look like SABnzbd: check the URL")
	}
	if e.Error != "" {
		if strings.Contains(strings.ToLower(e.Error), "api key") {
			return ErrUnauthorized
		}
		return fmt.Errorf("SABnzbd error: %s", e.Error)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func (s *sabnzbd) get(ctx context.Context, mode string, extra url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url(mode, extra), nil)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	return s.send(req, out)
}

func (s *sabnzbd) Test(ctx context.Context) error {
	var v struct {
		Version string `json:"version"`
	}
	if err := s.get(ctx, "version", nil, &v); err != nil {
		return err
	}
	if v.Version == "" {
		return fmt.Errorf("the response does not look like SABnzbd: check the URL")
	}
	// The version call needs no key, so check the key with a real call.
	var q sabQueue
	return s.get(ctx, "queue", url.Values{"limit": {"1"}}, &q)
}

func (s *sabnzbd) Add(ctx context.Context, r Release) (string, error) {
	// Bragi fetches the NZB itself, so SABnzbd never needs to reach the indexer.
	nzb, err := fetch(ctx, s.hc, r.DownloadURL)
	if err != nil {
		return "", fmt.Errorf("could not fetch the NZB: %w", err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("name", r.Title+".nzb")
	fw.Write(nzb)
	mw.WriteField("nzbname", r.Title)
	if s.cfg.Category != "" {
		mw.WriteField("cat", s.cfg.Category)
	}
	mw.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url("addfile", nil), &body)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	var out struct {
		Status bool     `json:"status"`
		IDs    []string `json:"nzo_ids"`
	}
	if err := s.send(req, &out); err != nil {
		return "", err
	}
	if !out.Status || len(out.IDs) == 0 {
		return "", fmt.Errorf("SABnzbd refused the NZB")
	}
	return out.IDs[0], nil
}

type sabQueue struct {
	Queue struct {
		Slots []struct {
			ID         string `json:"nzo_id"`
			Status     string `json:"status"`
			MB         string `json:"mb"`
			MBLeft     string `json:"mbleft"`
			Percentage string `json:"percentage"`
		} `json:"slots"`
	} `json:"queue"`
}

func (s *sabnzbd) Status(ctx context.Context, jobIDs []string) ([]Job, error) {
	ids := url.Values{"nzo_ids": {strings.Join(jobIDs, ",")}, "limit": {strconv.Itoa(len(jobIDs) + 10)}}
	var q sabQueue
	if err := s.get(ctx, "queue", ids, &q); err != nil {
		return nil, err
	}
	var h struct {
		History struct {
			Slots []struct {
				ID          string `json:"nzo_id"`
				Status      string `json:"status"`
				Storage     string `json:"storage"`
				FailMessage string `json:"fail_message"`
			} `json:"slots"`
		} `json:"history"`
	}
	if err := s.get(ctx, "history", ids, &h); err != nil {
		return nil, err
	}
	found := map[string]Job{}
	for _, sl := range q.Queue.Slots {
		j := Job{ID: sl.ID, State: JobDownloading}
		mb, _ := strconv.ParseFloat(sl.MB, 64)
		left, _ := strconv.ParseFloat(sl.MBLeft, 64)
		if mb > 0 {
			j.Progress = (mb - left) / mb
		} else if p, err := strconv.ParseFloat(sl.Percentage, 64); err == nil {
			j.Progress = p / 100
		}
		switch sl.Status {
		case "Queued", "Paused", "Grabbing", "Fetching", "Propagating":
			j.State = JobQueued
		}
		found[sl.ID] = j
	}
	for _, sl := range h.History.Slots {
		if _, ok := found[sl.ID]; ok {
			continue
		}
		j := Job{ID: sl.ID, Progress: 1, OutputPath: sl.Storage}
		switch sl.Status {
		case "Completed":
			j.State = JobCompleted
		case "Failed":
			j.State = JobFailed
			j.Error = "SABnzbd reports failed"
			if sl.FailMessage != "" {
				j.Error += ": " + sl.FailMessage
			}
		default:
			// Verifying, Repairing, Extracting, Moving, Running: still busy.
			j.State = JobDownloading
		}
		found[sl.ID] = j
	}
	out := make([]Job, 0, len(jobIDs))
	for _, id := range jobIDs {
		j, ok := found[id]
		if !ok {
			j = Job{ID: id, State: JobMissing}
		}
		out = append(out, j)
	}
	return out, nil
}

func (s *sabnzbd) Remove(ctx context.Context, jobID string, deleteFiles bool) error {
	v := url.Values{"name": {"delete"}, "value": {jobID}}
	if deleteFiles {
		v.Set("del_files", "1")
	}
	var out struct {
		Status bool `json:"status"`
	}
	if err := s.get(ctx, "queue", v, &out); err == nil && out.Status {
		return nil
	}
	return s.get(ctx, "history", v, &out)
}
