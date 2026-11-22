package music

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/metadata"
	"github.com/Tharkis/bragi/internal/scan"
	"github.com/Tharkis/bragi/internal/store"
)

// Adopting music already on disk.
//
// Music libraries are laid out every which way: Lidarr and beets write
// "Artist/Album (Year)/", Picard users pick their own patterns, and
// Navidrome does not care at all, since it goes by the files' tags. So a
// scan treats every folder holding audio files as one unit (with "CD1" and
// "Disc 2" folders belonging to the folder above), groups its files by
// their album tags, and only falls back to the folder names when there are
// no tags. Files tagged by Picard, beets or Lidarr carry MusicBrainz ids
// and match directly; the rest are looked up by artist and album name.

// Units lists the album folders in a library.
func (s *Service) Units(ctx context.Context, lib *store.Library) ([]string, error) {
	if _, err := os.Stat(lib.Path); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var units []string
	filepath.WalkDir(lib.Path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") && p != lib.Path {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !integrations.IsAudioFile(p) {
			return nil
		}
		dir := filepath.Dir(p)
		if reDiscDir.MatchString(filepath.Base(dir)) && dir != lib.Path {
			dir = filepath.Dir(dir)
		}
		rel, _ := filepath.Rel(lib.Path, dir)
		rel = filepath.ToSlash(rel)
		if !seen[rel] {
			seen[rel] = true
			units = append(units, rel)
		}
		return nil
	})
	sort.Strings(units)
	return units, nil
}

// unitFiles lists a unit's audio files: those in the folder and in its
// disc folders.
func unitFiles(dir string) []string {
	var out []string
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		switch {
		case strings.HasPrefix(e.Name(), "."):
		case e.IsDir() && reDiscDir.MatchString(e.Name()):
			sub, _ := os.ReadDir(p)
			for _, f := range sub {
				if !f.IsDir() && integrations.IsAudioFile(f.Name()) {
					out = append(out, filepath.Join(p, f.Name()))
				}
			}
		case !e.IsDir() && integrations.IsAudioFile(e.Name()):
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

var reFolderYear = regexp.MustCompile(`\s*[\(\[]((19|20)\d\d)[\)\]]\s*`)

// guessFromPath reads "Artist/Album (Year)" or "Artist - Album (Year)"
// from a unit's folder names.
func guessFromPath(unit string) (artist, album string, year int) {
	parts := strings.Split(unit, "/")
	album = parts[len(parts)-1]
	if m := reFolderYear.FindStringSubmatch(album); m != nil {
		year, _ = strconv.Atoi(m[1])
		album = strings.TrimSpace(reFolderYear.ReplaceAllString(album, " "))
	}
	if a, b, ok := strings.Cut(album, " - "); ok {
		return strings.TrimSpace(a), strings.TrimSpace(b), year
	}
	if len(parts) > 1 {
		artist = parts[len(parts)-2]
	}
	return artist, album, year
}

type albumGroup struct {
	files  []audioFile
	artist string
	album  string
	year   int
	// MusicBrainz ids from the tags.
	albumID, releaseID, artistID string
}

// groupFiles splits a unit's files by album, using their tags.
func groupFiles(paths []string, unit string) []*albumGroup {
	byKey := map[string]*albumGroup{}
	var order []string
	pathArtist, pathAlbum, pathYear := guessFromPath(unit)
	for _, p := range paths {
		t := ReadTags(p)
		f := audioFile{path: p, tags: t}
		f.disc, f.track, f.title = numbersFromName(p, true)
		if t.Track > 0 {
			f.track = t.Track
		}
		if t.Disc > 0 {
			f.disc = t.Disc
		}
		if t.Title != "" {
			f.title = t.Title
		}
		artist := firstNonEmpty(t.AlbumArtist, t.Artist)
		key := strings.ToLower(artist + "|" + t.Album)
		switch {
		case t.AlbumID != "":
			key = t.AlbumID
		case t.ReleaseID != "":
			key = t.ReleaseID
		case t.Album == "":
			key = "|path"
		}
		g := byKey[key]
		if g == nil {
			g = &albumGroup{artist: artist, album: t.Album, year: t.Year, albumID: t.AlbumID, releaseID: t.ReleaseID, artistID: t.ArtistID}
			if g.album == "" {
				g.artist, g.album, g.year = firstNonEmpty(artist, pathArtist), pathAlbum, pathYear
			}
			if g.year == 0 {
				g.year = pathYear
			}
			byKey[key] = g
			order = append(order, key)
		}
		g.files = append(g.files, f)
	}
	out := make([]*albumGroup, len(order))
	for i, k := range order {
		out[i] = byKey[k]
	}
	return out
}

// Adopt matches the albums in one folder and links their files. With
// albumID set (a MusicBrainz release group) it skips matching.
func (s *Service) Adopt(ctx context.Context, lib *store.Library, unit, albumID string) (scan.Result, error) {
	dir := filepath.Join(lib.Path, filepath.FromSlash(unit))
	paths := unitFiles(dir)
	if len(paths) == 0 {
		return scan.Result{}, &scan.NoMatchError{Reason: "No audio files"}
	}
	// Already adopted and nothing new: skip the lookups.
	if albumID == "" {
		known := 0
		for _, p := range paths {
			if id, err := s.Store.FileOwner(ctx, p); err == nil && id != 0 {
				known++
			}
		}
		if known == len(paths) {
			return scan.Result{Name: unit}, nil
		}
	}
	groups := groupFiles(paths, unit)
	if albumID != "" {
		// A hand-picked album covers the whole folder.
		merged := &albumGroup{albumID: albumID}
		for _, g := range groups {
			merged.files = append(merged.files, g.files...)
		}
		groups = []*albumGroup{merged}
	}
	var res scan.Result
	var firstErr error
	matched := 0
	for _, g := range groups {
		r, err := s.adoptGroup(ctx, lib, unit, g)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		matched++
		res.Files += r.Files
		res.New = res.New || r.New
		if res.Name == "" {
			res.Name = r.Name
		} else {
			res.Name += ", " + r.Name
		}
	}
	if matched == 0 {
		return res, firstErr
	}
	return res, nil
}

func (s *Service) adoptGroup(ctx context.Context, lib *store.Library, unit string, g *albumGroup) (scan.Result, error) {
	mb := s.MB()
	guess := strings.Trim(g.artist+" - "+g.album, " -")
	id := g.albumID
	if id == "" && g.releaseID != "" {
		if rel, err := mb.Release(ctx, g.releaseID); err == nil {
			id = rel.AlbumID
		}
	}
	if id == "" {
		if g.album == "" {
			return scan.Result{}, &scan.NoMatchError{Reason: "No album name in the tags or the folder name", Guess: guess}
		}
		found, err := mb.FindAlbum(ctx, g.artist, g.album)
		if err != nil {
			return scan.Result{}, err
		}
		id = pickAlbum(found, g)
		if id == "" {
			return scan.Result{}, &scan.NoMatchError{Reason: "No match on MusicBrainz", Guess: guess}
		}
	}
	info, err := mb.Album(ctx, id)
	if errors.Is(err, metadata.ErrNoSuchMusic) {
		return scan.Result{}, &scan.NoMatchError{Reason: "MusicBrainz has no such album", Guess: guess}
	}
	if err != nil {
		return scan.Result{}, err
	}
	if info.ArtistID == "" {
		return scan.Result{}, &scan.NoMatchError{Reason: "MusicBrainz does not say whose album this is", Guess: guess}
	}
	before, _ := s.Store.GetArtistByForeignID(ctx, info.ArtistID)
	ar, err := s.AddArtist(ctx, info.ArtistID, AddOptions{Monitor: MonitorNone, LibraryID: lib.ID})
	if err != nil {
		if errors.Is(err, metadata.ErrNoSuchMusic) {
			return scan.Result{}, &scan.NoMatchError{Reason: "MusicBrainz has no such artist", Guess: guess}
		}
		return scan.Result{}, err
	}
	al, err := s.Store.GetAlbumByForeignID(ctx, id)
	if err != nil {
		aid, _, err := s.Store.UpsertAlbum(ctx, ar.ID, store.AlbumInput{ForeignID: info.ID, Title: info.Title, Year: info.Year,
			ReleaseDate: info.ReleaseDate, Type: info.Type, Folder: AlbumFolder(info.Title, info.Year)})
		if err != nil {
			return scan.Result{}, err
		}
		if al, err = s.Store.GetAlbum(ctx, aid); err != nil {
			return scan.Result{}, err
		}
	}
	if err := s.EnsureTracks(ctx, al); err != nil {
		return scan.Result{}, err
	}
	if al, err = s.Store.GetAlbum(ctx, al.ID); err != nil {
		return scan.Result{}, err
	}
	// The artist's folder follows the first album found on disk, so new
	// downloads land next to the albums already there.
	if before == nil && strings.Contains(unit, "/") {
		parent := unit[:strings.LastIndex(unit, "/")]
		s.Store.SetArtistFolder(ctx, ar.ID, parent)
		ar.Folder = parent
	}
	folder := "/" + unit
	if rest, ok := strings.CutPrefix(unit, ar.Folder+"/"); ok {
		folder = rest
	}
	s.Store.SetAlbumFolder(ctx, al.ID, folder)

	wasImported := al.Status == "imported" || al.Status == "upgrade_wanted"
	pairs, _, _ := match(g.files, al.TrackList)
	linked := 0
	for fi, ti := range pairs {
		f := g.files[fi]
		t := al.TrackList[ti]
		if owner, _ := s.Store.FileOwner(ctx, f.path); owner == t.ID {
			continue
		}
		if t.FilePath != "" && t.FilePath != f.path && fileExists(t.FilePath) {
			continue // that track has a file elsewhere already
		}
		var size int64
		if st, err := os.Stat(f.path); err == nil {
			size = st.Size()
		}
		if err := s.Store.AddFile(ctx, t.ID, f.path, size, qualityOfFile(f.path), ""); err != nil {
			return scan.Result{}, err
		}
		linked++
	}
	if al, err = s.Store.GetAlbum(ctx, al.ID); err == nil {
		switch {
		case al.Tracks > 0 && al.Imported == al.Tracks:
			if al.Status != "imported" && al.Status != "upgrade_wanted" {
				s.Store.SetAlbumState(ctx, al.ID, "imported", "", timeZero)
			}
		case al.Imported > 0 && al.Status != "downloading":
			s.Store.SetAlbumState(ctx, al.ID, "wanted", fmt.Sprintf("%d of %d tracks on disk", al.Imported, al.Tracks), timeZero)
		}
		s.Store.MarkMusicRequestsAvailable(ctx, al.ID)
	}
	s.publish("album", map[string]any{"id": al.ID, "artistId": ar.ID})
	return scan.Result{Name: ar.Name + " - " + info.Title, Files: linked, New: !wasImported && linked > 0}, nil
}

// pickAlbum takes the search result whose title and artist agree with
// what was read from the files, preferring the same year.
func pickAlbum(found []metadata.Album, g *albumGroup) string {
	best, bestScore := "", 0
	for _, a := range found {
		if !sameTitle(a.Title, g.album) || g.artist != "" && !sameTitle(a.Artist, g.artist) {
			continue
		}
		score := a.Score
		if g.year > 0 && a.Year == g.year {
			score += 20
		}
		if a.Type == "Album" {
			score += 5
		}
		if score > bestScore {
			best, bestScore = a.ID, score
		}
	}
	return best
}

var timeZero time.Time

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
