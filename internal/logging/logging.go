// Package logging sets up Bragi's logs: plain text lines on stdout (what
// Unraid's docker log shows), a rotating file under the config folder that
// people can send in, and an in-memory buffer that feeds the Logs page.
// Secrets such as API keys and passwords are blanked out of every line.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Defaults for the log file and the in-memory buffer.
const (
	FileName     = "bragi.log"
	MaxFileBytes = 5 << 20
	KeepFiles    = 5
	BufferSize   = 5000
)

// Manager owns the log outputs and the live level.
type Manager struct {
	// Dir holds bragi.log and its rotated copies. Empty means no file.
	Dir string
	// EnvLevel is the level BRAGI_LOG_LEVEL asked for, used when no level
	// is saved in Settings.
	EnvLevel slog.Level

	level slog.LevelVar
	ring  *Ring
	file  *rotatingFile
	out   io.Writer
}

// New builds a Manager that writes to out (usually stdout) and, when dir is
// not empty, to dir/bragi.log.
func New(out io.Writer, dir string, level slog.Level) (*Manager, error) {
	m := &Manager{Dir: dir, EnvLevel: level, ring: NewRing(BufferSize), out: out}
	m.level.Set(level)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create log folder: %w", err)
		}
		f, err := openRotating(filepath.Join(dir, FileName), MaxFileBytes, KeepFiles)
		if err != nil {
			return nil, err
		}
		m.file = f
		m.out = io.MultiWriter(out, f)
	}
	return m, nil
}

// Logger returns the root logger.
func (m *Manager) Logger() *slog.Logger {
	text := slog.NewTextHandler(m.out, &slog.HandlerOptions{Level: slog.LevelDebug, ReplaceAttr: replaceAttr})
	return slog.New(&handler{m: m, text: text})
}

// Level returns the level in effect.
func (m *Manager) Level() slog.Level { return m.level.Level() }

// SetLevel changes the level right away.
func (m *Manager) SetLevel(l slog.Level) { m.level.Set(l) }

// Ring returns the in-memory buffer.
func (m *Manager) Ring() *Ring { return m.ring }

// Files returns the log files on disk, oldest first, so they read in order
// when joined.
func (m *Manager) Files() []string {
	if m.Dir == "" {
		return nil
	}
	base := filepath.Join(m.Dir, FileName)
	var out []string
	for i := KeepFiles - 1; i >= 1; i-- {
		p := fmt.Sprintf("%s.%d", base, i)
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	if _, err := os.Stat(base); err == nil {
		out = append(out, base)
	}
	return out
}

// Close closes the log file.
func (m *Manager) Close() error {
	if m.file != nil {
		return m.file.Close()
	}
	return nil
}

// ParseLevel reads "debug", "info", "warn" or "error".
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("log level must be debug, info, warn or error, got %q", s)
}

// LevelName is the lower-case name ParseLevel reads.
func LevelName(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "debug"
	case l < slog.LevelWarn:
		return "info"
	case l < slog.LevelError:
		return "warn"
	}
	return "error"
}

// ---- handler ----

type handler struct {
	m      *Manager
	text   slog.Handler
	attrs  []Attr // attributes from With, already flattened and redacted
	prefix string // group prefix from WithGroup
}

func (h *handler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.m.level.Level() }

func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	n := *h
	n.attrs = slices.Clone(h.attrs)
	red := make([]slog.Attr, 0, len(as))
	for _, a := range as {
		a = redactAttr(a)
		red = append(red, a)
		n.attrs = flatten(n.attrs, h.prefix, a)
	}
	n.text = h.text.WithAttrs(red)
	return &n
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	n := *h
	n.prefix = h.prefix + name + "."
	n.text = h.text.WithGroup(name)
	return &n
}

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, Redact(r.Message), r.PC)
	attrs := slices.Clone(h.attrs)
	r.Attrs(func(a slog.Attr) bool {
		a = redactAttr(a)
		out.AddAttrs(a)
		attrs = flatten(attrs, h.prefix, a)
		return true
	})
	e := Entry{Time: r.Time, Level: LevelName(r.Level), Message: out.Message}
	for _, a := range attrs {
		if a.Key == "component" && e.Component == "" {
			e.Component = a.Value
			continue
		}
		e.Attrs = append(e.Attrs, a)
	}
	h.m.ring.Add(e)
	return h.text.Handle(ctx, out)
}

func flatten(dst []Attr, prefix string, a slog.Attr) []Attr {
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		p := prefix
		if a.Key != "" {
			p += a.Key + "."
		}
		for _, g := range v.Group() {
			dst = flatten(dst, p, g)
		}
		return dst
	}
	if a.Key == "" {
		return dst
	}
	s := v.String()
	if v.Kind() == slog.KindDuration {
		s = v.Duration().Round(time.Millisecond).String()
	}
	return append(dst, Attr{Key: prefix + a.Key, Value: s})
}

// replaceAttr shortens durations in the text output.
func replaceAttr(_ []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() == slog.KindDuration {
		return slog.String(a.Key, a.Value.Duration().Round(time.Millisecond).String())
	}
	return a
}

// ---- redaction ----

var (
	// key=value in URLs and messages, e.g. "apikey=abc123".
	reSecretParam = regexp.MustCompile(`(?i)\b(api_?key|apikey|x-api-key|password|passwd|pass|token|access_token|secret|session|sid)=([^&\s"',;]+)`)
	// user:password@ in URLs.
	reURLUser = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)([^/\s:@]+):([^/\s@]+)@`)
	// JSON-ish "password":"...".
	reSecretJSON = regexp.MustCompile(`(?i)("(?:api_?key|apikey|password|token|secret)"\s*:\s*")([^"]*)(")`)
)

// Redact blanks API keys, passwords and tokens out of s.
func Redact(s string) string {
	if s == "" {
		return s
	}
	s = reSecretParam.ReplaceAllString(s, "$1=***")
	s = reURLUser.ReplaceAllString(s, "$1$2:***@")
	s = reSecretJSON.ReplaceAllString(s, "$1***$3")
	return s
}

func secretKey(k string) bool {
	k = strings.ToLower(k)
	for _, s := range []string{"password", "apikey", "api_key", "token", "secret"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

func redactAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch {
	case v.Kind() == slog.KindGroup:
		g := v.Group()
		out := make([]any, len(g))
		for i, x := range g {
			out[i] = redactAttr(x)
		}
		return slog.Group(a.Key, out...)
	case secretKey(a.Key):
		return slog.String(a.Key, "***")
	case v.Kind() == slog.KindString:
		return slog.String(a.Key, Redact(v.String()))
	case v.Kind() == slog.KindAny:
		if err, ok := v.Any().(error); ok {
			return slog.String(a.Key, Redact(err.Error()))
		}
		if st, ok := v.Any().(fmt.Stringer); ok {
			return slog.String(a.Key, Redact(st.String()))
		}
	}
	return slog.Attr{Key: a.Key, Value: v}
}
