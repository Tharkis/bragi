package integrations

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
)

// transmission talks to Transmission's RPC API (Transmission 2.80 or newer).
//
// Transmission has no categories. Like Sonarr, Bragi puts each download in
// a subfolder of Transmission's download folder named after the category,
// and on Transmission 4 also gives it that label.
type transmission struct {
	cfg Config
	hc  *http.Client

	mu      sync.Mutex
	session string // X-Transmission-Session-Id
}

// transmissionMinRPC is RPC version 15, Transmission 2.80, the first whose
// torrent-get reports everything Bragi reads.
const transmissionMinRPC = 15

// transmissionLabelsRPC is RPC version 17, Transmission 4.0, the first
// whose torrent-add takes labels.
const transmissionLabelsRPC = 17

// rpcURL is the RPC endpoint. A bare host gets Transmission's default path.
func (t *transmission) rpcURL() string {
	u, err := url.Parse(t.cfg.BaseURL)
	if err != nil || strings.HasSuffix(u.Path, "/rpc") {
		return t.cfg.BaseURL
	}
	p := strings.TrimSuffix(u.Path, "/web")
	if !strings.HasSuffix(p, "/transmission") {
		p += "/transmission"
	}
	u.Path = p + "/rpc"
	return u.String()
}

// call runs one RPC method. Transmission answers the first request of a
// session with 409 and a session id to send back, which call handles.
func (t *transmission) call(ctx context.Context, method string, args, out any) error {
	payload, _ := json.Marshal(map[string]any{"method": method, "arguments": args})
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.rpcURL(), bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("invalid URL: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if t.cfg.Username != "" || t.cfg.Password != "" {
			req.SetBasicAuth(t.cfg.Username, t.cfg.Password)
		}
		t.mu.Lock()
		if t.session != "" {
			req.Header.Set("X-Transmission-Session-Id", t.session)
		}
		t.mu.Unlock()
		resp, err := do(t.hc, req)
		if err != nil {
			return err
		}
		if resp.StatusCode == http.StatusConflict && attempt == 0 {
			resp.Body.Close()
			id := resp.Header.Get("X-Transmission-Session-Id")
			if id == "" {
				return fmt.Errorf("the response does not look like Transmission: check the URL")
			}
			t.mu.Lock()
			t.session = id
			t.mu.Unlock()
			continue
		}
		body, err := readBody(resp)
		if err != nil {
			return err
		}
		switch resp.StatusCode {
		case http.StatusOK:
		case http.StatusForbidden:
			return fmt.Errorf("Transmission refused this address: add Bragi's IP to rpc-whitelist in Transmission's settings.json, or turn the whitelist off")
		default:
			return fmt.Errorf("Transmission answered HTTP %d", resp.StatusCode)
		}
		var rpc struct {
			Result    string          `json:"result"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(body, &rpc); err != nil || rpc.Result == "" {
			return fmt.Errorf("the response does not look like Transmission: check the URL")
		}
		if rpc.Result != "success" {
			return fmt.Errorf("Transmission error: %s", rpc.Result)
		}
		if out == nil {
			return nil
		}
		return json.Unmarshal(rpc.Arguments, out)
	}
}

type transmissionSession struct {
	Version     string `json:"version"`
	RPCVersion  int    `json:"rpc-version"`
	DownloadDir string `json:"download-dir"`
}

func (t *transmission) sessionInfo(ctx context.Context) (*transmissionSession, error) {
	var s transmissionSession
	if err := t.call(ctx, "session-get", map[string]any{"fields": []string{"version", "rpc-version", "download-dir"}}, &s); err != nil {
		return nil, err
	}
	if s.RPCVersion == 0 {
		return nil, fmt.Errorf("the response does not look like Transmission: check the URL")
	}
	return &s, nil
}

func (t *transmission) Test(ctx context.Context) error {
	_, err := t.Check(ctx)
	return err
}

// Check tests the connection and the version.
func (t *transmission) Check(ctx context.Context) ([]string, error) {
	s, err := t.sessionInfo(ctx)
	if err != nil {
		return nil, err
	}
	if s.RPCVersion < transmissionMinRPC {
		return nil, fmt.Errorf("Transmission %s is too old: Bragi needs 2.80 or newer", s.Version)
	}
	var warnings []string
	if s.DownloadDir == "" {
		warnings = append(warnings, "Transmission did not say where it saves downloads, so Bragi cannot put them in category folders.")
	}
	return warnings, nil
}

func (t *transmission) Add(ctx context.Context, r Release) (string, error) {
	src, err := resolveTorrent(ctx, t.hc, r)
	if err != nil {
		return "", fmt.Errorf("could not fetch the torrent: %w", err)
	}
	s, err := t.sessionInfo(ctx)
	if err != nil {
		return "", err
	}
	args := map[string]any{}
	if src.Magnet != "" {
		args["filename"] = src.Magnet
	} else {
		args["metainfo"] = base64.StdEncoding.EncodeToString(src.File)
	}
	if cat := strings.Trim(strings.TrimSpace(t.cfg.Category), "/"); cat != "" {
		if s.DownloadDir != "" {
			args["download-dir"] = joinRemote(s.DownloadDir, cat)
		}
		if s.RPCVersion >= transmissionLabelsRPC {
			args["labels"] = []string{cat}
		}
	} else if s.RPCVersion >= transmissionLabelsRPC {
		args["labels"] = []string{"bragi"}
	}
	var added map[string]struct {
		HashString string `json:"hashString"`
	}
	if err := t.call(ctx, "torrent-add", args, &added); err != nil {
		return "", err
	}
	// "torrent-added", or "torrent-duplicate" when Transmission has it.
	for _, a := range added {
		if a.HashString != "" {
			return strings.ToLower(a.HashString), nil
		}
	}
	return src.Hash, nil
}

// joinRemote joins a folder on the client's side, keeping Windows paths.
func joinRemote(dir, name string) string {
	if strings.Contains(dir, `\`) {
		return strings.TrimRight(dir, `\`) + `\` + name
	}
	return path.Join(dir, name)
}

// Transmission's torrent status values.
const (
	trStopped = iota
	trCheckWait
	trCheck
	trDownloadWait
	trDownload
	trSeedWait
	trSeed
)

// trLocalError is Transmission's error value for a local problem, such as
// a full disk or a missing folder. Values 1 and 2 are tracker trouble,
// which Transmission retries on its own.
const trLocalError = 3

type transmissionTorrent struct {
	HashString      string  `json:"hashString"`
	Name            string  `json:"name"`
	Status          int     `json:"status"`
	PercentDone     float64 `json:"percentDone"`
	LeftUntilDone   int64   `json:"leftUntilDone"`
	SizeWhenDone    int64   `json:"sizeWhenDone"`
	DownloadDir     string  `json:"downloadDir"`
	Error           int     `json:"error"`
	ErrorString     string  `json:"errorString"`
	MetadataPercent float64 `json:"metadataPercentComplete"`
}

var transmissionFields = []string{"hashString", "name", "status", "percentDone", "leftUntilDone", "sizeWhenDone",
	"downloadDir", "error", "errorString", "metadataPercentComplete"}

// job maps one torrent onto Bragi's job states.
func (tt transmissionTorrent) job() Job {
	j := Job{ID: strings.ToLower(tt.HashString), Progress: tt.PercentDone, OutputPath: joinRemote(tt.DownloadDir, tt.Name)}
	done := tt.SizeWhenDone > 0 && tt.LeftUntilDone == 0 && tt.MetadataPercent >= 1
	switch {
	case tt.Error == trLocalError:
		j.State, j.Error = JobFailed, "Transmission reports: "+tt.ErrorString
	case tt.Status == trCheckWait || tt.Status == trCheck:
		j.State, j.Stage = JobDownloading, "Checking files"
	case done:
		j.State, j.Progress = JobCompleted, 1
	case tt.MetadataPercent < 1:
		j.State, j.Stage = JobQueued, "Fetching metadata"
	case tt.Status == trStopped:
		j.State, j.Stage = JobQueued, "Paused in Transmission"
	case tt.Status == trDownloadWait:
		j.State = JobQueued
	default:
		j.State = JobDownloading
	}
	return j
}

func (t *transmission) Status(ctx context.Context, jobIDs []string) ([]Job, error) {
	var got struct {
		Torrents []transmissionTorrent `json:"torrents"`
	}
	if err := t.call(ctx, "torrent-get", map[string]any{"ids": jobIDs, "fields": transmissionFields}, &got); err != nil {
		return nil, err
	}
	found := map[string]Job{}
	for _, tt := range got.Torrents {
		j := tt.job()
		found[j.ID] = j
	}
	out := make([]Job, 0, len(jobIDs))
	for _, id := range jobIDs {
		j, ok := found[strings.ToLower(id)]
		if !ok {
			j = Job{State: JobMissing}
		}
		j.ID = id
		out = append(out, j)
	}
	return out, nil
}

func (t *transmission) Remove(ctx context.Context, jobID string, deleteFiles bool) error {
	return t.call(ctx, "torrent-remove", map[string]any{"ids": []string{jobID}, "delete-local-data": deleteFiles}, nil)
}
