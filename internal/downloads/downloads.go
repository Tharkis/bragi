// Package downloads holds small rules shared by every pipeline's download
// tracking.
package downloads

import (
	"time"

	"github.com/Tharkis/bragi/internal/store"
)

// MissingGrace is how long a new download may be absent from its client's
// list before Bragi decides it is gone. Clients can take a moment to list a
// job, magnet links above all.
var MissingGrace = 3 * time.Minute

// JustAdded reports whether d was sent to its client within MissingGrace.
func JustAdded(d store.Download) bool {
	t, err := time.Parse("2006-01-02T15:04:05Z", d.CreatedAt)
	return err == nil && time.Since(t) < MissingGrace
}
