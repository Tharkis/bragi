package music

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/library"
	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/store"
)

// PollDownloads asks each download client about Bragi's music downloads,
// records progress, imports finished ones and retries failed ones.
func (s *Service) PollDownloads(ctx context.Context) error {
	active, err := s.Store.ActiveDownloadsOf(ctx, "music")
	if err != nil {
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
		if d.Status != j.State || d.Progress != j.Progress || d.Stage != j.Stage {
			s.Store.UpdateDownload(ctx, d.ID, j.State, j.Progress, j.OutputPath, j.Stage)
			s.publish("download", map[string]any{"id": d.ID, "albumId": d.MediaItemID, "progress": j.Progress})
		}
	case integrations.JobFailed:
		s.fail(ctx, d, j.Error, true)
		client.Remove(ctx, d.ClientJobID, true)
	case integrations.JobMissing:
		s.fail(ctx, d, "The download disappeared from the download client", false)
	case integrations.JobCompleted:
		d.OutputPath = j.OutputPath
		if err := s.importDownload(ctx, d, s.forced(d.ID)); err != nil {
			var wait waitError
			if errors.As(err, &wait) {
				if d.Status != "completed" || d.Error != wait.Error() {
					s.Store.UpdateDownload(ctx, d.ID, "completed", 1, j.OutputPath, wait.Error())
					s.publish("download", map[string]any{"id": d.ID, "albumId": d.MediaItemID})
				}
				return
			}
			s.fail(ctx, d, err.Error(), true)
			return
		}
		if d.Protocol != string(integrations.Torrent) {
			client.Remove(ctx, d.ClientJobID, false)
		}
	}
}

// waitError is an import problem that may fix itself or needs a decision.
type waitError struct{ msg string }

func (w waitError) Error() string { return w.msg }

// NeedsDecision starts the message of a download that waits for someone to
// import it anyway or remove it.
const NeedsDecision = "Needs a decision: "

func (s *Service) forced(id int64) bool {
	s.forceMu.Lock()
	defer s.forceMu.Unlock()
	return s.force[id]
}

// ImportAnyway imports a finished download whose files did not clearly
// match the album, as well as they match.
func (s *Service) ImportAnyway(ctx context.Context, downloadID int64) error {
	d, err := s.Store.GetDownload(ctx, downloadID)
	if err != nil {
		return err
	}
	if d.MediaType != "music" || d.Status != "completed" {
		return errors.New("only a finished music download waiting for a decision can be imported this way")
	}
	s.forceMu.Lock()
	s.force[downloadID] = true
	s.forceMu.Unlock()
	if err := s.importDownload(ctx, *d, true); err != nil {
		var wait waitError
		if errors.As(err, &wait) {
			return errors.New(wait.msg)
		}
		s.fail(ctx, *d, err.Error(), true)
		return err
	}
	// Clear the client's list as a normal import does.
	if dc, err := s.Store.GetIntegration(ctx, store.DownloadClients, d.DownloadClientID); err == nil && d.Protocol != string(integrations.Torrent) {
		if a, err := integrations.NewDownloadClient(dc.Kind, configOf(*dc), s.HC); err == nil {
			a.Remove(ctx, d.ClientJobID, false)
		}
	}
	return nil
}

// fail marks a download failed, puts the album back to wanted and, when
// the release itself was bad, blocklists it.
func (s *Service) fail(ctx context.Context, d store.Download, reason string, blocklist bool) {
	if reason == "" {
		reason = "Download failed"
	}
	s.Store.UpdateDownload(ctx, d.ID, "failed", d.Progress, d.OutputPath, reason)
	if blocklist {
		s.Store.Blocklist(ctx, d.MediaItemID, d.ReleaseTitle, reason)
	}
	if al, err := s.Store.GetAlbum(ctx, d.MediaItemID); err == nil && al.Status == "downloading" {
		status := "wanted"
		if al.Imported > 0 {
			status = "upgrade_wanted"
		}
		s.Store.SetAlbumState(ctx, al.ID, status, "Last download failed: "+reason, time.Now())
	}
	s.Log.Warn("download failed", "release", d.ReleaseTitle, "reason", reason)
	s.publish("download", map[string]any{"id": d.ID, "albumId": d.MediaItemID})
	s.publish("album", map[string]any{"id": d.MediaItemID})
	s.Kick()
}

// Cancel removes a download from its client, blocklists the release and
// puts the album back to wanted.
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

// ---- Matching files to tracks ----

type audioFile struct {
	path  string
	tags  FileTags
	disc  int
	track int
	title string
}

// match pairs files with an album's tracks: by disc and track number from
// the tags or the file names, then by title, then (if the counts agree) in
// order. sure is false when tracks are missing, files are left over, or
// the titles mostly disagree.
func match(files []audioFile, tracks []store.Track) (pairs map[int]int, sure bool, why string) {
	multi := false
	for _, t := range tracks {
		if t.Disc > 1 {
			multi = true
		}
	}
	key := func(disc, n int) int {
		if !multi || disc == 0 {
			disc = 1
		}
		return disc*1000 + n
	}
	byNum := map[int]int{}
	for i, t := range tracks {
		byNum[key(t.Disc, t.Number)] = i
	}
	pairs = map[int]int{} // file index → track index
	usedTrack := map[int]bool{}
	for fi, f := range files {
		if f.track == 0 {
			continue
		}
		if ti, ok := byNum[key(f.disc, f.track)]; ok && !usedTrack[ti] {
			pairs[fi], usedTrack[ti] = ti, true
		}
	}
	// By title for what numbers did not place.
	for fi, f := range files {
		if _, ok := pairs[fi]; ok || f.title == "" {
			continue
		}
		for ti, t := range tracks {
			if !usedTrack[ti] && sameTitle(f.title, t.Title) {
				pairs[fi], usedTrack[ti] = ti, true
				break
			}
		}
	}
	// In order, when exactly as many files as tracks are left.
	var freeFiles, freeTracks []int
	for fi := range files {
		if _, ok := pairs[fi]; !ok {
			freeFiles = append(freeFiles, fi)
		}
	}
	for ti := range tracks {
		if !usedTrack[ti] {
			freeTracks = append(freeTracks, ti)
		}
	}
	if len(freeFiles) == len(freeTracks) {
		for i := range freeFiles {
			pairs[freeFiles[i]] = freeTracks[i]
		}
		freeFiles, freeTracks = nil, nil
	}
	// How many matched pairs have titles that agree.
	titled, agree := 0, 0
	for fi, ti := range pairs {
		if files[fi].title == "" {
			continue
		}
		titled++
		if sameTitle(files[fi].title, tracks[ti].Title) {
			agree++
		}
	}
	switch {
	case len(freeTracks) > 0:
		return pairs, false, fmt.Sprintf("%d of %d tracks are missing from the download", len(freeTracks), len(tracks))
	case len(freeFiles) > 0:
		return pairs, false, fmt.Sprintf("the download has %d tracks, the album %d (another edition?)", len(files), len(tracks))
	case titled >= 3 && agree*10 < titled*6:
		return pairs, false, fmt.Sprintf("only %d of %d track titles match the album (another edition?)", agree, titled)
	}
	return pairs, true, ""
}

// sameTitle compares track titles loosely: one contains the other, or most
// words are shared.
func sameTitle(a, b string) bool {
	wa, wb := words(a), words(b)
	if len(wa) == 0 || len(wb) == 0 {
		return false
	}
	ja, jb := strings.Join(wa, " "), strings.Join(wb, " ")
	if strings.Contains(ja, jb) || strings.Contains(jb, ja) {
		return true
	}
	set := map[string]bool{}
	for _, w := range wa {
		set[w] = true
	}
	common := 0
	for _, w := range wb {
		if set[w] {
			common++
		}
	}
	return common*2 >= max(len(wa), len(wb))
}

// ---- Import ----

// importDownload moves (Usenet, Soulseek) or copies (torrents, which keep
// seeding untouched) an album's files into
// "Library/Artist/Album (Year)/[Disc N/]NN - Title.ext", writes tags and
// saves the cover. force imports a download whose files did not clearly
// match the album.
func (s *Service) importDownload(ctx context.Context, d store.Download, force bool) error {
	s.importMu.Lock()
	defer s.importMu.Unlock()

	if d.OutputPath == "" {
		return waitError{"The download client has not reported where the files are yet"}
	}
	info, err := os.Stat(d.OutputPath)
	if err != nil {
		return waitError{fmt.Sprintf("Bragi cannot see %s. Map the same /data folder into Bragi and the download client.", d.OutputPath)}
	}
	al, err := s.Store.GetAlbum(ctx, d.MediaItemID)
	if err != nil {
		return errors.New("the album was deleted from Bragi")
	}
	if err := s.EnsureTracks(ctx, al); err != nil {
		return waitError{"Could not load the track list from MusicBrainz yet"}
	}
	if al.Tracks > 0 && len(al.TrackList) == 0 {
		if al, err = s.Store.GetAlbum(ctx, d.MediaItemID); err != nil {
			return err
		}
	}
	ar, err := s.Store.GetArtist(ctx, al.ArtistID)
	if err != nil {
		return errors.New("the artist was deleted from Bragi")
	}
	lib, err := s.Store.GetLibrary(ctx, ar.LibraryID)
	if err != nil {
		return errors.New("the artist's library was removed")
	}
	root := d.OutputPath
	all := []string{d.OutputPath}
	if info.IsDir() {
		all = walkFiles(d.OutputPath)
	} else {
		root = filepath.Dir(d.OutputPath)
	}
	paths := bestAudio(all)
	if len(paths) == 0 {
		return errors.New("the download has no audio files")
	}
	multi := false
	discs := 1
	for _, t := range al.TrackList {
		if t.Disc > 1 {
			multi = true
		}
		discs = max(discs, t.Disc)
	}
	files := make([]audioFile, len(paths))
	for i, p := range paths {
		f := audioFile{path: p, tags: ReadTags(p)}
		f.disc, f.track, f.title = numbersFromName(p, multi)
		if f.tags.Track > 0 {
			f.track = f.tags.Track
		}
		if f.tags.Disc > 0 {
			f.disc = f.tags.Disc
		}
		if f.tags.Title != "" {
			f.title = f.tags.Title
		}
		files[i] = f
	}
	pairs, sure, why := match(files, al.TrackList)
	if !sure && !force {
		return waitError{NeedsDecision + why}
	}
	s.Store.UpdateDownload(ctx, d.ID, "importing", 1, d.OutputPath, "")

	folder := al.Folder
	if folder == "" {
		folder = AlbumFolder(al.Title, al.Year)
	}
	dir := filepath.Join(lib.Path, filepath.FromSlash(ar.Folder), filepath.FromSlash(folder))
	if strings.HasPrefix(folder, "/") {
		// An album found on disk outside the artist's folder stays put.
		dir = filepath.Join(lib.Path, filepath.FromSlash(folder))
	}
	quality := QualityOf(d.ReleaseTitle)
	if q := qualityOfFile(paths[0]); q != "" && (quality == "" || !strings.HasPrefix(quality, q)) {
		quality = q
	}
	// An upgrade replaces the old files.
	old := map[string]bool{}
	for _, t := range al.TrackList {
		if t.FilePath != "" {
			old[t.FilePath] = true
		}
	}
	if len(old) > 0 {
		s.Store.ClearAlbumFiles(ctx, al.ID)
	}
	type placed struct {
		track *store.Track
		dest  string
	}
	var done []placed
	for fi, f := range files {
		dest := ""
		var tr *store.Track
		if ti, ok := pairs[fi]; ok {
			tr = &al.TrackList[ti]
			name := fmt.Sprintf("%02d - %s%s", tr.Number, library.CleanName(tr.Title), strings.ToLower(filepath.Ext(f.path)))
			sub := ""
			if multi {
				sub = fmt.Sprintf("Disc %d", max(tr.Disc, 1))
			}
			dest = filepath.Join(dir, sub, name)
		} else {
			dest = filepath.Join(dir, library.CleanName(filepath.Base(f.path)))
		}
		// Torrents keep seeding, so their files are copied rather than
		// hardlinked: tagging must not change what is being seeded.
		if d.Protocol == string(integrations.Torrent) {
			err = library.CopyFile(f.path, dest)
		} else {
			err = library.Transfer(f.path, dest, false)
		}
		if err != nil {
			return fmt.Errorf("could not copy %s into the library: %w", filepath.Base(f.path), err)
		}
		delete(old, dest)
		done = append(done, placed{tr, dest})
	}
	for p := range old {
		os.Remove(p)
	}
	s.saveCover(ctx, all, dir, al.ForeignID, d.Protocol == string(integrations.Torrent))

	releaseID := al.ReleaseID
	for _, p := range done {
		if p.track == nil {
			continue
		}
		t := p.track
		if err := WriteTags(p.dest, TrackTags{Artist: firstNonEmpty(t.Artist, ar.Name), AlbumArtist: ar.Name, Album: al.Title, Title: t.Title,
			Year: al.Year, Disc: max(t.Disc, 1), Discs: discs, Track: t.Number, Tracks: countDisc(al.TrackList, t.Disc),
			ArtistID: ar.ForeignID, AlbumID: al.ForeignID, ReleaseID: releaseID, RecordingID: t.RecordingID}); err != nil && !errors.Is(err, ErrUntaggable) {
			s.Log.Debug("could not write tags", "file", p.dest, "err", err)
		}
		var size int64
		if st, err := os.Stat(p.dest); err == nil {
			size = st.Size()
		}
		if err := s.Store.AddFile(ctx, t.ID, p.dest, size, quality, d.ReleaseTitle); err != nil {
			return err
		}
	}
	s.Store.SetAlbumFolder(ctx, al.ID, folder)

	status, next := "imported", time.Time{}
	if s.upgrades(ctx) && rankOf(s.qualities(ctx, ar.LibraryID), quality) > 0 {
		status, next = "upgrade_wanted", time.Now().Add(UpgradeAfter)
	}
	s.Store.SetAlbumState(ctx, al.ID, status, "", next)
	if d.Protocol != string(integrations.Torrent) {
		removeEmptyDirs(root)
	}
	s.Store.UpdateDownload(ctx, d.ID, "imported", 1, d.OutputPath, "")
	s.forceMu.Lock()
	delete(s.force, d.ID)
	s.forceMu.Unlock()
	s.Log.Info("imported", "album", al.Artist+" - "+al.Title, "files", len(done), "quality", quality, "folder", dir)
	s.publish("download", map[string]any{"id": d.ID, "albumId": al.ID})
	s.publish("album", map[string]any{"id": al.ID, "artistId": al.ArtistID})
	if ok, err := s.Store.MarkMusicRequestsAvailable(ctx, al.ID); err == nil && ok {
		s.publish("request", map[string]any{"albumId": al.ID, "status": "available"})
	}
	if s.OnImported != nil {
		if err := s.OnImported(ctx); err != nil {
			s.Log.Warn("media server refresh failed", "err", err)
		}
	}
	return nil
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func countDisc(ts []store.Track, disc int) int {
	n := 0
	for _, t := range ts {
		if t.Disc == disc {
			n++
		}
	}
	return n
}

// qualityOfFile tells lossless files by their extension.
func qualityOfFile(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".flac", ".alac", ".wav", ".ape", ".wv":
		return "FLAC"
	case ".mp3":
		return "MP3"
	case ".m4a", ".aac":
		return "AAC"
	case ".ogg":
		return "OGG"
	case ".opus":
		return "OPUS"
	}
	return ""
}

// saveCover keeps the cover that came with the download, or fetches the
// album's cover from the Cover Art Archive, as cover.jpg.
func (s *Service) saveCover(ctx context.Context, files []string, dir, albumID string, keepSource bool) {
	dest := filepath.Join(dir, "cover.jpg")
	for _, f := range files {
		base := strings.ToLower(strings.TrimSuffix(filepath.Base(f), filepath.Ext(f)))
		ext := strings.ToLower(filepath.Ext(f))
		if (base == "cover" || base == "folder" || base == "front") && (ext == ".jpg" || ext == ".jpeg" || ext == ".png") {
			if ext == ".png" {
				dest = filepath.Join(dir, "cover.png")
			}
			if keepSource {
				library.CopyFile(f, dest)
			} else {
				library.Transfer(f, dest, false)
			}
			return
		}
	}
	if library.Exists(dest) {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, metadata.AlbumCoverURL(albumID, 1200), nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", metadata.UserAgent)
	resp, err := s.HC.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil || len(body) == 0 {
		return
	}
	os.MkdirAll(dir, 0o755)
	saveAtomically(dest, body)
}

// bestAudio returns the audio files of the best format present, so a
// download with both FLAC and MP3 copies imports only the FLAC.
func bestAudio(files []string) []string {
	byFormat := map[string][]string{}
	for _, f := range files {
		if integrations.IsAudioFile(f) {
			q := qualityOfFile(f)
			byFormat[q] = append(byFormat[q], f)
		}
	}
	for _, q := range []string{"FLAC", "MP3", "AAC", "OGG", "OPUS", ""} {
		if fs := byFormat[q]; len(fs) > 0 {
			sort.Strings(fs)
			return fs
		}
	}
	return nil
}

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
	os.Remove(dir)
}
