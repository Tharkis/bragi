package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Tharkis/bragi/internal/books"
	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/release"
	"github.com/Tharkis/bragi/internal/store"
)

// Books and audiobooks: discover, request, library and manual search. The
// request endpoints are shared with TV; they hand book requests to the
// functions here.

func coverURL(coverID, size string) string { return metadata.CoverURL(coverID, size) }

// bookError turns Open Library and pipeline failures into a status and a
// message for the UI.
func (s *Server) bookError(w http.ResponseWriter, err error) {
	var nolib books.NoLibraryError
	switch {
	case errors.As(err, &nolib):
		writeError(w, http.StatusFailedDependency, sentence(err.Error()))
	case errors.Is(err, metadata.ErrNotFound), errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "book not found")
	default:
		s.log.Warn("book error", "err", err)
		writeError(w, http.StatusBadGateway, sentence(err.Error()))
	}
}

type bookResult struct {
	BookID   string `json:"bookId"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Author   string `json:"author"`
	Year     int    `json:"year,omitempty"`
	CoverURL string `json:"coverUrl,omitempty"`
	Overview string `json:"overview,omitempty"`
	// ItemID is set when the book is already in the library as this type.
	ItemID int64 `json:"itemId,omitempty"`
	// Requested is the status of the newest open request for it, if any.
	Requested string `json:"requested,omitempty"`
}

func (s *Server) annotateBook(ctx context.Context, d *bookResult) {
	if b, err := s.store.GetBookByForeignID(ctx, d.Type, d.BookID); err == nil {
		d.ItemID = b.ID
	}
	if reqs, err := s.store.ListRequests(ctx, store.RequestFilter{MediaType: d.Type, ForeignID: d.BookID}); err == nil {
		for _, rq := range reqs {
			if rq.Status == "pending" || rq.Status == "approved" || rq.Status == "available" {
				d.Requested = rq.Status
				break
			}
		}
	}
}

// handleDiscoverBooks is GET /discover/search?type=book|audiobook.
func (s *Server) handleDiscoverBooks(w http.ResponseWriter, r *http.Request, mediaType string) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusOK, []bookResult{})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	found, err := s.books.OpenLibrary().SearchBooks(ctx, q)
	if err != nil {
		s.bookError(w, err)
		return
	}
	out := make([]bookResult, 0, len(found))
	for _, b := range found {
		d := bookResult{BookID: b.ID, Type: mediaType, Title: b.Title, Author: b.Author, Year: b.Year,
			CoverURL: coverURL(metadata.CoverIDString(b.CoverID), "M")}
		s.annotateBook(ctx, &d)
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDiscoverBook is GET /discover/book/{id}: one work's details.
func (s *Server) handleDiscoverBook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	mediaType := r.URL.Query().Get("type")
	if !store.IsBookType(mediaType) {
		mediaType = "book"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	b, err := s.books.OpenLibrary().Work(ctx, id)
	if err != nil {
		s.bookError(w, err)
		return
	}
	d := bookResult{BookID: b.ID, Type: mediaType, Title: b.Title, Author: b.Author, Year: b.Year, Overview: b.Overview,
		CoverURL: coverURL(metadata.CoverIDString(b.CoverID), "L")}
	s.annotateBook(ctx, &d)
	writeJSON(w, http.StatusOK, d)
}

// createBookRequest handles POST /requests for type book or audiobook.
func (s *Server) createBookRequest(w http.ResponseWriter, r *http.Request, in requestBody) {
	u := userFrom(r.Context())
	if !metadata.ValidWorkID(in.BookID) {
		writeError(w, http.StatusBadRequest, `bookId must be an Open Library work id, like "OL27448W"`)
		return
	}
	if mine, err := s.store.ListRequests(r.Context(), store.RequestFilter{UserID: u.ID, MediaType: in.Type, ForeignID: in.BookID}); err == nil {
		for _, rq := range mine {
			if rq.Status == "pending" {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "you already asked for this book", "request": s.toRequestResponse(r.Context(), rq, false)})
				return
			}
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	b, err := s.books.OpenLibrary().Work(ctx, in.BookID)
	if err != nil {
		s.bookError(w, err)
		return
	}
	rq, err := s.store.CreateRequest(r.Context(), store.Request{UserID: u.ID, MediaType: in.Type, ForeignID: b.ID, Author: b.Author,
		Title: b.Title, Year: b.Year, PosterPath: metadata.CoverIDString(b.CoverID), Scope: "{}", Status: "pending"})
	if err != nil {
		s.storeError(w, err, "request")
		return
	}
	s.log.Info("request created", "user", u.Username, "type", in.Type, "title", b.Title)
	if u.Role == "admin" || u.AutoApprove {
		rq, _ = s.approve(r.Context(), rq, "")
	} else {
		s.log.Info("request waits for an admin to approve it", "request", rq.ID, "user", u.Username)
	}
	s.events.Publish(eventRequest(rq))
	writeJSON(w, http.StatusCreated, s.toRequestResponse(r.Context(), *rq, false))
}

// approveBook adds the book and marks the request approved, or leaves it
// pending with the reason.
func (s *Server) approveBook(ctx context.Context, rq *store.Request, reason string) (*store.Request, error) {
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	b, err := s.books.AddBook(actx, rq.MediaType, rq.ForeignID, 0)
	if err != nil {
		s.log.Warn("request approved but could not be added; it stays pending", "request", rq.ID, "type", rq.MediaType, "title", rq.Title, "err", err)
		s.store.SetRequestStatus(ctx, rq.ID, "pending", "Could not add the book: "+sentence(err.Error()), 0)
		out, _ := s.store.GetRequest(ctx, rq.ID)
		if out == nil {
			out = rq
		}
		return out, err
	}
	s.log.Info("request approved", "request", rq.ID, "type", rq.MediaType, "title", rq.Title)
	s.store.SetRequestStatus(ctx, rq.ID, "approved", reason, b.ID)
	if b.Status == "imported" {
		s.store.MarkBookRequestsAvailable(ctx, b.ID)
	}
	out, err := s.store.GetRequest(ctx, rq.ID)
	if err != nil {
		return rq, err
	}
	return out, nil
}

// bookRequestResponse fills a book request's cover and progress.
func (s *Server) bookRequestResponse(ctx context.Context, rq store.Request) requestResponse {
	out := requestResponse{Request: rq, PosterURL: coverURL(rq.PosterPath, "M")}
	if rq.MediaItemID != 0 {
		if b, err := s.store.GetBook(ctx, rq.MediaItemID); err == nil {
			p := &requestProgress{SeriesID: b.ID, Episodes: 1, Activities: b.Activities}
			if b.Status == "imported" {
				p.Imported = 1
			} else {
				p.Missing = 1
			}
			out.Progress = p
		}
	}
	return out
}

// ---- Library ----

type bookResponse struct {
	store.Book
	CoverURL string `json:"coverUrl,omitempty"`
	Path     string `json:"path,omitempty"`
}

func (s *Server) toBook(ctx context.Context, b store.Book, size string) bookResponse {
	out := bookResponse{Book: b, CoverURL: coverURL(b.CoverID, size)}
	if lib, err := s.store.GetLibrary(ctx, b.LibraryID); err == nil {
		out.Path = lib.Path + "/" + b.Folder
	}
	return out
}

func (s *Server) handleListBooks(w http.ResponseWriter, r *http.Request) {
	t := r.URL.Query().Get("type")
	if t != "" && !store.IsBookType(t) {
		writeError(w, http.StatusBadRequest, "type must be book or audiobook")
		return
	}
	list, err := s.store.ListBooks(r.Context(), t)
	if err != nil {
		s.storeError(w, err, "books")
		return
	}
	out := make([]bookResponse, 0, len(list))
	for _, b := range list {
		out = append(out, bookResponse{Book: b, CoverURL: coverURL(b.CoverID, "M")})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) bookFromPath(w http.ResponseWriter, r *http.Request) (*store.Book, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return nil, false
	}
	b, err := s.store.GetBook(r.Context(), id)
	if err != nil {
		s.storeError(w, err, "book")
		return nil, false
	}
	return b, true
}

func (s *Server) handleGetBook(w http.ResponseWriter, r *http.Request) {
	b, ok := s.bookFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.toBook(r.Context(), *b, "L"))
}

func (s *Server) handleDeleteBook(w http.ResponseWriter, r *http.Request) {
	b, ok := s.bookFromPath(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteBook(r.Context(), b.ID); err != nil {
		s.storeError(w, err, "book")
		return
	}
	s.events.Publish(eventT{Type: "book", Data: map[string]any{"id": b.ID}})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSearchBook(w http.ResponseWriter, r *http.Request) {
	b, ok := s.bookFromPath(w, r)
	if !ok {
		return
	}
	if err := s.books.SearchNow(r.Context(), b.ID); err != nil {
		writeError(w, http.StatusConflict, sentence(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"queued": 1})
}

func (s *Server) handleMonitorBook(w http.ResponseWriter, r *http.Request) {
	b, ok := s.bookFromPath(w, r)
	if !ok {
		return
	}
	var in struct {
		Monitored bool `json:"monitored"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if err := s.store.SetBookMonitored(r.Context(), b.ID, in.Monitored); err != nil {
		s.storeError(w, err, "book")
		return
	}
	if in.Monitored {
		s.books.Kick()
	}
	s.handleGetBook(w, r)
}

type bookReleaseResponse struct {
	books.Candidate
	Size string `json:"size"`
	Age  string `json:"age,omitempty"`
}

// handleBookReleases is the manual pick: every release the indexers and
// Soulseek return for a book, best first, with the reason any was rejected.
func (s *Server) handleBookReleases(w http.ResponseWriter, r *http.Request) {
	b, ok := s.bookFromPath(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	cands, _, err := s.books.Search(ctx, b)
	if err != nil {
		writeError(w, http.StatusBadGateway, sentence(err.Error()))
		return
	}
	out := make([]bookReleaseResponse, 0, len(cands))
	for _, c := range cands {
		a := age(c.PublishedAt)
		if c.Protocol == integrations.Soulseek {
			a = "" // Soulseek results have no age
		}
		out = append(out, bookReleaseResponse{Candidate: c, Size: release.HumanSize(c.SizeBytes), Age: a})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGrabBook(w http.ResponseWriter, r *http.Request) {
	b, ok := s.bookFromPath(w, r)
	if !ok {
		return
	}
	var in struct {
		Release integrations.Release `json:"release"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Release.DownloadURL == "" || in.Release.Title == "" {
		writeError(w, http.StatusBadRequest, "release title and downloadUrl are required")
		return
	}
	switch in.Release.Protocol {
	case integrations.Usenet, integrations.Torrent, integrations.Soulseek:
	default:
		writeError(w, http.StatusBadRequest, "release protocol must be usenet, torrent or soulseek")
		return
	}
	c := books.Candidate{Release: in.Release, Format: books.FormatOf(in.Release.Title)}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := s.books.Grab(ctx, b, c); err != nil {
		writeError(w, http.StatusBadGateway, sentence(err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
