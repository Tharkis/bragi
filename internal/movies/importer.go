package movies

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tharkis/bragi/internal/downloads"
	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/library"
	"github.com/Tharkis/bragi/internal/store"
)

// PollDownloads asks each download client about Bragi's active movie
// downloads, records progress, imports finished ones and retries failed ones.
func (s *Service) PollDownloads(ctx context.Context) error {
	active, err := s.Store.ActiveDownloadsOf(ctx, "movie")
	if err != nil || len(active) == 0 {
		return err
	}
	byClient := map[int64][]store.Download{}
	for _, d := range active {
		byClient[d.DownloadClientID] = append(byClient[d.DownloadClientID], d)
	}
	for clientID, ds := range byClient {
		dc, err := s.Store.GetIntegration(ctx, store.DownloadClients, clientID)
		if err != nil {
			for _, d := range ds {
				s.fail(ctx, d, "The download client was removed from Bragi", false)
			}
			continue
		}
		a, err := integrations.NewDownloadClient(dc.Kind, configOf(*dc), s.HC)
		if err != nil {
			s.Log.Error("download client", "client", dc.Name, "err", err)
			continue
		}
		ids := make([]string, len(ds))
		for i, d := range ds {
			ids[i] = d.ClientJobID
		}
		jobs, err := a.Status(ctx, ids)
		if err != nil {
			s.Log.Warn("could not check downloads", "client", dc.Name, "err", err)
			continue
		}
		for i, j := range jobs {
			s.track(ctx, ds[i], j, a)
		}
	}
	return nil
}

func (s *Service) track(ctx context.Context, d store.Download, j integrations.Job, client integrations.DownloadClient) {
	switch j.State {
	case integrations.JobQueued, integrations.JobDownloading:
		if d.Status != j.State {
			s.Log.Info("download status", "release", d.ReleaseTitle, "from", d.Status, "to", j.State,
				"progress", fmt.Sprintf("%.0f%%", j.Progress*100), "stage", j.Stage)
		}
		if d.Status != j.State || d.Progress != j.Progress || d.Stage != j.Stage {
			s.Store.UpdateDownload(ctx, d.ID, j.State, j.Progress, j.OutputPath, j.Stage)
			s.publish("download", map[string]any{"id": d.ID, "movieId": d.MediaItemID, "progress": j.Progress})
		}
	case integrations.JobFailed:
		s.fail(ctx, d, j.Error, true)
		client.Remove(ctx, d.ClientJobID, true)
	case integrations.JobMissing:
		if downloads.JustAdded(d) {
			// Clients can take a moment to list a new job, magnets above all.
			s.Log.Debug("download not listed by the client yet", "release", d.ReleaseTitle, "job", d.ClientJobID)
			return
		}
		s.fail(ctx, d, "The download disappeared from the download client", false)
	case integrations.JobCompleted:
		if d.Status != "completed" {
			s.Log.Info("download finished, importing", "release", d.ReleaseTitle, "path", j.OutputPath)
		}
		d.OutputPath = j.OutputPath
		if err := s.importDownload(ctx, d); err != nil {
			var wait waitError
			if errors.As(err, &wait) {
				if d.Error != wait.Error() {
					s.Log.Warn("import waiting", "release", d.ReleaseTitle, "path", j.OutputPath, "reason", wait.Error())
				}
				s.Store.UpdateDownload(ctx, d.ID, "completed", 1, j.OutputPath, wait.Error())
				s.publish("download", map[string]any{"id": d.ID, "movieId": d.MediaItemID})
				return
			}
			s.fail(ctx, d, err.Error(), true)
			return
		}
		if d.Protocol == string(integrations.Usenet) {
			// The file was moved out; clear the job from the client's history.
			client.Remove(ctx, d.ClientJobID, false)
		}
	}
}

// waitError is an import problem that may fix itself, so the download stays
// completed and is retried.
type waitError struct{ msg string }

func (w waitError) Error() string { return w.msg }

// fail marks a download failed, puts the movie back to wanted and, when the
// release itself was bad, blocklists it so the next search skips it.
func (s *Service) fail(ctx context.Context, d store.Download, reason string, blocklist bool) {
	if reason == "" {
		reason = "Download failed"
	}
	s.Store.UpdateDownload(ctx, d.ID, "failed", d.Progress, d.OutputPath, reason)
	if blocklist {
		s.Store.Blocklist(ctx, d.MediaItemID, d.ReleaseTitle, reason)
	}
	if m, err := s.Store.GetMovie(ctx, d.MediaItemID); err == nil {
		status := "wanted"
		if m.FilePath != "" {
			status = "imported" // a failed upgrade keeps the file it had
		}
		next := time.Now()
		if status == "imported" || !m.Monitored {
			next = time.Time{}
		}
		s.Store.SetEpisodeState(ctx, []int64{m.ID}, status, "Last download failed: "+reason, next)
		s.publish("movie", map[string]any{"id": m.ID})
	}
	s.Log.Warn("download failed", "release", d.ReleaseTitle, "reason", reason)
	s.publish("download", map[string]any{"id": d.ID, "movieId": d.MediaItemID})
	s.Kick()
}

// mainVideo picks the movie out of a download: the biggest video file.
func mainVideo(files []string) string {
	best, size := "", int64(-1)
	for _, f := range files {
		if st, err := os.Stat(f); err == nil && st.Size() > size {
			best, size = f, st.Size()
		}
	}
	return best
}

// movieFileName builds "Movie (2024) [WEBDL-1080p].mkv".
func movieFileName(m *store.Movie, quality, src string) string {
	name := m.Folder
	if quality != "" {
		name += " [" + quality + "]"
	}
	return name + strings.ToLower(filepath.Ext(src))
}

// importDownload moves (Usenet) or hardlinks (torrents, which keep seeding)
// the movie file into its folder with a clean name.
func (s *Service) importDownload(ctx context.Context, d store.Download) error {
	s.importMu.Lock()
	defer s.importMu.Unlock()

	if d.OutputPath == "" {
		return waitError{"The download client has not reported where the files are yet"}
	}
	info, err := os.Stat(d.OutputPath)
	if err != nil {
		return waitError{fmt.Sprintf("Bragi cannot see %s. Map the same /data folder into Bragi and the download client.", d.OutputPath)}
	}
	m, err := s.Store.GetMovie(ctx, d.MediaItemID)
	if err != nil {
		return errors.New("the movie was deleted from Bragi")
	}
	lib, err := s.Store.GetLibrary(ctx, m.LibraryID)
	if err != nil {
		return errors.New("the movie's library was removed")
	}
	src := d.OutputPath
	if info.IsDir() {
		src = mainVideo(library.VideoFiles(d.OutputPath))
	}
	if src == "" {
		return errors.New("the download has no video files")
	}
	s.Store.UpdateDownload(ctx, d.ID, "importing", 1, d.OutputPath, "")

	dest := filepath.Join(lib.Path, m.Folder, movieFileName(m, d.Quality, src))
	if err := library.Transfer(src, dest, d.Protocol == string(integrations.Torrent)); err != nil {
		return fmt.Errorf("could not copy %s into the library: %w", filepath.Base(src), err)
	}
	var size int64
	if st, err := os.Stat(dest); err == nil {
		size = st.Size()
	}
	// An upgrade replaces the old file.
	if m.FilePath != "" && m.FilePath != dest {
		os.Remove(m.FilePath)
	}
	if err := s.Store.AddFile(ctx, m.ID, dest, size, d.Quality, d.ReleaseTitle); err != nil {
		return err
	}
	s.Log.Info("imported", "file", dest)
	s.Store.UpdateDownload(ctx, d.ID, "imported", 1, d.OutputPath, "")
	s.publish("download", map[string]any{"id": d.ID, "movieId": m.ID})
	s.publish("movie", map[string]any{"id": m.ID})

	if done, err := s.Store.MarkMovieRequestsAvailable(ctx, m.ID); err == nil && done {
		s.publish("request", map[string]any{"movieId": m.ID, "status": "available"})
	}
	if s.RefreshJellyfin != nil {
		if err := s.RefreshJellyfin(ctx); err != nil {
			s.Log.Warn("Jellyfin library refresh failed", "err", err)
		}
	}
	return nil
}

// Cancel removes a download from its client, blocklists the release and
// puts the movie back to wanted, so the next search picks another one.
func (s *Service) Cancel(ctx context.Context, downloadID int64) error {
	d, err := s.Store.GetDownload(ctx, downloadID)
	if err != nil {
		return err
	}
	if dc, err := s.Store.GetIntegration(ctx, store.DownloadClients, d.DownloadClientID); err == nil {
		if a, err := integrations.NewDownloadClient(dc.Kind, configOf(*dc), s.HC); err == nil {
			if err := a.Remove(ctx, d.ClientJobID, true); err != nil {
				s.Log.Warn("could not remove the download from the client", "client", dc.Name, "err", err)
			}
		}
	}
	s.fail(ctx, *d, "Removed by you", true)
	return nil
}
