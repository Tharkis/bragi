package integrations

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
)

// mapped rewrites the paths a download client reports when it sees its
// download folder under another path than Bragi does, e.g. NZBGet in its
// own container with /downloads mounted where Bragi has /data/usenet.
type mapped struct {
	DownloadClient
	from, to string
}

// cleanDir normalizes a folder for prefix matching: forward slashes and no
// trailing slash. Windows paths keep their drive letter.
func cleanDir(p string) string {
	p = strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return p
}

// MapPath rewrites p when it lies under from. Windows paths match without
// regard to case, as Windows does.
func MapPath(p, from, to string) string {
	if p == "" || from == "" || to == "" {
		return p
	}
	from, to = cleanDir(from), cleanDir(to)
	norm := strings.ReplaceAll(p, `\`, "/")
	windows := strings.Contains(p, `\`) || (len(from) > 1 && from[1] == ':')
	head := norm
	if len(head) > len(from) {
		head = head[:len(from)]
	}
	same := head == from
	if windows {
		same = strings.EqualFold(head, from)
	}
	if !same {
		return p
	}
	rest := norm[len(head):]
	if rest != "" && rest[0] != '/' && from != "/" {
		return p // "/downloads2" is not under "/downloads"
	}
	return path.Join(to, rest)
}

func (m *mapped) Status(ctx context.Context, jobIDs []string) ([]Job, error) {
	jobs, err := m.DownloadClient.Status(ctx, jobIDs)
	for i := range jobs {
		jobs[i].OutputPath = MapPath(jobs[i].OutputPath, m.from, m.to)
	}
	return jobs, err
}

// Check runs the client's own checks and also makes sure Bragi can see
// the mapped folder.
func (m *mapped) Check(ctx context.Context) ([]string, error) {
	var warnings []string
	var err error
	if c, ok := m.DownloadClient.(Checker); ok {
		warnings, err = c.Check(ctx)
	} else {
		err = m.DownloadClient.Test(ctx)
	}
	if err != nil {
		return nil, err
	}
	if st, statErr := os.Stat(m.to); statErr != nil || !st.IsDir() {
		warnings = append(warnings, fmt.Sprintf("Bragi cannot see the folder %s. Check the path mapping and the container's volumes.", m.to))
	}
	return warnings, nil
}
