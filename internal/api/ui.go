package api

import (
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

const uiMissing = `<!doctype html><meta charset="utf-8"><title>Bragi</title>
<p style="font-family:sans-serif">Bragi is running, but the web UI was not built into this binary.
Run <code>make ui</code> (or <code>npm run build</code> in <code>web/</code>) and rebuild.</p>`

// handleUI serves the single-page app. Paths that are not files fall back
// to index.html so client-side routes survive a reload.
func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.ui == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(uiMissing))
		return
	}
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name != "" {
		if fi, err := fs.Stat(s.ui, name); err == nil && !fi.IsDir() {
			if strings.HasPrefix(name, "assets/") {
				// Vite fingerprints asset names, so they can be cached forever.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeFileFS(w, r, s.ui, name)
			return
		}
	}
	index, err := fs.ReadFile(s.ui, "index.html")
	if errors.Is(err, fs.ErrNotExist) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(uiMissing))
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(index)
}
