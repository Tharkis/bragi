// Package grabs holds the step every pipeline shares after a search: try
// the accepted releases best first until a download client takes one, and
// explain what went wrong when none was sent.
package grabs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Tharkis/bragi/internal/integrations"
	"github.com/Tharkis/bragi/internal/release"
)

// Retry delays after a failed grab. Variables so tests can shorten them.
var (
	ClientRetry   = 15 * time.Minute // a download client failed or was unreachable
	NoClientRetry = 1 * time.Hour    // no client handles the releases found
)

// Release is what Try needs to know about a candidate.
type Release struct {
	Title    string
	Protocol integrations.Protocol
	Rejected string
}

// Outcome sums up a grab attempt.
type Outcome struct {
	Grabbed bool
	// ClientErr is set when a download client failed. Later releases of
	// the same protocol would hit the same problem, so Try skips them.
	ClientErr error
	// NoClient lists protocols with releases but no enabled client.
	NoClient []string
	// FetchErrs counts releases whose file could not be fetched or that
	// the client refused; LastFetchErr is the last such error.
	FetchErrs    int
	LastFetchErr error
}

// Try sends the accepted candidates to grab, best first, until one works.
// A release that is bad in itself is blocklisted; a failing download client
// is not the release's fault, so nothing is blocklisted for it.
// errNoClient is the pipeline's "no client for this protocol" error.
// blocklist is called for releases that are bad in themselves.
func Try[C any](log *slog.Logger, what string, cands []C, describe func(C) Release, grab func(C) error,
	blocklist func(C, string), errNoClient error) Outcome {
	var o Outcome
	failed := map[integrations.Protocol]bool{} // protocols whose client failed
	for _, c := range cands {
		r := describe(c)
		if r.Rejected != "" {
			break
		}
		if failed[r.Protocol] {
			continue
		}
		err := grab(c)
		if err == nil {
			o.Grabbed = true
			return o
		}
		switch {
		case errors.Is(err, errNoClient):
			p := string(r.Protocol)
			if !slices.Contains(o.NoClient, p) {
				o.NoClient = append(o.NoClient, p)
				log.Warn("no enabled download client handles this kind of release", "for", what, "protocol", p, "release", r.Title)
			}
		case integrations.IsReleaseError(err):
			o.FetchErrs++
			o.LastFetchErr = err
			block := integrations.ShouldBlocklist(err)
			log.Warn("grab failed, trying the next release", "for", what, "release", r.Title, "err", err, "blocklisted", block)
			if block {
				blocklist(c, err.Error())
			}
		default:
			// The client for this protocol is the problem: skip its other
			// releases, but a client for another protocol may still work.
			o.ClientErr = err
			failed[r.Protocol] = true
			log.Error("download client failed", "for", what, "release", r.Title, "protocol", string(r.Protocol), "err", err)
		}
	}
	return o
}

// Note explains why nothing was grabbed, with the delay before the next
// search, when the grab step knows more than the search results do. ok is
// false when the search results tell the story (nothing suitable found).
// A zero retry means the pipeline's usual delay.
func (o Outcome) Note() (note string, retry time.Duration, ok bool) {
	switch {
	case o.Grabbed:
		return "", 0, false
	case o.ClientErr != nil:
		return "Could not send to the download client: " + o.ClientErr.Error(), ClientRetry, true
	case o.FetchErrs > 0:
		return fmt.Sprintf("%d suitable %s could not be downloaded from the indexer (%v)", o.FetchErrs,
			plural(o.FetchErrs, "release", "releases"), o.LastFetchErr), 0, true
	case len(o.NoClient) > 0:
		return "Found releases, but no enabled download client handles " + strings.Join(o.NoClient, " or ") + " downloads",
			NoClientRetry, true
	}
	return "", 0, false
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// LogResults logs a search's outcome: counts and the rejection reasons at
// info, and every release with its verdict at debug. detail adds fields
// to the debug line, such as quality and size.
func LogResults[C any](log *slog.Logger, what string, sources int, cands []C, describe func(C) Release, detail func(C) []any) {
	accepted := 0
	reasons := map[string]int{}
	best := ""
	for _, c := range cands {
		r := describe(c)
		if r.Rejected == "" {
			if accepted == 0 {
				best = r.Title
			}
			accepted++
			continue
		}
		reasons[shortReason(r.Rejected)]++
	}
	args := []any{"for", what, "sources", sources, "results", len(cands), "accepted", accepted}
	if len(reasons) > 0 {
		args = append(args, "rejected", summarize(reasons))
	}
	if best != "" {
		args = append(args, "best", best)
	}
	log.Info("search finished", args...)
	if !log.Enabled(context.Background(), slog.LevelDebug) {
		return
	}
	for _, c := range cands {
		r := describe(c)
		verdict := r.Rejected
		if verdict == "" {
			verdict = "accepted"
		}
		a := []any{"for", what, "title", r.Title, "verdict", verdict, "protocol", string(r.Protocol)}
		if detail != nil {
			a = append(a, detail(c)...)
		}
		log.Debug("release", a...)
	}
}

func shortReason(r string) string {
	if i := strings.Index(r, " ("); i > 0 {
		r = r[:i]
	}
	if strings.HasSuffix(r, "is not in the quality preset") {
		r = "quality not in preset"
	}
	return strings.ToLower(r)
}

func summarize(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return counts[keys[i]] > counts[keys[j]] || counts[keys[i]] == counts[keys[j]] && keys[i] < keys[j]
	})
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%d %s", counts[k], k)
	}
	return strings.Join(parts, ", ")
}

// Video describes a TV or movie candidate.
func Video(c release.Candidate) Release {
	return Release{Title: c.Title, Protocol: c.Protocol, Rejected: c.Rejected}
}

// VideoDetail is the debug detail for a TV or movie candidate.
func VideoDetail(c release.Candidate) []any {
	return []any{"quality", c.Quality, "size", release.HumanSize(c.SizeBytes), "seeders", c.Seeders, "indexer", c.Indexer}
}
