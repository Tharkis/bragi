package integrations

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// nzbget talks to NZBGet's JSON-RPC API (version 16 or newer).
type nzbget struct {
	cfg Config
	hc  *http.Client
}

// nzbgetMinVersion is the first NZBGet whose append call takes the
// arguments Bragi sends.
const nzbgetMinVersion = 16

// nzbgetMaxBody caps one JSON-RPC answer. NZBGet's history call has no
// filter, and a few thousand entries run to many megabytes.
const nzbgetMaxBody = 128 << 20

// errNZBGetAccess is NZBGet's answer when the login lacks the right for a
// method, e.g. the add-only user calling listgroups.
var errNZBGetAccess = errors.New("access denied")

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
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return errNZBGetAccess
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
	if err := json.NewDecoder(io.LimitReader(resp.Body, nzbgetMaxBody)).Decode(&rpc); err != nil {
		return fmt.Errorf("the response does not look like NZBGet: check the URL")
	}
	if rpc.Error != nil {
		if strings.Contains(strings.ToLower(rpc.Error.Message), "access denied") {
			return errNZBGetAccess
		}
		return fmt.Errorf("NZBGet error: %s", rpc.Error.Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(rpc.Result, out)
}

func (n *nzbget) Test(ctx context.Context) error {
	_, err := n.Check(ctx)
	return err
}

// Check tests the connection and returns problems that do not stop Bragi
// from working but are worth fixing, such as a category NZBGet lacks.
func (n *nzbget) Check(ctx context.Context) ([]string, error) {
	var version string
	if err := n.call(ctx, "version", nil, &version); err != nil {
		if errors.Is(err, errNZBGetAccess) {
			return nil, ErrUnauthorized
		}
		return nil, err
	}
	if version == "" {
		return nil, fmt.Errorf("NZBGet did not report a version")
	}
	if major := nzbgetMajor(version); major > 0 && major < nzbgetMinVersion {
		return nil, fmt.Errorf("NZBGet %s is too old: Bragi needs version %d or newer", version, nzbgetMinVersion)
	}
	// The add-only login can add downloads but not see them, so Bragi
	// could never tell when one finished.
	var queue []json.RawMessage
	if err := n.call(ctx, "listgroups", []any{0}, &queue); err != nil {
		if errors.Is(err, errNZBGetAccess) || errors.Is(err, ErrUnauthorized) {
			return nil, fmt.Errorf("this login can add downloads but cannot see the queue: use NZBGet's control or restricted login instead (Settings, Security)")
		}
		return nil, err
	}

	var warnings []string
	if missing := n.missingCategories(ctx); len(missing) > 0 {
		warnings = append(warnings, fmt.Sprintf("NZBGet has no %s %s, so those downloads land in its main folder. Add it under Settings, Categories in NZBGet.",
			plural(len(missing), "category", "categories"), quoteList(missing)))
	}
	return warnings, nil
}

// missingCategories lists the categories Bragi will send that NZBGet does
// not define. It returns nothing when NZBGet will not share its settings.
func (n *nzbget) missingCategories(ctx context.Context) []string {
	if len(n.cfg.Categories) == 0 {
		return nil
	}
	var opts []struct {
		Name  string `json:"Name"`
		Value string `json:"Value"`
	}
	if err := n.call(ctx, "config", nil, &opts); err != nil {
		return nil
	}
	have := map[string]bool{}
	for _, o := range opts {
		if strings.HasPrefix(o.Name, "Category") && strings.HasSuffix(o.Name, ".Name") {
			have[strings.ToLower(o.Value)] = true
		}
	}
	var missing []string
	seen := map[string]bool{}
	for _, c := range n.cfg.Categories {
		key := strings.ToLower(c)
		if c == "" || have[key] || seen[key] {
			continue
		}
		seen[key] = true
		missing = append(missing, c)
	}
	return missing
}

func nzbgetMajor(version string) int {
	head, _, _ := strings.Cut(version, ".")
	major, _ := strconv.Atoi(strings.TrimSpace(head))
	return major
}

func (n *nzbget) Add(ctx context.Context, r Release) (string, error) {
	// Bragi fetches the NZB itself, so NZBGet never needs to reach the indexer.
	nzb, err := fetch(ctx, n.hc, r.DownloadURL)
	if err != nil {
		return "", fmt.Errorf("could not fetch the NZB: %w", err)
	}
	name := r.Title + ".nzb"
	// DupeMode FORCE: Bragi has already decided to grab this release, so
	// NZBGet's own duplicate check must not quietly drop it, e.g. a re-grab
	// after the first copy failed.
	params := []any{name, base64.StdEncoding.EncodeToString(nzb), n.cfg.Category, 0, false, false, "", 0, "FORCE", []any{}}
	var id int64
	if err := n.call(ctx, "append", params, &id); err != nil {
		if errors.Is(err, errNZBGetAccess) {
			return "", ErrUnauthorized
		}
		return "", err
	}
	if id <= 0 {
		return "", fmt.Errorf("NZBGet refused the NZB")
	}
	return strconv.FormatInt(id, 10), nil
}

// nzbgetStages names the post-processing steps NZBGet reports in its queue.
var nzbgetStages = map[string]string{
	"PP_QUEUED":          "Waiting to unpack",
	"LOADING_PARS":       "Verifying",
	"VERIFYING_SOURCES":  "Verifying",
	"VERIFYING_REPAIRED": "Verifying",
	"REPAIRING":          "Repairing",
	"RENAMING":           "Renaming",
	"UNPACKING":          "Unpacking",
	"MOVING":             "Moving",
	"EXECUTING_SCRIPT":   "Running scripts",
	"PP_FINISHED":        "Finishing",
	"FETCHING":           "Fetching the NZB",
}

// nzbgetOutcome turns a history status such as "WARNING/DAMAGED" into a
// job state and, for failures, a reason a person can act on.
func nzbgetOutcome(status string) (state, reason string) {
	kind, detail, _ := strings.Cut(status, "/")
	switch kind {
	case "SUCCESS":
		return JobCompleted, ""
	case "WARNING":
		switch detail {
		case "SCRIPT":
			// A post-processing script failed; the files themselves are fine.
			return JobCompleted, ""
		case "SPACE":
			return JobFailed, "NZBGet ran out of disk space while unpacking"
		case "PASSWORD":
			return JobFailed, "the release is password protected"
		case "DAMAGED", "REPAIRABLE":
			return JobFailed, "NZBGet finished with damaged files that were not repaired"
		case "HEALTH":
			return JobFailed, "too many articles are missing on the server"
		}
		return JobFailed, "NZBGet finished with a warning (" + strings.ToLower(detail) + ")"
	case "DELETED":
		switch detail {
		case "MANUAL":
			return JobFailed, "removed in NZBGet"
		case "DUPE", "COPY", "GOOD":
			return JobFailed, "NZBGet skipped it as a duplicate"
		case "HEALTH":
			return JobFailed, "too many articles are missing on the server"
		case "BAD":
			return JobFailed, "marked as bad in NZBGet"
		case "SCAN":
			return JobFailed, "NZBGet could not read the NZB"
		}
		return JobFailed, "removed in NZBGet"
	case "FAILURE":
		switch detail {
		case "PAR":
			return JobFailed, "the download is damaged and could not be repaired"
		case "UNPACK":
			return JobFailed, "NZBGet could not unpack the download"
		case "MOVE":
			return JobFailed, "NZBGet could not move the files to its destination folder"
		case "HEALTH":
			return JobFailed, "too many articles are missing on the server"
		case "FETCH":
			return JobFailed, "NZBGet could not fetch the NZB"
		case "BAD":
			return JobFailed, "marked as bad in NZBGet"
		}
	}
	return JobFailed, "NZBGet reports " + strings.ToLower(strings.ReplaceAll(status, "/", " "))
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
		Kind     string `json:"Kind"`
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
		switch q.Status {
		case "QUEUED", "PAUSED":
			j.State = JobQueued
			if q.Status == "PAUSED" {
				j.Stage = "Paused in NZBGet"
			}
		default:
			if stage, ok := nzbgetStages[q.Status]; ok {
				j.Stage = stage
				if q.Status != "FETCHING" {
					j.Progress = 1 // downloaded; NZBGet is post-processing
				}
			}
		}
		found[id] = j
	}
	for _, h := range history {
		id := strconv.FormatInt(h.NZBID, 10)
		if _, ok := found[id]; ok || (h.Kind != "" && h.Kind != "NZB" && h.Kind != "URL") {
			continue
		}
		j := Job{ID: id, Progress: 1, OutputPath: h.DestDir}
		if h.FinalDir != "" {
			j.OutputPath = h.FinalDir
		}
		j.State, j.Error = nzbgetOutcome(h.Status)
		if j.Error != "" {
			j.Error = sentenceCase(j.Error)
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

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func quoteList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = "“" + s + "”"
	}
	switch len(q) {
	case 1:
		return q[0]
	case 2:
		return q[0] + " or " + q[1]
	}
	return strings.Join(q[:len(q)-1], ", ") + " or " + q[len(q)-1]
}

func sentenceCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
