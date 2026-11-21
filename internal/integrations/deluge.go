package integrations

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
)

// deluge talks to Deluge's web UI JSON-RPC API (Deluge 1.3 and 2.x).
//
// Categories become Deluge labels, which need the Label plugin. Without it
// downloads still work, unlabeled, and Test says so.
type deluge struct {
	cfg Config
	hc  *http.Client
	id  atomic.Int64
}

// errDelugeAuth is Deluge's answer to a call without a valid session.
var errDelugeAuth = errors.New("not authenticated")

// delugeLabel is what Deluge accepts as a label name.
var delugeLabel = regexp.MustCompile(`^[a-z0-9_.\-]+$`)

type delugeError struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

// call runs one method on a session from login.
func (d *deluge) call(ctx context.Context, hc *http.Client, method string, params []any, out any) error {
	if params == nil {
		params = []any{}
	}
	payload, _ := json.Marshal(map[string]any{"method": method, "params": params, "id": d.id.Add(1)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.cfg.BaseURL+"/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := do(hc, req)
	if err != nil {
		return err
	}
	body, err := readBody(resp)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Deluge answered HTTP %d", resp.StatusCode)
	}
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  *delugeError    `json:"error"`
		ID     *int64          `json:"id"`
	}
	if err := json.Unmarshal(body, &rpc); err != nil || rpc.ID == nil {
		return fmt.Errorf("the response does not look like Deluge's web UI: check the URL")
	}
	if rpc.Error != nil {
		if rpc.Error.Code == 1 || strings.Contains(strings.ToLower(rpc.Error.Message), "not authenticated") {
			return errDelugeAuth
		}
		return fmt.Errorf("Deluge error: %s", rpc.Error.Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(rpc.Result, out)
}

// login signs in to the web UI and makes sure it is connected to a Deluge
// daemon, connecting to the first one it knows if not.
func (d *deluge) login(ctx context.Context) (*http.Client, error) {
	jar, _ := cookiejar.New(nil)
	hc := *d.hc
	hc.Jar = jar
	var ok bool
	if err := d.call(ctx, &hc, "auth.login", []any{d.cfg.Password}, &ok); err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrUnauthorized
	}
	var connected bool
	if err := d.call(ctx, &hc, "web.connected", nil, &connected); err != nil {
		return nil, err
	}
	if connected {
		return &hc, nil
	}
	var hosts [][]any
	if err := d.call(ctx, &hc, "web.get_hosts", nil, &hosts); err != nil {
		return nil, err
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("Deluge's web UI has no daemon to connect to: add one in its Connection Manager")
	}
	hostID, _ := hosts[0][0].(string)
	if err := d.call(ctx, &hc, "web.connect", []any{hostID}, nil); err != nil {
		return nil, fmt.Errorf("Deluge's web UI could not connect to its daemon: %w", err)
	}
	if err := d.call(ctx, &hc, "web.connected", nil, &connected); err != nil {
		return nil, err
	}
	if !connected {
		return nil, fmt.Errorf("Deluge's web UI could not connect to its daemon: check the Connection Manager")
	}
	return &hc, nil
}

// labelsEnabled reports whether the Label plugin is on.
func (d *deluge) labelsEnabled(ctx context.Context, hc *http.Client) bool {
	var plugins []string
	if err := d.call(ctx, hc, "core.get_enabled_plugins", nil, &plugins); err != nil {
		return false
	}
	return slices.Contains(plugins, "Label")
}

func (d *deluge) Test(ctx context.Context) error {
	_, err := d.Check(ctx)
	return err
}

// Check tests the login and the daemon, and warns about labels Bragi
// cannot set.
func (d *deluge) Check(ctx context.Context) ([]string, error) {
	hc, err := d.login(ctx)
	if err != nil {
		return nil, err
	}
	var version string
	if err := d.call(ctx, hc, "daemon.info", nil, &version); err != nil {
		return nil, err
	}
	var warnings []string
	cats := d.categories()
	if len(cats) == 0 {
		return nil, nil
	}
	if !d.labelsEnabled(ctx, hc) {
		warnings = append(warnings, "Deluge's Label plugin is off, so Bragi cannot label its downloads. Turn it on in Deluge under Preferences, Plugins.")
		return warnings, nil
	}
	var bad []string
	for _, c := range cats {
		if !delugeLabel.MatchString(strings.ToLower(c)) {
			bad = append(bad, c)
		}
	}
	if len(bad) > 0 {
		warnings = append(warnings, fmt.Sprintf("Deluge labels may only use letters, digits, dots, dashes and underscores, so Bragi cannot use %s.", quoteList(bad)))
	}
	return warnings, nil
}

// categories is every category Bragi may send, deduplicated.
func (d *deluge) categories() []string {
	var out []string
	for _, c := range append([]string{d.cfg.Category}, d.cfg.Categories...) {
		c = strings.TrimSpace(c)
		if c != "" && !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, c) }) {
			out = append(out, c)
		}
	}
	return out
}

func (d *deluge) Add(ctx context.Context, r Release) (string, error) {
	src, err := resolveTorrent(ctx, d.hc, r)
	if err != nil {
		return "", fmt.Errorf("could not fetch the torrent: %w", err)
	}
	hc, err := d.login(ctx)
	if err != nil {
		return "", err
	}
	var hash *string
	if src.Magnet != "" {
		err = d.call(ctx, hc, "core.add_torrent_magnet", []any{src.Magnet, map[string]any{}}, &hash)
	} else {
		err = d.call(ctx, hc, "core.add_torrent_file", []any{"bragi.torrent", base64.StdEncoding.EncodeToString(src.File), map[string]any{}}, &hash)
	}
	// Deluge 2 refuses a torrent it already has; that is fine.
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "already in session") {
		return "", err
	}
	id := src.Hash
	if hash != nil && *hash != "" {
		id = strings.ToLower(*hash)
	}
	if label := strings.ToLower(strings.TrimSpace(d.cfg.Category)); label != "" && delugeLabel.MatchString(label) && d.labelsEnabled(ctx, hc) {
		if err := d.setLabel(ctx, hc, id, label); err != nil {
			return "", err
		}
	}
	return id, nil
}

// setLabel labels a torrent, creating the label first if Deluge lacks it.
func (d *deluge) setLabel(ctx context.Context, hc *http.Client, id, label string) error {
	var labels []string
	if err := d.call(ctx, hc, "label.get_labels", nil, &labels); err != nil {
		return err
	}
	if !slices.Contains(labels, label) {
		if err := d.call(ctx, hc, "label.add", []any{label}, nil); err != nil && !strings.Contains(err.Error(), "already exists") {
			return fmt.Errorf("could not create the label %q in Deluge: %w", label, err)
		}
	}
	if err := d.call(ctx, hc, "label.set_torrent", []any{id, label}, nil); err != nil {
		return fmt.Errorf("could not label the torrent in Deluge: %w", err)
	}
	return nil
}

type delugeTorrent struct {
	State        string  `json:"state"`
	Progress     float64 `json:"progress"` // 0 to 100
	SavePath     string  `json:"save_path"`
	Name         string  `json:"name"`
	Message      string  `json:"message"`
	IsFinished   bool    `json:"is_finished"`
	TotalWanted  int64   `json:"total_wanted"`
	TotalDone    int64   `json:"total_done"`
	MoveOnDone   bool    `json:"move_on_completed"`
	MoveOnPath   string  `json:"move_on_completed_path"`
	DownloadPath string  `json:"download_location"`
}

var delugeFields = []string{"state", "progress", "save_path", "name", "message", "is_finished", "total_wanted", "total_done",
	"move_on_completed", "move_on_completed_path", "download_location"}

// job maps one torrent onto Bragi's job states.
func (t delugeTorrent) job(id string) Job {
	dir := t.SavePath
	if dir == "" {
		dir = t.DownloadPath
	}
	j := Job{ID: id, Progress: t.Progress / 100, OutputPath: joinRemote(dir, t.Name)}
	done := t.IsFinished || (t.TotalWanted > 0 && t.TotalDone >= t.TotalWanted)
	switch t.State {
	case "Error":
		msg := t.Message
		if msg == "" || msg == "OK" {
			msg = "an error"
		}
		j.State, j.Error = JobFailed, "Deluge reports: "+msg
	case "Checking":
		j.State, j.Stage = JobDownloading, "Checking files"
	case "Moving":
		j.State, j.Stage = JobDownloading, "Moving files"
	case "Allocating":
		j.State, j.Stage = JobQueued, "Allocating"
	default:
		switch {
		case done:
			j.State, j.Progress = JobCompleted, 1
		case t.State == "Paused":
			j.State, j.Stage = JobQueued, "Paused in Deluge"
		case t.State == "Queued":
			j.State = JobQueued
		case t.State == "Downloading" && t.TotalWanted == 0:
			j.State, j.Stage = JobQueued, "Fetching metadata"
		default:
			j.State = JobDownloading
		}
	}
	// Deluge moves a finished torrent when "move completed" is on; until it
	// has, the files are still in the download folder.
	if j.State == JobCompleted && t.MoveOnDone && t.MoveOnPath != "" && t.SavePath != "" && cleanDir(t.SavePath) != cleanDir(t.MoveOnPath) {
		j.State, j.Stage, j.Progress = JobDownloading, "Moving files", 1
	}
	return j
}

func (d *deluge) Status(ctx context.Context, jobIDs []string) ([]Job, error) {
	hc, err := d.login(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(jobIDs))
	for i, id := range jobIDs {
		ids[i] = strings.ToLower(id)
	}
	var got map[string]delugeTorrent
	if err := d.call(ctx, hc, "core.get_torrents_status", []any{map[string]any{"id": ids}, delugeFields}, &got); err != nil {
		return nil, err
	}
	out := make([]Job, 0, len(jobIDs))
	for i, id := range jobIDs {
		t, ok := got[ids[i]]
		if !ok {
			out = append(out, Job{ID: id, State: JobMissing})
			continue
		}
		out = append(out, t.job(id))
	}
	return out, nil
}

func (d *deluge) Remove(ctx context.Context, jobID string, deleteFiles bool) error {
	hc, err := d.login(ctx)
	if err != nil {
		return err
	}
	return d.call(ctx, hc, "core.remove_torrent", []any{strings.ToLower(jobID), deleteFiles}, nil)
}
