package tv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/release"
	"github.com/Tharkis/bragi/internal/store"
)

// PollDownloads asks each download client about Bragi's active downloads,
// records progress, imports finished ones and retries failed ones.
func (s *Service) PollDownloads(ctx context.Context) error {
	active, err := s.Store.ActiveDownloads(ctx)
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
		if d.Status != j.State || d.Progress != j.Progress {
			s.Store.UpdateDownload(ctx, d.ID, j.State, j.Progress, j.OutputPath, "")
			s.publish("download", map[string]any{"id": d.ID, "seriesId": d.SeriesID, "progress": j.Progress})
		}
	case integrations.JobFailed:
		s.fail(ctx, d, j.Error, true)
		client.Remove(ctx, d.ClientJobID, true)
	case integrations.JobMissing:
		s.fail(ctx, d, "The download disappeared from the download client", false)
	case integrations.JobCompleted:
		d.OutputPath = j.OutputPath
		if err := s.importDownload(ctx, d); err != nil {
			var wait waitError
			if errors.As(err, &wait) {
				// Not ready (for example the folder is not visible yet): try again next poll.
				s.Store.UpdateDownload(ctx, d.ID, "completed", 1, j.OutputPath, wait.Error())
				s.publish("download", map[string]any{"id": d.ID, "seriesId": d.SeriesID})
				return
			}
			s.fail(ctx, d, err.Error(), true)
			return
		}
		if d.Protocol == string(integrations.Usenet) {
			// The files were moved out; clear the job from NZBGet's history.
			client.Remove(ctx, d.ClientJobID, false)
		}
	}
}

// waitError is an import problem that may fix itself, so the download stays
// completed and is retried.
type waitError struct{ msg string }

func (w waitError) Error() string { return w.msg }

// fail marks a download failed, puts its episodes back to wanted and, when
// the release itself was bad, blocklists it so the next search skips it.
func (s *Service) fail(ctx context.Context, d store.Download, reason string, blocklist bool) {
	if reason == "" {
		reason = "Download failed"
	}
	s.Store.UpdateDownload(ctx, d.ID, "failed", d.Progress, d.OutputPath, reason)
	if blocklist {
		s.Store.Blocklist(ctx, d.MediaItemID, d.ReleaseTitle, reason)
	}
	eps, _ := s.episodesFor(ctx, d)
	var ids []int64
	for _, e := range eps {
		if e.Status != "imported" {
			ids = append(ids, e.ID)
		}
	}
	s.Store.SetEpisodeState(ctx, ids, "wanted", "Last download failed: "+reason, time.Now())
	s.Log.Warn("download failed", "release", d.ReleaseTitle, "reason", reason)
	s.publish("download", map[string]any{"id": d.ID, "seriesId": d.SeriesID})
	s.Kick()
}

// episodesFor returns the episodes a download covers: one episode, or a
// whole season.
func (s *Service) episodesFor(ctx context.Context, d store.Download) ([]store.Episode, error) {
	if d.SeriesID == 0 {
		return nil, errors.New("the show was deleted")
	}
	all, err := s.Store.EpisodesBySeries(ctx, d.SeriesID)
	if err != nil {
		return nil, err
	}
	var out []store.Episode
	for _, e := range all {
		if e.ID == d.MediaItemID || e.SeasonID == d.MediaItemID {
			out = append(out, e)
		}
	}
	return out, nil
}

// importDownload moves (Usenet) or hardlinks (torrents, which keep seeding)
// the finished video files into the show's folder with clean names.
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
	sr, err := s.Store.GetSeries(ctx, d.SeriesID)
	if err != nil {
		return errors.New("the show was deleted from Bragi")
	}
	lib, err := s.Store.GetLibrary(ctx, sr.LibraryID)
	if err != nil {
		return errors.New("the show's library was removed")
	}
	all, err := s.Store.EpisodesBySeries(ctx, sr.ID)
	if err != nil {
		return err
	}
	wanted, err := s.episodesFor(ctx, d)
	if err != nil {
		return err
	}

	files := []string{d.OutputPath}
	if info.IsDir() {
		files = videoFiles(d.OutputPath)
	}
	if len(files) == 0 {
		return errors.New("the download has no video files")
	}
	s.Store.UpdateDownload(ctx, d.ID, "importing", 1, d.OutputPath, "")

	matches := matchFiles(files, all, wanted)
	if len(matches) == 0 {
		return errors.New("none of the downloaded files match the wanted episodes")
	}
	q := d.Quality
	imported := 0
	for _, m := range matches {
		dest := filepath.Join(lib.Path, sr.Folder, seasonFolder(m.eps[0].Season), episodeFileName(sr, m.eps, q, m.path))
		if err := s.transfer(m.path, dest, d.Protocol == string(integrations.Torrent)); err != nil {
			return fmt.Errorf("could not copy %s into the library: %w", filepath.Base(m.path), err)
		}
		st, _ := os.Stat(dest)
		var size int64
		if st != nil {
			size = st.Size()
		}
		for _, e := range m.eps {
			// An upgrade replaces the old file.
			if e.FilePath != "" && e.FilePath != dest {
				os.Remove(e.FilePath)
			}
			if err := s.Store.AddFile(ctx, e.ID, dest, size, q, d.ReleaseTitle); err != nil {
				return err
			}
			imported++
		}
		s.Log.Info("imported", "file", dest)
	}
	s.Store.UpdateDownload(ctx, d.ID, "imported", 1, d.OutputPath, "")
	s.publish("download", map[string]any{"id": d.ID, "seriesId": sr.ID})
	s.publish("episodes", map[string]any{"seriesId": sr.ID})

	if done, err := s.Store.MarkRequestsAvailable(ctx, sr.ID); err == nil && done {
		s.publish("request", map[string]any{"seriesId": sr.ID, "status": "available"})
	}
	if err := s.RefreshJellyfin(ctx); err != nil {
		s.Log.Warn("Jellyfin library refresh failed", "err", err)
	}
	return nil
}

type fileMatch struct {
	path string
	eps  []store.Episode
}

// matchFiles pairs video files with episodes by the SxxEyy in their names.
// A lone file in a single-episode download is that episode even if its name
// does not say so.
func matchFiles(files []string, all, wanted []store.Episode) []fileMatch {
	type se struct{ s, e int }
	idx := map[se]store.Episode{}
	for _, e := range all {
		idx[se{e.Season, e.Number}] = e
	}
	wantedIDs := map[int64]bool{}
	for _, e := range wanted {
		wantedIDs[e.ID] = true
	}
	var out []fileMatch
	used := map[int64]bool{}
	for _, f := range files {
		info := release.Parse(filepath.Base(f))
		if len(info.Episodes) == 0 {
			// Folder names often carry the episode when file names do not.
			info = release.Parse(filepath.Base(filepath.Dir(f)))
		}
		var eps []store.Episode
		for _, n := range info.Episodes {
			if e, ok := idx[se{info.Season, n}]; ok && wantedIDs[e.ID] && !used[e.ID] {
				eps = append(eps, e)
			}
		}
		if len(eps) == 0 {
			continue
		}
		for _, e := range eps {
			used[e.ID] = true
		}
		out = append(out, fileMatch{path: f, eps: eps})
	}
	if len(out) == 0 && len(files) == 1 && len(wanted) == 1 {
		out = append(out, fileMatch{path: files[0], eps: wanted})
	}
	return out
}

// videoFiles lists the video files under dir, skipping samples and extras.
func videoFiles(dir string) []string {
	var out []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !release.IsVideo(p) {
			return nil
		}
		lower := strings.ToLower(p)
		if strings.Contains(lower, "sample") || strings.Contains(lower, "/extras/") || strings.Contains(lower, "/featurettes/") {
			if fi, err := d.Info(); err == nil && fi.Size() < 300<<20 {
				return nil
			}
		}
		out = append(out, p)
		return nil
	})
	sort.Strings(out)
	return out
}

func seasonFolder(n int) string {
	if n == 0 {
		return "Specials"
	}
	return fmt.Sprintf("Season %02d", n)
}

// episodeFileName builds "Show (2022) - S01E02 - Title [WEBDL-1080p].mkv".
func episodeFileName(sr *store.Series, eps []store.Episode, quality, src string) string {
	code := fmt.Sprintf("S%02dE%02d", eps[0].Season, eps[0].Number)
	if len(eps) > 1 {
		code += fmt.Sprintf("-E%02d", eps[len(eps)-1].Number)
	}
	name := sr.Folder + " - " + code
	if t := cleanName(eps[0].Title); t != "" && !strings.HasPrefix(strings.ToLower(t), "episode ") {
		if len(t) > 80 {
			t = t[:80]
		}
		name += " - " + t
	}
	if quality != "" {
		name += " [" + quality + "]"
	}
	return name + strings.ToLower(filepath.Ext(src))
}

// transfer places src at dest. Torrents are hardlinked so they keep
// seeding; Usenet downloads are moved. Either falls back to a copy when the
// paths are on different file systems.
func (s *Service) transfer(src, dest string, keepSource bool) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o775); err != nil {
		return err
	}
	if fileExists(dest) {
		if err := os.Remove(dest); err != nil {
			return err
		}
	}
	if keepSource {
		if err := os.Link(src, dest); err == nil {
			return nil
		}
		return copyFile(src, dest)
	}
	if err := os.Rename(src, dest); err == nil {
		return nil
	}
	if err := copyFile(src, dest); err != nil {
		return err
	}
	return os.Remove(src)
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dest + ".bragi-partial"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}

// Cancel removes a download from its client, blocklists the release and
// puts its episodes back to wanted, so the next search picks another one.
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
