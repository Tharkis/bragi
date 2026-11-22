package music

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/bogem/id3v2/v2"
	"github.com/dhowden/tag"
	"github.com/go-flac/flacvorbis"
	flac "github.com/go-flac/go-flac"
)

// FileTags is what Bragi reads from an audio file's own tags.
type FileTags struct {
	Artist      string
	AlbumArtist string
	Album       string
	Title       string
	Year        int
	Disc        int
	Track       int
	// MusicBrainz ids, from files tagged by Picard, beets or Lidarr.
	AlbumID   string // release group
	ReleaseID string
	ArtistID  string // album artist
}

// ReadTags reads a file's tags. A file without tags gives zero values.
func ReadTags(path string) FileTags {
	f, err := os.Open(path)
	if err != nil {
		return FileTags{}
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil {
		return FileTags{}
	}
	t := FileTags{Artist: m.Artist(), AlbumArtist: m.AlbumArtist(), Album: m.Album(), Title: m.Title(), Year: m.Year()}
	t.Track, _ = m.Track()
	t.Disc, _ = m.Disc()
	for k, v := range m.Raw() {
		var key, val string
		switch x := v.(type) {
		case *tag.Comm: // ID3 TXXX
			key, val = x.Description, x.Text
		case string:
			key, val = k, x
		case []string:
			if len(x) > 0 {
				key, val = k, x[0]
			}
		default:
			continue
		}
		key = strings.ToLower(strings.TrimPrefix(key, "----:com.apple.iTunes:"))
		key = strings.ReplaceAll(key, "_", " ")
		val = strings.TrimSpace(strings.SplitN(val, "\x00", 2)[0])
		switch key {
		case "musicbrainz release group id", "musicbrainz releasegroupid":
			t.AlbumID = val
		case "musicbrainz album id", "musicbrainz albumid":
			t.ReleaseID = val
		case "musicbrainz album artist id", "musicbrainz albumartistid":
			t.ArtistID = strings.SplitN(val, "/", 2)[0]
		}
	}
	return t
}

// TrackTags is what Bragi writes into an imported file.
type TrackTags struct {
	Artist      string // the track's artist
	AlbumArtist string
	Album       string
	Title       string
	Year        int
	Disc, Discs int
	Track       int
	Tracks      int
	ArtistID    string
	AlbumID     string
	ReleaseID   string
	RecordingID string
}

// ErrUntaggable means Bragi cannot write tags into that kind of file.
var ErrUntaggable = errors.New("Bragi writes tags into MP3 and FLAC files only")

// WriteTags sets the core tags and MusicBrainz ids, leaving the file's
// other tags alone.
func WriteTags(path string, t TrackTags) (err error) {
	// The tag libraries can panic on damaged files; a file Bragi cannot
	// tag is still imported.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("could not tag %s: %v", filepath.Base(path), r)
		}
	}()
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3":
		return writeID3(path, t)
	case ".flac":
		return writeVorbis(path, t)
	}
	return ErrUntaggable
}

func (t TrackTags) pairs() [][2]string {
	p := [][2]string{
		{"ARTIST", t.Artist}, {"ALBUMARTIST", t.AlbumArtist}, {"ALBUM", t.Album}, {"TITLE", t.Title},
		{"TRACKNUMBER", strconv.Itoa(t.Track)}, {"TRACKTOTAL", strconv.Itoa(t.Tracks)},
		{"DISCNUMBER", strconv.Itoa(t.Disc)}, {"DISCTOTAL", strconv.Itoa(t.Discs)},
		{"MUSICBRAINZ_ALBUMARTISTID", t.ArtistID}, {"MUSICBRAINZ_RELEASEGROUPID", t.AlbumID},
		{"MUSICBRAINZ_ALBUMID", t.ReleaseID}, {"MUSICBRAINZ_TRACKID", t.RecordingID},
	}
	if t.Year > 0 {
		p = append(p, [2]string{"DATE", strconv.Itoa(t.Year)})
	}
	return p
}

func writeVorbis(path string, t TrackTags) error {
	f, err := flac.ParseFile(path)
	if err != nil {
		return err
	}
	var cmt *flacvorbis.MetaDataBlockVorbisComment
	idx := -1
	for i, m := range f.Meta {
		if m.Type == flac.VorbisComment {
			if cmt, err = flacvorbis.ParseFromMetaDataBlock(*m); err != nil {
				return err
			}
			idx = i
			break
		}
	}
	if cmt == nil {
		cmt = flacvorbis.New()
	}
	set := map[string]string{}
	for _, kv := range t.pairs() {
		if kv[1] != "" && kv[1] != "0" {
			set[kv[0]] = kv[1]
		}
	}
	kept := cmt.Comments[:0]
	for _, c := range cmt.Comments {
		k, _, _ := strings.Cut(c, "=")
		if _, replaced := set[strings.ToUpper(k)]; !replaced {
			kept = append(kept, c)
		}
	}
	cmt.Comments = kept
	for _, kv := range t.pairs() {
		if v := set[kv[0]]; v != "" {
			cmt.Add(kv[0], v)
		}
	}
	block := cmt.Marshal()
	if idx >= 0 {
		f.Meta[idx] = &block
	} else {
		f.Meta = append(f.Meta, &block)
	}
	return saveAtomically(path, f.Marshal())
}

func writeID3(path string, t TrackTags) error {
	tg, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		return err
	}
	defer tg.Close()
	tg.SetDefaultEncoding(id3v2.EncodingUTF8)
	tg.SetVersion(4)
	set := func(id, v string) {
		if v != "" && v != "0" && !strings.HasPrefix(v, "0/") {
			tg.AddTextFrame(id, id3v2.EncodingUTF8, v)
		}
	}
	set("TPE1", t.Artist)
	set("TPE2", t.AlbumArtist)
	set("TALB", t.Album)
	set("TIT2", t.Title)
	set("TRCK", strconv.Itoa(t.Track)+"/"+strconv.Itoa(t.Tracks))
	set("TPOS", strconv.Itoa(t.Disc)+"/"+strconv.Itoa(t.Discs))
	if t.Year > 0 {
		set("TDRC", strconv.Itoa(t.Year))
	}
	for desc, v := range map[string]string{"MusicBrainz Album Artist Id": t.ArtistID, "MusicBrainz Release Group Id": t.AlbumID,
		"MusicBrainz Album Id": t.ReleaseID} {
		if v != "" {
			tg.AddUserDefinedTextFrame(id3v2.UserDefinedTextFrame{Encoding: id3v2.EncodingUTF8, Description: desc, Value: v})
		}
	}
	if t.RecordingID != "" {
		tg.AddUFIDFrame(id3v2.UFIDFrame{OwnerIdentifier: "http://musicbrainz.org", Identifier: []byte(t.RecordingID)})
	}
	return tg.Save()
}

// saveAtomically writes data next to path and renames it into place, so a
// crash never leaves half a file.
func saveAtomically(path string, data []byte) error {
	tmp := path + ".bragi-tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

var (
	reDiscTrack = regexp.MustCompile(`^(\d{1,2})[-._ ](\d{1,2})\b`)
	reLeadNum   = regexp.MustCompile(`^(\d{1,3})\b`)
	reDiscDir   = regexp.MustCompile(`(?i)^(cd|dis[ck])[\s._-]*(\d+)\b`)
	reNumPrefix = regexp.MustCompile(`^[\d\s._-]+`)
)

// numbersFromName reads a disc and track number from a file name like
// "1-03 Title.flac", "103 Title.mp3" (with several discs) or "03. Title",
// and the disc from a "CD2" or "Disc 2" folder.
func numbersFromName(path string, multiDisc bool) (disc, track int, title string) {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if m := reDiscDir.FindStringSubmatch(filepath.Base(filepath.Dir(path))); m != nil {
		disc, _ = strconv.Atoi(m[2])
	}
	switch m := reDiscTrack.FindStringSubmatch(base); {
	case m != nil && multiDisc:
		d, _ := strconv.Atoi(m[1])
		track, _ = strconv.Atoi(m[2])
		if disc == 0 {
			disc = d
		}
	default:
		if m := reLeadNum.FindStringSubmatch(base); m != nil {
			n, _ := strconv.Atoi(m[1])
			if n >= 100 && multiDisc {
				if disc == 0 {
					disc = n / 100
				}
				n %= 100
			}
			track = n
		}
	}
	title = strings.TrimSpace(reNumPrefix.ReplaceAllString(base, ""))
	// "Artist - Title" names carry the artist first.
	if _, after, ok := strings.Cut(title, " - "); ok {
		title = after
	}
	return disc, track, title
}
