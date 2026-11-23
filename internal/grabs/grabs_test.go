package grabs

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/Tharkis/bragi/internal/integrations"
)

var errNoClient = errors.New("no client")

type cand struct {
	title    string
	protocol integrations.Protocol
	rejected string
	err      error
}

func describe(c cand) Release {
	return Release{Title: c.title, Protocol: c.protocol, Rejected: c.rejected}
}

func run(cands []cand) (Outcome, []string, []string) {
	var tried, blocked []string
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	o := Try(log, "x", cands, describe,
		func(c cand) error { tried = append(tried, c.title); return c.err },
		func(c cand, _ string) { blocked = append(blocked, c.title) }, errNoClient)
	return o, tried, blocked
}

func TestClientFailureStopsWithoutBlocklisting(t *testing.T) {
	clientDown := fmt.Errorf("qBit: %w", &integrations.UnreachableError{Host: "qbit:8080", Err: errors.New("connection refused")})
	o, tried, blocked := run([]cand{{title: "a", protocol: integrations.Usenet, err: clientDown}, {title: "b", protocol: integrations.Usenet}})
	if o.Grabbed || len(tried) != 1 || len(blocked) != 0 || o.ClientErr == nil {
		t.Fatalf("outcome %+v tried %v blocked %v", o, tried, blocked)
	}
	note, retry, ok := o.Note()
	if !ok || retry != ClientRetry || !strings.Contains(note, "could not reach qbit:8080") {
		t.Errorf("note %q retry %v", note, retry)
	}
}

func TestClientFailureFallsBackToOtherProtocol(t *testing.T) {
	clientDown := &integrations.UnreachableError{Host: "nzbget", Err: errors.New("connection refused")}
	o, tried, blocked := run([]cand{
		{title: "a", protocol: integrations.Usenet, err: clientDown},
		{title: "b", protocol: integrations.Usenet},
		{title: "c", protocol: integrations.Torrent},
	})
	if !o.Grabbed || strings.Join(tried, ",") != "a,c" || len(blocked) != 0 {
		t.Fatalf("outcome %+v tried %v blocked %v", o, tried, blocked)
	}
}

func TestUnauthorizedClientIsNotBlocklisted(t *testing.T) {
	o, _, blocked := run([]cand{{title: "a", err: integrations.ErrUnauthorized}, {title: "b"}})
	if len(blocked) != 0 || o.ClientErr == nil {
		t.Fatalf("blocked %v outcome %+v", blocked, o)
	}
}

func TestBadReleaseIsBlocklistedAndNextTried(t *testing.T) {
	bad := &integrations.ReleaseError{Err: errors.New("the indexer did not return a .torrent file")}
	o, tried, blocked := run([]cand{{title: "a", err: bad}, {title: "b"}})
	if !o.Grabbed || len(tried) != 2 || len(blocked) != 1 || blocked[0] != "a" {
		t.Fatalf("outcome %+v tried %v blocked %v", o, tried, blocked)
	}
}

func TestIndexerDownIsNotBlocklisted(t *testing.T) {
	down := &integrations.ReleaseError{Err: &integrations.StatusError{Host: "prowlarr", Code: 503}}
	o, tried, blocked := run([]cand{{title: "a", err: down}, {title: "r", rejected: "Different show"}})
	if o.Grabbed || len(tried) != 1 || len(blocked) != 0 || o.FetchErrs != 1 {
		t.Fatalf("outcome %+v tried %v blocked %v", o, tried, blocked)
	}
	if note, _, ok := o.Note(); !ok || !strings.Contains(note, "could not be downloaded from the indexer") {
		t.Errorf("note %q", note)
	}
}

func TestNoClientForProtocol(t *testing.T) {
	o, _, _ := run([]cand{{title: "a", protocol: integrations.Torrent, err: errNoClient}, {title: "b", protocol: integrations.Torrent, err: errNoClient}})
	note, retry, ok := o.Note()
	if !ok || retry != NoClientRetry || note != "Found releases, but no enabled download client handles torrent downloads" {
		t.Errorf("note %q retry %v", note, retry)
	}
}
