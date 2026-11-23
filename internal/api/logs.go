package api

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Tharkis/bragi/internal/logging"
)

// SettingLogLevel holds the log level chosen under Settings. Empty means
// the BRAGI_LOG_LEVEL default.
const SettingLogLevel = "log_level"

type logsResponse struct {
	Entries    []logging.Entry `json:"entries"`
	Last       int64           `json:"last"`
	Level      string          `json:"level"`
	EnvLevel   string          `json:"envLevel"`
	Components []string        `json:"components"`
	File       string          `json:"file,omitempty"`
}

// handleLogs returns recent log lines: ?after=<id>&level=&component=&q=&limit=
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > logging.BufferSize {
		limit = 1000
	}
	entries, last := s.logs.Ring().Query(logging.Filter{After: after, MinLevel: q.Get("level"),
		Component: q.Get("component"), Text: strings.TrimSpace(q.Get("q")), Limit: limit})
	out := logsResponse{Entries: entries, Last: last, Level: logging.LevelName(s.logs.Level()),
		EnvLevel: logging.LevelName(s.logs.EnvLevel), Components: s.logs.Ring().Components()}
	if s.logs.Dir != "" {
		out.File = filepath.Join(s.logs.Dir, logging.FileName)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleDownloadLogs sends the log files as one zip.
func (s *Server) handleDownloadLogs(w http.ResponseWriter, r *http.Request) {
	name := "bragi-logs-" + time.Now().Format("2006-01-02-1504") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	zw := zip.NewWriter(w)
	defer zw.Close()

	info, _ := zw.Create("about.txt")
	fmt.Fprintf(info, "Bragi %s\nCreated %s\nLog level %s (BRAGI_LOG_LEVEL %s)\n", s.version,
		time.Now().Format(time.RFC3339), logging.LevelName(s.logs.Level()), logging.LevelName(s.logs.EnvLevel))

	files := s.logs.Files()
	if len(files) == 0 {
		// No log folder (tests, or it could not be created): send the buffer.
		f, _ := zw.Create(logging.FileName)
		entries, _ := s.logs.Ring().Query(logging.Filter{})
		for _, e := range entries {
			fmt.Fprintln(f, e.Line())
		}
		return
	}
	for _, p := range files {
		src, err := os.Open(p)
		if err != nil {
			continue
		}
		dst, err := zw.Create(filepath.Base(p))
		if err == nil {
			io.Copy(dst, src)
		}
		src.Close()
	}
}

// applyLogLevel sets the live level from a saved setting value.
func (s *Server) applyLogLevel(v string) error {
	if v == "" {
		s.logs.SetLevel(s.logs.EnvLevel)
		s.log.Info("log level reset to the BRAGI_LOG_LEVEL default", "level", logging.LevelName(s.logs.EnvLevel))
		return nil
	}
	l, err := logging.ParseLevel(v)
	if err != nil {
		return err
	}
	s.logs.SetLevel(l)
	s.log.Info("log level changed", "level", logging.LevelName(l))
	return nil
}
