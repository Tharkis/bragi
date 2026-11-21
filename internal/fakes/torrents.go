package fakes

import (
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// torrentPath is p (under Dir) as the fake Transmission or Deluge sees it.
func (d *Downloader) torrentPath(p string) string {
	if d.TorrentRoot == "" {
		return filepath.ToSlash(p)
	}
	rel, err := filepath.Rel(d.Dir, p)
	if err != nil {
		return p
	}
	return path.Join(d.TorrentRoot, filepath.ToSlash(rel))
}

// addTorrent starts a job from a magnet link or a .torrent file's bytes.
func (d *Downloader) addTorrent(magnet string, file []byte) *job {
	if magnet != "" {
		u, _ := url.Parse(magnet)
		hash := strings.TrimPrefix(u.Query().Get("xt"), "urn:btih:")
		return d.add(strings.ToLower(hash), u.Query().Get("dn"))
	}
	sum := sha1.Sum(file)
	return d.add(hex.EncodeToString(sum[:]), "torrent-"+hex.EncodeToString(sum[:4]))
}

// find returns the job with id, or nil. The caller holds d.mu.
func (d *Downloader) find(id string) *job {
	for _, j := range d.jobs {
		if j.id == strings.ToLower(id) && !j.removed {
			return j
		}
	}
	return nil
}

// failed is the error TorrentError gives j once it is half done.
func (d *Downloader) failed(j *job, p float64) string {
	if d.TorrentError == nil || p < 0.5 {
		return ""
	}
	return d.TorrentError(j.name)
}

// Transmission serves the fake Transmission RPC API, with its session-id
// handshake and labels (as Transmission 4).
func (d *Downloader) Transmission() http.Handler {
	const session = "fake-session"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transmission/rpc" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Transmission-Session-Id") != session {
			w.Header().Set("X-Transmission-Session-Id", session)
			w.WriteHeader(http.StatusConflict)
			return
		}
		var call struct {
			Method    string          `json:"method"`
			Arguments json.RawMessage `json:"arguments"`
		}
		json.NewDecoder(r.Body).Decode(&call)
		reply := func(args any) { writeJSON(w, map[string]any{"result": "success", "arguments": args}) }
		root := d.torrentPath(d.Dir)
		switch call.Method {
		case "session-get":
			reply(map[string]any{"version": "4.0.6 (38c164933e)", "rpc-version": 17, "download-dir": root})
		case "torrent-add":
			var a struct {
				Filename    string   `json:"filename"`
				Metainfo    string   `json:"metainfo"`
				DownloadDir string   `json:"download-dir"`
				Labels      []string `json:"labels"`
			}
			json.Unmarshal(call.Arguments, &a)
			file, _ := base64.StdEncoding.DecodeString(a.Metainfo)
			j := d.addTorrent(a.Filename, file)
			d.mu.Lock()
			if rel, ok := strings.CutPrefix(a.DownloadDir, root+"/"); ok {
				j.sub = filepath.FromSlash(rel)
			}
			if len(a.Labels) > 0 {
				j.category = a.Labels[0]
			}
			d.mu.Unlock()
			reply(map[string]any{"torrent-added": map[string]any{"hashString": j.id, "name": j.name, "id": 1}})
		case "torrent-get":
			var a struct {
				IDs []string `json:"ids"`
			}
			json.Unmarshal(call.Arguments, &a)
			d.mu.Lock()
			defer d.mu.Unlock()
			out := []map[string]any{}
			for _, id := range a.IDs {
				j := d.find(id)
				if j == nil {
					continue
				}
				p := d.progress(j)
				t := map[string]any{"hashString": j.id, "name": j.name, "status": 4, "percentDone": p,
					"sizeWhenDone": 1000, "leftUntilDone": int(1000 * (1 - p)), "metadataPercentComplete": 1,
					"downloadDir": d.torrentPath(filepath.Join(d.Dir, j.sub)), "error": 0, "errorString": ""}
				if p >= 1 {
					t["status"] = 6
				}
				if msg := d.failed(j, p); msg != "" {
					t["status"], t["error"], t["errorString"] = 0, 3, msg
				}
				out = append(out, t)
			}
			reply(map[string]any{"torrents": out})
		case "torrent-remove":
			var a struct {
				IDs []string `json:"ids"`
			}
			json.Unmarshal(call.Arguments, &a)
			d.mu.Lock()
			for _, id := range a.IDs {
				if j := d.find(id); j != nil {
					j.removed = true
				}
			}
			d.mu.Unlock()
			reply(map[string]any{})
		default:
			writeJSON(w, map[string]any{"result": "method name not recognized"})
		}
	})
}

// Deluge serves the fake Deluge web UI JSON-RPC API. Its password is
// "deluge" and it starts disconnected from its one daemon.
func (d *Downloader) Deluge() http.Handler {
	var connected bool
	labels := []string{}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json" {
			http.NotFound(w, r)
			return
		}
		var call struct {
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
			ID     int               `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&call)
		reply := func(v any) { writeJSON(w, map[string]any{"result": v, "error": nil, "id": call.ID}) }
		fail := func(code int, msg string) {
			writeJSON(w, map[string]any{"result": nil, "error": map[string]any{"message": msg, "code": code}, "id": call.ID})
		}
		param := func(i int, v any) {
			if i < len(call.Params) {
				json.Unmarshal(call.Params[i], v)
			}
		}
		if call.Method == "auth.login" {
			var pw string
			param(0, &pw)
			if pw != "deluge" {
				reply(false)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "_session_id", Value: "fake", Path: "/"})
			reply(true)
			return
		}
		if c, err := r.Cookie("_session_id"); err != nil || c.Value != "fake" {
			fail(1, "Not authenticated")
			return
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		switch call.Method {
		case "web.connected":
			reply(connected)
		case "web.get_hosts":
			reply([][]any{{"c0ffee", "127.0.0.1", 58846, "localclient"}})
		case "web.connect":
			connected = true
			reply(nil)
		case "daemon.info":
			reply("2.1.1")
		case "core.get_enabled_plugins":
			if d.DelugeNoLabel {
				reply([]string{})
			} else {
				reply([]string{"Label"})
			}
		case "label.get_labels":
			reply(labels)
		case "label.add":
			var l string
			param(0, &l)
			if !slices.Contains(labels, l) {
				labels = append(labels, l)
			}
			reply(nil)
		case "label.set_torrent":
			var id, l string
			param(0, &id)
			param(1, &l)
			if !slices.Contains(labels, l) {
				fail(4, "Unknown Label")
				return
			}
			if j := d.find(id); j != nil {
				j.category = l
			}
			reply(nil)
		case "core.add_torrent_magnet", "core.add_torrent_file":
			var magnet, file string
			if call.Method == "core.add_torrent_magnet" {
				param(0, &magnet)
			} else {
				param(1, &file)
			}
			raw, _ := base64.StdEncoding.DecodeString(file)
			d.mu.Unlock()
			j := d.addTorrent(magnet, raw)
			d.mu.Lock()
			reply(j.id)
		case "core.get_torrents_status":
			var filter struct {
				ID []string `json:"id"`
			}
			param(0, &filter)
			out := map[string]any{}
			for _, id := range filter.ID {
				j := d.find(id)
				if j == nil {
					continue
				}
				p := d.progress(j)
				t := map[string]any{"state": "Downloading", "progress": p * 100, "name": j.name, "message": "OK",
					"save_path": d.torrentPath(d.Dir), "is_finished": p >= 1, "total_wanted": 1000, "total_done": int(1000 * p),
					"move_on_completed": false, "move_on_completed_path": ""}
				if p >= 1 {
					t["state"] = "Seeding"
				}
				if msg := d.failed(j, p); msg != "" {
					t["state"], t["message"] = "Error", msg
				}
				out[j.id] = t
			}
			reply(out)
		case "core.remove_torrent":
			var id string
			param(0, &id)
			if j := d.find(id); j != nil {
				j.removed = true
			}
			reply(true)
		default:
			fail(2, "Unknown method")
		}
	})
}
