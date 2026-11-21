// Package api serves Bragi's REST API and the embedded web UI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Tharkis/bragi/internal/events"
	"github.com/Tharkis/bragi/internal/store"
	"github.com/Tharkis/bragi/internal/tv"
)

// Server holds the API's dependencies.
type Server struct {
	store   *store.Store
	log     *slog.Logger
	version string
	hc      *http.Client
	ui      fs.FS
	mux     *http.ServeMux
	tv      *tv.Service
	events  *events.Hub
}

// Options configures a Server.
type Options struct {
	Store   *store.Store
	Logger  *slog.Logger
	Version string
	// UI is the built web app (index.html at its root). Nil serves a notice.
	UI fs.FS
	// HTTPClient is used for outbound calls to indexers and clients.
	HTTPClient *http.Client
	// TV runs the TV pipeline. Nil builds one that is not scheduled, for tests.
	TV *tv.Service
	// Events carries live status changes to /api/v1/events.
	Events *events.Hub
}

// New returns the HTTP handler for the whole app.
func New(o Options) http.Handler {
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Events == nil {
		o.Events = events.NewHub()
	}
	if o.TV == nil {
		o.TV = tv.New(o.Store, o.HTTPClient, o.Logger, o.Events)
	}
	s := &Server{store: o.Store, log: o.Logger, version: o.Version, hc: o.HTTPClient, ui: o.UI, mux: http.NewServeMux(),
		tv: o.TV, events: o.Events}
	s.routes()
	return s.withUser(s.logRequests(s.mux))
}

func (s *Server) routes() {
	m := s.mux

	// Public
	m.HandleFunc("GET /api/v1/health", s.handleHealth)
	m.HandleFunc("GET /api/v1/setup", s.handleSetupStatus)
	m.HandleFunc("POST /api/v1/setup", s.handleSetup)
	m.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	m.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)

	// Signed-in users
	m.HandleFunc("GET /api/v1/auth/me", s.requireUser(s.handleMe))
	m.HandleFunc("POST /api/v1/auth/apikey", s.requireUser(s.handleRotateAPIKey))
	m.HandleFunc("GET /api/v1/qualitypresets", s.requireUser(s.handleListPresets))

	// Admin: settings
	m.HandleFunc("GET /api/v1/settings", s.requireAdmin(s.handleGetSettings))
	m.HandleFunc("PUT /api/v1/settings", s.requireAdmin(s.handlePutSettings))
	m.HandleFunc("GET /api/v1/integrations/kinds", s.requireAdmin(s.handleKinds))

	for _, t := range []struct {
		path  string
		table store.IntegrationTable
	}{{"indexers", store.Indexers}, {"downloadclients", store.DownloadClients}} {
		h := integrationHandlers{s: s, table: t.table}
		base := "/api/v1/" + t.path
		m.HandleFunc("GET "+base, s.requireAdmin(h.list))
		m.HandleFunc("POST "+base, s.requireAdmin(h.create))
		m.HandleFunc("POST "+base+"/test", s.requireAdmin(h.testUnsaved))
		m.HandleFunc("GET "+base+"/{id}", s.requireAdmin(h.get))
		m.HandleFunc("PUT "+base+"/{id}", s.requireAdmin(h.update))
		m.HandleFunc("DELETE "+base+"/{id}", s.requireAdmin(h.delete))
		m.HandleFunc("POST "+base+"/{id}/test", s.requireAdmin(h.testSaved))
	}

	m.HandleFunc("GET /api/v1/libraries", s.requireAdmin(s.handleListLibraries))
	m.HandleFunc("POST /api/v1/libraries", s.requireAdmin(s.handleCreateLibrary))
	m.HandleFunc("PUT /api/v1/libraries/{id}", s.requireAdmin(s.handleUpdateLibrary))
	m.HandleFunc("DELETE /api/v1/libraries/{id}", s.requireAdmin(s.handleDeleteLibrary))

	m.HandleFunc("POST /api/v1/settings/test/tmdb", s.requireAdmin(s.handleTestTMDB))
	m.HandleFunc("POST /api/v1/settings/test/jellyfin", s.requireAdmin(s.handleTestJellyfin))
	m.HandleFunc("POST /api/v1/libraries/scan", s.requireAdmin(s.handleStartScan))
	m.HandleFunc("GET /api/v1/libraries/scan", s.requireAdmin(s.handleScanState))

	// Discover and requests: the API the web UI, scripts and the Jellyfin
	// plugin share.
	m.HandleFunc("GET /api/v1/discover/search", s.requireUser(s.handleDiscoverSearch))
	m.HandleFunc("GET /api/v1/discover/tv/{id}", s.requireUser(s.handleDiscoverShow))
	m.HandleFunc("GET /api/v1/discover/trending", s.requireUser(s.handleDiscoverTrending))
	m.HandleFunc("POST /api/v1/requests", s.requireUser(s.handleCreateRequest))
	m.HandleFunc("GET /api/v1/requests", s.requireUser(s.handleListRequests))
	m.HandleFunc("GET /api/v1/requests/{id}", s.requireUser(s.handleGetRequest))
	m.HandleFunc("POST /api/v1/requests/{id}/approve", s.requireAdmin(s.handleApproveRequest))
	m.HandleFunc("POST /api/v1/requests/{id}/deny", s.requireAdmin(s.handleDenyRequest))
	m.HandleFunc("DELETE /api/v1/requests/{id}", s.requireUser(s.handleDeleteRequest))
	m.HandleFunc("GET /api/v1/events", s.requireUser(s.handleEvents))

	// Library and activity
	m.HandleFunc("GET /api/v1/series", s.requireUser(s.handleListSeries))
	m.HandleFunc("GET /api/v1/series/{id}", s.requireUser(s.handleGetSeries))
	m.HandleFunc("DELETE /api/v1/series/{id}", s.requireAdmin(s.handleDeleteSeries))
	m.HandleFunc("POST /api/v1/series/{id}/search", s.requireAdmin(s.handleSearchSeries))
	m.HandleFunc("POST /api/v1/series/{id}/refresh", s.requireAdmin(s.handleRefreshSeries))
	m.HandleFunc("PUT /api/v1/series/{id}/monitor", s.requireAdmin(s.handleMonitor))
	m.HandleFunc("GET /api/v1/series/{id}/releases", s.requireAdmin(s.handleReleases))
	m.HandleFunc("POST /api/v1/series/{id}/grab", s.requireAdmin(s.handleGrab))
	m.HandleFunc("GET /api/v1/activity/queue", s.requireUser(s.handleQueue))
	m.HandleFunc("GET /api/v1/activity/history", s.requireUser(s.handleHistory))
	m.HandleFunc("DELETE /api/v1/activity/queue/{id}", s.requireAdmin(s.handleRemoveDownload))

	// Jellyseerr-compatible endpoints for existing request clients.
	s.seerrRoutes()

	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "no such endpoint")
	})
	m.HandleFunc("/", s.handleUI)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DB.PingContext(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
}

// ---- helpers ----

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errorBody{Error: msg})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

// storeError maps store errors to HTTP responses.
func (s *Server) storeError(w http.ResponseWriter, err error, what string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, what+" not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, what+" with that name or path already exists")
	default:
		s.log.Error("store error", "what", what, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush lets server-sent events through the logging wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.log.Debug("request", "method", r.Method, "path", r.URL.Path, "status", rec.status, "dur", time.Since(start))
		}
	})
}

// testTimeout bounds a connection test so the UI never hangs.
const testTimeout = 10 * time.Second

func withTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
