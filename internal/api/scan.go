package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Tharkis/bragi/internal/books"
	"github.com/Tharkis/bragi/internal/events"
	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/movies"
	"github.com/Tharkis/bragi/internal/scan"
	"github.com/Tharkis/bragi/internal/store"
	"github.com/Tharkis/bragi/internal/tv"
)

// NewScanManager builds the library scanner with every media type's
// pipeline. Once a scan ends, the pipeline searches for anything newly
// wanted (new episodes of an adopted show, say).
func NewScanManager(st *store.Store, log *slog.Logger, hub *events.Hub, t *tv.Service, m *movies.Service, b *books.Service) *scan.Manager {
	mgr := scan.New(st, log, hub)
	mgr.Register("tv", t)
	mgr.Register("movie", m)
	mgr.Register("book", b)
	mgr.Register("audiobook", b)
	mgr.After = func(mediaType string) {
		switch mediaType {
		case "tv":
			t.Kick()
		case "movie":
			m.Kick()
		default:
			b.Kick()
		}
	}
	return mgr
}

func scanType(r *http.Request) string {
	if t := r.URL.Query().Get("type"); t != "" {
		return t
	}
	return "tv"
}

// handleStartScan scans one library: the one in the body, or the first of
// ?type= (tv by default).
func (s *Server) handleStartScan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LibraryID int64 `json:"libraryId"`
	}
	if r.ContentLength > 0 && !readJSON(w, r, &in) {
		return
	}
	st, err := s.scans.Start(r.Context(), scanType(r), in.LibraryID)
	switch {
	case errors.Is(err, scan.ErrRunning):
		writeError(w, http.StatusConflict, sentence(err.Error()))
	case errors.Is(err, scan.ErrNoLibrary):
		writeError(w, http.StatusFailedDependency, "Add a library of that type under Settings, Libraries first")
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such library")
	case err != nil:
		writeError(w, http.StatusBadRequest, sentence(err.Error()))
	default:
		writeJSON(w, http.StatusAccepted, st)
	}
}

// handleScanState returns the current or last scan of a media type.
func (s *Server) handleScanState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.scans.State(scanType(r)))
}

type scanAllState struct {
	Running  bool                   `json:"running"`
	Nightly  bool                   `json:"nightly"`
	LastFull string                 `json:"lastFull,omitempty"`
	Hour     int                    `json:"hour"`
	Types    map[string]scan.Status `json:"types"`
}

func (s *Server) scanAllState(ctx context.Context) scanAllState {
	nightly, _ := s.store.GetSetting(ctx, scan.SettingNightly)
	last, _ := s.store.GetSetting(ctx, scan.SettingLastFull)
	out := scanAllState{Running: s.scans.Busy(), Nightly: nightly != "false", LastFull: last, Hour: scan.NightlyHour,
		Types: map[string]scan.Status{}}
	for _, t := range []string{"tv", "movie", "book", "audiobook"} {
		out.Types[t] = s.scans.State(t)
	}
	return out
}

// handleScanAll scans every library, retrying what earlier scans missed.
func (s *Server) handleScanAll(w http.ResponseWriter, r *http.Request) {
	n, err := s.scans.StartAll(r.Context(), false)
	if err != nil {
		s.storeError(w, err, "libraries")
		return
	}
	if n == 0 && !s.scans.Busy() {
		writeError(w, http.StatusFailedDependency, "Add a library under Settings, Libraries first")
		return
	}
	writeJSON(w, http.StatusAccepted, s.scanAllState(r.Context()))
}

func (s *Server) handleScanAllState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.scanAllState(r.Context()))
}

// handleListUnmatched lists what scans could not match, for ?type= (all
// types when empty). ?ignored=1 includes the ignored ones.
func (s *Server) handleListUnmatched(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListUnmatched(r.Context(), r.URL.Query().Get("type"), r.URL.Query().Get("ignored") == "1")
	if err != nil {
		s.storeError(w, err, "unmatched items")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleMatchUnmatched adopts an unmatched item as the TMDB id or Open
// Library work in the body.
func (s *Server) handleMatchUnmatched(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.ID = strings.TrimSpace(in.ID)
	if in.ID == "" {
		writeError(w, http.StatusBadRequest, "pick what this is first")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	res, err := s.scans.Match(ctx, id, in.ID)
	var nm *scan.NoMatchError
	switch {
	case errors.As(err, &nm):
		writeError(w, http.StatusConflict, sentence(nm.Reason))
	case errors.Is(err, metadata.ErrNotFound):
		writeError(w, http.StatusNotFound, "Open Library has no such book")
	case err != nil:
		s.metadataError(w, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"name": res.Name, "files": res.Files, "new": res.New})
	}
}

// handleIgnoreUnmatched hides an unmatched item from the list and from
// later scans, or brings it back.
func (s *Server) handleIgnoreUnmatched(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	in := struct {
		Ignored *bool `json:"ignored"`
	}{}
	if r.ContentLength > 0 && !readJSON(w, r, &in) {
		return
	}
	ignored := in.Ignored == nil || *in.Ignored
	if err := s.store.SetUnmatchedIgnored(r.Context(), id, ignored); err != nil {
		s.storeError(w, err, "unmatched item")
		return
	}
	s.events.Publish(events.Event{Type: "scan"})
	writeJSON(w, http.StatusOK, map[string]bool{"ignored": ignored})
}
