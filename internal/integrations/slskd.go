package integrations

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"
)

// slskd talks to slskd (https://github.com/slskd/slskd), the Soulseek
// client with a web API. It is a download client that can also search:
// Soulseek has no indexers, so searching goes through the client itself.
type slskd struct {
	cfg Config
	hc  *http.Client
}

// SlskdSearchTimeout is how long slskd collects answers from peers.
var SlskdSearchTimeout = 15 * time.Second

// Searcher returns the search side of a download client that can search
// on its own (only slskd so far), or false.
func Searcher(kind string, cfg Config, hc *http.Client) (Indexer, bool) {
	if kind != "slskd" {
		return nil, false
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	return &slskd{cfg: cfg, hc: hc}, true
}

func (s *slskd) call(ctx context.Context, method, p string, in, out any) (int, error) {
	var body *bytes.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	var req *http.Request
	var err error
	if body != nil {
		req, err = http.NewRequestWithContext(ctx, method, s.cfg.BaseURL+p, body)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, s.cfg.BaseURL+p, nil)
	}
	if err != nil {
		return 0, fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("X-API-Key", s.cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := do(s.hc, req)
	if err != nil {
		return 0, err
	}
	b, err := readBody(resp)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode == http.StatusForbidden {
		return resp.StatusCode, ErrUnauthorized
	}
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(b))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return resp.StatusCode, fmt.Errorf("slskd answered HTTP %d %s", resp.StatusCode, msg)
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, fmt.Errorf("the response does not look like slskd: check the URL")
		}
	}
	return resp.StatusCode, nil
}

func (s *slskd) Test(ctx context.Context) error {
	var app struct {
		Server *struct {
			IsConnected bool   `json:"isConnected"`
			State       string `json:"state"`
		} `json:"server"`
	}
	if _, err := s.call(ctx, http.MethodGet, "/api/v0/application", nil, &app); err != nil {
		return err
	}
	if app.Server == nil {
		return errors.New("the response does not look like slskd: check the URL")
	}
	if !app.Server.IsConnected {
		return fmt.Errorf("slskd is running but not connected to the Soulseek network (%s)", app.Server.State)
	}
	return nil
}

// ---- Search ----

type slskdFile struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}

type slskdResponse struct {
	Username          string      `json:"username"`
	HasFreeUploadSlot bool        `json:"hasFreeUploadSlot"`
	UploadSpeed       int64       `json:"uploadSpeed"`
	QueueLength       int64       `json:"queueLength"`
	Files             []slskdFile `json:"files"`
}

// slskdPick is what a Soulseek release downloads: files from one user.
// It travels in Release.DownloadURL as "slskd:" + base64(JSON).
type slskdPick struct {
	User  string      `json:"u"`
	Files []slskdFile `json:"f"`
}

const slskdScheme = "slskd:"

func encodePick(p slskdPick) string {
	b, _ := json.Marshal(p)
	return slskdScheme + base64.RawURLEncoding.EncodeToString(b)
}

func decodePick(u string) (slskdPick, error) {
	var p slskdPick
	raw, ok := strings.CutPrefix(u, slskdScheme)
	if !ok {
		return p, errors.New("not a Soulseek release")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return p, errors.New("damaged Soulseek release")
	}
	if err := json.Unmarshal(b, &p); err != nil || p.User == "" || len(p.Files) == 0 {
		return p, errors.New("damaged Soulseek release")
	}
	return p, nil
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-a" + h[17:20] + "-" + h[20:32]
}

// Soulseek paths use backslashes whatever the sharer's OS.
func remoteDir(filename string) string {
	if i := strings.LastIndexAny(filename, `\/`); i >= 0 {
		return filename[:i]
	}
	return ""
}

func remoteBase(filename string) string {
	if i := strings.LastIndexAny(filename, `\/`); i >= 0 {
		return filename[i+1:]
	}
	return filename
}

// lastParts returns the last n parts of a Soulseek path, joined by " / ".
func lastParts(p string, n int) string {
	parts := strings.FieldsFunc(p, func(r rune) bool { return r == '\\' || r == '/' })
	if len(parts) > n {
		parts = parts[len(parts)-n:]
	}
	return strings.Join(parts, " / ")
}

var (
	ebookExts = map[string]bool{".epub": true, ".azw3": true, ".azw": true, ".mobi": true, ".pdf": true, ".cbz": true, ".cbr": true}
	audioExts = map[string]bool{".m4b": true, ".mp3": true, ".m4a": true, ".flac": true, ".ogg": true, ".opus": true, ".aac": true, ".wma": true}
)

// IsEbookFile and IsAudioFile tell book files apart by extension.
func IsEbookFile(name string) bool {
	return ebookExts[strings.ToLower(path.Ext(strings.ReplaceAll(name, `\`, "/")))]
}

// IsAudioFile reports whether name is an audiobook or music file.
func IsAudioFile(name string) bool {
	return audioExts[strings.ToLower(path.Ext(strings.ReplaceAll(name, `\`, "/")))]
}

// Search asks the Soulseek network. For books, each ebook file is a
// release; for audiobooks, each folder of audio files is one release.
func (s *slskd) Search(ctx context.Context, q Query) ([]Release, error) {
	if q.MediaType != "book" && q.MediaType != "audiobook" && q.MediaType != "music" {
		return nil, nil
	}
	id := newID()
	in := map[string]any{"id": id, "searchText": q.Text, "searchTimeout": SlskdSearchTimeout.Milliseconds(),
		"responseLimit": 100, "fileLimit": 10000, "filterResponses": true}
	if _, err := s.call(ctx, http.MethodPost, "/api/v0/searches", in, nil); err != nil {
		return nil, err
	}
	defer func() {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		s.call(dctx, http.MethodDelete, "/api/v0/searches/"+id, nil, nil)
	}()
	deadline := time.Now().Add(SlskdSearchTimeout + 10*time.Second)
	for {
		var st struct {
			IsComplete bool `json:"isComplete"`
		}
		if _, err := s.call(ctx, http.MethodGet, "/api/v0/searches/"+id, nil, &st); err != nil {
			return nil, err
		}
		if st.IsComplete || time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	var responses []slskdResponse
	if _, err := s.call(ctx, http.MethodGet, "/api/v0/searches/"+id+"/responses", nil, &responses); err != nil {
		return nil, err
	}
	return groupResponses(responses, q.MediaType), nil
}

// groupResponses turns peers' file lists into releases.
func groupResponses(responses []slskdResponse, mediaType string) []Release {
	var out []Release
	now := time.Now()
	for _, r := range responses {
		// Seeders has no Soulseek meaning; it carries how soon this peer
		// can send: a free upload slot first, then a short queue, then speed.
		score := int(min(r.UploadSpeed/(100<<10), 99))
		if r.HasFreeUploadSlot {
			score += 1000
		} else {
			score += int(max(0, 500-r.QueueLength))
		}
		mk := func(title string, files []slskdFile) Release {
			var size int64
			for _, f := range files {
				size += f.Size
			}
			pick := slskdPick{User: r.Username, Files: files}
			return Release{Title: title, GUID: r.Username + ":" + files[0].Filename, DownloadURL: encodePick(pick),
				Protocol: Soulseek, SizeBytes: size, Seeders: score, PublishedAt: now, Indexer: "Soulseek: " + r.Username}
		}
		if mediaType == "book" {
			for _, f := range r.Files {
				if IsEbookFile(f.Filename) {
					out = append(out, mk(lastParts(f.Filename, 2), []slskdFile{f}))
				}
			}
			continue
		}
		// Audiobooks and albums: one release per folder.
		dirs := map[string][]slskdFile{}
		covers := map[string]slskdFile{}
		var order []string
		for _, f := range r.Files {
			if isCoverFile(f.Filename) {
				covers[remoteDir(f.Filename)] = f
			}
			if !IsAudioFile(f.Filename) {
				continue
			}
			d := remoteDir(f.Filename)
			if _, ok := dirs[d]; !ok {
				order = append(order, d)
			}
			dirs[d] = append(dirs[d], f)
		}
		for _, d := range order {
			files := dirs[d]
			sort.Slice(files, func(i, j int) bool { return files[i].Filename < files[j].Filename })
			ext := strings.ToUpper(strings.TrimPrefix(path.Ext(strings.ReplaceAll(files[0].Filename, `\`, "/")), "."))
			title := fmt.Sprintf("%s [%s]", lastParts(d, 2), ext)
			if len(files) == 1 {
				title = fmt.Sprintf("%s [%s]", lastParts(files[0].Filename, 2), ext)
			}
			// Bring the folder's cover art along.
			if c, ok := covers[d]; ok {
				files = append(files, c)
			}
			out = append(out, mk(title, files))
		}
	}
	return out
}

// isCoverFile reports whether name is a folder's cover art.
func isCoverFile(name string) bool {
	base := strings.ToLower(remoteBase(name))
	return base == "cover.jpg" || base == "cover.png" || base == "folder.jpg" || base == "folder.png"
}

// ---- Downloads ----

// slskd jobs are named by the user and the remote folder, as JSON.
func jobName(user, dir string) string {
	b, _ := json.Marshal([]string{user, dir})
	return string(b)
}

func parseJob(id string) (user, dir string, ok bool) {
	var parts []string
	if json.Unmarshal([]byte(id), &parts) != nil || len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (s *slskd) Add(ctx context.Context, r Release) (string, error) {
	pick, err := decodePick(r.DownloadURL)
	if err != nil {
		return "", err
	}
	if _, err := s.call(ctx, http.MethodPost, "/api/v0/transfers/downloads/"+url.PathEscape(pick.User), pick.Files, nil); err != nil {
		return "", fmt.Errorf("slskd could not queue the files: %w", err)
	}
	return jobName(pick.User, remoteDir(pick.Files[0].Filename)), nil
}

type slskdTransfer struct {
	ID              string  `json:"id"`
	Filename        string  `json:"filename"`
	Size            int64   `json:"size"`
	State           string  `json:"state"`
	PercentComplete float64 `json:"percentComplete"`
	BytesTransfered int64   `json:"bytesTransferred"`
}

type slskdUserTransfers struct {
	Username    string `json:"username"`
	Directories []struct {
		Directory string          `json:"directory"`
		Files     []slskdTransfer `json:"files"`
	} `json:"directories"`
}

func (s *slskd) transfers(ctx context.Context) (map[string][]slskdTransfer, error) {
	var users []slskdUserTransfers
	if _, err := s.call(ctx, http.MethodGet, "/api/v0/transfers/downloads", nil, &users); err != nil {
		return nil, err
	}
	out := map[string][]slskdTransfer{}
	for _, u := range users {
		for _, d := range u.Directories {
			for _, f := range d.Files {
				k := jobName(u.Username, remoteDir(f.Filename))
				out[k] = append(out[k], f)
			}
		}
	}
	return out, nil
}

// downloadsDir is where slskd puts finished files, as Bragi sees it: the
// folder set on the connection, or slskd's own setting.
func (s *slskd) downloadsDir(ctx context.Context) (string, error) {
	if s.cfg.Category != "" {
		return strings.TrimRight(s.cfg.Category, "/"), nil
	}
	var opts struct {
		Directories struct {
			Downloads string `json:"downloads"`
		} `json:"directories"`
	}
	if _, err := s.call(ctx, http.MethodGet, "/api/v0/options", nil, &opts); err != nil {
		return "", err
	}
	if opts.Directories.Downloads == "" {
		return "", errors.New("slskd did not say where it saves downloads; set the downloads folder on the connection")
	}
	return strings.TrimRight(opts.Directories.Downloads, "/"), nil
}

func (s *slskd) Status(ctx context.Context, jobIDs []string) ([]Job, error) {
	all, err := s.transfers(ctx)
	if err != nil {
		return nil, err
	}
	var dir string
	out := make([]Job, 0, len(jobIDs))
	for _, id := range jobIDs {
		files := all[id]
		if len(files) == 0 {
			out = append(out, Job{ID: id, State: JobMissing})
			continue
		}
		j := slskdJob(id, files)
		if j.State == JobCompleted {
			if dir == "" {
				if dir, err = s.downloadsDir(ctx); err != nil {
					return nil, err
				}
			}
			_, rdir, _ := parseJob(id)
			// slskd saves into a folder named after the remote folder.
			// Other jobs can share that folder, so a one-file job points
			// at its file.
			j.OutputPath = dir + "/" + remoteBase(rdir)
			if len(files) == 1 {
				j.OutputPath += "/" + remoteBase(files[0].Filename)
			}
		}
		out = append(out, j)
	}
	return out, nil
}

// slskdJob sums up one job's files. It is finished when every file is; it
// failed if any file did not arrive.
func slskdJob(id string, files []slskdTransfer) Job {
	j := Job{ID: id}
	var size, done int64
	finished, failed, active := 0, "", false
	for _, f := range files {
		size += f.Size
		done += f.BytesTransfered
		switch {
		case f.State == "Completed, Succeeded":
			finished++
		case strings.HasPrefix(f.State, "Completed"):
			finished++
			if failed == "" {
				failed = remoteBase(f.Filename) + ": " + strings.ToLower(strings.TrimPrefix(f.State, "Completed, "))
			}
		case strings.Contains(f.State, "InProgress") || strings.Contains(f.State, "Initializing"):
			active = true
		}
	}
	if size > 0 {
		j.Progress = float64(done) / float64(size)
	}
	switch {
	case finished == len(files) && failed != "":
		j.State, j.Error = JobFailed, "Soulseek transfer failed ("+failed+")"
	case finished == len(files):
		j.State, j.Progress = JobCompleted, 1
	case active || done > 0:
		j.State = JobDownloading
	default:
		j.State = JobQueued
	}
	return j
}

func (s *slskd) Remove(ctx context.Context, jobID string, _ bool) error {
	user, _, ok := parseJob(jobID)
	if !ok {
		return errors.New("not a slskd job")
	}
	all, err := s.transfers(ctx)
	if err != nil {
		return err
	}
	var firstErr error
	for _, f := range all[jobID] {
		p := "/api/v0/transfers/downloads/" + url.PathEscape(user) + "/" + url.PathEscape(f.ID)
		// The first call cancels a transfer in progress; the second
		// removes it from the list.
		if !strings.HasPrefix(f.State, "Completed") {
			s.call(ctx, http.MethodDelete, p, nil, nil)
		}
		if _, err := s.call(ctx, http.MethodDelete, p+"?remove=true", nil, nil); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
