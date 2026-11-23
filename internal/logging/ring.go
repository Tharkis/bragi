package logging

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// Attr is one key=value pair of a log line, as text.
type Attr struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Entry is one log line kept for the Logs page.
type Entry struct {
	ID        int64     `json:"id"`
	Time      time.Time `json:"time"`
	Level     string    `json:"level"`
	Component string    `json:"component,omitempty"`
	Message   string    `json:"message"`
	Attrs     []Attr    `json:"attrs,omitempty"`
}

// Ring keeps the most recent entries.
type Ring struct {
	mu   sync.Mutex
	buf  []Entry
	next int   // where the next entry goes
	full bool  // buf has wrapped
	seq  int64 // ID of the last entry added
}

// NewRing returns a buffer of n entries.
func NewRing(n int) *Ring { return &Ring{buf: make([]Entry, n)} }

// Add stores e and gives it the next ID.
func (r *Ring) Add(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	e.ID = r.seq
	r.buf[r.next] = e
	r.next = (r.next + 1) % len(r.buf)
	if r.next == 0 {
		r.full = true
	}
}

// Filter picks entries for Query.
type Filter struct {
	// After returns only entries with a larger ID.
	After int64
	// MinLevel is "debug", "info", "warn" or "error"; "" means all.
	MinLevel string
	// Component keeps one component, e.g. "tv".
	Component string
	// Text keeps entries whose message or values contain it, any case.
	Text string
	// Limit caps how many of the newest matches come back.
	Limit int
}

var levelRank = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

// Query returns matching entries, oldest first, and the newest ID in the
// buffer so a poller can ask for what came after.
func (r *Ring) Query(f Filter) ([]Entry, int64) {
	r.mu.Lock()
	all := make([]Entry, 0, len(r.buf))
	if r.full {
		all = append(all, r.buf[r.next:]...)
	}
	all = append(all, r.buf[:r.next]...)
	last := r.seq
	r.mu.Unlock()

	min := levelRank[f.MinLevel]
	text := strings.ToLower(f.Text)
	out := make([]Entry, 0, 256)
	for _, e := range all {
		if e.ID <= f.After || levelRank[e.Level] < min {
			continue
		}
		if f.Component != "" && e.Component != f.Component {
			continue
		}
		if text != "" && !e.contains(text) {
			continue
		}
		out = append(out, e)
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[len(out)-f.Limit:]
	}
	return out, last
}

func (e Entry) contains(lower string) bool {
	if strings.Contains(strings.ToLower(e.Message), lower) || strings.Contains(e.Component, lower) {
		return true
	}
	for _, a := range e.Attrs {
		if strings.Contains(strings.ToLower(a.Value), lower) || strings.Contains(strings.ToLower(a.Key), lower) {
			return true
		}
	}
	return false
}

// Components lists the components seen in the buffer, sorted.
func (r *Ring) Components() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	for _, e := range r.buf {
		if e.Component != "" {
			seen[e.Component] = true
		}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sortStrings(out)
	return out
}

// Line formats e like the text on stdout.
func (e Entry) Line() string {
	var b strings.Builder
	b.WriteString(e.Time.Format("2006-01-02T15:04:05.000Z07:00"))
	b.WriteString(" " + strings.ToUpper(e.Level))
	if e.Component != "" {
		b.WriteString(" [" + e.Component + "]")
	}
	b.WriteString(" " + e.Message)
	for _, a := range e.Attrs {
		v := a.Value
		if v == "" || strings.ContainsAny(v, " \"=") {
			v = strconv.Quote(v)
		}
		b.WriteString(" " + a.Key + "=" + v)
	}
	return b.String()
}
