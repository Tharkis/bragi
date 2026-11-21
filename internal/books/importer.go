package books

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/library"
	"github.com/Tharkis/bragi/internal/store"
)

// PollDownloads asks each download client about Bragi's book downloads,
// records progress, imports finished ones and retries failed ones.
func (s *Service) PollDownloads(ctx context.Context) error {
	var active []store.Download
	for _, t := range []string{"book", "audiobook"} {
		ds, err := s.Store.ActiveDownloadsOf(ctx, t)
		if err != nil {
			return err
		}
		active = append(active, ds...)
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
			s.publish("download", map[string]any{"id": d.ID, "bookId": d.MediaItemID, "progress": j.Progress})
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
				s.Store.UpdateDownload(ctx, d.ID, "completed", 1, j.OutputPath, wait.Error())
				s.publish("download", map[string]any{"id": d.ID, "bookId": d.MediaItemID})
				return
			}
			s.fail(ctx, d, err.Error(), true)
			return
		}
		// Usenet and Soulseek files were moved out; clear the client's list.
		// Torrents stay to seed.
		if d.Protocol != string(integrations.Torrent) {
			client.Remove(ctx, d.ClientJobID, false)
		}
	}
}

// waitError is an import problem that may fix itself.
type waitError struct{ msg string }

func (w waitError) Error() string { return w.msg }

// fail marks a download failed, puts the book back to wanted and, when the
// release itself was bad, blocklists it.
func (s *Service) fail(ctx context.Context, d store.Download, reason string, blocklist bool) {
	if reason == "" {
		reason = "Download failed"
	}
	s.Store.UpdateDownload(ctx, d.ID, "failed", d.Progress, d.OutputPath, reason)
	if blocklist {
		s.Store.Blocklist(ctx, d.MediaItemID, d.ReleaseTitle, reason)
	}
	if b, err := s.Store.GetBook(ctx, d.MediaItemID); err == nil && b.Status != "imported" {
		status := "wanted"
		if len(b.Files) > 0 {
			status = "imported"
		}
		s.Store.SetBookState(ctx, b.ID, status, "Last download failed: "+reason, time.Now())
	}
	s.Log.Warn("download failed", "release", d.ReleaseTitle, "reason", reason)
	s.publish("download", map[string]any{"id": d.ID, "bookId": d.MediaItemID})
	s.publish("book", map[string]any{"id": d.MediaItemID})
	s.Kick()
}

// Cancel removes a download from its client, blocklists the release and
// puts the book back to wanted.
func (s *Service) Cancel(ctx context.Context, d *store.Download) error {
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

// importDownload moves (Usenet, Soulseek) or hardlinks (torrents) the book's
// files into "Library/Author/Title (Year)/".
//
// An ebook keeps one file, the best format in the download, named
// "Title - Author.epub". An audiobook keeps every audio file; a single
// file is named "Title.m4b", several keep their names (prefixed with their
// disc folder when the download has one) so players sort them in order.
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
	b, err := s.Store.GetBook(ctx, d.MediaItemID)
	if err != nil {
		return errors.New("the book was deleted from Bragi")
	}
	lib, err := s.Store.GetLibrary(ctx, b.LibraryID)
	if err != nil {
		return errors.New("the book's library was removed")
	}
	root := d.OutputPath
	files := []string{d.OutputPath}
	if info.IsDir() {
		files = walkFiles(d.OutputPath)
	} else {
		root = filepath.Dir(d.OutputPath)
	}
	s.Store.UpdateDownload(ctx, d.ID, "importing", 1, d.OutputPath, "")

	dir := filepath.Join(lib.Path, filepath.FromSlash(b.Folder))
	var plan []move // source → destination
	if b.MediaType == "book" {
		best := pickEbook(files)
		if best == "" {
			return errors.New("the download has no ebook files")
		}
		name := library.CleanName(b.Title)
		if b.Author != "" {
			name += " - " + library.CleanName(b.Author)
		}
		plan = append(plan, move{best, filepath.Join(dir, name+strings.ToLower(filepath.Ext(best)))})
	} else {
		audio := audioFiles(files)
		if len(audio) == 0 {
			return errors.New("the download has no audio files")
		}
		if len(audio) == 1 {
			plan = append(plan, move{audio[0], filepath.Join(dir, library.CleanName(b.Title)+strings.ToLower(filepath.Ext(audio[0])))})
		} else {
			for _, f := range audio {
				plan = append(plan, move{f, filepath.Join(dir, trackName(root, f))})
			}
		}
		// Keep the cover art that came with it.
		if c := coverImage(files); c != "" {
			plan = append(plan, move{c, filepath.Join(dir, "cover"+strings.ToLower(filepath.Ext(c)))})
		}
	}

	keep := d.Protocol == string(integrations.Torrent)
	var recorded []store.BookFile
	newPaths := map[string]bool{}
	for _, m := range plan {
		if err := library.Transfer(m.src, m.dest, keep); err != nil {
			return fmt.Errorf("could not copy %s into the library: %w", filepath.Base(m.src), err)
		}
		newPaths[m.dest] = true
		if isCover(m.dest) {
			continue
		}
		var size int64
		if st, err := os.Stat(m.dest); err == nil {
			size = st.Size()
		}
		recorded = append(recorded, store.BookFile{Path: m.dest, SizeBytes: size, Format: FormatOf(m.dest)})
	}
	// An upgrade replaces the old files.
	for _, f := range b.Files {
		if !newPaths[f.Path] {
			os.Remove(f.Path)
		}
	}
	if err := s.Store.ReplaceBookFiles(ctx, b.ID, recorded, d.ReleaseTitle); err != nil {
		return err
	}
	if !keep {
		// Tidy the folders the move emptied.
		removeEmptyDirs(root)
	}
	s.Store.UpdateDownload(ctx, d.ID, "imported", 1, d.OutputPath, "")
	s.Log.Info("imported", "book", b.Title, "files", len(recorded), "folder", dir)
	s.publish("download", map[string]any{"id": d.ID, "bookId": b.ID})
	s.publish("book", map[string]any{"id": b.ID})
	if done, err := s.Store.MarkBookRequestsAvailable(ctx, b.ID); err == nil && done {
		s.publish("request", map[string]any{"bookId": b.ID, "status": "available"})
	}
	if s.OnImported != nil {
		if err := s.OnImported(ctx); err != nil {
			s.Log.Warn("Jellyfin library refresh failed", "err", err)
		}
	}
	return nil
}

type move struct{ src, dest string }

func walkFiles(dir string) []string {
	var out []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// pickEbook returns the best-format ebook file, the largest of that format.
func pickEbook(files []string) string {
	best, bestRank, bestSize := "", len(ebookOrder)+1, int64(-1)
	for _, f := range files {
		if !integrations.IsEbookFile(f) {
			continue
		}
		r := orderOf(ebookOrder, FormatOf(f))
		var size int64
		if st, err := os.Stat(f); err == nil {
			size = st.Size()
		}
		if r < bestRank || r == bestRank && size > bestSize {
			best, bestRank, bestSize = f, r, size
		}
	}
	return best
}

// audioFiles returns the audio files of the best format present, so a
// download with both an .m4b and its .mp3 parts keeps only the .m4b.
func audioFiles(files []string) []string {
	byFormat := map[string][]string{}
	for _, f := range files {
		if integrations.IsAudioFile(f) {
			fm := FormatOf(f)
			byFormat[fm] = append(byFormat[fm], f)
		}
	}
	best, bestRank := "", len(audioOrder)+1
	for fm := range byFormat {
		if r := orderOf(audioOrder, fm); r < bestRank {
			best, bestRank = fm, r
		}
	}
	out := byFormat[best]
	sort.Strings(out)
	return out
}

// trackName keeps a part's file name, prefixed with its sub-folder ("CD1 -
// 01 Chapter.mp3") so parts from different discs do not collide.
func trackName(root, f string) string {
	rel, err := filepath.Rel(root, f)
	if err != nil {
		return library.CleanName(filepath.Base(f))
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for i := range parts {
		parts[i] = library.CleanName(parts[i])
	}
	return strings.Join(parts, " - ")
}

// removeEmptyDirs deletes dir and its sub-folders if they hold no files.
func removeEmptyDirs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			removeEmptyDirs(filepath.Join(dir, e.Name()))
		}
	}
	os.Remove(dir) // fails, harmlessly, if anything is left
}

func isCover(p string) bool {
	base := strings.ToLower(strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
	ext := strings.ToLower(filepath.Ext(p))
	return (base == "cover" || base == "folder") && (ext == ".jpg" || ext == ".jpeg" || ext == ".png")
}

func coverImage(files []string) string {
	for _, f := range files {
		if isCover(f) {
			return f
		}
	}
	return ""
}
