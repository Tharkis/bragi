package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/store"
)

const settingSetupComplete = "setup_complete"

// editableSettings are the keys PUT /settings accepts.
var editableSettings = map[string]bool{
	settingSetupComplete: true,
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	out := map[string]string{}
	for k := range editableSettings {
		v, err := s.store.GetSetting(r.Context(), k)
		if err != nil {
			s.storeError(w, err, "settings")
			return
		}
		out[k] = v
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var in map[string]string
	if !readJSON(w, r, &in) {
		return
	}
	for k := range in {
		if !editableSettings[k] {
			writeError(w, http.StatusBadRequest, "unknown setting "+k)
			return
		}
	}
	for k, v := range in {
		if err := s.store.SetSetting(r.Context(), k, v); err != nil {
			s.storeError(w, err, "settings")
			return
		}
	}
	s.handleGetSettings(w, r)
}

func (s *Server) handleListPresets(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.ListQualityPresets(r.Context())
	if err != nil {
		s.storeError(w, err, "quality presets")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleKinds(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, integrations.Kinds())
}

// ---- Libraries ----

var mediaTypes = map[string]bool{"tv": true, "movie": true, "music": true, "book": true, "audiobook": true}

func (s *Server) handleListLibraries(w http.ResponseWriter, r *http.Request) {
	l, err := s.store.ListLibraries(r.Context())
	if err != nil {
		s.storeError(w, err, "libraries")
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// validateLibrary checks the path exists inside the container, so a typo is
// caught when the library is added, not at the first import.
func validateLibrary(l *store.Library) error {
	l.Name = strings.TrimSpace(l.Name)
	l.Path = filepath.Clean(strings.TrimSpace(l.Path))
	if !mediaTypes[l.MediaType] {
		return errors.New("mediaType must be tv, movie, music, book or audiobook")
	}
	if !filepath.IsAbs(l.Path) {
		return errors.New("path must be absolute, e.g. /data/media/tv")
	}
	fi, err := os.Stat(l.Path)
	if err != nil {
		return errors.New("Bragi cannot see " + l.Path + ": check the folder exists and is mapped into the container")
	}
	if !fi.IsDir() {
		return errors.New(l.Path + " is a file, not a folder")
	}
	if l.Name == "" {
		l.Name = filepath.Base(l.Path)
	}
	return nil
}

func (s *Server) handleCreateLibrary(w http.ResponseWriter, r *http.Request) {
	var l store.Library
	if !readJSON(w, r, &l) {
		return
	}
	if err := validateLibrary(&l); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := s.store.CreateLibrary(r.Context(), l)
	if err != nil {
		s.storeError(w, err, "library")
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) handleUpdateLibrary(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var l store.Library
	if !readJSON(w, r, &l) {
		return
	}
	l.ID = id
	if err := validateLibrary(&l); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := s.store.UpdateLibrary(r.Context(), l)
	if err != nil {
		s.storeError(w, err, "library")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteLibrary(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteLibrary(r.Context(), id); err != nil {
		s.storeError(w, err, "library")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
