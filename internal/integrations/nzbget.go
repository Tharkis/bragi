package integrations

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// nzbget talks to NZBGet's JSON-RPC API.
type nzbget struct {
	cfg Config
	hc  *http.Client
}

func (n *nzbget) call(ctx context.Context, method string, params []any, out any) error {
	if params == nil {
		params = []any{}
	}
	payload, _ := json.Marshal(map[string]any{"method": method, "params": params, "id": 1})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.cfg.BaseURL+"/jsonrpc", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if n.cfg.Username != "" || n.cfg.Password != "" {
		req.SetBasicAuth(n.cfg.Username, n.cfg.Password)
	}
	resp, err := do(n.hc, req)
	if err != nil {
		return err
	}
	body, err := readBody(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("NZBGet answered HTTP %d", resp.StatusCode)
	}
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &rpc); err != nil {
		return fmt.Errorf("the response does not look like NZBGet: check the URL")
	}
	if rpc.Error != nil {
		return fmt.Errorf("NZBGet error: %s", rpc.Error.Message)
	}
	return json.Unmarshal(rpc.Result, out)
}

func (n *nzbget) Test(ctx context.Context) error {
	var version string
	if err := n.call(ctx, "version", nil, &version); err != nil {
		return err
	}
	if version == "" {
		return fmt.Errorf("NZBGet did not report a version")
	}
	return nil
}

func (n *nzbget) Add(ctx context.Context, r Release) (string, error) {
	// Bragi fetches the NZB itself, so NZBGet never needs to reach the indexer.
	nzb, err := fetch(ctx, n.hc, r.DownloadURL)
	if err != nil {
		return "", fmt.Errorf("could not fetch the NZB: %w", err)
	}
	name := r.Title + ".nzb"
	params := []any{name, base64.StdEncoding.EncodeToString(nzb), n.cfg.Category, 0, false, false, "", 0, "SCORE", []any{}}
	var id int64
	if err := n.call(ctx, "append", params, &id); err != nil {
		return "", err
	}
	if id <= 0 {
		return "", fmt.Errorf("NZBGet refused the NZB")
	}
	return strconv.FormatInt(id, 10), nil
}

func (n *nzbget) Status(ctx context.Context, jobIDs []string) ([]Job, error) {
	var queue []struct {
		NZBID           int64  `json:"NZBID"`
		Status          string `json:"Status"`
		FileSizeMB      int64  `json:"FileSizeMB"`
		RemainingSizeMB int64  `json:"RemainingSizeMB"`
		DestDir         string `json:"DestDir"`
	}
	if err := n.call(ctx, "listgroups", []any{0}, &queue); err != nil {
		return nil, err
	}
	var history []struct {
		NZBID    int64  `json:"NZBID"`
		Status   string `json:"Status"`
		DestDir  string `json:"DestDir"`
		FinalDir string `json:"FinalDir"`
	}
	if err := n.call(ctx, "history", []any{false}, &history); err != nil {
		return nil, err
	}
	found := map[string]Job{}
	for _, q := range queue {
		id := strconv.FormatInt(q.NZBID, 10)
		j := Job{ID: id, State: JobDownloading, OutputPath: q.DestDir}
		if q.FileSizeMB > 0 {
			j.Progress = float64(q.FileSizeMB-q.RemainingSizeMB) / float64(q.FileSizeMB)
		}
		if q.Status == "QUEUED" || q.Status == "PAUSED" {
			j.State = JobQueued
		}
		found[id] = j
	}
	for _, h := range history {
		id := strconv.FormatInt(h.NZBID, 10)
		if _, ok := found[id]; ok {
			continue
		}
		j := Job{ID: id, Progress: 1, OutputPath: h.DestDir}
		if h.FinalDir != "" {
			j.OutputPath = h.FinalDir
		}
		kind, detail, _ := strings.Cut(h.Status, "/")
		switch kind {
		case "SUCCESS", "WARNING":
			j.State = JobCompleted
		case "DELETED":
			j.State = JobFailed
			j.Error = "Removed in NZBGet"
		default:
			j.State = JobFailed
			j.Error = "NZBGet reports " + strings.ToLower(kind+" "+detail)
		}
		found[id] = j
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

func (n *nzbget) Remove(ctx context.Context, jobID string, deleteFiles bool) error {
	id, err := strconv.ParseInt(jobID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid NZBGet id %q", jobID)
	}
	queueCmd, historyCmd := "GroupDelete", "HistoryDelete"
	if deleteFiles {
		queueCmd, historyCmd = "GroupFinalDelete", "HistoryFinalDelete"
	}
	var ok bool
	if err := n.call(ctx, "editqueue", []any{queueCmd, "", []int64{id}}, &ok); err == nil && ok {
		return nil
	}
	return n.call(ctx, "editqueue", []any{historyCmd, "", []int64{id}}, &ok)
}
