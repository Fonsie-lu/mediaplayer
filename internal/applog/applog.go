// Package applog is an in-memory, parsed log sink.
//
// When the TUI is active the standard library logger is redirected here
// (instead of stderr, which would corrupt the terminal UI). Each written line
// is parsed for a "[session <id>]" tag and an "opened path=<file>" association
// so the Logs tab can group entries by session and filename.
package applog

import (
	"bytes"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Entry is one parsed log line.
type Entry struct {
	Time    time.Time
	Session string // session id, or "" for non-session lines
	File    string // associated filename, or "" if none known
	Msg     string // the message text (timestamp prefix stripped)
}

// Store is a bounded ring buffer of parsed log entries. It implements io.Writer
// so it can be installed via log.SetOutput. All access is mutex-guarded.
type Store struct {
	mu      sync.Mutex
	entries []Entry
	max     int
	files   map[string]string // session id -> last-known filename
	partial []byte            // buffer for a not-yet-newline-terminated write
	version uint64            // bumped on every appended entry
}

// Default is the process-wide store used by main when redirecting the logger.
var Default = New(5000)

// New returns a Store retaining up to max entries.
func New(max int) *Store {
	if max <= 0 {
		max = 1000
	}
	return &Store{max: max, files: map[string]string{}}
}

var (
	reSession = regexp.MustCompile(`^\[session ([^\]]+)\]\s*(.*)$`)
	reOpened  = regexp.MustCompile(`opened path=(.+?) dur=`)
)

// Write implements io.Writer. The standard logger emits one full line per call,
// but partial writes are tolerated: text is buffered until a newline arrives.
func (s *Store) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partial = append(s.partial, p...)
	for {
		i := bytes.IndexByte(s.partial, '\n')
		if i < 0 {
			break
		}
		line := string(s.partial[:i])
		s.partial = s.partial[i+1:]
		s.appendLine(line)
	}
	return len(p), nil
}

// appendLine parses one line and appends it. Caller holds mu.
func (s *Store) appendLine(line string) {
	line = strings.TrimRight(line, "\r")
	if strings.TrimSpace(line) == "" {
		return
	}
	e := Entry{Time: time.Now(), Msg: line}
	if m := reSession.FindStringSubmatch(line); m != nil {
		e.Session = m[1]
		e.Msg = m[2]
		if om := reOpened.FindStringSubmatch(line); om != nil {
			s.files[e.Session] = om[1]
		}
		e.File = s.files[e.Session]
	}
	s.entries = append(s.entries, e)
	if len(s.entries) > s.max {
		s.entries = s.entries[len(s.entries)-s.max:]
	}
	s.version++
}

// Version returns a counter bumped on each new entry; the TUI polls it to learn
// when a rebuild is needed.
func (s *Store) Version() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

// Snapshot returns a copy of the current entries.
func (s *Store) Snapshot() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(s.entries))
	copy(out, s.entries)
	return out
}

// Clear drops all retained entries (the session->file map is kept so future
// lines still resolve their filename).
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
	s.version++
}
