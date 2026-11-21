// Package library holds the file-system helpers every media type shares
// when it imports downloads: safe names, finding video files, and moving or
// hardlinking a file into place.
package library

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Tharkis/bragi/internal/release"
)

var reBadPath = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

// CleanName makes a title safe as a file or folder name on every OS.
func CleanName(s string) string {
	s = strings.ReplaceAll(s, ": ", " - ")
	s = reBadPath.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimRight(s, ". ")
}

// VideoFiles lists the video files under dir, skipping samples and extras.
func VideoFiles(dir string) []string {
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

// Exists reports whether path exists.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Transfer places src at dest. With keepSource (torrents, which keep
// seeding) the file is hardlinked; otherwise (Usenet) it is moved. Either
// falls back to a copy when the paths are on different file systems.
func Transfer(src, dest string, keepSource bool) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o775); err != nil {
		return err
	}
	if Exists(dest) {
		if err := os.Remove(dest); err != nil {
			return err
		}
	}
	if keepSource {
		if err := os.Link(src, dest); err == nil {
			return nil
		}
		return CopyFile(src, dest)
	}
	if err := os.Rename(src, dest); err == nil {
		return nil
	}
	if err := CopyFile(src, dest); err != nil {
		return err
	}
	return os.Remove(src)
}

// CopyFile copies src to dest through a temporary file, so a half-written
// copy never appears under the final name.
func CopyFile(src, dest string) error {
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

// Folders lists the visible sub-folders of dir by name.
func Folders(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}
